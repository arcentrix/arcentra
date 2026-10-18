package process

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/log"
	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestProcessArtifactUploadSurvivesWorkspaceCleanup(t *testing.T) {
	ctx := context.Background()
	t.Setenv("ARCENTRA_ARTIFACT_ROOT", filepath.Join(t.TempDir(), "managed"))
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "local.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err = db.AutoMigrate(&model.Project{}, &model.Pipeline{}, &model.PipelineRun{}, &model.JobRun{}, &model.StepRun{}, &model.StepRunLog{}, &model.ProjectArtifact{}); err != nil {
		t.Fatal(err)
	}
	adapter := processTestDB{db}
	repos := &repo.Repositories{Pipeline: repo.NewPipelineRepo(adapter), JobRun: repo.NewJobRunRepo(adapter), StepRun: repo.NewStepRunRepo(adapter), Log: repo.NewLogRepository(db), Artifact: repo.NewArtifactRepo(adapter)}
	engine := NewProcess(repos, nil, nil, nil, &log.Logger{SugaredLogger: zap.NewNop().Sugar()}, nil, nil)
	t.Cleanup(engine.Stop)
	projectID := "artifact-project"
	pipelineID := id.GetUild()
	for _, value := range []any{&model.Project{ProjectID: projectID, IsEnabled: 1, Status: model.ProjectStatusActive}, &model.Pipeline{PipelineID: pipelineID, ProjectID: projectID, Name: "local-artifacts", IsEnabled: 1}} {
		if err = db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	run := &model.PipelineRun{RunID: id.GetUild(), PipelineID: pipelineID, Status: model.PipelineStatusPending, TotalJobs: 2}
	if err = repos.Pipeline.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	build := &spec.Pipeline{Namespace: "integration", Jobs: []*spec.Job{{Name: "build", Steps: []*spec.Step{shellStep("produce", "mkdir -p dist; printf durable-output > dist/result.txt"), {Name: "publish", Uses: "artifacts", Action: "upload", Args: spec.MapAsStruct(map[string]any{"paths": []any{"dist"}, "expireIn": "7d"})}}}}}
	build.Jobs = append(build.Jobs, &spec.Job{Name: "consume", DependsOn: []string{"build"}, Steps: []*spec.Step{{Name: "fetch", Uses: "artifacts", Action: "download", Args: spec.MapAsStruct(map[string]any{"job": "build"})}, shellStep("verify", "test \"$(cat dist/result.txt)\" = durable-output")}})
	if err = engine.Submit(run, build); err != nil {
		t.Fatal(err)
	}
	waitRun(t, repos, run.RunID, model.PipelineStatusSuccess)
	engine.Stop() // Includes deferred workspace cleanup, avoiding status/cleanup race.
	rows, count, err := repos.Artifact.List(ctx, repo.ArtifactQuery{ProjectID: projectID, Limit: 20})
	if err != nil || count != 1 {
		t.Fatalf("artifact count=%d error=%v", count, err)
	}
	entry := rows[0]
	if entry.Name != "dist/result.txt" || entry.ExpiresAt == nil || entry.ExpiresAt.Before(time.Now().Add(6*24*time.Hour)) {
		t.Fatalf("bad metadata: %+v", entry)
	}
	download, err := service.NewArtifactService(repos.Artifact, nil, artifact.Root()).Open(ctx, projectID, entry.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(download.Reader)
	_ = download.Reader.Close()
	if err != nil || string(data) != "durable-output" {
		t.Fatalf("download %q %v", data, err)
	}
	workspace := filepath.Join(os.TempDir(), "pipelines", pipelineID, run.RunID)
	t.Cleanup(func() { _ = os.RemoveAll(workspace) })
	source := filepath.Join(workspace, "integration", run.RunID, "artifacts", "build", "dist", "result.txt")
	if _, err = os.Stat(source); err != nil {
		t.Fatalf("source output not preserved: %v", err)
	}
	coordinator := &Coordinator{engine: engine, run: run, spec: build}
	if err = coordinator.checkLocalArtifacts(ctx, "build", []string{"dist"}); err != nil {
		t.Fatal(err)
	}
	// Exercise the second repository page: older requested output must remain readable.
	for index := 0; index < 501; index++ {
		filler := entry
		filler.BaseModel = model.BaseModel{}
		filler.Name = fmt.Sprintf("newer-%03d", index)
		filler.ArtifactID = artifact.ID(filler.ProjectID, filler.PipelineRunID, filler.JobRunID, filler.StepRunID, filler.Name)
		filler.StoragePath, _ = artifact.LocalPath(filler.ProjectID, filler.PipelineRunID, filler.JobRunID, filler.StepRunID, filler.Name)
		if err = db.Create(&filler).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err = coordinator.checkLocalArtifacts(ctx, "build", []string{"dist"}); err != nil {
		t.Fatalf("paginated source unavailable: %v", err)
	}
	sourceRoot := filepath.Dir(filepath.Dir(source))
	if err = coordinator.recordLocalArtifacts(ctx, entry.JobRunID, entry.StepRunID, sourceRoot, []string{"dist"}, "7d"); err != nil {
		t.Fatalf("idempotent registration counted twice: %v", err)
	}
	if err = os.Mkdir(filepath.Join(sourceRoot, "extra"), 0700); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 501; index++ {
		if err = os.WriteFile(filepath.Join(sourceRoot, "extra", fmt.Sprintf("%03d", index)), []byte("extra"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = coordinator.recordLocalArtifacts(ctx, entry.JobRunID, entry.StepRunID, sourceRoot, []string{"extra"}, "7d"); err == nil {
		t.Fatal("per-job artifact limit bypassed by another upload")
	}
	expired := time.Now().Add(-time.Minute)
	if err = db.Model(&model.ProjectArtifact{}).Where("artifact_id = ?", entry.ArtifactID).Update("expires_at", expired).Error; err != nil {
		t.Fatal(err)
	}
	if err = coordinator.checkLocalArtifacts(ctx, "build", []string{"dist"}); err == nil {
		t.Fatal("expired local source readable")
	}
	if err = db.Model(&model.ProjectArtifact{}).Where("artifact_id = ?", entry.ArtifactID).Update("expires_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(source, []byte("changed-output"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = coordinator.checkLocalArtifacts(ctx, "build", []string{"dist"}); err == nil {
		t.Fatal("changed local source readable")
	}
}

func TestManagedArtifactCopyIsImmutableAndConfined(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	target := filepath.Join(t.TempDir(), "managed")
	if err := os.WriteFile(filepath.Join(source, "output"), []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	size, hash, err := copyManagedArtifact(ctx, source, "output", target, "project/run/file")
	if err != nil || size != 3 || len(hash) != 64 {
		t.Fatalf("copy %d %s %v", size, hash, err)
	}
	if _, again, err := copyManagedArtifact(ctx, source, "output", target, "project/run/file"); err != nil || again != hash {
		t.Fatalf("repeat %s %v", again, err)
	}
	_ = os.WriteFile(filepath.Join(source, "output"), []byte("two"), 0600)
	if _, _, err := copyManagedArtifact(ctx, source, "output", target, "project/run/file"); err == nil {
		t.Fatal("immutable content overwritten")
	}
	if _, _, err := copyManagedArtifact(ctx, source, "output", target, "../escape"); err == nil {
		t.Fatal("traversal accepted")
	}
	outside := t.TempDir()
	_ = os.Symlink(outside, filepath.Join(target, "link"))
	if _, _, err := copyManagedArtifact(ctx, source, "output", target, "link/file"); err == nil {
		t.Fatal("destination symlink accepted")
	}
	_ = os.Symlink(filepath.Join(source, "output"), filepath.Join(source, "alias"))
	if _, _, err := copyManagedArtifact(ctx, source, "alias", target, "project/alias"); err == nil {
		t.Fatal("source symlink accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := copyManagedArtifact(cancelled, source, "output", target, "project/cancelled"); err == nil {
		t.Fatal("cancelled copy succeeded")
	}
}

func TestWorkspaceCleanupPreservesOutputsOnly(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"ns/run/artifacts/job/a", "ns/run/reports/job/r", "ns/run/workspace/job/source", "scratch"} {
		dest := filepath.Join(root, name)
		_ = os.MkdirAll(filepath.Dir(dest), 0700)
		_ = os.WriteFile(dest, []byte(name), 0600)
	}
	if err := cleanupRunWorkspace(root, "ns/run/artifacts", "ns/run/reports"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ns/run/artifacts/job/a", "ns/run/reports/job/r"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"ns/run/workspace", "scratch"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("working data retained: %s %v", name, err)
		}
	}
}
