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
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/internal/shared/pipeline"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/log"
)

// Coordinator drives a single PipelineRun through its full lifecycle:
// Pending → Running → (execute DAG) → Success / Failed / Cancelled.
type Coordinator struct {
	run    *model.PipelineRun
	spec   *spec.Pipeline
	engine *Process

	cancelFn context.CancelFunc
	mu       sync.Mutex
	paused   bool
	pauseCh  chan struct{} // closed when resume is called
}

// NewCoordinator creates a coordinator for one pipeline run.
func NewCoordinator(run *model.PipelineRun, s *spec.Pipeline, engine *Process) *Coordinator {
	return &Coordinator{
		run:     run,
		spec:    s,
		engine:  engine,
		pauseCh: make(chan struct{}),
	}
}

// SetCancel stores the cancel function for this run's context.
func (rc *Coordinator) SetCancel(cancel context.CancelFunc) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.cancelFn = cancel
}

// Cancel cancels the run.
func (rc *Coordinator) Cancel() {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.cancelFn != nil {
		rc.cancelFn()
	}
}

// Pause sets the paused flag. The reconciler will block before dispatching new jobs.
func (rc *Coordinator) Pause() {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if !rc.paused {
		rc.paused = true
		rc.pauseCh = make(chan struct{})
	}
}

// Resume clears the paused flag and unblocks any waiting goroutines.
func (rc *Coordinator) Resume() {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.paused {
		rc.paused = false
		close(rc.pauseCh)
	}
}

// IsPaused returns whether this run is currently paused.
func (rc *Coordinator) IsPaused() bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.paused
}

