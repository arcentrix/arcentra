// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type rerunBeginRecorder struct {
	*sql.DB
	options []sql.TxOptions
	denied  bool
}

func (r *rerunBeginRecorder) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	if opts != nil {
		r.options = append(r.options, *opts)
	}
	if r.denied {
		return nil, errors.New("synthetic unsupported transaction option")
	}
	return r.DB.BeginTx(ctx, opts)
}
func TestCopilotRerunAttachmentRequestsScopedRepeatableRead(t *testing.T) {
	ctx, db, _, _ := sourceTestRepository(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	recorder := &rerunBeginRecorder{DB: sqlDB}
	controlled := db.Session(&gorm.Session{NewDB: true}).WithContext(ctx)
	controlled.Statement.ConnPool = recorder
	called := false
	err = rerunAttachmentTransaction(ctx, controlled, func(tx *gorm.DB) error {
		called = true
		require.NoError(t, requireRerunAttachmentTransaction(tx))
		require.NoError(t, lockOrdinaryRerunParent(ctx, tx, "absent"))
		return nil
	})
	require.NoError(t, err)
	require.True(t, called)
	require.Equal(t, []sql.TxOptions{{Isolation: sql.LevelRepeatableRead}}, recorder.options)
	recorder.denied = true
	called = false
	err = rerunAttachmentTransaction(ctx, controlled, func(*gorm.DB) error { called = true; return nil })
	require.Error(t, err)
	require.False(t, called, "unsupported isolation must never fall back")
}
func TestCopilotRerunUnknownNestedTransactionOnlyAllowsExistingParent(t *testing.T) {
	ctx, db, _, _ := sourceTestRepository(t)
	err := db.Transaction(func(tx *gorm.DB) error {
		return rerunAttachmentTransaction(ctx, tx, func(inner *gorm.DB) error {
			require.Error(t, requireRerunAttachmentTransaction(inner))
			require.NoError(t, lockOrdinaryRerunParent(ctx, inner, "run"))
			require.ErrorIs(t, lockOrdinaryRerunParent(ctx, inner, "absent"), ErrCopilotRerunUnsupported)
			return nil
		})
	})
	require.NoError(t, err)
	var proof any
	require.NoError(t, rerunAttachmentTransaction(ctx, db, func(tx *gorm.DB) error {
		proof, _ = tx.Statement.Settings.Load(rerunAttachmentTransactionKey{})
		return rerunAttachmentTransaction(ctx, tx, func(inner *gorm.DB) error {
			return lockOrdinaryRerunParent(ctx, inner, "absent")
		})
	}))
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		tx.Statement.Settings.Store(rerunAttachmentTransactionKey{}, proof)
		require.ErrorIs(t, requireRerunAttachmentTransaction(tx), ErrCopilotRerunUnsupported, "proof cannot migrate to another transaction")
		return nil
	}))
	require.Error(t, requireRerunAttachmentTransaction(db))
}
func TestCopilotRerunNestedLegacyCreationPreservesExistingParentOnly(t *testing.T) {
	ctx, db, _, _ := sourceTestRepository(t)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		jobs := &JobRunRepo{IDatabase: statusTestDB{tx}}
		require.NoError(t, jobs.Create(ctx, &model.JobRun{JobRunID: "nested-existing", PipelineRunID: "run"}))
		require.ErrorIs(t, jobs.Create(ctx, &model.JobRun{JobRunID: "nested-missing", PipelineRunID: "absent"}), ErrCopilotRerunUnsupported)
		return nil
	}))
}

