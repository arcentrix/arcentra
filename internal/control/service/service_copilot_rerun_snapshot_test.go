// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/validation"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func rerunSnapshotServiceSpec() *pipelinev1.Spec {
	p := pipelineRuntimeSpec()
	p.Jobs = p.Jobs[:1]
	p.Jobs[0].Retry = &pipelinev1.Retry{Mode: "whole_job", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}}
	p.Jobs[0].Approval = &pipelinev1.Approval{Required: true, Type: "manual", Plugin: retryapproval.Plugin}
	p.Jobs[0].Source = &pipelinev1.Source{Type: "git", Repo: "https://fixture.invalid/source.git", Branch: "main"}
	p.Jobs[0].Steps[0].Action = ""
	p.Jobs[0].Steps[0].Env = map[string]string{"STEP": "${{\tsecrets.STEP\t}}"}
	p.Runtime.Env = map[string]string{"RUNTIME": "${secrets.RUNTIME}"}
	p.Variables = map[string]string{"VAR": "${{ secrets.PIPELINE }}"}
	p.Jobs[0].Env = map[string]string{"JOB": "${{ secrets.JOB }}"}
	p.Jobs[0].Steps[0].Args = spec.MapAsStruct(map[string]any{"run": "printf '%s' original-private-command"})
	return p
}
func rerunSnapshotPolicy() taskqueue.OfflineRuntimePolicy {
	p := pipelineRuntimePolicy()
	p.AllowWholeJobRetry = true
	p.AllowWholeJobRetryApproval = true
	return p
}
func rerunSnapshotRun(t *testing.T, p *model.Pipeline, q *model.Project, parsed *pipelinev1.Spec, receipt *copilotDefinitionLoadReceipt) *model.PipelineRun {
	t.Helper()
	digest, err := validation.ResolvedDefinitionDigest(parsed)
	require.NoError(t, err)
	policy := retryapproval.Policy{ID: "policy", Candidates: []string{"reviewer"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 300}
	policy.Fingerprint, err = policy.Digest()
	require.NoError(t, err)
	c := retryapproval.RunContext{Version: 1, ProjectID: q.ProjectID, PipelineID: p.PipelineID, PipelineRunID: "original-run", ActorID: "actor", DefinitionCommitSHA: receipt.revision, DefinitionPath: p.PipelineFilePath, ResolvedDefinitionDigest: digest, GatedJobs: []string{parsed.Jobs[0].Name}, Policy: policy}
	body, err := c.Canonical()
	require.NoError(t, err)
	contextDigest, err := c.Digest()
	require.NoError(t, err)
	env, err := json.Marshal(receipt.triggerVariables)
	require.NoError(t, err)
	return &model.PipelineRun{RunID: c.PipelineRunID, PipelineID: p.PipelineID, ProjectID: q.ProjectID, Branch: p.DefaultBranch, DefinitionCommitSha: receipt.revision, DefinitionPath: p.PipelineFilePath, Status: model.PipelineStatusPending, TriggeredBy: "actor", TotalJobs: 1, Env: string(env), RetryApprovalContext: body, RetryApprovalContextDigest: contextDigest}
}
func rerunSnapshotLoadFixture(t *testing.T) (*PipelineServiceImpl, *model.Pipeline, *model.Project, *copilotDefinitionLoadReceipt, *pipelinev1.Spec, *model.PipelineRun, *SecretService, string) {
	t.Helper()
	_, repositories, calls, _ := pipelineRuntimeFixture(t, rerunSnapshotServiceSpec())
	p, err := repositories.Pipeline.Get(context.Background(), "runtime-pipeline")
	require.NoError(t, err)
	q, err := repositories.Project.Get(context.Background(), "runtime-project")
	require.NoError(t, err)
	svc := &PipelineServiceImpl{offlineRuntimePolicy: rerunSnapshotPolicy()}
	vars := map[string]string{"parameter": "original-trigger-value", "RUN": "${{secrets.RUN}}"}
	receipt, parsed, includes, err := svc.loadDefinitionForRerunCapture(context.Background(), p, q, vars)
	require.NoError(t, err)
	require.False(t, includes)
	vars["parameter"] = "caller-mutated"
	run := rerunSnapshotRun(t, p, q, parsed, receipt)
	t.Setenv("ARCENTRA_SECRET_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x35}, 32)))
	key := NewSecretService(nil)
	return svc, p, q, receipt, parsed, run, key, calls
}
func TestCopilotDefinitionSnapshotActualLoaderRoundTrip(t *testing.T) {
	svc, p, q, receipt, parsed, run, key, calls := rerunSnapshotLoadFixture(t)
	now := time.Now().UTC()
	snapshot, err := buildCopilotDefinitionSnapshot(receipt, p, q, run, parsed, svc.offlineRuntimePolicy, key, now)
	require.NoError(t, err)
	require.NoError(t, repo.ValidateCopilotDefinitionSnapshot(snapshot))
	require.NotContains(t, string(snapshot.PayloadCiphertext), "original-private-command")
	require.NotContains(t, string(snapshot.PayloadCiphertext), "original-trigger-value")
	public, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.JSONEq(t, "{}", string(public))
	before, err := os.ReadFile(calls)
	require.NoError(t, err)
	require.Equal(t, "clone\nrev-parse\n", string(before))
	// No loader or template access is required to decrypt the frozen original.
	require.NoError(t, os.Remove(os.Getenv("ARCENTRA_TEST_PIPELINE_FILE")))
	decoded, variables, refs, err := openCopilotDefinitionSnapshot(snapshot, svc.offlineRuntimePolicy, NewSecretService(nil))
	require.NoError(t, err)
	require.True(t, proto.Equal(parsed, decoded))
	require.Equal(t, "original-trigger-value", variables["parameter"])
	require.Equal(t, []string{"JOB", "PIPELINE", "RUN", "RUNTIME", "STEP"}, refs)
	variables["parameter"] = "changed"
	decoded.Jobs[0].Steps[0].Name = "changed"
	again, againVars, _, err := openCopilotDefinitionSnapshot(snapshot, svc.offlineRuntimePolicy, key)
	require.NoError(t, err)
	require.True(t, proto.Equal(parsed, again))
	require.Equal(t, "original-trigger-value", againVars["parameter"])
	after, err := os.ReadFile(calls)
	require.NoError(t, err)
	require.Equal(t, before, after)
}
func TestCopilotDefinitionSnapshotRejectsDriftAndUnsupported(t *testing.T) {
	for _, kind := range []string{"pipeline-repo", "project-repo", "path", "branch", "revision", "project", "run-vars", "parsed", "context", "missing-key", "wrong-key-size", "second-job", "non-idempotent", "failed-step", "artifact", "action-deploy", "condition", "dependency", "local-runtime", "disabled-policy", "no-source", "deployment", "notify", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			svc, p, q, receipt, parsed, run, key, _ := rerunSnapshotLoadFixture(t)
			switch kind {
			case "pipeline-repo":
				p.RepoURL = "https://fixture.invalid/other.git"
			case "project-repo":
				q.RepoURL = "https://fixture.invalid/other.git"
			case "path":
				p.PipelineFilePath = "other.json"
			case "branch":
				p.DefaultBranch = "other"
			case "revision":
				run.DefinitionCommitSha = "HEAD"
			case "project":
				q.ProjectID = "other"
			case "run-vars":
				run.Env = `{"parameter":"changed"}`
			case "parsed":
				parsed.Jobs[0].Steps[0].Args = spec.MapAsStruct(map[string]any{"run": "changed command"})
			case "context":
				run.RetryApprovalContextDigest = "v1:" + strings.Repeat("b", 64)
			case "missing-key":
				key = nil
			case "wrong-key-size":
				key = &SecretService{encryptKey: []byte("short")}
			default:
				switch kind {
				case "second-job":
					parsed.Jobs = append(parsed.Jobs, proto.Clone(parsed.Jobs[0]).(*pipelinev1.Job))
				case "non-idempotent":
					parsed.Jobs[0].Retry.Idempotent = false
				case "failed-step":
					parsed.Jobs[0].Retry.Mode = "failed_step"
				case "artifact":
					parsed.Jobs[0].Steps[0].Uses = "artifact"
				case "action-deploy":
					parsed.Jobs[0].Steps[0].Action = "deploy"
				case "condition":
					parsed.Jobs[0].Steps[0].When = "true"
				case "dependency":
					parsed.Jobs[0].DependsOn = []string{"other"}
				case "local-runtime":
					parsed.Runtime = nil
				case "disabled-policy":
					svc.offlineRuntimePolicy = taskqueue.OfflineRuntimePolicy{}
				case "no-source":
					parsed.Jobs[0].Source = nil
				case "deployment":
					parsed.Jobs[0].Target = &pipelinev1.Target{}
				case "notify":
					parsed.Jobs[0].Notify = &pipelinev1.Notify{}
				case "oversized":
					parsed.Jobs[0].Steps[0].Args = spec.MapAsStruct(map[string]any{"run": strings.Repeat("x", repo.CopilotRerunMaxDefinitionBytes)})
				}
				// Exercise eligibility independently of the immutable-receipt drift fence.
				receipt.resolvedSpec, _ = (proto.MarshalOptions{Deterministic: true}).Marshal(parsed)
				run = rerunSnapshotRun(t, p, q, parsed, receipt)
			}
			snapshot, err := buildCopilotDefinitionSnapshot(receipt, p, q, run, parsed, svc.offlineRuntimePolicy, key, time.Now())
			require.ErrorIs(t, err, errCopilotDefinitionCapture)
			require.Nil(t, snapshot)
			require.NotContains(t, err.Error(), "original-private-command")
		})
	}
}
func TestCopilotDefinitionSnapshotRejectsTamper(t *testing.T) {
	svc, p, q, receipt, parsed, run, key, _ := rerunSnapshotLoadFixture(t)
	snapshot, err := buildCopilotDefinitionSnapshot(receipt, p, q, run, parsed, svc.offlineRuntimePolicy, key, time.Now())
	require.NoError(t, err)
	for _, kind := range []string{"ciphertext", "project", "snapshot", "revision", "refs", "wrong-key"} {
		t.Run(kind, func(t *testing.T) {
			changed := *snapshot
			changed.PayloadCiphertext = bytes.Clone(snapshot.PayloadCiphertext)
			cipherKey := key
			switch kind {
			case "ciphertext":
				changed.PayloadCiphertext[12] ^= 1
			case "project":
				changed.ProjectID = "other"
			case "snapshot":
				changed.SnapshotID = "other"
			case "revision":
				changed.DefinitionRevision = strings.Repeat("b", 40)
			case "refs":
				changed.SecretReferencesDigest = "v1:" + strings.Repeat("b", 64)
			case "wrong-key":
				cipherKey = &SecretService{encryptKey: bytes.Repeat([]byte{0x36}, 32)}
			}
			got, vars, refs, err := openCopilotDefinitionSnapshot(&changed, svc.offlineRuntimePolicy, cipherKey)
			require.ErrorIs(t, err, errCopilotDefinitionCapture)
			require.Nil(t, got)
			require.Nil(t, vars)
			require.Nil(t, refs)
		})
	}
}
func TestCopilotDefinitionLoaderRejectsUnresolvedIncludesAndInvalidSHA(t *testing.T) {
	for _, kind := range []string{"include", "short-sha", "upper-sha", "load-error", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			_, repositories, _, _ := pipelineRuntimeFixture(t, rerunSnapshotServiceSpec())
			p, err := repositories.Pipeline.Get(context.Background(), "runtime-pipeline")
			require.NoError(t, err)
			q, err := repositories.Project.Get(context.Background(), "runtime-project")
			require.NoError(t, err)
			ctx := context.Background()
			switch kind {
			case "include":
				data, err := os.ReadFile(os.Getenv("ARCENTRA_TEST_PIPELINE_FILE"))
				require.NoError(t, err)
				var doc map[string]any
				require.NoError(t, json.Unmarshal(data, &doc))
				doc["include"] = []any{map[string]any{"template": "unresolved"}}
				data, err = json.Marshal(doc)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(os.Getenv("ARCENTRA_TEST_PIPELINE_FILE"), data, 0600))
			case "load-error":
				require.NoError(t, os.Remove(os.Getenv("ARCENTRA_TEST_PIPELINE_FILE")))
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			default:
				git := filepath.Join(filepath.Dir(os.Getenv("ARCENTRA_TEST_PIPELINE_FILE")), "git")
				script, err := os.ReadFile(git)
				require.NoError(t, err)
				sha := "aaaa"
				if kind == "upper-sha" {
					sha = strings.Repeat("A", 40)
				}
				require.NoError(t, os.WriteFile(git, bytes.ReplaceAll(script, []byte(strings.Repeat("a", 40)), []byte(sha)), 0700))
			}
			svc := &PipelineServiceImpl{offlineRuntimePolicy: rerunSnapshotPolicy()}
			receipt, parsed, _, err := svc.loadDefinitionForRerunCapture(ctx, p, q, nil)
			require.ErrorIs(t, err, errCopilotDefinitionCapture)
			require.Nil(t, receipt)
			require.Nil(t, parsed)
		})
	}
}

