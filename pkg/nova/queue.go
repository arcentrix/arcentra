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

package nova

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/google/uuid"
)

const (
	// DefaultGroupID is the default consumer group ID format
	DefaultGroupID = "TASK_QUEUE_GROUP_%d"
	// DefaultTopicPrefix is the default topic prefix
	DefaultTopicPrefix = "TASK_QUEUE"
	// DefaultDelaySlotCount is the default number of delay slots
	DefaultDelaySlotCount = 24
	// DefaultDelaySlotDuration is the default time interval for each delay slot
	DefaultDelaySlotDuration = time.Hour
	// DefaultAutoCommit is the default auto-commit setting
	DefaultAutoCommit = false
	// DefaultSessionTimeout is the default session timeout in milliseconds
	DefaultSessionTimeout = 30000
	// DefaultMaxPollInterval is the default maximum poll interval in milliseconds
	DefaultMaxPollInterval = 300000

	// PriorityHighSuffix is the suffix for high priority queues
	PriorityHighSuffix = "_PRIORITY_HIGH"
	// PriorityNormalSuffix is the suffix for normal priority queues
	PriorityNormalSuffix = "_PRIORITY_NORMAL"
	// PriorityLowSuffix is the suffix for low priority queues
	PriorityLowSuffix = "_PRIORITY_LOW"
	// TasksSuffix is the suffix for task topics
	TasksSuffix = "%s_TASKS"
)

// TaskMessage represents a task message structure
type TaskMessage struct {
	TaskID   string   `json:"task_id"`
	Task     *Task    `json:"task"`
	Queue    string   `json:"queue"`
	Priority Priority `json:"priority"`
}

// generateTaskID generates a task ID
func generateTaskID() string {
	return fmt.Sprintf(TasksSuffix, uuid.New().String())
}

// TaskQueueImpl is a generic task queue implementation
type TaskQueueImpl struct {
	config         *queueConfig
	broker         MessageQueueBroker
	delayManager   DelayManager
	handler        IHandler
	batchHandler   IBatchHandler
	aggregator     IAggregator
	recorder       TaskRecorder // Task recorder (optional)
	codec          MessageCodec // Message codec
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	mu             sync.RWMutex
	running        bool
	priorityQueues map[Priority]string
}

// DelayManager is the interface for delay managers
type DelayManager interface {
	Start() error
	Stop() error
	EnqueueDelay(task *Task, executeAt time.Time, targetQueue string, priority Priority) error
	SetCodec(codec MessageCodec) // Set message codec
}

