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
	"sync"
	"time"

	"github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
)

const (
	// rocketmqDelayTopicFormat is the format string for RocketMQ delay topics
	rocketmqDelayTopicFormat = "%s_DELAY_%d"
)

// RocketMQDelayMessage represents a delayed message structure
type RocketMQDelayMessage struct {
	TaskID      string    `json:"task_id"`
	Task        *Task     `json:"task"`
	TargetTopic string    `json:"target_topic"`
	TargetQueue string    `json:"target_queue"`
	Priority    Priority  `json:"priority"`
	ExecuteAt   time.Time `json:"execute_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// RocketMQTaskMessage represents a task message structure
type RocketMQTaskMessage struct {
	TaskID   string   `json:"task_id"`
	Task     *Task    `json:"task"`
	Queue    string   `json:"queue"`
	Priority Priority `json:"priority"`
}

// RocketMQDelayManager is a RocketMQ delay manager
type RocketMQDelayManager struct {
	producer     rocketmq.Producer
	consumer     rocketmq.PushConsumer
	delayTopics  []string
	targetTopic  string
	slotDuration time.Duration
	slotCount    int
	timerWheel   *TimerWheel
	codec        MessageCodec // Message codec
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
}

// NewRocketMQDelayManager creates a new RocketMQ delay manager
func NewRocketMQDelayManager(
	p rocketmq.Producer,
	c rocketmq.PushConsumer,
	targetTopic string,
	slotCount int,
	slotDuration time.Duration,
) *RocketMQDelayManager {
	ctx, cancel := context.WithCancel(context.Background())

	dm := &RocketMQDelayManager{
		producer:     p,
		consumer:     c,
		targetTopic:  targetTopic,
		slotCount:    slotCount,
		slotDuration: slotDuration,
		codec:        DefaultMessageCodec, // Default to JSON codec
		ctx:          ctx,
		cancel:       cancel,
		timerWheel:   NewTimerWheel(DefaultDelaySlotCount, int64(DefaultDelaySlotDuration.Milliseconds())),
	}

	// Generate delay topic names
	dm.delayTopics = make([]string, slotCount)
	for i := 0; i < slotCount; i++ {
		dm.delayTopics[i] = fmt.Sprintf(rocketmqDelayTopicFormat, targetTopic, i)
	}

	return dm
}

// Start starts the delay manager
func (dm *RocketMQDelayManager) Start() error {
	dm.timerWheel.Start()

	// Subscribe to all delay topics
	for _, topic := range dm.delayTopics {
		if err := dm.consumer.Subscribe(topic, consumer.MessageSelector{}, dm.handleDelayMessage); err != nil {
			return fmt.Errorf("failed to subscribe delay topic %s: %w", topic, err)
		}
	}

	// Start consumer
	if err := dm.consumer.Start(); err != nil {
		return fmt.Errorf("failed to start consumer: %w", err)
	}

	return nil
}

// Stop stops the delay manager
func (dm *RocketMQDelayManager) Stop() error {
	dm.cancel()
	dm.timerWheel.Stop()
	dm.wg.Wait()
	return nil
}

// EnqueueDelay enqueues a delayed task
func (dm *RocketMQDelayManager) EnqueueDelay(
	task *Task,
	executeAt time.Time,
	targetQueue string,
	priority Priority,
) error {
	now := time.Now()
	if executeAt.Before(now) || executeAt.Equal(now) {
		return dm.sendToTargetTopic(task, targetQueue, priority)
	}

	delay := executeAt.Sub(now)

	// Use timer wheel if delay time is less than maximum timer wheel delay
	maxTimerWheelDelay := time.Duration(dm.timerWheel.GetSlotCount()) * time.Duration(dm.timerWheel.GetTickMs()) * time.Millisecond
	if delay < maxTimerWheelDelay {
		dm.timerWheel.AddAt(task, executeAt, func(t *Task) {
			_ = dm.sendToTargetTopic(t, targetQueue, priority)
		})
		return nil
	}

	// Use RocketMQ delay message feature
	delayMsg := &RocketMQDelayMessage{
		TaskID:      generateTaskID(),
		Task:        task,
		TargetTopic: dm.targetTopic,
		TargetQueue: targetQueue,
		Priority:    priority,
		ExecuteAt:   executeAt,
		CreatedAt:   now,
	}

	msgData, err := dm.codec.Encode(delayMsg)
	if err != nil {
		return fmt.Errorf("failed to encode delay message: %w", err)
	}

	// Select delay topic
	topicIndex := dm.selectDelayTopic(executeAt)
	delayTopic := dm.delayTopics[topicIndex]

	// Calculate delay level (RocketMQ supports 18 delay levels)
	delayLevel := dm.calculateDelayLevel(delay)

	msg := primitive.NewMessage(delayTopic, msgData)
	msg.WithKeys([]string{delayMsg.TaskID})
	msg.WithProperty("execute_at", executeAt.Format(time.RFC3339))
	msg.WithProperty("target_topic", dm.targetTopic)
	msg.WithProperty("target_queue", targetQueue)
	msg.WithProperty("priority", fmt.Sprintf("%d", priority))

	// Set delay level
	msg.WithDelayTimeLevel(delayLevel)

	if _, err := dm.producer.SendSync(context.Background(), msg); err != nil {
		return fmt.Errorf("failed to send delay message: %w", err)
	}

	return nil
}

// handleDelayMessage handles delayed messages
func (dm *RocketMQDelayManager) handleDelayMessage(ctx context.Context, msgs ...*primitive.MessageExt) (consumer.ConsumeResult, error) {
	for _, msg := range msgs {
		var delayMsg RocketMQDelayMessage
		if err := dm.codec.Decode(msg.Body, &delayMsg); err != nil {
			return consumer.ConsumeSuccess, nil // Ignore error messages
		}

		now := time.Now()
		if delayMsg.ExecuteAt.After(now) {
			// Not yet execution time, reschedule using timer wheel
			remainingDelay := delayMsg.ExecuteAt.Sub(now)
			maxTimerWheelDelay := time.Duration(dm.timerWheel.GetSlotCount()) * time.Duration(dm.timerWheel.GetTickMs()) * time.Millisecond

			if remainingDelay < maxTimerWheelDelay {
				dm.timerWheel.AddAt(delayMsg.Task, delayMsg.ExecuteAt, func(t *Task) {
					_ = dm.sendToTargetTopic(t, delayMsg.TargetQueue, delayMsg.Priority)
				})
			} else {
				_ = dm.EnqueueDelay(delayMsg.Task, delayMsg.ExecuteAt, delayMsg.TargetQueue, delayMsg.Priority)
			}
		} else {
			_ = dm.sendToTargetTopic(delayMsg.Task, delayMsg.TargetQueue, delayMsg.Priority)
		}
	}

	return consumer.ConsumeSuccess, nil
}

// selectDelayTopic selects a delay topic based on execution time
func (dm *RocketMQDelayManager) selectDelayTopic(executeAt time.Time) int {
	now := time.Now()
	delay := executeAt.Sub(now)

	slotIndex := int(delay / dm.slotDuration)
	if slotIndex >= dm.slotCount {
		slotIndex = dm.slotCount - 1
	}

	return slotIndex
}

// calculateDelayLevel calculates the RocketMQ delay level
// RocketMQ supports 18 delay levels: 1s, 5s, 10s, 30s, 1m, 2m, 3m, 4m, 5m, 6m, 7m, 8m, 9m, 10m, 20m, 30m, 1h, 2h
func (dm *RocketMQDelayManager) calculateDelayLevel(delay time.Duration) int {
	seconds := int(delay.Seconds())

	levels := []int{1, 5, 10, 30, 60, 120, 180, 240, 300, 360, 420, 480, 540, 600, 1200, 1800, 3600, 7200}

	for i, level := range levels {
		if seconds <= level {
			return i + 1
		}
	}

	return 18 // Maximum delay level
}

// sendToTargetTopic sends a task to the target topic
func (dm *RocketMQDelayManager) sendToTargetTopic(task *Task, queue string, priority Priority) error {
	taskMsg := &RocketMQTaskMessage{
		TaskID:   generateTaskID(),
		Task:     task,
		Queue:    queue,
		Priority: priority,
	}

	msgData, err := dm.codec.Encode(taskMsg)
	if err != nil {
		return fmt.Errorf("failed to encode task message: %w", err)
	}

	msg := primitive.NewMessage(dm.targetTopic, msgData)
	msg.WithKeys([]string{taskMsg.TaskID})
	msg.WithProperty("queue", queue)
	msg.WithProperty("priority", fmt.Sprintf("%d", priority))
	msg.WithProperty("task_type", task.Type)

	if _, err := dm.producer.SendSync(context.Background(), msg); err != nil {
		return fmt.Errorf("failed to send task message: %w", err)
	}

	return nil
}

// SetCodec sets the message codec
func (dm *RocketMQDelayManager) SetCodec(codec MessageCodec) {
	dm.codec = codec
}
