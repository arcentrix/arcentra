package repo

import (
	"context"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
)

func executionFixture(t *testing.T) (*generationFixture, *Repositories, CopilotExecutionPrepareInput) {
	t.Helper()
	f := gatedPinFixture(t)
	f.admission(t)
	f.pin(t)
	installRetryApprovalTestAuthority(t, f, nil)
	require.NoError(t, f.db.AutoMigrate(&model.CopilotConversation{}, &model.CopilotTurn{}, &model.CopilotActionPlan{}, &model.CopilotExecutionIntent{}))
	require.NoError(t, f.db.Model(&model.Pipeline{}).Where("pipeline_id = ?", "pipeline").Update("is_enabled", 1).Error)
	require.NoError(t, f.db.Model(&model.Project{}).Where("project_id = ?", "project").Updates(map[string]any{"is_enabled": 1, "status": model.ProjectStatusActive}).Error)
	require.NoError(t, f.db.Model(&model.PipelineRun{}).Where("run_id = ?", "run").Update("project_id", "project").Error)
	r := &Repositories{database: statusTestDB{f.db}}
	c := historyConversation(t, r, CopilotHistoryScope{"voter", "project", "pipeline", "run"}, "execution-conversation")
	turn := historyCompleted(t, r, c, "execution-turn")
	p := &model.CopilotActionPlan{PlanID: "execution-plan", RequestID: "plan-request", ConversationID: c.ConversationID, TurnID: turn.TurnID, OwnerUserID: c.OwnerUserID, ProjectID: c.ProjectID, PipelineID: c.PipelineID, RunID: c.RunID, Kind: "retry_approval_review", State: "reviewed", Version: 2, PlanDigest: "test-plan-digest", SchemaVersion: 1, PayloadPresent: true, PayloadCiphertext: []byte("encrypted-plan"), CipherVersion: 1, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
	require.NoError(t, f.db.Create(p).Error)
	ar, a := retryApprovalTestRow(t, f)
	review, err := ar.ReadRetryApproval(f.ctx, a.ApprovalID)
	require.NoError(t, err)
	return f, r, CopilotExecutionPrepareInput{OwnerUserID: "voter", ProjectID: "project", ConversationID: c.ConversationID, PlanID: p.PlanID, PlanDigest: p.PlanDigest, PlanVersion: p.Version, RequestID: "execution-request", Review: review}
}
func executionPrepare(t *testing.T, r *Repositories, in CopilotExecutionPrepareInput) *model.CopilotExecutionIntent {
	t.Helper()
	v, created, err := r.CopilotExecutionPrepare(context.Background(), in, historyTestAllow)
	require.NoError(t, err)
	require.True(t, created)
	return v
}
func executionMutationInput(v *model.CopilotExecutionIntent) CopilotExecutionMutationInput {
	return CopilotExecutionMutationInput{OwnerUserID: v.OwnerUserID, ProjectID: v.ProjectID, IntentID: v.IntentID, IntentDigest: v.IntentDigest, Version: v.Version}
}
func TestCopilotExecutionSubmissionAtomicAndReplay(t *testing.T) {
	f, r, in := executionFixture(t)
	v := executionPrepare(t, r, in)
	input := executionMutationInput(v)
	again, created, err := r.CopilotExecutionPrepare(f.ctx, in, historyTestAllow)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, v.IntentID, again.IntentID)
	got, err := r.CopilotExecutionConfirm(f.ctx, input, historyTestAllow)
	require.NoError(t, err)
	require.Equal(t, "submitted", got.State)
	var vote model.ApprovalDecision
	require.NoError(t, f.db.Where("decision_id = ?", v.DecisionID).First(&vote).Error)
	require.Equal(t, v.ApprovalID, vote.ApprovalID)
	// Receipt replay ignores current live generation; it cannot create another vote.
	require.NoError(t, f.db.Model(&model.PipelineRun{}).Where("run_id = ?", "run").Update("status", model.PipelineStatusSuccess).Error)
	got, err = r.CopilotExecutionConfirm(f.ctx, input, historyTestAllow)
	require.NoError(t, err)
	require.Equal(t, "submitted", got.State)
	_, err = r.CopilotExecutionCancel(f.ctx, input, historyTestAllow)
	require.ErrorIs(t, err, ErrCopilotHistoryConflict)
	var count int64
	require.NoError(t, f.db.Model(&model.ApprovalDecision{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}
func TestCopilotExecutionCancellationWinsAndDetachedHistory(t *testing.T) {
	f, r, in := executionFixture(t)
	v := executionPrepare(t, r, in)
	require.NoError(t, f.db.Where("conversation_id = ?", in.ConversationID).Delete(&model.CopilotConversation{}).Error)
	require.NoError(t, f.db.Where("plan_id = ?", in.PlanID).Delete(&model.CopilotActionPlan{}).Error)
	got, err := r.CopilotExecutionGet(f.ctx, v.OwnerUserID, v.ProjectID, v.IntentID, historyTestAllow)
	require.NoError(t, err)
	require.Equal(t, v.IntentID, got.IntentID)
	got, err = r.CopilotExecutionCancel(f.ctx, executionMutationInput(v), historyTestAllow)
	require.NoError(t, err)
	require.Equal(t, "cancelled", got.State)
	_, err = r.CopilotExecutionConfirm(f.ctx, executionMutationInput(v), historyTestAllow)
	require.ErrorIs(t, err, ErrCopilotHistoryConflict)
	var count int64
	require.NoError(t, f.db.Model(&model.ApprovalDecision{}).Count(&count).Error)
	require.Zero(t, count)
}
func TestCopilotExecutionRejectsOrdinaryVote(t *testing.T) {
	f, r, in := executionFixture(t)
	v := executionPrepare(t, r, in)
	approveRetryApproval(t, f)
	_, err := r.CopilotExecutionConfirm(f.ctx, executionMutationInput(v), historyTestAllow)
	require.ErrorIs(t, err, ErrCopilotHistoryConflict)
	got, err := r.CopilotExecutionGet(f.ctx, v.OwnerUserID, v.ProjectID, v.IntentID, historyTestAllow)
	require.NoError(t, err)
	require.Equal(t, "prepared", got.State)
}
func TestCopilotExecutionPrepareBindingAndOwner(t *testing.T) {
	for _, kind := range []string{"owner", "version", "digest", "deleted", "cancelled", "expiry", "evidence", "permission"} {
		t.Run(kind, func(t *testing.T) {
			f, r, in := executionFixture(t)
			auth := CopilotHistoryAuthorize(historyTestAllow)
			switch kind {
			case "owner":
				in.OwnerUserID = "actor"
			case "version":
				in.PlanVersion++
			case "digest":
				in.PlanDigest = "changed"
			case "deleted":
				require.NoError(t, f.db.Where("plan_id = ?", in.PlanID).Delete(&model.CopilotActionPlan{}).Error)
			case "cancelled":
				require.NoError(t, f.db.Model(&model.CopilotActionPlan{}).Where("plan_id = ?", in.PlanID).Update("state", "cancelled").Error)
			case "expiry":
				require.NoError(t, f.db.Model(&model.CopilotActionPlan{}).Where("plan_id = ?", in.PlanID).Update("expires_at", time.Now().Add(-time.Minute)).Error)
			case "evidence":
				in.Review.EvidenceHash = "different"
			case "permission":
				auth = func(context.Context, *Repositories, string, *model.Project, string) error {
					return ErrCopilotHistoryForbidden
				}
			}
			_, _, err := r.CopilotExecutionPrepare(f.ctx, in, auth)
			require.Error(t, err)
		})
	}
}
func TestCopilotExecutionConfirmFreshPermissionAndExpiry(t *testing.T) {
	for _, kind := range []string{"permission", "expiry", "wrong-owner", "digest", "version"} {
		t.Run(kind, func(t *testing.T) {
			f, r, in := executionFixture(t)
			v := executionPrepare(t, r, in)
			mutation := executionMutationInput(v)
			switch kind {
			case "permission":
				require.NoError(t, f.db.Model(&model.User{}).Where("user_id = ?", "voter").Update("is_enabled", 0).Error)
			case "expiry":
				v.ExpiresAt = time.Now().Add(-time.Minute)
				v.IntentDigest = executionDigest(v)
				require.NoError(t, f.db.Save(v).Error)
				mutation = executionMutationInput(v)
			case "wrong-owner":
				mutation.OwnerUserID = "actor"
			case "digest":
				mutation.IntentDigest = "changed"
			case "version":
				mutation.Version++
			}
			_, err := r.CopilotExecutionConfirm(f.ctx, mutation, historyTestAllow)
			require.Error(t, err)
			var count int64
			require.NoError(t, f.db.Model(&model.ApprovalDecision{}).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

func TestCopilotExecutionDetachedConfirmAndRecoveryList(t *testing.T) {
	f, r, in := executionFixture(t)
	v := executionPrepare(t, r, in)
	require.NoError(t, f.db.Where("conversation_id = ?", in.ConversationID).Delete(&model.CopilotConversation{}).Error)
	require.NoError(t, f.db.Where("plan_id = ?", in.PlanID).Delete(&model.CopilotActionPlan{}).Error)
	page, err := r.CopilotExecutionList(f.ctx, "voter", "project", 0, 20, historyTestAllow)
	require.NoError(t, err)
	require.Len(t, page.List, 1)
	require.Equal(t, v.IntentID, page.List[0].IntentID)
	got, err := r.CopilotExecutionConfirm(f.ctx, executionMutationInput(v), historyTestAllow)
	require.NoError(t, err)
	require.Equal(t, "submitted", got.State)
	gen, err := r.CopilotExecutionGeneration(f.ctx, "voter", "project", v.IntentID, historyTestAllow)
	require.NoError(t, err)
	require.NotNil(t, gen)
	require.Equal(t, v.JobRunID, gen.JobRunID)
	require.Empty(t, gen.RetryApprovalAdmission)
	// Missing decision linkage must not be represented as a durable receipt.
	require.NoError(t, f.db.Where("decision_id = ?", v.DecisionID).Delete(&model.ApprovalDecision{}).Error)
	_, err = r.CopilotExecutionGet(f.ctx, "voter", "project", v.IntentID, historyTestAllow)
	require.ErrorIs(t, err, ErrCopilotHistoryConflict)
}
func TestCopilotExecutionRequestConflictAndImmutableBinding(t *testing.T) {
	f, r, in := executionFixture(t)
	v := executionPrepare(t, r, in)
	in.Review.EvidenceHash = "invalid"
	_, _, err := r.CopilotExecutionPrepare(f.ctx, in, historyTestAllow)
	require.Error(t, err)
	require.NoError(t, f.db.Model(&model.CopilotExecutionIntent{}).Where("intent_id = ?", v.IntentID).Update("job_run_id", "another-job").Error)
	_, err = r.CopilotExecutionConfirm(f.ctx, executionMutationInput(v), historyTestAllow)
	require.ErrorIs(t, err, ErrCopilotHistoryConflict)
}

func TestCopilotExecutionEmptyListRequiresCurrentAuthorization(t *testing.T) {
	for _, kind := range []string{"valid", "revoked-owner", "foreign-project", "disabled-project", "nil-authorizer"} {
		t.Run(kind, func(t *testing.T) {
			f, r, _ := executionFixture(t)
			authorize := CopilotHistoryAuthorize(historyTestAllow)
			project := "project"
			switch kind {
			case "revoked-owner":
				require.NoError(t, f.db.Model(&model.User{}).Where("user_id = ?", "voter").Update("is_enabled", 0).Error)
			case "foreign-project":
				project = "foreign-project"
				require.NoError(t, f.db.Create(&model.Project{ProjectID: project, IsEnabled: 1, Status: model.ProjectStatusActive}).Error)
				authorize = func(_ context.Context, _ *Repositories, _ string, p *model.Project, _ string) error {
					if p.ProjectID != "project" {
						return ErrCopilotHistoryForbidden
					}
					return nil
				}
			case "disabled-project":
				require.NoError(t, f.db.Model(&model.Project{}).Where("project_id = ?", "project").Update("is_enabled", 0).Error)
			case "nil-authorizer":
				authorize = nil
			}
			page, err := r.CopilotExecutionList(f.ctx, "voter", project, 0, 20, authorize)
			if kind == "valid" {
				require.NoError(t, err)
				require.Empty(t, page.List)
			} else {
				require.ErrorIs(t, err, ErrCopilotHistoryForbidden)
				require.Nil(t, page)
			}
		})
	}
}
