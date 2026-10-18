// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/safe"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func automaticAdmissionRepository(t *testing.T) (*PipelineRepo, *model.PipelineRun) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "admission.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.AutoMigrate(&model.Project{}, &model.Pipeline{}, &model.PipelineRun{}, &model.JobRun{}); err != nil {
		t.Fatal(err)
	}
	for _, row := range []any{&model.Project{ProjectID: "project", IsEnabled: 1, Status: model.ProjectStatusActive}, &model.Pipeline{PipelineID: "pipeline", ProjectID: "project", IsEnabled: 1, Metadata: "{}"}} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	r := &PipelineRepo{IDatabase: statusTestDB{db}}
	run := &model.PipelineRun{RunID: "run", PipelineID: "pipeline", RequestID: "cron:pipeline:delivery", TriggerType: int(pipelinev1.TriggerType_TRIGGER_TYPE_CRON), TriggeredBy: "cron", Status: model.PipelineStatusPending, DefinitionCommitSha: "definition", DefinitionPath: "pipeline.json", Branch: "main", CommitSha: "source"}
	if err := r.CreateRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	return r, run
}

func TestAutomaticAdmissionCASPreservesStateAndExecutionEvidence(t *testing.T) {
	now := time.Now()
	for _, start := range []bool{false, true} {
		for _, guard := range []string{"fresh", "running", "success", "failed", "cancelled", "paused", "start-time", "end-time", "job", "deployment-plan", "deployment-actor", "deployment-fingerprint", "deployment-hash", "deployment-optin", "approval-context", "approval-digest"} {
			t.Run(map[bool]string{true: "start", false: "reject"}[start]+"/"+guard, func(t *testing.T) {
				r, run := automaticAdmissionRepository(t)
				updates := map[string]any{}
				switch guard {
				case "running":
					updates["status"] = model.PipelineStatusRunning
				case "success":
					updates["status"] = model.PipelineStatusSuccess
				case "failed":
					updates["status"] = model.PipelineStatusFailed
				case "cancelled":
					updates["status"] = model.PipelineStatusCancelled
				case "paused":
					updates["status"] = model.PipelineStatusPaused
				case "start-time":
					updates["start_time"] = now
				case "end-time":
					updates["end_time"] = now
				case "job":
					if err := r.Database().WithContext(context.Background()).Create(&model.JobRun{JobRunID: "job", PipelineRunID: run.RunID, Status: model.JobRunStatusPending}).Error; err != nil {
						t.Fatal(err)
					}
				case "deployment-plan":
					updates["deployment_plan"] = "protected"
				case "deployment-actor":
					updates["deployment_actor_id"] = "actor"
				case "deployment-fingerprint":
					updates["deployment_target_fingerprint"] = "target"
				case "deployment-hash":
					updates["deployment_plan_hash"] = "hash"
				case "deployment-optin":
					updates["allow_deployments"] = true
				case "approval-context":
					updates["retry_approval_context"] = "protected"
				case "approval-digest":
					updates["retry_approval_context_digest"] = "protected"
				}
				if len(updates) > 0 {
					if err := r.Database().WithContext(context.Background()).Model(&model.PipelineRun{}).Where("run_id = ?", run.RunID).Updates(updates).Error; err != nil {
						t.Fatal(err)
					}
				}
				before, err := r.GetRun(context.Background(), run.RunID)
				if err != nil {
					t.Fatal(err)
				}
				transition := r.RejectPendingAutomaticRun
				if start {
					transition = r.StartAutomaticRunIfPending
				}
				changed, err := transition(context.Background(), run)
				if err != nil || changed != (guard == "fresh") {
					t.Fatalf("changed=%v err=%v", changed, err)
				}
				after, err := r.GetRun(context.Background(), run.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if guard == "fresh" {
					if start && (after.Status != model.PipelineStatusRunning || after.StartTime == nil || after.EndTime != nil) || !start && (after.Status != model.PipelineStatusFailed || after.EndTime == nil || after.StartTime != nil) {
						t.Fatalf("bad transition: %+v", after)
					}
					if changed, err := transition(context.Background(), run); err != nil || changed {
						t.Fatalf("replay changed row: %v %v", changed, err)
					}
				} else if before.Status != after.Status || !sameOptionalTime(before.StartTime, after.StartTime) || !sameOptionalTime(before.EndTime, after.EndTime) {
					t.Fatalf("guarded row changed: before=%+v after=%+v", before, after)
				}
			})
		}
	}
}

func sameOptionalTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func TestAutomaticAdmissionCASRequiresExactPersistedIdentity(t *testing.T) {
	mutations := map[string]func(*model.PipelineRun){
		"run":      func(r *model.PipelineRun) { r.RunID = "RUN" },
		"pipeline": func(r *model.PipelineRun) { r.PipelineID = "PIPELINE"; r.RequestID = "cron:PIPELINE:delivery" },
		"request":  func(r *model.PipelineRun) { r.RequestID = "cron:pipeline:DELIVERY" },
		"trigger": func(r *model.PipelineRun) {
			r.TriggerType = int(pipelinev1.TriggerType_TRIGGER_TYPE_EVENT)
			r.TriggeredBy = "webhook"
			r.RequestID = "event:pipeline:delivery"
		},
		"actor":      func(r *model.PipelineRun) { r.TriggeredBy = "CRON" },
		"definition": func(r *model.PipelineRun) { r.DefinitionCommitSha = "DEFINITION" },
		"path":       func(r *model.PipelineRun) { r.DefinitionPath = "Pipeline.json" },
		"branch":     func(r *model.PipelineRun) { r.Branch = "Main" },
		"commit":     func(r *model.PipelineRun) { r.CommitSha = "SOURCE" },
		"env":        func(r *model.PipelineRun) { r.Env = `{"CHANGED":"1"}` },
		"manual": func(r *model.PipelineRun) {
			r.TriggerType = int(pipelinev1.TriggerType_TRIGGER_TYPE_MANUAL)
			r.TriggeredBy = "actor"
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			r, run := automaticAdmissionRepository(t)
			candidate := *run
			mutate(&candidate)
			for _, transition := range []func(context.Context, *model.PipelineRun) (bool, error){r.StartAutomaticRunIfPending, r.RejectPendingAutomaticRun} {
				if changed, err := transition(context.Background(), &candidate); err != nil || changed {
					t.Fatalf("changed identity accepted: %v %v", changed, err)
				}
			}
			stored, err := r.GetRun(context.Background(), run.RunID)
			if err != nil || stored.Status != model.PipelineStatusPending || stored.StartTime != nil || stored.EndTime != nil {
				t.Fatalf("identity mismatch mutated row: %+v %v", stored, err)
			}
		})
	}
}

