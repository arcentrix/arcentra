// Copyright 2025 Arcentra Authors.
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
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	mqkafka "github.com/arcentrix/arcentra/pkg/mq/kafka"
	ckafka "github.com/confluentinc/confluent-kafka-go/v2/kafka"
)

const (
	// kafkaDelayTopicFormat is the format string for Kafka delay topics
	kafkaDelayTopicFormat = "%s_DELAY_%d"
)

// DelayTopicManager manages delay topics using multiple delay topics (time-sharded) to manage delayed tasks
type DelayTopicManager struct {
	producer     *mqkafka.Producer
	consumer     *mqkafka.Consumer
	delayTopics  []string      // List of delay topics
	targetTopic  string        // Target topic (where messages are sent after delay expires)
	slotDuration time.Duration // Time interval for each delay topic
	slotCount    int           // Number of delay topics
	timerWheel   *TimerWheel   // Timer wheel for precise delay control
	codec        MessageCodec  // Message codec
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
}

// DelayMessage represents a delayed message structure
type DelayMessage struct {
	TaskID      string    `json:"task_id"`
	Task        *Task     `json:"task"`
	TargetTopic string    `json:"target_topic"`
	TargetQueue string    `json:"target_queue"`
	Priority    Priority  `json:"priority"`
	ExecuteAt   time.Time `json:"execute_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// NewDelayTopicManager creates a new delay topic manager
func NewDelayTopicManager(
	producer *mqkafka.Producer,
	consumer *mqkafka.Consumer,
	targetTopic string,
	slotCount int,
	slotDuration time.Duration,
) *DelayTopicManager {
	ctx, cancel := context.WithCancel(context.Background())

	dm := &DelayTopicManager{
		producer:     producer,
		consumer:     consumer,
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
		dm.delayTopics[i] = fmt.Sprintf(kafkaDelayTopicFormat, targetTopic, i)
	}

	return dm
}

// Start starts the delay topic manager
func (dm *DelayTopicManager) Start() error {
	// Start timer wheel
	dm.timerWheel.Start()

	// Subscribe to all delay topics
	topics := make([]string, len(dm.delayTopics))
	copy(topics, dm.delayTopics)

	if err := dm.consumer.Subscribe(topics); err != nil {
		return fmt.Errorf("failed to subscribe delay topics: %w", err)
	}

	// Start consumption goroutine
	dm.wg.Add(1)
	go dm.consumeDelayMessages()

	return nil
}

// Stop stops the delay topic manager
func (dm *DelayTopicManager) Stop() error {
	dm.cancel()
	dm.timerWheel.Stop()
	dm.wg.Wait()
	return nil
}

// EnqueueDelay enqueues a delayed task
func (dm *DelayTopicManager) EnqueueDelay(
	task *Task,
	executeAt time.Time,
	targetQueue string,
	priority Priority,
) error {
	now := time.Now()
	if executeAt.Before(now) || executeAt.Equal(now) {
		// Execute immediately, send directly to target topic
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

	// Otherwise use delay topics
	delayMsg := &DelayMessage{
		TaskID:      generateTaskID(),
		Task:        task,
		TargetTopic: dm.targetTopic,
		TargetQueue: targetQueue,
		Priority:    priority,
		ExecuteAt:   executeAt,
		CreatedAt:   now,
	}

	// Select delay topic (based on execution time)
	topicIndex := dm.selectDelayTopic(executeAt)
	delayTopic := dm.delayTopics[topicIndex]

	// Serialize message
	msgData, err := dm.codec.Encode(delayMsg)
	if err != nil {
		return fmt.Errorf("failed to encode delay message: %w", err)
	}

	// Send to delay topic, using execution time as key (for partitioning)
	key := fmt.Sprintf("%d", executeAt.Unix())
	headers := map[string]string{
		"execute_at":   executeAt.Format(time.RFC3339),
		"target_topic": dm.targetTopic,
		"target_queue": targetQueue,
		"priority":     strconv.Itoa(int(priority)),
	}
	if err := dm.producer.Send(dm.ctx, delayTopic, key, msgData, headers); err != nil {
		return fmt.Errorf("failed to produce delay message: %w", err)
	}

	return nil
}

// selectDelayTopic selects a delay topic based on execution time
func (dm *DelayTopicManager) selectDelayTopic(executeAt time.Time) int {
	now := time.Now()
	delay := executeAt.Sub(now)

	// Calculate which delay topic to use
	slotIndex := int(delay / dm.slotDuration)
	if slotIndex >= dm.slotCount {
		slotIndex = dm.slotCount - 1
	}

	return slotIndex
}

// consumeDelayMessages consumes messages from delay topics
func (dm *DelayTopicManager) consumeDelayMessages() {
	defer dm.wg.Done()

	for {
		select {
		case <-dm.ctx.Done():
			return
		default:
			msg, err := dm.consumer.ReadMessage(100 * time.Millisecond)
			if err != nil {
				var kafkaErr ckafka.Error
				if errors.As(err, &kafkaErr) && kafkaErr.Code() == ckafka.ErrTimedOut {
					continue
				}
				// Log error but continue running
				continue
			}

			// Decode delay message
			var delayMsg DelayMessage
			if err := dm.codec.Decode(msg.Value, &delayMsg); err != nil {
				// Log error but continue processing
				continue
			}

			// Check if execution time has arrived
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
					// Resend to a more appropriate delay topic
					_ = dm.EnqueueDelay(delayMsg.Task, delayMsg.ExecuteAt, delayMsg.TargetQueue, delayMsg.Priority)
				}
			} else {
				// Execution time has arrived, send to target topic
				_ = dm.sendToTargetTopic(delayMsg.Task, delayMsg.TargetQueue, delayMsg.Priority)
			}
		}
	}
}

// sendToTargetTopic sends a task to the target topic
func (dm *DelayTopicManager) sendToTargetTopic(task *Task, queue string, priority Priority) error {
	taskMsg := &TaskMessage{
		TaskID:   generateTaskID(),
		Task:     task,
		Queue:    queue,
		Priority: priority,
	}

	msgData, err := dm.codec.Encode(taskMsg)
	if err != nil {
		return fmt.Errorf("failed to encode task message: %w", err)
	}

	headers := map[string]string{
		"queue":     queue,
		"priority":  strconv.Itoa(int(priority)),
		"task_type": task.Type,
	}
	if err := dm.producer.Send(dm.ctx, dm.targetTopic, taskMsg.TaskID, msgData, headers); err != nil {
		return fmt.Errorf("failed to produce task message: %w", err)
	}

	return nil
}

// SetCodec sets the message codec
func (dm *DelayTopicManager) SetCodec(codec MessageCodec) {
	dm.codec = codec
}
