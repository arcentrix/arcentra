// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/namespaces"
	"github.com/google/uuid"
)

const maxLiveStepAttempts = 5

// LiveSessionIdentity 绑定仅限当前进程的随机会话代次与原作业执行声明
// SessionID 必须为新生成的标准 UUID，不得从持久记录恢复执行能力
type LiveSessionIdentity struct {
	SessionID        string
	ExecutionClaimID string
}

// StepIdentity 指定调用方已经持久准入的精确步骤与本地生效期限
// AdmissionDigest 仅绑定上层 CAS 结果，本包不独立证明控制面授权
type StepIdentity struct {
	StepRunID          string
	PreviousStepRunID  string
	StepIndex          int32
	Attempt            int32
	AdmissionDigest    string
	NotBeforeUnixMilli int64
	ExpiresUnixMilli   int64
}

// LiveSessionBackend 提供默认关闭的显式会话创建接口，不跳过调用方持久生命周期
type LiveSessionBackend interface {
	CreateLiveSession(context.Context, *CreateOptions, LiveSessionIdentity) (LiveSession, error)
}

// LiveSession 只允许当前进程的精确句柄执行已准入步骤，失败边界必须显式消费
// Create 与 Start 分离；重启后只有按 Resource 清理的能力，不能恢复该接口
type LiveSession interface {
	Identity() LiveSessionIdentity
	Resource() ResourceRef
	Start(context.Context) error
	ExecuteStep(context.Context, StepIdentity, []string, *ExecuteOptions) (*ExecuteResult, *FailedStepBoundary, error)
	ResumeStep(context.Context, *FailedStepBoundary, StepIdentity, []string, *ExecuteOptions) (*ExecuteResult, *FailedStepBoundary, error)
	CheckBoundary(context.Context, *FailedStepBoundary) error
}

// FailedStepBoundary 保存冻结的失败步骤证据，仅原会话持有的原指针可消费一次
// 结构体复制、零值和 JSON 均不能产生可用执行权限
type FailedStepBoundary struct {
	session       *liveSession
	identity      LiveSessionIdentity
	resource      ResourceRef
	failed        StepIdentity
	exitCode      int32
	commandDigest [32]byte
	consumed      bool
}

// MarshalJSON 拒绝把进程内边界误作持久执行凭据
func (FailedStepBoundary) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("live failed-step boundary cannot be serialized")
}

// UnmarshalJSON 拒绝从持久数据恢复进程内执行权限
func (*FailedStepBoundary) UnmarshalJSON([]byte) error {
	return fmt.Errorf("live failed-step boundary cannot be reconstructed")
}

// FailedStep 返回边界绑定的失败步骤身份副本，不授予恢复能力
func (b *FailedStepBoundary) FailedStep() StepIdentity {
	if b == nil {
		return StepIdentity{}
	}
	return b.failed
}

// ExitCode 返回已观察并完成回收的命令退出码
func (b *FailedStepBoundary) ExitCode() int32 {
	if b == nil {
		return -1
	}
	return b.exitCode
}

type liveSession struct {
	sandbox     *Containerd
	state       *containerState
	identity    LiveSessionIdentity
	resource    ResourceRef
	gate        chan struct{}
	pin         func(context.Context, uint32) (pinnedInit, error)
	proof       *taskQuiescence
	started     bool
	pending     *FailedStepBoundary
	previous    StepIdentity
	hasPrevious bool
	usedIDs     map[string]struct{}
}

type liveExecution struct {
	session       *liveSession
	step          StepIdentity
	boundary      *FailedStepBoundary
	resume        bool
	commandDigest [32]byte
	output        *FailedStepBoundary
}

var _ LiveSessionBackend = (*Containerd)(nil)
var _ LiveSession = (*liveSession)(nil)

// CreateLiveSession 仅创建已预分配资源，固定非派生 init，必须另行调用 Start
// 默认关闭；生产配置工厂不会打开该候选开关，也不构成真实运行时验收
func (s *Containerd) CreateLiveSession(ctx context.Context, opts *CreateOptions, identity LiveSessionIdentity) (LiveSession, error) {
	return s.createLiveSession(ctx, opts, identity, pinInitProcess)
}

