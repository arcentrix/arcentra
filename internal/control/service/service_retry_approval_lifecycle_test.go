// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The production provider has one Services owner per database. A temporary
// browser HTTP facade may configure its local policy, but must not replace the
// owner's database-wide live authorization closure.
func TestRetryApprovalBrowserFacadePolicyLifecycle(t *testing.T) {
	for _, scenario := range []string{"single-owner", "revoke-before-vote", "revoke-before-startup", "reproduce-replaced-owner"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "lifecycle.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			pool, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			repositories := repo.NewRepositories(generationServiceDatabase{db}, nil)
			t.Cleanup(func() { _ = repo.SetRetryApprovalAuthorizer(repositories, nil); _ = pool.Close() })
			if err := db.AutoMigrate(&model.Agent{}, &model.PipelineRun{}, &model.Pipeline{}, &model.Project{}, &model.JobRun{}, &model.StepRun{}, &model.ProjectArtifact{}, &model.ApprovalRequest{}, &model.ApprovalDecision{}, &model.JobRunGeneration{}, &model.User{}, &model.TeamMember{}, &model.Team{}, &model.Permission{}, &model.RolePermissionBinding{}, &model.Role{}, &model.RoleGrant{}); err != nil {
				t.Fatal(err)
			}
			whole := mysqlAuthSeedMode(t, db, "whole-", "project", "pipeline", "whole-actor", "whole-voter", 60, time.Time{}, "whole_job")
			step := mysqlAuthSeedMode(t, db, "step-", "project", "pipeline", "step-actor", "step-voter", 60, time.Time{}, "failed_step")
			// Both immutable runs are prospectively seeded above; select the sole owner
			// once before exercising the lifecycle (seed helpers install their own).
			owner := whole.services
			if err := owner.ConfigureRetryApprovalAuthorization(); err != nil {
				t.Fatal(err)
			}
			observe := func(f *mysqlAuthFixture) (*retryapproval.Response, error) {
				return f.repos.JobRun.(*repo.JobRunRepo).ReconcileJobRetryApproval(ctx, *f.request)
			}
			if r, err := observe(whole); err != nil || r == nil || r.State != "pending" || r.ExecutionAllowed {
				t.Fatalf("whole-job pending: %+v %v", r, err)
			}
			browser := service.NewServices(generationServiceDatabase{db}, nil, repositories)
			oldPolicy := &retryapproval.Policy{ID: "retry", Candidates: []string{"whole-voter"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 60}
			if err := browser.ConfigureRetryApprovalPolicy(oldPolicy); err != nil {
				t.Fatal(err)
			}
			if scenario == "reproduce-replaced-owner" {
				if err := browser.ConfigureRetryApprovalAuthorization(); err != nil {
					t.Fatal(err)
				}
			}
			// Full browser confirmation calls the same repository decision transaction.
			if _, err := browser.Approval.Decide(ctx, whole.approvalID, "whole-voter", model.ApprovalDecisionApprove, ""); err != nil {
				t.Fatal(err)
			}
			if r, err := whole.admit(ctx); err != nil || r == nil || !r.ExecutionAllowed || r.Admission == nil {
				t.Fatalf("whole-job startup: %+v %v", r, err)
			}
			nextPolicy := &retryapproval.Policy{ID: "retry", Candidates: []string{"step-voter"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 60}
			if err := owner.ConfigureRetryApprovalPolicy(nextPolicy); err != nil {
				t.Fatal(err)
			}
			r, err := observe(step)
			if scenario == "reproduce-replaced-owner" {
				if !errors.Is(err, repo.ErrApprovalForbidden) {
					t.Fatalf("stale browser policy should refuse: %+v %v", r, err)
				}
				if _, err := owner.Approval.Decide(ctx, step.approvalID, "step-voter", model.ApprovalDecisionApprove, ""); !errors.Is(err, repo.ErrApprovalForbidden) {
					t.Fatalf("stale browser vote: %v", err)
				}
				return
			}
			if err != nil || r == nil || r.State != "pending" || r.ExecutionAllowed {
				t.Fatalf("failed-step pending after policy switch: %+v %v", r, err)
			}
			revoke := func() {
				if err := db.Model(&model.Team{}).Where("team_id = ?", "step-team").Update("is_enabled", 0).Error; err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "revoke-before-vote" {
				revoke()
			}
			_, err = owner.Approval.Decide(ctx, step.approvalID, "step-voter", model.ApprovalDecisionApprove, "")
			if scenario == "revoke-before-vote" {
				if err == nil {
					t.Fatal("revoked candidate voted")
				}
				if r, err := step.admit(ctx); err == nil && r != nil && r.ExecutionAllowed {
					t.Fatal("unapproved revoked candidate allowed startup")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "revoke-before-startup" {
				revoke()
			}
			r, err = step.admit(ctx)
			if scenario == "revoke-before-startup" {
				if err == nil && r != nil && r.ExecutionAllowed {
					t.Fatal("revoked approval vote allowed startup")
				}
				return
			}
			if err != nil || r == nil || !r.ExecutionAllowed || r.Admission == nil {
				t.Fatalf("failed-step startup: %+v %v", r, err)
			}
		})
	}
}