// NewTaskQueue creates a new task queue using the option pattern to configure different brokers
func NewTaskQueue(opts ...QueueOption) (TaskQueue, error) {
	// Default configuration
	config := &queueConfig{
		GroupID:           fmt.Sprintf(DefaultGroupID, os.Getpid()),
		TopicPrefix:       DefaultTopicPrefix,
		DelaySlotCount:    DefaultDelaySlotCount,
		DelaySlotDuration: DefaultDelaySlotDuration,
		AutoCommit:        DefaultAutoCommit,
		SessionTimeout:    DefaultSessionTimeout,
		MaxPollInterval:   DefaultMaxPollInterval,
		messageFormat:     MessageFormatJSON, // Default to JSON
		messageCodec:      DefaultMessageCodec,
	}

	// Apply options
	for _, opt := range opts {
		opt.apply(config)
	}

	// Validate required configuration
	if config.Type == "" {
		return nil, fmt.Errorf("broker type is required, use WithKafka or WithRocketMQ")
	}

	// Use default GroupID if not set
	if config.GroupID == "" {
		config.GroupID = fmt.Sprintf(DefaultGroupID, os.Getpid())
	}

	// Use default TopicPrefix if not set
	if config.TopicPrefix == "" {
		config.TopicPrefix = DefaultTopicPrefix
	}

	// Create broker based on type
	var broker MessageQueueBroker
	var delayManager DelayManager
	var err error

	switch config.Type {
	case QueueTypeKafka:
		broker, delayManager, err = newKafkaBroker(config)
	case QueueTypeRocketMQ:
		broker, delayManager, err = newRocketMQBroker(config)
	default:
		return nil, fmt.Errorf("unsupported queue type: %s", config.Type)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create broker: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Get actual topic prefix
	topicPrefix := config.TopicPrefix
	if config.kafkaConfig != nil && config.kafkaConfig.TopicPrefix != "" {
		topicPrefix = config.kafkaConfig.TopicPrefix
	} else if config.rocketmqConfig != nil && config.rocketmqConfig.TopicPrefix != "" {
		topicPrefix = config.rocketmqConfig.TopicPrefix
	}

	// Initialize priority queue mapping
	priorityQueues := map[Priority]string{
		PriorityHigh:   fmt.Sprintf("%s%s", topicPrefix, PriorityHighSuffix),
		PriorityNormal: fmt.Sprintf("%s%s", topicPrefix, PriorityNormalSuffix),
		PriorityLow:    fmt.Sprintf("%s%s", topicPrefix, PriorityLowSuffix),
	}

	// Determine message codec
	codec := config.messageCodec
	if codec == nil {
		codec = DefaultMessageCodec
	}

	queue := &TaskQueueImpl{
		config:         config,
		broker:         broker,
		delayManager:   delayManager,
		recorder:       config.taskRecorder, // Get task recorder from config
		codec:          codec,               // Set message codec
		ctx:            ctx,
		cancel:         cancel,
		priorityQueues: priorityQueues,
	}

	return queue, nil
}

// Enqueue enqueues a single task
func (q *TaskQueueImpl) Enqueue(task *Task, opts ...Option) (*Result, error) {
	optsObj := &TaskOpts{
		Priority:  PriorityNormal,
		ProcessAt: time.Time{},
		Queue:     "",
	}

	for _, opt := range opts {
		opt.apply(optsObj)
	}

	// Determine queue name
	queueName := q.getQueueName(optsObj.Queue, optsObj.Priority)

	// Use delay manager if there's a delay time
	if !optsObj.ProcessAt.IsZero() {
		now := time.Now()
		if optsObj.ProcessAt.After(now) {
			err := q.delayManager.EnqueueDelay(task, optsObj.ProcessAt, queueName, optsObj.Priority)
			if err != nil {
				return nil, err
			}
			return &Result{
				ID:       generateTaskID(),
				Queue:    queueName,
				Priority: optsObj.Priority,
				ETA:      optsObj.ProcessAt,
			}, nil
		}
	}

	// Send immediately
	return q.sendTask(task, queueName, optsObj.Priority)
}

// EnqueueBatch enqueues multiple tasks in batch
func (q *TaskQueueImpl) EnqueueBatch(tasks []*Task, opts ...Option) (*Result, error) {
	if len(tasks) == 0 {
		return nil, fmt.Errorf("no tasks to enqueue")
	}

	optsObj := &TaskOpts{
		Priority:  PriorityNormal,
		ProcessAt: time.Time{},
		Queue:     "",
	}

	for _, opt := range opts {
		opt.apply(optsObj)
	}

	// Determine queue name
	queueName := q.getQueueName(optsObj.Queue, optsObj.Priority)

	// Use delay manager for batch sending if there's a delay time
	if !optsObj.ProcessAt.IsZero() {
		now := time.Now()
		if optsObj.ProcessAt.After(now) {
			// Batch delay tasks
			for _, task := range tasks {
				if err := q.delayManager.EnqueueDelay(task, optsObj.ProcessAt, queueName, optsObj.Priority); err != nil {
					return nil, fmt.Errorf("failed to enqueue delayed task: %w", err)
				}
			}
			return &Result{
				ID:       generateTaskID(),
				Queue:    queueName,
				Priority: optsObj.Priority,
				ETA:      optsObj.ProcessAt,
			}, nil
		}
	}

	// Send batch immediately
	return q.sendBatchTasks(tasks, queueName, optsObj.Priority)
}

// Start starts the consumer in single task processing mode
func (q *TaskQueueImpl) Start(handler IHandler) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.running {
		return fmt.Errorf("task queue is already running")
	}

	if handler == nil {
		return fmt.Errorf("handler is required")
	}

	q.handler = handler
	q.running = true

	// Start delay manager
	if err := q.delayManager.Start(); err != nil {
		q.running = false
		return fmt.Errorf("failed to start delay manager: %w", err)
	}

	// Subscribe to all priority queues
	topics := make([]string, 0, len(q.priorityQueues))
	for _, topic := range q.priorityQueues {
		topics = append(topics, topic)
	}
	topicPrefix := q.config.TopicPrefix
	if q.config.kafkaConfig != nil && q.config.kafkaConfig.TopicPrefix != "" {
		topicPrefix = q.config.kafkaConfig.TopicPrefix
	} else if q.config.rocketmqConfig != nil && q.config.rocketmqConfig.TopicPrefix != "" {
		topicPrefix = q.config.rocketmqConfig.TopicPrefix
	}
	topics = append(topics, fmt.Sprintf("%s%s", topicPrefix, TasksSuffix))

	// Start consumption goroutine
	q.wg.Add(1)
	safe.Go(func() {
		defer q.wg.Done()
		_ = q.broker.Subscribe(q.ctx, topics, q.handleMessage)
	})

	return nil
}

