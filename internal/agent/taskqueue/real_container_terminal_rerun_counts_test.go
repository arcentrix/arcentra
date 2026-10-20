//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	controlservice "github.com/arcentrix/arcentra/internal/control/service"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Run after dispatcher join and queue drain, so an accidental second intent or
// delayed target cannot hide behind browser POST counters. The three materialized
// targets are the primary success and the two existing auxiliary scenarios; the
// separately cancelled preparation must still have no Run/Job/gate/schedule.
func terminalRerunAssertTargetCounts(t *testing.T, f *controllerWorkerFixture, owner string, primary, cancelled controlservice.CopilotRerunIntent, auxiliary map[string]any) map[string]int64 {
	t.Helper()
	counts, err := terminalRerunReadTargetCounts(f.ctx, f.db, f.projectID, f.pipelineID, owner, primary, cancelled, auxiliary)
	if err != nil {
		t.Fatal(err)
	}
	return counts
}

func terminalRerunReadTargetCounts(ctx context.Context, db *gorm.DB, project, pipeline, owner string, primary, cancelled controlservice.CopilotRerunIntent, auxiliary map[string]any) (map[string]int64, error) {
	expected := map[string]string{primary.NewRunID: primary.NewJobRunID, cancelled.NewRunID: cancelled.NewJobRunID}
	for _, prefix := range []string{"cancelled", "lostResponse"} {
		run, runOK := auxiliary[prefix+"RunId"].(string)
		job, jobOK := auxiliary[prefix+"JobRunId"].(string)
		if !runOK || !jobOK || run == "" || job == "" || expected[run] != "" {
			return nil, errors.New("existing auxiliary target identities are missing or duplicated")
		}
		expected[run] = job
	}
	if len(expected) != 4 {
		return nil, errors.New("terminal scenario requires four distinct fixed intents")
	}
	var intents []model.CopilotRerunIntent
	if err := db.WithContext(ctx).Select("intent_id", "owner_user_id", "request_id", "pipeline_id", "new_run_id", "new_job_run_id", "gate_id", "schedule_id").Where("project_id = ? AND original_job_run_id = ?", project, primary.OriginalJobRunID).Find(&intents).Error; err != nil || len(intents) != 4 {
		return nil, errors.New("response recovery or replay created extra intents for the original job")
	}
	var browserRequestIntents int64
	if err := db.WithContext(ctx).Model(&model.CopilotRerunIntent{}).Where("owner_user_id = ? AND project_id = ? AND request_id = ?", owner, project, primary.RequestID).Count(&browserRequestIntents).Error; err != nil || browserRequestIntents != 1 {
		return nil, errors.New("browser RequestID did not retain exactly one durable intent")
	}
	counts := map[string]int64{"intents": int64(len(intents)), "browserRequestIntents": browserRequestIntents, "targetRuns": 0, "targetJobs": 0, "admissionGates": 0, "schedules": 0}
	seen := make(map[string]bool)
	for _, intent := range intents {
		if intent.OwnerUserID != owner || intent.PipelineID != pipeline || expected[intent.NewRunID] != intent.NewJobRunID || intent.NewJobRunID == "" || intent.GateID == "" || intent.ScheduleID == "" || seen[intent.NewRunID] {
			return nil, errors.New("durable intent targets do not match the exact four scenarios")
		}
		seen[intent.NewRunID] = true
		want := int64(1)
		if intent.NewRunID == cancelled.NewRunID {
			want = 0
			if intent.IntentID != cancelled.IntentID {
				return nil, errors.New("cancelled preparation identity changed")
			}
		}
		if intent.NewRunID == primary.NewRunID && (intent.IntentID != primary.IntentID || intent.RequestID != primary.RequestID) {
			return nil, errors.New("browser recovery replaced its durable intent or RequestID")
		}
		for _, item := range []struct {
			row     any
			key     string
			filter  map[string]any
			binding map[string]any
		}{
			{&model.PipelineRun{}, "targetRuns", map[string]any{"copilot_rerun_intent_id": intent.IntentID}, map[string]any{"run_id": intent.NewRunID}},
			{&model.JobRun{}, "targetJobs", map[string]any{"copilot_rerun_intent_id": intent.IntentID}, map[string]any{"job_run_id": intent.NewJobRunID, "pipeline_run_id": intent.NewRunID}},
			{&model.CopilotRerunAdmissionGate{}, "admissionGates", map[string]any{"schedule_id": intent.ScheduleID}, map[string]any{"gate_id": intent.GateID, "run_id": intent.NewRunID, "job_run_id": intent.NewJobRunID}},
			{&model.CopilotRerunSchedule{}, "schedules", map[string]any{"intent_id": intent.IntentID}, map[string]any{"schedule_id": intent.ScheduleID, "run_id": intent.NewRunID, "job_run_id": intent.NewJobRunID}},
		} {
			var count int64
			if err := db.WithContext(ctx).Model(item.row).Where(item.filter).Count(&count).Error; err != nil || count != want {
				return nil, fmt.Errorf("fixed terminal intent has unexpected %s count", item.key)
			}
			var bound int64
			if err := db.WithContext(ctx).Model(item.row).Where(item.filter).Where(item.binding).Count(&bound).Error; err != nil || bound != want {
				return nil, fmt.Errorf("fixed terminal intent changed exact %s identity", item.key)
			}
			counts[item.key] += count
		}
	}
	for _, key := range []string{"targetRuns", "targetJobs", "admissionGates", "schedules"} {
		if counts[key] != 3 {
			return nil, fmt.Errorf("terminal journey must retain exactly three materialized %s", key)
		}
	}
	return counts, nil
}