func TestAutomaticAdmissionCASContextFailureDoesNotChangeRun(t *testing.T) {
	r, run := automaticAdmissionRepository(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, transition := range []func(context.Context, *model.PipelineRun) (bool, error){r.StartAutomaticRunIfPending, r.RejectPendingAutomaticRun} {
		if changed, err := transition(ctx, run); changed || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled DB operation: %v %v", changed, err)
		}
	}
	stored, err := r.GetRun(context.Background(), run.RunID)
	if err != nil || stored.Status != model.PipelineStatusPending || stored.StartTime != nil || stored.EndTime != nil {
		t.Fatalf("cancellation changed row: %+v %v", stored, err)
	}
}

func TestAutomaticAdmissionEnvironmentUsesExactStringValues(t *testing.T) {
	for _, tc := range []struct {
		name, stored, submitted string
		equal                   bool
	}{
		{"MySQL formatting", `{"A": "one", "B": "two"}`, `{"B":"two","A":"one"}`, true},
		{"empty", `{}`, ` { } `, true},
		{"case-sensitive key", `{"A":"one"}`, `{"a":"one"}`, false},
		{"case-sensitive value", `{"A":"One"}`, `{"A":"one"}`, false},
		{"large numeric string", `{"A":"9007199254740993"}`, `{"A":"9007199254740992"}`, false},
		{"number forbidden", `{"A":1}`, `{"A":1}`, false},
		{"null forbidden", `null`, `null`, false},
		{"null value forbidden", `{"A":null}`, `{"A":""}`, false},
		{"nested value forbidden", `{"A":{}}`, `{"A":{}}`, false},
		{"array forbidden", `[]`, `[]`, false},
		{"malformed", `{`, `{`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameAutomaticEnvironment(tc.stored, tc.submitted); got != tc.equal {
				t.Fatalf("same environment=%v; expected %v", got, tc.equal)
			}
		})
	}
}

func TestAutomaticAdmissionCancellationRaceKeepsTerminalOutcome(t *testing.T) {
	for i := 0; i < 20; i++ {
		r, run := automaticAdmissionRepository(t)
		start := make(chan struct{})
		var wait sync.WaitGroup
		wait.Add(2)
		var rejected bool
		var rejectErr, cancelErr error
		safe.Go(func() {
			defer wait.Done()
			<-start
			rejected, rejectErr = r.RejectPendingAutomaticRun(context.Background(), run)
		})
		safe.Go(func() {
			defer wait.Done()
			<-start
			cancelErr = r.UpdateRunIfStatus(context.Background(), run.RunID, model.PipelineStatusPending, map[string]any{"status": model.PipelineStatusCancelled, "end_time": time.Now()})
		})
		close(start)
		wait.Wait()
		if rejectErr != nil {
			t.Fatal(rejectErr)
		}
		if cancelErr != nil && !errors.Is(cancelErr, gorm.ErrRecordNotFound) {
			t.Fatal(cancelErr)
		}
		stored, err := r.GetRun(context.Background(), run.RunID)
		if err != nil || stored.EndTime == nil || stored.StartTime != nil {
			t.Fatalf("race left nonterminal metadata: %+v %v", stored, err)
		}
		if rejected && stored.Status != model.PipelineStatusFailed || !rejected && stored.Status != model.PipelineStatusCancelled {
			t.Fatalf("race overwrote winner: rejected=%v status=%d", rejected, stored.Status)
		}
	}
}
