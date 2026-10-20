// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"sort"
	"strings"
)

// Existing entry points cannot activate or mutate an inert rerun target. The
// future dedicated gate admission must supply its own reviewed transaction path;
// there is deliberately no caller-controlled bypass flag here.
func emptyRunRerun(run *model.PipelineRun) bool {
	return run != nil && run.CopilotRerunIntentID == "" && run.CopilotRerunGateID == ""
}
func emptyJobRerun(job *model.JobRun) bool {
	return job != nil && job.CopilotRerunIntentID == "" && job.CopilotRerunGateID == "" && job.ExpectedSourceCommit == ""
}
func rerunProtectedField(field string) bool {
	normalized := strings.ReplaceAll(strings.ToLower(field), "_", "")
	return strings.HasPrefix(normalized, "copilotrerun") || normalized == "expectedsourcecommit"
}
func sameJobRerun(a, b *model.JobRun) bool {
	return a.CopilotRerunIntentID == b.CopilotRerunIntentID && a.CopilotRerunGateID == b.CopilotRerunGateID && a.ExpectedSourceCommit == b.ExpectedSourceCommit
}

const ordinaryRunPredicate = "copilot_rerun_intent_id = '' AND copilot_rerun_gate_id = ''"
const ordinaryJobPredicate = "copilot_rerun_intent_id = '' AND copilot_rerun_gate_id = '' AND expected_source_commit = ''"

// Generic child mutations lock only parent Run keys/ranges before child writes.
// They never acquire Pipeline/scope locks afterwards. Missing parents remain
// legacy-compatible, but under MySQL RR their key ranges serialize with future
// protected parent insertion. Confirm must reject existing children after its
// parent insertion; neither half alone proves the attachment boundary.
func lockOrdinaryRerunParent(ctx context.Context, tx *gorm.DB, runID string) error {
	if runID == "" {
		return nil
	}
	var rows []model.PipelineRun
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select("run_id", "copilot_rerun_intent_id", "copilot_rerun_gate_id").Where("run_id = ?", runID).Limit(2).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return requireRerunAttachmentTransaction(tx)
	}
	if len(rows) > 1 || len(rows) == 1 && (rows[0].RunID != runID || !emptyRunRerun(&rows[0])) {
		return ErrCopilotRerunUnsupported
	}
	return nil
}
func rerunUpdateRunDestination(updates map[string]any) (string, error) {
	target := ""
	seen := false
	for field, value := range updates {
		if strings.ReplaceAll(strings.ToLower(field), "_", "") == "pipelinerunid" {
			id, ok := value.(string)
			if !ok {
				return "", gorm.ErrInvalidData
			}
			if seen {
				return "", gorm.ErrInvalidData
			}
			seen = true
			target = id
		}
	}
	return target, nil
}
func ordinaryStepMutation(ctx context.Context, db *gorm.DB, stepID string, updates map[string]any, mutate func(*gorm.DB) error) error {
	target, err := rerunUpdateRunDestination(updates)
	if err != nil {
		return err
	}
	var hint model.StepRun
	err = db.WithContext(ctx).Clauses(database.Write()).Select("step_run_id", "pipeline_run_id", "job_run_id").Where("step_run_id = ?", stepID).First(&hint).Error
	if err != nil {
		return err
	}
	if hint.StepRunID != stepID {
		return ErrCopilotRerunConflict
	}
	return rerunAttachmentTransaction(ctx, db, func(tx *gorm.DB) error {
		parents := []string{hint.PipelineRunID, target}
		sort.Strings(parents)
		previous := ""
		for _, parent := range parents {
			if parent == "" || parent == previous {
				continue
			}
			if err := lockOrdinaryRerunParent(ctx, tx, parent); err != nil {
				return err
			}
			previous = parent
		}
		if hint.JobRunID != "" {
			var parents []model.JobRun
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("job_run_id", "retry_plan", "retry_plan_digest", "copilot_rerun_intent_id", "copilot_rerun_gate_id", "expected_source_commit").Where("job_run_id = ?", hint.JobRunID).Limit(2).Find(&parents).Error; err != nil {
				return err
			}
			if len(parents) == 0 {
				if err := requireRerunAttachmentTransaction(tx); err != nil {
					return err
				}
			}
			if len(parents) > 1 || len(parents) == 1 && (parents[0].JobRunID != hint.JobRunID || !emptyJobRerun(&parents[0]) || parents[0].RetryPlan != "" || parents[0].RetryPlanDigest != "") {
				return ErrCopilotRerunUnsupported
			}
		}
		var current model.StepRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("step_run_id", "pipeline_run_id", "job_run_id").Where("step_run_id = ?", stepID).First(&current).Error; err != nil {
			return err
		}
		if current.StepRunID != stepID || current.PipelineRunID != hint.PipelineRunID || current.JobRunID != hint.JobRunID {
			return ErrCopilotRerunConflict
		}
		if hint.PipelineRunID == "" {
			return mutate(tx.Where("(pipeline_run_id IS NULL OR pipeline_run_id = ?)", ""))
		}
		return mutate(tx.Where("pipeline_run_id = ?", hint.PipelineRunID))
	})
}
