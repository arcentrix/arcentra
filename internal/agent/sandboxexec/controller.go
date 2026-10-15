// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandboxexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/arcentrix/arcentra/pkg/sandbox"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/google/uuid"
)

// DurableBackend 提供预分配身份及逐资源所有权验证，不允许广泛命名空间清理
// SealOwned 只有在全部容器进程已经停止并验证后才能成功返回
type DurableBackend interface {
	Backend
	Namespace() string
	PrepareResource(context.Context, string, string) (sandbox.ResourceRef, error)
	InspectOwned(context.Context, sandbox.ResourceRef) (sandbox.ResourceState, error)
	SealOwned(context.Context, sandbox.ResourceRef) error
	RemoveOwned(context.Context, sandbox.ResourceRef) error
}

// ControllerOptions 定义稳定可信绑定，两个根目录必须预先创建且为当前用户私有
// ControllerID 必须是实际控制端信任绑定的非秘密稳定摘要，不能使用访问令牌
type ControllerOptions struct {
	JournalRoot   string
	WorkspaceRoot string
	ControllerID  string
	AgentID       string
	MaxRecords    int
}

// Recovery 表示中断或完成清理后仍待确认的执行身份，不表示作业执行成功
// CleanupComplete 仅证明运行时和工作目录已删除，不能用于推断业务执行结果
type Recovery struct {
	PipelineRunID         string
	JobRunID              string
	ExecutionClaimID      string
	RuntimeBindingDigest  string
	CleanupComplete       bool
	Generation            *taskpayload.JobGeneration
	CleanupReceipt        *taskpayload.JobGenerationCleanupReceipt
	TransitionIntent      *taskpayload.JobGenerationTransition
	SuccessorAdmission    *taskpayload.JobGeneration
	GenerationObservation *taskpayload.JobGeneration
}

// Controller 独占持久日志，在接受作业前恢复已记录的准确资源
// 私有目录所有者属于可信边界；本地管理员或同 UID 的恶意程序不在保护范围内
type Controller struct {
	mu                    sync.Mutex
	opts                  ControllerOptions
	backend               DurableBackend
	journal               *os.Root
	journalDir            *os.File
	lock                  *os.File
	journalInfo           os.FileInfo
	workspaceRoot         *os.Root
	workspaceDir          *os.File
	workspaceInfo         os.FileInfo
	runtimeIOIdentity     string
	records               map[string]journalRecord
	admissions            map[string]generationAdmission
	stepAttempts          map[string]stepAttemptJournal
	sessions              map[string]*Session
	runtimeBlocked        map[string]error
	ready                 bool
	reconciliationPending bool
	closed                bool
	draining              bool
	fatal                 error
	hook                  func(string) error
}

// OpenController 验证全部日志和资源身份，清理中断资源后才开放恢复确认入口
// 任何损坏、所有权冲突、配额或清理错误保留证据并拒绝就绪，不重跑中断作业
func OpenController(ctx context.Context, backend DurableBackend, opts ControllerOptions) (*Controller, error) {
	return openController(ctx, backend, opts, nil)
}

