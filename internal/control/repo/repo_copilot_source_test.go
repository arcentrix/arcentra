// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/gorm"
)

func TestCopilotSourceQueriesEnforceSQLBounds(t *testing.T) {
	ctx, db, r, _ := sourceTestRepository(t)
	jobs, steps := []model.JobRun{}, []model.StepRun{}
	for i := 0; i < 250; i++ {
		jobs = append(jobs, model.JobRun{JobRunID: fmt.Sprintf("job-%03d", i), PipelineRunID: "run", PipelineID: "pipeline"})
		steps = append(steps, model.StepRun{StepRunID: fmt.Sprintf("step-%03d", i), JobRunID: "job", PipelineRunID: "run", PipelineID: "pipeline"})
	}
	if err := db.Create(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&steps).Error; err != nil {
		t.Fatal(err)
	}
	statements := []string{}
	if err := db.Callback().Query().After("gorm:query").Register("source-test-sql", func(tx *gorm.DB) { statements = append(statements, tx.Statement.SQL.String()) }); err != nil {
		t.Fatal(err)
	}
	result, err := r.ListCopilotSourceJobs(ctx, "run")
	if err != nil || len(result) != 101 {
		t.Fatalf("jobs %d %v", len(result), err)
	}
	rows, err := r.ListCopilotSourceSteps(ctx, "run", []string{"job"})
	if err != nil || len(rows) != 201 {
		t.Fatalf("steps %d %v", len(rows), err)
	}
	if len(statements) != 2 || !strings.Contains(statements[0], "LIMIT 101") || !strings.Contains(statements[1], "LIMIT 201") || strings.Contains(statements[0], "SELECT *") || strings.Contains(statements[1], "SELECT *") {
		t.Fatalf("query bounds/projections missing: %v", statements)
	}
	foreign, err := r.ListCopilotSourceSteps(ctx, "other-run", []string{"job"})
	if err != nil || len(foreign) != 0 {
		t.Fatal("foreign run rows returned")
	}
	if _, err := r.ListCopilotSourceSteps(ctx, "run", []string{"1", "2", "3", "4"}); err == nil {
		t.Fatal("unbounded job selection accepted")
	}
}

func TestCopilotPrimaryFactoryFailsClosedWithoutConstructor(t *testing.T) {
	for _, r := range []*Repositories{nil, {}} {
		if _, err := r.CopilotPrimaryRepositories(context.Background()); err == nil {
			t.Fatal("missing primary database accepted")
		}
	}
}

func TestCopilotSecretValueRequiresGlobalUniqueIdentity(t *testing.T) {
	ctx, db, _, _ := sourceTestRepository(t)
	if err := db.AutoMigrate(&model.Secret{}); err != nil {
		t.Fatal(err)
	}
	repository := NewSecretRepo(statusTestDB{db}).(*SecretRepo)
	if err := db.Create(&model.Secret{SecretID: "unique", SecretValue: "ciphertext-a", Scope: "project", ScopeID: "p"}).Error; err != nil {
		t.Fatal(err)
	}
	if value, err := repository.GetCopilotSecretValue(ctx, "unique"); err != nil || value != "ciphertext-a" {
		t.Fatal("unique value unavailable", err)
	}
	if err := db.Create(&model.Secret{SecretID: "unique", SecretValue: "ciphertext-b", Scope: "project", ScopeID: "foreign"}).Error; err != nil {
		t.Fatal(err)
	}
	sql := ""
	if err := db.Callback().Query().After("gorm:query").Register("secret-unique-sql", func(tx *gorm.DB) { sql = tx.Statement.SQL.String() }); err != nil {
		t.Fatal(err)
	}
	if value, err := repository.GetCopilotSecretValue(ctx, "unique"); err == nil || value != "" {
		t.Fatal("foreign duplicate selected a value")
	}
	if !strings.Contains(sql, "LIMIT 2") || strings.Contains(sql, "SELECT *") {
		t.Fatal("unbounded uniqueness read", sql)
	}
	if value, err := repository.GetCopilotSecretValue(ctx, "missing"); err == nil || value != "" {
		t.Fatal("missing value accepted")
	}
}
