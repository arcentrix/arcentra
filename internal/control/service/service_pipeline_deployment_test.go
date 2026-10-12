package service

import (
	"context"
	"errors"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/deployment"
	"github.com/bytedance/sonic"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

func pipelineTriggerSpec(t *testing.T, environment string) *pipelinev1.Spec {
	t.Helper()
	args, err := structpb.NewStruct(map[string]any{"environmentId": environment, "artifact": map[string]any{"job": "build", "step": "upload", "name": "manifest.json"}})
	if err != nil {
		t.Fatal(err)
	}
	return &pipelinev1.Spec{Namespace: "pipeline", Jobs: []*pipelinev1.Job{{Name: "build", Steps: []*pipelinev1.Step{{Name: "upload", Uses: "builtin:artifacts", Action: "upload"}}}, {Name: "deploy", DependsOn: []string{"build"}, Timeout: "20s", Steps: []*pipelinev1.Step{{Name: "release", Uses: "builtin:kubernetes", Action: "deploy", Args: args}}}}}
}
func TestPipelineDeploymentTriggerRequiresVerifiedActorConsentAndReviewedTarget(t *testing.T) {
	releases, db, input, adapter := releaseFixture(t)
	binding := releases.targets["staging"]
	binding.AllowPipelineDeployments = true
	releases.targets["staging"] = binding
	denied := false
	authorizeCalls := 0
	releases.ConfigurePipelineDeployments(repo.NewJobRunRepo(artifactServiceDatabase{db}), repo.NewStepRunRepo(artifactServiceDatabase{db}), func(ctx context.Context, actor, project, pipeline, environment string) error {
		authorizeCalls++
		if denied {
			return errors.New("revoked")
		}
		if actor != "user" || project != "project" || pipeline != "pipe" || environment != input.EnvironmentID {
			t.Fatal("authorization scope changed")
		}
		return nil
	})
	svc := &PipelineServiceImpl{deployment: releases, pipelineRepo: releases.pipelines}
	parsed := pipelineTriggerSpec(t, input.EnvironmentID)
	plan, err := deployment.DecodePipelinePlan(parsed)
	if err != nil {
		t.Fatal(err)
	}
	canonical, hash, _ := plan.Canonical()
	fingerprint, err := releases.PipelineDeploymentTargetFingerprint(context.Background(), "project", input.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("c", 40)
	request := &pipelinev1.TriggerPipelineRequest{PipelineId: "pipe", TriggeredBy: "user", AllowDeployments: true, ExpectedDefinitionCommitSha: sha, ExpectedDeploymentPlanHash: deploymentReviewHash(canonical, fingerprint)}
	pipeline := &model.Pipeline{PipelineID: "pipe", ProjectID: "project"}
	trusted := WithVerifiedPipelineActor(context.Background(), "user")
	tests := []struct {
		name     string
		ctx      context.Context
		mutate   func(*pipelinev1.TriggerPipelineRequest)
		includes bool
		code     int
	}{
		{name: "direct RPC actor string is not authenticated", ctx: context.Background(), code: 403},
		{name: "spoofed actor", ctx: trusted, mutate: func(r *pipelinev1.TriggerPipelineRequest) { r.TriggeredBy = "admin" }, code: 403},
		{name: "opt-in missing", ctx: trusted, mutate: func(r *pipelinev1.TriggerPipelineRequest) { r.AllowDeployments = false }, code: 400},
		{name: "definition stale", ctx: trusted, mutate: func(r *pipelinev1.TriggerPipelineRequest) { r.ExpectedDefinitionCommitSha = strings.Repeat("d", 40) }, code: 409},
		{name: "definition missing", ctx: trusted, mutate: func(r *pipelinev1.TriggerPipelineRequest) { r.ExpectedDefinitionCommitSha = "" }, code: 409},
		{name: "target stale", ctx: trusted, mutate: func(r *pipelinev1.TriggerPipelineRequest) { r.ExpectedDeploymentPlanHash = "stale" }, code: 409},
		{name: "expanded includes unreviewed", ctx: trusted, includes: true, code: 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := proto.Clone(request).(*pipelinev1.TriggerPipelineRequest)
			if tt.mutate != nil {
				tt.mutate(r)
			}
			consent, code, err := svc.authorizeDeploymentTrigger(tt.ctx, r, pipeline, sha, parsed, tt.includes)
			if err == nil || consent != nil || code != tt.code {
				t.Fatalf("accepted unsafe trigger: %+v %d %v", consent, code, err)
			}
		})
	}
	consent, _, err := svc.authorizeDeploymentTrigger(trusted, request, pipeline, sha, parsed, false)
	if err != nil || consent == nil || consent.DeploymentActorID != "user" || consent.DeploymentPlanHash != hash || consent.DeploymentTargetFingerprint != fingerprint {
		t.Fatalf("valid trigger: %+v %v", consent, err)
	}
	denied = true
	if _, code, err := svc.authorizeDeploymentTrigger(trusted, request, pipeline, sha, parsed, false); err == nil || code != 403 {
		t.Fatalf("revocation accepted: %v %d", err, code)
	}
	if authorizeCalls == 0 || adapter.apply+adapter.inspect+adapter.observe != 0 {
		t.Fatal("trigger performed cluster IO or omitted authorization")
	}
	review, reviewHash, err := svc.GetPipelineDeploymentReview(context.Background(), "pipe", parsed)
	if err != nil || review.Target.EnvironmentID != input.EnvironmentID || reviewHash != request.ExpectedDeploymentPlanHash {
		t.Fatalf("review differs from trigger: %+v %s %v", review, reviewHash, err)
	}
	data, _ := sonic.MarshalString(review)
	if strings.Contains(data, "fingerprint") || strings.Contains(data, "bearer") || strings.Contains(data, "server") {
		t.Fatalf("private target config exposed: %s", data)
	}
}
func TestPipelineDeploymentDuplicateBindsOriginalIntent(t *testing.T) {
	existing := &model.PipelineRun{AllowDeployments: true, DeploymentActorID: "user", DefinitionCommitSha: "sha", DeploymentPlan: "plan", DeploymentTargetFingerprint: "target", Env: `{"VERSION":"v1"}`}
	request := &pipelinev1.TriggerPipelineRequest{TriggeredBy: "user", AllowDeployments: true, ExpectedDefinitionCommitSha: "sha", ExpectedDeploymentPlanHash: deploymentReviewHash("plan", "target"), Variables: map[string]string{"VERSION": "v1"}}
	ctx := WithVerifiedPipelineActor(context.Background(), "user")
	if err := validateDeploymentDuplicate(ctx, request, existing); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*pipelinev1.TriggerPipelineRequest){"actor": func(r *pipelinev1.TriggerPipelineRequest) { r.TriggeredBy = "other" }, "optin": func(r *pipelinev1.TriggerPipelineRequest) { r.AllowDeployments = false }, "revision": func(r *pipelinev1.TriggerPipelineRequest) { r.ExpectedDefinitionCommitSha = "new" }, "target": func(r *pipelinev1.TriggerPipelineRequest) { r.ExpectedDeploymentPlanHash = "new" }, "variables": func(r *pipelinev1.TriggerPipelineRequest) { r.Variables["VERSION"] = "v2" }} {
		t.Run(name, func(t *testing.T) {
			r := proto.Clone(request).(*pipelinev1.TriggerPipelineRequest)
			mutate(r)
			if err := validateDeploymentDuplicate(ctx, r, existing); err == nil {
				t.Fatal("rebound duplicate accepted")
			}
		})
	}
	if err := validateDeploymentDuplicate(context.Background(), request, existing); err == nil {
		t.Fatal("unverified duplicate exposed")
	}
}

