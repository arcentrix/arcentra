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

package trigger

import (
	"context"
	"fmt"
	"sync"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/pkg/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/cron"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/log"
)

// IPipelineEngine abstracts the engine to avoid circular imports.
type IPipelineEngine interface {
	Submit(run *model.PipelineRun, parsedSpec *spec.Pipeline) error
}

// DefinitionLoader loads a pipeline's YAML content from its backing repository.
type DefinitionLoader func(ctx context.Context, pipeline *model.Pipeline, project *model.Project) (content string, headSha string, err error)

// CronTriggerManager dynamically registers pipeline cron triggers with the
// global pkg/cron scheduler. Each pipeline with triggers[].type=cron gets its
// own cron entry keyed by "pipeline-cron:{pipelineID}".
type CronTriggerManager struct {
	scheduler    *cron.Cron
	engine       IPipelineEngine
	pipelineRepo repo.IPipelineRepository
	projectRepo  repo.IProjectRepository
	loader       DefinitionLoader

	mu         sync.RWMutex
	registered map[string]string // pipelineID -> cron expression currently registered
}

// NewCronTriggerManager creates a manager for dynamic per-pipeline cron triggers.
func NewCronTriggerManager(
	scheduler *cron.Cron,
	engine IPipelineEngine,
	pipelineRepo repo.IPipelineRepository,
	projectRepo repo.IProjectRepository,
	loader DefinitionLoader,
) *CronTriggerManager {
	return &CronTriggerManager{
		scheduler:    scheduler,
		engine:       engine,
		pipelineRepo: pipelineRepo,
		projectRepo:  projectRepo,
		loader:       loader,
		registered:   make(map[string]string),
	}
}

// SyncAll loads all enabled pipelines, parses their specs, and reconciles
// cron entries with the scheduler (add new, remove stale, update changed).
func (m *CronTriggerManager) SyncAll(ctx context.Context) {
	pipelines, _, err := m.pipelineRepo.List(ctx, &repo.PipelineQuery{
		Page:     1,
		PageSize: 500,
	})
	if err != nil {
		log.Warnw("cron trigger sync: failed to list pipelines", "error", err)
		return
	}

	wanted := make(map[string]string) // pipelineID -> expression

	for _, p := range pipelines {
		if p == nil || p.IsEnabled != 1 {
			continue
		}
		expr := m.extractCronExpression(ctx, p)
		if expr != "" {
			wanted[p.PipelineID] = expr
		}
	}

	m.reconcile(wanted)
}

// Register adds or updates a single pipeline cron trigger.
func (m *CronTriggerManager) Register(pipelineID, expression string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	entryName := cronEntryName(pipelineID)

	if existing, ok := m.registered[pipelineID]; ok {
		if existing == expression {
			return nil
		}
		_ = m.scheduler.Remove(entryName)
	}

	if err := m.scheduler.AddFunc(expression, m.makeTriggerFunc(pipelineID), entryName); err != nil {
		return fmt.Errorf("register cron for pipeline %s: %w", pipelineID, err)
	}
	m.registered[pipelineID] = expression
	log.Infow("registered pipeline cron trigger", "pipelineId", pipelineID, "expression", expression)
	return nil
}

// Unregister removes a pipeline cron trigger.
func (m *CronTriggerManager) Unregister(pipelineID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.registered[pipelineID]; !ok {
		return
	}
	_ = m.scheduler.Remove(cronEntryName(pipelineID))
	delete(m.registered, pipelineID)
	log.Infow("unregistered pipeline cron trigger", "pipelineId", pipelineID)
}

// reconcile diffs the wanted map against the currently registered map
// and applies add / remove / update operations.
func (m *CronTriggerManager) reconcile(wanted map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for pid := range m.registered {
		if _, ok := wanted[pid]; !ok {
			_ = m.scheduler.Remove(cronEntryName(pid))
			delete(m.registered, pid)
			log.Infow("cron trigger removed (pipeline disabled/deleted)", "pipelineId", pid)
		}
	}

	for pid, expr := range wanted {
		existing, ok := m.registered[pid]
		if ok && existing == expr {
			continue
		}
		if ok {
			_ = m.scheduler.Remove(cronEntryName(pid))
		}
		if err := m.scheduler.AddFunc(expr, m.makeTriggerFunc(pid), cronEntryName(pid)); err != nil {
			log.Warnw("cron trigger register failed", "pipelineId", pid, "expression", expr, "error", err)
			continue
		}
		m.registered[pid] = expr
		if ok {
			log.Infow("cron trigger updated", "pipelineId", pid, "expression", expr)
		} else {
			log.Infow("cron trigger registered", "pipelineId", pid, "expression", expr)
		}
	}
}

