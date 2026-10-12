package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	cluster "github.com/arcentrix/arcentra/internal/shared/deployment"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/bytedance/sonic"
	"gorm.io/gorm"
)

func pipelineReleaseFixture(t *testing.T) (*DeploymentService, *gorm.DB, PipelineDeploymentExecution, *releaseFakeAdapter) {
	t.Helper()
	s, db, input, adapter := releaseFixture(t)
	ctx := context.Background()
	artifact, err := s.artifacts.Get(ctx, "project", input.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	name := "image-manifest.json"
	path, _ := artifactutil.LocalPath("project", "run", "job", "step", name)
	if err = os.Rename(filepath.Join(s.files.root, artifact.StoragePath), filepath.Join(s.files.root, path)); err != nil {
		t.Fatal(err)
	}
	artifactID := artifactutil.ID("project", "run", "job", "step", name)
	if err = db.Model(&model.ProjectArtifact{}).Where("artifact_id = ?", artifact.ArtifactID).Updates(map[string]any{"artifact_id": artifactID, "name": name, "storage_path": path}).Error; err != nil {
		t.Fatal(err)
	}
	binding := s.targets["staging"]
	binding.AllowPipelineDeployments = true
	s.targets["staging"] = binding
	plan := &cluster.PipelineDeploymentPlan{Version: 1, JobName: "deploy", StepName: "release", EnvironmentID: input.EnvironmentID, Artifact: cluster.PipelineArtifactReference{Job: "build", Step: "upload", Name: name}, TimeoutSeconds: 5, Predecessors: []cluster.PipelinePredecessor{{JobName: "build", Steps: []cluster.PipelinePlanStep{{Name: "upload", Index: 0}}}}}
	planJSON, hash, err := plan.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&model.PipelineRun{}).Where("run_id = ?", "run").Updates(map[string]any{"status": model.PipelineStatusRunning, "commit_sha": "", "definition_commit_sha": strings.Repeat("d", 40), "trigger_type": 1, "triggered_by": "user", "allow_deployments": true, "deployment_actor_id": "user", "deployment_plan": planJSON, "deployment_plan_hash": hash, "deployment_target_fingerprint": binding.Fingerprint}).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(map[string]any{"job_name": "build", "status": 4, "total_steps": 1, "completed_steps": 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&model.StepRun{}).Where("step_run_id = ?", "step").Updates(map[string]any{"name": "upload", "status": 4, "attempt": 1, "uses": "builtin:artifacts", "action": "upload"}).Error; err != nil {
		t.Fatal(err)
	}
	args, _ := sonic.MarshalString(map[string]any{"environmentId": input.EnvironmentID, "artifact": plan.Artifact})
	for _, value := range []any{&model.JobRun{JobRunID: "deploy-job", PipelineID: "pipe", PipelineRunID: "run", JobName: "deploy", Status: 3, TotalSteps: 1}, &model.StepRun{StepRunID: "deploy-step", JobRunID: "deploy-job", PipelineID: "pipe", PipelineRunID: "run", Name: "release", CreatedBy: "user", Uses: "builtin:kubernetes", Action: "deploy", Args: args, Attempt: 1, Status: 3}} {
		if err = db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	s.ConfigurePipelineDeployments(repo.NewJobRunRepo(artifactServiceDatabase{db}), repo.NewStepRunRepo(artifactServiceDatabase{db}), func(context.Context, string, string, string, string) error { return nil })
	return s, db, PipelineDeploymentExecution{ProjectID: "project", PipelineID: "pipe", PipelineRunID: "run", JobRunID: "deploy-job", StepRunID: "deploy-step", ActorID: "user"}, adapter
}

func TestPipelineReleasePreparationPinsSourceAndLinksAtomically(t *testing.T) {
	s, db, execution, adapter := pipelineReleaseFixture(t)
	ctx := context.Background()
	release, err := s.PreparePipelineRelease(ctx, execution)
	if err != nil {
		t.Fatal(err)
	}
	if release.Origin != "pipeline_job" || release.ProducerStepRunID != "step" || release.ExecutionStepRunID == nil || *release.ExecutionStepRunID != execution.StepRunID || release.DeploymentPlanHash == "" || release.CreatedBy != "user" || adapter.apply+adapter.inspect+adapter.observe != 0 {
		t.Fatalf("unbound preparation: %+v", release)
	}
	var step model.StepRun
	if err = db.Where("step_run_id = ?", execution.StepRunID).First(&step).Error; err != nil || step.DeploymentReleaseID != release.ReleaseID {
		t.Fatalf("release link not transactional: %+v %v", step, err)
	}
	// Re-entry does not require or select source bytes again.
	if err = db.Where("artifact_id = ?", release.ArtifactID).Delete(&model.ProjectArtifact{}).Error; err != nil {
		t.Fatal(err)
	}
	same, err := s.PreparePipelineRelease(ctx, execution)
	if err != nil || same.ReleaseID != release.ReleaseID {
		t.Fatalf("reselected source: %+v %v", same, err)
	}
	if _, err = s.AdvanceAuthorized(ctx, "project", release.ReleaseID, "user"); !errors.Is(err, ErrPipelineDeploymentDenied) {
		t.Fatalf("applied missing source: %v", err)
	}
	if adapter.inspect+adapter.apply != 0 {
		t.Fatal("source invalid before apply")
	}
	// Public manual preparation remains closed to active runs and pipeline request IDs.
	_, err = s.CreateRelease(ctx, "project", "user", DeploymentReleaseInput{RequestID: release.RequestID, EnvironmentID: release.EnvironmentID, PipelineID: release.PipelineID, PipelineRunID: release.PipelineRunID, ArtifactID: release.ArtifactID})
	if !errors.Is(err, repo.ErrDeploymentConflict) {
		t.Fatalf("manual endpoint rebound pipeline source: %v", err)
	}
}

func TestPipelineReleaseFailClosedBeforeAnyClusterCall(t *testing.T) {
	for _, test := range []string{"missing consent", "wrong actor", "unverified trigger", "changed plan", "changed target", "disabled target", "permission revoked", "continue failure", "missing step", "failed retry", "wrong artifact step"} {
		t.Run(test, func(t *testing.T) {
			s, db, execution, adapter := pipelineReleaseFixture(t)
			switch test {
			case "missing consent":
				db.Model(&model.PipelineRun{}).Where("run_id = ?", "run").Update("allow_deployments", false)
			case "wrong actor":
				execution.ActorID = "forged"
			case "unverified trigger":
				db.Model(&model.PipelineRun{}).Where("run_id = ?", "run").Update("trigger_type", 2)
			case "changed plan":
				db.Model(&model.PipelineRun{}).Where("run_id = ?", "run").Update("deployment_plan_hash", strings.Repeat("a", 64))
			case "changed target":
				b := s.targets["staging"]
				b.Fingerprint = strings.Repeat("c", 64)
				s.targets["staging"] = b
			case "disabled target":
				b := s.targets["staging"]
				b.AllowPipelineDeployments = false
				s.targets["staging"] = b
			case "permission revoked":
				s.pipelineAuthorize = func(context.Context, string, string, string, string) error { return errors.New("revoked") }
			case "continue failure":
				db.Model(&model.StepRun{}).Where("step_run_id = ?", "step").Updates(map[string]any{"status": 5, "continue_on_error": 1})
			case "missing step":
				db.Where("step_run_id = ?", "step").Delete(&model.StepRun{})
			case "failed retry":
				db.Create(&model.StepRun{StepRunID: "new-attempt", JobRunID: "job", PipelineID: "pipe", PipelineRunID: "run", Name: "upload", Attempt: 2, PreviousStepRunID: "step", Status: 5, Uses: "builtin:artifacts", Action: "upload"})
			case "wrong artifact step":
				db.Model(&model.StepRun{}).Where("step_run_id = ?", "step").Update("uses", "shell")
			}
			if release, err := s.PreparePipelineRelease(context.Background(), execution); err == nil || release != nil {
				t.Fatalf("accepted invalid source: %+v %v", release, err)
			}
			if adapter.inspect+adapter.apply+adapter.observe != 0 {
				t.Fatal("invalid pipeline accessed cluster")
			}
			var count int64
			db.Model(&model.DeploymentRelease{}).Count(&count)
			if count != 0 {
				t.Fatal("invalid source reserved environment")
			}
		})
	}
}

func TestPipelineReleaseAmbiguousApplyRequiresExplicitSameIDRecovery(t *testing.T) {
	s, db, execution, adapter := pipelineReleaseFixture(t)
	adapter.applyErr = cluster.ErrUnavailable
	release, err := s.ExecutePipelineDeployment(context.Background(), execution, func(*model.DeploymentRelease) error { return nil })
	if !errors.Is(err, ErrPipelineDeploymentUncertain) || release == nil || release.Status != "applying" || adapter.apply != 1 {
		t.Fatalf("ambiguous apply lost: %+v %v", release, err)
	}
	again, err := s.ExecutePipelineDeployment(context.Background(), execution, func(*model.DeploymentRelease) error { return nil })
	if !errors.Is(err, ErrPipelineDeploymentUncertain) || again.ReleaseID != release.ReleaseID || adapter.apply != 1 {
		t.Fatalf("automatically replayed ambiguous apply: %+v %v", again, err)
	}
	if _, err = s.Advance(context.Background(), "project", release.ReleaseID); !errors.Is(err, ErrPipelineDeploymentUncertain) {
		t.Fatalf("anonymous recovery accepted: %v", err)
	}
	if _, err = s.Cancel(context.Background(), "project", release.ReleaseID); !errors.Is(err, ErrDeploymentCancel) {
		t.Fatalf("ambiguous release cancelled: %v", err)
	}
	db.Model(&model.PipelineRun{}).Where("run_id = ?", "run").Update("status", model.PipelineStatusCancelled)
	db.Model(&model.StepRun{}).Where("step_run_id = ?", execution.StepRunID).Update("status", 6)
	db.Model(&model.JobRun{}).Where("job_run_id = ?", execution.JobRunID).Update("status", 6)
	b := s.targets["staging"]
	b.AllowPipelineDeployments = false
	s.targets["staging"] = b
	adapter.applyErr = nil
	recovered, err := s.RecoverAuthorized(context.Background(), "project", release.ReleaseID, "operator")
	if err != nil || recovered.Status != "observing" || recovered.LastRecoveryBy != "operator" || recovered.LastRecoveryAt == nil || recovered.CreatedBy != "user" || adapter.apply != 2 {
		t.Fatalf("explicit original release cannot recover: %+v %v", recovered, err)
	}
}

func TestPipelineReleasePreparationCallbackFailureNeverApplies(t *testing.T) {
	s, _, execution, adapter := pipelineReleaseFixture(t)
	release, err := s.ExecutePipelineDeployment(context.Background(), execution, func(*model.DeploymentRelease) error { return errors.New("persistence failed") })
	if err == nil || release == nil || release.Status != "queued" || adapter.apply+adapter.inspect != 0 {
		t.Fatalf("persistence failure applied: %+v %v", release, err)
	}
}

func TestPipelineLatestAttemptSelection(t *testing.T) {
	job := &model.JobRun{JobRunID: "job", PipelineID: "pipe", PipelineRunID: "run", TotalSteps: 1}
	expected := []cluster.PipelinePlanStep{{Name: "upload", Index: 0}}
	first := model.StepRun{StepRunID: "one", JobRunID: "job", PipelineID: "pipe", PipelineRunID: "run", Name: "upload", Attempt: 1, Status: 4}
	unused := first
	unused.StepRunID = "two"
	unused.Attempt = 2
	unused.PreviousStepRunID = "one"
	unused.Status = 8
	got, err := successfulPipelineAttempts(job, expected, []model.StepRun{first, unused})
	if err != nil || got["upload"].StepRunID != "one" {
		t.Fatalf("unused reservation hides latest success: %+v %v", got, err)
	}
	now := time.Now()
	unused.StartTime = &now
	if _, err = successfulPipelineAttempts(job, expected, []model.StepRun{first, unused}); err == nil {
		t.Fatal("executed skipped attempt fell back to old success")
	}
	first.RetryMode = "whole_job"
	unused.RetryMode = "whole_job"
	unused.StartTime = nil
	build := first
	build.Name = "build"
	build.StepRunID = "build-one"
	build.StepIndex = 1
	buildTwo := build
	buildTwo.StepRunID = "build-two"
	buildTwo.Attempt = 2
	buildTwo.PreviousStepRunID = "build-one"
	job.TotalSteps = 2
	expected = append(expected, cluster.PipelinePlanStep{Name: "build", Index: 1})
	if _, err = successfulPipelineAttempts(job, expected, []model.StepRun{first, unused, build, buildTwo}); err == nil {
		t.Fatal("latest whole-job skipped producer fell back to old success")
	}
}

func TestPipelineReleaseInspectionDoesNotClaimUnadmittedPatchWasSuperseded(t *testing.T) {
	s, _, execution, adapter := pipelineReleaseFixture(t)
	adapter.applyErr = cluster.ErrUnavailable
	entry, err := s.ExecutePipelineDeployment(context.Background(), execution, func(*model.DeploymentRelease) error { return nil })
	if !errors.Is(err, ErrPipelineDeploymentUncertain) {
		t.Fatal(err)
	}
	adapter.observation = cluster.Observation{Status: "superseded", Reason: "generation differs"}
	inspected, err := s.InspectAuthorized(context.Background(), "project", entry.ReleaseID, "operator")
	if err != nil || inspected.Status != "unresolved" || inspected.PersistedStatus != "applying" || adapter.apply != 1 {
		t.Fatalf("inspection overclaimed: %+v %v", inspected, err)
	}
	persisted, err := s.GetRelease(context.Background(), "project", entry.ReleaseID)
	if err != nil || persisted.Status != "applying" || persisted.Version != entry.Version {
		t.Fatalf("inspection mutated release: %+v %v", persisted, err)
	}
}

func TestPipelineReleaseRecoveryAuditFailureAbortsBeforeMutation(t *testing.T) {
	s, db, execution, adapter := pipelineReleaseFixture(t)
	adapter.applyErr = cluster.ErrUnavailable
	entry, err := s.ExecutePipelineDeployment(context.Background(), execution, func(*model.DeploymentRelease) error { return nil })
	if !errors.Is(err, ErrPipelineDeploymentUncertain) {
		t.Fatal(err)
	}
	if err = db.Callback().Update().Before("gorm:update").Register("fail_recovery_audit", func(tx *gorm.DB) {
		if updates, ok := tx.Statement.Dest.(map[string]any); ok {
			if _, exists := updates["last_recovery_by"]; exists {
				tx.AddError(errors.New("audit write failed"))
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	adapter.applyErr = nil
	if _, err = s.RecoverAuthorized(context.Background(), "project", entry.ReleaseID, "operator"); err == nil {
		t.Fatal("recovery continued without durable actor")
	}
	if adapter.apply != 1 {
		t.Fatal("recovery mutation happened despite audit failure")
	}
}

type recoveryTestTransport func(*http.Request) (*http.Response, error)

func (f recoveryTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDeploymentManualRecoveryRechecksPermissionAfterKubernetesRead(t *testing.T) {
	s, db, input, _ := releaseFixture(t)
	allowed, patches := true, 0
	adapter, err := cluster.NewKubernetesAdapter("https://cluster.invalid", &http.Client{Transport: recoveryTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPatch {
			patches++
		}
		allowed = false
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"metadata":{"uid":"workload","generation":7,"resourceVersion":"old"},"spec":{"replicas":1,"template":{"metadata":{"annotations":{}},"spec":{"containers":[{"name":"app","image":"old:latest"}]}}}}`))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	binding := s.targets["staging"]
	binding.Adapter = adapter
	s.targets["staging"] = binding
	s.ConfigurePipelineDeployments(repo.NewJobRunRepo(artifactServiceDatabase{db}), repo.NewStepRunRepo(artifactServiceDatabase{db}), func(context.Context, string, string, string, string) error {
		if !allowed {
			return errors.New("revoked")
		}
		return nil
	})
	entry, err := s.CreateRelease(context.Background(), "project", "user", input)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := s.records.Transition(context.Background(), entry, "applying", map[string]any{"workload_uid": "workload", "baseline_generation": int64(7)}); err != nil || !changed {
		t.Fatal(err)
	}
	if _, err = s.RecoverAuthorized(context.Background(), "project", entry.ReleaseID, "operator"); !errors.Is(err, ErrPipelineDeploymentDenied) {
		t.Fatalf("manual recovery did not recheck: %v", err)
	}
	if patches != 0 {
		t.Fatalf("revoked manual recovery sent %d patches", patches)
	}
}

func TestPipelineConcurrentPreparationCreatesOneRelease(t *testing.T) {
	s, db, execution, adapter := pipelineReleaseFixture(t)
	var mu sync.Mutex
	ids := []string{}
	t.Run("simultaneous preparations", func(t *testing.T) {
		for i := 0; i < 8; i++ {
			t.Run(fmt.Sprint(i), func(t *testing.T) {
				t.Parallel()
				entry, err := s.PreparePipelineRelease(context.Background(), execution)
				if err != nil {
					t.Errorf("concurrent preparation: %v", err)
					return
				}
				mu.Lock()
				ids = append(ids, entry.ReleaseID)
				mu.Unlock()
			})
		}
	})
	if len(ids) != 8 {
		t.Fatalf("only %d preparations completed", len(ids))
	}
	for _, id := range ids {
		if id != ids[0] {
			t.Fatal("execution step has multiple release identities")
		}
	}
	var count int64
	if err := db.Model(&model.DeploymentRelease{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("release count=%d error=%v", count, err)
	}
	if adapter.apply+adapter.inspect+adapter.observe != 0 {
		t.Fatal("preparation touched cluster")
	}
}
