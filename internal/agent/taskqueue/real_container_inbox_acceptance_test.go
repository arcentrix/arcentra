//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	controlservice "github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	taskqueuepkg "github.com/arcentrix/arcentra/pkg/taskqueue"
	"gorm.io/gorm"
)

// This disposable fixture uses actual primary authorization and durable creation,
// rather than assigning provenance or notification rows directly.
func authorizeContainerInboxFixture(ctx context.Context, view *repo.Repositories, project *model.Project, pipeline *model.Pipeline, actor string) error {
	if project == nil || pipeline == nil || pipeline.ProjectID != project.ProjectID || project.IsEnabled != 1 || project.Status != model.ProjectStatusActive || pipeline.IsEnabled != 1 || pipeline.IsDeleted != 0 {
		return repo.ErrManualTriggerForbidden
	}
	reader, ok := view.User.(repo.IInboxActorReader)
	if !ok {
		return repo.ErrManualTriggerForbidden
	}
	user, err := reader.GetInboxActor(ctx, actor)
	if err != nil {
		return err
	}
	if user == nil || user.UserID != actor || user.IsEnabled != 1 {
		return repo.ErrManualTriggerForbidden
	}
	checker, err := authz.NewLiveAuthorizer(ctx, view)
	if err != nil {
		return err
	}
	for _, action := range []string{"project:read", "pipeline:read", "pipeline:trigger"} {
		scope := authz.ResourceRef{Type: "pipeline", ID: pipeline.PipelineID, ProjectID: project.ProjectID, OrgID: project.OrgID}
		if action == "project:read" {
			scope.Type, scope.ID = "project", project.ProjectID
		}
		allowed, err := checker.Check(ctx, authz.Subject{UserID: actor}, action, scope)
		if err != nil {
			return err
		}
		if !allowed {
			return repo.ErrManualTriggerForbidden
		}
	}
	return ctx.Err()
}

func verifyContainerApprovalInbox(t *testing.T, f *controllerWorkerFixture, services *controlservice.Services, approval *model.ApprovalRequest, requester, reviewer, grant string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 40*time.Second)
	defer cancel()
	svc := controlservice.NewInboxService(f.repos)
	lookup := func() *repo.InboxItem {
		page, err := svc.List(ctx, reviewer, "all", "", 50)
		if err != nil {
			t.Fatal(err)
		}
		if page.ScanIncomplete || page.NextCursor != nil {
			t.Fatal("fixture inbox exceeded complete bounded page")
		}
		var found *repo.InboxItem
		for _, item := range page.List {
			if item.Target != nil && item.Target.ApprovalID == approval.ApprovalID && item.Kind == "approval_requested" {
				if found != nil {
					t.Fatal("duplicate approval notification")
				}
				copy := item
				found = &copy
			}
		}
		return found
	}
	var item *repo.InboxItem
	f.wait("actual pinned approval delivers authorized personal inbox", func() bool {
		tick, stop := context.WithTimeout(ctx, 5*time.Second)
		_, err := svc.ReconcileOnce(tick)
		stop()
		if err != nil {
			t.Fatal(err)
		}
		item = lookup()
		return item != nil
	})
	if item.Status != "pending" || item.ReadAt != nil || item.Target.RunID != approval.PipelineRunID || item.Target.ProjectID != f.projectID || item.Target.PipelineID != f.pipelineID {
		t.Fatal("notification source/recipient binding mismatch")
	}
	if _, err := svc.MarkRead(ctx, requester, item.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("foreign recipient read must fail closed: %v", err)
	}
	marked, err := svc.MarkRead(ctx, reviewer, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	svc = controlservice.NewInboxService(f.repos)
	replay, err := svc.MarkRead(ctx, reviewer, item.ID)
	if err != nil || replay.ID != marked.ID || !replay.ReadAt.Equal(marked.ReadAt) {
		t.Fatal("durable read replay changed after service reconstruction")
	}
	if got := lookup(); got == nil || got.ReadAt == nil || !got.ReadAt.Equal(marked.ReadAt) {
		t.Fatal("read state not durable")
	}
	// Only notification visibility is revoked. The live worker must retain the
	// reviewer's approval authority while observing this same pending approval.
	binding, err := revokeContainerInboxRead(ctx, f.db, grant, reviewer)
	if err != nil {
		t.Fatal(err)
	}
	restored := false
	restore := func() {
		if restored {
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := f.db.WithContext(cleanup).Create(&binding).Error; err != nil {
			t.Error(err)
			return
		}
		restored = true
	}
	defer restore()
	checker, err := authz.NewLiveAuthorizer(ctx, f.repos)
	if err != nil {
		t.Fatal(err)
	}
	for action, expected := range map[string]bool{"approval:read": false, "approval:approve": true, "approval:reject": true, "project:read": true, "pipeline:read": true} {
		scope := authz.ResourceRef{Type: "pipeline", ID: f.pipelineID, ProjectID: f.projectID}
		if action == "project:read" {
			scope.Type, scope.ID = "project", f.projectID
		}
		allowed, err := checker.Check(ctx, authz.Subject{UserID: reviewer}, action, scope)
		if err != nil || allowed != expected {
			t.Fatalf("notification-only revoke changed %s authority: allowed=%v err=%v", action, allowed, err)
		}
	}
	var job model.JobRun
	if approval.RetryJobRunID == nil {
		t.Fatal("missing actual retry job")
	}
	if err := f.db.WithContext(ctx).Where("job_run_id = ?", *approval.RetryJobRunID).First(&job).Error; err != nil {
		t.Fatal(err)
	}
	var generation model.JobRunGeneration
	if err := f.db.WithContext(ctx).Where("generation_id = ?", job.CurrentGenerationID).First(&generation).Error; err != nil {
		t.Fatal(err)
	}
	descriptor, err := taskqueuepkg.ParseJobGeneration(generation.GenerationJSON)
	if err != nil {
		t.Fatal(err)
	}
	observer := f.repos.JobRun.(interface {
		ReconcileJobRetryApproval(context.Context, retryapproval.Request) (*retryapproval.Response, error)
	})
	observed, err := observer.ReconcileJobRetryApproval(ctx, retryapproval.Request{Version: 1, Operation: "observe", Identity: taskqueuepkg.JobInputIdentity{PipelineID: job.PipelineID, PipelineRunID: job.PipelineRunID, JobRunID: job.JobRunID, AgentID: job.AgentID, ClaimID: job.ClaimID, RuntimeBindingDigest: job.RuntimeBindingDigest, SourceBindingDigest: job.SourceBindingDigest}, GenerationID: generation.GenerationID, GenerationBindingDigest: descriptor.BindingDigest, RetryPlanDigest: job.RetryPlanDigest, InputManifestDigest: job.InputManifestDigest})
	if err != nil || observed == nil || observed.State != "pending" || observed.ExecutionAllowed || observed.Admission != nil {
		t.Fatalf("notification-only revoke changed actual observer authority: %v", err)
	}
	if got := lookup(); got != nil {
		t.Fatal("revoked permission exposed approval notification")
	}
	if _, err := svc.MarkRead(ctx, reviewer, item.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("revoked permission read must fail closed: %v", err)
	}
	restore()
	var source model.ApprovalRequest
	if err := f.db.WithContext(ctx).Where("approval_id = ?", approval.ApprovalID).First(&source).Error; err != nil {
		t.Fatal(err)
	}
	if source.Status != model.ApprovalStatusPending {
		t.Fatal("inbox altered authoritative approval state")
	}
	t.Log("personal inbox: actual pinned approval delivered, exact recipient/read replay verified, revoked access hidden, approval unchanged")
}