// StartBatch starts the consumer in batch processing mode
func (q *TaskQueueImpl) StartBatch(handler IBatchHandler, agg IAggregator) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.running {
		return fmt.Errorf("task queue is already running")
	}

	if handler == nil {
		return fmt.Errorf("batch handler is required")
	}

	if agg == nil {
		return fmt.Errorf("aggregator is required")
	}

	q.batchHandler = handler
	q.aggregator = agg
	q.running = true

	// Start delay manager
	if err := q.delayManager.Start(); err != nil {
		q.running = false
		return fmt.Errorf("failed to start delay manager: %w", err)
	}

	// Subscribe to all priority queues
	topics := make([]string, 0, len(q.priorityQueues))
	for _, topic := range q.priorityQueues {
		topics = append(topics, topic)
	}
	topicPrefix := q.config.TopicPrefix
	if q.config.kafkaConfig != nil && q.config.kafkaConfig.TopicPrefix != "" {
		topicPrefix = q.config.kafkaConfig.TopicPrefix
	} else if q.config.rocketmqConfig != nil && q.config.rocketmqConfig.TopicPrefix != "" {
		topicPrefix = q.config.rocketmqConfig.TopicPrefix
	}
	topics = append(topics, fmt.Sprintf("%s%s", topicPrefix, TasksSuffix))

	// Start batch consumption goroutine
	q.wg.Add(1)
	safe.Go(func() {
		defer q.wg.Done()
		_ = q.broker.Subscribe(q.ctx, topics, q.handleBatchMessage)
	})

	return nil
}

// Stop stops the task queue
func (q *TaskQueueImpl) Stop() error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if !q.running {
		return nil
	}

	q.running = false
	q.cancel()

	// Stop delay manager
	if err := q.delayManager.Stop(); err != nil {
		// Log error but continue shutdown
	}

	// Wait for all goroutines to exit
	q.wg.Wait()

	// Close broker
	if err := q.broker.Close(); err != nil {
		return fmt.Errorf("failed to close broker: %w", err)
	}

	return nil
}

// handleMessage handles a single message
func (q *TaskQueueImpl) handleMessage(ctx context.Context, msg *Message) error {
	// Decode task message
	var taskMsg TaskMessage
	if err := q.codec.Decode(msg.Value, &taskMsg); err != nil {
		return fmt.Errorf("failed to decode task message: %w", err)
	}

	// Process task
	if q.handler != nil {
		if err := q.handler.ProcessTask(ctx, taskMsg.Task); err != nil {
			return fmt.Errorf("failed to process task: %w", err)
		}
	}

	return nil
}

