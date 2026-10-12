package process

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/log"
	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type processTestDB struct{ db *gorm.DB }

func (db processTestDB) Database() *gorm.DB { return db.db }

func newTestProcess(t *testing.T) (*Process, *repo.Repositories) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "pipeline.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
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
	adapter := processTestDB{db}
	repos := &repo.Repositories{Log: repo.NewLogRepository(db), Pipeline: repo.NewPipelineRepo(adapter), JobRun: repo.NewJobRunRepo(adapter), StepRun: repo.NewStepRunRepo(adapter)}
	logger := &log.Logger{SugaredLogger: zap.NewNop().Sugar()}
	engine := NewProcess(repos, nil, nil, nil, logger, nil, nil)
	t.Cleanup(engine.Stop)
	return engine, repos
}

func submitTestPipeline(t *testing.T, engine *Process, repos *repo.Repositories, jobs ...*spec.Job) *model.PipelineRun {
	t.Helper()
	pipelineID := id.GetUild()
	if err := repos.Pipeline.Create(context.Background(), &model.Pipeline{PipelineID: pipelineID, Name: "integration", IsEnabled: 1}); err != nil {
		t.Fatal(err)
	}
	run := &model.PipelineRun{RunID: id.GetUild(), PipelineID: pipelineID, Status: model.PipelineStatusPending, Env: `{"INTEGRATION_VALUE":"real"}`, TotalJobs: len(jobs)}
	if err := repos.Pipeline.CreateRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := engine.Submit(run, &spec.Pipeline{Namespace: "integration", Jobs: jobs}); err != nil {
		t.Fatal(err)
	}
	return run
}