func (s *Containerd) createLiveSession(ctx context.Context, opts *CreateOptions, identity LiveSessionIdentity, pin func(context.Context, uint32) (pinnedInit, error)) (LiveSession, error) {
	if !s.config.EnableLiveStepRetry {
		return nil, fmt.Errorf("live failed-step sessions are disabled")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opts == nil || opts.Resource == nil {
		return nil, fmt.Errorf("live session requires a preallocated durable resource")
	}
	parsed, err := uuid.Parse(identity.SessionID)
	if err != nil || parsed.String() != identity.SessionID || parsed.Version() != 4 || !boundedLiveID(identity.ExecutionClaimID) {
		return nil, fmt.Errorf("live session requires a fresh UUID nonce and exact execution claim")
	}
	user, err := numericUser(opts.User)
	if err != nil || user.UID == 0 || user.GID == 0 {
		return nil, fmt.Errorf("live session requires an explicit nonzero numeric UID and GID")
	}
	if pin == nil {
		return nil, fmt.Errorf("live session requires kernel init pin support")
	}
	copy := *opts
	ref := *opts.Resource
	copy.Resource = &ref
	copy.Command, copy.Args = slices.Clone(quiescenceInit), nil
	live := &liveSession{sandbox: s, identity: identity, resource: ref, gate: make(chan struct{}, 1), pin: pin, usedIDs: make(map[string]struct{})}
	_, state, err := s.createWithLiveSession(ctx, &copy, live)
	if err != nil {
		if state != nil {
			return live, err
		}
		return nil, err
	}
	return live, nil
}

// Identity 返回与当前随机会话代次绑定的身份副本
func (s *liveSession) Identity() LiveSessionIdentity { return s.identity }

// Resource 返回创建前已持久分配的精确资源身份副本
func (s *liveSession) Resource() ResourceRef { return s.resource }
func (s *liveSession) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			s.release()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *liveSession) release() { <-s.gate }
func (s *liveSession) fail(ctx context.Context, err error) error {
	if s.state == nil {
		return err
	}
	return errors.Join(err, s.sandbox.removeState(ctx, s.resource.ID, s.state))
}

// Start 单独启动并核验固定 init，仅允许调用一次
func (s *liveSession) Start(ctx context.Context) error {
	if err := s.acquire(ctx); err != nil {
		return s.fail(ctx, err)
	}
	defer s.release()
	if err := s.sandbox.startLiveState(ctx, s.state, s); err != nil {
		return s.fail(ctx, err)
	}
	return nil
}

// ExecuteStep 执行已准入的首个尝试，并在失败时返回冻结边界
func (s *liveSession) ExecuteStep(ctx context.Context, step StepIdentity, cmd []string, opts *ExecuteOptions) (*ExecuteResult, *FailedStepBoundary, error) {
	return s.run(ctx, &liveExecution{session: s, step: step}, cmd, opts)
}

// ResumeStep 消费同一失败步骤的单次边界并执行精确后继尝试
func (s *liveSession) ResumeStep(ctx context.Context, boundary *FailedStepBoundary, step StepIdentity, cmd []string, opts *ExecuteOptions) (*ExecuteResult, *FailedStepBoundary, error) {
	return s.run(ctx, &liveExecution{session: s, step: step, boundary: boundary, resume: true}, cmd, opts)
}
func (s *liveSession) run(ctx context.Context, admission *liveExecution, cmd []string, opts *ExecuteOptions) (*ExecuteResult, *FailedStepBoundary, error) {
	if err := s.acquire(ctx); err != nil {
		return nil, nil, s.fail(ctx, err)
	}
	defer s.release()
	if !s.started || s.proof == nil {
		return nil, nil, s.fail(ctx, fmt.Errorf("live session has not been started"))
	}
	// Snapshot caller-owned mutable command/options before binding and execution.
	cmd = slices.Clone(cmd)
	if opts != nil {
		copy := *opts
		copy.Env = maps.Clone(opts.Env)
		opts = &copy
	}
	if opts != nil && opts.Stdin != nil {
		return nil, nil, s.fail(ctx, fmt.Errorf("live steps do not accept replay-ambiguous stdin"))
	}
	admission.commandDigest = liveCommandDigest(cmd, opts)
	result, err := s.sandbox.execute(ctx, s.resource.ID, cmd, opts, s.proof, admission)
	if err != nil {
		return result, nil, s.fail(ctx, err)
	}
	return result, admission.output, nil
}

// All admission/boundary state below is accessed while holding state.gate.
func (s *liveSession) admitStep(a *liveExecution) error {
	if a == nil || a.session != s || !s.started || s.state.liveSession != s || s.state.ref != s.resource {
		return fmt.Errorf("live step requires its exact session and resource")
	}
	if err := validateLiveStep(a.step); err != nil {
		return err
	}
	if _, used := s.usedIDs[a.step.StepRunID]; used {
		return fmt.Errorf("step run identity was already used")
	}
	if a.resume {
		if err := s.matchBoundary(a.boundary); err != nil {
			return err
		}
		previous := a.boundary.failed
		if a.step.StepIndex != previous.StepIndex || a.step.Attempt != previous.Attempt+1 || a.step.PreviousStepRunID != previous.StepRunID || a.step.AdmissionDigest == previous.AdmissionDigest || a.commandDigest != a.boundary.commandDigest {
			return fmt.Errorf("resume does not match exact failed step successor and command")
		}
		// Consume before any Resume RPC, including uncertain acknowledgements.
		a.boundary.consumed = true
		s.pending = nil
	} else {
		if s.pending != nil || s.proof.paused || a.step.Attempt != 1 || a.step.PreviousStepRunID != "" || s.hasPrevious && a.step.StepIndex <= s.previous.StepIndex {
			return fmt.Errorf("new logical step requires successful prior boundary and first attempt")
		}
	}
	s.usedIDs[a.step.StepRunID] = struct{}{}
	return nil
}
func (s *liveSession) matchBoundary(b *FailedStepBoundary) error {
	if b == nil || b != s.pending || b.session != s || b.consumed || b.identity != s.identity || b.resource != s.resource || b.failed != s.previous || !s.proof.paused || s.proof.invalid {
		return fmt.Errorf("failed-step boundary is absent, replaced, consumed or invalid")
	}
	return nil
}
func (s *liveSession) finishStep(a *liveExecution, result *ExecuteResult) {
	s.previous, s.hasPrevious = a.step, true
	if result.ExitCode != 0 {
		b := &FailedStepBoundary{session: s, identity: s.identity, resource: s.resource, failed: a.step, exitCode: result.ExitCode, commandDigest: a.commandDigest}
		s.pending, a.output = b, b
	}
}

