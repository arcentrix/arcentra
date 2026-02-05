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
	"maps"
	"sync"
	"time"

	"github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"github.com/apache/rocketmq-client-go/v2/producer"
)

// RocketMQConfig represents RocketMQ configuration
type RocketMQConfig struct {
	NameServer        []string              // NameServer address list
	GroupID           string                // Consumer group ID
	TopicPrefix       string                // Topic prefix
	DelaySlotCount    int                   // Number of delay topic slots
	DelaySlotDuration time.Duration         // Time interval for each delay slot
	ConsumerModel     consumer.MessageModel // Consumer model
	ConsumeTimeout    time.Duration         // Consume timeout
	MaxReconsumeTimes int32                 // Maximum retry times
	// Authentication configuration
	AccessKey   string                 // ACL AccessKey
	SecretKey   string                 // ACL SecretKey
	Credentials *primitive.Credentials // RocketMQ credentials (takes precedence if provided)
}

// NewRocketMQConfig creates a RocketMQ configuration using the option pattern
func NewRocketMQConfig(nameServers []string, opts ...RocketMQOption) *RocketMQConfig {
	config := &RocketMQConfig{
		NameServer:        nameServers,
		DelaySlotCount:    DefaultDelaySlotCount,
		DelaySlotDuration: DefaultDelaySlotDuration,
		ConsumerModel:     consumer.Clustering,
		ConsumeTimeout:    5 * time.Minute,
		MaxReconsumeTimes: 3,
	}

	for _, opt := range opts {
		opt.apply(config)
	}

	return config
}

// rocketmqBroker is the RocketMQ broker implementation
type rocketmqBroker struct {
	producer rocketmq.Producer
	consumer rocketmq.PushConsumer
	config   *RocketMQConfig
	mu       sync.RWMutex
}

// newRocketMQBroker creates a RocketMQ broker
func newRocketMQBroker(config *queueConfig) (MessageQueueBroker, DelayManager, error) {
	rocketmqConfig := config.rocketmqConfig
	if rocketmqConfig == nil {
		rocketmqConfig = NewRocketMQConfig(
			[]string{config.BootstrapServers},
			WithRocketMQGroupID(config.GroupID),
			WithRocketMQTopicPrefix(config.TopicPrefix),
		)
		rocketmqConfig.DelaySlotCount = config.DelaySlotCount
		rocketmqConfig.DelaySlotDuration = config.DelaySlotDuration
	}

	// Prepare authentication credentials
	var credentials *primitive.Credentials
	var err error
	if rocketmqConfig.Credentials != nil {
		credentials = rocketmqConfig.Credentials
	} else if rocketmqConfig.AccessKey != "" && rocketmqConfig.SecretKey != "" {
		cred := primitive.Credentials{
			AccessKey: rocketmqConfig.AccessKey,
			SecretKey: rocketmqConfig.SecretKey,
		}
		credentials = &cred
	}

	// Create producer options
	producerOpts := []producer.Option{
		producer.WithNsResolver(primitive.NewPassthroughResolver(rocketmqConfig.NameServer)),
		producer.WithGroupName(fmt.Sprintf("%s-producer", rocketmqConfig.GroupID)),
		producer.WithRetry(3),
	}
	if credentials != nil {
		producerOpts = append(producerOpts, producer.WithCredentials(*credentials))
	}

	p, err := rocketmq.NewProducer(producerOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create producer: %w", err)
	}

	if err = p.Start(); err != nil {
		return nil, nil, fmt.Errorf("failed to start producer: %w", err)
	}

	// Create consumer options
	consumerOpts := []consumer.Option{
		consumer.WithGroupName(rocketmqConfig.GroupID),
		consumer.WithNsResolver(primitive.NewPassthroughResolver(rocketmqConfig.NameServer)),
		consumer.WithConsumerModel(rocketmqConfig.ConsumerModel),
		consumer.WithConsumeTimeout(rocketmqConfig.ConsumeTimeout),
		consumer.WithMaxReconsumeTimes(rocketmqConfig.MaxReconsumeTimes),
	}
	if credentials != nil {
		consumerOpts = append(consumerOpts, consumer.WithCredentials(*credentials))
	}

	c, err := rocketmq.NewPushConsumer(consumerOpts...)
	if err != nil {
		err = p.Shutdown()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("failed to create consumer: %w", err)
	}

	broker := &rocketmqBroker{
		producer: p,
		consumer: c,
		config:   rocketmqConfig,
	}

	// Create delay manager (using RocketMQ delay message feature)
	delayManager := NewRocketMQDelayManager(
		p,
		c,
		fmt.Sprintf("%s-tasks", rocketmqConfig.TopicPrefix),
		rocketmqConfig.DelaySlotCount,
		rocketmqConfig.DelaySlotDuration,
	)

	return broker, delayManager, nil
}

