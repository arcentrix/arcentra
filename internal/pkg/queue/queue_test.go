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
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockClickHouse 不再需要，直接使用 *gorm.DB

// 创建测试用的配置（用于 Server）
func createTestConfigForServer() *Config {
	redisClient := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   1, // 使用不同的 DB 避免冲突
	})

	return &Config{
		RedisClient:      redisClient,
		ClickHouse:       nil, // Server 需要 ClickHouse，测试中使用 nil
		Concurrency:      2,
		StrictPriority:   false,
		Queues:           map[string]int{Critical: 6, Default: 3, Low: 1},
		DefaultQueue:     Default,
		LogLevel:         "info",
		ShutdownTimeout:  10,
		GroupGracePeriod: 5,
		GroupMaxDelay:    20,
		GroupMaxSize:     100,
	}
}

// 创建测试用的配置（用于 Client）
func createTestConfig() *Config {
	redisClient := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   1, // 使用不同的 DB 避免冲突
	})

	return &Config{
		RedisClient:      redisClient,
		ClickHouse:       nil, // Client 不需要 ClickHouse
		Concurrency:      2,
		StrictPriority:   false,
		Queues:           map[string]int{Critical: 6, Default: 3, Low: 1},
		DefaultQueue:     Default,
		LogLevel:         "info",
		ShutdownTimeout:  10,
		GroupGracePeriod: 5,
		GroupMaxDelay:    20,
		GroupMaxSize:     100,
	}
}

func TestNewQueueServer(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *Config
		wantErr bool
		errMsg  string
	}{
		{
			name:    "nil config",
			cfg:     nil,
			wantErr: true,
			errMsg:  "queue config is required",
		},
		{
			name:    "nil redis client",
			cfg:     &Config{RedisClient: nil},
			wantErr: true,
			errMsg:  "redis client is required",
		},
		{
			name:    "nil clickhouse",
			cfg:     &Config{RedisClient: redis.NewClient(&redis.Options{Addr: "localhost:6379"})},
			wantErr: true,
			errMsg:  "clickhouse is required for queue server",
		},
		{
			name:    "valid config",
			cfg:     createTestConfig(),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, err := NewQueueServer(tt.cfg)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
				assert.Nil(t, server)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, server)
				assert.NotNil(t, server.client)
				assert.NotNil(t, server.config)
			}
		})
	}
}

func TestNewQueueClient(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *Config
		wantErr bool
		errMsg  string
	}{
		{
			name:    "nil config",
			cfg:     nil,
			wantErr: true,
			errMsg:  "queue config is required",
		},
		{
			name:    "nil redis client",
			cfg:     &Config{RedisClient: nil},
			wantErr: true,
			errMsg:  "redis client is required",
		},
		{
			name:    "valid config",
			cfg:     createTestConfig(),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewQueueClient(tt.cfg)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
				assert.Nil(t, client)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, client)
				assert.NotNil(t, client.server)
				assert.NotNil(t, client.mux)
				assert.NotNil(t, client.config)
				assert.NotNil(t, client.handlers)
			}
		})
	}
}