// makeTriggerFunc returns a closure invoked by the cron scheduler when a
// pipeline's cron fires. It loads the spec, creates a PipelineRun, and submits.
func (m *CronTriggerManager) makeTriggerFunc(pipelineID string) func() {
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		pipeline, err := m.pipelineRepo.Get(ctx, pipelineID)
		if err != nil || pipeline == nil || pipeline.IsEnabled != 1 {
			log.Warnw("cron trigger: pipeline unavailable", "pipelineId", pipelineID, "error", err)
			return
		}

		project, err := m.projectRepo.Get(ctx, pipeline.ProjectID)
		if err != nil || project == nil {
			log.Warnw("cron trigger: project unavailable", "pipelineId", pipelineID, "error", err)
			return
		}

		content, headSha, err := m.loader(ctx, pipeline, project)
		if err != nil {
			log.Warnw("cron trigger: load definition failed", "pipelineId", pipelineID, "error", err)
			return
		}

		parsedSpec, err := spec.ParseContentToProto(content, pipelinev1.SpecFormat_SPEC_FORMAT_UNSPECIFIED)
		if err != nil {
			log.Warnw("cron trigger: parse spec failed", "pipelineId", pipelineID, "error", err)
			return
		}

		fireTS := time.Now().UTC().Truncate(time.Minute).Format("200601021504")
		requestID := fmt.Sprintf("cron:%s:%s", pipelineID, fireTS)

		existing, _ := m.pipelineRepo.GetRunByRequestID(ctx, pipelineID, requestID)
		if existing != nil {
			return
		}

		run := &model.PipelineRun{
			RunID:               id.GetUild(),
			PipelineID:          pipeline.PipelineID,
			RequestID:           requestID,
			PipelineName:        pipeline.Name,
			Branch:              pipeline.DefaultBranch,
			DefinitionCommitSha: headSha,
			DefinitionPath:      pipeline.PipelineFilePath,
			Status:              model.PipelineStatusPending,
			TriggerType:         int(pipelinev1.TriggerType_TRIGGER_TYPE_CRON),
			TriggeredBy:         "cron",
		}
		if err := m.pipelineRepo.CreateRun(ctx, run); err != nil {
			log.Warnw("cron trigger: create run failed", "pipelineId", pipelineID, "error", err)
			return
		}

		if err := m.engine.Submit(run, parsedSpec); err != nil {
			log.Warnw("cron trigger: engine submit failed", "pipelineId", pipelineID, "runId", run.RunID, "error", err)
			return
		}

		log.Infow("cron trigger fired", "pipelineId", pipelineID, "runId", run.RunID, "requestId", requestID)
	}
}

// extractCronExpression loads a pipeline's YAML spec and returns the first
// cron expression found in its pipeline-level triggers.
func (m *CronTriggerManager) extractCronExpression(ctx context.Context, p *model.Pipeline) string {
	project, err := m.projectRepo.Get(ctx, p.ProjectID)
	if err != nil || project == nil {
		return ""
	}

	content, _, err := m.loader(ctx, p, project)
	if err != nil {
		log.Debugw("cron trigger: skip pipeline (load failed)", "pipelineId", p.PipelineID, "error", err)
		return ""
	}

	parsed, err := spec.ParseContentToProto(content, pipelinev1.SpecFormat_SPEC_FORMAT_UNSPECIFIED)
	if err != nil {
		log.Debugw("cron trigger: skip pipeline (parse failed)", "pipelineId", p.PipelineID, "error", err)
		return ""
	}

	exprs := ExtractCronExpressions(parsed)
	if len(exprs) == 0 {
		return ""
	}
	return exprs[0]
}

func cronEntryName(pipelineID string) string {
	return "pipeline-cron:" + pipelineID
}
