package process

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/logstream"
)

func TestLocalProcessDurablyPublishesLogsWithoutKafka(t *testing.T) {
	engine, repos := newTestProcess(t)
	aggregator := service.NewLogAggregator(repos.Log)
	t.Cleanup(func() { _ = aggregator.Close(context.Background()) })
	engine.appConf = &config.AppConfig{LogSpool: logstream.SpoolConfig{Directory: filepath.Join(t.TempDir(), "logs"), MaxDiskUsageMB: 1}}
	engine.initLocalLogPublisher(aggregator)
	if engine.logInitErr != nil {
		t.Fatal(engine.logInitErr)
	}
	run := submitTestPipeline(t, engine, repos, &spec.Job{Name: "local-logs", Steps: []*spec.Step{shellStep("output", "printf 'durable-local-output\\n'")}})
	waitRun(t, repos, run.RunID, model.PipelineStatusSuccess)
	jobs, err := repos.JobRun.ListByPipelineRunID(context.Background(), run.RunID)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs: %v %v", jobs, err)
	}
	steps, _, err := repos.StepRun.List(context.Background(), repo.StepRunFilter{PipelineRunID: run.RunID})
	if err != nil || len(steps) != 1 {
		t.Fatalf("steps: %v %v", steps, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := repos.Log.ListByStepRunID(context.Background(), steps[0].StepRunID, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.Contains(entry.Content, "durable-local-output") {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("durable local shell output did not reach SQLite")
}

type rejectingProcessLogPublisher struct{}

func (rejectingProcessLogPublisher) Publish(context.Context, *logstream.BuildLogMessage) error {
	return executor.ErrBuildLogSpoolFull
}

func TestLocalProcessFailsWhenDurableLogsRejected(t *testing.T) {
	engine, repos := newTestProcess(t)
	engine.logPublisher = rejectingProcessLogPublisher{}
	run := submitTestPipeline(t, engine, repos, &spec.Job{Name: "failed-logs", Steps: []*spec.Step{shellStep("output", "printf 'cannot retain logs\\n'; sleep 30")}})
	waitRun(t, repos, run.RunID, model.PipelineStatusFailed)
	steps, _, err := repos.StepRun.List(context.Background(), repo.StepRunFilter{PipelineRunID: run.RunID})
	if err != nil || len(steps) != 1 {
		t.Fatalf("steps: %v %v", steps, err)
	}
	if steps[0].Status != 5 || !strings.Contains(steps[0].ErrorMessage, "spool is full") {
		t.Fatalf("log rejection must be explicit failed step: %+v", steps[0])
	}
}
