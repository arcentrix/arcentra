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

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	// rabbitmqDelayTopicFormat is the format string for RabbitMQ delay topics
	rabbitmqDelayTopicFormat     = "%s_DELAY_%d"
	rabbitmqDelayExchangeSuffix  = "_DELAY_EXCHANGE"
	rabbitmqTargetExchangeSuffix = "_TARGET_EXCHANGE"
	rabbitmqTargetQueueSuffix    = "_TARGET_QUEUE"
	rabbitmqQueueSuffix          = "_QUEUE"
)

// RabbitMQDelayMessage represents a delayed message structure
type RabbitMQDelayMessage struct {
	TaskID      string    `json:"task_id"`
	Task        *Task     `json:"task"`
	TargetTopic string    `json:"target_topic"`
	TargetQueue string    `json:"target_queue"`
	Priority    Priority  `json:"priority"`
	ExecuteAt   time.Time `json:"execute_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// RabbitMQTaskMessage represents a task message structure
type RabbitMQTaskMessage struct {
	TaskID   string   `json:"task_id"`
	Task     *Task    `json:"task"`
	Queue    string   `json:"queue"`
	Priority Priority `json:"priority"`
}

// RabbitMQDelayManager is a RabbitMQ delay manager that implements delayed messages using dead letter queues (DLX) and TTL
type RabbitMQDelayManager struct {
	channel      *amqp.Channel
	exchange     string
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

// NewRabbitMQDelayManager creates a new RabbitMQ delay manager
func NewRabbitMQDelayManager(
	channel *amqp.Channel,
	exchange string,
	targetTopic string,
	slotCount int,
	slotDuration time.Duration,
) *RabbitMQDelayManager {
	ctx, cancel := context.WithCancel(context.Background())

	dm := &RabbitMQDelayManager{
		channel:      channel,
		exchange:     exchange,
		targetTopic:  targetTopic,
		slotCount:    slotCount,
		slotDuration: slotDuration,
		codec:        DefaultMessageCodec, // Default to JSON codec
		ctx:          ctx,
		cancel:       cancel,
		timerWheel:   NewTimerWheel(DefaultDelaySlotCount, int64(DefaultDelaySlotDuration.Milliseconds())),
	}

	// Generate delay queue names
	dm.delayTopics = make([]string, slotCount)
	for i := 0; i < slotCount; i++ {
		dm.delayTopics[i] = fmt.Sprintf(rabbitmqDelayTopicFormat, targetTopic, i)
	}

	return dm
}

// Start starts the delay manager
func (dm *RabbitMQDelayManager) Start() error {
	dm.timerWheel.Start()

	// Create delay exchange and queues
	delayExchange := fmt.Sprintf("%s%s", dm.exchange, rabbitmqDelayExchangeSuffix)

	// Declare delay exchange
	if err := dm.channel.ExchangeDeclare(
		delayExchange,
		"topic",
		true,
		false,
		false,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("failed to declare delay exchange: %w", err)
	}

	// Declare target exchange (for dead letters)
	targetExchange := fmt.Sprintf("%s%s", dm.exchange, rabbitmqTargetExchangeSuffix)
	if err := dm.channel.ExchangeDeclare(
		targetExchange,
		"topic",
		true,
		false,
		false,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("failed to declare target exchange: %w", err)
	}

	// Declare target queue
	targetQueue := fmt.Sprintf("%s%s", dm.targetTopic, rabbitmqTargetQueueSuffix)
	if _, err := dm.channel.QueueDeclare(
		targetQueue,
		true,
		false,
		false,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("failed to declare target queue: %w", err)
	}

	// Bind target queue to target exchange
	if err := dm.channel.QueueBind(
		targetQueue,
		dm.targetTopic,
		targetExchange,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("failed to bind target queue: %w", err)
	}

	// Create queue for each delay slot
	for i, delayTopic := range dm.delayTopics {
		queueName := fmt.Sprintf("%s%s", delayTopic, rabbitmqQueueSuffix)

		// Calculate TTL (in milliseconds)
		ttl := int64((time.Duration(i+1) * dm.slotDuration).Milliseconds())

		// Declare delay queue, set dead letter exchange and routing key
		_, err := dm.channel.QueueDeclare(
			queueName,
			true,
			false,
			false,
			false,
			amqp.Table{
				"x-dead-letter-exchange":    targetExchange,
				"x-dead-letter-routing-key": dm.targetTopic,
				"x-message-ttl":             ttl,
			},
		)
		if err != nil {
			return fmt.Errorf("failed to declare delay queue: %w", err)
		}

		// Bind delay queue to delay exchange
		if err := dm.channel.QueueBind(
			queueName,
			delayTopic,
			delayExchange,
			false,
			nil,
		); err != nil {
			return fmt.Errorf("failed to bind delay queue: %w", err)
		}

		// Start consumption goroutine
		dm.wg.Add(1)
		go dm.consumeDelayQueue(delayTopic, queueName)
	}

	return nil
}

// Stop stops the delay manager
func (dm *RabbitMQDelayManager) Stop() error {
	dm.cancel()
	dm.timerWheel.Stop()
	dm.wg.Wait()
	return nil
}

// EnqueueDelay enqueues a delayed task
func (dm *RabbitMQDelayManager) EnqueueDelay(
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

	// Use RabbitMQ delay queue
	delayMsg := &RabbitMQDelayMessage{
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

	delayExchange := fmt.Sprintf("%s%s", dm.exchange, rabbitmqDelayExchangeSuffix)

	// Calculate TTL (in milliseconds)
	ttl := int64(delay.Milliseconds())

	err = dm.channel.PublishWithContext(
		context.Background(),
		delayExchange,
		delayTopic,
		false,
		false,
		amqp.Publishing{
			ContentType:  "application/json",
			Body:         msgData,
			MessageId:    delayMsg.TaskID,
			DeliveryMode: amqp.Persistent,
			Timestamp:    now,
			Expiration:   fmt.Sprintf("%d", ttl),
			Headers: amqp.Table{
				"execute_at":   executeAt.Format(time.RFC3339),
				"target_topic": dm.targetTopic,
				"target_queue": targetQueue,
				"priority":     fmt.Sprintf("%d", priority),
			},
		},
	)

	if err != nil {
		return fmt.Errorf("failed to publish delay message: %w", err)
	}

	return nil
}

// consumeDelayQueue consumes messages from delay queue
func (dm *RabbitMQDelayManager) consumeDelayQueue(_ string, queueName string) {
	defer dm.wg.Done()

	msgs, err := dm.channel.Consume(
		queueName,
		"",
		false,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		return
	}

	for {
		select {
		case <-dm.ctx.Done():
			return
		case msg, ok := <-msgs:
			if !ok {
				return
			}

			// Decode delay message
			var delayMsg RabbitMQDelayMessage
			if err := dm.codec.Decode(msg.Body, &delayMsg); err != nil {
				_ = msg.Nack(false, false)
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
					_ = dm.EnqueueDelay(delayMsg.Task, delayMsg.ExecuteAt, delayMsg.TargetQueue, delayMsg.Priority)
				}
				_ = msg.Ack(false)
			} else {
				// Execution time has arrived, send to target topic (automatically forwarded via dead letter queue)
				_ = msg.Ack(false)
			}
		}
	}
}

// selectDelayTopic selects a delay topic based on execution time
func (dm *RabbitMQDelayManager) selectDelayTopic(executeAt time.Time) int {
	now := time.Now()
	delay := executeAt.Sub(now)

	slotIndex := int(delay / dm.slotDuration)
	if slotIndex >= dm.slotCount {
		slotIndex = dm.slotCount - 1
	}

	return slotIndex
}

// sendToTargetTopic sends a task to the target topic
func (dm *RabbitMQDelayManager) sendToTargetTopic(task *Task, queue string, priority Priority) error {
	taskMsg := &RabbitMQTaskMessage{
		TaskID:   generateTaskID(),
		Task:     task,
		Queue:    queue,
		Priority: priority,
	}

	msgData, err := dm.codec.Encode(taskMsg)
	if err != nil {
		return fmt.Errorf("failed to encode task message: %w", err)
	}

	targetExchange := fmt.Sprintf("%s%s", dm.exchange, rabbitmqTargetExchangeSuffix)

	err = dm.channel.PublishWithContext(
		context.Background(),
		targetExchange,
		dm.targetTopic,
		false,
		false,
		amqp.Publishing{
			ContentType:  "application/json",
			Body:         msgData,
			MessageId:    taskMsg.TaskID,
			DeliveryMode: amqp.Persistent,
			Timestamp:    time.Now(),
			Headers: amqp.Table{
				"queue":     queue,
				"priority":  fmt.Sprintf("%d", priority),
				"task_type": task.Type,
			},
		},
	)

	if err != nil {
		return fmt.Errorf("failed to publish task message: %w", err)
	}

	return nil
}

// SetCodec sets the message codec
func (dm *RabbitMQDelayManager) SetCodec(codec MessageCodec) {
	dm.codec = codec
}
