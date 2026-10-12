package taskqueue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
)

func TestResultToStepRunStatusPreservesLogPersistenceFailure(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelDeadline := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelDeadline()
	persistenceErr := fmt.Errorf("%w: disk full", executor.ErrBuildLogPersistence)
	for _, test := range []struct {
		name string
		ctx  context.Context
		err  error
		want int
	}{
		{"internal-cancel", context.Background(), errors.Join(context.Canceled, persistenceErr), 5},
		{"publisher-timeout", context.Background(), errors.Join(context.DeadlineExceeded, persistenceErr), 5},
		{"caller-cancel", canceled, errors.Join(context.Canceled, persistenceErr), 6},
		{"caller-timeout", expired, errors.Join(context.Canceled, persistenceErr), 7},
		{"ordinary-cancel", context.Background(), context.Canceled, 6},
		{"ordinary-timeout", context.Background(), context.DeadlineExceeded, 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, exitCode, reason := resultToStepRunStatus(test.ctx, &executor.ExecutionResult{ExitCode: 9, Error: "original execution failure"}, test.err)
			if int(status) != test.want {
				t.Fatalf("status=%v want=%d reason=%s", status, test.want, reason)
			}
			if test.want == 5 && (exitCode != 9 || !strings.Contains(reason, "disk full")) {
				t.Fatalf("persistence failure reason or exit code lost: code=%d reason=%s", exitCode, reason)
			}
		})
	}
}

func TestJobDurableLogRejectionReportsFailedInsteadOfCancelled(t *testing.T) {
	for _, test := range []struct {
		name       string
		continued  bool
		wantJob    int32
		wantSecond int
	}{
		{"stop-on-failure", false, 5, 8},
		{"explicit-continue-on-error", true, 4, 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			publisher, err := executor.NewDurableLogPublisher(context.Background(), &capturedLogs{}, t.TempDir(), 1)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := publisher.Close(ctx); err != nil {
					t.Error(err)
				}
			})
			manager := executor.NewExecutorManager()
			manager.Register(executor.NewShellExecutor())
			manager.SetLogPublisher(publisher)
			client := &artifactStatusClient{}
			wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
			conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "log-failure-agent", WorkspaceDir: t.TempDir()}}
			payload := &taskpayload.JobRunTaskPayload{
				PipelineRunID: "log-failure-run", JobRunID: "log-failure-job", JobName: "build",
				Steps: []taskpayload.StepPayload{
					{Name: "first", StepRunID: "log-failure-first", ContinueOnError: test.continued, Args: map[string]any{"run": "printf 'private-log-payload'; sleep 30 & wait"}},
					{Name: "second", StepRunID: "log-failure-second", Args: map[string]any{"run": "true"}},
				},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			err = executeJobRun(ctx, conf, wrapper, manager, payload)
			if test.continued {
				if err != nil {
					t.Fatalf("explicit continuation was lost: %v", err)
				}
			} else if !errors.Is(err, executor.ErrBuildLogPersistence) || !errors.Is(err, executor.ErrBuildLogSpoolFull) {
				t.Fatalf("durability error was not preserved: %v", err)
			}
			if len(client.jobReports) == 0 {
				t.Fatal("missing job status")
			}
			job := client.jobReports[len(client.jobReports)-1]
			if job.Status != test.wantJob || (!test.continued && !strings.Contains(job.ErrorMessage, "build log spool is full")) || strings.Contains(job.ErrorMessage, "private-log-payload") {
				t.Fatalf("wrong job durability failure: %+v", job)
			}
			stepStatuses := make(map[string]int)
			for _, report := range client.stepReports {
				if int(report.Status) < 4 {
					continue
				}
				stepStatuses[report.StepRunId] = int(report.Status)
				if report.StepRunId == "log-failure-first" && !strings.Contains(report.ErrorMessage, "build log spool is full") {
					t.Fatalf("step failure lost disk reason: %+v", report)
				}
			}
			if stepStatuses["log-failure-first"] != 5 || stepStatuses["log-failure-second"] != test.wantSecond {
				t.Fatalf("wrong rejected/skipped step states: %+v", stepStatuses)
			}
		})
	}
}
