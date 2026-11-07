// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func rerunReviewFixture(t *testing.T, withReferences ...bool) (*gorm.DB, *repo.CopilotRerunScope, *model.CopilotDefinitionSnapshot, *repo.CopilotRerunOriginalEvidence, *repo.CopilotRerunExecutionIdentities, *SecretService, *retryapproval.Policy) {
	return rerunReviewFixtureWithSourceAuth(t, nil, withReferences...)
}
func rerunReviewFixtureWithSourceAuth(t *testing.T, auth *pipelinev1.SourceAuth, withReferences ...bool) (*gorm.DB, *repo.CopilotRerunScope, *model.CopilotDefinitionSnapshot, *repo.CopilotRerunOriginalEvidence, *repo.CopilotRerunExecutionIdentities, *SecretService, *retryapproval.Policy) {
	return rerunReviewFixtureWithSpec(t, auth, nil, withReferences...)
}
func rerunReviewFixtureWithSpec(t *testing.T, auth *pipelinev1.SourceAuth, mutate func(*pipelinev1.Spec), withReferences ...bool) (*gorm.DB, *repo.CopilotRerunScope, *model.CopilotDefinitionSnapshot, *repo.CopilotRerunOriginalEvidence, *repo.CopilotRerunExecutionIdentities, *SecretService, *retryapproval.Policy) {
	t.Helper()
	parsed := rerunSnapshotServiceSpec()
	parsed.Runtime.Env = nil
	parsed.Variables = nil
	parsed.Jobs[0].Env = nil
	parsed.Jobs[0].Source.Auth = auth
	parsed.Jobs[0].Steps[0].Env = nil
	if len(withReferences) > 0 && withReferences[0] {
		parsed.Jobs[0].Steps[0].Env = map[string]string{"TOKEN": "${{ secrets.TOKEN }}"}
	}
	if mutate != nil {
		mutate(parsed)
	}
	database, repositories, _, _ := pipelineRuntimeFixture(t, parsed)
	db := database.Database()
	p, err := repositories.Pipeline.Get(context.Background(), "runtime-pipeline")
	require.NoError(t, err)
	q, err := repositories.Project.Get(context.Background(), "runtime-project")
	require.NoError(t, err)
	svc := &PipelineServiceImpl{offlineRuntimePolicy: rerunSnapshotPolicy()}
	receipt, parsed, _, err := svc.loadDefinitionForRerunCapture(context.Background(), p, q, map[string]string{"original": "value"})
	require.NoError(t, err)
	run := rerunSnapshotRun(t, p, q, parsed, receipt)
	t.Setenv("ARCENTRA_SECRET_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x35}, 32)))
	key := NewSecretService(nil)
	snapshot, err := buildCopilotDefinitionSnapshot(receipt, p, q, run, parsed, svc.offlineRuntimePolicy, key, time.Now())
	require.NoError(t, err)
	layout, err := repo.ParseCopilotRerunLayout(snapshot.LayoutJSON)
	require.NoError(t, err)
	ids, err := repo.NewCopilotRerunExecutionIdentities(layout)
	require.NoError(t, err)
	now := time.Now()
	run.Status = model.PipelineStatusFailed
	run.EndTime = &now
	source, err := sourceprovenance.NewBinding(p.PipelineID, run.RunID, "old-job", "git", parsed.Jobs[0].Source.Repo, parsed.Jobs[0].Source.Branch)
	require.NoError(t, err)
	runtime := layout.Runtime
	runtime.BindingDigest, err = taskqueue.RuntimeBindingDigest(p.PipelineID, run.RunID, "old-job", "agent", &runtime)
	require.NoError(t, err)
	payload := &taskqueue.JobRunTaskPayload{PipelineID: p.PipelineID, PipelineRunID: run.RunID, JobRunID: "old-job", AgentID: "agent", JobName: layout.JobName, Runtime: &runtime, Retry: layout.Retry, RetryApprovalRequired: true, RetryApprovalTimeoutSeconds: 300}
	for _, step := range layout.Steps {
		payload.Steps = append(payload.Steps, taskqueue.StepPayload{Name: step.Name, StepIndex: step.Index, StepRunID: "old-" + step.Name, AttemptRunIDs: []string{"old-" + step.Name, "old-" + step.Name + "-2"}, Uses: step.Uses, Action: step.Action})
	}
	plan, err := taskqueue.NewJobRetryPlan(payload)
	require.NoError(t, err)
	planBody, err := plan.Canonical()
	require.NoError(t, err)
	planDigest, err := plan.Digest(p.PipelineID, run.RunID, "old-job", "agent", runtime.BindingDigest)
	require.NoError(t, err)
	job := &model.JobRun{JobRunID: "old-job", PipelineID: p.PipelineID, PipelineRunID: run.RunID, JobName: layout.JobName, AgentID: "agent", ClaimID: "old-claim", Status: model.JobRunStatusFailed, EndTime: &now, TerminalReportDigest: strings.Repeat("d", 64), RetryApprovalRequired: true, SourceExpectationCaptured: true, SourceType: source.Type, SourceRepository: source.Repository, SourceRef: source.Ref, SourceBindingDigest: source.Digest, SourceResolvedCommit: strings.Repeat("e", 40), SourceProvenanceState: "reported", RuntimeType: runtime.Type, RuntimeImage: runtime.Image, RuntimeCPU: runtime.Resources.CPU, RuntimeMemory: runtime.Resources.Memory, RuntimeBindingDigest: runtime.BindingDigest, RetryPlan: planBody, RetryPlanDigest: planDigest, InputManifestState: "pinned", Env: `{"OLD":"never-copy"}`}
	manifest, err := taskqueue.NewJobInputManifest(job.SourceResolvedCommit, nil, nil)
	require.NoError(t, err)
	job.InputManifest, err = manifest.Canonical()
	require.NoError(t, err)
	identity := taskqueue.JobInputIdentity{PipelineID: p.PipelineID, PipelineRunID: run.RunID, JobRunID: job.JobRunID, AgentID: job.AgentID, ClaimID: job.ClaimID, RuntimeBindingDigest: job.RuntimeBindingDigest, SourceBindingDigest: job.SourceBindingDigest}
	job.InputManifestDigest, err = manifest.Digest(identity)
	require.NoError(t, err)
	evidence, err := repo.BuildCopilotRerunOriginalEvidence(snapshot, run, job, 1, now)
	require.NoError(t, err)
	for _, obj := range []any{&model.User{UserID: "actor", IsEnabled: 1}, &model.Role{RoleID: "deploy-role", Name: "review", IsEnabled: 1}, &model.RoleGrant{GrantID: "grant", SubjectType: "user", SubjectID: "actor", RoleID: "deploy-role", ScopeType: "project", ScopeID: q.ProjectID, IsEnabled: 1}} {
		require.NoError(t, db.Create(obj).Error)
	}
	for _, action := range []string{"project:read", "pipeline:read", "pipeline:trigger", "approval:approve"} {
		var permission model.Permission
		require.NoError(t, db.Where("permission_id = ?", action).First(&permission).Error)
		require.Equal(t, action, permission.ResourceType+":"+permission.Action)
		require.NoError(t, db.Create(&model.RolePermissionBinding{RoleID: "deploy-role", PermissionID: action}).Error)
	}
	require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Update("scope_id", q.ProjectID).Error)
	require.NoError(t, db.Model(&model.Project{}).Where("project_id = ?", q.ProjectID).Updates(map[string]any{"is_enabled": 1, "status": model.ProjectStatusActive}).Error)
	require.NoError(t, db.Model(&model.Pipeline{}).Where("pipeline_id = ?", p.PipelineID).Update("is_enabled", 1).Error)
	for _, obj := range []any{&model.User{UserID: "reviewer", IsEnabled: 1}, &model.RoleGrant{GrantID: "reviewer-grant", SubjectType: "user", SubjectID: "reviewer", RoleID: "deploy-role", ScopeType: "project", ScopeID: q.ProjectID, IsEnabled: 1}} {
		require.NoError(t, db.Create(obj).Error)
	}
	return db, &repo.CopilotRerunScope{Project: q, Pipeline: p, Run: run, Job: job, JobCount: 1}, snapshot, evidence, ids, key, &retryapproval.Policy{ID: "current-policy", Candidates: []string{"actor", "reviewer"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 300}
}
func TestCopilotRerunReviewFreezesFreshContextAndRechecksLiveCandidates(t *testing.T) {
	db, scope, snapshot, evidence, ids, key, policy := rerunReviewFixture(t)
	runtime := rerunSnapshotPolicy()
	builder, err := newCopilotRerunReviewBuilder(policy, runtime, key)
	require.NoError(t, err)
	call := func(b repo.CopilotRerunReviewBuilder) (*repo.CopilotRerunReviewedTarget, error) {
		var result *repo.CopilotRerunReviewedTarget
		err := db.Transaction(func(tx *gorm.DB) error {
			view, current := repo.NewScopedAuthorizationView(tx)
			var err error
			result, err = b(context.Background(), view, scope, "actor", snapshot, evidence, ids)
			if err != nil {
				return err
			}
			return current(context.Background())
		})
		return result, err
	}
	first, err := call(builder)
	require.NoError(t, err)
	c, err := retryapproval.ParseRunContext(first.RunContext)
	require.NoError(t, err)
	require.Equal(t, ids.RunID, c.PipelineRunID)
	require.Equal(t, "actor", c.ActorID)
	require.Equal(t, []string{"reviewer"}, c.Policy.Candidates)
	require.Equal(t, snapshot.DefinitionRevision, c.DefinitionCommitSHA)
	require.Equal(t, evidence.SourceCommit, first.ExpectedSourceCommit)
	require.NotEqual(t, c.DefinitionCommitSHA, first.ExpectedSourceCommit)
	require.NotContains(t, first.RunContext, "never-copy")
	require.NotContains(t, first.RunContext, "original-private-command")
	policy.Candidates[1] = "changed"
	runtime.AllowedImages[0] = "changed"
	key.encryptKey[0] ^= 1
	repeated, err := call(builder)
	require.NoError(t, err)
	require.Equal(t, first, repeated)
	require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "reviewer-grant").Update("is_enabled", 0).Error)
	denied, err := call(builder)
	require.ErrorIs(t, err, repo.ErrCopilotRerunForbidden)
	require.Nil(t, denied)
}

