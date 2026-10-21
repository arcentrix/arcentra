package repo

import (
	"encoding/json"
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func rerunValueFixture(t *testing.T) (*model.PipelineRun, CopilotDefinitionSnapshotInput, time.Time) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	context := retryapproval.RunContext{Version: 1, ProjectID: "project", PipelineID: "pipeline", PipelineRunID: "run", ActorID: "actor", DefinitionCommitSHA: strings.Repeat("a", 40), DefinitionPath: "ci/pipeline.yaml", ResolvedDefinitionDigest: "v1:" + strings.Repeat("b", 64), GatedJobs: []string{"build"}, Policy: retryapproval.Policy{ID: "policy", Candidates: []string{"approver"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 300}}
	context.Policy.Fingerprint, _ = context.Policy.Digest()
	body, e := context.Canonical()
	require.NoError(t, e)
	digest, _ := context.Digest()
	run := &model.PipelineRun{RunID: "run", PipelineID: "pipeline", ProjectID: "project", Status: model.PipelineStatusPending, Env: `{"parameter":"original"}`, TriggeredBy: "actor", DefinitionCommitSha: context.DefinitionCommitSHA, DefinitionPath: context.DefinitionPath, RetryApprovalContext: body, RetryApprovalContextDigest: digest}
	runtime := *repoRuntimeRequest(t)
	runtime.BindingDigest = ""
	variablesDigest, e := CopilotRerunTriggerVariablesDigest(run.Env)
	require.NoError(t, e)
	in := CopilotDefinitionSnapshotInput{ProjectID: "project", PayloadSchemaVersion: 1, RepositoryConfigDigest: "v1:" + strings.Repeat("f", 64), TriggerVariablesDigest: variablesDigest, DefinitionRepository: "https://example.invalid/owner/definitions.git", DefinitionPath: context.DefinitionPath, DefinitionRevision: context.DefinitionCommitSHA, ResolvedSpecDigest: context.ResolvedDefinitionDigest, SecretReferencesDigest: "v1:" + strings.Repeat("c", 64), CipherVersion: 1, ExpiresAt: now.Add(time.Hour), Layout: CopilotRerunLayout{Version: 1, JobName: "build", Runtime: runtime, Retry: &pipelinev1.Retry{Mode: "whole_job", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}}, ApprovalTimeoutSeconds: 300, Steps: []CopilotRerunStepLayout{{Name: "compile", Index: 0, Uses: "shell"}}}}
	return run, in, now
}
func TestCopilotRerunDefinitionSnapshotIsProspectiveAndDetachedFromCheckout(t *testing.T) {
	run, in, now := rerunValueFixture(t)
	snapshot, e := newTestCopilotDefinitionSnapshot(run, in, now)
	require.NoError(t, e)
	require.NoError(t, ValidateCopilotDefinitionSnapshot(snapshot))
	// No JobRun, source checkout SHA or delivered environment exists at capture.
	require.Empty(t, run.CommitSha)
	require.Equal(t, run.RunID, snapshot.RunID)
	require.NoError(t, ValidateCopilotDefinitionSnapshot(snapshot), "caller cannot mutate stored envelope")
	encoded, e := json.Marshal(snapshot)
	require.NoError(t, e)
	require.JSONEq(t, `{}`, string(encoded))
	run.Status = model.PipelineStatusFailed
	_, e = newTestCopilotDefinitionSnapshot(run, in, now)
	require.ErrorIs(t, e, ErrCopilotRerunInvalid)
}
func TestCopilotRerunDefinitionBindingRejectsDrift(t *testing.T) {
	for _, kind := range []string{"credentials", "query", "relative-path", "moving-revision", "runtime-tag", "failed-step", "non-idempotent", "artifact-step", "missing-cipher", "expired", "context", "cipher-tamper", "policy-tamper"} {
		t.Run(kind, func(t *testing.T) {
			run, in, now := rerunValueFixture(t)
			switch kind {
			case "credentials":
				in.DefinitionRepository = "https://user:secret@example.invalid/repo.git"
			case "query":
				in.DefinitionRepository += "?token=secret"
			case "relative-path":
				in.DefinitionPath = "../pipeline.yaml"
			case "moving-revision":
				in.DefinitionRevision = "main"
			case "runtime-tag":
				in.Layout.Runtime.Image = "registry.example/build:latest"
			case "failed-step":
				in.Layout.Retry.Mode = "failed_step"
			case "non-idempotent":
				in.Layout.Retry.Idempotent = false
			case "artifact-step":
				in.Layout.Steps[0].Uses = "builtin:artifacts"
			case "missing-cipher":
				in.CipherVersion = 0
			case "expired":
				in.ExpiresAt = now
			case "context":
				run.RetryApprovalContextDigest = "v1:" + strings.Repeat("d", 64)
			}
			snapshot, e := newTestCopilotDefinitionSnapshot(run, in, now)
			if kind == "cipher-tamper" || kind == "policy-tamper" {
				require.NoError(t, e)
				if kind == "cipher-tamper" {
					snapshot.PayloadCiphertext[0] ^= 1
				} else {
					snapshot.RuntimePolicyDigest = "v1:" + strings.Repeat("d", 64)
				}
				e = ValidateCopilotDefinitionSnapshot(snapshot)
			}
			require.ErrorIs(t, e, ErrCopilotRerunInvalid)
		})
	}
}
func rerunTerminalEvidenceFixture(t *testing.T) (*model.CopilotDefinitionSnapshot, *model.PipelineRun, *model.JobRun, time.Time) {
	t.Helper()
	run, in, now := rerunValueFixture(t)
	snapshot, e := newTestCopilotDefinitionSnapshot(run, in, now)
	require.NoError(t, e)
	runtime := repoRuntimeRequest(t)
	source, e := sourceprovenance.NewBinding("pipeline", "run", "job", "git", "https://example.invalid/owner/source.git", "main")
	require.NoError(t, e)
	payload := &taskqueue.JobRunTaskPayload{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", AgentID: "agent", JobName: "build", Runtime: runtime, Retry: in.Layout.Retry, RetryApprovalRequired: true, RetryApprovalTimeoutSeconds: 300, Steps: []taskqueue.StepPayload{{Name: "compile", StepIndex: 0, StepRunID: "old-step-1", AttemptRunIDs: []string{"old-step-1", "old-step-2"}, Uses: "shell", Args: map[string]any{"run": "exit 9"}}}}
	plan, e := taskqueue.NewJobRetryPlan(payload)
	require.NoError(t, e)
	planBody, e := plan.Canonical()
	require.NoError(t, e)
	pd, e := plan.Digest("pipeline", "run", "job", "agent", runtime.BindingDigest)
	require.NoError(t, e)
	job := &model.JobRun{JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run", JobName: "build", AgentID: "agent", ClaimID: "original-claim", Status: model.JobRunStatusFailed, EndTime: &now, TerminalReportDigest: strings.Repeat("d", 64), RetryApprovalRequired: true, SourceExpectationCaptured: true, SourceType: source.Type, SourceRepository: source.Repository, SourceRef: source.Ref, SourceBindingDigest: source.Digest, SourceResolvedCommit: strings.Repeat("e", 40), SourceProvenanceState: "reported", RuntimeType: runtime.Type, RuntimeImage: runtime.Image, RuntimeCPU: runtime.Resources.CPU, RuntimeMemory: runtime.Resources.Memory, RuntimeBindingDigest: runtime.BindingDigest, RetryPlan: planBody, RetryPlanDigest: pd, InputManifestState: "pinned", Env: `{"old":"MUST_NOT_COPY"}`}
	manifest, e := taskqueue.NewJobInputManifest(job.SourceResolvedCommit, nil, nil)
	require.NoError(t, e)
	job.InputManifest, e = manifest.Canonical()
	require.NoError(t, e)
	job.InputManifestDigest, e = manifest.Digest(generationIdentity(job))
	require.NoError(t, e)
	run.Status = model.PipelineStatusFailed
	run.EndTime = &now
	run.TotalJobs = 1
	return snapshot, run, job, now
}
func TestCopilotRerunCombinesLaterVerifiedSourceWithoutMutatingDefinition(t *testing.T) {
	snapshot, run, job, now := rerunTerminalEvidenceFixture(t)
	before := snapshot.BindingDigest
	evidence, e := BuildCopilotRerunOriginalEvidence(snapshot, run, job, 1, now)
	require.NoError(t, e)
	require.Equal(t, job.SourceResolvedCommit, evidence.SourceCommit)
	_, e = evidence.Digest()
	require.NoError(t, e)
	require.Equal(t, before, snapshot.BindingDigest)
	encoded, e := json.Marshal(evidence)
	require.NoError(t, e)
	require.NotContains(t, string(encoded), "MUST_NOT_COPY")
	require.NotContains(t, string(encoded), "approval")
}
func TestCopilotRerunTerminalEvidenceFailsClosed(t *testing.T) {
	for _, kind := range []string{"multiple-jobs", "success", "run-not-terminal", "unverified-source", "missing-claim", "moved-source", "manifest-drift", "runtime-drift", "unapproved-plan", "expired-definition", "path-drift", "missing-terminal-proof"} {
		t.Run(kind, func(t *testing.T) {
			snapshot, run, job, now := rerunTerminalEvidenceFixture(t)
			count := 1
			switch kind {
			case "multiple-jobs":
				count = 2
			case "success":
				job.Status = model.JobRunStatusSuccess
			case "run-not-terminal":
				run.Status = model.PipelineStatusRunning
			case "unverified-source":
				job.SourceProvenanceState = "unknown"
			case "missing-claim":
				job.ClaimID = ""
			case "moved-source":
				job.SourceResolvedCommit = strings.Repeat("f", 40)
			case "manifest-drift":
				job.InputManifestDigest = "v1:" + strings.Repeat("f", 64)
			case "runtime-drift":
				job.RuntimeMemory = "128Mi"
			case "unapproved-plan":
				job.RetryApprovalRequired = false
			case "expired-definition":
				now = snapshot.ExpiresAt
			case "path-drift":
				run.DefinitionPath = "other.yaml"
			case "missing-terminal-proof":
				job.TerminalReportDigest = ""
			}
			_, e := BuildCopilotRerunOriginalEvidence(snapshot, run, job, count, now)
			require.ErrorIs(t, e, ErrCopilotRerunInvalid)
		})
	}
}
func TestCopilotRerunFreshIdentitiesAndSealLeaseSeparation(t *testing.T) {
	_, in, now := rerunValueFixture(t)
	ids, e := NewCopilotRerunExecutionIdentities(&in.Layout)
	require.NoError(t, e)
	require.NoError(t, ids.Validate(&in.Layout))
	other, e := NewCopilotRerunExecutionIdentities(&in.Layout)
	require.NoError(t, e)
	require.NotEqual(t, ids.RunID, other.RunID)
	require.Len(t, ids.Steps[0].Attempts, 2)
	ids.Steps[0].Attempts[1] = ids.Steps[0].Attempts[0]
	require.Error(t, ids.Validate(&in.Layout))
	expires, lease := now.Add(time.Hour), now.Add(time.Minute)
	cipher := []byte("sealed-current-values-encrypted")
	binding := &CopilotRerunSealBinding{Version: 1, SealID: uuid.NewString(), ScheduleID: uuid.NewString(), RunID: uuid.NewString(), JobRunID: uuid.NewString(), AgentID: "agent", PayloadDigest: "v1:" + strings.Repeat("a", 64), CipherVersion: 1, CiphertextDigest: rerunCipherDigest(cipher), SecretVersionsDigest: "v1:" + strings.Repeat("b", 64), ExpiresAtUnixMilli: expires.UnixMilli()}
	digest, e := binding.Digest()
	require.NoError(t, e)
	schedule := &model.CopilotRerunSchedule{ScheduleID: binding.ScheduleID, RunID: binding.RunID, JobRunID: binding.JobRunID, AgentID: binding.AgentID, SealID: &binding.SealID, SealDigest: digest, PayloadDigest: binding.PayloadDigest, CipherVersion: 1, PayloadCiphertext: cipher, CiphertextDigest: binding.CiphertextDigest, SecretVersionsDigest: binding.SecretVersionsDigest, ExpiresAt: expires, State: "sealed", LeaseOwner: "controller-one", LeaseEpoch: 1, LeaseUntil: &lease}
	require.NoError(t, ValidateCopilotRerunSealedBinding(schedule, binding))
	require.True(t, CopilotRerunControllerLeaseCurrent(schedule, "controller-one", 1, now))
	schedule.LeaseEpoch = 2
	schedule.LeaseOwner = "controller-two"
	require.False(t, CopilotRerunControllerLeaseCurrent(schedule, "controller-one", 1, now))
	require.NoError(t, ValidateCopilotRerunSealedBinding(schedule, binding), "takeover preserves identical in-flight delivery identity")
	schedule.ClaimedID = "authoritative-claim"
	schedule.ClaimedAt = &now
	require.False(t, CopilotRerunControllerLeaseCurrent(schedule, "controller-two", 2, now))
	require.NoError(t, ValidateCopilotRerunSealedBinding(schedule, binding), "integrity-only inspect remains possible after claim")
	schedule.AgentID = "different-agent"
	require.Error(t, ValidateCopilotRerunSealedBinding(schedule, binding))
}

func TestCopilotRerunSnapshotFitsVerifiedManualPreAdmissionScope(t *testing.T) {
	run, in, now := rerunValueFixture(t)
	run.ProjectID = ""
	snapshot, e := newTestCopilotDefinitionSnapshot(run, in, now)
	require.NoError(t, e)
	require.Equal(t, "project", snapshot.ProjectID)
	require.Empty(t, run.ProjectID, "value validation must not rewrite existing manual admission")
	in.ProjectID = "foreign"
	_, e = newTestCopilotDefinitionSnapshot(run, in, now)
	require.ErrorIs(t, e, ErrCopilotRerunInvalid)
}
func TestCopilotRerunBoundsOriginAndCoherentTriggerVariables(t *testing.T) {
	for _, kind := range []string{"fragment", "oversize-repo", "oversize-path", "noncanonical-origin", "wrong-config-digest", "schema", "mutable-variables", "variables-null", "unknown-layout-field", "too-many-steps", "short-digest"} {
		t.Run(kind, func(t *testing.T) {
			run, in, now := rerunValueFixture(t)
			switch kind {
			case "fragment":
				in.DefinitionRepository += "#main"
			case "oversize-repo":
				in.DefinitionRepository = "https://example.invalid/" + strings.Repeat("a", 2048)
			case "oversize-path":
				in.DefinitionPath = strings.Repeat("a", 1025)
			case "noncanonical-origin":
				in.DefinitionRepository = "https://EXAMPLE.invalid/repo.git"
			case "wrong-config-digest":
				in.RepositoryConfigDigest = "not-a-digest"
			case "schema":
				in.PayloadSchemaVersion = 2
			case "mutable-variables":
				run.Env = `{"parameter":"changed"}`
			case "variables-null":
				run.Env = `null`
			case "too-many-steps":
				in.Layout.Steps = make([]CopilotRerunStepLayout, CopilotRerunMaxSteps+1)
			case "short-digest":
				in.ResolvedSpecDigest = "v1:abc"
			}
			if kind == "unknown-layout-field" {
				body, e := in.Layout.Canonical()
				require.NoError(t, e)
				_, e = ParseCopilotRerunLayout(strings.TrimSuffix(body, "}") + `,"env":{"secret":"value"}}`)
				require.ErrorIs(t, e, ErrCopilotRerunInvalid)
				return
			}
			_, e := newTestCopilotDefinitionSnapshot(run, in, now)
			require.ErrorIs(t, e, ErrCopilotRerunInvalid)
		})
	}
	one, e := CopilotRerunTriggerVariablesDigest(`{"a":"1","b":"2"}`)
	require.NoError(t, e)
	two, e := CopilotRerunTriggerVariablesDigest(`{ "b": "2", "a": "1" }`)
	require.NoError(t, e)
	require.Equal(t, one, two)
	for _, body := range []string{`[]`, `{"key":null}`, `{"key":1}`, strings.Repeat("x", 64*1024+1)} {
		_, e = CopilotRerunTriggerVariablesDigest(body)
		require.Error(t, e)
	}
}
func TestCopilotRerunGateDoesNotReenterFirstClaimAfterReceipt(t *testing.T) {
	now := time.Now()
	g := &model.CopilotRerunAdmissionGate{GateID: uuid.NewString(), ScheduleID: uuid.NewString(), ProjectID: "project", PipelineID: "pipeline", RunID: uuid.NewString(), JobRunID: uuid.NewString(), State: "unclaimed", Version: 1}
	require.NoError(t, ValidateCopilotRerunGate(g))
	require.False(t, CopilotRerunGateReceiptMatches(g, g.RunID, g.JobRunID, "agent", g.SealID, "v1:"+strings.Repeat("a", 64), "claim"))
	g.AgentID = "agent"
	g.SealID = uuid.NewString()
	g.SealDigest = "v1:" + strings.Repeat("a", 64)
	g.ClaimID = uuid.NewString()
	require.Error(t, ValidateCopilotRerunGate(g), "claim without atomic gate transition is invalid")
	g.State = "claimed"
	g.AdmissionNonceDigest = "v1:" + strings.Repeat("c", 64)
	g.ClaimedAt = &now
	g.Version++
	require.NoError(t, ValidateCopilotRerunGate(g))
	require.True(t, CopilotRerunGateReceiptMatches(g, g.RunID, g.JobRunID, "agent", g.SealID, g.SealDigest, g.ClaimID))
	require.False(t, CopilotRerunGateReceiptMatches(g, g.RunID, g.JobRunID, "another-agent", g.SealID, g.SealDigest, g.ClaimID))
	require.False(t, CopilotRerunGateReceiptMatches(g, g.RunID, g.JobRunID, "agent", g.SealID, g.SealDigest, "another-claim"))
	g.State = "closed"
	g.ClosedAt = &now
	require.Error(t, ValidateCopilotRerunGate(g), "closed preclaim gate cannot relabel a claimed Agent")
}

func newTestCopilotDefinitionSnapshot(run *model.PipelineRun, in CopilotDefinitionSnapshotInput, now time.Time) (*model.CopilotDefinitionSnapshot, error) {
	metadata, err := NewCopilotDefinitionSnapshotMetadata(run, in, now)
	if err != nil {
		return nil, err
	}
	return FinalizeCopilotDefinitionSnapshot(metadata, []byte("authenticated-encrypted-template-not-delivered-env"))
}
func TestCopilotRerunDefinitionTwoPhaseContentBinding(t *testing.T) {
	run, in, now := rerunValueFixture(t)
	metadata, err := NewCopilotDefinitionSnapshotMetadata(run, in, now)
	require.NoError(t, err)
	require.NotEmpty(t, metadata.SnapshotID)
	require.Empty(t, metadata.PayloadCiphertext)
	require.Empty(t, metadata.BindingDigest)
	for _, ciphertext := range [][]byte{nil, make([]byte, 15), make([]byte, CopilotRerunMaxDefinitionBytes+1)} {
		_, err = FinalizeCopilotDefinitionSnapshot(metadata, ciphertext)
		require.ErrorIs(t, err, ErrCopilotRerunInvalid)
	}
	callerCipher := []byte("synthetic authenticated ciphertext buffer")
	copied, err := FinalizeCopilotDefinitionSnapshot(metadata, callerCipher)
	require.NoError(t, err)
	callerCipher[0] ^= 1
	require.NoError(t, ValidateCopilotDefinitionSnapshot(copied))

	first, err := FinalizeCopilotDefinitionSnapshot(metadata, []byte("first authenticated synthetic ciphertext"))
	require.NoError(t, err)
	second, err := FinalizeCopilotDefinitionSnapshot(metadata, []byte("second authenticated synthetic ciphertext"))
	require.NoError(t, err)
	require.Equal(t, first.ContentBinding, second.ContentBinding)
	require.NotEqual(t, first.BindingDigest, second.BindingDigest)
	require.Equal(t, metadata.SnapshotID, first.SnapshotID)
	tampered := *metadata
	tampered.ProjectID = "foreign"
	_, err = FinalizeCopilotDefinitionSnapshot(&tampered, []byte("authenticated-encrypted-template-not-delivered-env"))
	require.ErrorIs(t, err, ErrCopilotRerunInvalid)
	_, err = FinalizeCopilotDefinitionSnapshot(first, []byte("authenticated-encrypted-template-not-delivered-env"))
	require.ErrorIs(t, err, ErrCopilotRerunInvalid)
}

func TestCopilotRerunSealTwoPhaseContentBinding(t *testing.T) {
	in := CopilotRerunSealBinding{Version: 1, ScheduleID: uuid.NewString(), RunID: uuid.NewString(), JobRunID: uuid.NewString(), AgentID: "agent", PayloadDigest: "v1:" + strings.Repeat("a", 64), CipherVersion: 1, SecretVersionsDigest: "v1:" + strings.Repeat("b", 64), ExpiresAtUnixMilli: time.Now().Add(time.Hour).UnixMilli()}
	metadata, err := NewCopilotRerunSealMetadata(in)
	require.NoError(t, err)
	content, err := metadata.ContentBinding()
	require.NoError(t, err)
	sealed, err := FinalizeCopilotRerunSeal(metadata, content, []byte("authenticated synthetic ciphertext"))
	require.NoError(t, err)
	require.Equal(t, metadata.SealID, sealed.SealID)
	actual, err := sealed.ContentBinding()
	require.NoError(t, err)
	require.Equal(t, content, actual)
	_, err = sealed.Digest()
	require.NoError(t, err)
	in.SealID = "caller-selected"
	_, err = NewCopilotRerunSealMetadata(in)
	require.ErrorIs(t, err, ErrCopilotRerunInvalid)
	metadata.AgentID = "replacement"
	_, err = FinalizeCopilotRerunSeal(metadata, content, []byte("authenticated synthetic ciphertext"))
	require.ErrorIs(t, err, ErrCopilotRerunInvalid)
}

func TestCopilotRerunGeneratedIdentityCanonicalAndUnique(t *testing.T) {
	_, in, _ := rerunValueFixture(t)
	for _, bad := range []string{"legacy-name", strings.ToUpper("a5ba395f-7948-4cf7-b683-52d6b80292f7"), "a5ba395f79484cf7b68352d6b80292f7", uuid.Nil.String(), "{a5ba395f-7948-4cf7-b683-52d6b80292f7}"} {
		ids, err := NewCopilotRerunExecutionIdentities(&in.Layout)
		require.NoError(t, err)
		ids.RunID = bad
		require.ErrorIs(t, ids.Validate(&in.Layout), ErrCopilotRerunInvalid)
	}
	for _, field := range []string{"job", "schedule", "attempt", "case-alias"} {
		ids, err := NewCopilotRerunExecutionIdentities(&in.Layout)
		require.NoError(t, err)
		switch field {
		case "job":
			ids.JobRunID = ids.RunID
		case "schedule":
			ids.ScheduleID = ids.RunID
		case "attempt":
			ids.Steps[0].Attempts[0] = ids.RunID
		case "case-alias":
			ids.JobRunID = strings.ToUpper(ids.RunID)
		}
		require.ErrorIs(t, ids.Validate(&in.Layout), ErrCopilotRerunInvalid)
	}
}
func TestCopilotRerunSnapshotVersionUUIDRevisionAndEnvelopeBounds(t *testing.T) {
	for _, field := range []string{"uuid-case", "uuid-short", "uuid-zero", "cipher-zero", "cipher-future", "revision-sha256", "revision-uppercase"} {
		t.Run(field, func(t *testing.T) {
			run, in, now := rerunValueFixture(t)
			snapshot, err := newTestCopilotDefinitionSnapshot(run, in, now)
			require.NoError(t, err)
			switch field {
			case "uuid-case":
				snapshot.SnapshotID = strings.ToUpper(snapshot.SnapshotID)
			case "uuid-short":
				snapshot.SnapshotID = "snapshot"
			case "uuid-zero":
				snapshot.SnapshotID = uuid.Nil.String()
			case "cipher-zero":
				snapshot.CipherVersion = 0
			case "cipher-future":
				snapshot.CipherVersion = 2
			case "revision-sha256":
				snapshot.DefinitionRevision = strings.Repeat("a", 64)
			case "revision-uppercase":
				snapshot.DefinitionRevision = strings.Repeat("A", 40)
			}
			snapshot.ContentBinding = CopilotDefinitionContentBinding(snapshot)
			snapshot.BindingDigest = rerunSnapshotDigest(snapshot)
			require.ErrorIs(t, ValidateCopilotDefinitionSnapshot(snapshot), ErrCopilotRerunInvalid)
		})
	}
	run, in, now := rerunValueFixture(t)
	in.CipherVersion = 2
	_, err := NewCopilotDefinitionSnapshotMetadata(run, in, now)
	require.ErrorIs(t, err, ErrCopilotRerunInvalid)
	in.CipherVersion = 1
	metadata, err := NewCopilotDefinitionSnapshotMetadata(run, in, now)
	require.NoError(t, err)
	for n := 0; n < CopilotRerunMinCiphertextBytes; n++ {
		_, err = FinalizeCopilotDefinitionSnapshot(metadata, make([]byte, n))
		require.ErrorIs(t, err, ErrCopilotRerunInvalid)
	}
	_, err = FinalizeCopilotDefinitionSnapshot(metadata, make([]byte, CopilotRerunMinCiphertextBytes))
	require.NoError(t, err)
}
func TestCopilotRerunTerminalContextMustEqualProspectiveContext(t *testing.T) {
	for _, kind := range []string{"body", "digest", "valid-policy-replacement", "whitespace", "actor"} {
		t.Run(kind, func(t *testing.T) {
			snapshot, run, job, now := rerunTerminalEvidenceFixture(t)
			switch kind {
			case "actor":
				run.TriggeredBy = "different-actor"
			case "body":
				run.RetryApprovalContext = "{}"
			case "digest":
				run.RetryApprovalContextDigest = "v1:" + strings.Repeat("e", 64)
			case "whitespace":
				run.RetryApprovalContext += " "
			case "valid-policy-replacement":
				ctx, err := retryapproval.ParseRunContext(run.RetryApprovalContext)
				require.NoError(t, err)
				ctx.Policy.Candidates = []string{"different-approver"}
				ctx.Policy.Fingerprint, err = ctx.Policy.Digest()
				require.NoError(t, err)
				run.RetryApprovalContext, err = ctx.Canonical()
				require.NoError(t, err)
				run.RetryApprovalContextDigest, err = ctx.Digest()
				require.NoError(t, err)
			}
			_, err := BuildCopilotRerunOriginalEvidence(snapshot, run, job, 1, now)
			require.ErrorIs(t, err, ErrCopilotRerunInvalid)
		})
	}
}
func TestCopilotRerunSealRejectsVersionsAliasesAndShortEnvelopes(t *testing.T) {
	valid := CopilotRerunSealBinding{Version: 1, ScheduleID: uuid.NewString(), RunID: uuid.NewString(), JobRunID: uuid.NewString(), AgentID: "legacy-agent", PayloadDigest: "v1:" + strings.Repeat("a", 64), CipherVersion: 1, SecretVersionsDigest: "v1:" + strings.Repeat("b", 64), ExpiresAtUnixMilli: time.Now().Add(time.Hour).UnixMilli()}
	for _, kind := range []string{"future-version", "zero-version", "alias", "same-id", "legacy-target"} {
		in := valid
		switch kind {
		case "future-version":
			in.CipherVersion = 2
		case "zero-version":
			in.CipherVersion = 0
		case "alias":
			in.JobRunID = strings.ToUpper(in.RunID)
		case "same-id":
			in.JobRunID = in.RunID
		case "legacy-target":
			in.RunID = "old-run"
		}
		_, err := NewCopilotRerunSealMetadata(in)
		require.ErrorIs(t, err, ErrCopilotRerunInvalid)
	}
	metadata, err := NewCopilotRerunSealMetadata(valid)
	require.NoError(t, err)
	content, err := metadata.ContentBinding()
	require.NoError(t, err)
	for n := 0; n < CopilotRerunMinCiphertextBytes; n++ {
		_, err = FinalizeCopilotRerunSeal(metadata, content, make([]byte, n))
		require.ErrorIs(t, err, ErrCopilotRerunInvalid)
	}
	sealed, err := FinalizeCopilotRerunSeal(metadata, content, make([]byte, CopilotRerunMinCiphertextBytes))
	require.NoError(t, err)
	sealed.SealID = strings.ToUpper(sealed.SealID)
	_, err = sealed.Digest()
	require.ErrorIs(t, err, ErrCopilotRerunInvalid)
}
