package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/logstream"
	"go.uber.org/zap"
)

type logFailureRecordStore struct {
	jobRunStoreIface
	jobUpdates  map[string]any
	stepUpdates map[string]any
}

func (s *logFailureRecordStore) UpdateJobRun(_ context.Context, _ string, updates map[string]any) error {
	s.jobUpdates = updates
	return nil
}

func TestLocalLogPersistenceFailureStatus(t *testing.T) {
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
			if got := stepFailureStatus(test.ctx, test.err); got != test.want {
				t.Fatalf("step status=%d want=%d", got, test.want)
			}
			logger := log.Logger{SugaredLogger: zap.NewNop().Sugar()}
			execution := NewExecutionContext(&spec.Pipeline{Namespace: "test"}, nil, t.TempDir(), logger)
			store := &logFailureRecordStore{}
			execution.JobRunStore = store
			execution.JobRunID = "local-log-failure"
			task := &Task{Name: "local", State: TaskStateFailed, Job: &spec.Job{Name: "local"}}
			NewTaskFramework(execution, logger).finishRecords(test.ctx, task, test.err)
			if got := store.jobUpdates["status"]; got != test.want {
				t.Fatalf("job status=%v want=%d updates=%+v", got, test.want, store.jobUpdates)
			}
			if test.want == 5 && !strings.Contains(store.jobUpdates["error_message"].(string), "disk full") {
				t.Fatalf("lost durability failure reason: %+v", store.jobUpdates)
			}
		})
	}
}

func (s *logFailureRecordStore) UpdateStepRun(_ context.Context, _ string, updates map[string]any) error {
	s.stepUpdates = updates
	return nil
}

type rejectedLocalLogs struct{}

func (rejectedLocalLogs) Publish(context.Context, *logstream.BuildLogMessage) error {
	return executor.ErrBuildLogSpoolFull
}

func TestLocalStepLogFailurePreservesTimeoutAndContinueOnError(t *testing.T) {
	for _, test := range []struct {
		name      string
		timeout   string
		continued bool
		wantStep  int
		wantJob   int
	}{
		{"execution-timeout", "50ms", false, 7, 7},
		{"log-persistence-failure", "", false, 5, 5},
		{"explicit-continue-on-error", "", true, 5, 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			logger := log.Logger{SugaredLogger: zap.NewNop().Sugar()}
			execution := NewExecutionContext(&spec.Pipeline{Namespace: "test"}, nil, t.TempDir(), logger)
			execution.Env["secrets.token"] = "secret-token-value"
			execution.LogPublisher = rejectedLocalLogs{}
			execution.JobRunID = "log-failure-job"
			execution.StepRunIDs = map[string]string{"shell": "log-failure-step"}
			store := &logFailureRecordStore{}
			execution.JobRunStore = store
			command := "printf 'secret-token-value'"
			if test.timeout != "" {
				command += "; sleep 30 & wait"
			}
			step := &spec.Step{Name: "shell", Uses: "shell", Timeout: test.timeout, ContinueOnError: test.continued, Args: spec.MapAsStruct(map[string]any{"script": command})}
			task := &Task{Name: "build", State: TaskStateStarted, Job: &spec.Job{Name: "build", Steps: []*spec.Step{step}}}
			task.Set("stepStatuses", map[string]int{})
			framework := NewTaskFramework(execution, logger)
			err := framework.executeStepOnce(context.Background(), task, step)
			if test.continued {
				if err != nil {
					t.Fatalf("explicit continuation was lost: %v", err)
				}
			} else if !errors.Is(err, executor.ErrBuildLogPersistence) {
				t.Fatalf("persistence failure was lost: %v", err)
			}
			if got := store.stepUpdates["status"]; got != test.wantStep {
				t.Fatalf("step status=%v want=%d err=%v", got, test.wantStep, err)
			}
			if message, _ := store.stepUpdates["error_message"].(string); !strings.Contains(message, "build log spool is full") || strings.Contains(message, "secret-token-value") {
				t.Fatalf("unsafe or missing step error: %s", message)
			}
			framework.finishRecords(context.Background(), task, err)
			if got := store.jobUpdates["status"]; got != test.wantJob {
				t.Fatalf("job status=%v want=%d updates=%+v", got, test.wantJob, store.jobUpdates)
			}
		})
	}
}
