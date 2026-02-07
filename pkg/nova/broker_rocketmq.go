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

	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	mqrocket "github.com/arcentrix/arcentra/pkg/mq/rocketmq"
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
	producer *mqrocket.Producer
	consumer *mqrocket.Consumer
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

	clientOpts := []mqrocket.ClientOption{
		mqrocket.WithAccessKey(rocketmqConfig.AccessKey),
		mqrocket.WithSecretKey(rocketmqConfig.SecretKey),
		mqrocket.WithCredentials(rocketmqConfig.Credentials),
	}

	producerOpts := []mqrocket.ProducerOption{
		mqrocket.WithProducerClientOptions(clientOpts...),
		mqrocket.WithProducerGroupName(fmt.Sprintf("%s-producer", rocketmqConfig.GroupID)),
		mqrocket.WithProducerRetry(3),
	}

	p, err := mqrocket.NewProducer(rocketmqConfig.NameServer, producerOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create producer: %w", err)
	}

	consumerOpts := []mqrocket.ConsumerOption{
		mqrocket.WithConsumerClientOptions(clientOpts...),
		mqrocket.WithConsumerModel(rocketmqConfig.ConsumerModel),
		mqrocket.WithConsumerConsumeTimeout(rocketmqConfig.ConsumeTimeout),
		mqrocket.WithConsumerMaxReconsumeTimes(rocketmqConfig.MaxReconsumeTimes),
	}

	c, err := mqrocket.NewConsumer(rocketmqConfig.NameServer, rocketmqConfig.GroupID, consumerOpts...)
	if err != nil {
		_ = p.Close()
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
	if err := b.producer.Send(ctx, topic, key, value, headers); err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	return nil
}

// SendBatchMessages sends multiple messages in batch
func (b *rocketmqBroker) SendBatchMessages(ctx context.Context, topic string, messages []Message) error {
	if len(messages) == 0 {
		return nil
	}

	for _, msg := range messages {
		if err := b.producer.Send(ctx, topic, msg.Key, msg.Value, msg.Headers); err != nil {
			return fmt.Errorf("failed to send batch messages: %w", err)
		}
	}

	return nil
}

// Subscribe subscribes to topics and consumes messages
func (b *rocketmqBroker) Subscribe(ctx context.Context, topics []string, handler MessageHandler) error {
	return b.consumer.Subscribe(ctx, topics, func(ctx context.Context, msg *mqrocket.Message) error {
		if msg == nil {
			return nil
		}
		message := &Message{
			Key:     msg.Key,
			Value:   msg.Value,
			Headers: msg.Headers,
		}
		return handler(ctx, message)
	})
}

// Close closes the connection
func (b *rocketmqBroker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	var errs []error

	if b.consumer != nil {
		if err := b.consumer.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close consumer: %w", err))
		}
	}

	if b.producer != nil {
		if err := b.producer.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close producer: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing rocketmq broker: %v", errs)
	}

	return nil
}
