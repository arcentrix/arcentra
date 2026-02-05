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
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/bytedance/sonic"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// TaskQueue 基于 asynq 的分布式任务队列
type TaskQueue struct {
	client        *asynq.Client
	server        *asynq.Server
	mux           *asynq.ServeMux
	config        *Config
	handlers      map[string]TaskHandler
	taskRecordMgr *StepRunRecordManager
	redisOpt      asynq.RedisConnOpt // 保存 Redis 连接选项，用于创建 Inspector
}

// Config queue 配置
type Config struct {
	RedisClient      redis.UniversalClient // Redis 客户端（复用已有的客户端）
	ClickHouse       *gorm.DB              // ClickHouse GORM 实例（用于任务记录）
	Concurrency      int                   // 并发处理数
	StrictPriority   bool                  // 是否严格优先级
	Queues           map[string]int        // 队列配置：队列名 -> 优先级权重
	DefaultQueue     string                // 默认队列名称
	LogLevel         string                // 日志级别: debug, info, warn, error
	ShutdownTimeout  int                   // 关闭超时时间（秒）
	GroupGracePeriod int                   // 组优雅关闭周期（秒）
	GroupMaxDelay    int                   // 组最大延迟（秒）
	GroupMaxSize     int                   // 组最大大小
}

// TaskPayload 任务负载
type TaskPayload struct {
	TaskID        string            `json:"task_id"`
	TaskType      string            `json:"task_type"`
	Priority      int               `json:"priority"`
	PipelineID    string            `json:"pipeline_id"`
	PipelineRunID string            `json:"pipeline_run_id"`
	StageID       string            `json:"stage_id"`
	Stage         int               `json:"stage"`
	AgentID       string            `json:"agent_id"`
	Name          string            `json:"name"`
	Commands      []string          `json:"commands"`
	Env           map[string]string `json:"env"`
	Workspace     string            `json:"workspace"`
	Timeout       int               `json:"timeout"`
	RetryCount    int               `json:"retry_count"`
	LabelSelector map[string]any    `json:"label_selector"`
	Data          map[string]any    `json:"data"` // 扩展数据
}

// TaskHandler 任务处理器接口
type TaskHandler interface {
	HandleTask(ctx context.Context, payload *TaskPayload) error
}

// TaskHandlerFunc 任务处理器函数类型
type TaskHandlerFunc func(ctx context.Context, payload *TaskPayload) error

func (f TaskHandlerFunc) HandleTask(ctx context.Context, payload *TaskPayload) error {
	return f(ctx, payload)
}

// 任务类型常量
const (
	TaskTypePipeline = "pipeline" // 流水线任务
	TaskTypeJob      = "job"      // 作业任务
	TaskTypeStep     = "step"     // 步骤任务
	TaskTypeCustom   = "custom"   // 自定义任务
)

// 队列名称常量
const (
	Critical = "critical" // 关键队列（优先级最高）
	Default  = "default"  // 默认队列
	Low      = "low"      // 低优先级队列
)

// 任务记录状态常量
const (
	TaskRecordStatusPending   = "pending"   // 等待中
	TaskRecordStatusRunning   = "running"   // 执行中
	TaskRecordStatusCompleted = "completed" // 已完成
	TaskRecordStatusFailed    = "failed"    // 失败
)

