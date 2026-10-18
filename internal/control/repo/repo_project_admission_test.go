// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"sync"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
)

func manualAdmissionRun(t *testing.T, r *PipelineRepo) *model.PipelineRun {
	t.Helper()
	run := &model.PipelineRun{RunID: "manual", PipelineID: "pipeline", RequestID: "manual-request", TriggerType: int(pipelinev1.TriggerType_TRIGGER_TYPE_MANUAL), TriggeredBy: "actor", Status: model.PipelineStatusPending, DefinitionCommitSha: "definition", DefinitionPath: "pipeline.json", Branch: "main", CommitSha: "source"}
	if err := r.CreateRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestProjectLifecycleBlocksEveryNewRunAdmission(t *testing.T) {
	for _, source := range []string{"manual", "automatic"} {
		for _, state := range []string{"active", "archived", "disabled", "inactive", "not-enabled", "missing-project", "missing-pipeline", "pipeline-disabled", "pipeline-deleted"} {
			t.Run(source+"/"+state, func(t *testing.T) {
				r, run := automaticAdmissionRepository(t)
				transition := r.StartAutomaticRunIfPending
				if source == "manual" {
					run, transition = manualAdmissionRun(t, r), r.StartManualRunIfPending
				}
				db := r.Database()
				var err error
				switch state {
				case "archived":
					err = db.Model(&model.Project{}).Where("project_id = ?", "project").Update("status", model.ProjectStatusArchived).Error
				case "disabled":
					err = db.Model(&model.Project{}).Where("project_id = ?", "project").Update("status", model.ProjectStatusDisabled).Error
				case "inactive":
					err = db.Model(&model.Project{}).Where("project_id = ?", "project").Update("status", model.ProjectStatusInactive).Error
				case "not-enabled":
					err = db.Model(&model.Project{}).Where("project_id = ?", "project").Update("is_enabled", 0).Error
				case "missing-project":
					err = db.Where("project_id = ?", "project").Delete(&model.Project{}).Error
				case "missing-pipeline":
					err = db.Where("pipeline_id = ?", "pipeline").Delete(&model.Pipeline{}).Error
				case "pipeline-disabled":
					err = db.Model(&model.Pipeline{}).Where("pipeline_id = ?", "pipeline").Update("is_enabled", 0).Error
				case "pipeline-deleted":
					err = db.Model(&model.Pipeline{}).Where("pipeline_id = ?", "pipeline").Update("is_deleted", 1).Error
				}
				if err != nil {
					t.Fatal(err)
				}
				admitted, err := transition(context.Background(), run)
				if err != nil || admitted != (state == "active") {
					t.Fatalf("admitted=%v err=%v", admitted, err)
				}
				stored, err := r.GetRun(context.Background(), run.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if state != "active" && (stored.Status != model.PipelineStatusPending || stored.StartTime != nil || stored.EndTime != nil) {
					t.Fatal("denied admission changed durable run")
				}
				if state == "active" {
					if err := db.Model(&model.Project{}).Where("project_id = ?", "project").Update("status", model.ProjectStatusArchived).Error; err != nil {
						t.Fatal(err)
					}
					if replay, err := transition(context.Background(), run); err != nil || replay {
						t.Fatal("archive allowed readmission", err)
					}
					after, err := r.GetRun(context.Background(), run.RunID)
					if err != nil || after.Status != model.PipelineStatusRunning || !sameOptionalTime(stored.StartTime, after.StartTime) || after.EndTime != nil {
						t.Fatal("archive cancelled an admitted run", err)
					}
				}
			})
		}
	}
}

func TestManualAdmissionRequiresExactIdentityAndNoExecution(t *testing.T) {
	for _, change := range []string{"actor", "request", "pipeline", "definition", "path", "branch", "commit", "env", "deployment", "approval", "running", "finished", "start-time", "end-time", "job"} {
		t.Run(change, func(t *testing.T) {
			r, _ := automaticAdmissionRepository(t)
			run := manualAdmissionRun(t, r)
			candidate := *run
			var err error
			switch change {
			case "actor":
				candidate.TriggeredBy = "other"
			case "request":
				candidate.RequestID = "other"
			case "pipeline":
				candidate.PipelineID = "other"
			case "definition":
				candidate.DefinitionCommitSha = "other"
			case "path":
				candidate.DefinitionPath = "other"
			case "branch":
				candidate.Branch = "other"
			case "commit":
				candidate.CommitSha = "other"
			case "env":
				candidate.Env = `{"OTHER":"value"}`
			case "deployment":
				candidate.AllowDeployments = true
			case "approval":
				candidate.RetryApprovalContext = "other"
			case "running":
				err = r.Database().Model(&model.PipelineRun{}).Where("run_id = ?", run.RunID).Update("status", model.PipelineStatusRunning).Error
			case "finished":
				err = r.Database().Model(&model.PipelineRun{}).Where("run_id = ?", run.RunID).Update("status", model.PipelineStatusSuccess).Error
			case "start-time":
				err = r.Database().Model(&model.PipelineRun{}).Where("run_id = ?", run.RunID).Update("start_time", time.Now()).Error
			case "end-time":
				err = r.Database().Model(&model.PipelineRun{}).Where("run_id = ?", run.RunID).Update("end_time", time.Now()).Error
			case "job":
				err = r.Database().Create(&model.JobRun{JobRunID: "execution", PipelineRunID: run.RunID, Status: model.JobRunStatusPending}).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := r.GetRun(context.Background(), run.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if admitted, err := r.StartManualRunIfPending(context.Background(), &candidate); err != nil || admitted {
				t.Fatalf("changed identity admitted=%v err=%v", admitted, err)
			}
			after, err := r.GetRun(context.Background(), run.RunID)
			if err != nil || before.Status != after.Status || !sameOptionalTime(before.StartTime, after.StartTime) || !sameOptionalTime(before.EndTime, after.EndTime) {
				t.Fatal("denial changed durable evidence", err)
			}
		})
	}
}

func TestArchivedProjectStillAllowsSafeAutomaticRejection(t *testing.T) {
	r, run := automaticAdmissionRepository(t)
	if err := r.Database().Model(&model.Project{}).Where("project_id = ?", "project").Update("status", model.ProjectStatusArchived).Error; err != nil {
		t.Fatal(err)
	}
	if rejected, err := r.RejectPendingAutomaticRun(context.Background(), run); err != nil || !rejected {
		t.Fatal("archive prevented safe rejection", err)
	}
}

func TestManualAdmissionConcurrentCASHasOneWinner(t *testing.T) {
	r, _ := automaticAdmissionRepository(t)
	run := manualAdmissionRun(t, r)
	start := make(chan struct{})
	var wait sync.WaitGroup
	results := make(chan bool, 2)
	failures := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			won, err := r.StartManualRunIfPending(context.Background(), run)
			results <- won
			failures <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(failures)
	wins := 0
	for won := range results {
		if won {
			wins++
		}
	}
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("CAS winners=%d", wins)
	}
}