func TestCopilotRerunReviewPolicyChangesAndInvalidEvidence(t *testing.T) {
	for _, kind := range []string{"new-policy", "new-runtime-policy", "timeout", "quorum", "source-commit", "source-repo", "snapshot", "old-job-env", "foreign-owner", "revoked-owner", "alias-ids", "cancelled-context", "references"} {
		t.Run(kind, func(t *testing.T) {
			db, scope, snapshot, evidence, ids, key, policy := rerunReviewFixture(t, kind == "references")
			runtime := rerunSnapshotPolicy()
			builder, err := newCopilotRerunReviewBuilder(policy, runtime, key)
			require.NoError(t, err)
			call := func(b repo.CopilotRerunReviewBuilder, owner string, ctx context.Context) (*repo.CopilotRerunReviewedTarget, error) {
				var out *repo.CopilotRerunReviewedTarget
				err := db.Transaction(func(tx *gorm.DB) error {
					view, current := repo.NewScopedAuthorizationView(tx)
					var err error
					out, err = b(ctx, view, scope, owner, snapshot, evidence, ids)
					if err != nil {
						return err
					}
					return current(ctx)
				})
				return out, err
			}
			if kind == "references" {
				out, err := call(builder, "actor", context.Background())
				require.ErrorIs(t, err, repo.ErrCopilotRerunUnsupported)
				require.Nil(t, out)
				return
			}
			first, err := call(builder, "actor", context.Background())
			require.NoError(t, err)
			owner, ctx := "actor", context.Background()
			switch kind {
			case "new-policy":
				policy.ID = "changed-policy"
			case "new-runtime-policy":
				runtime.MaxCPU = "2"
			case "timeout":
				policy.TimeoutSeconds = 301
			case "quorum":
				policy.RequiredApproverCount = 2
			case "source-commit":
				evidence.SourceCommit = strings.Repeat("f", 40)
			case "source-repo":
				evidence.Source.Repository = "https://other.invalid/repo.git"
			case "snapshot":
				snapshot.PayloadCiphertext[0] ^= 1
			case "old-job-env":
				scope.Job.Env = `{"OLD":"changed-secret-never-copy"}`
			case "foreign-owner":
				owner = "unknown"
			case "revoked-owner":
				require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Update("is_enabled", 0).Error)
			case "alias-ids":
				ids.JobRunID = ids.RunID
			case "cancelled-context":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			fresh, err := newCopilotRerunReviewBuilder(policy, runtime, key)
			require.NoError(t, err)
			out, err := call(fresh, owner, ctx)
			switch kind {
			case "new-policy", "new-runtime-policy":
				require.NoError(t, err)
				require.NotEqual(t, first.AdmissionPolicyDigest, out.AdmissionPolicyDigest)
			case "old-job-env":
				require.NoError(t, err)
				require.Equal(t, first, out)
			case "timeout":
				require.ErrorIs(t, err, repo.ErrCopilotRerunUnsupported)
				require.Nil(t, out)
			default:
				require.Error(t, err)
				require.Nil(t, out)
			}
		})
	}
}