func openController(ctx context.Context, backend DurableBackend, opts ControllerOptions, hook func(string) error) (_ *Controller, resultErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if backend == nil ||
		backend.Namespace() == "" ||
		len(backend.Namespace()) > 128 ||
		strings.ContainsAny(backend.Namespace(), "\x00\r\n/\\") {
		return nil, fmt.Errorf("durable sandbox backend and bounded namespace are required")
	}
	if !jobIDPattern.MatchString(opts.ControllerID) || !jobIDPattern.MatchString(opts.AgentID) {
		return nil, fmt.Errorf("stable non-secret controller and Agent identities are required")
	}
	if opts.MaxRecords == 0 {
		opts.MaxRecords = 1024
	}
	if opts.MaxRecords < 1 || opts.MaxRecords > 4096 {
		return nil, fmt.Errorf("sandbox journal quota must be between 1 and 4096 records")
	}
	if pathsOverlap(opts.JournalRoot, opts.WorkspaceRoot) {
		return nil, fmt.Errorf("sandbox journal must be separate from the workspace root")
	}
	c := &Controller{opts: opts, backend: backend, records: make(map[string]journalRecord), admissions: make(map[string]generationAdmission), stepAttempts: make(map[string]stepAttemptJournal), sessions: make(map[string]*Session), runtimeBlocked: make(map[string]error), hook: hook}
	defer func() {
		if resultErr != nil {
			_ = c.closeHandles()
		}
	}()
	var err error
	c.runtimeIOIdentity, err = backendRuntimeIOIdentity(backend)
	if err != nil {
		return nil, err
	}
	c.journal, err = openJournalDirectory(ctx, opts.JournalRoot)
	if err != nil {
		return nil, err
	}
	c.workspaceRoot, err = openJournalDirectory(ctx, opts.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	c.journalInfo, err = c.journal.Stat(".")
	if err != nil {
		return nil, err
	}
	c.workspaceInfo, err = c.workspaceRoot.Stat(".")
	if err != nil {
		return nil, err
	}
	c.journalDir, err = c.journal.Open(".")
	if err != nil {
		return nil, err
	}
	c.workspaceDir, err = c.workspaceRoot.Open(".")
	if err != nil {
		return nil, err
	}
	c.lock, err = c.openJournalFile(ctx, journalLockFile, true)
	if err != nil {
		return nil, err
	}
	if err := journalLock(c.lock); err != nil {
		return nil, err
	}
	if err := c.verifyRoots(ctx); err != nil {
		return nil, err
	}
	if err := c.lock.Sync(); err != nil {
		return nil, err
	}
	if err := c.syncJournal(ctx); err != nil {
		return nil, err
	}
	if err := c.load(ctx); err != nil {
		return nil, err
	}
	if err := c.reconcile(ctx); err != nil {
		return nil, err
	}
	c.ready = true
	c.reconciliationPending = c.journalCount() > 0
	return c, nil
}

func pathsOverlap(first, second string) bool {
	a, b := filepath.Clean(first), filepath.Clean(second)
	return a == b || strings.HasPrefix(a, b+string(os.PathSeparator)) || strings.HasPrefix(b, a+string(os.PathSeparator))
}

func (c *Controller) load(ctx context.Context) error {
	dir, err := c.journal.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(c.opts.MaxRecords + 3)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(entries) > c.opts.MaxRecords+2 {
		return fmt.Errorf("sandbox journal record quota exceeded")
	}
	names := make([]string, 0, len(entries))
	admissionNames := make([]string, 0, len(entries))
	stepNames := make([]string, 0, len(entries))
	hasBinding := false
	for _, entry := range entries {
		name := entry.Name()
		info, err := c.journal.Lstat(name)
		if err != nil {
			return err
		}
		if err := privateJournalNode(info, false); err != nil {
			return err
		}
		switch name {
		case journalLockFile:
			if info.Size() != 0 {
				return fmt.Errorf("sandbox journal lock contains unexpected data")
			}
		case journalBindingFile:
			hasBinding = true
		default:
			if strings.HasPrefix(name, "step-") {
				id, parseErr := uuid.Parse(strings.TrimSuffix(strings.TrimPrefix(name, "step-"), ".json"))
				if parseErr != nil || name != "step-"+id.String()+".json" {
					return fmt.Errorf("invalid step attempt journal filename")
				}
				stepNames = append(stepNames, name)
				continue
			}
			if strings.HasPrefix(name, "admission-") {
				id, err := uuid.Parse(strings.TrimSuffix(strings.TrimPrefix(name, "admission-"), ".json"))
				if err != nil || name != "admission-"+id.String()+".json" {
					return fmt.Errorf("invalid admission journal filename")
				}
				admissionNames = append(admissionNames, name)
				continue
			}
			id, err := uuid.Parse(strings.TrimSuffix(name, ".json"))
			if err != nil || name != id.String()+".json" {
				return fmt.Errorf("sandbox journal has unknown or interrupted files; operator review required")
			}
			names = append(names, name)
		}
	}
	if !hasBinding {
		if len(names)+len(admissionNames)+len(stepNames) != 0 {
			return fmt.Errorf("sandbox journal owner binding is missing")
		}
		return c.writeJournal(ctx, journalBindingFile, nil)
	}
	binding, err := c.readJournal(ctx, journalBindingFile)
	if err != nil {
		return err
	}
	if binding != nil {
		return fmt.Errorf("sandbox journal owner binding contains a session")
	}
	sort.Strings(admissionNames)
	for _, name := range admissionNames {
		admission, err := c.readAdmission(ctx, name)
		if err != nil {
			return err
		}
		if name != admissionFilename(admission.Request) {
			return fmt.Errorf("admission journal identity mismatch")
		}
		c.admissions[name] = *admission
	}
	sort.Strings(names)
	for _, name := range names {
		record, err := c.readJournal(ctx, name)
		if err != nil {
			return err
		}
		if err := c.validateRecord(record, name); err != nil {
			return err
		}
		c.records[record.ID] = *record
	}
	for _, name := range stepNames {
		a, err := c.readStepAttempt(ctx, name)
		if err != nil {
			return err
		}
		c.stepAttempts[name] = *a
	}
	if err := c.validateStepAttemptHistory(); err != nil {
		return err
	}
	return c.validateGenerationHistory()
}

func (c *Controller) reconcile(ctx context.Context) (resultErr error) {
	ids := make([]string, 0, len(c.records))
	for id := range c.records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	// 全量预检在第一次破坏性操作之前完成，损坏记录不会触发部分猜测性清理
	var workspaceErrors []error
	var quiesce []sandbox.ResourceRef
	for _, id := range ids {
		record := c.records[id]
		if record.Phase == phaseIdentityConflict {
			return fmt.Errorf("sandbox runtime identity conflict requires operator review")
		}
		if err := c.verifyRoots(ctx); err != nil {
			return err
		}
		missingAllowed := record.Phase == phaseIntent || record.Phase == phaseRemoving || record.Phase == phaseClosed || record.Phase == phaseRecovered
		workspace, workspaceErr := c.verifyRecordWorkspace(record, missingAllowed)
		if workspaceErr != nil {
			workspaceErrors = append(workspaceErrors, workspaceErr)
		}
		state, err := c.backend.InspectOwned(ctx, record.Resource)
		if err != nil {
			return fmt.Errorf("inspect recorded sandbox ownership: %w", err)
		}
		if state.ContainerExists {
			quiesce = append(quiesce, record.Resource)
		}
		if record.Phase == phaseClosed || record.Phase == phaseRecovered || record.Phase == phaseIntent {
			if workspace != nil || state.ContainerExists || state.SnapshotExists || state.TaskExists {
				workspaceErrors = append(workspaceErrors, fmt.Errorf("terminal or preallocation sandbox record has unexpected resources"))
			}
		}
	}
	// 完成所有权预检后，持久化或删除失败也不能遗留已知所有权的运行进程
	defer func() {
		if resultErr == nil {
			return
		}
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		for _, ref := range quiesce {
			state, err := c.backend.InspectOwned(stopCtx, ref)
			if err == nil && state.ContainerExists {
				err = c.backend.SealOwned(stopCtx, ref)
			}
			resultErr = errors.Join(resultErr, err)
		}
	}()
	if len(workspaceErrors) != 0 {
		return errors.Join(workspaceErrors...)
	}
	for _, id := range ids {
		record := c.records[id]
		if record.Phase == phaseClosed || record.Phase == phaseRecovered {
			continue
		}
		if record.Phase == phaseIntent {
			record.Phase = phaseRecovered
			if err := c.persist(ctx, record); err != nil {
				return err
			}
			continue
		}
		record.Phase = phaseRemoving
		if err := c.persist(ctx, record); err != nil {
			return err
		}
		if err := c.backend.RemoveOwned(ctx, record.Resource); err != nil {
			return fmt.Errorf("recover recorded sandbox: %w", err)
		}
		if _, err := c.verifyRecordWorkspace(record, true); err != nil {
			return err
		}
		if err := c.workspaceRoot.RemoveAll(record.Directory); err != nil {
			return err
		}
		if err := c.workspaceDir.Sync(); err != nil {
			return err
		}
		if err := c.verifyCleanupAbsent(ctx, record); err != nil {
			return err
		}
		record.Phase = phaseRecovered
		if record.Generation != nil && record.Inode != 0 {
			receipt := c.generationCleanupReceipt(record)
			record.CleanupReceipt = &receipt
		}
		if err := c.persist(ctx, record); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.closed || !c.ready {
		return fmt.Errorf("sandbox controller is not ready")
	}
	if c.fatal != nil {
		return fmt.Errorf("sandbox controller requires operator recovery: %w", c.fatal)
	}
	if err := c.verifyRoots(ctx); err != nil {
		return c.poison(err)
	}
	record, err := c.readJournal(ctx, journalBindingFile)
	if err != nil {
		return c.poison(err)
	}
	if record != nil {
		return c.poison(fmt.Errorf("sandbox journal owner binding changed"))
	}
	return nil
}

func (c *Controller) poison(err error) error { c.fatal = errors.Join(c.fatal, err); return err }

func (c *Controller) persist(ctx context.Context, record journalRecord) error {
	previous, exists := c.records[record.ID]
	if !exists {
		if _, err := c.journal.Lstat(record.ID + ".json"); !errors.Is(err, os.ErrNotExist) {
			return c.poison(fmt.Errorf("sandbox journal allocation collided with an existing path"))
		}
	}
	if exists {
		disk, err := c.readJournal(ctx, record.ID+".json")
		if err != nil {
			return c.poison(err)
		}
		if disk == nil || !reflect.DeepEqual(*disk, previous) {
			return c.poison(fmt.Errorf("sandbox journal session changed unexpectedly"))
		}
	}
	if err := c.writeJournal(ctx, record.ID+".json", &record); err != nil {
		return c.poison(err)
	}
	c.records[record.ID] = record
	return nil
}

// Allocate 持久化完整资源意图并分配工作目录，不创建或启动任何容器
// 调用方在可信输入暂存成功后调用 Start，输入凭据不会写入日志
func (c *Controller) Allocate(ctx context.Context, opts Options) (*Session, error) {
	return c.allocate(ctx, opts, nil, false)
}

func (c *Controller) allocate(ctx context.Context, opts Options, generation *taskpayload.JobGeneration, stepRetry bool) (*Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return nil, err
	}
	if c.draining || c.reconciliationPending || len(c.runtimeBlocked) != 0 || (c.pendingGenerationHandoff() || c.pendingStepAttemptHandoff()) {
		return nil, fmt.Errorf("sandbox startup has unacknowledged execution outcomes")
	}
	if opts.WorkspaceRoot != "" && opts.WorkspaceRoot != c.opts.WorkspaceRoot {
		return nil, fmt.Errorf("session cannot override the controller workspace root")
	}
	if !jobIDPattern.MatchString(opts.JobRunID) ||
		!jobIDPattern.MatchString(opts.ExecutionClaimID) ||
		opts.PipelineRunID != "" && !jobIDPattern.MatchString(opts.PipelineRunID) ||
		!validRuntimeBinding(opts.RuntimeBindingDigest) {
		return nil, fmt.Errorf("valid job, pipeline, execution claim and runtime binding identities are required")
	}
	if strings.TrimSpace(opts.Image) == "" || opts.Resources == nil || opts.Resources.CPU == "" || opts.Resources.Memory == "" {
		return nil, fmt.Errorf("operator-approved image and explicit CPU and memory limits are required")
	}
	user := opts.User
	if err := validateDurableUser(user); err != nil {
		return nil, err
	}
	if err := c.validateGenerationAllocation(ctx, opts, generation); err != nil {
		return nil, err
	}
	needed := 1
	if stepRetry {
		if opts.StepAttemptSlots < 1 || opts.StepAttemptSlots > c.opts.MaxRecords {
			return nil, fmt.Errorf("failed-step plan requires a bounded reserved attempt count")
		}
		needed += opts.StepAttemptSlots
	}
	if c.reservedJournalCount()+needed > c.opts.MaxRecords {
		return nil, fmt.Errorf("sandbox journal record quota exceeded")
	}
	id := uuid.NewString()
	if generation != nil {
		id = generation.RuntimeClaimID
	}
	record := journalRecord{
		ID: id, PipelineRunID: opts.PipelineRunID, JobRunID: opts.JobRunID, ExecutionClaimID: opts.ExecutionClaimID,
		RuntimeBindingDigest: opts.RuntimeBindingDigest, Directory: "job-" + id, Phase: phaseIntent, Generation: generation,
	}
	if stepRetry {
		record.StepSessionNonce = uuid.NewString()
		record.ReservedStepAttempts = opts.StepAttemptSlots
	}
	ref, err := c.backend.PrepareResource(ctx, "arcentra-"+id, "pending-owner-resolution")
	if err != nil {
		return nil, err
	}
	if ref.ID != "arcentra-"+id || ref.Namespace != c.backend.Namespace() || ref.Snapshotter == "" {
		return nil, fmt.Errorf("runtime returned an inconsistent prepared resource identity")
	}
	record.Resource = ref
	record.Resource.Owner = c.resourceOwner(record)
	if err := c.validateRecord(&record, id+".json"); err != nil {
		return nil, err
	}
	if generation != nil {
		state, err := c.backend.InspectOwned(ctx, record.Resource)
		if err != nil {
			return nil, err
		}
		if state.TaskExists || state.ContainerExists || state.SnapshotExists {
			return nil, fmt.Errorf("new generation resource identity is already in use")
		}
	}
	if err := c.persist(ctx, record); err != nil {
		return nil, err
	}
	if err := c.workspaceRoot.Mkdir(record.Directory, 0o700); err != nil {
		return nil, c.poison(err)
	}
	info, err := c.workspaceRoot.Lstat(record.Directory)
	if err != nil {
		return nil, c.poison(err)
	}
	record.Device, record.Inode, err = workspaceFileIdentity(info)
	if err != nil {
		return nil, c.poison(err)
	}
	if err := c.workspaceDir.Sync(); err != nil {
		return nil, c.poison(err)
	}
	record.Phase = phaseAllocated
	if err := c.persist(ctx, record); err != nil {
		return nil, err
	}
	resources := *opts.Resources
	workspace := filepath.Join(c.opts.WorkspaceRoot, record.Directory)
	ref = record.Resource
	s := &Session{
		backend: c.backend, controller: c, recordID: id, root: c.workspaceRoot, rootPath: c.opts.WorkspaceRoot,
		directory: record.Directory, workspace: workspace, workspaceInfo: info, containerID: ref.ID, user: user, resourceRef: ref,
		stepRetry: stepRetry, gate: make(chan struct{}, 1), createOptions: &sandbox.CreateOptions{
			Resource: &ref, Image: opts.Image, User: user,
			Command:    []string{"sh", "-ec", "trap 'exit 0' TERM INT; while :; do sleep 1; done"},
			WorkingDir: "/", NetworkMode: "none", Resources: &resources,
			Mounts: []sandbox.Mount{{Source: workspace, Target: containerWorkspace, Type: "bind"}},
			Labels: map[string]string{
				"io.arcentra.controller": c.opts.ControllerID, "io.arcentra.agent": c.opts.AgentID, "io.arcentra.job-run": opts.JobRunID,
				"io.arcentra.execution-claim": opts.ExecutionClaimID, "io.arcentra.workspace": record.Directory,
			},
		}}
	c.sessions[id] = s
	return s, nil
}

// Ready 只有运行时清理和中断业务结果确认全部完成后才报告可以接受新作业
func (c *Controller) Ready() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ready && !c.closed && !c.draining && c.fatal == nil && !c.reconciliationPending && len(c.runtimeBlocked) == 0 && !c.pendingGenerationHandoff() && !c.pendingStepAttemptHandoff()
}