// handleBatchMessage handles batch messages
func (q *TaskQueueImpl) handleBatchMessage(ctx context.Context, msg *Message) error {
	// Decode task message
	var taskMsg TaskMessage
	if err := q.codec.Decode(msg.Value, &taskMsg); err != nil {
		return fmt.Errorf("failed to decode task message: %w", err)
	}

	// Add to aggregator
	if q.aggregator != nil {
		q.aggregator.Add(taskMsg.Task)

		// Check if flush is needed
		if q.aggregator.ShouldFlush() {
			tasks := q.aggregator.Flush()
			if len(tasks) > 0 && q.batchHandler != nil {
				if err := q.batchHandler.ProcessBatch(ctx, tasks); err != nil {
					return fmt.Errorf("failed to process batch: %w", err)
				}
			}
		}
	}

	return nil
}

// sendTask sends a single task
func (q *TaskQueueImpl) sendTask(task *Task, queueName string, priority Priority) (*Result, error) {
	taskMsg := &TaskMessage{
		TaskID:   generateTaskID(),
		Task:     task,
		Queue:    queueName,
		Priority: priority,
	}

	msgData, err := q.codec.Encode(taskMsg)
	if err != nil {
		return nil, fmt.Errorf("failed to encode task message: %w", err)
	}

	headers := map[string]string{
		"queue":     queueName,
		"priority":  fmt.Sprintf("%d", priority),
		"task_type": task.Type,
	}

	if err := q.broker.SendMessage(q.ctx, queueName, taskMsg.TaskID, msgData, headers); err != nil {
		return nil, fmt.Errorf("failed to send message: %w", err)
	}

	return &Result{
		ID:       taskMsg.TaskID,
		Queue:    queueName,
		Priority: priority,
		ETA:      time.Now(),
	}, nil
}

// sendBatchTasks sends multiple tasks in batch
func (q *TaskQueueImpl) sendBatchTasks(tasks []*Task, queueName string, priority Priority) (*Result, error) {
	if len(tasks) == 0 {
		return nil, fmt.Errorf("no tasks to send")
	}

	messages := make([]Message, 0, len(tasks))
	for _, task := range tasks {
		taskMsg := &TaskMessage{
			TaskID:   generateTaskID(),
			Task:     task,
			Queue:    queueName,
			Priority: priority,
		}

		msgData, err := q.codec.Encode(taskMsg)
		if err != nil {
			return nil, fmt.Errorf("failed to encode task message: %w", err)
		}

		headers := map[string]string{
			"queue":     queueName,
			"priority":  fmt.Sprintf("%d", priority),
			"task_type": task.Type,
		}

		messages = append(messages, Message{
			Key:     taskMsg.TaskID,
			Value:   msgData,
			Headers: headers,
		})
	}

	if err := q.broker.SendBatchMessages(q.ctx, queueName, messages); err != nil {
		return nil, fmt.Errorf("failed to send batch messages: %w", err)
	}

	return &Result{
		ID:       generateTaskID(),
		Queue:    queueName,
		Priority: priority,
		ETA:      time.Now(),
	}, nil
}

// getQueueName gets the queue name based on custom queue and priority
func (q *TaskQueueImpl) getQueueName(customQueue string, priority Priority) string {
	if customQueue != "" {
		topicPrefix := q.config.TopicPrefix
		if q.config.kafkaConfig != nil && q.config.kafkaConfig.TopicPrefix != "" {
			topicPrefix = q.config.kafkaConfig.TopicPrefix
		} else if q.config.rocketmqConfig != nil && q.config.rocketmqConfig.TopicPrefix != "" {
			topicPrefix = q.config.rocketmqConfig.TopicPrefix
		}
		return fmt.Sprintf("%s-%s", topicPrefix, customQueue)
	}
	return q.priorityQueues[priority]
}
