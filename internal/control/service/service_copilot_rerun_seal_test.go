// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type rerunSealFixture struct {
	db       *gorm.DB
	scope    *repo.CopilotRerunScope
	snapshot *model.CopilotDefinitionSnapshot
	evidence *repo.CopilotRerunOriginalEvidence
	ids      *repo.CopilotRerunExecutionIdentities
	key      *SecretService
	policy   *retryapproval.Policy
	intent   *model.CopilotRerunIntent
	agent    *model.Agent
	sealID   string
}

func newRerunSealFixture(t *testing.T, sourceAuth ...*pipelinev1.SourceAuth) *rerunSealFixture {
	t.Helper()
	var auth *pipelinev1.SourceAuth
	if len(sourceAuth) > 0 {
		auth = sourceAuth[0]
	}
	return newRerunSealFixtureWithSpec(t, auth, nil)
}
func newRerunSealFixtureWithSpec(t *testing.T, auth *pipelinev1.SourceAuth, mutate func(*pipelinev1.Spec)) *rerunSealFixture {
	t.Helper()
	db, scope, snapshot, evidence, ids, key, policy := rerunReviewFixtureWithSpec(t, auth, mutate)
	review, err := newCopilotRerunReviewBuilder(policy, rerunSnapshotPolicy(), key)
	require.NoError(t, err)
	var reviewed *repo.CopilotRerunReviewedTarget
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		v, finish := repo.NewScopedAuthorizationView(tx)
		var err error
		reviewed, err = review(context.Background(), v, scope, "actor", snapshot, evidence, ids)
		if err != nil {
			return err
		}
		return finish(context.Background())
	}))
	digest, err := evidence.Digest()
	require.NoError(t, err)
	evidenceJSON, err := json.Marshal(evidence)
	require.NoError(t, err)
	intent := &model.CopilotRerunIntent{IntentID: "intent", OwnerUserID: "actor", ProjectID: snapshot.ProjectID, PipelineID: snapshot.PipelineID, OriginalRunID: snapshot.RunID, OriginalJobRunID: evidence.Identity.JobRunID, SnapshotID: snapshot.SnapshotID, SnapshotDigest: snapshot.BindingDigest, OriginalEvidenceJSON: string(evidenceJSON), OriginalEvidenceDigest: digest, NewRunID: ids.RunID, NewJobRunID: ids.JobRunID, ScheduleID: ids.ScheduleID, GateID: ids.GateID, ReviewedRunContext: reviewed.RunContext, ReviewedRunContextDigest: reviewed.RunContextDigest, ReviewedAdmissionPolicyDigest: reviewed.AdmissionPolicyDigest, ExpectedSourceCommit: evidence.SourceCommit, ExpiresAt: time.Now().Add(time.Minute), State: "confirmed"}
	scope.Run = &model.PipelineRun{RunID: ids.RunID, ProjectID: intent.ProjectID, PipelineID: intent.PipelineID, TriggeredBy: "actor", RetryApprovalContext: reviewed.RunContext, RetryApprovalContextDigest: reviewed.RunContextDigest}
	scope.Job = &model.JobRun{JobRunID: ids.JobRunID, PipelineRunID: ids.RunID, PipelineID: intent.PipelineID}
	now := time.Now()
	caps, _ := json.Marshal(taskqueue.AgentRuntimeCapabilities{ExecutionMode: taskqueue.RuntimeContainerdOfflineV1, ContainerdOfflineV1: true, ContainerdWholeJobRetryV1: true, ContainerdRetryApprovalV1: true, CopilotWholeJobRerunPreparationV1: true})
	agent := &model.Agent{AgentID: "target-agent", OS: "linux", IsEnabled: 1, Status: 1, LastHeartbeat: &now, RuntimeCapabilities: caps}
	identity, err := repo.NewCopilotRerunSealIdentity(repo.CopilotRerunSealBinding{Version: 1, ScheduleID: ids.ScheduleID, RunID: ids.RunID, JobRunID: ids.JobRunID, AgentID: agent.AgentID, CipherVersion: 1, SecretVersionsDigest: repo.CopilotRerunEmptySecretVersionsDigest(), ExpiresAtUnixMilli: intent.ExpiresAt.UnixMilli()})
	require.NoError(t, err)
	return &rerunSealFixture{db, scope, snapshot, evidence, ids, key, policy, intent, agent, identity.SealID}
}
func (f *rerunSealFixture) build(t *testing.T) (*repo.CopilotRerunSealedPayload, error) {
	t.Helper()
	builder, err := newCopilotRerunSealBuilder(f.policy, rerunSnapshotPolicy(), f.key)
	if err != nil {
		return nil, err
	}
	var result *repo.CopilotRerunSealedPayload
	err = f.db.Transaction(func(tx *gorm.DB) error {
		v, finish := repo.NewScopedAuthorizationView(tx)
		var err error
		result, err = builder(context.Background(), v, f.scope, f.snapshot, f.evidence, f.ids, f.intent, f.agent, f.sealID)
		if err != nil {
			return err
		}
		return finish(context.Background())
	})
	return result, err
}
func TestCopilotRerunSealBuilderBindsFrozenPayload(t *testing.T) {
	f := newRerunSealFixture(t)
	result, err := f.build(t)
	require.NoError(t, err)
	require.NotNil(t, result)
	opened, err := openCopilotRerunDispatch(f.intent.ProjectID, f.intent.PipelineID, result.Binding, result.Ciphertext, f.key)
	require.NoError(t, err)
	expectedJSON, err := json.Marshal(result.Payload)
	require.NoError(t, err)
	actualJSON, err := json.Marshal(opened)
	require.NoError(t, err)
	require.Equal(t, string(expectedJSON), string(actualJSON))
	require.NotContains(t, string(result.Ciphertext), "original-private-command")
	require.Equal(t, f.evidence.SourceCommit, opened.SourceExpectation.ExpectedSourceCommit)
	require.Empty(t, opened.Source.Commit)
	require.Equal(t, f.ids.RunID, opened.PipelineRunID)
	require.Equal(t, f.ids.JobRunID, opened.JobRunID)
	require.Equal(t, f.agent.AgentID, opened.AgentID)
	require.Equal(t, f.sealID, opened.SourceExpectation.SealID)
	require.True(t, opened.RetryApprovalRequired)
	for i, step := range opened.Steps {
		require.Equal(t, f.ids.Steps[i].Attempts, step.AttemptRunIDs)
		require.Equal(t, step.AttemptRunIDs[0], step.StepRunID)
	}
	require.NotContains(t, opened.Env, "OLD")
	require.NoError(t, taskqueue.ValidateJobRuntime(opened, rerunSnapshotPolicy()))
	digest, err := repo.CopilotRerunPayloadDigest(opened)
	require.NoError(t, err)
	require.Equal(t, result.Binding.PayloadDigest, digest)
	// No approval, claim, task or queue side effect exists in the builder.
	require.Empty(t, f.scope.Job.ClaimID)
	require.Empty(t, f.scope.Job.InputManifest)
	for _, table := range []string{"approval_request", "approval_decision"} {
		if f.db.Migrator().HasTable(table) {
			var count int64
			require.NoError(t, f.db.Table(table).Count(&count).Error)
			require.Zero(t, count)
		}
	}
	for _, mutate := range []func(*repo.CopilotRerunSealBinding){func(b *repo.CopilotRerunSealBinding) { b.AgentID = "foreign" }, func(b *repo.CopilotRerunSealBinding) { b.PayloadDigest = "v1:" + strings.Repeat("a", 64) }, func(b *repo.CopilotRerunSealBinding) { b.SecretVersionsDigest = "v1:" + strings.Repeat("b", 64) }, func(b *repo.CopilotRerunSealBinding) { b.ExpiresAtUnixMilli++ }} {
		b := *result.Binding
		mutate(&b)
		_, err := openCopilotRerunDispatch(f.intent.ProjectID, f.intent.PipelineID, &b, result.Ciphertext, f.key)
		require.Error(t, err)
	}
	_, err = openCopilotRerunDispatch("foreign", f.intent.PipelineID, result.Binding, result.Ciphertext, f.key)
	require.Error(t, err)
	ciphertext := append([]byte(nil), result.Ciphertext...)
	ciphertext[len(ciphertext)-1] ^= 1
	_, err = openCopilotRerunDispatch(f.intent.ProjectID, f.intent.PipelineID, result.Binding, ciphertext, f.key)
	require.Error(t, err)
}
func TestCopilotRerunSealBuilderRejectsDrift(t *testing.T) {
	for name, mutate := range map[string]func(*rerunSealFixture){"source": func(f *rerunSealFixture) { f.intent.ExpectedSourceCommit = strings.Repeat("a", 40) }, "context": func(f *rerunSealFixture) { f.intent.ReviewedRunContextDigest = "v1:" + strings.Repeat("a", 64) }, "new startup policy": func(f *rerunSealFixture) { f.policy.ID = "new-policy"; f.policy.Fingerprint = "" }, "repository": func(f *rerunSealFixture) { f.scope.Pipeline.RepoURL = "https://changed.invalid/repo.git" }, "ref": func(f *rerunSealFixture) { f.scope.Pipeline.DefaultBranch = "changed" }, "path": func(f *rerunSealFixture) { f.scope.Pipeline.PipelineFilePath = "changed.yaml" }, "project repository": func(f *rerunSealFixture) { f.scope.Project.RepoURL = "https://changed.invalid/project.git" }, "repository type": func(f *rerunSealFixture) { f.scope.Project.RepoType = "changed" }, "policy": func(f *rerunSealFixture) { f.intent.ReviewedAdmissionPolicyDigest = "v1:" + strings.Repeat("a", 64) }, "target": func(f *rerunSealFixture) { f.scope.Job.JobRunID = "other" }, "expired": func(f *rerunSealFixture) { f.intent.ExpiresAt = time.Now().Add(-time.Second) }, "capability": func(f *rerunSealFixture) { f.agent.RuntimeCapabilities = []byte(`{}`) }, "disabled": func(f *rerunSealFixture) { f.agent.IsEnabled = 0 }, "seal": func(f *rerunSealFixture) { f.sealID = "bad" }, "snapshot": func(f *rerunSealFixture) { f.snapshot.PayloadCiphertext[0] ^= 1 }, "identity": func(f *rerunSealFixture) { f.ids.Steps[0].Attempts[0] = f.ids.RunID }} {
		t.Run(name, func(t *testing.T) {
			f := newRerunSealFixture(t)
			mutate(f)
			result, err := f.build(t)
			require.Error(t, err)
			require.Nil(t, result)
		})
	}
	t.Run("current voter revoked", func(t *testing.T) {
		f := newRerunSealFixture(t)
		require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "reviewer-grant").Update("is_enabled", 0).Error)
		result, err := f.build(t)
		require.Error(t, err)
		require.Nil(t, result)
	})
}

