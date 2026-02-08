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

package kafka

import (
	"fmt"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/pkg/mq"
	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
)

// ConsumerConfig represents Kafka consumer configuration.
type ConsumerConfig struct {
	KafkaConfig `json:",inline" mapstructure:",squash"`

	AutoOffsetReset   string `json:"autoOffsetReset" mapstructure:"autoOffsetReset"`
	EnableAutoCommit  *bool  `json:"enableAutoCommit" mapstructure:"enableAutoCommit"`
	SessionTimeoutMs  int    `json:"sessionTimeoutMs" mapstructure:"sessionTimeoutMs"`
	MaxPollIntervalMs int    `json:"maxPollIntervalMs" mapstructure:"maxPollIntervalMs"`
}

// ConsumerOption defines optional configuration for ConsumerConfig.
type ConsumerOption interface {
	apply(*ConsumerConfig)
}

type consumerOptionFunc func(*ConsumerConfig)

func (fn consumerOptionFunc) apply(cfg *ConsumerConfig) {
	fn(cfg)
}

func WithConsumerClientOptions(opts ...ClientOption) ConsumerOption {
	return consumerOptionFunc(func(cfg *ConsumerConfig) {
		for _, opt := range opts {
			opt.apply(&cfg.KafkaConfig)
		}
	})
}

func WithConsumerAutoOffsetReset(reset string) ConsumerOption {
	return consumerOptionFunc(func(cfg *ConsumerConfig) {
		cfg.AutoOffsetReset = reset
	})
}

func WithConsumerEnableAutoCommit(enable bool) ConsumerOption {
	return consumerOptionFunc(func(cfg *ConsumerConfig) {
		cfg.EnableAutoCommit = &enable
	})
}

func WithConsumerSessionTimeoutMs(timeoutMs int) ConsumerOption {
	return consumerOptionFunc(func(cfg *ConsumerConfig) {
		cfg.SessionTimeoutMs = timeoutMs
	})
}

func WithConsumerMaxPollIntervalMs(intervalMs int) ConsumerOption {
	return consumerOptionFunc(func(cfg *ConsumerConfig) {
		cfg.MaxPollIntervalMs = intervalMs
	})
}

// Consumer wraps a Kafka consumer instance.
type Consumer struct {
	consumer *kafka.Consumer
}

// NewConsumer creates a new Kafka consumer.
func NewConsumer(bootstrapServers string, topicName string, programName string, opts ...ConsumerOption) (*Consumer, error) {
	if err := mq.RequireNonEmpty("topicName", topicName); err != nil {
		return nil, err
	}
	if err := mq.RequireNonEmpty("programName", programName); err != nil {
		return nil, err
	}
	cfg := ConsumerConfig{
		KafkaConfig: KafkaConfig{
			BootstrapServers: bootstrapServers,
		},
		AutoOffsetReset:   "earliest",
		SessionTimeoutMs:  10000,
		MaxPollIntervalMs: 300000,
	}
	for _, opt := range opts {
		opt.apply(&cfg)
	}
	normalizeConsumerConfig(&cfg)

	enableAutoCommit := true
	if cfg.EnableAutoCommit != nil {
		enableAutoCommit = *cfg.EnableAutoCommit
	}

	config, err := buildBaseConfig(cfg.KafkaConfig)
	if err != nil {
		return nil, err
	}
	clientID, err := buildClientID(programName)
	if err != nil {
		return nil, err
	}
	groupID := strings.ToUpper(fmt.Sprintf("%s_CONSUMER", strings.TrimSpace(topicName)))
	_ = config.SetKey("client.id", clientID)
	_ = config.SetKey("group.id", groupID)
	_ = config.SetKey("auto.offset.reset", cfg.AutoOffsetReset)
	_ = config.SetKey("enable.auto.commit", enableAutoCommit)
	_ = config.SetKey("session.timeout.ms", cfg.SessionTimeoutMs)
	_ = config.SetKey("max.poll.interval.ms", cfg.MaxPollIntervalMs)

	consumer, err := kafka.NewConsumer(config)
	if err != nil {
		return nil, fmt.Errorf("create consumer: %w", err)
	}

	return &Consumer{consumer: consumer}, nil
}

func normalizeConsumerConfig(cfg *ConsumerConfig) {
	if cfg.AutoOffsetReset == "" {
		cfg.AutoOffsetReset = "earliest"
	}
	if cfg.SessionTimeoutMs == 0 {
		cfg.SessionTimeoutMs = 10000
	}
	if cfg.MaxPollIntervalMs == 0 {
		cfg.MaxPollIntervalMs = 300000
	}
}

// Subscribe subscribes to topics for consumption.
func (c *Consumer) Subscribe(topics []string) error {
	if c == nil || c.consumer == nil {
		return fmt.Errorf("consumer is not initialized")
	}
	if len(topics) == 0 {
		return fmt.Errorf("topics is required")
	}
	return c.consumer.SubscribeTopics(topics, nil)
}

// ReadMessage reads a message with the given timeout.
func (c *Consumer) ReadMessage(timeout time.Duration) (*kafka.Message, error) {
	if c == nil || c.consumer == nil {
		return nil, fmt.Errorf("consumer is not initialized")
	}
	return c.consumer.ReadMessage(timeout)
}

// CommitMessage commits the provided message.
func (c *Consumer) CommitMessage(msg *kafka.Message) error {
	if c == nil || c.consumer == nil {
		return fmt.Errorf("consumer is not initialized")
	}
	if msg == nil {
		return fmt.Errorf("message is required")
	}
	_, err := c.consumer.CommitMessage(msg)
	return err
}

// Close closes the consumer.
func (c *Consumer) Close() error {
	if c == nil || c.consumer == nil {
		return nil
	}
	return c.consumer.Close()
}
