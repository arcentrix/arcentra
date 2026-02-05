// Copyright 2025 Arcentra Team
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package queue

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

func TestNewStepRunRecordManager(t *testing.T) {
	tests := []struct {
		name       string
		clickHouse *gorm.DB
		wantNil    bool
	}{
		{
			name:       "nil ClickHouse",
			clickHouse: nil,
			wantNil:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, err := NewStepRunRecordManager(tt.clickHouse)
			if tt.wantNil {
				assert.NoError(t, err)
				assert.Nil(t, manager)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, manager)
			}
		})
	}
}

func TestStepRunRecordManager_RecordStepRunEnqueued(t *testing.T) {
	// 测试 nil manager 的情况（不 panic）
	var manager *StepRunRecordManager

	payload := &TaskPayload{
		TaskID:        "test-task-1",
		TaskType:      TaskTypeJob,
		Priority:      5,
		PipelineID:    "pipeline-1",
		PipelineRunID: "run-1",
		StageID:       "stage-1",
		AgentID:       "agent-1",
		RetryCount:    3,
		Data:          map[string]any{"key": "value"},
	}

	// 应该不 panic
	assert.NotPanics(t, func() {
		manager.RecordStepRunEnqueued(payload, Default)
	})
}

func TestStepRunRecordManager_RecordStepRunStarted(t *testing.T) {
	// 测试 nil manager 的情况（不 panic）
	var manager *StepRunRecordManager

	payload := &TaskPayload{
		TaskID: "test-task-1",
	}

	// 应该不 panic
	assert.NotPanics(t, func() {
		manager.RecordStepRunStarted(payload)
	})
}

func TestStepRunRecordManager_RecordStepRunCompleted(t *testing.T) {
	// 测试 nil manager 的情况（不 panic）
	var manager *StepRunRecordManager

	payload := &TaskPayload{
		TaskID: "test-task-1",
	}

	// 应该不 panic
	assert.NotPanics(t, func() {
		manager.RecordStepRunCompleted(payload)
	})
}

func TestStepRunRecordManager_RecordStepRunFailed(t *testing.T) {
	// 测试 nil manager 的情况（不 panic）
	var manager *StepRunRecordManager

	payload := &TaskPayload{
		TaskID: "test-task-1",
	}
	err := errors.New("test error")

	// 应该不 panic
	assert.NotPanics(t, func() {
		manager.RecordStepRunFailed(payload, err)
	})
}

func TestStepRunRecordManager_NilSafety(t *testing.T) {
	var manager *StepRunRecordManager

	payload := &TaskPayload{
		TaskID: "test-task",
	}

	// 这些调用不应该 panic
	assert.NotPanics(t, func() {
		manager.RecordStepRunEnqueued(payload, Default)
	})

	assert.NotPanics(t, func() {
		manager.RecordStepRunStarted(payload)
	})

	assert.NotPanics(t, func() {
		manager.RecordStepRunCompleted(payload)
	})

	assert.NotPanics(t, func() {
		manager.RecordStepRunFailed(payload, errors.New("test"))
	})
}
