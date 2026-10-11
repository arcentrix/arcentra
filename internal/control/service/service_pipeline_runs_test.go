package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/bytedance/sonic"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type pipelineServiceTestDB struct{ db *gorm.DB }

func (d pipelineServiceTestDB) Database() *gorm.DB { return d.db }

func runServiceFixture(t *testing.T) *PipelineServiceImpl {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "runs.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.Pipeline{}, &model.PipelineRun{}, &model.JobRun{}, &model.StepRun{}, &model.StepRunLog{}); err != nil {
		t.Fatal(err)
	}
	adapter := pipelineServiceTestDB{db}
	svc := &PipelineServiceImpl{pipelineRepo: repo.NewPipelineRepo(adapter), jobRunRepo: repo.NewJobRunRepo(adapter), stepRunRepo: repo.NewStepRunRepo(adapter), logAggregator: NewLogAggregator(repo.NewLogRepository(db))}
	t.Cleanup(func() { _ = svc.logAggregator.Close(context.Background()) })
	for _, suffix := range []string{"a", "b"} {
		if err := svc.pipelineRepo.Create(context.Background(), &model.Pipeline{PipelineID: "pipeline-" + suffix, Name: suffix, IsEnabled: 1}); err != nil {
			t.Fatal(err)
		}
		if err := svc.pipelineRepo.CreateRun(context.Background(), &model.PipelineRun{PipelineID: "pipeline-" + suffix, RunID: "run-" + suffix, Status: model.PipelineStatusRunning}); err != nil {
			t.Fatal(err)
		}
		if err := svc.jobRunRepo.Create(context.Background(), &model.JobRun{PipelineID: "pipeline-" + suffix, PipelineRunID: "run-" + suffix, JobRunID: "job-" + suffix, JobName: "build", Status: 3, Env: `{"TOKEN":"hidden-value"}`}); err != nil {
			t.Fatal(err)
		}
		if err := svc.stepRunRepo.Create(context.Background(), &model.StepRun{PipelineID: "pipeline-" + suffix, PipelineRunID: "run-" + suffix, JobRunID: "job-" + suffix, StepRunID: "step-" + suffix, Name: "shell", Status: 3, Env: `{"TOKEN":"hidden-value"}`, Secrets: `{"PASSWORD":"hidden-value"}`, Args: `{"token":"hidden-value"}`}); err != nil {
			t.Fatal(err)
		}
		if err := svc.logAggregator.PushLog(&LogEntry{StepRunID: "step-" + suffix, Timestamp: 1, LineNumber: 1, Content: "output-" + suffix}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = svc.logAggregator.CloseStream("step-" + suffix) })
	}
	return svc
}

func TestPipelineRunEndpointsEnforceRunAndStepScope(t *testing.T) {
	svc := runServiceFixture(t)
	ctx := context.Background()
	if _, err := svc.GetScopedPipelineRun(ctx, "pipeline-a", "run-b"); !errors.Is(err, ErrPipelineRunNotFound) {
		t.Fatalf("cross-pipeline run accepted: %v", err)
	}
	if _, err := svc.ListPipelineRunJobs(ctx, "pipeline-a", "run-b"); !errors.Is(err, ErrPipelineRunNotFound) {
		t.Fatalf("cross-pipeline jobs accepted: %v", err)
	}
	if _, _, err := svc.GetPipelineRunLogs(ctx, "pipeline-a", "run-a", "step-b", 0, 100); !errors.Is(err, ErrPipelineRunNotFound) {
		t.Fatalf("foreign step accepted: %v", err)
	}
	logs, truncated, err := svc.GetPipelineRunLogs(ctx, "pipeline-a", "run-a", "", 0, 100)
	if err != nil || truncated || len(logs) != 1 || logs[0].Message != "output-a" || logs[0].JobID != "job-a" {
		t.Fatalf("run log scope mismatch: %+v %v %v", logs, truncated, err)
	}
	jobs, err := svc.ListPipelineRunJobs(ctx, "pipeline-a", "run-a")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := sonic.MarshalString(jobs)
	for _, forbidden := range []string{"hidden-value", "secrets", `"env"`, `"args"`} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("job DTO exposes %q: %s", forbidden, raw)
		}
	}
	if len(jobs) != 1 || len(jobs[0].Steps) != 1 {
		t.Fatalf("missing nested steps: %+v", jobs)
	}
}

func TestStopPipelineRejectsCrossPipelineAndTerminalRuns(t *testing.T) {
	svc := runServiceFixture(t)
	ctx := context.Background()
	response, err := svc.StopPipeline(ctx, &pipelinev1.StopPipelineRequest{PipelineId: "pipeline-a", RunId: "run-b"})
	if err != nil || response.Success || response.Error.Code != 404 {
		t.Fatalf("cross-pipeline stop: %+v %v", response, err)
	}
	_ = svc.pipelineRepo.UpdateRun(ctx, "run-a", map[string]any{"status": model.PipelineStatusSuccess})
	response, err = svc.StopPipeline(ctx, &pipelinev1.StopPipelineRequest{PipelineId: "pipeline-a", RunId: "run-a"})
	if err != nil || response.Success || response.Error.Code != 409 {
		t.Fatalf("finished run mutated: %+v %v", response, err)
	}
	run, _ := svc.pipelineRepo.GetRun(ctx, "run-a")
	if run.Status != model.PipelineStatusSuccess {
		t.Fatal("terminal run was changed")
	}
}

