// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"sort"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A child mutation can name a different/missing Run as well as an existing Job. Its
// locator is not authority: parent links are checked again under current locks.
// Existing Pipeline identities must precede every Run gap, matching Confirm's
// source prefix even when a destination Run sorts before the source Run.
type ordinaryAttachmentScope struct {
	runIDs    []string
	runs      map[string]*model.PipelineRun
	job       *model.JobRun
	pipelines []string
}

func locateOrdinaryAttachmentScope(ctx context.Context, db *gorm.DB, runs []string, jobID string) (*ordinaryAttachmentScope, error) {
	scope := &ordinaryAttachmentScope{runs: make(map[string]*model.PipelineRun)}
	pipelines := make(map[string]bool)
	read := func() *gorm.DB {
		return db.Session(&gorm.Session{NewDB: true}).WithContext(ctx).Clauses(database.Write())
	}
	for _, id := range runs {
		if id == "" {
			continue
		}
		if _, exists := scope.runs[id]; exists {
			continue
		}
		var rows []model.PipelineRun
		if err := read().Select("run_id", "pipeline_id").Where("run_id = ?", id).Limit(2).Find(&rows).Error; err != nil {
			return nil, err
		}
		if len(rows) > 1 || len(rows) == 1 && rows[0].RunID != id {
			return nil, ErrCopilotRerunConflict
		}
		scope.runIDs = append(scope.runIDs, id)
		scope.runs[id] = nil
		if len(rows) == 1 {
			scope.runs[id] = &rows[0]
			pipelines[rows[0].PipelineID] = true
		}
	}
	if jobID != "" {
		var rows []model.JobRun
		if err := read().Select("job_run_id", "pipeline_run_id", "pipeline_id").Where("job_run_id = ?", jobID).Limit(2).Find(&rows).Error; err != nil {
			return nil, err
		}
		if len(rows) > 1 || len(rows) == 1 && rows[0].JobRunID != jobID {
			return nil, ErrCopilotRerunConflict
		}
		if len(rows) == 1 {
			scope.job = &rows[0]
			pipelines[rows[0].PipelineID] = true
		}
	}
	for id := range pipelines {
		if id != "" {
			scope.pipelines = append(scope.pipelines, id)
		}
	}
	sort.Strings(scope.pipelines)
	sort.Strings(scope.runIDs)
	if !scope.needsPipelinePrefix() {
		// A single Run followed by its own Job (or no existing Job) already
		// has the source Run -> Job order, including legacy nested callers.
		scope.pipelines = nil
	} else if _, nested := db.Statement.ConnPool.(gorm.TxCommitter); nested {
		// The outer caller may already own child locks. It cannot prepend an
		// unproven Pipeline prefix merely by entering a nested transaction.
		return nil, ErrCopilotRerunUnsupported
	}
	return scope, nil
}

func (s *ordinaryAttachmentScope) needsPipelinePrefix() bool {
	if len(s.runIDs) > 1 {
		return true
	}
	if len(s.runIDs) != 1 || s.job == nil {
		return false
	}
	run := s.runs[s.runIDs[0]]
	return run == nil || s.job.PipelineRunID != run.RunID || s.job.PipelineID != run.PipelineID
}

func (s *ordinaryAttachmentScope) lockParents(ctx context.Context, tx *gorm.DB) error {
	for _, id := range s.pipelines {
		var rows []model.Pipeline
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select("pipeline_id").Where("pipeline_id = ?", id).Limit(2).Find(&rows).Error; err != nil {
			return err
		}
		// Legacy links without a Pipeline row retain their existing behavior;
		// aliases and ambiguous identities never authorize a different prefix.
		if len(rows) > 1 || len(rows) == 1 && rows[0].PipelineID != id {
			return ErrCopilotRerunConflict
		}
	}
	for _, id := range s.runIDs {
		current, err := lockOrdinaryRerunParentRow(ctx, tx, id)
		if err != nil {
			return err
		}
		hint := s.runs[id]
		if (hint == nil) != (current == nil) || hint != nil && current != nil && hint.PipelineID != current.PipelineID {
			return ErrCopilotRerunConflict
		}
	}
	return nil
}

func (s *ordinaryAttachmentScope) validateJob(current *model.JobRun) error {
	if (s.job == nil) != (current == nil) || s.job != nil && current != nil && (s.job.JobRunID != current.JobRunID || s.job.PipelineID != current.PipelineID || s.job.PipelineRunID != current.PipelineRunID) {
		return ErrCopilotRerunConflict
	}
	return nil
}

func (s *ordinaryAttachmentScope) lockJob(ctx context.Context, tx *gorm.DB, id string) error {
	var rows []model.JobRun
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select("job_run_id", "pipeline_id", "pipeline_run_id").Where("job_run_id = ?", id).Limit(2).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) > 1 {
		return ErrCopilotRerunConflict
	}
	var current *model.JobRun
	if len(rows) == 1 {
		current = &rows[0]
	}
	return s.validateJob(current)
}
