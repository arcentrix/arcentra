// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
)

func TestCopilotRerunArtifactSchemaSessionKeepsModelTypes(t *testing.T) {
	for _, collation := range []string{"utf8mb4_0900_ai_ci", "utf8mb4_bin"} {
		t.Run(collation, func(t *testing.T) {
			db := executionSchemaDB(t)
			require.NoError(t, db.AutoMigrate(&model.StepRunArtifact{}, &model.ProjectArtifact{}))
			scoped := rerunSchemaMySQLSession(db, collation)
			options, ok := scoped.Get("gorm:table_options")
			require.True(t, ok)
			require.Equal(t, "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE="+collation, options)
			// Set alone leaves an initialized statement. Reusing it would apply the
			// step artifact schema to the project artifact's differently ordered
			// fields, including binding StorageID's string to the integer size.
			require.NoError(t, scoped.Create(&model.StepRunArtifact{ArtifactID: "step", JobRunID: "step-job", Size: 17}).Error)
			require.NoError(t, scoped.Create(&model.ProjectArtifact{ArtifactID: "project", JobRunID: "project-job", StorageID: "store", Size: 23}).Error)
			for _, entity := range []any{&model.StepRunArtifact{}, &model.ProjectArtifact{}} {
				var count int64
				require.NoError(t, db.Model(entity).Count(&count).Error)
				require.EqualValues(t, 1, count, "each create must target its own artifact table")
			}
			var step model.StepRunArtifact
			require.NoError(t, scoped.Where("artifact_id = ?", "step").First(&step).Error)
			require.Equal(t, "step-job", step.JobRunID)
			require.EqualValues(t, 17, step.Size)
			var project model.ProjectArtifact
			require.NoError(t, scoped.Where("artifact_id = ?", "project").First(&project).Error)
			require.Equal(t, "project-job", project.JobRunID)
			require.Equal(t, "store", project.StorageID)
			require.EqualValues(t, 23, project.Size)
			require.Nil(t, scoped.Statement.Model)
			require.Empty(t, scoped.Statement.Clauses)
		})
	}
}

func TestCopilotRerunArtifactIndexMetadata(t *testing.T) {
	for _, entry := range []struct {
		name, kind string
		prefix     int64
	}{
		{rerunStepArtifactJobIndex, "longtext", 191}, {rerunProjectArtifactJobIndex, "varchar(64)", 0},
	} {
		t.Run(entry.name, func(t *testing.T) {
			column := rerunSchemaColumn{ColumnName: "job_run_id", ColumnType: entry.kind, IsNullable: "YES", CollationName: sql.NullString{String: "utf8mb4_bin", Valid: true}}
			index := rerunSchemaIndex{IndexName: entry.name, NonUnique: 1, SeqInIndex: 1, ColumnName: sql.NullString{String: "job_run_id", Valid: true}, SubPart: sql.NullInt64{Int64: entry.prefix, Valid: entry.prefix > 0}, Collation: sql.NullString{String: "A", Valid: true}, IndexType: "BTREE", IsVisible: "YES"}
			add, err := validateRerunJobAccessShape(column, nil, entry.name, entry.kind, entry.prefix)
			require.NoError(t, err)
			require.True(t, add)
			add, err = validateRerunJobAccessShape(column, []rerunSchemaIndex{index}, entry.name, entry.kind, entry.prefix)
			require.NoError(t, err)
			require.False(t, add)
			for _, mutate := range []func(*rerunSchemaIndex){
				func(i *rerunSchemaIndex) { i.IndexName = "unknown_job_index" }, func(i *rerunSchemaIndex) { i.NonUnique = 0 }, func(i *rerunSchemaIndex) { i.SeqInIndex = 2 }, func(i *rerunSchemaIndex) { i.ColumnName.String = "pipeline_run_id" }, func(i *rerunSchemaIndex) { i.SubPart = sql.NullInt64{Int64: 190, Valid: true} }, func(i *rerunSchemaIndex) { i.IsVisible = "NO" }, func(i *rerunSchemaIndex) { i.Expression = sql.NullString{String: "lower(job_run_id)", Valid: true} },
			} {
				changed := index
				mutate(&changed)
				_, err := validateRerunJobAccessShape(column, []rerunSchemaIndex{changed}, entry.name, entry.kind, entry.prefix)
				require.ErrorIs(t, err, errRerunSchemaShape)
			}
			for _, mutate := range []func(*rerunSchemaColumn){func(c *rerunSchemaColumn) { c.ColumnType = "text" }, func(c *rerunSchemaColumn) { c.IsNullable = "NO" }, func(c *rerunSchemaColumn) { c.ColumnDefault = sql.NullString{Valid: true} }, func(c *rerunSchemaColumn) { c.CollationName.Valid = false }} {
				changed := column
				mutate(&changed)
				_, err := validateRerunJobAccessShape(changed, nil, entry.name, entry.kind, entry.prefix)
				require.ErrorIs(t, err, errRerunSchemaShape)
			}
		})
	}
}

