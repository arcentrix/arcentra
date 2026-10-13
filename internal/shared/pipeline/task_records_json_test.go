// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/bytedance/sonic"
	"go.uber.org/zap"
)

type jsonCreationStore struct {
	jobRunStoreIface
	job   *model.JobRun
	steps []*model.StepRun
}

func (s *jsonCreationStore) CreateJobRun(_ context.Context, value *model.JobRun) error {
	s.job = value
	return nil
}

func (s *jsonCreationStore) CreateStepRun(_ context.Context, value *model.StepRun) error {
	s.steps = append(s.steps, value)
	return nil
}

// TestCreatedRecordsHaveValidMySQLJSON 防止 SQLite 宽松类型掩盖 MySQL JSON 空字符串错误
func TestCreatedRecordsHaveValidMySQLJSON(t *testing.T) {
	for _, mode := range []string{"none", "failed_step", "whole_job"} {
		t.Run(mode, func(t *testing.T) {
			logger := log.Logger{SugaredLogger: zap.NewNop().Sugar()}
			ctx := NewExecutionContext(&spec.Pipeline{Namespace: "json"}, nil, t.TempDir(), logger)
			ctx.PipelineIDRef, ctx.PipelineRunID = "pipeline", "run"
			store := &jsonCreationStore{}
			ctx.JobRunStore = store
			job := &spec.Job{Name: "build", Steps: []*spec.Step{{Name: "step", Uses: "shell", RunOnAgent: true}}}
			wantSteps := 1
			if mode != "none" {
				job.Retry = &spec.Retry{Mode: mode, MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{7}}
				wantSteps = 2
			}
			if err := NewTaskFramework(ctx, logger).createRecords(context.Background(), &Task{Name: job.Name, Job: job}); err != nil {
				t.Fatal(err)
			}
			if store.job == nil || len(store.steps) != wantSteps {
				t.Fatalf("missing created records: job=%v steps=%d", store.job != nil, len(store.steps))
			}
			if store.job.ArtifactReferences != "{}" {
				t.Fatalf("new job artifact references must be an empty JSON object, got %q", store.job.ArtifactReferences)
			}
			assertCreatedJSONFields(t, store.job)
			for _, step := range store.steps {
				assertCreatedJSONFields(t, step)
			}
		})
	}
}

func assertCreatedJSONFields(t *testing.T, record any) {
	t.Helper()
	value := reflect.ValueOf(record).Elem()
	for i := 0; i < value.NumField(); i++ {
		field := value.Type().Field(i)
		if !strings.Contains(field.Tag.Get("gorm"), "type:json") {
			continue
		}
		if value.Field(i).Kind() != reflect.String || !sonic.Valid([]byte(value.Field(i).String())) {
			t.Errorf("%T.%s is not valid JSON at record creation: %q", record, field.Name, value.Field(i))
		}
	}
}
