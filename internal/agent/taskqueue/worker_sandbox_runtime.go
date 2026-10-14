package taskqueue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/agent/sandboxexec"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/sandbox"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
)

type durableOfflineRuntime struct {
	stopping   atomic.Bool
	backend    *sandbox.Containerd
	controller *sandboxexec.Controller
	user       string
}

func newOfflineRuntime(ctx context.Context, conf *config.AgentConfig, client *agentgrpc.ClientWrapper) (*durableOfflineRuntime, error) {
	if conf == nil || !conf.Agent.Sandbox.Enable {
		return nil, nil
	}
	if client == nil {
		return nil, fmt.Errorf("offline runtime requires authenticated controller transport")
	}
	binding, err := client.ControllerIdentityBinding()
	if err != nil {
		return nil, fmt.Errorf("offline runtime requires verified controller TLS")
	}
	cfg := conf.Agent.Sandbox.Containerd
	if err := cfg.Policy.ValidateProduction(); err != nil {
		return nil, err
	}
	if cfg.Namespace == "" || cfg.JournalDir == "" || cfg.User == "" || cfg.FIFODir == "" {
		return nil, fmt.Errorf("offline runtime requires explicit namespace, private journal and FIFO directories, and numeric user")
	}
	if err := validateOfflineIORoot(cfg.FIFODir, conf.Agent.WorkspaceDir, cfg.JournalDir); err != nil {
		return nil, err
	}
	if cfg.Network != "none" {
		return nil, fmt.Errorf("offline runtime network must explicitly be none")
	}
	if cfg.Image != "" || cfg.Resources.CPU != "" || cfg.Resources.Memory != "" {
		return nil, fmt.Errorf("offline runtime image and limits must come from the bound job and approved policy, not legacy defaults")
	}
	parts := strings.Split(cfg.User, ":")
	if len(parts) != 2 {
		return nil, fmt.Errorf("offline runtime requires explicit non-root UID:GID matching the Agent")
	}
	uid, e1 := strconv.ParseUint(parts[0], 10, 32)
	gid, e2 := strconv.ParseUint(parts[1], 10, 32)
	if e1 != nil || e2 != nil || uid == 0 || gid == 0 || int(uid) != os.Geteuid() || int(gid) != os.Getegid() {
		return nil, fmt.Errorf("offline runtime UID:GID must match the non-root Agent; directory ownership is never changed automatically")
	}
	backend, err := sandbox.NewContainerdSandbox(&sandbox.ContainerdConfig{UnixSocket: cfg.UnixSocket, Namespace: cfg.Namespace, RuntimeRoot: cfg.RuntimeRoot, FIFODir: cfg.FIFODir, NetworkMode: "none"}, log.Logger{})
	if err != nil {
		return nil, fmt.Errorf("offline runtime connection failed")
	}
	controller, err := sandboxexec.OpenController(ctx, backend, sandboxexec.ControllerOptions{JournalRoot: cfg.JournalDir, WorkspaceRoot: conf.Agent.WorkspaceDir, ControllerID: binding, AgentID: conf.Agent.ID, MaxRecords: 1000})
	if err != nil {
		_ = backend.Close()
		return nil, fmt.Errorf("offline runtime ownership recovery failed; preserve journal and resources: %w", err)
	}
	result := &durableOfflineRuntime{backend: backend, controller: controller, user: cfg.User}
	if err := confirmOfflineRecoveries(ctx, controller, conf, client); err != nil {
		_ = result.Close(ctx)
		return nil, err
	}
	if !controller.Ready() {
		_ = result.Close(ctx)
		return nil, fmt.Errorf("offline runtime recovery remains unresolved")
	}
	return result, nil
}

// confirmOfflineRecoveries 只在精确资源清理后查询权威终态，不伪造回执或重新执行
type offlineRecoveryController interface {
	Recoveries() []sandboxexec.Recovery
	AcknowledgeCompletion(context.Context, string) error
}

func confirmOfflineRecoveries(ctx context.Context, controller offlineRecoveryController, conf *config.AgentConfig, client *agentgrpc.ClientWrapper) error {
	for _, record := range controller.Recoveries() {
		if !record.CleanupComplete {
			return fmt.Errorf("offline runtime cleanup remains unconfirmed")
		}
		callCtx, cancel := client.WithTimeoutAndAuth(ctx)
		response, err := client.AgentClient.InspectJobExecution(callCtx, &agentv1.InspectJobExecutionRequest{AgentId: conf.Agent.ID, JobRunId: record.JobRunID, OriginalClaimId: record.ExecutionClaimID, RuntimeBindingDigest: record.RuntimeBindingDigest})
		cancel()
		if err != nil || response == nil || !response.Terminal || response.Status < 4 || response.Status > 7 {
			return fmt.Errorf("offline job recovery requires verified terminal state; preserve journal and perform explicit offline reconciliation if execution was interrupted")
		}
		if err := controller.AcknowledgeCompletion(ctx, record.ExecutionClaimID); err != nil {
			return err
		}
	}
	return nil
}
func (r *durableOfflineRuntime) Allocate(ctx context.Context, payload *taskpayload.JobRunTaskPayload, claim string) (offlineSession, error) {
	if !r.Ready() {
		return nil, fmt.Errorf("offline runtime is not ready")
	}
	session, err := r.controller.Allocate(ctx, sandboxexec.Options{JobRunID: payload.JobRunID, PipelineRunID: payload.PipelineRunID, ExecutionClaimID: claim, RuntimeBindingDigest: payload.Runtime.BindingDigest, Image: payload.Runtime.Image, Resources: &sandbox.Resources{CPU: payload.Runtime.Resources.CPU, Memory: payload.Runtime.Resources.Memory}, User: r.user})
	if session == nil {
		return nil, err
	}
	return session, err
}
func (r *durableOfflineRuntime) HasJob(job string) bool { return r.controller.HasJob(job) }
func (r *durableOfflineRuntime) Ready() bool {
	return r != nil && !r.stopping.Load() && r.controller.Ready()
}
func (r *durableOfflineRuntime) Acknowledge(ctx context.Context, job, claim string) error {
	if !r.controller.HasJob(job) {
		return nil
	}
	for _, record := range r.controller.Recoveries() {
		if record.JobRunID == job && record.ExecutionClaimID == claim && record.CleanupComplete {
			return r.controller.AcknowledgeCompletion(ctx, claim)
		}
	}
	return fmt.Errorf("offline runtime completion identity or cleanup was not confirmed")
}
func (r *durableOfflineRuntime) Close(ctx context.Context) error {
	r.stopping.Store(true)
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	return errors.Join(r.controller.Close(closeCtx), r.backend.Close())
}

// OfflineRuntimeReady 返回已验证初始化、恢复和清理状态，不把配置启用当作运行能力
func (q *workerQueues) OfflineRuntimeReady() bool {
	return q != nil && q.offline != nil && q.offline.Ready()
}

// validateOfflineIORoot 保证客户端管道不暴露在工作空间或持久日志目录内
func validateOfflineIORoot(fifo, workspace, journal string) error {
	for _, dir := range []string{fifo, workspace, journal} {
		if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || dir == "/" {
			return fmt.Errorf("offline IO, workspace and journal roots must be explicit clean absolute private directories")
		}
	}
	for _, other := range []string{workspace, journal} {
		for _, pair := range [][2]string{{fifo, other}, {other, fifo}} {
			rel, err := filepath.Rel(pair[0], pair[1])
			if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
				return fmt.Errorf("offline FIFO root must not overlap workspace or journal roots")
			}
		}
	}
	return nil
}
