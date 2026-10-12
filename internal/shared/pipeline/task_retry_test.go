package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/log"
	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type retryRecordStore struct {
	jobRunStoreIface
	db     *gorm.DB
	fault  string
	failed bool
}

func (s *retryRecordStore) CreateJobRun(ctx context.Context, j *model.JobRun) error {
	return s.db.WithContext(ctx).Create(j).Error
}
func (s *retryRecordStore) CreateStepRun(ctx context.Context, j *model.StepRun) error {
	return s.db.WithContext(ctx).Create(j).Error
}
func (s *retryRecordStore) UpdateJobRun(ctx context.Context, id string, v map[string]any) error {
	return s.db.WithContext(ctx).Model(&model.JobRun{}).Where("job_run_id = ?", id).Updates(v).Error
}
func (s *retryRecordStore) UpdateStepRun(ctx context.Context, id string, v map[string]any) error {
	if !s.failed {
		_, exit := v["exit_code"]
		state, _ := v["status"].(int)
		if (s.fault == "start" && state == 3) || (s.fault == "exit" && exit && state == 0) || (s.fault == "terminal" && state >= 4) {
			s.failed = true
			return fmt.Errorf("injected %s persistence failure", s.fault)
		}
	}

	var current model.StepRun
	if err := s.db.WithContext(ctx).Where("step_run_id = ?", id).First(&current).Error; err != nil {
		return err
	}
	if current.Status >= 4 {
		return fmt.Errorf("terminal attempt overwritten: %s", id)
	}
	return s.db.WithContext(ctx).Model(&model.StepRun{}).Where("step_run_id = ?", id).Updates(v).Error
}

func TestLocalRetryPreservesAttemptHistoryAndSideEffects(t *testing.T) {
	for _, mode := range []string{"failed_step", "whole_job"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "retry.db")), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			if err = db.WithContext(ctx).AutoMigrate(&model.StepRun{}, &model.JobRun{}); err != nil {
				t.Fatal(err)
			}
			sqlDB, _ := db.DB()
			defer sqlDB.Close()
			logger := log.Logger{SugaredLogger: zap.NewNop().Sugar()}
			execution := NewExecutionContext(&spec.Pipeline{Namespace: "pipeline"}, nil, t.TempDir(), logger)
			execution.JobRunStore = &retryRecordStore{db: db}
			execution.PipelineRunID = "run"
			execution.PipelineIDRef = "pipeline"
			execution.TriggeredBy = "authorized-operator"
			job := &spec.Job{Name: "build", Retry: &spec.Retry{Mode: mode, MaxAttempts: 3, Idempotent: true, AllowedExitCodes: []int32{9}}, Steps: []*spec.Step{
				{Name: "first", Uses: "shell", Args: spec.MapAsStruct(map[string]any{"run": "printf x >> first"})},
				{Name: "flaky", Uses: "shell", Args: spec.MapAsStruct(map[string]any{"run": "printf x >> flaky; if [ ! -f failed ]; then touch failed; exit 9; fi"})},
				{Name: "last", Uses: "shell", Args: spec.MapAsStruct(map[string]any{"run": "printf x >> last"})}}}
			task := &Task{Name: job.Name, Job: job}
			if err = NewTaskFramework(execution, logger).Execute(ctx, task); err != nil {
				t.Fatal(err)
			}
			wantFirst := "x"
			if mode == "whole_job" {
				wantFirst = "xx"
			}
			for name, want := range map[string]string{"first": wantFirst, "flaky": "xx", "last": "x"} {
				got, err := os.ReadFile(filepath.Join(execution.WorkspaceRoot, "pipeline", "build", name))
				if err != nil || string(got) != want {
					t.Fatalf("%s=%q %v want %q", name, got, err, want)
				}
			}
			var attempts []model.StepRun
			if err = db.WithContext(ctx).Select("step_run_id", "name", "attempt", "status", "previous_step_run_id", "retry_mode", "exit_code", "created_by").Order("name, attempt").Find(&attempts).Error; err != nil {
				t.Fatal(err)
			}
			if len(attempts) != 9 {
				t.Fatalf("attempts=%d", len(attempts))
			}
			seen := map[string]bool{}
			for _, a := range attempts {
				if seen[a.StepRunID] || a.Status < 4 || a.RetryMode != mode || a.CreatedBy != "authorized-operator" {
					t.Fatalf("invalid attempt %+v", a)
				}
				seen[a.StepRunID] = true
				if a.Attempt > 1 && a.PreviousStepRunID == "" {
					t.Fatal("missing lineage")
				}
				if a.Name == "flaky" && a.Attempt == 1 && (a.Status != 5 || a.ExitCode == nil || *a.ExitCode != 9) {
					t.Fatalf("failure overwritten: %+v", a)
				}
			}
			var stored model.JobRun
			if err = db.WithContext(ctx).Select("status", "failed_steps", "completed_steps").First(&stored).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Status != 4 || stored.FailedSteps != 0 || stored.CompletedSteps != 3 {
				t.Fatalf("wrong logical counts %+v", stored)
			}
		})
	}
}

func (s *retryRecordStore) GetJobRun(ctx context.Context, id string) (*model.JobRun, error) {
	var job model.JobRun
	err := s.db.WithContext(ctx).Select("job_run_id", "status", "artifact_uris", "artifact_references").Where("job_run_id = ?", id).First(&job).Error
	return &job, err
}

func TestLocalRetryPersistenceFailuresFenceAllFurtherCommands(t *testing.T) {
	for _, fault := range []string{"start", "exit", "terminal"} {
		t.Run(fault, func(t *testing.T) {
			ctx := context.Background()
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "fault.db")), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, _ := db.DB()
			defer sqlDB.Close()
			if err = db.WithContext(ctx).AutoMigrate(&model.StepRun{}, &model.JobRun{}); err != nil {
				t.Fatal(err)
			}
			logger := log.Logger{SugaredLogger: zap.NewNop().Sugar()}
			execution := NewExecutionContext(&spec.Pipeline{Namespace: "fault"}, nil, t.TempDir(), logger)
			execution.JobRunStore = &retryRecordStore{db: db, fault: fault}
			job := &spec.Job{Name: "job", Retry: &spec.Retry{Mode: "failed_step", MaxAttempts: 3, Idempotent: true, AllowedExitCodes: []int32{9}}, Steps: []*spec.Step{
				{Name: "first", Uses: "shell", ContinueOnError: true, Args: spec.MapAsStruct(map[string]any{"run": "printf x >> first; exit 9"})},
				{Name: "later", Uses: "shell", Args: spec.MapAsStruct(map[string]any{"run": "touch forbidden"})}}}
			if err = NewTaskFramework(execution, logger).Execute(ctx, &Task{Name: "job", Job: job}); err == nil {
				t.Fatal("persistence failure reported success")
			}
			root := filepath.Join(execution.WorkspaceRoot, "fault", "job")
			if _, err = os.Stat(filepath.Join(root, "forbidden")); !os.IsNotExist(err) {
				t.Fatalf("later command executed: %v", err)
			}
			got, _ := os.ReadFile(filepath.Join(root, "first"))
			if len(got) > 1 {
				t.Fatalf("command retried after store failure: %q", got)
			}
			var stored model.JobRun
			if err = db.WithContext(ctx).Select("status").First(&stored).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Status == 4 {
				t.Fatal("job falsely succeeded")
			}
		})
	}
}
