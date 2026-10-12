package repo

import (
	"context"
	"errors"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
)

func TestDeploymentRepositoryCASPreventsCancelApplyRace(t *testing.T) {
	db, _ := artifactRepositoryFixture(t)
	if err := db.AutoMigrate(&model.DeploymentEnvironment{}, &model.DeploymentRelease{}); err != nil {
		t.Fatal(err)
	}
	r := NewDeploymentRepo(artifactTestDatabase{db})
	ctx := context.Background()
	for _, id := range []string{"one", "two"} {
		if err := r.CreateEnvironment(ctx, &model.DeploymentEnvironment{EnvironmentID: id, ProjectID: "a", Name: id, TargetRef: "target-" + id, Namespace: "staging", Deployment: "web", Container: "app"}); err != nil {
			t.Fatal(err)
		}
	}
	release, err := r.CreateRelease(ctx, &model.DeploymentRelease{Version: 1, ReleaseID: "release", RequestID: "request", ProjectID: "a", EnvironmentID: "one", Status: "queued", PipelineID: "pipe", PipelineRunID: "run", ArtifactID: "manifest", Image: "immutable-image", CommitSHA: "commit", ArtifactSHA256: "manifest-sha"})
	if err != nil {
		t.Fatal(err)
	}
	stale := *release
	changed, err := r.Transition(ctx, release, "applying", map[string]any{"workload_uid": "uid", "baseline_generation": 1})
	if err != nil || !changed {
		t.Fatalf("claim %t %v", changed, err)
	}
	if changed, err := r.Transition(ctx, &stale, "cancelled", map[string]any{"reason": "stale cancellation"}); err != nil || changed {
		t.Fatalf("stale cancellation won %t %v", changed, err)
	}
	environment, _ := r.GetEnvironment(ctx, "a", "one")
	if environment.ActiveReleaseID != "release" {
		t.Fatal("active target was unlocked")
	}
	another := stale
	another.ReleaseID = "different"
	another.EnvironmentID = "two"
	if _, err := r.CreateRelease(ctx, &another); !errors.Is(err, ErrDeploymentConflict) {
		t.Fatalf("idempotency key rebound %v", err)
	}
	environment, _ = r.GetEnvironment(ctx, "a", "two")
	if environment.ActiveReleaseID != "" {
		t.Fatal("failed transaction retained environment claim")
	}
	actual, _ := r.GetRelease(ctx, "a", "release")
	if actual.Status != "applying" || actual.Version != 2 {
		t.Fatalf("CAS state %+v", actual)
	}
	if changed, err := r.Transition(ctx, actual, "superseded", map[string]any{"reason": "observed replaced workload"}); err != nil || !changed {
		t.Fatalf("terminal %v", err)
	}
	environment, _ = r.GetEnvironment(ctx, "a", "one")
	if environment.ActiveReleaseID != "" {
		t.Fatal("terminal state did not release target")
	}
}

func TestDeploymentPipelineCreationLinksStepOrRollsBackTogether(t *testing.T) {
	db, _ := artifactRepositoryFixture(t)
	if err := db.AutoMigrate(&model.DeploymentEnvironment{}, &model.DeploymentRelease{}); err != nil {
		t.Fatal(err)
	}
	r := NewDeploymentRepo(artifactTestDatabase{db})
	ctx := context.Background()
	if err := db.Model(&model.PipelineRun{}).Where("run_id = ?", "r-a").Updates(map[string]any{"status": model.PipelineStatusRunning, "allow_deployments": true, "deployment_actor_id": "actor", "deployment_plan_hash": "plan-hash", "definition_commit_sha": "definition", "deployment_target_fingerprint": "target"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.JobRun{}).Where("job_run_id = ?", "j-a").Update("status", model.JobRunStatusRunning).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.StepRun{}).Where("step_run_id = ?", "s-a").Updates(map[string]any{"status": 3, "created_by": "actor"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"busy", "free"} {
		if err := r.CreateEnvironment(ctx, &model.DeploymentEnvironment{EnvironmentID: id, ProjectID: "a", TargetRef: id, Namespace: "staging", Deployment: "web", Container: "app"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&model.DeploymentEnvironment{}).Where("environment_id = ?", "busy").Update("active_release_id", "existing").Error; err != nil {
		t.Fatal(err)
	}
	step := "s-a"
	value := &model.DeploymentRelease{Origin: "pipeline_job", Version: 1, ReleaseID: "release", ProjectID: "a", RequestID: "request", EnvironmentID: "busy", PipelineID: "p-a", PipelineRunID: "r-a", ExecutionJobRunID: "j-a", ExecutionStepRunID: &step, DeploymentPlanHash: "plan-hash", CommitSHA: "definition", TargetFingerprint: "target", CreatedBy: "actor", Status: "queued"}
	if _, err := r.CreateRelease(ctx, value); !errors.Is(err, ErrDeploymentConflict) {
		t.Fatalf("busy environment accepted: %v", err)
	}
	var persisted model.StepRun
	if err := db.Where("step_run_id = ?", step).First(&persisted).Error; err != nil || persisted.DeploymentReleaseID != "" {
		t.Fatalf("failed transaction left step link: %+v %v", persisted, err)
	}
	value.EnvironmentID = "free"
	first, err := r.CreateRelease(ctx, value)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Where("step_run_id = ?", step).First(&persisted).Error; err != nil || persisted.DeploymentReleaseID != first.ReleaseID {
		t.Fatalf("missing atomic step link: %+v %v", persisted, err)
	}
	if same, err := r.CreateRelease(ctx, value); err != nil || same.ReleaseID != first.ReleaseID {
		t.Fatalf("same request not idempotent: %+v %v", same, err)
	}
	if changed, err := r.Transition(ctx, first, "cancelled", map[string]any{"reason": "cancel before apply"}); err != nil || !changed {
		t.Fatal(err)
	}
	second := *value
	second.BaseModel = model.BaseModel{}
	second.ReleaseID = "second"
	second.RequestID = "second-request"
	if _, err := r.CreateRelease(ctx, &second); !errors.Is(err, ErrDeploymentConflict) {
		t.Fatalf("same step created new release after terminal first: %v", err)
	}
	var count int64
	db.Model(&model.DeploymentRelease{}).Count(&count)
	if count != 1 {
		t.Fatalf("duplicate release count: %d", count)
	}
}
