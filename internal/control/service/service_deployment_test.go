package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	cluster "github.com/arcentrix/arcentra/internal/shared/deployment"
	"gorm.io/gorm"
)

type releaseFakeAdapter struct {
	inspect, apply, observe    int
	applyErr, errorObservation error
	observation                cluster.Observation
}

func (f *releaseFakeAdapter) Inspect(context.Context, cluster.Target) (cluster.Receipt, error) {
	f.inspect++
	return cluster.Receipt{UID: "workload", Generation: 7}, nil
}
func (f *releaseFakeAdapter) Apply(context.Context, cluster.Target, string, string, cluster.Receipt) (cluster.Receipt, error) {
	f.apply++
	return cluster.Receipt{UID: "workload", Generation: 8}, f.applyErr
}
func (f *releaseFakeAdapter) Observe(context.Context, cluster.Target, string, string, cluster.Receipt) (cluster.Observation, error) {
	f.observe++
	return f.observation, f.errorObservation
}
func releaseFixture(t *testing.T) (*DeploymentService, *gorm.DB, DeploymentReleaseInput, *releaseFakeAdapter) {
	t.Helper()
	files, db, artifact := artifactServiceFixture(t)
	if err := db.AutoMigrate(&model.DeploymentEnvironment{}, &model.DeploymentRelease{}, &model.ApprovalRequest{}); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"image":"registry.example/team/app@sha256:` + strings.Repeat("a", 64) + `"}`)
	sum := sha256.Sum256(manifest)
	if err := os.WriteFile(filepath.Join(files.root, artifact.StoragePath), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.ProjectArtifact{}).Where("artifact_id = ?", artifact.ArtifactID).Updates(map[string]any{"size": len(manifest), "sha256": hex.EncodeToString(sum[:])}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.PipelineRun{}).Where("run_id = ?", "run").Updates(map[string]any{"status": model.PipelineStatusSuccess, "commit_sha": strings.Repeat("b", 40)}).Error; err != nil {
		t.Fatal(err)
	}
	database := artifactServiceDatabase{db}
	adapter := &releaseFakeAdapter{observation: cluster.Observation{Status: "succeeded", Reason: "all updated replicas available", ObservedGeneration: 8, DesiredReplicas: 2, UpdatedReplicas: 2, AvailableReplicas: 2}}
	service := NewDeploymentService(repo.NewDeploymentRepo(database), repo.NewPipelineRepo(database), files.artifacts, files, repo.NewApprovalRepo(database), DeploymentTargets{"staging": {ProjectID: "project", Target: cluster.Target{Namespace: "staging", Deployment: "web", Container: "app"}, Adapter: adapter}})
	environment, err := service.CreateEnvironment(context.Background(), "project", DeploymentEnvironmentInput{Name: "Staging", TargetRef: "staging", Namespace: "staging", Deployment: "web", Container: "app"})
	if err != nil {
		t.Fatal(err)
	}
	return service, db, DeploymentReleaseInput{RequestID: "request", EnvironmentID: environment.EnvironmentID, PipelineID: "pipe", PipelineRunID: "run", ArtifactID: artifact.ArtifactID}, adapter
}
func TestDeploymentReleaseLifecycleAndImmutableIdempotency(t *testing.T) {
	s, _, input, adapter := releaseFixture(t)
	ctx := context.Background()
	release, err := s.CreateRelease(ctx, "project", "user", input)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.CreateRelease(ctx, "project", "user", input)
	if err != nil || duplicate.ReleaseID != release.ReleaseID {
		t.Fatalf("idempotency %+v %v", duplicate, err)
	}
	changed := input
	changed.ArtifactID = "different"
	if _, err := s.CreateRelease(ctx, "project", "user", changed); !errors.Is(err, repo.ErrDeploymentConflict) {
		t.Fatalf("request rebound %v", err)
	}
	second := input
	second.RequestID = "second"
	if _, err := s.CreateRelease(ctx, "project", "user", second); !errors.Is(err, repo.ErrDeploymentConflict) {
		t.Fatalf("concurrent release accepted %v", err)
	}
	if _, err := s.GetRelease(ctx, "other", release.ReleaseID); !errors.Is(err, ErrDeploymentNotFound) {
		t.Fatalf("cross project read %v", err)
	}
	release, err = s.Advance(ctx, "project", release.ReleaseID)
	if err != nil || release.Status != "observing" || adapter.observe != 0 {
		t.Fatalf("apply faked health %+v %v", release, err)
	}
	if _, err := s.Cancel(ctx, "project", release.ReleaseID); !errors.Is(err, ErrDeploymentCancel) {
		t.Fatalf("active cancellation accepted %v", err)
	}
	release, err = s.Advance(ctx, "project", release.ReleaseID)
	if err != nil || release.Status != "succeeded" || release.LastObservedAt == nil || release.FinishedAt == nil {
		t.Fatalf("observation %+v %v", release, err)
	}
	if _, err = s.Advance(ctx, "project", release.ReleaseID); err != nil || adapter.apply != 1 || adapter.observe != 1 {
		t.Fatal("terminal release was reexecuted")
	}
	if _, err := s.CreateRelease(ctx, "project", "user", second); err != nil {
		t.Fatalf("environment not released %v", err)
	}
}
func TestDeploymentUnconfiguredExactScopeAndSafeCancel(t *testing.T) {
	for _, scope := range []string{"missing registry", "wrong project", "wrong namespace", "wrong container"} {
		t.Run(scope, func(t *testing.T) {
			s, _, input, adapter := releaseFixture(t)
			release, err := s.CreateRelease(context.Background(), "project", "user", input)
			if err != nil {
				t.Fatal(err)
			}
			binding := s.targets["staging"]
			switch scope {
			case "missing registry":
				s.targets = nil
			case "wrong project":
				binding.ProjectID = "other"
				s.targets["staging"] = binding
			case "wrong namespace":
				binding.Target.Namespace = "production"
				s.targets["staging"] = binding
			case "wrong container":
				binding.Target.Container = "admin"
				s.targets["staging"] = binding
			}
			if _, err := s.Advance(context.Background(), "project", release.ReleaseID); !errors.Is(err, cluster.ErrUnconfigured) {
				t.Fatalf("not fail closed %v", err)
			}
			if adapter.inspect+adapter.apply+adapter.observe != 0 {
				t.Fatal("unauthorized target accessed")
			}
			environments, _ := s.Environments(context.Background(), "project", 1, 20)
			if environments.List[0].Configured {
				t.Fatal("claimed configured")
			}
			result, err := s.Cancel(context.Background(), "project", release.ReleaseID)
			if err != nil || result.Status != "cancelled" {
				t.Fatalf("cancel %v", err)
			}
			if _, err := s.Cancel(context.Background(), "project", release.ReleaseID); err != nil {
				t.Fatal("cancel not idempotent")
			}
		})
	}
}
func TestDeploymentUnknownApplyRetainsClaimAndRetries(t *testing.T) {
	s, _, input, adapter := releaseFixture(t)
	ctx := context.Background()
	release, err := s.CreateRelease(ctx, "project", "user", input)
	if err != nil {
		t.Fatal(err)
	}
	adapter.applyErr = cluster.ErrUnavailable
	if _, err := s.Advance(ctx, "project", release.ReleaseID); !errors.Is(err, cluster.ErrUnavailable) {
		t.Fatal(err)
	}
	after, _ := s.GetRelease(ctx, "project", release.ReleaseID)
	if after.Status != "applying" || after.WorkloadUID != "workload" || after.BaselineGeneration != 7 {
		t.Fatalf("lost durable baseline %+v", after)
	}
	if _, err := s.Cancel(ctx, "project", release.ReleaseID); !errors.Is(err, ErrDeploymentCancel) {
		t.Fatal("ambiguous apply cancelled")
	}
	adapter.applyErr = nil
	after, err = s.Advance(ctx, "project", release.ReleaseID)
	if err != nil || after.Status != "observing" || adapter.inspect != 1 {
		t.Fatalf("failed recovery %+v %v", after, err)
	}
	adapter.errorObservation = cluster.ErrUnavailable
	if _, err := s.Advance(ctx, "project", release.ReleaseID); err == nil {
		t.Fatal("API failure considered success")
	}
	after, _ = s.GetRelease(ctx, "project", release.ReleaseID)
	if after.Status != "observing" {
		t.Fatal("unknown health became terminal")
	}
}
func TestDeploymentSourceValidationAndFiltering(t *testing.T) {
	for _, bad := range []string{"pending run", "missing commit", "pending approval", "wrong run", "wrong project", "tampered artifact", "mutable image"} {
		t.Run(bad, func(t *testing.T) {
			s, db, input, _ := releaseFixture(t)
			switch bad {
			case "pending run":
				db.Model(&model.PipelineRun{}).Where("run_id = ?", "run").Update("status", model.PipelineStatusRunning)
			case "missing commit":
				db.Model(&model.PipelineRun{}).Where("run_id = ?", "run").Update("commit_sha", "")
			case "pending approval":
				db.Create(&model.ApprovalRequest{ApprovalID: "pending", PipelineRunID: "run", Status: model.ApprovalStatusPending})
			case "wrong run":
				input.PipelineRunID = "other"
			case "wrong project":
				db.Model(&model.Pipeline{}).Where("pipeline_id = ?", "pipe").Update("project_id", "other")
			case "tampered artifact":
				db.Model(&model.ProjectArtifact{}).Where("artifact_id = ?", input.ArtifactID).Update("sha256", strings.Repeat("c", 64))
			case "mutable image":
				entry, _ := s.artifacts.Get(context.Background(), "project", input.ArtifactID)
				body := []byte(`{"image":"app:latest"}`)
				sum := sha256.Sum256(body)
				_ = os.WriteFile(filepath.Join(s.files.root, entry.StoragePath), body, 0600)
				db.Model(&model.ProjectArtifact{}).Where("artifact_id = ?", input.ArtifactID).Updates(map[string]any{"size": len(body), "sha256": hex.EncodeToString(sum[:])})
			}
			if _, err := s.CreateRelease(context.Background(), "project", "user", input); !errors.Is(err, ErrDeploymentSource) {
				t.Fatalf("bad source accepted %v", err)
			}
		})
	}
	s, _, input, _ := releaseFixture(t)
	_, err := s.CreateRelease(context.Background(), "project", "user", input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Releases(context.Background(), "project", 1, 20, func(string) (bool, error) { return false, nil })
	if err != nil || result.Total != 0 || len(result.List) != 0 {
		t.Fatalf("unauthorized history leaked %+v %v", result, err)
	}
}
func TestDeploymentConfigurationDisabledDoesNoIO(t *testing.T) {
	s, _, _, _ := releaseFixture(t)
	if err := ConfigureDeployment(context.Background(), s, cluster.Config{Targets: []cluster.TargetConfig{{Server: "bad", BearerTokenFile: "/nonexistent/private"}}}); err != nil || len(s.targets) != 0 {
		t.Fatalf("disabled config used %v", err)
	}
	if err := ConfigureDeployment(context.Background(), s, cluster.Config{Enabled: true, Targets: []cluster.TargetConfig{{Ref: "target", ProjectID: "project", Server: "https://secret.invalid", BearerTokenFile: "/nonexistent/private"}}}); err == nil || strings.Contains(err.Error(), "secret.invalid") || len(s.targets) != 0 {
		t.Fatalf("bad config accepted or leaked %v", err)
	}
}

func TestDeploymentEnvironmentCrossProjectDeclarationsCannotSquatTarget(t *testing.T) {
	s, _, _, _ := releaseFixture(t)
	input := DeploymentEnvironmentInput{Name: "Other project declaration", TargetRef: "staging", Namespace: "staging", Deployment: "web", Container: "app"}
	result, err := s.CreateEnvironment(context.Background(), "other-project", input)
	if err != nil || result.Configured {
		t.Fatalf("independent declaration collided or received access: %+v %v", result, err)
	}
}

func TestDeploymentTargetIdentityCannotChangeAfterPreparation(t *testing.T) {
	s, _, input, adapter := releaseFixture(t)
	ctx := context.Background()
	release, err := s.CreateRelease(ctx, "project", "user", input)
	if err != nil {
		t.Fatal(err)
	}
	binding := s.targets["staging"]
	binding.Fingerprint = "changed-cluster-identity"
	s.targets["staging"] = binding
	if _, err := s.Advance(ctx, "project", release.ReleaseID); !errors.Is(err, ErrDeploymentTargetChanged) {
		t.Fatalf("retargeted queued release: %v", err)
	}
	if adapter.inspect+adapter.apply != 0 {
		t.Fatal("changed target was accessed")
	}
	if _, err := s.Cancel(ctx, "project", release.ReleaseID); err != nil {
		t.Fatal(err)
	}
	s.targets = nil
	input.RequestID = "unconfigured-request"
	if _, err := s.CreateRelease(ctx, "project", "user", input); !errors.Is(err, cluster.ErrUnconfigured) {
		t.Fatalf("unconfigured target acquired release identity: %v", err)
	}
}

func TestDeploymentManualSourceUsesDefinitionRevisionWithoutSourceClaim(t *testing.T) {
	s, db, input, _ := releaseFixture(t)
	if err := db.Model(&model.PipelineRun{}).Where("run_id = ?", input.PipelineRunID).Updates(map[string]any{"commit_sha": "", "definition_commit_sha": strings.Repeat("d", 40)}).Error; err != nil {
		t.Fatal(err)
	}
	release, err := s.CreateRelease(context.Background(), "project", "user", input)
	if err != nil || release.CommitSHA != strings.Repeat("d", 40) {
		t.Fatalf("definition revision missing: %+v %v", release, err)
	}
	if _, err = s.Advance(context.Background(), "project", release.ReleaseID); err != nil {
		t.Fatal(err)
	}
}