// HasJob 判断作业是否仍有执行记录或等待业务结果确认的墓碑
func (c *Controller) HasJob(jobRunID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, admission := range c.admissions {
		if admission.Request.Identity.JobRunID == jobRunID {
			return true
		}
	}
	for _, record := range c.records {
		if record.JobRunID == jobRunID {
			return true
		}
	}
	return false
}

// Recoveries 返回尚未确认终态的清理完成记录，不推断执行结果
func (c *Controller) Recoveries() []Recovery {
	c.mu.Lock()
	defer c.mu.Unlock()
	records := make([]Recovery, 0)
	for _, record := range c.records {
		if record.Phase == phaseClosed || record.Phase == phaseRecovered {
			records = append(records, Recovery{
				PipelineRunID: record.PipelineRunID, JobRunID: record.JobRunID, ExecutionClaimID: record.ExecutionClaimID,
				RuntimeBindingDigest: record.RuntimeBindingDigest, CleanupComplete: true,
				Generation: copyGeneration(record.Generation), CleanupReceipt: copyCleanupReceipt(record.CleanupReceipt),
				TransitionIntent: copyGenerationTransition(record.TransitionIntent), SuccessorAdmission: copyGeneration(record.SuccessorAdmission), GenerationObservation: copyGeneration(record.GenerationObservation),
			})
		}
	}
	for _, admission := range c.admissions {
		hasRecord := false
		for _, record := range c.records {
			if record.ExecutionClaimID == admission.Request.Identity.ClaimID {
				hasRecord = true
				break
			}
		}
		if !hasRecord {
			identity := admission.Request.Identity
			records = append(records, Recovery{PipelineRunID: identity.PipelineRunID, JobRunID: identity.JobRunID, ExecutionClaimID: identity.ClaimID,
				RuntimeBindingDigest: identity.RuntimeBindingDigest, CleanupComplete: true, Generation: copyGeneration(admission.Generation), TransitionIntent: copyGenerationTransition(&admission.Request), GenerationObservation: copyGeneration(admission.Observation)})
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].JobRunID != records[j].JobRunID {
			return records[i].JobRunID < records[j].JobRunID
		}
		return records[i].Generation != nil && records[j].Generation != nil && records[i].Generation.Number < records[j].Generation.Number
	})
	return records
}