func TestCopilotRerunSealAgentSelectors(t *testing.T) {
	f := newRerunSealFixture(t)
	for name, selector := range map[string]*pipelinev1.AgentSelector{"empty": nil, "linux": {MatchLabels: map[string]string{"os": "linux"}}, "missing": {MatchLabels: map[string]string{"missing": "value"}}, "malformed": {MatchExpressions: []*pipelinev1.LabelExpression{nil}}, "unknown operator": {MatchExpressions: []*pipelinev1.LabelExpression{{Key: "os", Operator: "wrong"}}}} {
		t.Run(name, func(t *testing.T) {
			job := &pipelinev1.Job{Steps: []*pipelinev1.Step{{AgentSelector: selector}}}
			require.Equal(t, name == "empty" || name == "linux", copilotRerunSealAgentEligible(f.agent, job))
		})
	}
	f.agent.Labels = []byte(`{"os":5}`)
	require.False(t, copilotRerunSealAgentEligible(f.agent, &pipelinev1.Job{}))
}
func TestCopilotRerunSealRejectsSecretReferenceSnapshot(t *testing.T) {
	f := newRerunSealFixture(t)
	_, _, snapshot, evidence, _, _, _ := rerunReviewFixture(t, true)
	f.snapshot = snapshot
	f.evidence = evidence
	f.intent.SnapshotID = snapshot.SnapshotID
	f.intent.SnapshotDigest = snapshot.BindingDigest
	f.intent.OriginalRunID = snapshot.RunID
	f.intent.OriginalJobRunID = evidence.Identity.JobRunID
	f.intent.ExpectedSourceCommit = evidence.SourceCommit
	f.intent.OriginalEvidenceDigest, _ = evidence.Digest()
	body, _ := json.Marshal(evidence)
	f.intent.OriginalEvidenceJSON = string(body)
	result, err := f.build(t)
	require.ErrorIs(t, err, repo.ErrCopilotRerunUnsupported)
	require.Nil(t, result)
}

