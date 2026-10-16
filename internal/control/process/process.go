// Copyright 2026 Arcentra Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package process

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/internal/shared/pipeline"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/admission"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/validation"
	"github.com/arcentrix/arcentra/internal/shared/storage"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/plugin"
	"github.com/arcentrix/arcentra/pkg/polling"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/protobuf/proto"
)

const defaultMaxConcurrentRuns = 10

// Process manages pipeline run orchestration. It bridges the trigger layer
// (HTTP/gRPC/Cron/Webhook) with the execution process (Executor DAG).
type Process struct {
	repos                *repo.Repositories
	pluginMgr            *plugin.Manager
	taskQueue            nova.TaskQueue
	storage              storage.IStorage
	logger               *log.Logger
	appConf              *config.AppConfig
	secretSvc            *service.SecretService
	deploymentService    *service.DeploymentService
	auditWriter          *AuditWriter
	logPublisher         executor.LogPublisher
	closeLogs            func()
	logInitErr           error
	offlineRuntimePolicy taskqueue.OfflineRuntimePolicy

	mu        sync.Mutex
	stopped   bool
	nextAgent atomic.Uint64
	runs      sync.Map      // runID -> *Coordinator
	sem       chan struct{} // concurrency limiter
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// NewProcess creates a pipeline process.
func NewProcess(
	repos *repo.Repositories,
	pluginMgr *plugin.Manager,
	taskQueue nova.TaskQueue,
	st storage.IStorage,
	logger *log.Logger,
	appConf *config.AppConfig,
	secretSvc *service.SecretService,
) *Process {
	ctx, cancel := context.WithCancel(context.Background())
	maxRuns := defaultMaxConcurrentRuns
	engine := &Process{
		repos:     repos,
		pluginMgr: pluginMgr,
		taskQueue: taskQueue,
		storage:   st,
		logger:    logger,
		appConf:   appConf,
		secretSvc: secretSvc,
		sem:       make(chan struct{}, maxRuns),
		ctx:       ctx,
		cancel:    cancel,
	}
	if appConf != nil {
		if err := appConf.Pipeline.OfflineRuntime.ValidateProduction(); err != nil {
			engine.logInitErr = err
			return engine
		}
		engine.offlineRuntimePolicy = appConf.Pipeline.OfflineRuntime.Clone()
	}
	engine.initLogPublisher()
	return engine
}

// SetAuditWriter injects the audit writer into the process. Called during
// bootstrap after DB initialization.
func (e *Process) SetAuditWriter(aw *AuditWriter) {
	e.auditWriter = aw
}

// Submit asynchronously starts a pipeline run. It returns immediately;
// the actual execution happens in a background goroutine.
func (e *Process) Submit(run *model.PipelineRun, parsedSpec *spec.Pipeline) error {
	return e.submit(context.Background(), run, parsedSpec)
}

// SubmitVerified 使用认证入口上下文提交人工审批运行，普通 Submit 不可替代
func (e *Process) SubmitVerified(ctx context.Context, run *model.PipelineRun, parsedSpec *spec.Pipeline) error {
	return e.submit(ctx, run, parsedSpec)
}

func (e *Process) submit(triggerCtx context.Context, run *model.PipelineRun, parsedSpec *spec.Pipeline) error {
	// 重复提交必须先于验证检查：已接收但等待并发名额的运行仍可能是 PENDING
	// 同一锁覆盖验证、拒绝持久化和协调器注册，避免拒绝覆盖已经接收的运行
	e.mu.Lock()
	defer e.mu.Unlock()
	if run != nil {
		if _, exists := e.runs.Load(run.RunID); exists {
			return fmt.Errorf("run %s is already submitted", run.RunID)
		}
	}
	if run == nil || parsedSpec == nil {
		return e.rejectSubmission(run, fmt.Errorf("run and spec must not be nil"))
	}

	isPolling, pollingErr := validatePollingSubmission(run, parsedSpec)
	if pollingErr != nil {
		return e.rejectSubmission(run, pollingErr)
	}
	if err := validation.NewSchemaValidatorWithRuntimePolicy(e.offlineRuntimePolicy).Validate(parsedSpec); err != nil {
		return e.rejectSubmission(run, err)
	}
	isDeployment, err := e.validateDeploymentRun(run, parsedSpec)
	if err != nil {
		return e.rejectSubmission(run, err)
	}
	isRetryApproval, err := e.validateRetryApprovalRun(run, parsedSpec)
	if err != nil {
		return e.rejectSubmission(run, err)
	}
	if isRetryApproval && (service.VerifiedPipelineActor(triggerCtx) == "" || service.VerifiedPipelineActor(triggerCtx) != run.TriggeredBy) {
		return e.rejectSubmission(run, fmt.Errorf("retry approval requires verified manual submission context"))
	}
	runCopy := *run
	run = &runCopy
	parsedSpec = proto.Clone(parsedSpec).(*spec.Pipeline)
	if _, _, err := pipeline.BuildDAG(parsedSpec.Jobs); err != nil {
		return e.rejectSubmission(run, fmt.Errorf("invalid pipeline DAG: %w", err))
	}
	if e.logInitErr != nil {
		return e.rejectSubmission(run, fmt.Errorf("initialize durable build logs: %w", e.logInitErr))
	}
	if e.stopped {
		return e.rejectSubmission(run, fmt.Errorf("process is shutting down"))
	}
	automatic := run.TriggerType == int(pipelinev1.TriggerType_TRIGGER_TYPE_CRON) || run.TriggerType == int(pipelinev1.TriggerType_TRIGGER_TYPE_EVENT)
	if !automatic {
		// 调用方不得将持久化自动运行改称人工运行，以绕过自动入口的认领与终态保护
		// 此读取不更改人工运行状态，也不替代后续审批上下文验证
		if e.repos == nil || e.repos.Pipeline == nil {
			return fmt.Errorf("durable pipeline identity is unavailable")
		}
		checkCtx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
		defer cancel()
		stored, err := e.repos.Pipeline.GetRun(checkCtx, run.RunID)
		if err != nil {
			return fmt.Errorf("read pipeline admission identity: %w", err)
		}
		if stored == nil || stored.RunID != run.RunID || stored.PipelineID != run.PipelineID || stored.TriggerType != run.TriggerType {
			return fmt.Errorf("pipeline admission identity does not match persisted run")
		}
	}
	if isDeployment || isRetryApproval {
		starter, ok := e.repos.Pipeline.(interface {
			StartRunIfPending(context.Context, string) error
		})
		if !ok {
			return fmt.Errorf("durable gated scheduling is unavailable")
		}
		checkCtx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
		defer cancel()
		stored, err := e.repos.Pipeline.GetRun(checkCtx, run.RunID)
		if err != nil {
			return err
		}
		if stored == nil || stored.Status != model.PipelineStatusPending || stored.PipelineID != run.PipelineID || stored.DefinitionCommitSha != run.DefinitionCommitSha || stored.RetryApprovalContext != run.RetryApprovalContext || stored.RetryApprovalContextDigest != run.RetryApprovalContextDigest || isRetryApproval && (stored.Env != run.Env || stored.TriggeredBy != run.TriggeredBy || stored.TriggerType != run.TriggerType || stored.DefinitionPath != run.DefinitionPath) || isDeployment && (!stored.AllowDeployments || stored.DeploymentActorID != run.DeploymentActorID || stored.DeploymentPlan != run.DeploymentPlan || stored.DeploymentPlanHash != run.DeploymentPlanHash || stored.DeploymentTargetFingerprint != run.DeploymentTargetFingerprint) {
			return fmt.Errorf("gated run cannot be replayed or changed")
		}
		jobs, err := e.repos.JobRun.ListByPipelineRunID(checkCtx, run.RunID)
		if err != nil {
			return err
		}
		if len(jobs) != 0 {
			return fmt.Errorf("gated run already has execution records; automatic resume is prohibited")
		}
		if err := starter.StartRunIfPending(checkCtx, run.RunID); err != nil {
			return err
		}
	}
	if isPolling {
		if err := e.claimPollingSubmission(run); err != nil {
			return err
		}
	} else if automatic {
		if e.repos == nil || e.repos.Pipeline == nil {
			return fmt.Errorf("durable automatic scheduling is unavailable")
		}
		starter, ok := e.repos.Pipeline.(interface {
			StartAutomaticRunIfPending(context.Context, *model.PipelineRun) (bool, error)
		})
		if !ok {
			return fmt.Errorf("durable automatic scheduling is unavailable")
		}
		checkCtx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
		defer cancel()
		claimed, err := starter.StartAutomaticRunIfPending(checkCtx, run)
		if err != nil {
			return fmt.Errorf("claim automatic pipeline run: %w", err)
		}
		if !claimed {
			return fmt.Errorf("automatic run is no longer eligible for scheduling")
		}
	}
	runCtx, runCancel := context.WithCancel(e.ctx)
	rc := NewCoordinator(run, parsedSpec, e)
	rc.SetCancel(runCancel)
	if _, loaded := e.runs.LoadOrStore(run.RunID, rc); loaded {
		runCancel()
		return fmt.Errorf("run %s is already submitted", run.RunID)
	}

	e.wg.Add(1)
	safe.Go(func() {
		defer e.wg.Done()
		defer e.runs.Delete(run.RunID)
		defer runCancel()

		// Acquire semaphore
		select {
		case e.sem <- struct{}{}:
			defer func() { <-e.sem }()
		case <-runCtx.Done():
			// 即使尚未获得并发名额，也必须持久化取消状态
			_ = rc.Execute(runCtx)
			return
		}

		if err := rc.Execute(runCtx); err != nil {
			log.Errorw("pipeline run failed", "runId", run.RunID, "error", err)
		}
	})

	return nil
}

// rejectSubmission 仅在持有入场锁且尚未认领调度时调用，保留人工提交的持久化行为
func (e *Process) rejectSubmission(run *model.PipelineRun, cause error) error {
	rejection := &admission.RejectionError{Cause: cause}
	if repo.IsPollingPipelineRun(run) {
		if e.repos == nil || e.repos.Pipeline == nil {
			return errors.Join(rejection, fmt.Errorf("polling rejection persistence unavailable"))
		}
		rejector, ok := e.repos.Pipeline.(interface {
			RejectPendingPollingRun(context.Context, *model.PipelineRun) (polling.AdmissionOutcome, error)
		})
		if !ok {
			return errors.Join(rejection, fmt.Errorf("polling rejection persistence unavailable"))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := rejector.RejectPendingPollingRun(ctx, run)
		return errors.Join(rejection, err)
	}
	if !repo.IsAutomaticPipelineRun(run) {
		return rejection
	}
	var persistenceErr error
	if e.repos == nil || e.repos.Pipeline == nil {
		persistenceErr = fmt.Errorf("automatic rejection persistence unavailable")
	} else if rejector, ok := e.repos.Pipeline.(interface {
		RejectPendingAutomaticRun(context.Context, *model.PipelineRun) (bool, error)
	}); ok {
		// 关闭和取消不应丢弃尚未接收运行的最终状态写入
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, persistenceErr = rejector.RejectPendingAutomaticRun(ctx, run)
	} else {
		persistenceErr = fmt.Errorf("automatic rejection persistence unavailable")
	}
	if persistenceErr != nil {
		return errors.Join(rejection, fmt.Errorf("persist automatic submission rejection: %w", persistenceErr))
	}
	return rejection
}

// CancelRun cancels a running pipeline run and propagates cancellation
// to the Agent through its independent control queue.
func (e *Process) CancelRun(runID string) error {
	val, ok := e.runs.Load(runID)
	if !ok {
		return fmt.Errorf("run %s not found in process", runID)
	}
	rc := val.(*Coordinator)
	rc.Cancel()

	// Propagate cancellation to remote Agents for any running JobRuns.
	e.cancelRemoteJobRuns(runID)

	return nil
}

// cancelRemoteJobRuns queries running JobRuns for a pipeline run and sends
// cancellation commands to the corresponding Agents.
func (e *Process) cancelRemoteJobRuns(runID string) {
	if e.repos.JobRun == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	jobs, err := e.repos.JobRun.ListByPipelineRunID(ctx, runID)
	if err != nil {
		log.Warnw("list jobs for cancellation failed", "error", err)
		return
	}
	for _, job := range jobs {
		if model.IsJobRunTerminal(job.Status) || job.AgentID == "" {
			continue
		}
		if err := e.cancelJobRunOnAgent(ctx, job.AgentID, job.JobRunID, "pipeline run cancelled"); err != nil {
			log.Warnw("cancel agent job failed", "jobRunId", job.JobRunID, "error", err)
		}
	}
}

// PauseRun pauses a running pipeline. Already dispatched Agent jobs continue
// to completion, but no new jobs will be dispatched until resumed.
func (e *Process) PauseRun(runID string) error {
	val, ok := e.runs.Load(runID)
	if !ok {
		return fmt.Errorf("run %s not found in process", runID)
	}
	rc := val.(*Coordinator)
	rc.Pause()
	return nil
}

// ResumeRun resumes a paused pipeline run.
func (e *Process) ResumeRun(runID string) error {
	val, ok := e.runs.Load(runID)
	if !ok {
		return fmt.Errorf("run %s not found in process", runID)
	}
	rc := val.(*Coordinator)
	rc.Resume()
	return nil
}

// Stop gracefully shuts down the process, cancelling all in-progress runs
// and waiting for them to finish.
func (e *Process) Stop() {
	log.Info("Pipeline process shutting down...")
	e.mu.Lock()
	e.stopped = true
	e.cancel()
	e.mu.Unlock()
	e.wg.Wait()
	if e.closeLogs != nil {
		e.closeLogs()
	}
	log.Info("Pipeline process stopped")
}