// Uses the production template resolver and renderer; only its database lookup
// is replaced, so capture proves the resolved include result is frozen once.
type rerunSnapshotTemplateRepository struct {
	repo.IPipelineTemplateRepository
	template *model.PipelineTemplate
	calls    int
}

func (r *rerunSnapshotTemplateRepository) GetLatestTemplateByName(_ context.Context, name, scope, scopeID string) (*model.PipelineTemplate, error) {
	r.calls++
	if name != "frozen" || scope != model.TemplateScopeProject || scopeID != "runtime-project" {
		return nil, errCopilotDefinitionCapture
	}
	return r.template, nil
}
func TestCopilotDefinitionSnapshotFreezesResolvedIncludes(t *testing.T) {
	original := rerunSnapshotServiceSpec()
	_, repositories, calls, _ := pipelineRuntimeFixture(t, original)
	p, err := repositories.Pipeline.Get(context.Background(), "runtime-pipeline")
	require.NoError(t, err)
	q, err := repositories.Project.Get(context.Background(), "runtime-project")
	require.NoError(t, err)
	body, err := os.ReadFile(os.Getenv("ARCENTRA_TEST_PIPELINE_FILE"))
	require.NoError(t, err)
	var document map[string]any
	require.NoError(t, json.Unmarshal(body, &document))
	templateBody, err := json.Marshal(map[string]any{"jobs": document["jobs"]})
	require.NoError(t, err)
	delete(document, "jobs")
	document["include"] = []any{map[string]any{"template": "frozen"}}
	body, err = json.Marshal(document)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(os.Getenv("ARCENTRA_TEST_PIPELINE_FILE"), body, 0600))
	templates := &rerunSnapshotTemplateRepository{template: &model.PipelineTemplate{Name: "frozen", SpecContent: string(templateBody)}}
	svc := &PipelineServiceImpl{offlineRuntimePolicy: rerunSnapshotPolicy(), templateResolver: NewPipelineTemplateService(templates, nil)}
	receipt, parsed, changed, err := svc.loadDefinitionForRerunCapture(context.Background(), p, q, map[string]string{})
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, 1, templates.calls)
	require.NotEqual(t, receipt.originalContentDigest, receipt.resolvedContentDigest)
	require.True(t, proto.Equal(original, parsed))
	templates.template.SpecContent = `{"jobs":[]}`
	require.NoError(t, os.Remove(os.Getenv("ARCENTRA_TEST_PIPELINE_FILE")))
	t.Setenv("ARCENTRA_SECRET_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x35}, 32)))
	key := NewSecretService(nil)
	run := rerunSnapshotRun(t, p, q, parsed, receipt)
	snapshot, err := buildCopilotDefinitionSnapshot(receipt, p, q, run, parsed, svc.offlineRuntimePolicy, key, time.Now())
	require.NoError(t, err)
	decoded, _, _, err := openCopilotDefinitionSnapshot(snapshot, svc.offlineRuntimePolicy, key)
	require.NoError(t, err)
	require.True(t, proto.Equal(original, decoded))
	require.Equal(t, 1, templates.calls)
	gitCalls, err := os.ReadFile(calls)
	require.NoError(t, err)
	require.Equal(t, "clone\nrev-parse\n", string(gitCalls))
}

