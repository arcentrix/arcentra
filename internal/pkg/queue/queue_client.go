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
	"context"
	"fmt"
	"time"

	"github.com/arcentrix/arcade/pkg/log"
	"github.com/bytedance/sonic"
	"github.com/hibiken/asynq"
)

// Client 队列客户端（Agent 使用）
// 负责执行任务，不发布任务
type Client struct {
	server   *asynq.Server
	mux      *asynq.ServeMux
	config   *Config
	handlers map[string]TaskHandler
	redisOpt asynq.RedisConnOpt // 保存 Redis 连接选项，用于创建 Inspector
}

// NewQueueClient 创建队列客户端（Agent 使用）
func NewQueueClient(cfg *Config) (*Client, error) {
	if cfg == nil {
		return nil, fmt.Errorf("queue config is required")
	}

	if cfg.RedisClient == nil {
		return nil, fmt.Errorf("redis client is required")
	}

	redisOpt := &redisConnOptWrapper{client: cfg.RedisClient}

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

	client := &Client{
		server:   server,
		mux:      mux,
		config:   cfg,
		handlers: make(map[string]TaskHandler),
		redisOpt: redisOpt,
	}

	log.Infow("queue client created",
		"concurrency", cfg.Concurrency,
		"queues", queues,
	)

	return client, nil
}

// RegisterHandler 注册任务处理器
func (c *Client) RegisterHandler(taskType string, handler TaskHandler) {
	c.handlers[taskType] = handler

	// 注册到 asynq ServeMux
	c.mux.HandleFunc(taskType, func(ctx context.Context, t *asynq.Task) error {
		var taskPayload TaskPayload
		if err := sonic.Unmarshal(t.Payload(), &taskPayload); err != nil {
			return fmt.Errorf("unmarshal task payload: %w", err)
		}

		log.Infow("processing task",
			"task_id", taskPayload.TaskID,
			"task_type", taskPayload.TaskType,
			"priority", taskPayload.Priority,
		)

		// 执行任务
		err := handler.HandleTask(ctx, &taskPayload)
		if err != nil {
			log.Errorw("task execution failed",
				"task_id", taskPayload.TaskID,
				"task_type", taskPayload.TaskType,
				"error", err,
			)
			return err
		}

		log.Infow("task execution completed",
			"task_id", taskPayload.TaskID,
			"task_type", taskPayload.TaskType,
		)

		return nil
	})

	log.Infow("task handler registered", "task_type", taskType)
}

// RegisterHandlerFunc 注册任务处理器函数
func (c *Client) RegisterHandlerFunc(taskType string, handlerFunc TaskHandlerFunc) {
	c.RegisterHandler(taskType, handlerFunc)
}

// Start 启动任务队列客户端
// 注意：Start() 方法会立即返回，不会阻塞。如果需要阻塞等待，请使用 Run() 方法。
func (c *Client) Start() error {
	log.Info("starting queue client")
	return c.server.Start(c.mux)
}

// Run 启动任务队列客户端并阻塞等待信号
// 此方法会阻塞直到收到退出信号，然后自动关闭服务器
func (c *Client) Run() error {
	log.Info("running queue client")
	return c.server.Run(c.mux)
}

// Shutdown 关闭任务队列客户端
func (c *Client) Shutdown() {
	c.server.Shutdown()
	log.Info("asynq server shut down successfully")
}

// GetServer 获取 asynq 服务器
func (c *Client) GetServer() *asynq.Server {
	return c.server
}

// GetRedisConnOpt 获取 Redis 连接选项（用于创建 Inspector）
func (c *Client) GetRedisConnOpt() asynq.RedisConnOpt {
	return c.redisOpt
}
