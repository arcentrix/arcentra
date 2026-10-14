package taskqueue

import (
	"strings"
	"testing"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/bytedance/sonic"
	"github.com/google/uuid"
)

func generationContractFixture(t *testing.T) (*JobRunTaskPayload, JobGeneration) {
	t.Helper()
	runtime := &RuntimeRequest{Type: RuntimeContainerdOfflineV1, Image: "fixture/image@sha256:" + strings.Repeat("a", 64), Resources: RuntimeResources{CPU: "500m", Memory: "64Mi"}}
	runtime.BindingDigest, _ = RuntimeBindingDigest("pipeline", "run", "job", "agent", runtime)
	p := &JobRunTaskPayload{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", AgentID: "agent", Runtime: runtime, Retry: &pipelinev1.Retry{Mode: "whole_job", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}, Delay: "1s"}, Steps: []StepPayload{{Name: "shell", StepRunID: "step-1", AttemptRunIDs: []string{"step-1", "step-2"}, Uses: "shell", Args: map[string]any{"run": "echo original"}, Env: map[string]string{"A": "first", "Z": "last"}}}, Env: map[string]string{"JOB": "original"}, ArtifactURIs: map[string]string{}}
	var err error
	p.RetryPlan, err = NewJobRetryPlan(p)
	if err != nil {
		t.Fatal(err)
	}
	p.RetryPlanDigest, err = p.RetryPlan.Digest("pipeline", "run", "job", "agent", runtime.BindingDigest)
	if err != nil {
		t.Fatal(err)
	}
	g := JobGeneration{Version: 1, PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", AgentID: "agent", OriginalClaimID: "claim", RuntimeBindingDigest: runtime.BindingDigest, RetryPlanDigest: p.RetryPlanDigest, GenerationID: uuid.NewString(), RuntimeClaimID: uuid.NewString(), Number: 1, PreparationOnly: true, ExecutionAllowed: true}
	g.BindingDigest, _ = g.Digest()
	return p, g
}
func TestRetryPlanBindsFullExecutablePolicyAndAttemptUpperBound(t *testing.T) {
	p, _ := generationContractFixture(t)
	if err := ValidateJobRetryPlan(p); err != nil {
		t.Fatal(err)
	}
	bytes, _ := sonic.Marshal(p)
	cases := map[string]func(*JobRunTaskPayload){"command": func(p *JobRunTaskPayload) { p.Steps[0].Args["run"] = "echo changed" }, "step-env": func(p *JobRunTaskPayload) { p.Steps[0].Env["A"] = "changed" }, "job-env": func(p *JobRunTaskPayload) { p.Env["JOB"] = "changed" }, "timeout": func(p *JobRunTaskPayload) { p.Timeout = "1h" }, "policy-code": func(p *JobRunTaskPayload) { p.Retry.AllowedExitCodes = []int32{10} }, "policy-delay": func(p *JobRunTaskPayload) { p.Retry.Delay = "1h" }, "policy-idempotent": func(p *JobRunTaskPayload) { p.Retry.Idempotent = false }, "attempt": func(p *JobRunTaskPayload) { p.Steps[0].AttemptRunIDs[1] = "step-other" }, "maximum": func(p *JobRunTaskPayload) { p.Retry.MaxAttempts = 3 }, "step-order": func(p *JobRunTaskPayload) { p.Steps[0].StepIndex = 1 }, "agent": func(p *JobRunTaskPayload) { p.AgentID = "other" }, "source": func(p *JobRunTaskPayload) {
		p.Source = &SourcePayload{Type: "git", Repo: "https://example.invalid/repo", Branch: "main"}
	}, "reserved-env": func(p *JobRunTaskPayload) { p.Steps[0].Env[JobGenerationEnvironment] = "2" }}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var changed JobRunTaskPayload
			if err := sonic.Unmarshal(bytes, &changed); err != nil {
				t.Fatal(err)
			}
			mutate(&changed)
			if err := ValidateJobRetryPlan(&changed); err == nil {
				t.Fatal("changed full retry definition accepted")
			}
		})
	}
	p.Steps[0].Env = map[string]string{"Z": "last", "A": "first"}
	if err := ValidateJobRetryPlan(p); err != nil {
		t.Fatal("map order changed binding", err)
	}
}
func TestGenerationDescriptorBootstrapAndReadOnlyFlags(t *testing.T) {
	_, g := generationContractFixture(t)
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	original := g.BindingDigest
	g.ExecutionAllowed = false
	g.PreparationOnly = false
	if err := g.Validate(); err != nil || g.BindingDigest != original {
		t.Fatal("read-only observation changed immutable binding")
	}
	g.Number = 2
	g.PreviousGenerationID = uuid.NewString()
	g.BindingDigest, _ = g.Digest()
	if err := g.Validate(); err == nil {
		t.Fatal("successor omitted immutable input binding")
	}
	g.InputManifestDigest = "v1:" + strings.Repeat("a", 64)
	g.BindingDigest, _ = g.Digest()
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"not-uuid", strings.ToUpper(g.RuntimeClaimID), "00000000-0000-0000-0000-000000000000"} {
		bad := g
		bad.RuntimeClaimID = id
		bad.BindingDigest, _ = bad.Digest()
		if bad.Validate() == nil {
			t.Fatal("invalid runtime claim accepted")
		}
	}
}
func TestGenerationCanonicalParsersRejectUnknownDuplicateAndChangedRequests(t *testing.T) {
	p, g := generationContractFixture(t)
	encoded, _ := g.Canonical()
	for _, bad := range []string{encoded + " ", strings.Replace(encoded, "{", "{\"unexpected\":true,", 1), strings.Replace(encoded, "{", "{\"version\":1,", 1)} {
		if _, err := ParseJobGeneration(bad); err == nil {
			t.Fatal("noncanonical generation accepted")
		}
	}
	plan, _ := p.RetryPlan.Canonical()
	for _, bad := range []string{plan + " ", strings.Replace(plan, "{", "{\"version\":1,", 1)} {
		if _, err := ParseJobRetryPlan(bad); err == nil {
			t.Fatal("noncanonical plan accepted")
		}
	}
	req := JobGenerationTransition{Version: 1, Identity: JobInputIdentity{PipelineID: g.PipelineID, PipelineRunID: g.PipelineRunID, JobRunID: g.JobRunID, AgentID: g.AgentID, ClaimID: g.OriginalClaimID, RuntimeBindingDigest: g.RuntimeBindingDigest}, RetryPlanDigest: g.RetryPlanDigest, Operation: "admit"}
	req.RequestDigest, _ = req.Digest()
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	req.Operation = "advance"
	if req.Validate() == nil {
		t.Fatal("mutated or evidence-free advance accepted")
	}
}
