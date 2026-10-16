package retryapproval

import (
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"strings"
	"testing"
)

func fixture(t *testing.T) *Evidence {
	t.Helper()
	p := Policy{ID: "policy", Candidates: []string{"voter"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 300}
	p.Fingerprint, _ = p.Digest()
	c := RunContext{Version: 1, ProjectID: "project", PipelineID: "pipeline", PipelineRunID: "run", ActorID: "actor", DefinitionCommitSHA: strings.Repeat("a", 40), DefinitionPath: "pipeline.yaml", ResolvedDefinitionDigest: "v1:" + strings.Repeat("b", 64), GatedJobs: []string{"build"}, Policy: p}
	r := taskqueue.RuntimeRequest{Type: taskqueue.RuntimeContainerdOfflineV1, Image: "example/image@sha256:" + strings.Repeat("a", 64), Resources: taskqueue.RuntimeResources{CPU: "1", Memory: "128Mi"}}
	r.BindingDigest, _ = taskqueue.RuntimeBindingDigest("pipeline", "run", "job", "agent", &r)
	id := taskqueue.JobInputIdentity{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", AgentID: "agent", ClaimID: "claim", RuntimeBindingDigest: r.BindingDigest}
	payload := &taskqueue.JobRunTaskPayload{RetryApprovalRequired: true, RetryApprovalTimeoutSeconds: 300, PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", AgentID: "agent", JobName: "build", Runtime: &r, Retry: &pipelinev1.Retry{Mode: "whole_job", Idempotent: true, MaxAttempts: 2, AllowedExitCodes: []int32{1}}, Steps: []taskqueue.StepPayload{{Name: "step", StepRunID: "step1", AttemptRunIDs: []string{"step1", "step2"}, Uses: "shell", Args: map[string]any{"script": "SECRET_COMMAND"}, Env: map[string]string{"TOKEN": "SECRET_TOKEN"}}}}
	plan, err := taskqueue.NewJobRetryPlan(payload)
	if err != nil {
		t.Fatal(err)
	}
	pd, _ := plan.Digest("pipeline", "run", "job", "agent", r.BindingDigest)
	m, err := taskqueue.NewJobInputManifest("", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	md, _ := m.Digest(id)
	return &Evidence{Version: 1, Kind: Kind, ApprovalID: "approval", Context: c, Identity: id, JobName: "build", RetryPlan: *plan, RetryPlanDigest: pd, Runtime: r, Inputs: *m, InputManifestDigest: md, Artifacts: []Artifact{}, CreatedAtUnixMilli: 1000, ExpiresAtUnixMilli: 301000}
}
func TestEvidenceCanonicalAndSecretFree(t *testing.T) {
	e := fixture(t)
	s, err := e.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s, "SECRET_") {
		t.Fatal("secret exposed")
	}
	got, err := ParseEvidence(s)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := e.Digest()
	b, _ := got.Digest()
	if a != b {
		t.Fatal("digest changed")
	}
	for _, bad := range []string{" " + s, strings.TrimSuffix(s, "}") + `,"unknown":true}`, strings.TrimSuffix(s, "}") + `,"version":1}`} {
		if _, err := ParseEvidence(bad); err == nil {
			t.Fatal("noncanonical accepted")
		}
	}
}
func TestEvidenceTamperingFailsClosed(t *testing.T) {
	for name, change := range map[string]func(*Evidence){"actor": func(e *Evidence) { e.Context.ActorID = "voter" }, "source": func(e *Evidence) { e.Inputs.SourceCommit = strings.Repeat("c", 40) }, "plan": func(e *Evidence) { e.RetryPlan.Policy.MaxAttempts = 3 }, "claim": func(e *Evidence) { e.Identity.ClaimID = "foreign" }, "runtime": func(e *Evidence) { e.Runtime.Resources.CPU = "2" }, "gates": func(e *Evidence) { e.Context.GatedJobs = []string{"other"} }, "expiry": func(e *Evidence) { e.ExpiresAtUnixMilli++ }, "artifact": func(e *Evidence) { e.Artifacts = nil }, "policy timeout": func(e *Evidence) { e.Context.Policy.TimeoutSeconds = 3601 }} {
		t.Run(name, func(t *testing.T) {
			e := fixture(t)
			change(e)
			if _, err := e.Canonical(); err == nil {
				t.Fatal("tampering accepted")
			}
		})
	}
}
func TestResponsesCannotInventAuthority(t *testing.T) {
	for _, r := range []*Response{{}, {Version: 1, Required: false, State: "approved"}, {Version: 1, Required: false, State: "ungated", ExecutionAllowed: true}, {Version: 1, Required: true, State: "pending"}} {
		if _, err := r.Canonical(); err == nil {
			t.Fatal("incomplete authority accepted")
		}
	}
	r := &Response{Version: 1, State: "ungated"}
	s, err := r.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseResponse(s); err != nil {
		t.Fatal(err)
	}
}
func TestRetryMarkerIsPartOfExecutableDigest(t *testing.T) {
	e := fixture(t)
	if _, err := e.Canonical(); err != nil {
		t.Fatal(err)
	}
	r := e.Runtime
	p := &taskqueue.JobRunTaskPayload{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", AgentID: "agent", Runtime: &r, Retry: e.RetryPlan.Policy, Steps: []taskqueue.StepPayload{{Name: "step", StepRunID: "a", AttemptRunIDs: []string{"a", "b"}, Uses: "shell"}}}
	a, err := taskqueue.NewJobRetryPlan(p)
	if err != nil {
		t.Fatal(err)
	}
	p.RetryApprovalRequired = true
	p.RetryApprovalTimeoutSeconds = 300
	b, err := taskqueue.NewJobRetryPlan(p)
	if err != nil {
		t.Fatal(err)
	}
	if a.DefinitionDigest == b.DefinitionDigest {
		t.Fatal("marker omitted from exact plan")
	}
}

func TestWholeJobApprovalCannotRelabelFailedStepPlan(t *testing.T) {
	e := fixture(t)
	e.RetryPlan.Version = 2
	e.RetryPlan.Policy.Mode = "failed_step"
	if _, err := e.Canonical(); err == nil {
		t.Fatal("whole-job evidence accepted failed-step plan")
	}
}

func failedStepEvidence(t *testing.T) *Evidence {
	e := fixture(t)
	e.Kind = FailedStepKind
	e.RetryPlan.Version = 2
	e.RetryPlan.Policy.Mode = "failed_step"
	var err error
	e.RetryPlanDigest, err = e.RetryPlan.Digest(e.Identity.PipelineID, e.Identity.PipelineRunID, e.Identity.JobRunID, e.Identity.AgentID, e.Runtime.BindingDigest)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func TestFailedStepApprovalExactTupleAndCompleteEvidence(t *testing.T) {
	e := failedStepEvidence(t)
	body, err := e.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseEvidence(body); err != nil {
		t.Fatal(err)
	}
	kind, err := KindForPlan(&e.RetryPlan)
	if err != nil || kind != FailedStepKind {
		t.Fatal(kind, err)
	}
	for name, mutate := range map[string]func(*Evidence){
		"whole kind":   func(e *Evidence) { e.Kind = Kind },
		"unknown kind": func(e *Evidence) { e.Kind = "retry_failed_step_v2" },
		"old version":  func(e *Evidence) { e.RetryPlan.Version = 1 },
		"whole mode":   func(e *Evidence) { e.RetryPlan.Policy.Mode = "whole_job" },
		"no gate": func(e *Evidence) {
			e.RetryPlan.RetryApprovalRequired = false
			e.RetryPlan.RetryApprovalTimeoutSeconds = 0
		},
		"no timeout":         func(e *Evidence) { e.RetryPlan.RetryApprovalTimeoutSeconds = 0 },
		"changed step":       func(e *Evidence) { e.RetryPlan.Steps[0].AttemptRunIDs[1] = "replacement" },
		"missing attempt":    func(e *Evidence) { e.RetryPlan.Steps[0].AttemptRunIDs = e.RetryPlan.Steps[0].AttemptRunIDs[:1] },
		"duplicate attempt":  func(e *Evidence) { e.RetryPlan.Steps[0].AttemptRunIDs[1] = e.RetryPlan.Steps[0].AttemptRunIDs[0] },
		"changed definition": func(e *Evidence) { e.RetryPlan.Steps[0].DefinitionDigest = "v1:" + strings.Repeat("f", 64) },
		"continue":           func(e *Evidence) { e.RetryPlan.Steps[0].ContinueOnError = true },
	} {
		t.Run(name, func(t *testing.T) {
			e := failedStepEvidence(t)
			mutate(e)
			if _, err := e.Canonical(); err == nil {
				t.Fatal("forged evidence accepted")
			}
		})
	}
	whole := fixture(t)
	whole.Kind = FailedStepKind
	if _, err := whole.Canonical(); err == nil {
		t.Fatal("new kind accepted whole job tuple")
	}
}