// SendMessage sends a single message
func (b *rocketmqBroker) SendMessage(ctx context.Context, topic string, key string, value []byte, headers map[string]string) error {
	msg := primitive.NewMessage(topic, value)
	msg.WithKeys([]string{key})

	// Set message properties
	for k, v := range headers {
		msg.WithProperty(k, v)
	}

	result, err := b.producer.SendSync(ctx, msg)
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	if result.Status != primitive.SendOK {
		return fmt.Errorf("failed to send message: status=%v", result.Status)
	}

	return nil
}

// SendBatchMessages sends multiple messages in batch
func (b *rocketmqBroker) SendBatchMessages(ctx context.Context, topic string, messages []Message) error {
	if len(messages) == 0 {
		return nil
	}

	msgs := make([]*primitive.Message, 0, len(messages))
	for _, msg := range messages {
		rmqMsg := primitive.NewMessage(topic, msg.Value)
		rmqMsg.WithKeys([]string{msg.Key})

		// Set message properties
		for k, v := range msg.Headers {
			rmqMsg.WithProperty(k, v)
		}

		msgs = append(msgs, rmqMsg)
	}

	result, err := b.producer.SendSync(ctx, msgs...)
	if err != nil {
		return fmt.Errorf("failed to send batch messages: %w", err)
	}

	if result.Status != primitive.SendOK {
		return fmt.Errorf("failed to send batch messages: status=%v", result.Status)
	}

	return nil
}

// Subscribe subscribes to topics and consumes messages
func (b *rocketmqBroker) Subscribe(ctx context.Context, topics []string, handler MessageHandler) error {
	// Register consumer for each topic
	for _, topic := range topics {
		if err := b.consumer.Subscribe(topic, consumer.MessageSelector{}, func(ctx context.Context, msgs ...*primitive.MessageExt) (consumer.ConsumeResult, error) {
			for _, msg := range msgs {
				// Convert message format
				headers := make(map[string]string)
				maps.Copy(headers, msg.GetProperties())

				message := &Message{
					Key:     msg.GetKeys(),
					Value:   msg.Body,
					Headers: headers,
				}

				// Process message
				if err := handler(ctx, message); err != nil {
					return consumer.ConsumeRetryLater, err
				}
			}
			return consumer.ConsumeSuccess, nil
		}); err != nil {
			return fmt.Errorf("failed to subscribe topic %s: %w", topic, err)
		}
	}

	// Start consumer
	if err := b.consumer.Start(); err != nil {
		return fmt.Errorf("failed to start consumer: %w", err)
	}

	// Wait for context cancellation
	<-ctx.Done()
	return nil
}

// Close closes the connection
func (b *rocketmqBroker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	var errs []error

	if b.consumer != nil {
		if err := b.consumer.Shutdown(); err != nil {
			errs = append(errs, fmt.Errorf("failed to shutdown consumer: %w", err))
		}
	}

	if b.producer != nil {
		if err := b.producer.Shutdown(); err != nil {
			errs = append(errs, fmt.Errorf("failed to shutdown producer: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing rocketmq broker: %v", errs)
	}

	return nil
}