// SQLite checks only the authoritative counting query and rejection contract.
// It is not the required hosted MySQL/browser/runtime acceptance.
func TestContainerTerminalRerunTargetCounts(t *testing.T) {
	for _, scenario := range []string{"exact", "extra-intent", "changed-request", "changed-owner", "missing-run", "extra-run", "missing-job", "extra-job", "missing-gate", "missing-schedule", "changed-run-id", "changed-job-id", "changed-gate-run", "changed-schedule-job", "cancelled-run", "cancelled-job", "cancelled-gate", "cancelled-schedule", "duplicate-auxiliary"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "target-counts.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			pool, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = pool.Close() })
			if err := db.WithContext(ctx).AutoMigrate(&model.CopilotRerunIntent{}, &model.PipelineRun{}, &model.JobRun{}, &model.CopilotRerunAdmissionGate{}, &model.CopilotRerunSchedule{}); err != nil {
				t.Fatal(err)
			}
			var intents []model.CopilotRerunIntent
			for _, prefix := range []string{"primary", "prepared-cancel", "pending-cancel", "lost-claim"} {
				intent := model.CopilotRerunIntent{IntentID: prefix, OwnerUserID: "owner", ProjectID: "project", PipelineID: "pipeline", OriginalJobRunID: "original-job", RequestID: prefix + "-request", NewRunID: prefix + "-run", NewJobRunID: prefix + "-job", GateID: prefix + "-gate", ScheduleID: prefix + "-schedule"}
				if err := db.WithContext(ctx).Create(&intent).Error; err != nil {
					t.Fatal(err)
				}
				intents = append(intents, intent)
				if prefix == "prepared-cancel" {
					continue
				}
				for _, row := range []any{
					&model.PipelineRun{RunID: intent.NewRunID, RequestID: intent.RequestID, PipelineID: intent.PipelineID, CopilotRerunIntentID: intent.IntentID},
					&model.JobRun{JobRunID: intent.NewJobRunID, PipelineRunID: intent.NewRunID, CopilotRerunIntentID: intent.IntentID},
					&model.CopilotRerunAdmissionGate{GateID: intent.GateID, ScheduleID: intent.ScheduleID, RunID: intent.NewRunID, JobRunID: intent.NewJobRunID},
					&model.CopilotRerunSchedule{ScheduleID: intent.ScheduleID, IntentID: intent.IntentID, RunID: intent.NewRunID, JobRunID: intent.NewJobRunID},
				} {
					if err := db.WithContext(ctx).Create(row).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			primary := controlservice.CopilotRerunIntent{IntentID: intents[0].IntentID, RequestID: intents[0].RequestID, OriginalJobRunID: "original-job", NewRunID: intents[0].NewRunID, NewJobRunID: intents[0].NewJobRunID}
			cancelled := controlservice.CopilotRerunIntent{IntentID: intents[1].IntentID, NewRunID: intents[1].NewRunID, NewJobRunID: intents[1].NewJobRunID}
			auxiliary := map[string]any{"cancelledRunId": intents[2].NewRunID, "cancelledJobRunId": intents[2].NewJobRunID, "lostResponseRunId": intents[3].NewRunID, "lostResponseJobRunId": intents[3].NewJobRunID}
			var mutation *gorm.DB
			switch scenario {
			case "extra-intent":
				mutation = db.WithContext(ctx).Create(&model.CopilotRerunIntent{IntentID: "extra", OwnerUserID: "owner", ProjectID: "project", OriginalJobRunID: "original-job", RequestID: "extra", NewRunID: "extra", NewJobRunID: "extra", GateID: "extra", ScheduleID: "extra"})
			case "changed-request":
				mutation = db.WithContext(ctx).Model(&model.CopilotRerunIntent{}).Where("intent_id = ?", primary.IntentID).Update("request_id", "replaced")
			case "changed-owner":
				mutation = db.WithContext(ctx).Model(&model.CopilotRerunIntent{}).Where("intent_id = ?", primary.IntentID).Update("owner_user_id", "reviewer")
			case "missing-run":
				mutation = db.WithContext(ctx).Where("run_id = ?", primary.NewRunID).Delete(&model.PipelineRun{})
			case "extra-run":
				mutation = db.WithContext(ctx).Create(&model.PipelineRun{RunID: "extra", RequestID: "extra", CopilotRerunIntentID: primary.IntentID})
			case "missing-job":
				mutation = db.WithContext(ctx).Where("job_run_id = ?", primary.NewJobRunID).Delete(&model.JobRun{})
			case "extra-job":
				mutation = db.WithContext(ctx).Create(&model.JobRun{JobRunID: "extra", CopilotRerunIntentID: primary.IntentID})
			case "missing-gate":
				mutation = db.WithContext(ctx).Where("gate_id = ?", intents[0].GateID).Delete(&model.CopilotRerunAdmissionGate{})
			case "missing-schedule":
				mutation = db.WithContext(ctx).Where("schedule_id = ?", intents[0].ScheduleID).Delete(&model.CopilotRerunSchedule{})
			case "changed-run-id":
				mutation = db.WithContext(ctx).Model(&model.PipelineRun{}).Where("run_id = ?", primary.NewRunID).Update("run_id", "wrong-run")
			case "changed-job-id":
				mutation = db.WithContext(ctx).Model(&model.JobRun{}).Where("job_run_id = ?", primary.NewJobRunID).Update("job_run_id", "wrong-job")
			case "changed-gate-run":
				mutation = db.WithContext(ctx).Model(&model.CopilotRerunAdmissionGate{}).Where("gate_id = ?", intents[0].GateID).Update("run_id", "wrong-run")
			case "changed-schedule-job":
				mutation = db.WithContext(ctx).Model(&model.CopilotRerunSchedule{}).Where("schedule_id = ?", intents[0].ScheduleID).Update("job_run_id", "wrong-job")
			case "cancelled-run":
				mutation = db.WithContext(ctx).Create(&model.PipelineRun{RunID: cancelled.NewRunID, RequestID: "unexpected", CopilotRerunIntentID: cancelled.IntentID})
			case "cancelled-job":
				mutation = db.WithContext(ctx).Create(&model.JobRun{JobRunID: cancelled.NewJobRunID, CopilotRerunIntentID: cancelled.IntentID})
			case "cancelled-gate":
				mutation = db.WithContext(ctx).Create(&model.CopilotRerunAdmissionGate{GateID: intents[1].GateID, ScheduleID: intents[1].ScheduleID, RunID: cancelled.NewRunID, JobRunID: cancelled.NewJobRunID})
			case "cancelled-schedule":
				mutation = db.WithContext(ctx).Create(&model.CopilotRerunSchedule{ScheduleID: intents[1].ScheduleID, IntentID: cancelled.IntentID, RunID: cancelled.NewRunID, JobRunID: cancelled.NewJobRunID})
			case "duplicate-auxiliary":
				auxiliary["lostResponseRunId"] = primary.NewRunID
			}
			if mutation != nil && mutation.Error != nil {
				t.Fatal(mutation.Error)
			}
			counts, err := terminalRerunReadTargetCounts(ctx, db, "project", "pipeline", "owner", primary, cancelled, auxiliary)
			if scenario != "exact" {
				if err == nil {
					t.Fatal("inexact durable intent/target count was accepted")
				}
				return
			}
			want := map[string]int64{"intents": 4, "browserRequestIntents": 1, "targetRuns": 3, "targetJobs": 3, "admissionGates": 3, "schedules": 3}
			if err != nil || !reflect.DeepEqual(counts, want) {
				t.Fatalf("exact durable target counts rejected: %v", err)
			}
		})
	}
}
