// Copyright 2025 Arcade Team
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
	"time"

	"github.com/arcentrix/arcade/pkg/log"
	"github.com/bytedance/sonic"
	"github.com/hibiken/asynq"
)

// Server 队列服务器（主程序使用）
// 负责任务发布和调度，不执行任务
type Server struct {
	client        *asynq.Client
	config        *Config
	taskRecordMgr *StepRunRecordManager
	redisOpt      asynq.RedisConnOpt // 保存 Redis 连接选项，用于创建 Inspector
}

// NewQueueServer 创建队列服务器（主程序使用）
func NewQueueServer(cfg *Config) (*Server, error) {
	if cfg == nil {
		return nil, fmt.Errorf("queue config is required")
	}

	if cfg.RedisClient == nil {
		return nil, fmt.Errorf("redis client is required")
	}

	if cfg.ClickHouse == nil {
		return nil, fmt.Errorf("clickhouse is required for queue server")
	}

	redisOpt := &redisConnOptWrapper{client: cfg.RedisClient}
	client := asynq.NewClient(redisOpt)

	// 初始化步骤执行记录管理器
	taskRecordMgr, err := NewStepRunRecordManager(cfg.ClickHouse)
	if err != nil {
		return nil, fmt.Errorf("failed to create step run record manager: %w", err)
	}

	server := &Server{
		client:        client,
		config:        cfg,
		taskRecordMgr: taskRecordMgr,
		redisOpt:      redisOpt,
	}

	log.Infow("queue server created",
		"queues", cfg.Queues,
	)

	return server, nil
}

// Enqueue 入队任务
func (s *Server) Enqueue(payload *TaskPayload, queueName string) error {
	data, err := sonic.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal task payload: %w", err)
	}

	// 确定队列名称
	if queueName == "" {
		queueName = s.config.DefaultQueue
		if queueName == "" {
			queueName = Default
		}
	}

	// 创建 asynq 任务
	task := asynq.NewTask(payload.TaskType, data)

	// 设置任务选项
	opts := []asynq.Option{
		asynq.Queue(queueName),
		asynq.MaxRetry(payload.RetryCount),
	}

	if payload.Timeout > 0 {
		opts = append(opts, asynq.Timeout(time.Duration(payload.Timeout)*time.Second))
	}

	// 发送任务
	info, err := s.client.Enqueue(task, opts...)
	if err != nil {
		return fmt.Errorf("enqueue task: %w", err)
	}

	// 记录到 ClickHouse
	if s.taskRecordMgr != nil {
		s.taskRecordMgr.RecordStepRunEnqueued(payload, queueName)
	}

	log.Infow("task enqueued",
		"task_id", payload.TaskID,
		"task_type", payload.TaskType,
		"queue", queueName,
		"task_id", info.ID,
	)

	return nil
}

// EnqueueWithPriority 根据权重入队任务
// priorityWeight: 任务权重，根据配置的队列权重自动选择最合适的队列
// 选择策略：找到权重 >= priorityWeight 的最小队列（向上匹配），如果没有则使用权重最高的队列
func (s *Server) EnqueueWithPriority(payload *TaskPayload, priorityWeight int) error {
	queueName := s.config.DefaultQueue
	if queueName == "" {
		queueName = Default
	}

	// 找到权重 >= priorityWeight 的最小队列（向上匹配）
	bestQueue := queueName
	bestWeight := -1
	for name, weight := range s.config.Queues {
		if weight >= priorityWeight {
			if bestWeight == -1 || weight < bestWeight {
				bestWeight = weight
				bestQueue = name
			}
		}
	}

	// 如果没有找到合适的队列（所有队列权重都 < priorityWeight），使用权重最高的队列
	if bestWeight == -1 && len(s.config.Queues) > 0 {
		maxWeight := 0
		for name, weight := range s.config.Queues {
			if weight > maxWeight {
				maxWeight = weight
				bestQueue = name
			}
		}
	}

	return s.Enqueue(payload, bestQueue)
}

// EnqueueDelayed 延迟入队任务
func (s *Server) EnqueueDelayed(payload *TaskPayload, delay time.Duration, queueName string) error {
	data, err := sonic.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal task payload: %w", err)
	}

	// 确定队列名称
	if queueName == "" {
		queueName = s.config.DefaultQueue
		if queueName == "" {
			queueName = Default
		}
	}

	// 创建 asynq 任务
	task := asynq.NewTask(payload.TaskType, data)

	// 设置任务选项
	opts := []asynq.Option{
		asynq.Queue(queueName),
		asynq.ProcessIn(delay),
		asynq.MaxRetry(payload.RetryCount),
	}

	if payload.Timeout > 0 {
		opts = append(opts, asynq.Timeout(time.Duration(payload.Timeout)*time.Second))
	}

	// 发送任务
	info, err := s.client.Enqueue(task, opts...)
	if err != nil {
		return fmt.Errorf("enqueue delayed task: %w", err)
	}

	// 记录到 ClickHouse
	if s.taskRecordMgr != nil {
		s.taskRecordMgr.RecordStepRunEnqueued(payload, queueName)
	}

	log.Infow("delayed task enqueued",
		"task_id", payload.TaskID,
		"task_type", payload.TaskType,
		"queue", queueName,
		"delay", delay,
		"task_id", info.ID,
	)

	return nil
}

// Shutdown 关闭队列服务器
func (s *Server) Shutdown() {
	log.Info("Shutting down queue server...")
	if err := s.client.Close(); err != nil {
		log.Warnw("failed to close queue client", "error", err)
	}
}

// GetClient 获取 asynq 客户端
func (s *Server) GetClient() *asynq.Client {
	return s.client
}

// GetRedisConnOpt 获取 Redis 连接选项（用于创建 Inspector）
func (s *Server) GetRedisConnOpt() asynq.RedisConnOpt {
	return s.redisOpt
}
