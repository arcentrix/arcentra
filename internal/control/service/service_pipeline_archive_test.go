// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
)

func TestTriggerArchivedProjectDoesNotLoadSCMOrCreateRun(t *testing.T) {
	adapter, repositories, calls, _ := pipelineRuntimeFixture(t, pipelineRuntimeSpec())
	if err := adapter.db.Model(&model.Project{}).Where("project_id = ?", "runtime-project").Update("status", model.ProjectStatusArchived).Error; err != nil {
		t.Fatal(err)
	}
	engine := &triggerCaptureEngine{}
	svc := &PipelineServiceImpl{pipelineRepo: repositories.Pipeline, projectRepo: repositories.Project, engine: engine, offlineRuntimePolicy: pipelineRuntimePolicy()}
	response, err := svc.TriggerPipeline(context.Background(), &pipelinev1.TriggerPipelineRequest{PipelineId: "runtime-pipeline", TriggeredBy: "actor"})
	if err != nil || response.Success || response.GetError().GetCode() != 409 || engine.calls != 0 {
		t.Fatalf("archived trigger accepted: %+v %v", response, err)
	}
	if _, err := os.Stat(calls); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("archived trigger accessed SCM")
	}
	var count int64
	if err := adapter.db.Model(&model.PipelineRun{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("archived trigger created a run", err)
	}
}

type committedButUnknownEngine struct {
	triggerCaptureEngine
	repository repo.IPipelineRepository
}

func (e *committedButUnknownEngine) Submit(run *model.PipelineRun, _ *pipelinev1.Spec) error {
	if err := e.repository.UpdateRun(context.Background(), run.RunID, map[string]any{"status": model.PipelineStatusRunning, "start_time": time.Now()}); err != nil {
		return err
	}
	return errors.New("synthetic admission ACK unavailable")
}
func TestTriggerUnknownAdmissionDoesNotFailCommittedRun(t *testing.T) {
	_, repositories, _, _ := pipelineRuntimeFixture(t, pipelineRuntimeSpec())
	engine := &committedButUnknownEngine{repository: repositories.Pipeline}
	svc := &PipelineServiceImpl{pipelineRepo: repositories.Pipeline, projectRepo: repositories.Project, engine: engine, offlineRuntimePolicy: pipelineRuntimePolicy()}
	response, err := svc.TriggerPipeline(context.Background(), &pipelinev1.TriggerPipelineRequest{PipelineId: "runtime-pipeline", TriggeredBy: "actor", RequestId: "unknown-ack"})
	if err != nil || response.Success || response.RunId == "" {
		t.Fatalf("unknown admission response %+v %v", response, err)
	}
	stored, err := repositories.Pipeline.GetRun(context.Background(), response.RunId)
	if err != nil || stored.Status != model.PipelineStatusRunning || stored.StartTime == nil || stored.EndTime != nil {
		t.Fatal("unknown admission overwrote committed claim", err)
	}
}
