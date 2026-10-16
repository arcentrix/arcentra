// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package repo

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/polling"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type pollingProjectConfiguration struct {
	ProjectID     string
	RepoURL       string
	RepoType      string
	DefaultBranch string
	AuthType      int
	Credential    string
	TriggerMode   int
	IsEnabled     int
	Status        int
}

var pollingProjectConfigurationColumns = []string{
	"project_id", "repo_url", "repo_type", "default_branch", "auth_type", "credential", "trigger_mode", "is_enabled", "status",
}

type pollingPipelineConfiguration struct {
	PipelineID       string
	ProjectID        string
	RepoURL          string
	DefaultBranch    string
	PipelineFilePath string
	IsEnabled        int
	IsDeleted        int
}

var pollingPipelineConfigurationColumns = []string{
	"pipeline_id", "project_id", "repo_url", "default_branch", "pipeline_file_path", "is_enabled", "is_deleted",
}

func pollingConfigurationUpdate(updates map[string]any, project bool) bool {
	// GORM accepts both database columns and Go field names in update maps.
	for key := range updates {
		key = strings.ReplaceAll(strings.ToLower(key), "_", "")
		switch key {
		case "repourl", "defaultbranch", "isenabled":
			return true
		case "repotype", "authtype", "credential", "triggermode", "status":
			if project {
				return true
			}
		case "pipelinefilepath", "projectid", "isdeleted":
			if !project {
				return true
			}
		}
	}
	return false
}

func updateProjectWithPollingInvalidation(ctx context.Context, db *gorm.DB, projectID string, updates map[string]any) error {
	write := func(tx *gorm.DB) error {
		return tx.WithContext(ctx).Model(&model.Project{}).Where("project_id = ?", projectID).Updates(updates).Error
	}
	if !pollingConfigurationUpdate(updates, true) {
		return write(db)
	}
	return db.WithContext(ctx).Clauses(database.Write()).Transaction(func(tx *gorm.DB) error {
		// Legacy SQLite tests can omit polling state. On MySQL, a missing or
		// unreadable polling schema must fail closed until explicit migration;
		// HasTable alone would incorrectly turn metadata errors into permission.
		if tx.Dialector.Name() == "sqlite" && !tx.WithContext(ctx).Migrator().HasTable(&model.SCMPollingStream{}) {
			return write(tx)
		}
		var before pollingProjectConfiguration
		err := tx.WithContext(ctx).Model(&model.Project{}).Clauses(clause.Locking{Strength: "UPDATE"}).
			Select(pollingProjectConfigurationColumns).Where("project_id = ?", projectID).Take(&before).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := write(tx); err != nil {
			return err
		}
		var after pollingProjectConfiguration
		if err := tx.WithContext(ctx).Model(&model.Project{}).Clauses(clause.Locking{Strength: "UPDATE"}).Select(pollingProjectConfigurationColumns).
			Where("project_id = ?", before.ProjectID).Take(&after).Error; err != nil {
			return err
		}
		if before == after {
			return nil
		}
		// A project writer takes only the project -> stream suffix; it must
		// never acquire pipeline locks after holding the project lock.
		return invalidatePollingProjectStreams(ctx, tx, []string{before.ProjectID}, "")
	})
}

func updatePipelineWithPollingInvalidation(ctx context.Context, db *gorm.DB, pipelineID string, updates map[string]any) error {
	write := func(tx *gorm.DB) error {
		return tx.WithContext(ctx).Model(&model.Pipeline{}).Where("pipeline_id = ?", pipelineID).Updates(updates).Error
	}
	if !pollingConfigurationUpdate(updates, false) {
		return write(db)
	}
	return db.WithContext(ctx).Clauses(database.Write()).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "sqlite" && !tx.WithContext(ctx).Migrator().HasTable(&model.SCMPollingStream{}) {
			return write(tx)
		}
		var before pollingPipelineConfiguration
		err := tx.WithContext(ctx).Model(&model.Pipeline{}).Clauses(clause.Locking{Strength: "UPDATE"}).
			Select(pollingPipelineConfigurationColumns).Where("pipeline_id = ?", pipelineID).Take(&before).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := write(tx); err != nil {
			return err
		}
		var after pollingPipelineConfiguration
		if err := tx.WithContext(ctx).Model(&model.Pipeline{}).Clauses(clause.Locking{Strength: "UPDATE"}).Select(pollingPipelineConfigurationColumns).
			Where("pipeline_id = ?", before.PipelineID).Take(&after).Error; err != nil {
			return err
		}
		if before == after {
			return nil
		}
		projectIDs := []string{before.ProjectID, after.ProjectID}
		slices.Sort(projectIDs)
		projectIDs = slices.Compact(projectIDs)
		// Ownership moves invalidate both project selections. Acquire each
		// project in sorted order before acquiring any existing stream row.
		for _, id := range projectIDs {
			var project model.Project
			if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
				Select("project_id").Where("project_id = ?", id).Find(&project).Error; err != nil {
				return err
			}
		}
		return invalidatePollingProjectStreams(ctx, tx, projectIDs, before.PipelineID)
	})
}

// The caller already owns every affected project lock. Read existing IDs first
// and then lock each row in stable order, without locking a new stream gap.
func invalidatePollingProjectStreams(ctx context.Context, tx *gorm.DB, projectIDs []string, selectedPipeline string) error {
	var streamIDs []string
	if err := tx.WithContext(ctx).Model(&model.SCMPollingStream{}).Select("stream_id").
		Where("project_id IN ?", projectIDs).Order("stream_id ASC").Pluck("stream_id", &streamIDs).Error; err != nil {
		return err
	}
	slices.Sort(streamIDs)
	for _, streamID := range streamIDs {
		var stream model.SCMPollingStream
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("stream_id", "project_id", "revision", "activation_generation", "state", "selection_json", "selection_digest").
			Where("stream_id = ?", streamID).Take(&stream).Error; err != nil {
			return err
		}
		if !slices.Contains(projectIDs, stream.ProjectID) {
			return errors.New("polling stream project changed during invalidation")
		}
		if selectedPipeline != "" {
			if stream.SelectionJSON == "" && stream.SelectionDigest == "" && stream.State != polling.StateEnabled {
				continue
			}
			selection, err := polling.ParseSelection(stream.SelectionJSON)
			if err != nil {
				return err
			}
			digest, err := selection.Digest()
			if err != nil || digest != stream.SelectionDigest {
				return polling.ErrConflict
			}
			if _, selected := selection.Find(selectedPipeline); !selected {
				continue
			}
		}
		if stream.Revision == math.MaxUint64 || stream.ActivationGeneration == math.MaxUint64 {
			return errors.New("polling stream generation exhausted")
		}
		result := tx.WithContext(ctx).Model(&model.SCMPollingStream{}).
			Where("stream_id = ? AND revision = ? AND activation_generation = ?", stream.StreamID, stream.Revision, stream.ActivationGeneration).
			Updates(map[string]any{
				"revision": stream.Revision + 1, "activation_generation": stream.ActivationGeneration + 1,
				"state": "paused", "pause_reason": "configuration_changed",
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("polling stream changed during invalidation")
		}
	}
	return nil
}
