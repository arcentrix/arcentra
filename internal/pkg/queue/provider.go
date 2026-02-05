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
	"fmt"

	agentconfig "github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/engine/config"
	"github.com/google/wire"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// ProviderSet 提供 queue 相关的依赖（主程序使用）
var ProviderSet = wire.NewSet(
	ProvideConfig,
	ProvideQueueServer,
)

// AgentProviderSet 提供 queue 相关的依赖（Agent 使用）
var AgentProviderSet = wire.NewSet(
	ProvideAgentConfig,
	ProvideQueueClient,
	ProvidePipelineTaskHandler,
	ProvideJobTaskHandler,
	ProvideStepTaskHandler,
)

// cmdableToUniversalClient 将 redis.Cmdable 转换为 redis.UniversalClient
// *redis.Client 和 *redis.ClusterClient 都实现了 redis.UniversalClient 接口
func cmdableToUniversalClient(cmdable redis.Cmdable) (redis.UniversalClient, error) {
	// 尝试类型断言为 UniversalClient（*redis.Client 和 *redis.ClusterClient 都实现了此接口）
	if universalClient, ok := cmdable.(redis.UniversalClient); ok {
		return universalClient, nil
	}
	return nil, fmt.Errorf("redis.Cmdable (type %T) cannot be converted to redis.UniversalClient", cmdable)
}

// ProvideConfig 提供 queue 配置（主程序使用）
func ProvideConfig(appConf *config.AppConfig, clickHouse *gorm.DB, cmdable redis.Cmdable) (*Config, error) {
	redisClient, err := cmdableToUniversalClient(cmdable)
	if err != nil {
		return nil, fmt.Errorf("failed to convert redis.Cmdable to redis.UniversalClient: %w", err)
	}

	taskQueueConf := appConf.TaskQueue

	// 默认队列配置
	queues := taskQueueConf.Priority
	if len(queues) == 0 {
		queues = map[string]int{
			Critical: 6,
			Default:  3,
			Low:      1,
		}
	}

	// 默认并发数
	concurrency := taskQueueConf.Concurrency
	if concurrency <= 0 {
		concurrency = 10
	}

	return &Config{
		RedisClient:      redisClient, // 复用已有的 Redis 客户端
		ClickHouse:       clickHouse,  // ClickHouse 用于任务记录
		Concurrency:      concurrency,
		StrictPriority:   taskQueueConf.StrictPriority,
		Queues:           queues,
		DefaultQueue:     Default,
		LogLevel:         taskQueueConf.LogLevel,
		ShutdownTimeout:  taskQueueConf.ShutdownTimeout,
		GroupGracePeriod: taskQueueConf.GroupGracePeriod,
		GroupMaxDelay:    taskQueueConf.GroupMaxDelay,
		GroupMaxSize:     taskQueueConf.GroupMaxSize,
	}, nil
}

// ProvideAgentConfig 提供 queue 配置（Agent 使用）
func ProvideAgentConfig(agentConf *agentconfig.AgentConfig, cmdable redis.Cmdable) (*Config, error) {
	redisClient, err := cmdableToUniversalClient(cmdable)
	if err != nil {
		return nil, fmt.Errorf("failed to convert redis.Cmdable to redis.UniversalClient: %w", err)
	}

	taskQueueConf := agentConf.TaskQueue

	// 默认队列配置
	queues := taskQueueConf.Priority
	if len(queues) == 0 {
		queues = map[string]int{
			Critical: 6,
			Default:  3,
			Low:      1,
		}
	}

	// 默认并发数
	concurrency := taskQueueConf.Concurrency
	if concurrency <= 0 {
		concurrency = 10
	}

	return &Config{
		RedisClient:      redisClient, // 复用已有的 Redis 客户端
		ClickHouse:       nil,         // Agent 不需要 ClickHouse 实例
		Concurrency:      concurrency,
		StrictPriority:   taskQueueConf.StrictPriority,
		Queues:           queues,
		DefaultQueue:     Default,
		LogLevel:         taskQueueConf.LogLevel,
		ShutdownTimeout:  taskQueueConf.ShutdownTimeout,
		GroupGracePeriod: taskQueueConf.GroupGracePeriod,
		GroupMaxDelay:    taskQueueConf.GroupMaxDelay,
		GroupMaxSize:     taskQueueConf.GroupMaxSize,
	}, nil
}

// ProvideQueueServer 提供队列服务器（主程序使用）
func ProvideQueueServer(taskQueueConfig *Config) (*Server, error) {
	return NewQueueServer(taskQueueConfig)
}

// ProvideQueueClient 提供队列客户端（Agent 使用）
func ProvideQueueClient(taskQueueConfig *Config) (*Client, error) {
	return NewQueueClient(taskQueueConfig)
}

// ProvidePipelineTaskHandler 提供流水线任务处理器
func ProvidePipelineTaskHandler() *PipelineTaskHandler {
	return NewPipelineTaskHandler()
}

// ProvideJobTaskHandler 提供作业任务处理器
func ProvideJobTaskHandler() *JobTaskHandler {
	return NewJobTaskHandler()
}

// ProvideStepTaskHandler 提供步骤任务处理器
func ProvideStepTaskHandler() *StepTaskHandler {
	return NewStepTaskHandler()
}
