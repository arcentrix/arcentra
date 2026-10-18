package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	cluster "github.com/arcentrix/arcentra/internal/shared/deployment"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/bytedance/sonic"
	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const lifecycleImage = "registry.example/team/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// This fake is only the Kubernetes API boundary. The controller, shell executor,
// upload executor, managed files, repositories, release service, and adapter are real.
type lifecycleKubernetes struct {
	mu                       sync.Mutex
	patches, gets            int
	generation               int64
	image, releaseID         string
	ready, losePatchResponse bool
	afterRead                func(int)
	applied                  chan struct{}
	applyOnce                sync.Once
}

func newLifecycleKubernetes(t *testing.T) (*lifecycleKubernetes, *cluster.KubernetesAdapter) {
	t.Helper()
	k := &lifecycleKubernetes{generation: 7, image: "registry.example/team/app:old", applied: make(chan struct{})}
	server := httptest.NewTLSServer(http.HandlerFunc(k.serveHTTP))
	t.Cleanup(server.Close)
	adapter, err := cluster.NewKubernetesAdapter(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return k, adapter
}

func (k *lifecycleKubernetes) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/apis/apps/v1/namespaces/staging/deployments/web" {
		http.Error(w, "unexpected target", http.StatusNotFound)
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	switch r.Method {
	case http.MethodGet:
		k.gets++
		if k.afterRead != nil {
			k.afterRead(k.gets)
		}
	case http.MethodPatch:
		if r.Header.Get("Content-Type") != "application/json-patch+json" {
			http.Error(w, "wrong patch type", http.StatusBadRequest)
			return
		}
		raw, err := io.ReadAll(r.Body)
		var operations []struct {
			Op    string `json:"op"`
			Path  string `json:"path"`
			Value any    `json:"value"`
		}
		if err != nil || sonic.Unmarshal(raw, &operations) != nil || len(operations) != 6 {
			http.Error(w, "invalid conditional patch", http.StatusBadRequest)
			return
		}
		for i, path := range []string{"/metadata/uid", "/metadata/resourceVersion", "/metadata/generation", "/spec/template/spec/containers/0/name"} {
			if operations[i].Op != "test" || operations[i].Path != path {
				http.Error(w, "missing precondition", http.StatusBadRequest)
				return
			}
		}
		if operations[0].Value != "lifecycle-workload" || operations[1].Value != fmt.Sprint(k.generation) || operations[2].Value != float64(k.generation) || operations[3].Value != "app" {
			http.Error(w, "precondition failed", http.StatusConflict)
			return
		}
		image, imageOK := operations[4].Value.(string)
		release, releaseOK := operations[5].Value.(string)
		if !imageOK || !releaseOK || operations[4].Op != "replace" || operations[4].Path != "/spec/template/spec/containers/0/image" || operations[5].Op != "add" || operations[5].Path != "/spec/template/metadata/annotations/arcentra.io~1release-id" {
			http.Error(w, "unexpected mutation", http.StatusBadRequest)
			return
		}
		k.patches++
		k.generation++
		k.image, k.releaseID = image, release
		k.applyOnce.Do(func() { close(k.applied) })
		if k.losePatchResponse {
			// The API committed the patch, but the caller never receives its receipt.
			if hijacker, ok := w.(http.Hijacker); ok {
				connection, _, err := hijacker.Hijack()
				if err == nil {
					_ = connection.Close()
					return
				}
			}
			http.Error(w, "response lost after commit", http.StatusServiceUnavailable)
			return
		}
	default:
		http.Error(w, "unexpected operation", http.StatusMethodNotAllowed)
		return
	}
	observed, available := k.generation-1, 0
	if k.ready {
		observed, available = k.generation, 2
	}
	payload := map[string]any{
		"metadata": map[string]any{"uid": "lifecycle-workload", "resourceVersion": fmt.Sprint(k.generation), "generation": k.generation},
		"spec": map[string]any{"replicas": 2, "template": map[string]any{
			"metadata": map[string]any{"annotations": map[string]string{"arcentra.io/release-id": k.releaseID}},
			"spec":     map[string]any{"containers": []any{map[string]string{"name": "app", "image": k.image}}},
		}},
		"status": map[string]any{"observedGeneration": observed, "replicas": 2, "updatedReplicas": 2, "availableReplicas": available, "unavailableReplicas": 2 - available},
	}
	body, err := sonic.Marshal(payload)
	if err != nil {
		http.Error(w, "fixture encoding failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func (k *lifecycleKubernetes) setReady(ready bool) { k.mu.Lock(); defer k.mu.Unlock(); k.ready = ready }
func (k *lifecycleKubernetes) patchCount() int     { k.mu.Lock(); defer k.mu.Unlock(); return k.patches }

type lifecycleFixture struct {
	db                                   *gorm.DB
	repos                                *repo.Repositories
	engine                               *Process
	deployment                           *service.DeploymentService
	kube                                 *lifecycleKubernetes
	allowed                              atomic.Bool
	authorizations                       atomic.Int64
	projectID, pipelineID, environmentID string
}

func newLifecycleFixture(t *testing.T, policies ...*cluster.PipelineApprovalPolicy) *lifecycleFixture {
	t.Helper()
	t.Setenv("ARCENTRA_ARTIFACT_ROOT", filepath.Join(t.TempDir(), "managed"))
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "lifecycle.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err = db.AutoMigrate(&model.Project{}, &model.Pipeline{}, &model.PipelineRun{}, &model.JobRun{}, &model.StepRun{}, &model.StepRunLog{}, &model.ProjectArtifact{}, &model.DeploymentEnvironment{}, &model.DeploymentRelease{}, &model.ApprovalRequest{}, &model.ApprovalDecision{}); err != nil {
		t.Fatal(err)
	}
	adapter := processTestDB{db}
	repos := &repo.Repositories{Pipeline: repo.NewPipelineRepo(adapter), JobRun: repo.NewJobRunRepo(adapter), StepRun: repo.NewStepRunRepo(adapter), Log: repo.NewLogRepository(db), Artifact: repo.NewArtifactRepo(adapter), Deployment: repo.NewDeploymentRepo(adapter), Approval: repo.NewApprovalRepo(adapter), ApprovalDecision: repo.NewApprovalDecisionRepo(adapter)}
	f := &lifecycleFixture{db: db, repos: repos, projectID: "lifecycle-project", pipelineID: id.GetUild()}
	f.allowed.Store(true)
	kube, clusterAdapter := newLifecycleKubernetes(t)
	f.kube = kube
	var approvalPolicy *cluster.PipelineApprovalPolicy
	if len(policies) > 0 {
		approvalPolicy = policies[0]
	}
	f.deployment = service.NewDeploymentService(repos.Deployment, repos.Pipeline, repos.Artifact, service.NewArtifactService(repos.Artifact, nil, artifact.Root()), repos.Approval, service.DeploymentTargets{"staging": {PipelineApproval: approvalPolicy, ProjectID: f.projectID, Tier: "staging", AllowPipelineDeployments: true, Target: cluster.Target{Namespace: "staging", Deployment: "web", Container: "app"}, Adapter: clusterAdapter}})
	f.configureService()
	approvalAuthorize := func(ctx context.Context, actor, project, pipeline, environment string) error {
		if ctx.Err() != nil || actor == "lifecycle-actor" || actor == "" || project != f.projectID || pipeline != f.pipelineID || environment != f.environmentID {
			return errors.New("fixture approval permission denied")
		}
		return nil
	}
	f.deployment.ConfigureDeploymentApprovals(repos.ApprovalDecision, approvalAuthorize, approvalAuthorize)
	environment, err := f.deployment.CreateEnvironment(context.Background(), f.projectID, service.DeploymentEnvironmentInput{Name: "Staging", TargetRef: "staging", Namespace: "staging", Deployment: "web", Container: "app"})
	if err != nil {
		t.Fatal(err)
	}
	f.environmentID = environment.EnvironmentID
	for _, value := range []any{&model.Project{ProjectID: f.projectID, IsEnabled: 1, Status: model.ProjectStatusActive}, &model.Pipeline{PipelineID: f.pipelineID, ProjectID: f.projectID, Name: "lifecycle", IsEnabled: 1}} {
		if err = db.WithContext(context.Background()).Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	f.engine = f.newProcess()
	t.Cleanup(f.engine.Stop)
	return f
}

func (f *lifecycleFixture) configureService() {
	f.deployment.ConfigurePipelineDeployments(f.repos.JobRun, f.repos.StepRun, func(ctx context.Context, actor, project, pipeline, environment string) error {
		f.authorizations.Add(1)
		if err := ctx.Err(); err != nil {
			return err
		}
		if !f.allowed.Load() || actor != "lifecycle-actor" || project != f.projectID || pipeline != f.pipelineID || environment != f.environmentID {
			return errors.New("fixture deployment permission denied")
		}
		return nil
	})
}

func (f *lifecycleFixture) newProcess() *Process {
	engine := NewProcess(f.repos, nil, nil, nil, &log.Logger{SugaredLogger: zap.NewNop().Sugar()}, nil, nil)
	engine.ConfigurePipelineDeployments(f.deployment)
	return engine
}

func (f *lifecycleFixture) pipeline() *spec.Pipeline {
	return &spec.Pipeline{Namespace: "lifecycle", Jobs: []*spec.Job{
		{Name: "build", Steps: []*spec.Step{shellStep("produce", "printf '%s' '{\"image\":\""+lifecycleImage+"\"}' > manifest.json"), {Name: "publish", Uses: "builtin:artifacts", Action: "upload", Args: spec.MapAsStruct(map[string]any{"paths": []any{"manifest.json"}, "expireIn": "7d"})}}},
		{Name: "deploy", Timeout: "30s", DependsOn: []string{"build"}, Steps: []*spec.Step{{Name: "release", Uses: "builtin:kubernetes", Action: "deploy", Args: spec.MapAsStruct(map[string]any{"environmentId": f.environmentID, "artifact": map[string]any{"job": "build", "step": "publish", "name": "manifest.json"}})}}},
	}}
}

func (f *lifecycleFixture) submit(t *testing.T, definition *spec.Pipeline) *model.PipelineRun {
	t.Helper()
	plan, err := cluster.DecodePipelinePlan(definition)
	if err != nil {
		t.Fatal(err)
	}
	canonical, digest, err := plan.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := f.deployment.PipelineDeploymentTargetFingerprint(context.Background(), f.projectID, f.environmentID)
	if err != nil {
		t.Fatal(err)
	}
	run := &model.PipelineRun{RunID: id.GetUild(), PipelineID: f.pipelineID, Status: model.PipelineStatusPending, TotalJobs: len(definition.Jobs), TriggerType: int(pipelinev1.TriggerType_TRIGGER_TYPE_MANUAL), TriggeredBy: "lifecycle-actor", DefinitionCommitSha: strings.Repeat("b", 40), AllowDeployments: true, DeploymentActorID: "lifecycle-actor", DeploymentPlan: canonical, DeploymentPlanHash: digest, DeploymentTargetFingerprint: fingerprint}
	if err := f.repos.Pipeline.CreateRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.Submit(run, definition); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.engine.Stop()
		_ = os.RemoveAll(filepath.Join(os.TempDir(), "pipelines", f.pipelineID, run.RunID))
	})
	return run
}

func (f *lifecycleFixture) releases(t *testing.T) []model.DeploymentRelease {
	t.Helper()
	rows, _, err := f.repos.Deployment.ListReleases(context.Background(), f.projectID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func (f *lifecycleFixture) oneRelease(t *testing.T) *model.DeploymentRelease {
	t.Helper()
	rows := f.releases(t)
	if len(rows) != 1 {
		t.Fatalf("release count = %d; want exactly one", len(rows))
	}
	return &rows[0]
}

func (f *lifecycleFixture) waitRelease(t *testing.T, status string) *model.DeploymentRelease {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		rows := f.releases(t)
		if len(rows) == 1 && rows[0].Status == status {
			return &rows[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("release did not reach %s: %+v", status, f.releases(t))
	return nil
}

func TestPipelineReleaseIntegrationPublishesRegisteredArtifactAndWaitsForObservation(t *testing.T) {
	f := newLifecycleFixture(t)
	run := f.submit(t, f.pipeline())
	select {
	case <-f.kube.applied:
	case <-time.After(8 * time.Second):
		t.Fatal("controller did not submit the registered image manifest")
	}
	observing := f.waitRelease(t, "observing")
	inProgress, err := f.repos.Pipeline.GetRun(context.Background(), run.RunID)
	if err != nil || inProgress.Status != model.PipelineStatusRunning {
		t.Fatalf("PATCH was mistaken for terminal rollout success: %+v %v", inProgress, err)
	}
	if observing.LastObservedAt != nil && observing.ObservedGeneration >= observing.AppliedGeneration {
		t.Fatal("stale Kubernetes generation accepted as current")
	}
	f.kube.setReady(true)
	completed := waitRun(t, f.repos, run.RunID, model.PipelineStatusSuccess)
	f.engine.Stop()
	release := f.oneRelease(t)
	if release.Status != "succeeded" || release.LastObservedAt == nil || release.FinishedAt == nil || release.ObservedGeneration != release.AppliedGeneration || release.Image != lifecycleImage || release.Origin != "pipeline_job" || release.DeploymentPlanHash != run.DeploymentPlanHash || release.CommitSHA != run.DefinitionCommitSha || completed.CompletedJobs != 2 {
		t.Fatalf("observed release/run linkage incomplete: release=%+v run=%+v", release, completed)
	}
	artifacts, count, err := f.repos.Artifact.List(context.Background(), repo.ArtifactQuery{ProjectID: f.projectID, Limit: 20})
	if err != nil || count != 1 {
		t.Fatalf("real artifact registration: count=%d err=%v", count, err)
	}
	registered := artifacts[0]
	if registered.ArtifactID != release.ArtifactID || registered.SHA256 != release.ArtifactSHA256 || registered.JobRunID != release.ProducerJobRunID || registered.StepRunID != release.ProducerStepRunID {
		t.Fatalf("release did not pin registered producer identity: %+v %+v", release, registered)
	}
	if release.ExecutionStepRunID == nil {
		t.Fatal("release omitted execution step identity")
	}
	step, err := f.repos.StepRun.GetByStepRunID(context.Background(), *release.ExecutionStepRunID)
	if err != nil || step == nil || step.Status != 4 || step.DeploymentReleaseID != release.ReleaseID || step.JobRunID != release.ExecutionJobRunID {
		t.Fatalf("step/release linkage missing: %+v %v", step, err)
	}
	if f.kube.patchCount() != 1 || f.authorizations.Load() < 2 {
		t.Fatalf("patches=%d authorization checks=%d", f.kube.patchCount(), f.authorizations.Load())
	}
	if _, err := os.Stat(filepath.Join(artifact.Root(), registered.StoragePath)); err != nil {
		t.Fatalf("managed artifact lost during workspace cleanup: %v", err)
	}
}

func TestPipelineReleaseIntegrationBlocksUnusableProducers(t *testing.T) {
	for _, scenario := range []string{"failed producer", "skipped producer", "missing manifest", "expired manifest", "tampered manifest", "stale upload attempt"} {
		t.Run(scenario, func(t *testing.T) {
			f := newLifecycleFixture(t)
			definition := f.pipeline()
			var gate, releaseGate string
			switch scenario {
			case "failed producer":
				definition.Jobs[0].Steps[0] = shellStep("produce", "exit 19")
			case "skipped producer":
				definition.Jobs[0].When = "false"
			case "missing manifest":
				definition.Jobs[0].Steps[0] = shellStep("produce", "printf other > wrong.json")
				definition.Jobs[0].Steps[1].Args = spec.MapAsStruct(map[string]any{"paths": []any{"wrong.json"}})
			default:
				gate, releaseGate = filepath.Join(t.TempDir(), "ready"), filepath.Join(t.TempDir(), "continue")
				definition.Jobs = append(definition.Jobs, &spec.Job{Name: "gate", DependsOn: []string{"build"}, Steps: []*spec.Step{shellStep("wait", "touch '"+gate+"'; while [ ! -f '"+releaseGate+"' ]; do sleep 0.01; done")}})
				definition.Jobs[1].DependsOn = append(definition.Jobs[1].DependsOn, "gate")
			}
			run := f.submit(t, definition)
			if gate != "" {
				deadline := time.Now().Add(8 * time.Second)
				for {
					if _, err := os.Stat(gate); err == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("producer did not finish before gate")
					}
					time.Sleep(10 * time.Millisecond)
				}
				artifacts, count, err := f.repos.Artifact.List(context.Background(), repo.ArtifactQuery{ProjectID: f.projectID, Limit: 20})
				if err != nil || count != 1 {
					t.Fatalf("producer artifact count=%d error=%v", count, err)
				}
				registered := artifacts[0]
				switch scenario {
				case "expired manifest":
					err = f.db.WithContext(context.Background()).Model(&model.ProjectArtifact{}).Where("artifact_id = ?", registered.ArtifactID).Update("expires_at", time.Now().Add(-time.Minute)).Error
				case "tampered manifest":
					err = os.WriteFile(filepath.Join(artifact.Root(), registered.StoragePath), []byte("tampered"), 0600)
				case "stale upload attempt":
					// The old successful upload is no longer the latest executed attempt.
					original, readErr := f.repos.StepRun.GetByStepRunID(context.Background(), registered.StepRunID)
					if readErr != nil || original == nil {
						t.Fatalf("read upload: %v", readErr)
					}
					latest := *original
					latest.BaseModel = model.BaseModel{}
					latest.StepRunID, latest.Attempt, latest.PreviousStepRunID = id.GetUild(), 2, original.StepRunID
					latest.Status = 5
					err = f.repos.StepRun.Create(context.Background(), &latest)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(releaseGate, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			waitRun(t, f.repos, run.RunID, model.PipelineStatusFailed)
			if count := f.kube.patchCount(); count != 0 {
				t.Fatalf("unusable source patched Kubernetes %d times", count)
			}
			if releases := f.releases(t); len(releases) != 0 {
				t.Fatalf("unusable source acquired a release: %+v", releases)
			}
		})
	}
}

func TestPipelineReleaseIntegrationRevokedPermissionBeforeApplyPreventsPatch(t *testing.T) {
	for _, revokeAtRead := range []int{1, 2} {
		t.Run(fmt.Sprintf("Kubernetes read %d", revokeAtRead), func(t *testing.T) {
			f := newLifecycleFixture(t)
			f.kube.afterRead = func(read int) {
				if read == revokeAtRead {
					f.allowed.Store(false)
				}
			}
			run := f.submit(t, f.pipeline())
			waitRun(t, f.repos, run.RunID, model.PipelineStatusFailed)
			if f.kube.patchCount() != 0 {
				t.Fatal("permission revoked after a Kubernetes read still permitted PATCH")
			}
			release := f.oneRelease(t)
			if release.Status != "applying" {
				t.Fatalf("durable apply reservation changed unexpectedly: %+v", release)
			}
			environment, err := f.repos.Deployment.GetEnvironment(context.Background(), f.projectID, f.environmentID)
			if err != nil || environment.ActiveReleaseID != release.ReleaseID {
				t.Fatalf("applying reservation lost: %+v %v", environment, err)
			}
		})
	}
}

func TestPipelineReleaseIntegrationLiveRBACRevocationBeforePatch(t *testing.T) {
	for _, revoke := range []string{"team membership", "role grant"} {
		t.Run(revoke, func(t *testing.T) {
			f := newLifecycleFixture(t)
			ctx := context.Background()
			if err := f.db.WithContext(ctx).AutoMigrate(&model.User{}, &model.Team{}, &model.TeamMember{}, &model.Role{}, &model.RoleGrant{}, &model.Permission{}, &model.RolePermissionBinding{}); err != nil {
				t.Fatal(err)
			}
			for _, entry := range []any{
				&model.User{UserID: "lifecycle-actor", IsEnabled: 1},
				&model.Team{TeamID: "lifecycle-team", IsEnabled: 1},
				&model.TeamMember{TeamID: "lifecycle-team", UserID: "lifecycle-actor"},
				&model.Role{RoleID: "lifecycle-deployer", Name: "Deployer", IsEnabled: 1},
				&model.RoleGrant{GrantID: "lifecycle-grant", SubjectType: "team", SubjectID: "lifecycle-team", RoleID: "lifecycle-deployer", ScopeType: "project", ScopeID: f.projectID, IsEnabled: 1},
			} {
				if err := f.db.WithContext(ctx).Create(entry).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := f.db.WithContext(ctx).Model(&model.Project{}).Where("project_id = ?", f.projectID).Updates(map[string]any{"is_enabled": 1, "status": model.ProjectStatusActive}).Error; err != nil {
				t.Fatal(err)
			}
			for _, action := range []string{"project:read", "project:update", "pipeline:read", "pipeline:trigger", "release:deploy_staging"} {
				parts := strings.SplitN(action, ":", 2)
				if err := f.db.WithContext(ctx).Create(&model.Permission{PermissionID: action, ResourceType: parts[0], Action: parts[1]}).Error; err != nil {
					t.Fatal(err)
				}
				if err := f.db.WithContext(ctx).Create(&model.RolePermissionBinding{RoleID: "lifecycle-deployer", PermissionID: action}).Error; err != nil {
					t.Fatal(err)
				}
			}
			repositories := repo.NewRepositories(processTestDB{f.db}, nil)
			authorize := service.NewPipelineDeploymentAuthorizer(repositories, f.deployment)
			if err := authorize(ctx, "lifecycle-actor", f.projectID, f.pipelineID, f.environmentID); err != nil {
				t.Fatalf("initial real RBAC grant did not authorize the actor: %v", err)
			}
			f.deployment.ConfigurePipelineDeployments(f.repos.JobRun, f.repos.StepRun, authorize)
			revoked := make(chan error, 1)
			f.kube.afterRead = func(read int) {
				if read != 2 {
					return
				}
				// Change committed permission state after both the source checks and
				// workload inspection, immediately before the conditional PATCH.
				if revoke == "team membership" {
					revoked <- f.db.WithContext(ctx).Where("team_id = ? AND user_id = ?", "lifecycle-team", "lifecycle-actor").Delete(&model.TeamMember{}).Error
				} else {
					revoked <- f.db.WithContext(ctx).Model(&model.RoleGrant{}).Where("grant_id = ?", "lifecycle-grant").Update("is_enabled", 0).Error
				}
			}
			run := f.submit(t, f.pipeline())
			waitRun(t, f.repos, run.RunID, model.PipelineStatusFailed)
			select {
			case err := <-revoked:
				if err != nil {
					t.Fatalf("failed to revoke persisted RBAC state: %v", err)
				}
			default:
				t.Fatal("pipeline failed before reaching the actual RBAC revocation point")
			}
			if err := authorize(ctx, "lifecycle-actor", f.projectID, f.pipelineID, f.environmentID); !errors.Is(err, service.ErrDeploymentAuthorization) {
				t.Fatalf("committed RBAC revocation did not deny the actor: %v", err)
			}
			if f.kube.patchCount() != 0 || f.authorizations.Load() != 0 {
				t.Fatalf("revoked actual RBAC admitted PATCH or synthetic callback was used: patches=%d syntheticChecks=%d", f.kube.patchCount(), f.authorizations.Load())
			}
			release := f.oneRelease(t)
			environment, err := f.repos.Deployment.GetEnvironment(ctx, f.projectID, f.environmentID)
			if err != nil || release.Status != "applying" || environment.ActiveReleaseID != release.ReleaseID {
				t.Fatalf("revocation discarded the durable release reservation: %+v %+v %v", release, environment, err)
			}
		})
	}
}

func TestPipelineReleaseIntegrationRetriesSelectLatestExecutedUpload(t *testing.T) {
	for _, mode := range []string{"failed_step", "whole_job"} {
		t.Run(mode, func(t *testing.T) {
			f := newLifecycleFixture(t)
			f.kube.setReady(true)
			definition := f.pipeline()
			definition.Jobs[0].Retry = &spec.Retry{Mode: mode, MaxAttempts: 3, Idempotent: true, AllowedExitCodes: []int32{9}}
			definition.Jobs[0].Steps = append(definition.Jobs[0].Steps, shellStep("flaky", "if [ ! -f retried ]; then touch retried; exit 9; fi"))
			run := f.submit(t, definition)
			waitRun(t, f.repos, run.RunID, model.PipelineStatusSuccess)
			release := f.oneRelease(t)
			selected, err := f.repos.StepRun.GetByStepRunID(context.Background(), release.ProducerStepRunID)
			wantAttempt, wantArtifacts := 1, int64(1)
			if mode == "whole_job" {
				wantAttempt, wantArtifacts = 2, 2
			}
			if err != nil || selected == nil || selected.Attempt != wantAttempt || selected.Status != 4 {
				t.Fatalf("wrong executed producer attempt pinned: %+v %v", selected, err)
			}
			_, count, err := f.repos.Artifact.List(context.Background(), repo.ArtifactQuery{ProjectID: f.projectID, Limit: 20})
			if err != nil || count != wantArtifacts {
				t.Fatalf("registered attempt artifacts=%d, want %d: %v", count, wantArtifacts, err)
			}
			steps, _, err := f.repos.StepRun.List(context.Background(), repo.StepRunFilter{PipelineRunID: run.RunID, StepName: "publish", PageSize: 20})
			if err != nil || len(steps) != 3 {
				t.Fatalf("preallocated retry slots=%d: %v", len(steps), err)
			}
			if steps[2].Status != 8 || steps[2].StartTime != nil || f.kube.patchCount() != 1 {
				t.Fatalf("unused skipped retry slot replaced actual upload or repeated PATCH: %+v", steps)
			}
		})
	}
}

func TestPipelineReleaseIntegrationLostPatchResponseRequiresSameReleaseRecovery(t *testing.T) {
	f := newLifecycleFixture(t)
	f.kube.losePatchResponse = true
	run := f.submit(t, f.pipeline())
	waitRun(t, f.repos, run.RunID, model.PipelineStatusFailed)
	f.engine.Stop()
	release := f.oneRelease(t)
	if release.Status != "applying" || release.WorkloadUID != "lifecycle-workload" || release.BaselineGeneration != 7 || f.kube.patchCount() != 1 {
		t.Fatalf("ambiguous apply lost durable identity: %+v patches=%d", release, f.kube.patchCount())
	}
	if _, err := f.deployment.Cancel(context.Background(), f.projectID, release.ReleaseID); !errors.Is(err, service.ErrDeploymentCancel) {
		t.Fatalf("ambiguous apply cancellable: %v", err)
	}
	f.assertRestartDoesNotApply(t, run, release)
	f.assertInspectionAndAdvanceDoNotPatch(t, release)
	f.kube.setReady(true)
	recovered, err := f.deployment.RecoverAuthorized(context.Background(), f.projectID, release.ReleaseID, "lifecycle-actor")
	if err != nil || recovered == nil || recovered.Status != "observing" {
		t.Fatalf("explicit same-release apply recovery: %+v %v", recovered, err)
	}
	recovered, err = f.deployment.AdvanceAuthorized(context.Background(), f.projectID, release.ReleaseID, "lifecycle-actor")
	if err != nil || recovered == nil || recovered.Status != "succeeded" || recovered.ReleaseID != release.ReleaseID || f.kube.patchCount() != 1 || len(f.releases(t)) != 1 {
		t.Fatalf("recovery duplicated or replaced release: %+v %v patches=%d", recovered, err, f.kube.patchCount())
	}
	persisted, err := f.repos.Pipeline.GetRun(context.Background(), run.RunID)
	if err != nil || persisted.Status != model.PipelineStatusFailed {
		t.Fatalf("explicit recovery rewrote historical pipeline failure: %+v %v", persisted, err)
	}
}

func TestPipelineReleaseIntegrationCancellationAfterApplyPreservesReservation(t *testing.T) {
	f := newLifecycleFixture(t)
	run := f.submit(t, f.pipeline())
	f.waitRelease(t, "observing")
	if err := f.engine.CancelRun(run.RunID); err != nil {
		t.Fatal(err)
	}
	waitRun(t, f.repos, run.RunID, model.PipelineStatusCancelled)
	f.engine.Stop()
	release := f.oneRelease(t)
	environment, err := f.repos.Deployment.GetEnvironment(context.Background(), f.projectID, f.environmentID)
	if err != nil || release.Status != "observing" || environment.ActiveReleaseID != release.ReleaseID || f.kube.patchCount() != 1 {
		t.Fatalf("cancellation rolled back or released a live apply: %+v %+v %v", release, environment, err)
	}
	if _, err := f.deployment.Cancel(context.Background(), f.projectID, release.ReleaseID); !errors.Is(err, service.ErrDeploymentCancel) {
		t.Fatalf("applied release reported cancellable: %v", err)
	}
	f.assertRestartDoesNotApply(t, run, release)
}

func (f *lifecycleFixture) assertInspectionAndAdvanceDoNotPatch(t *testing.T, release *model.DeploymentRelease) {
	t.Helper()
	before := f.kube.patchCount()
	inspection, err := f.deployment.InspectAuthorized(context.Background(), f.projectID, release.ReleaseID, "lifecycle-actor")
	if err != nil || inspection == nil {
		t.Fatalf("read-only inspection failed: %+v %v", inspection, err)
	}
	if _, err := f.deployment.AdvanceAuthorized(context.Background(), f.projectID, release.ReleaseID, "lifecycle-actor"); !errors.Is(err, service.ErrPipelineDeploymentUncertain) {
		t.Fatalf("ordinary advance accepted ambiguous pipeline apply: %v", err)
	}
	if f.kube.patchCount() != before || len(f.releases(t)) != 1 {
		t.Fatal("read-only inspection or ordinary advance issued a replacement PATCH")
	}
}

func TestPipelineReleaseIntegrationCancellationDuringKubernetesReadFencesPatch(t *testing.T) {
	for _, cancelAtRead := range []int{1, 2} {
		t.Run(fmt.Sprintf("Kubernetes read %d", cancelAtRead), func(t *testing.T) {
			f := newLifecycleFixture(t)
			reached, continueRead := make(chan struct{}), make(chan struct{})
			var resume sync.Once
			defer resume.Do(func() { close(continueRead) })
			f.kube.afterRead = func(read int) {
				if read == cancelAtRead {
					close(reached)
					<-continueRead
				}
			}
			run := f.submit(t, f.pipeline())
			select {
			case <-reached:
			case <-time.After(8 * time.Second):
				t.Fatal("controller did not reach the selected Kubernetes read")
			}
			if err := f.engine.CancelRun(run.RunID); err != nil {
				t.Fatal(err)
			}
			resume.Do(func() { close(continueRead) })
			waitRun(t, f.repos, run.RunID, model.PipelineStatusCancelled)
			f.engine.Stop()
			release := f.oneRelease(t)
			if f.kube.patchCount() != 0 {
				t.Fatal("cancelled in-flight read was followed by PATCH")
			}
			environment, err := f.repos.Deployment.GetEnvironment(context.Background(), f.projectID, f.environmentID)
			if err != nil {
				t.Fatal(err)
			}
			if cancelAtRead == 1 {
				if release.Status != "cancelled" || environment.ActiveReleaseID != "" {
					t.Fatalf("unapplied queued release was not safely cancelled: %+v %+v", release, environment)
				}
				return
			}
			if release.Status != "applying" || environment.ActiveReleaseID != release.ReleaseID {
				t.Fatalf("potentially admitted apply lost reservation: %+v %+v", release, environment)
			}
			f.assertRestartDoesNotApply(t, run, release)
			f.assertInspectionAndAdvanceDoNotPatch(t, release)
			f.allowed.Store(false)
			if _, err := f.deployment.RecoverAuthorized(context.Background(), f.projectID, release.ReleaseID, "lifecycle-actor"); err == nil {
				t.Fatal("explicit recovery ignored newly revoked permission")
			}
			if f.kube.patchCount() != 0 {
				t.Fatal("unauthorized recovery issued PATCH")
			}
			f.allowed.Store(true)
			f.kube.setReady(true)
			recovered, err := f.deployment.RecoverAuthorized(context.Background(), f.projectID, release.ReleaseID, "lifecycle-actor")
			if err != nil || recovered == nil || recovered.ReleaseID != release.ReleaseID || recovered.Status != "observing" || f.kube.patchCount() != 1 {
				t.Fatalf("confirmed same-ID recovery did not apply once: %+v %v patches=%d", recovered, err, f.kube.patchCount())
			}
		})
	}
}

func (f *lifecycleFixture) assertRestartDoesNotApply(t *testing.T, run *model.PipelineRun, release *model.DeploymentRelease) {
	t.Helper()
	before := f.kube.patchCount()
	restarted := f.newProcess()
	defer restarted.Stop()
	if err := restarted.Submit(run, f.pipeline()); err == nil {
		t.Fatal("restarted controller accepted an already executed opt-in run")
	}
	if release.ExecutionStepRunID == nil {
		t.Fatal("release has no durable execution identity")
	}
	execution := service.PipelineDeploymentExecution{ProjectID: f.projectID, PipelineID: f.pipelineID, PipelineRunID: run.RunID, JobRunID: release.ExecutionJobRunID, StepRunID: *release.ExecutionStepRunID, ActorID: "lifecycle-actor"}
	if _, err := f.deployment.ExecutePipelineDeployment(context.Background(), execution, func(*model.DeploymentRelease) error { return nil }); err == nil {
		t.Fatal("runtime callback reentered a completed/cancelled pipeline")
	}
	if len(f.releases(t)) != 1 || f.kube.patchCount() != before {
		t.Fatal("controller restart or callback replay duplicated the pinned release/apply")
	}
}

type lifecycleTerminalFault struct {
	repo.IStepRunRepository
	fail     atomic.Bool
	failures atomic.Int64
}

func (r *lifecycleTerminalFault) PatchByStepRunID(ctx context.Context, stepID string, updates map[string]any) error {
	if updates["status"] == 4 && r.fail.Load() {
		step, err := r.IStepRunRepository.GetByStepRunID(ctx, stepID)
		if err != nil {
			return err
		}
		if step != nil && step.Uses == "builtin:kubernetes" {
			r.failures.Add(1)
			return errors.New("injected durable deployment step status failure")
		}
	}
	return r.IStepRunRepository.PatchByStepRunID(ctx, stepID, updates)
}

func TestPipelineReleaseIntegrationTerminalPersistenceFaultDoesNotRepeatApply(t *testing.T) {
	f := newLifecycleFixture(t)
	fault := &lifecycleTerminalFault{IStepRunRepository: f.repos.StepRun}
	fault.fail.Store(true)
	f.repos.StepRun = fault
	f.configureService()
	f.kube.setReady(true)
	run := f.submit(t, f.pipeline())
	waitRun(t, f.repos, run.RunID, model.PipelineStatusFailed)
	f.engine.Stop()
	release := f.oneRelease(t)
	if fault.failures.Load() == 0 || release.Status != "succeeded" || f.kube.patchCount() != 1 {
		t.Fatalf("fault did not follow successful observed release: %+v failures=%d patches=%d", release, fault.failures.Load(), f.kube.patchCount())
	}
	fault.fail.Store(false)
	f.assertRestartDoesNotApply(t, run, release)
	latest, err := f.deployment.AdvanceAuthorized(context.Background(), f.projectID, release.ReleaseID, "lifecycle-actor")
	if err != nil || latest.Status != "succeeded" || f.kube.patchCount() != 1 {
		t.Fatalf("terminal release replay repeated apply: %+v %v", latest, err)
	}
}