func (c *Controller) removeJournalRecord(ctx context.Context, id string) error {
	if err := c.verifyRoots(ctx); err != nil {
		return err
	}
	if err := c.fault(ctx, "record.remove"); err != nil {
		return err
	}
	if err := c.journal.Remove(id + ".json"); err != nil {
		return err
	}
	if err := c.syncJournal(ctx); err != nil {
		return err
	}
	delete(c.records, id)
	return nil
}

// AcknowledgeCompletion 仅在调用方已持久保存业务终态证据后删除对应清理墓碑
// 该操作不生成成功或失败结果，也不能用于授权再次执行同一作业
func (c *Controller) AcknowledgeCompletion(ctx context.Context, executionClaimID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return err
	}
	var ids []string
	for id, record := range c.records {
		if record.ExecutionClaimID != executionClaimID {
			continue
		}
		if record.Phase != phaseClosed && record.Phase != phaseRecovered {
			return fmt.Errorf("sandbox resources have not completed cleanup")
		}
		disk, err := c.readJournal(ctx, id+".json")
		if err != nil || disk == nil || !reflect.DeepEqual(*disk, record) {
			return c.poison(fmt.Errorf("sandbox completion record changed"))
		}
		if err := c.verifyCleanupAbsent(ctx, record); err != nil {
			return c.poison(err)
		}
		ids = append(ids, id)
	}
	var admissionIDs []string
	for name, admission := range c.admissions {
		if admission.Request.Identity.ClaimID != executionClaimID {
			continue
		}
		disk, err := c.readAdmission(ctx, name)
		if err != nil || disk == nil || !reflect.DeepEqual(*disk, admission) {
			return c.poison(fmt.Errorf("sandbox admission completion record changed"))
		}
		admissionIDs = append(admissionIDs, name)
	}
	if len(ids)+len(admissionIDs) == 0 {
		return fmt.Errorf("sandbox execution claim is not recorded")
	}
	sort.Strings(ids)
	sort.Strings(admissionIDs)
	for _, name := range admissionIDs {
		admission := c.admissions[name]
		if !admission.FinalAcknowledged {
			admission.FinalAcknowledged = true
			if err := c.persistAdmission(ctx, admission); err != nil {
				return err
			}
		}
	}
	// 最终 ACK 先落盘，再删除任意一代；部分删除崩溃仍保留明确终态来源
	for _, id := range ids {
		record := c.records[id]
		if record.Generation != nil && !record.FinalAcknowledged {
			record.FinalAcknowledged = true
			if err := c.persist(ctx, record); err != nil {
				return err
			}
		}
	}
	if err := c.acknowledgeStepAttempts(ctx, executionClaimID); err != nil {
		return c.poison(err)
	}
	for _, id := range ids {
		if err := c.removeJournalRecord(ctx, id); err != nil {
			return c.poison(err)
		}
	}
	for _, name := range admissionIDs {
		if err := c.removeAdmission(ctx, name); err != nil {
			return c.poison(err)
		}
	}
	if c.journalCount() == 0 {
		c.reconciliationPending = false
	}
	return nil
}

