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

package kafka

import (
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/pkg/mq"
	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
)

// ConsumerConfig represents Kafka consumer configuration.
type ConsumerConfig struct {
	Config `json:",inline" mapstructure:",squash"`

	GroupID           string `json:"groupId" mapstructure:"groupId"`
	AutoOffsetReset   string `json:"autoOffsetReset" mapstructure:"autoOffsetReset"`
	EnableAutoCommit  *bool  `json:"enableAutoCommit" mapstructure:"enableAutoCommit"`
	SessionTimeoutMs  int    `json:"sessionTimeoutMs" mapstructure:"sessionTimeoutMs"`
	MaxPollIntervalMs int    `json:"maxPollIntervalMs" mapstructure:"maxPollIntervalMs"`
}

// ConsumerOption defines optional configuration for ConsumerConfig.
type ConsumerOption func(*ConsumerConfig)

func WithConsumerOptions(opts ...Option) ConsumerOption {
	return func(conf *ConsumerConfig) {
		for _, opt := range opts {
			opt(&conf.Config)
		}
	}
}

func WithConsumerGroupID(groupID string) ConsumerOption {
	return func(conf *ConsumerConfig) {
		conf.GroupID = groupID
	}
}

func WithConsumerAutoOffsetReset(reset string) ConsumerOption {
	return func(conf *ConsumerConfig) {
		conf.AutoOffsetReset = reset
	}
}

func WithConsumerEnableAutoCommit(enable bool) ConsumerOption {
	return func(conf *ConsumerConfig) {
		conf.EnableAutoCommit = &enable
	}
}

func WithConsumerSessionTimeoutMs(timeoutMs int) ConsumerOption {
	return func(conf *ConsumerConfig) {
		conf.SessionTimeoutMs = timeoutMs
	}
}

func WithConsumerMaxPollIntervalMs(intervalMs int) ConsumerOption {
	return func(conf *ConsumerConfig) {
		conf.MaxPollIntervalMs = intervalMs
	}
}

// Consumer wraps a Kafka consumer instance.
type Consumer struct {
	consumer *kafka.Consumer
}

// NewConsumer creates a new Kafka consumer.
func NewConsumer(bootstrapServers string, topicName string, clientID string, opts ...ConsumerOption) (*Consumer, error) {
	if err := mq.RequireNonEmpty("topicName", topicName); err != nil {
		return nil, err
	}
	if err := mq.RequireNonEmpty("clientId", clientID); err != nil {
		return nil, err
	}
	conf := ConsumerConfig{
		Config: Config{
			BootstrapServers: bootstrapServers,
		},
		AutoOffsetReset:   "earliest",
		SessionTimeoutMs:  10000,
		MaxPollIntervalMs: 300000,
	}
	for _, opt := range opts {
		opt(&conf)
	}

	enableAutoCommit := true
	if conf.EnableAutoCommit != nil {
		enableAutoCommit = *conf.EnableAutoCommit
	}

	config, err := baseConfig(conf.Config)
	if err != nil {
		return nil, err
	}
	clientIDStr, err := baseClientID(clientID)
	if err != nil {
		return nil, err
	}
	if conf.GroupID == "" {
		return nil, fmt.Errorf("groupId is required, use WithConsumerGroupID to set it")
	}
	_ = config.SetKey("client.id", clientIDStr)
	_ = config.SetKey("group.id", conf.GroupID)
	_ = config.SetKey("auto.offset.reset", conf.AutoOffsetReset)
	_ = config.SetKey("enable.auto.commit", enableAutoCommit)
	_ = config.SetKey("session.timeout.ms", conf.SessionTimeoutMs)
	_ = config.SetKey("max.poll.interval.ms", conf.MaxPollIntervalMs)

	consumer, err := kafka.NewConsumer(config)
	if err != nil {
		return nil, fmt.Errorf("create consumer: %w", err)
	}

	return &Consumer{consumer: consumer}, nil
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
