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
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/internal/shared/pipeline"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/validation"
	"github.com/arcentrix/arcentra/internal/shared/storage"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/plugin"
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
	if run == nil || parsedSpec == nil {
		return fmt.Errorf("run and spec must not be nil")
	}

	if err := validation.NewSchemaValidatorWithRuntimePolicy(e.offlineRuntimePolicy).Validate(parsedSpec); err != nil {
		return err
	}
	isDeployment, err := e.validateDeploymentRun(run, parsedSpec)
	if err != nil {
		return err
	}
	runCopy := *run
	run = &runCopy
	parsedSpec = proto.Clone(parsedSpec).(*spec.Pipeline)
	if _, _, err := pipeline.BuildDAG(parsedSpec.Jobs); err != nil {
		return fmt.Errorf("invalid pipeline DAG: %w", err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.logInitErr != nil {
		return fmt.Errorf("initialize durable build logs: %w", e.logInitErr)
	}
	if e.stopped {
		return fmt.Errorf("process is shutting down")
	}
	if _, exists := e.runs.Load(run.RunID); exists {
		return fmt.Errorf("run %s is already submitted", run.RunID)
	}
	if isDeployment {
		starter, ok := e.repos.Pipeline.(interface {
			StartRunIfPending(context.Context, string) error
		})
		if !ok {
			return fmt.Errorf("durable deployment scheduling is unavailable")
		}
		checkCtx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
		defer cancel()
		stored, err := e.repos.Pipeline.GetRun(checkCtx, run.RunID)
		if err != nil {
			return err
		}
		if stored == nil || stored.Status != model.PipelineStatusPending || stored.PipelineID != run.PipelineID || !stored.AllowDeployments || stored.DeploymentActorID != run.DeploymentActorID || stored.DeploymentPlan != run.DeploymentPlan || stored.DeploymentPlanHash != run.DeploymentPlanHash || stored.DefinitionCommitSha != run.DefinitionCommitSha || stored.DeploymentTargetFingerprint != run.DeploymentTargetFingerprint {
			return fmt.Errorf("deployment run cannot be replayed or changed")
		}
		jobs, err := e.repos.JobRun.ListByPipelineRunID(checkCtx, run.RunID)
		if err != nil {
			return err
		}
		if len(jobs) != 0 {
			return fmt.Errorf("deployment run already has execution records; automatic resume is prohibited")
		}
		if err := starter.StartRunIfPending(checkCtx, run.RunID); err != nil {
			return err
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