// Close 只在全部会话已清理后释放独占日志锁，不丢弃失败记录
func (c *Controller) Close(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(c.sessions) != 0 {
		return fmt.Errorf("sandbox controller has live sessions; close each session before releasing ownership")
	}
	c.closed = true
	c.ready = false
	return c.closeHandles()
}

func (c *Controller) closeHandles() error {
	var errs []error
	for _, file := range []*os.File{c.lock, c.journalDir, c.workspaceDir} {
		if file != nil {
			errs = append(errs, file.Close())
		}
	}
	for _, root := range []*os.Root{c.journal, c.workspaceRoot} {
		if root != nil {
			errs = append(errs, root.Close())
		}
	}
	return errors.Join(errs...)
}

func validateDurableUser(user string) error {
	parts := strings.Split(user, ":")
	if len(parts) != 2 {
		return fmt.Errorf("durable sandbox requires explicit nonzero numeric UID:GID")
	}
	for _, part := range parts {
		value, err := strconv.ParseUint(part, 10, 32)
		if err != nil || value == 0 || strconv.FormatUint(value, 10) != part {
			return fmt.Errorf("durable sandbox requires explicit nonzero numeric UID:GID")
		}
	}
	return nil
}

func backendRuntimeIOIdentity(backend DurableBackend) (string, error) {
	if provider, ok := backend.(interface{ RuntimeIOIdentity() (string, error) }); ok {
		identity, err := provider.RuntimeIOIdentity()
		if err != nil {
			return "", err
		}
		if identity != "" && (len(identity) != 64 || strings.Trim(identity, "0123456789abcdef") != "") {
			return "", fmt.Errorf("invalid runtime IO identity")
		}
		return identity, nil
	}
	return "", nil
}