func TestPipelineDeploymentReviewKeepsUnconfiguredSpecEditable(t *testing.T) {
	releases, _, input, _ := releaseFixture(t)
	svc := &PipelineServiceImpl{deployment: releases, pipelineRepo: releases.pipelines}
	parsed := pipelineTriggerSpec(t, input.EnvironmentID)
	review, hash, err := svc.GetPipelineDeploymentReview(context.Background(), "pipe", parsed)
	if err == nil || review == nil || review.Target == nil || review.Target.AllowPipelineDeployments || hash != "" {
		t.Fatalf("disabled target must keep readable nonexecutable plan: %+v %q %v", review, hash, err)
	}
	releases.targets = nil
	review, hash, err = svc.GetPipelineDeploymentReview(context.Background(), "pipe", parsed)
	if err == nil || review == nil || review.Target == nil || review.Target.Configured || hash != "" {
		t.Fatalf("missing target config hides definition: %+v %q %v", review, hash, err)
	}
}

func TestScopedPipelineStepExposesReleaseLinkOnlyInItsRun(t *testing.T) {
	svc := runServiceFixture(t)
	if err := svc.stepRunRepo.PatchByStepRunID(context.Background(), "step-a", map[string]any{"deployment_release_id": "release-a"}); err != nil {
		t.Fatal(err)
	}
	jobs, err := svc.ListPipelineRunJobs(context.Background(), "pipeline-a", "run-a")
	if err != nil || len(jobs) != 1 || len(jobs[0].Steps) != 1 || jobs[0].Steps[0].DeploymentReleaseID != "release-a" {
		t.Fatalf("release link missing: %+v %v", jobs, err)
	}
	if _, err := svc.ListPipelineRunJobs(context.Background(), "pipeline-b", "run-a"); !errors.Is(err, ErrPipelineRunNotFound) {
		t.Fatalf("cross-pipeline release link disclosed: %v", err)
	}
}