// NewTaskQueue 创建任务队列
func NewTaskQueue(cfg *Config) (*TaskQueue, error) {
	if cfg == nil {
		return nil, fmt.Errorf("queue config is required")
	}

	if cfg.RedisClient == nil {
		return nil, fmt.Errorf("redis client is required")
	}
	redisOpt := &redisConnOptWrapper{client: cfg.RedisClient}

	client := asynq.NewClient(redisOpt)

	// 默认队列配置
	queues := cfg.Queues
	if len(queues) == 0 {
		queues = map[string]int{
			Critical: 6,
			Default:  3,
			Low:      1,
		}
	}

	var logLevel asynq.LogLevel
	if cfg.LogLevel != "" {
		if err := logLevel.Set(cfg.LogLevel); err != nil {
			log.Warnw("invalid log level, using default info", "logLevel", cfg.LogLevel, "error", err)
			logLevel = asynq.InfoLevel
		}
	} else {
		logLevel = asynq.InfoLevel
	}

	// 设置默认值
	shutdownTimeout := 10 * time.Second
	if cfg.ShutdownTimeout > 0 {
		shutdownTimeout = time.Duration(cfg.ShutdownTimeout) * time.Second
	}

	groupGracePeriod := 5 * time.Second
	if cfg.GroupGracePeriod > 0 {
		groupGracePeriod = time.Duration(cfg.GroupGracePeriod) * time.Second
	}

	groupMaxDelay := 20 * time.Second
	if cfg.GroupMaxDelay > 0 {
		groupMaxDelay = time.Duration(cfg.GroupMaxDelay) * time.Second
	}

	groupMaxSize := 100
	if cfg.GroupMaxSize > 0 {
		groupMaxSize = cfg.GroupMaxSize
	}

	// 创建 asynq 服务器配置
	serverConfig := asynq.Config{
		Concurrency:      cfg.Concurrency,
		StrictPriority:   cfg.StrictPriority,
		Queues:           queues,
		Logger:           &asynqLoggerAdapter{}, // 使用 pkg/log 作为 logger
		LogLevel:         logLevel,
		RetryDelayFunc:   asynq.DefaultRetryDelayFunc,
		ShutdownTimeout:  shutdownTimeout,
		GroupGracePeriod: groupGracePeriod,
		GroupMaxDelay:    groupMaxDelay,
		GroupMaxSize:     groupMaxSize,
	}

	// 创建 asynq 服务器
	server := asynq.NewServer(redisOpt, serverConfig)

	// 创建 ServeMux
	mux := asynq.NewServeMux()

	// 初始化步骤执行记录管理器
	taskRecordMgr, err := NewStepRunRecordManager(cfg.ClickHouse)
	if err != nil {
		return nil, fmt.Errorf("failed to create step run record manager: %w", err)
	}

	queue := &TaskQueue{
		client:        client,
		server:        server,
		mux:           mux,
		config:        cfg,
		handlers:      make(map[string]TaskHandler),
		taskRecordMgr: taskRecordMgr,
		redisOpt:      redisOpt, // 保存 Redis 连接选项
	}

	log.Infow("asynq task queue created",
		"concurrency", cfg.Concurrency,
		"queues", queues,
	)

	return queue, nil
}

// RegisterHandler 注册任务处理器
func (q *TaskQueue) RegisterHandler(taskType string, handler TaskHandler) {
	q.handlers[taskType] = handler

	// 注册到 asynq ServeMux
	q.mux.HandleFunc(taskType, func(ctx context.Context, t *asynq.Task) error {
		// 解析任务负载
		var taskPayload TaskPayload
		if err := sonic.Unmarshal(t.Payload(), &taskPayload); err != nil {
			return fmt.Errorf("unmarshal task payload: %w", err)
		}

		// 更新步骤执行状态为运行中
		if q.taskRecordMgr != nil {
			q.taskRecordMgr.RecordStepRunStarted(&taskPayload)
		}

		log.Infow("processing task",
			"task_id", taskPayload.TaskID,
			"task_type", taskPayload.TaskType,
			"priority", taskPayload.Priority,
		)

		// 执行任务
		err := handler.HandleTask(ctx, &taskPayload)

		// 更新步骤执行状态
		if err != nil {
			if q.taskRecordMgr != nil {
				q.taskRecordMgr.RecordStepRunFailed(&taskPayload, err)
			}
			return err
		}

		if q.taskRecordMgr != nil {
			q.taskRecordMgr.RecordStepRunCompleted(&taskPayload)
		}
		return nil
	})

	log.Infow("task handler registered", "task_type", taskType)
}

// RegisterHandlerFunc 注册任务处理器函数
func (q *TaskQueue) RegisterHandlerFunc(taskType string, handlerFunc TaskHandlerFunc) {
	q.RegisterHandler(taskType, handlerFunc)
}