// WaitIfPaused blocks until the run is resumed or the context is cancelled.
func (rc *Coordinator) WaitIfPaused(ctx context.Context) error {
	rc.mu.Lock()
	paused := rc.paused
	ch := rc.pauseCh
	rc.mu.Unlock()

	if !paused {
		return nil
	}

	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Execute runs the full pipeline lifecycle.
func (rc *Coordinator) Execute(ctx context.Context) error {
	pipelineRepo := rc.engine.repos.Pipeline
	auditWriter := rc.engine.auditWriter
	now := time.Now()

	// Audit: pipeline run started.
	if auditWriter != nil {
		auditWriter.Write(ctx, PipelineAudit("pipeline_run.started", "", rc.run.RunID, rc.run.PipelineID))
	}

	totalJobs := len(rc.spec.Jobs)
	if err := pipelineRepo.UpdateRun(ctx, rc.run.RunID, map[string]any{
		"status":     model.PipelineStatusRunning,
		"start_time": now,
		"total_jobs": totalJobs,
	}); err != nil {
		return fmt.Errorf("update run to running: %w", err)
	}

	execErr := rc.executeDAG(ctx)

	endTime := time.Now()
	duration := endTime.Sub(now).Milliseconds()

	updates := map[string]any{
		"end_time": endTime,
		"duration": duration,
	}

	var auditAction string
	if execErr != nil {
		if ctx.Err() != nil {
			updates["status"] = model.PipelineStatusCancelled
			auditAction = "pipeline_run.cancelled"
		} else {
			updates["status"] = model.PipelineStatusFailed
			auditAction = "pipeline_run.failed"
		}
	} else {
		updates["status"] = model.PipelineStatusSuccess
		auditAction = "pipeline_run.completed"
	}

	if err := pipelineRepo.UpdateRun(ctx, rc.run.RunID, updates); err != nil {
		log.Errorw("failed to update run terminal status", "runId", rc.run.RunID, "error", err)
	}

	// Audit: pipeline run terminal state.
	if auditWriter != nil {
		auditWriter.Write(ctx, PipelineAudit(auditAction, "", rc.run.RunID, rc.run.PipelineID))
	}

	rc.updatePipelineStats(ctx, updates["status"].(int))

	// Workspace cleanup: remove the run working directory unless kept for debugging.
	if !rc.engine.appConf.Pipeline.KeepWorkspace {
		workspace := rc.resolveWorkspace()
		if err := os.RemoveAll(workspace); err != nil {
			log.Warnw("failed to clean up workspace", "runId", rc.run.RunID, "path", workspace, "error", err)
		}
	}

	return execErr
}

// executeDAG builds the ExecutionContext and runs the DAG Executor.
func (rc *Coordinator) executeDAG(ctx context.Context) error {
	workspace := rc.resolveWorkspace()

	execCtx := pipeline.NewExecutionContext(
		rc.spec,
		rc.engine.pluginMgr,
		workspace,
		*rc.engine.logger,
	)

	if rc.engine.taskQueue != nil {
		execCtx.SetTaskQueue(rc.engine.taskQueue)
	}

	rc.loadSecrets(ctx, execCtx)
	rc.setupEventEmitter(execCtx)
	rc.setupLogPublisher(execCtx)

	jobRunStore := NewJobRunStore(
		rc.engine.repos.JobRun,
		rc.engine.repos.StepRun,
	)
	execCtx.JobRunStore = jobRunStore
	execCtx.RunCoordinator = rc
	execCtx.PipelineRunID = rc.run.RunID
	execCtx.PipelineIDRef = rc.run.PipelineID
	execCtx.ArtifactURIs = make(map[string]string)

	pipelineExec := pipeline.NewPipelineExecutorFromContext(execCtx, *rc.engine.logger)
	return pipelineExec.Execute(ctx)
}

// setupEventEmitter initialises the CloudEvents emitter so that
// TaskFramework job/step events are actually published (to Kafka when
// configured, otherwise silently dropped).
func (rc *Coordinator) setupEventEmitter(execCtx *pipeline.ExecutionContext) {
	cfg := executor.EventEmitterConfig{
		SourcePrefix:   "urn:arcentra:control",
		PublishTimeout: 5 * time.Second,
	}

	kafkaCfg := rc.engine.appConf.MessageQueue.Kafka
	if kafkaCfg.BootstrapServers == "" {
		// Fallback: log events instead of silently dropping them.
		execCtx.SetEventEmitter(executor.NewEventEmitter(&executor.LogEventPublisher{}, cfg))
		return
	}
	publisher, err := executor.NewKafkaPublisher(
		kafkaCfg.BootstrapServers,
		"arcentra-control-events",
		"EVENT_PIPELINE",
	)
	if err != nil {
		log.Warnw("failed to create event publisher, falling back to log", "error", err)
		execCtx.SetEventEmitter(executor.NewEventEmitter(&executor.LogEventPublisher{}, cfg))
		return
	}

	execCtx.SetEventEmitter(executor.NewEventEmitter(publisher, cfg))
}

// setupLogPublisher initialises a KafkaLogPublisher so that local steps
// executed on the control plane publish their logs to the BUILD_LOGS topic,
// making them visible through the WebSocket log stream.
func (rc *Coordinator) setupLogPublisher(execCtx *pipeline.ExecutionContext) {
	kafkaCfg := rc.engine.appConf.MessageQueue.Kafka
	if kafkaCfg.BootstrapServers == "" {
		return
	}
	pub, err := executor.NewKafkaLogPublisher(kafkaCfg, "arcentra-control-logs")
	if err != nil {
		log.Warnw("failed to create log publisher for control-plane steps", "error", err)
		return
	}
	execCtx.LogPublisher = pub
}

// loadSecrets fetches project-scoped secrets and injects them into the
// execution context env as "secrets.<name>" so that ${{ secrets.xxx }} resolves.
func (rc *Coordinator) loadSecrets(ctx context.Context, execCtx *pipeline.ExecutionContext) {
	if rc.engine.secretSvc == nil {
		return
	}
	secrets, _, err := rc.engine.secretSvc.GetSecretList(
		ctx, 1, 100, "", "project", rc.run.PipelineID, "",
	)
	if err != nil {
		log.Warnw("failed to load secrets for pipeline run", "runId", rc.run.RunID, "error", err)
		return
	}
	for _, s := range secrets {
		val, decErr := rc.engine.secretSvc.GetSecretValue(ctx, s.SecretID)
		if decErr != nil {
			log.Warnw("failed to decrypt secret", "secretId", s.SecretID, "error", decErr)
			continue
		}
		execCtx.Env["secrets."+s.Name] = val
	}
}

// resolveWorkspace returns the workspace root for this pipeline run.
func (rc *Coordinator) resolveWorkspace() string {
	base := os.TempDir()
	if rc.engine.appConf != nil && rc.engine.appConf.HTTP.Host != "" {
		base = filepath.Join(os.TempDir(), "arcentra")
	}
	p := filepath.Join(base, "pipelines", rc.run.PipelineID, rc.run.RunID)
	_ = os.MkdirAll(p, 0o755)
	return p
}

// updatePipelineStats updates the Pipeline aggregate counters.
func (rc *Coordinator) updatePipelineStats(ctx context.Context, status int) {
	pipelineRepo := rc.engine.repos.Pipeline
	p, err := pipelineRepo.Get(ctx, rc.run.PipelineID)
	if err != nil || p == nil {
		return
	}

	updates := map[string]any{
		"status": status,
	}
	switch status {
	case model.PipelineStatusSuccess:
		updates["success_runs"] = p.SuccessRuns + 1
	case model.PipelineStatusFailed:
		updates["failed_runs"] = p.FailedRuns + 1
	}
	_ = pipelineRepo.Update(ctx, rc.run.PipelineID, updates)
}