func TestCopilotRerunReviewCandidateSelectionChangesConsent(t *testing.T) {
	db, scope, snapshot, evidence, ids, key, policy := rerunReviewFixture(t)
	for _, obj := range []any{&model.User{UserID: "reviewer-two", IsEnabled: 1}, &model.RoleGrant{GrantID: "second-reviewer-grant", SubjectType: "user", SubjectID: "reviewer-two", RoleID: "deploy-role", ScopeType: "project", ScopeID: scope.Project.ProjectID, IsEnabled: 1}} {
		require.NoError(t, db.Create(obj).Error)
	}
	policy.Candidates = append(policy.Candidates, "reviewer-two")
	builder, err := newCopilotRerunReviewBuilder(policy, rerunSnapshotPolicy(), key)
	require.NoError(t, err)
	call := func() *repo.CopilotRerunReviewedTarget {
		var out *repo.CopilotRerunReviewedTarget
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			view, current := repo.NewScopedAuthorizationView(tx)
			var err error
			out, err = builder(context.Background(), view, scope, "actor", snapshot, evidence, ids)
			if err != nil {
				return err
			}
			return current(context.Background())
		}))
		return out
	}
	before := call()
	require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "reviewer-grant").Update("is_enabled", 0).Error)
	after := call()
	require.NotEqual(t, before.RunContextDigest, after.RunContextDigest)
	require.NotEqual(t, before.AdmissionPolicyDigest, after.AdmissionPolicyDigest)
	oldContext, err := retryapproval.ParseRunContext(before.RunContext)
	require.NoError(t, err)
	newContext, err := retryapproval.ParseRunContext(after.RunContext)
	require.NoError(t, err)
	require.Equal(t, oldContext.Policy.Fingerprint, newContext.Policy.Fingerprint, "configured fingerprint does not disguise effective candidate changes")
	require.Equal(t, oldContext.Policy.RequiredApproverCount, newContext.Policy.RequiredApproverCount)
	require.Equal(t, []string{"reviewer-two"}, newContext.Policy.Candidates)
}
func TestCopilotRerunReviewConstructorFailsClosed(t *testing.T) {
	policy := &retryapproval.Policy{ID: "policy", Candidates: []string{"reviewer"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 300}
	for _, kind := range []string{"missing-key", "wrong-key-size", "missing-policy", "duplicate-candidate", "disabled-runtime", "disabled-approval"} {
		t.Run(kind, func(t *testing.T) {
			p := *policy
			p.Candidates = append([]string(nil), policy.Candidates...)
			runtime := rerunSnapshotPolicy()
			key := &SecretService{encryptKey: bytes.Repeat([]byte{1}, 32)}
			cfg := &p
			switch kind {
			case "missing-key":
				key = nil
			case "wrong-key-size":
				key.encryptKey = []byte{1}
			case "missing-policy":
				cfg = nil
			case "duplicate-candidate":
				p.Candidates = append(p.Candidates, "reviewer")
			case "disabled-runtime":
				runtime.Enabled = false
			case "disabled-approval":
				runtime.AllowWholeJobRetryApproval = false
			}
			builder, err := newCopilotRerunReviewBuilder(cfg, runtime, key)
			require.ErrorIs(t, err, repo.ErrCopilotRerunUnsupported)
			require.Nil(t, builder)
		})
	}
}