func TestCopilotRerunArtifactIndexUpgradeRecovery(t *testing.T) {
	for mask := 0; mask < 4; mask++ {
		t.Run(string(rune('0'+mask)), func(t *testing.T) {
			db := executionSchemaDB(t)
			rerunSchemaLegacy(t, db)
			longID := strings.Repeat("历史", 200) + "-tail"
			require.NoError(t, db.Create(&model.StepRunArtifact{ArtifactID: "old-step", JobRunID: longID}).Error)
			require.NoError(t, db.Create(&model.ProjectArtifact{ArtifactID: "old-project", JobRunID: "old-job"}).Error)
			if mask&1 != 0 {
				require.NoError(t, db.Migrator().CreateIndex(&model.StepRunArtifact{}, rerunStepArtifactJobIndex))
			}
			if mask&2 != 0 {
				require.NoError(t, db.Migrator().CreateIndex(&model.ProjectArtifact{}, rerunProjectArtifactJobIndex))
			}
			for n := 0; n < 2; n++ {
				require.NoError(t, MigrateSchema(context.Background(), db))
			}
			require.True(t, db.Migrator().HasIndex(&model.StepRunArtifact{}, rerunStepArtifactJobIndex))
			require.True(t, db.Migrator().HasIndex(&model.ProjectArtifact{}, rerunProjectArtifactJobIndex))
			var old model.StepRunArtifact
			require.NoError(t, db.Where("artifact_id = ?", "old-step").First(&old).Error)
			require.Equal(t, longID, old.JobRunID)
			var project model.ProjectArtifact
			require.NoError(t, db.Where("artifact_id = ?", "old-project").First(&project).Error)
			require.Equal(t, "old-job", project.JobRunID)
		})
	}
}

func TestCopilotRerunArtifactBadIndexPreflightIsReadOnly(t *testing.T) {
	for _, table := range []string{"step_run_artifact", "project_artifact"} {
		t.Run(table, func(t *testing.T) {
			db := executionSchemaDB(t)
			rerunSchemaLegacy(t, db)
			name := rerunStepArtifactJobIndex
			if table == "project_artifact" {
				name = rerunProjectArtifactJobIndex
			}
			require.NoError(t, db.Exec("CREATE INDEX "+name+" ON "+table+"(pipeline_run_id)").Error)
			require.ErrorIs(t, MigrateSchema(context.Background(), db), errRerunSchemaShape)
			require.False(t, db.Migrator().HasIndex(&model.StepRun{}, rerunStepJobIndex))
			require.False(t, db.Migrator().HasTable(&model.CopilotRerunIntent{}))
		})
	}
}

func TestCopilotRerunArtifactIndexFresh(t *testing.T) {
	db := executionSchemaDB(t)
	for i := 0; i < 2; i++ {
		require.NoError(t, MigrateSchema(context.Background(), db))
	}
	require.True(t, db.Migrator().HasIndex(&model.StepRunArtifact{}, rerunStepArtifactJobIndex))
	require.True(t, db.Migrator().HasIndex(&model.ProjectArtifact{}, rerunProjectArtifactJobIndex))
}