func TestTriggerRejectsDisabledOrUnavailableEngine(t *testing.T) {
	svc := runServiceFixture(t)
	ctx := context.Background()
	_ = svc.pipelineRepo.Update(ctx, "pipeline-a", map[string]any{"is_enabled": 0})
	response, err := svc.TriggerPipeline(ctx, &pipelinev1.TriggerPipelineRequest{PipelineId: "pipeline-a"})
	if err != nil || response.Success || response.Error.Code != 409 {
		t.Fatalf("disabled pipeline accepted: %+v %v", response, err)
	}
	response, err = svc.TriggerPipeline(ctx, &pipelinev1.TriggerPipelineRequest{PipelineId: "pipeline-b"})
	if err != nil || response.Success || response.Error.Code != 503 {
		t.Fatalf("missing engine reported success: %+v %v", response, err)
	}
}

func TestDeletePipelineRemovesItFromListing(t *testing.T) {
	svc := runServiceFixture(t)
	ctx := context.Background()
	response, err := svc.DeletePipeline(ctx, &pipelinev1.DeletePipelineRequest{PipelineId: "pipeline-a"})
	if err != nil || !response.Success {
		t.Fatal(response, err)
	}
	list, total, err := svc.pipelineRepo.List(ctx, &repo.PipelineQuery{})
	if err != nil || total != 1 || list[0].PipelineID != "pipeline-b" {
		t.Fatalf("deleted pipeline still listed: %+v %d %v", list, total, err)
	}
	if _, err := svc.pipelineRepo.Get(ctx, "pipeline-a"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("deleted pipeline still readable: %v", err)
	}
}

func TestValidatedPipelinePath(t *testing.T) {
	for _, invalid := range []string{"", ".", "../outside", "/etc/passwd", ".git/config", "x/../../escape", `dir\\file.yml`} {
		if _, err := validatedPipelinePath(invalid); err == nil {
			t.Errorf("unsafe path accepted: %q", invalid)
		}
	}
	if path, err := validatedPipelinePath(".arcentra/build.yml"); err != nil || path != ".arcentra/build.yml" {
		t.Fatalf("valid path rejected: %q %v", path, err)
	}
}

func TestPipelineSCMCredentialsStayOnApprovedRepository(t *testing.T) {
	project := &model.Project{RepoURL: "https://git.example/team/app.git", AuthType: model.AuthTypeToken, Credential: "private-token"}
	for _, repository := range []string{"https://evil.example/team/app.git", "https://git.example/other/app.git", "http://git.example/team/app.git"} {
		if auth, err := scmAuthForPipeline(&model.Pipeline{RepoURL: repository}, project); err == nil || auth != nil {
			t.Fatalf("credentials allowed at unapproved repository %q", repository)
		}
	}
	auth, err := scmAuthForPipeline(&model.Pipeline{RepoURL: "https://git.example/team/app"}, project)
	if err != nil || auth["token"] != "private-token" {
		t.Fatalf("approved repository rejected: %v", err)
	}
	project.Credential = ""
	if _, err := scmAuthForPipeline(&model.Pipeline{RepoURL: "https://public.example/other.git"}, project); err != nil {
		t.Fatalf("credential-free source override rejected: %v", err)
	}
}

func TestSingleStepLogPagingUsesLineOrder(t *testing.T) {
	svc := runServiceFixture(t)
	if err := svc.logAggregator.PushLog(&LogEntry{StepRunID: "step-a", Timestamp: 0, LineNumber: 2, Content: "clock-skew"}); err != nil {
		t.Fatal(err)
	}
	entries, more, err := svc.GetPipelineRunLogs(context.Background(), "pipeline-a", "run-a", "step-a", 1, 1)
	if err != nil || !more || len(entries) != 1 || entries[0].LineNumber != 1 {
		t.Fatalf("line cursor ordering broken: %+v %v %v", entries, more, err)
	}
	entries, more, err = svc.GetPipelineRunLogs(context.Background(), "pipeline-a", "run-a", "step-a", 2, 1)
	if err != nil || more || len(entries) != 1 || entries[0].LineNumber != 2 {
		t.Fatalf("clock-skewed output skipped: %+v %v %v", entries, more, err)
	}
}

type completingPauseEngine struct {
	IPipelineEngine
	complete func()
	resumed  bool
}

func (e *completingPauseEngine) PauseRun(string) error  { e.complete(); return nil }
func (e *completingPauseEngine) ResumeRun(string) error { e.resumed = true; return nil }

func TestPauseCannotResurrectConcurrentCompletedRun(t *testing.T) {
	svc := runServiceFixture(t)
	ctx := context.Background()
	engine := &completingPauseEngine{complete: func() {
		if err := svc.pipelineRepo.UpdateRun(ctx, "run-a", map[string]any{"status": model.PipelineStatusSuccess}); err != nil {
			t.Fatal(err)
		}
	}}
	svc.engine = engine
	response, err := svc.PausePipeline(ctx, &pipelinev1.PausePipelineRequest{PipelineId: "pipeline-a", RunId: "run-a"})
	if err != nil || response.Success || response.Error.Code != 409 {
		t.Fatalf("completion race not rejected: %+v %v", response, err)
	}
	run, err := svc.pipelineRepo.GetRun(ctx, "run-a")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != model.PipelineStatusSuccess || !engine.resumed {
		t.Fatalf("terminal run resurrected or engine not restored: %+v", run)
	}
}