func waitRun(t *testing.T, repos *repo.Repositories, runID string, status int) *model.PipelineRun {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		run, err := repos.Pipeline.GetRun(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status == status {
			return run
		}
		if run.Status == model.PipelineStatusFailed || run.Status == model.PipelineStatusSuccess || run.Status == model.PipelineStatusCancelled {
			jobs, _ := repos.JobRun.ListByPipelineRunID(context.Background(), runID)
			for _, job := range jobs {
				t.Logf("job %s status %d error %s", job.JobName, job.Status, job.ErrorMessage)
			}
			t.Fatalf("run status %d; wanted %d", run.Status, status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run did not reach status %d", status)
	return nil
}

func shellStep(name, script string) *spec.Step {
	return &spec.Step{Name: name, Uses: "shell", Args: spec.MapAsStruct(map[string]any{"script": script})}
}

func TestProcessPersistsRealDAGAndSharedStepWorkspace(t *testing.T) {
	engine, repos := newTestProcess(t)
	marker := filepath.Join(t.TempDir(), "executions")
	run := submitTestPipeline(t, engine, repos,
		&spec.Job{Name: "slow", Steps: []*spec.Step{shellStep("write", "sleep 0.3; echo once >> '"+marker+"'; echo $INTEGRATION_VALUE > result"), shellStep("read", "test \"$(cat result)\" = real")}},
		&spec.Job{Name: "quick", Steps: []*spec.Step{shellStep("true", "true")}},
		&spec.Job{Name: "after", DependsOn: []string{"quick"}, Steps: []*spec.Step{shellStep("true", "true")}},
	)
	completed := waitRun(t, repos, run.RunID, model.PipelineStatusSuccess)
	if completed.CompletedJobs != 3 {
		t.Fatalf("completed jobs %d", completed.CompletedJobs)
	}
	jobs, err := repos.JobRun.ListByPipelineRunID(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 3 {
		t.Fatalf("duplicate/missing jobs: %d", len(jobs))
	}
	for _, job := range jobs {
		if job.Status != model.JobRunStatusSuccess || job.EndTime == nil {
			t.Fatalf("job not persisted: %+v", job)
		}
	}
	steps, count, err := repos.StepRun.List(context.Background(), repo.StepRunFilter{PipelineRunID: run.RunID, PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("steps = %d", count)
	}
	for _, step := range steps {
		if step.Status != 4 || step.ExitCode == nil || *step.ExitCode != 0 {
			t.Fatalf("step not successful: %+v", step)
		}
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "once") != 1 {
		t.Fatalf("job was dispatched repeatedly: %q", data)
	}
}

func TestProcessFailureBlocksDependentJobs(t *testing.T) {
	engine, repos := newTestProcess(t)
	run := submitTestPipeline(t, engine, repos, &spec.Job{Name: "fail", Steps: []*spec.Step{shellStep("bad", "exit 17"), shellStep("later", "true")}}, &spec.Job{Name: "blocked", DependsOn: []string{"fail"}, Steps: []*spec.Step{shellStep("not-run", "true")}})
	result := waitRun(t, repos, run.RunID, model.PipelineStatusFailed)
	if result.FailedJobs != 1 {
		t.Fatalf("failed jobs %d", result.FailedJobs)
	}
	jobs, _ := repos.JobRun.ListByPipelineRunID(context.Background(), run.RunID)
	if len(jobs) != 1 || jobs[0].Status != model.JobRunStatusFailed {
		t.Fatalf("unexpected jobs: %+v", jobs)
	}
	steps, _, _ := repos.StepRun.List(context.Background(), repo.StepRunFilter{PipelineRunID: run.RunID, SortBy: "step_index"})
	if steps[0].Status != 5 || steps[0].ExitCode == nil || *steps[0].ExitCode != 17 || steps[1].Status != 8 {
		t.Fatalf("bad step outcomes: %+v", steps)
	}
}

func TestProcessCancelsQueuedRunBeforeDispatch(t *testing.T) {
	engine, repos := newTestProcess(t)
	engine.sem = make(chan struct{}, 1)
	engine.sem <- struct{}{}
	run := submitTestPipeline(t, engine, repos, &spec.Job{Name: "never", Steps: []*spec.Step{shellStep("never", "true")}})
	if err := engine.CancelRun(run.RunID); err != nil {
		t.Fatal(err)
	}
	result := waitRun(t, repos, run.RunID, model.PipelineStatusCancelled)
	if result.EndTime == nil {
		t.Fatal("cancellation not persisted")
	}
	jobs, _ := repos.JobRun.ListByPipelineRunID(context.Background(), run.RunID)
	if len(jobs) != 0 {
		t.Fatal("queued cancellation dispatched a job")
	}
	<-engine.sem
}

func TestProcessCancelsRunningShellAndPersistsSteps(t *testing.T) {
	engine, repos := newTestProcess(t)
	run := submitTestPipeline(t, engine, repos, &spec.Job{Name: "sleep", Steps: []*spec.Step{shellStep("sleep", "sleep 30"), shellStep("later", "true")}})
	deadline := time.Now().Add(5 * time.Second)
	for {
		steps, _, _ := repos.StepRun.List(context.Background(), repo.StepRunFilter{PipelineRunID: run.RunID})
		if len(steps) > 0 && steps[0].Status == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shell did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := engine.CancelRun(run.RunID); err != nil {
		t.Fatal(err)
	}
	waitRun(t, repos, run.RunID, model.PipelineStatusCancelled)
	jobs, _ := repos.JobRun.ListByPipelineRunID(context.Background(), run.RunID)
	if len(jobs) != 1 || jobs[0].Status != model.JobRunStatusCancelled {
		t.Fatalf("jobs not cancelled: %+v", jobs)
	}
	steps, _, _ := repos.StepRun.List(context.Background(), repo.StepRunFilter{PipelineRunID: run.RunID})
	for _, step := range steps {
		if step.Status != 6 {
			t.Fatalf("step not cancelled: %+v", step)
		}
	}
}

func TestProcessJobTimeoutPersistsFailure(t *testing.T) {
	engine, repos := newTestProcess(t)
	run := submitTestPipeline(t, engine, repos, &spec.Job{Name: "timeout", Timeout: "100ms", Steps: []*spec.Step{shellStep("sleep", "sleep 30")}})
	waitRun(t, repos, run.RunID, model.PipelineStatusFailed)
	jobs, _ := repos.JobRun.ListByPipelineRunID(context.Background(), run.RunID)
	if jobs[0].Status != model.JobRunStatusTimeout {
		t.Fatalf("job status %d", jobs[0].Status)
	}
}

func TestConditionalJobUpdatePreservesConcurrentTerminalReport(t *testing.T) {
	_, repos := newTestProcess(t)
	ctx := context.Background()
	if err := repos.JobRun.Create(ctx, &model.JobRun{JobRunID: "remote", Status: model.JobRunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	store := NewJobRunStore(repos.JobRun, repos.StepRun)
	if err := repos.JobRun.UpdateByJobRunID(ctx, "remote", map[string]any{"status": model.JobRunStatusTimeout, "duration": int64(750)}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateJobRunIfStatus(ctx, "remote", model.JobRunStatusRunning, map[string]any{"status": model.JobRunStatusFailed}); err == nil {
		t.Fatal("stale finalization was accepted")
	}
	job, err := repos.JobRun.GetByJobRunID(ctx, "remote")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != model.JobRunStatusTimeout || job.Duration != 750 {
		t.Fatalf("remote terminal record overwritten: %+v", job)
	}
}