// Enqueue 入队任务
func (q *TaskQueue) Enqueue(payload *TaskPayload, queueName string) error {
	data, err := sonic.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal task payload: %w", err)
	}

	// 确定队列名称
	if queueName == "" {
		queueName = q.config.DefaultQueue
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
	info, err := q.client.Enqueue(task, opts...)
	if err != nil {
		return fmt.Errorf("enqueue task: %w", err)
	}

	// 记录到 ClickHouse
	if q.taskRecordMgr != nil {
		q.taskRecordMgr.RecordStepRunEnqueued(payload, queueName)
	}

	log.Infow("task enqueued",
		"task_id", payload.TaskID,
		"task_type", payload.TaskType,
		"queue", queueName,
		"asynq_task_id", info.ID,
	)

	return nil
}

// EnqueueWithPriority 按优先级入队任务
// EnqueueWithPriority 根据权重入队任务
// priorityWeight: 任务权重，根据配置的队列权重自动选择最合适的队列
// 选择策略：找到权重 >= priorityWeight 的最小队列（向上匹配），如果没有则使用权重最高的队列
func (q *TaskQueue) EnqueueWithPriority(payload *TaskPayload, priorityWeight int) error {
	queueName := q.config.DefaultQueue
	if queueName == "" {
		queueName = Default
	}

	// 找到权重 >= priorityWeight 的最小队列（向上匹配）
	bestQueue := queueName
	bestWeight := -1
	for name, weight := range q.config.Queues {
		if weight >= priorityWeight {
			if bestWeight == -1 || weight < bestWeight {
				bestWeight = weight
				bestQueue = name
			}
		}
	}

	// 如果没有找到合适的队列（所有队列权重都 < priorityWeight），使用权重最高的队列
	if bestWeight == -1 && len(q.config.Queues) > 0 {
		maxWeight := 0
		for name, weight := range q.config.Queues {
			if weight > maxWeight {
				maxWeight = weight
				bestQueue = name
			}
		}
	}

	return q.Enqueue(payload, bestQueue)
}

// EnqueueDelayed 延迟入队任务
func (q *TaskQueue) EnqueueDelayed(payload *TaskPayload, delay time.Duration, queueName string) error {
	data, err := sonic.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal task payload: %w", err)
	}

	// 确定队列名称
	if queueName == "" {
		queueName = q.config.DefaultQueue
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
	info, err := q.client.Enqueue(task, opts...)
	if err != nil {
		return fmt.Errorf("enqueue delayed task: %w", err)
	}

	// 记录到 ClickHouse
	if q.taskRecordMgr != nil {
		q.taskRecordMgr.RecordStepRunEnqueued(payload, queueName)
	}

	log.Infow("delayed task enqueued",
		"task_id", payload.TaskID,
		"task_type", payload.TaskType,
		"queue", queueName,
		"delay", delay,
		"asynq_task_id", info.ID,
	)

	return nil
}

// Start 启动任务队列服务器
// 注意：Start() 方法会立即返回，不会阻塞。如果需要阻塞等待，请使用 Run() 方法。
func (q *TaskQueue) Start() error {
	log.Info("starting task queue server")
	return q.server.Start(q.mux)
}

// Run 启动任务队列服务器并阻塞等待信号
// 此方法会阻塞直到收到退出信号，然后自动关闭服务器
func (q *TaskQueue) Run() error {
	log.Info("running task queue server")
	return q.server.Run(q.mux)
}

// Shutdown 关闭任务队列服务器
func (q *TaskQueue) Shutdown() {
	log.Info("shutting down task queue server")

	q.server.Shutdown()
	log.Info("asynq server shut down successfully")

	// 关闭客户端
	if err := q.client.Close(); err != nil {
		log.Warnw("error closing asynq client", "error", err)
	} else {
		log.Info("asynq client closed successfully")
	}
}

// GetClient 获取 asynq 客户端
func (q *TaskQueue) GetClient() *asynq.Client {
	return q.client
}

// GetServer 获取 asynq 服务器
func (q *TaskQueue) GetServer() *asynq.Server {
	return q.server
}

// GetRedisConnOpt 获取 Redis 连接选项（用于创建 Inspector）
func (q *TaskQueue) GetRedisConnOpt() asynq.RedisConnOpt {
	return q.redisOpt
}

// redisConnOptWrapper 包装已有的 Redis 客户端实现 RedisConnOpt 接口
type redisConnOptWrapper struct {
	client redis.UniversalClient
}

// MakeRedisClient 实现 RedisConnOpt 接口
func (r *redisConnOptWrapper) MakeRedisClient() interface{} {
	return r.client
}
