package service

import (
	"context"
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"testing"
)

type stepApprovalReviewReader struct {
	repo.IApprovalRepository
	review *retryapproval.Review
	calls  int
}

func (r *stepApprovalReviewReader) ReadRetryApproval(context.Context, string) (*retryapproval.Review, error) {
	r.calls++
	return r.review, nil
}
func TestStepRetryApprovalReviewExactKindRouting(t *testing.T) {
	job := "job"
	for _, kind := range []string{retryapproval.Kind, retryapproval.FailedStepKind} {
		t.Run(kind, func(t *testing.T) {
			mode := "whole_job"
			if kind == retryapproval.FailedStepKind {
				mode = "failed_step"
			}
			plan, err := taskqueue.NewJobRetryPlan(&taskqueue.JobRunTaskPayload{Runtime: &taskqueue.RuntimeRequest{}, RetryApprovalRequired: true, RetryApprovalTimeoutSeconds: 300, Retry: &pipelinev1.Retry{Mode: mode, MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{1}}, Steps: []taskqueue.StepPayload{{Name: "step", StepRunID: "first", AttemptRunIDs: []string{"first", "second"}, Uses: "shell"}}})
			if err != nil {
				t.Fatal(err)
			}
			reader := &stepApprovalReviewReader{review: &retryapproval.Review{ApprovalID: "approval", RetryJobRunID: job, EvidenceHash: "hash", Evidence: &retryapproval.Evidence{Kind: kind, RetryPlan: *plan}}}
			svc := NewApprovalService(reader, nil, nil)
			value := &model.ApprovalRequest{ApprovalID: "approval", RetryJobRunID: &job, EvidenceHash: "hash", BindingKind: kind, Plugin: retryapproval.Plugin}
			if _, err := svc.RetryReview(context.Background(), value); err != nil {
				t.Fatal(err)
			}
			reader.review.Evidence.Kind = "forged"
			if _, err := svc.RetryReview(context.Background(), value); err == nil {
				t.Fatal("substituted review kind accepted")
			}
			value.BindingKind = "unknown"
			calls := reader.calls
			if _, err := svc.RetryReview(context.Background(), value); err == nil || reader.calls != calls {
				t.Fatal("unknown kind reached review")
			}
			value.BindingKind = kind
			release := "release"
			value.DeploymentReleaseID = &release
			if _, err := svc.RetryReview(context.Background(), value); err == nil {
				t.Fatal("dual backlink accepted")
			}
		})
	}
}