func TestCopilotDefinitionRejectsUnknownWireFields(t *testing.T) {
	for _, level := range []string{"root", "job", "step", "argument"} {
		t.Run(level, func(t *testing.T) {
			parsed := rerunSnapshotServiceSpec()
			var target proto.Message = parsed
			switch level {
			case "job":
				target = parsed.Jobs[0]
			case "step":
				target = parsed.Jobs[0].Steps[0]
			case "argument":
				target = parsed.Jobs[0].Steps[0].Args.Fields["run"]
			}
			target.ProtoReflect().SetUnknown([]byte{0xf8, 0x7f, 0x01})
			_, err := copilotDefinitionLayout(parsed, rerunSnapshotPolicy(), 300)
			require.ErrorIs(t, err, errCopilotDefinitionCapture)
		})
	}
}

func TestCopilotDefinitionSnapshotRevalidatesAuthenticatedEnvelope(t *testing.T) {
	for _, kind := range []string{"variables", "references", "spec", "identity", "unknown-json", "trailing-json", "disabled-current-policy"} {
		t.Run(kind, func(t *testing.T) {
			svc, p, q, receipt, parsed, run, key, _ := rerunSnapshotLoadFixture(t)
			snapshot, err := buildCopilotDefinitionSnapshot(receipt, p, q, run, parsed, svc.offlineRuntimePolicy, key, time.Now())
			require.NoError(t, err)
			cipher, err := newCopilotRerunCipher(key.encryptKey)
			require.NoError(t, err)
			plain, err := cipher.open(copilotDefinitionCipherBinding(snapshot), snapshot.PayloadCiphertext)
			require.NoError(t, err)
			var e copilotDefinitionEnvelope
			require.NoError(t, json.Unmarshal(plain, &e))
			policy := svc.offlineRuntimePolicy
			switch kind {
			case "variables":
				e.TriggerVariables["new"] = "different"
			case "references":
				e.SecretReferences = []string{"OTHER"}
			case "spec":
				e.ResolvedSpec = []byte{0x01}
			case "identity":
				e.Path = "other.json"
			case "disabled-current-policy":
				policy.AllowWholeJobRetryApproval = false
			}
			plain, err = json.Marshal(e)
			require.NoError(t, err)
			if kind == "unknown-json" {
				plain = append(plain[:len(plain)-1], []byte(`,"unknown":true}`)...)
			}
			if kind == "trailing-json" {
				plain = append(plain, []byte(` {}`)...)
			}
			sealed, err := cipher.seal(copilotDefinitionCipherBinding(snapshot), plain)
			require.NoError(t, err)
			snapshot.BindingDigest = ""
			snapshot.CiphertextDigest = ""
			snapshot.PayloadCiphertext = nil
			snapshot, err = repo.FinalizeCopilotDefinitionSnapshot(snapshot, sealed)
			require.NoError(t, err)
			got, vars, refs, err := openCopilotDefinitionSnapshot(snapshot, policy, key)
			require.ErrorIs(t, err, errCopilotDefinitionCapture)
			require.Nil(t, got)
			require.Nil(t, vars)
			require.Nil(t, refs)
		})
	}
}
