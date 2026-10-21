// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package repo

import (
	"context"
	"errors"
	"strings"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
)

// rejectRunProjectUpdates prevents both column names and Go field names from
// replacing the authority captured by admission. Moving the pipeline itself
// must never change a historical run's project or pipeline identity.
func rejectRunProjectUpdates(updates map[string]any) error {
	for field := range updates {
		if rerunProtectedField(field) {
			return ErrCopilotRerunUnsupported
		}
		switch strings.ReplaceAll(strings.ToLower(field), "_", "") {
		case "inboxactorid", "inboxorigin":
			return errors.New("pipeline run inbox identity is immutable")
		case "projectid", "pipelineid", "runid":
			return errors.New("pipeline run project identity is immutable")
		}
	}
	return nil
}

// GetRunSecretProject reads only the admission-time project from the primary.
// It deliberately never falls back to a mutable pipeline definition. Legacy
// unpinned runs require a fresh admission rather than silently gaining access.
func (r *PipelineRepo) GetRunSecretProject(ctx context.Context, runID, pipelineID string) (string, error) {
	if runID == "" || pipelineID == "" {
		return "", errors.New("pipeline run secret authority is unavailable")
	}
	var rows []model.PipelineRun
	if err := r.Database().WithContext(ctx).Clauses(database.Write()).
		Select("run_id", "pipeline_id", "project_id", "start_time").
		Where("run_id = ?", runID).Limit(2).Find(&rows).Error; err != nil {
		return "", errors.New("pipeline run secret authority is unavailable")
	}
	if len(rows) != 1 || rows[0].RunID != runID || rows[0].PipelineID != pipelineID || rows[0].ProjectID == "" || rows[0].StartTime == nil {
		return "", errors.New("pipeline run secret authority is unavailable")
	}
	return rows[0].ProjectID, nil
}