// triggerCaptureEngine 不执行命令或集群请求，只记录真实服务入口传给执行器的快照
type triggerCaptureEngine struct {
	calls int
	run   *model.PipelineRun
}

func (e *triggerCaptureEngine) Submit(run *model.PipelineRun, _ *pipelinev1.Spec) error {
	e.calls++
	copy := *run
	e.run = &copy
	return nil
}
func (e *triggerCaptureEngine) CancelRun(string) error { return nil }
func (e *triggerCaptureEngine) PauseRun(string) error  { return nil }
func (e *triggerCaptureEngine) ResumeRun(string) error { return nil }

func TestTriggerPipelinePersistsVerifiedImmutableConsentThroughPublicService(t *testing.T) {
	releases, db, input, adapter := releaseFixture(t)
	binding := releases.targets["staging"]
	binding.AllowPipelineDeployments = true
	releases.targets["staging"] = binding
	database := artifactServiceDatabase{db}
	releases.ConfigurePipelineDeployments(repo.NewJobRunRepo(database), repo.NewStepRunRepo(database), func(context.Context, string, string, string, string) error { return nil })
	parsed := pipelineTriggerSpec(t, input.EnvironmentID)
	content, err := spec.MarshalProtoByFormat(parsed, pipelinev1.SpecFormat_SPEC_FORMAT_JSON, "pipeline.json")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		command := exec.CommandContext(context.Background(), "git", append([]string{"-C", directory}, args...)...)
		command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("local git fixture: %v %s", err, output)
		}
		return strings.TrimSpace(string(output))
	}
	runGit("-c", "init.defaultBranch=main", "init")
	if err := os.WriteFile(filepath.Join(directory, "pipeline.json"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "pipeline.json")
	runGit("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "fixture")
	head := runGit("rev-parse", "HEAD")
	if err := releases.pipelines.Update(context.Background(), "pipe", map[string]any{"repo_url": directory, "default_branch": "main", "pipeline_file_path": "pipeline.json", "is_enabled": 1}); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Project{}).Where("project_id = ?", "project").Updates(map[string]any{"repo_url": directory, "default_branch": "main"}).Error; err != nil {
		t.Fatal(err)
	}
	engine := &triggerCaptureEngine{}
	svc := &PipelineServiceImpl{pipelineRepo: releases.pipelines, projectRepo: repo.NewProjectRepo(database), deployment: releases, engine: engine}
	review, token, err := svc.GetPipelineDeploymentReview(context.Background(), "pipe", parsed)
	if err != nil || review == nil {
		t.Fatal(err)
	}
	unsafe := proto.Clone(parsed).(*pipelinev1.Spec)
	unsafe.Jobs[1].Steps[0].RunOnAgent = true
	validationResponse, err := svc.ValidatePipelineSpec(context.Background(), &pipelinev1.ValidatePipelineSpecRequest{PipelineId: "pipe", Spec: unsafe})
	if err != nil || validationResponse.Success || validationResponse.GetError().GetCode() != 400 {
		t.Fatalf("unsafe deployment validated: %+v %v", validationResponse, err)
	}
	saveResponse, err := svc.SavePipelineSpec(context.Background(), &pipelinev1.SavePipelineSpecRequest{PipelineId: "pipe", Spec: unsafe, ExpectedHeadCommitSha: head, RequestId: "invalid-save", Editor: "user"})
	if err != nil || saveResponse.Success || saveResponse.GetError().GetCode() != 400 {
		t.Fatalf("unsafe deployment saved: %+v %v", saveResponse, err)
	}
	if runGit("rev-parse", "HEAD") != head {
		t.Fatal("rejected save changed repository")
	}
	request := &pipelinev1.TriggerPipelineRequest{PipelineId: "pipe", TriggeredBy: "user", RequestId: "public-trigger", AllowDeployments: true, ExpectedDefinitionCommitSha: head, ExpectedDeploymentPlanHash: token, Variables: map[string]string{"BUILD": "one"}}
	// Direct RPC payload has exactly the same claimed identity and still cannot authorize a deployment.
	response, err := svc.TriggerPipeline(context.Background(), request)
	if err != nil || response.Success || response.GetError().GetCode() != 403 || engine.calls != 0 {
		t.Fatalf("unverified public trigger: %+v %v", response, err)
	}
	response, err = svc.TriggerPipeline(WithVerifiedPipelineActor(context.Background(), "user"), request)
	if err != nil || !response.Success || engine.calls != 1 {
		t.Fatalf("verified public trigger: %+v %v", response, err)
	}
	saved, err := svc.pipelineRepo.GetRun(context.Background(), response.RunId)
	if err != nil {
		t.Fatal(err)
	}
	if saved.DeploymentActorID != "user" || saved.TriggeredBy != "user" || !saved.AllowDeployments || saved.DefinitionCommitSha != head || saved.CommitSha != "" || saved.DeploymentPlan == "" || saved.DeploymentPlanHash == "" || saved.DeploymentTargetFingerprint == "" {
		t.Fatalf("immutable consent missing: %+v", saved)
	}
	duplicate, err := svc.TriggerPipeline(WithVerifiedPipelineActor(context.Background(), "user"), request)
	if err != nil || !duplicate.Success || duplicate.RunId != saved.RunID || engine.calls != 1 {
		t.Fatalf("duplicate scheduled again: %+v %v", duplicate, err)
	}
	request.Variables["BUILD"] = "two"
	conflict, err := svc.TriggerPipeline(WithVerifiedPipelineActor(context.Background(), "user"), request)
	if err != nil || conflict.Success || conflict.GetError().GetCode() != 409 || engine.calls != 1 {
		t.Fatalf("changed duplicate accepted: %+v %v", conflict, err)
	}
	if adapter.apply+adapter.inspect+adapter.observe != 0 {
		t.Fatal("trigger or review contacted Kubernetes")
	}
}