func TestCopilotRerunSnapshotRejectsLiteralSourceCredentials(t *testing.T) {
	for name, auth := range map[string]*pipelinev1.SourceAuth{"username": {Username: "literal-user"}, "password": {Password: "literal-password"}, "token": {Token: "literal-token"}, "reference": {Token: "${{ secrets.SCM }}"}} {
		t.Run(name, func(t *testing.T) {
			p := rerunSnapshotServiceSpec()
			p.Jobs[0].Source.Auth = auth
			_, err := copilotDefinitionLayout(p, rerunSnapshotPolicy(), 300)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "literal-")
		})
	}
	p := rerunSnapshotServiceSpec()
	p.Jobs[0].Source.Auth = &pipelinev1.SourceAuth{}
	_, err := copilotDefinitionLayout(p, rerunSnapshotPolicy(), 300)
	require.NoError(t, err)
}

func TestCopilotRerunSealEmptyAuthNormalizesBeforePlan(t *testing.T) {
	for name, auth := range map[string]*pipelinev1.SourceAuth{"nil": nil, "empty object": {}, "empty fields": {Username: "", Password: "", Token: ""}} {
		t.Run(name, func(t *testing.T) {
			f := newRerunSealFixture(t, auth)
			result, err := f.build(t)
			require.NoError(t, err)
			require.Nil(t, result.Payload.Source.Auth)
			require.NoError(t, taskqueue.ValidateJobRetryPlan(result.Payload))
			plain, err := openCopilotRerunDispatch(f.intent.ProjectID, f.intent.PipelineID, result.Binding, result.Ciphertext, f.key)
			require.NoError(t, err)
			require.Nil(t, plain.Source.Auth)
			require.Equal(t, result.Payload.Source.Repo, plain.Source.Repo)
		})
	}
}
func TestCopilotRerunSealDoesNotConsumeDefinitionCredentials(t *testing.T) {
	f := newRerunSealFixture(t)
	f.scope.Project.AuthType = model.AuthTypeToken
	f.scope.Project.Credential = "unused-definition-credential-canary"
	result, err := f.build(t)
	require.NoError(t, err)
	plain, err := openCopilotRerunDispatch(f.intent.ProjectID, f.intent.PipelineID, result.Binding, result.Ciphertext, f.key)
	require.NoError(t, err)
	body, err := json.Marshal(plain)
	require.NoError(t, err)
	require.NotContains(t, string(body), f.scope.Project.Credential)
	require.NotContains(t, string(result.Ciphertext), f.scope.Project.Credential)
	require.Nil(t, plain.Source.Auth)
}