func TestCopilotRerunUnknownNestedCannotAttachAbsentJob(t *testing.T) {
	ctx, db, _, _ := sourceTestRepository(t)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		steps := &StepRunRepo{IDatabase: statusTestDB{tx}}
		require.ErrorIs(t, steps.Create(ctx, &model.StepRun{StepRunID: "missing-job", JobRunID: "absent"}), ErrCopilotRerunUnsupported)
		require.NoError(t, steps.Create(ctx, &model.StepRun{StepRunID: "existing-job", JobRunID: "job", PipelineRunID: "run"}))
		return nil
	}))
}
func TestCopilotRerunGenericStepCannotMoveJobIdentity(t *testing.T) {
	ctx, db, _, _ := sourceTestRepository(t)
	steps := &StepRunRepo{IDatabase: statusTestDB{db}}
	require.NoError(t, steps.Create(ctx, &model.StepRun{StepRunID: "standalone"}))
	for _, field := range []string{"job_run_id", "JobRunID", "JOB_RUN_ID"} {
		require.Error(t, steps.PatchByStepRunID(ctx, "standalone", map[string]any{field: "reserved"}))
	}
}

func TestCopilotRerunStepJobIndexRetainsLongtext(t *testing.T) {
	_, db, _, _ := sourceTestRepository(t)
	require.NoError(t, db.AutoMigrate(&model.StepRunArtifact{}, &model.ProjectArtifact{}))
	for _, entry := range []struct {
		row         any
		kind, index string
		prefix      int
	}{
		{&model.StepRun{}, "longtext", "idx_step_run_job_run", 191},
		{&model.StepRunArtifact{}, "longtext", "idx_step_run_artifact_job_run", 191},
		{&model.ProjectArtifact{}, "varchar(64)", "idx_project_artifact_job_run", 0},
	} {
		statement := &gorm.Statement{DB: db}
		require.NoError(t, statement.Parse(entry.row))
		field := statement.Schema.LookUpField("JobRunID")
		require.Equal(t, entry.kind, field.TagSettings["TYPE"])
		require.False(t, field.NotNull)
		matches := 0
		for _, index := range statement.Schema.ParseIndexes() {
			for _, indexed := range index.Fields {
				if indexed.DBName != "job_run_id" {
					continue
				}
				matches++
				require.Equal(t, entry.index, index.Name)
				require.Empty(t, index.Class, "access path must remain nonunique")
				require.Len(t, index.Fields, 1)
				require.Equal(t, entry.prefix, indexed.Length)
			}
		}
		require.Equal(t, 1, matches, "exactly one reviewed JobRunID access index")
		columns, err := db.Migrator().ColumnTypes(entry.row)
		require.NoError(t, err)
		found := false
		for _, column := range columns {
			if column.Name() == "job_run_id" {
				nullable, known := column.Nullable()
				require.True(t, known)
				require.True(t, nullable)
				found = true
			}
		}
		require.True(t, found)
	}
	// SQLite proves model/value preservation only; MySQL prefix/collation and
	// range-lock behavior remain covered by the required schema016 MySQL gate.
	longID := strings.Repeat("历史", 200) + "-full-tail"
	step := &model.StepRun{StepRunID: "index-history-step", JobRunID: longID}
	retryApprovalMySQLJSONDefaults(step)
	require.NoError(t, db.Create(step).Error)
	artifact := &model.StepRunArtifact{ArtifactID: "index-history-artifact", JobRunID: longID}
	require.NoError(t, db.Create(artifact).Error)
	project := &model.ProjectArtifact{ArtifactID: "index-history-project", JobRunID: strings.Repeat("界", 64)}
	require.NoError(t, db.Create(project).Error)
	var storedStep model.StepRun
	require.NoError(t, db.Where("step_run_id = ?", step.StepRunID).First(&storedStep).Error)
	require.Equal(t, longID, storedStep.JobRunID)
	var storedArtifact model.StepRunArtifact
	require.NoError(t, db.Where("artifact_id = ?", artifact.ArtifactID).First(&storedArtifact).Error)
	require.Equal(t, longID, storedArtifact.JobRunID)
	var storedProject model.ProjectArtifact
	require.NoError(t, db.Where("artifact_id = ?", project.ArtifactID).First(&storedProject).Error)
	require.Equal(t, project.JobRunID, storedProject.JobRunID)
}