// CheckBoundary 重新核验原资源和冻结进程树，不解冻也不创建用户进程
func (s *liveSession) CheckBoundary(ctx context.Context, b *FailedStepBoundary) error {
	if err := s.acquire(ctx); err != nil {
		return s.fail(ctx, err)
	}
	defer s.release()
	err := s.checkBoundary(ctx, b)
	if err != nil {
		return s.fail(ctx, err)
	}
	return nil
}
func (s *liveSession) checkBoundary(ctx context.Context, b *FailedStepBoundary) error {
	ctx = namespaces.WithNamespace(ctx, s.sandbox.namespace)
	if err := s.state.acquire(ctx); err != nil {
		return err
	}
	defer s.state.release()
	if !s.started || s.proof == nil || s.state.removed || s.state.stopped || s.state.liveSession != s || s.state.ref != s.resource {
		return fmt.Errorf("live session is no longer available")
	}
	if err := s.matchBoundary(b); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.state.mu.Lock()
	if s.state.stopping {
		s.state.mu.Unlock()
		cancel()
		return fmt.Errorf("live session is stopping")
	}
	s.state.activeCancel = cancel
	s.state.mu.Unlock()
	defer func() { s.state.mu.Lock(); s.state.activeCancel = nil; s.state.mu.Unlock(); cancel() }()
	ctx = runCtx
	if err := s.proof.admit(ctx); err != nil {
		return err
	}
	return s.proof.verify(ctx, containerd.Paused, true)
}

func boundedLiveID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= 256 && !strings.ContainsAny(value, "\x00\r\n")
}
func validateLiveStep(step StepIdentity) error {
	if !boundedLiveID(step.StepRunID) || step.PreviousStepRunID != "" && !boundedLiveID(step.PreviousStepRunID) || step.StepIndex < 0 || step.Attempt < 1 || step.Attempt > maxLiveStepAttempts {
		return fmt.Errorf("invalid bounded live step identity")
	}
	if len(step.AdmissionDigest) != 67 || !strings.HasPrefix(step.AdmissionDigest, "v1:") || strings.ToLower(step.AdmissionDigest) != step.AdmissionDigest {
		return fmt.Errorf("live step requires exact admission digest")
	}
	if _, err := hex.DecodeString(step.AdmissionDigest[3:]); err != nil {
		return fmt.Errorf("invalid admission digest: %w", err)
	}
	if step.NotBeforeUnixMilli <= 0 || step.ExpiresUnixMilli <= step.NotBeforeUnixMilli {
		return fmt.Errorf("live step requires bounded admission validity")
	}
	return checkLiveStepTime(step)
}
func checkLiveStepTime(step StepIdentity) error {
	now := time.Now().UnixMilli()
	if now < step.NotBeforeUnixMilli || now >= step.ExpiresUnixMilli {
		return fmt.Errorf("live step admission is not currently effective")
	}
	return nil
}

// Hash explicit semantic execution inputs, never IO handles or a serialized proof.
// No environment variables are exempted; the current adapter has no attempt-ID injection.
func liveCommandDigest(cmd []string, opts *ExecuteOptions) [32]byte {
	h := sha256.New()
	put := func(value string) { hashLivePart(h, value) }
	put(fmt.Sprint(len(cmd)))
	for _, value := range cmd {
		put(value)
	}
	if opts == nil {
		opts = &ExecuteOptions{}
	}
	put(opts.WorkingDir)
	put(opts.User)
	put(fmt.Sprint(opts.TTY))
	put(fmt.Sprint(opts.Timeout))
	keys := slices.Sorted(maps.Keys(opts.Env))
	put(fmt.Sprint(len(keys)))
	for _, key := range keys {
		put(key)
		put(opts.Env[key])
	}
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result
}
func hashLivePart(h hash.Hash, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = h.Write(size[:])
	_, _ = h.Write([]byte(value))
}