func TestQueueServer_Enqueue(t *testing.T) {
	cfg := createTestConfigForServer()
	server, err := NewQueueServer(cfg)
	require.NoError(t, err)
	defer server.Shutdown()

	tests := []struct {
		name      string
		payload   *TaskPayload
		queueName string
		wantErr   bool
	}{
		{
			name: "valid payload with default queue",
			payload: &TaskPayload{
				TaskID:     "test-task-1",
				TaskType:   TaskTypeJob,
				Priority:   5,
				RetryCount: 3,
				Timeout:    3600,
			},
			queueName: "",
			wantErr:   false,
		},
		{
			name: "valid payload with specific queue",
			payload: &TaskPayload{
				TaskID:     "test-task-2",
				TaskType:   TaskTypePipeline,
				Priority:   6,
				RetryCount: 3,
				Timeout:    3600,
			},
			queueName: Critical,
			wantErr:   false,
		},
		{
			name: "payload with timeout",
			payload: &TaskPayload{
				TaskID:     "test-task-3",
				TaskType:   TaskTypeStep,
				Priority:   3,
				RetryCount: 2,
				Timeout:    1800,
			},
			queueName: Default,
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := server.Enqueue(tt.payload, tt.queueName)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestQueueServer_EnqueueWithPriority(t *testing.T) {
	cfg := createTestConfigForServer()
	server, err := NewQueueServer(cfg)
	require.NoError(t, err)
	defer server.Shutdown()

	tests := []struct {
		name           string
		payload        *TaskPayload
		priorityWeight int
		expectedQueue  string
		wantErr        bool
	}{
		{
			name: "high priority should use critical queue",
			payload: &TaskPayload{
				TaskID:     "test-high-priority",
				TaskType:   TaskTypeJob,
				RetryCount: 3,
				Timeout:    3600,
			},
			priorityWeight: 6,
			expectedQueue:  Critical,
			wantErr:        false,
		},
		{
			name: "medium priority should use default queue",
			payload: &TaskPayload{
				TaskID:     "test-medium-priority",
				TaskType:   TaskTypeJob,
				RetryCount: 3,
				Timeout:    3600,
			},
			priorityWeight: 3,
			expectedQueue:  Default,
			wantErr:        false,
		},
		{
			name: "low priority should use low queue",
			payload: &TaskPayload{
				TaskID:     "test-low-priority",
				TaskType:   TaskTypeJob,
				RetryCount: 3,
				Timeout:    3600,
			},
			priorityWeight: 1,
			expectedQueue:  Low,
			wantErr:        false,
		},
		{
			name: "priority higher than max should use highest queue",
			payload: &TaskPayload{
				TaskID:     "test-very-high-priority",
				TaskType:   TaskTypeJob,
				RetryCount: 3,
				Timeout:    3600,
			},
			priorityWeight: 10,
			expectedQueue:  Critical,
			wantErr:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := server.EnqueueWithPriority(tt.payload, tt.priorityWeight)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestQueueServer_EnqueueDelayed(t *testing.T) {
	cfg := createTestConfigForServer()
	server, err := NewQueueServer(cfg)
	require.NoError(t, err)
	defer server.Shutdown()

	payload := &TaskPayload{
		TaskID:     "test-delayed-task",
		TaskType:   TaskTypeJob,
		Priority:   5,
		RetryCount: 3,
		Timeout:    3600,
	}

	delay := 5 * time.Second
	err = server.EnqueueDelayed(payload, delay, "")
	assert.NoError(t, err)
}

func TestQueueClient_RegisterHandler(t *testing.T) {
	cfg := createTestConfig()
	client, err := NewQueueClient(cfg)
	require.NoError(t, err)
	defer client.Shutdown()

	handler := TaskHandlerFunc(func(ctx context.Context, payload *TaskPayload) error {
		assert.Equal(t, "test-task", payload.TaskID)
		return nil
	})

	client.RegisterHandler("test-type", handler)
	assert.NotNil(t, client.handlers["test-type"])
}

func TestQueueClient_RegisterHandlerFunc(t *testing.T) {
	cfg := createTestConfig()
	client, err := NewQueueClient(cfg)
	require.NoError(t, err)
	defer client.Shutdown()

	handlerFunc := func(ctx context.Context, payload *TaskPayload) error {
		return nil
	}

	client.RegisterHandlerFunc("test-func-type", handlerFunc)
	assert.NotNil(t, client.handlers["test-func-type"])
}

func TestQueueServer_GetClient(t *testing.T) {
	cfg := createTestConfigForServer()
	server, err := NewQueueServer(cfg)
	require.NoError(t, err)
	defer server.Shutdown()

	client := server.GetClient()
	assert.NotNil(t, client)
	assert.IsType(t, &asynq.Client{}, client)
}

func TestQueueServer_GetRedisConnOpt(t *testing.T) {
	cfg := createTestConfigForServer()
	server, err := NewQueueServer(cfg)
	require.NoError(t, err)
	defer server.Shutdown()

	redisOpt := server.GetRedisConnOpt()
	assert.NotNil(t, redisOpt)
}

func TestQueueClient_GetServer(t *testing.T) {
	cfg := createTestConfig()
	client, err := NewQueueClient(cfg)
	require.NoError(t, err)
	defer client.Shutdown()

	server := client.GetServer()
	assert.NotNil(t, server)
	assert.IsType(t, &asynq.Server{}, server)
}

func TestQueueClient_GetRedisConnOpt(t *testing.T) {
	cfg := createTestConfig()
	client, err := NewQueueClient(cfg)
	require.NoError(t, err)
	defer client.Shutdown()

	redisOpt := client.GetRedisConnOpt()
	assert.NotNil(t, redisOpt)
}

func TestNewTaskManager(t *testing.T) {
	cfg := createTestConfigForServer()
	server, err := NewQueueServer(cfg)
	require.NoError(t, err)
	defer server.Shutdown()

	manager := NewTaskManager(server)
	assert.NotNil(t, manager)
	assert.Equal(t, server, manager.server)
}

func TestTaskManager_EnqueueTask(t *testing.T) {
	cfg := createTestConfigForServer()
	server, err := NewQueueServer(cfg)
	require.NoError(t, err)
	defer server.Shutdown()

	manager := NewTaskManager(server)

	tests := []struct {
		name           string
		payload        *TaskPayload
		priorityWeight int
		wantErr        bool
	}{
		{
			name: "valid payload",
			payload: &TaskPayload{
				TaskID:     "test-manager-task",
				TaskType:   TaskTypeJob,
				RetryCount: 3,
				Timeout:    3600,
			},
			priorityWeight: 5,
			wantErr:        false,
		},
		{
			name:           "nil payload",
			payload:        nil,
			priorityWeight: 5,
			wantErr:        true,
		},
		{
			name: "payload with default timeout and retry",
			payload: &TaskPayload{
				TaskID:   "test-defaults",
				TaskType: TaskTypeJob,
			},
			priorityWeight: 3,
			wantErr:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			err := manager.EnqueueTask(ctx, tt.payload, tt.priorityWeight)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.payload != nil {
					assert.Equal(t, tt.priorityWeight, tt.payload.Priority)
					if tt.payload.Timeout == 0 {
						assert.Equal(t, 3600, tt.payload.Timeout)
					}
					if tt.payload.RetryCount == 0 {
						assert.Equal(t, 3, tt.payload.RetryCount)
					}
				}
			}
		})
	}
}

func TestTaskManager_EnqueueDelayedTask(t *testing.T) {
	cfg := createTestConfigForServer()
	server, err := NewQueueServer(cfg)
	require.NoError(t, err)
	defer server.Shutdown()

	manager := NewTaskManager(server)

	payload := &TaskPayload{
		TaskID:     "test-delayed-manager-task",
		TaskType:   TaskTypeJob,
		RetryCount: 3,
		Timeout:    3600,
	}

	delay := 10 * time.Second
	ctx := context.Background()
	err = manager.EnqueueDelayedTask(ctx, payload, delay)
	assert.NoError(t, err)
}

func TestTaskPayload_MarshalUnmarshal(t *testing.T) {
	original := &TaskPayload{
		TaskID:        "test-task",
		TaskType:      TaskTypePipeline,
		Priority:      5,
		PipelineID:    "pipeline-1",
		PipelineRunID: "run-1",
		StageID:       "stage-1",
		Stage:         1,
		AgentID:       "agent-1",
		Name:          "test-job",
		Commands:      []string{"echo", "hello"},
		Env:           map[string]string{"KEY": "value"},
		Workspace:     "/tmp",
		Timeout:       3600,
		RetryCount:    3,
		LabelSelector: map[string]any{"label": "value"},
		Data:          map[string]any{"data": "value"},
	}

	// Marshal
	data, err := json.Marshal(original)
	require.NoError(t, err)

	// Unmarshal
	var unmarshaled TaskPayload
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	// Compare
	assert.Equal(t, original.TaskID, unmarshaled.TaskID)
	assert.Equal(t, original.TaskType, unmarshaled.TaskType)
	assert.Equal(t, original.Priority, unmarshaled.Priority)
	assert.Equal(t, original.PipelineID, unmarshaled.PipelineID)
	assert.Equal(t, original.Commands, unmarshaled.Commands)
	assert.Equal(t, original.Env, unmarshaled.Env)
}

func TestTaskHandlerFunc(t *testing.T) {
	called := false
	handler := TaskHandlerFunc(func(ctx context.Context, payload *TaskPayload) error {
		called = true
		return nil
	})

	ctx := context.Background()
	payload := &TaskPayload{TaskID: "test"}
	err := handler.HandleTask(ctx, payload)

	assert.NoError(t, err)
	assert.True(t, called)
}

func TestConstants(t *testing.T) {
	assert.Equal(t, "pipeline", TaskTypePipeline)
	assert.Equal(t, "job", TaskTypeJob)
	assert.Equal(t, "step", TaskTypeStep)
	assert.Equal(t, "custom", TaskTypeCustom)

	assert.Equal(t, "critical", Critical)
	assert.Equal(t, "default", Default)
	assert.Equal(t, "low", Low)

	assert.Equal(t, "pending", TaskRecordStatusPending)
	assert.Equal(t, "running", TaskRecordStatusRunning)
	assert.Equal(t, "completed", TaskRecordStatusCompleted)
	assert.Equal(t, "failed", TaskRecordStatusFailed)
}
