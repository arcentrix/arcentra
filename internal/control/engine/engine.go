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

package engine

import (
	"context"
	"fmt"
	"sync"

	"github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/pkg/pipeline/spec"
	"github.com/arcentrix/arcentra/internal/pkg/storage"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/plugin"
	"github.com/arcentrix/arcentra/pkg/safe"
)

const defaultMaxConcurrentRuns = 10

// Engine manages pipeline run orchestration. It bridges the trigger layer
// (HTTP/gRPC/Cron/Webhook) with the execution engine (Executor DAG).
type Engine struct {
	repos       *repo.Repositories
	pluginMgr   *plugin.Manager
	taskQueue   nova.TaskQueue
	storage     storage.IStorage
	logger      *log.Logger
	appConf     *config.AppConfig
	secretSvc   *service.SecretService
	auditWriter *AuditWriter

	runs   sync.Map      // runID -> *RunCoordinator
	sem    chan struct{} // concurrency limiter
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewEngine creates a pipeline engine.
func NewEngine(
	repos *repo.Repositories,
	pluginMgr *plugin.Manager,
	taskQueue nova.TaskQueue,
	st storage.IStorage,
	logger *log.Logger,
	appConf *config.AppConfig,
	secretSvc *service.SecretService,
) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	maxRuns := defaultMaxConcurrentRuns
	return &Engine{
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
}

// SetAuditWriter injects the audit writer into the engine. Called during
// bootstrap after DB initialization.
func (e *Engine) SetAuditWriter(aw *AuditWriter) {
	e.auditWriter = aw
}

// Submit asynchronously starts a pipeline run. It returns immediately;
// the actual execution happens in a background goroutine.
func (e *Engine) Submit(run *model.PipelineRun, parsedSpec *spec.Pipeline) error {
	if run == nil || parsedSpec == nil {
		return fmt.Errorf("run and spec must not be nil")
	}

	select {
	case <-e.ctx.Done():
		return fmt.Errorf("engine is shutting down")
	default:
	}

	rc := NewRunCoordinator(run, parsedSpec, e)
	e.runs.Store(run.RunID, rc)

	e.wg.Add(1)
	safe.Go(func() {
		defer e.wg.Done()
		defer e.runs.Delete(run.RunID)

		// Acquire semaphore
		select {
		case e.sem <- struct{}{}:
			defer func() { <-e.sem }()
		case <-e.ctx.Done():
			return
		}

		runCtx, runCancel := context.WithCancel(e.ctx)
		rc.SetCancel(runCancel)
		defer runCancel()

		if err := rc.Execute(runCtx); err != nil {
			log.Errorw("pipeline run failed", "runId", run.RunID, "error", err)
		}
	})

	return nil
}

// CancelRun cancels a running pipeline run and propagates cancellation
// to the Agent via gRPC if any job is executing remotely.
func (e *Engine) CancelRun(runID string) error {
	val, ok := e.runs.Load(runID)
	if !ok {
		return fmt.Errorf("run %s not found in engine", runID)
	}
	rc := val.(*RunCoordinator)
	rc.Cancel()

	// Propagate cancellation to remote Agents for any running JobRuns.
	e.cancelRemoteJobRuns(runID)

	return nil
}

// cancelRemoteJobRuns queries running JobRuns for a pipeline run and sends
// CancelJobRun gRPC calls to the corresponding Agents.
func (e *Engine) cancelRemoteJobRuns(runID string) {
	if e.repos.JobRun == nil {
		return
	}
	ctx := context.Background()
	jobRuns, err := e.repos.JobRun.ListByPipelineRunID(ctx, runID)
	if err != nil {
		log.Warnw("failed to list job runs for cancel propagation", "runId", runID, "error", err)
		return
	}
	for _, jr := range jobRuns {
		if !model.IsJobRunRunning(jr.Status) || jr.AgentID == "" {
			continue
		}
		agent, err := e.repos.Agent.Get(ctx, jr.AgentID)
		if err != nil || agent == nil || agent.Address == "" {
			log.Warnw("skip cancel: agent not reachable", "agentId", jr.AgentID, "jobRunId", jr.JobRunID)
			continue
		}
		safe.Go(func() {
			if err := cancelJobRunOnAgent(agent.Address, jr.JobRunID, "pipeline run cancelled"); err != nil {
				log.Warnw("cancel job run on agent failed", "agentId", jr.AgentID, "jobRunId", jr.JobRunID, "error", err)
			}
		})
	}
}

// PauseRun pauses a running pipeline. Already dispatched Agent jobs continue
// to completion, but no new jobs will be dispatched until resumed.
func (e *Engine) PauseRun(runID string) error {
	val, ok := e.runs.Load(runID)
	if !ok {
		return fmt.Errorf("run %s not found in engine", runID)
	}
	rc := val.(*RunCoordinator)
	rc.Pause()
	return nil
}

// ResumeRun resumes a paused pipeline run.
func (e *Engine) ResumeRun(runID string) error {
	val, ok := e.runs.Load(runID)
	if !ok {
		return fmt.Errorf("run %s not found in engine", runID)
	}
	rc := val.(*RunCoordinator)
	rc.Resume()
	return nil
}

// Stop gracefully shuts down the engine, cancelling all in-progress runs
// and waiting for them to finish.
func (e *Engine) Stop() {
	log.Info("Pipeline engine shutting down...")
	e.cancel()
	e.wg.Wait()
	log.Info("Pipeline engine stopped")
}
