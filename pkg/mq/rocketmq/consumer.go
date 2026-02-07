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

package rocketmq

import (
	"context"
	"fmt"
	"time"

	"github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"github.com/arcentrix/arcentra/pkg/mq"
)

// ConsumerConfig represents RocketMQ consumer configuration.
type ConsumerConfig struct {
	ClientConfig      `json:",inline" mapstructure:",squash"`
	GroupId           string                `json:"groupId" mapstructure:"groupId"`
	ConsumerModel     consumer.MessageModel `json:"consumerModel" mapstructure:"consumerModel"`
	ConsumeTimeout    time.Duration         `json:"consumeTimeout" mapstructure:"consumeTimeout"`
	MaxReconsumeTimes int32                 `json:"maxReconsumeTimes" mapstructure:"maxReconsumeTimes"`
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
			opt.apply(&cfg.ClientConfig)
		}
	})
}

func WithConsumerModel(model consumer.MessageModel) ConsumerOption {
	return consumerOptionFunc(func(cfg *ConsumerConfig) {
		cfg.ConsumerModel = model
	})
}

func WithConsumerConsumeTimeout(timeout time.Duration) ConsumerOption {
	return consumerOptionFunc(func(cfg *ConsumerConfig) {
		cfg.ConsumeTimeout = timeout
	})
}

func WithConsumerMaxReconsumeTimes(times int32) ConsumerOption {
	return consumerOptionFunc(func(cfg *ConsumerConfig) {
		cfg.MaxReconsumeTimes = times
	})
}

// Message represents a RocketMQ message.
type Message struct {
	Key     string
	Value   []byte
	Headers map[string]string
}

// MessageHandler handles a RocketMQ message.
type MessageHandler func(context.Context, *Message) error

// Consumer wraps a RocketMQ push consumer instance.
type Consumer struct {
	consumer rocketmq.PushConsumer
}

// NewConsumer creates a new RocketMQ consumer.
func NewConsumer(nameServers []string, groupId string, opts ...ConsumerOption) (*Consumer, error) {
	cfg := ConsumerConfig{
		ClientConfig: ClientConfig{
			NameServers: nameServers,
		},
		GroupId:           groupId,
		ConsumerModel:     consumer.Clustering,
		ConsumeTimeout:    5 * time.Minute,
		MaxReconsumeTimes: 3,
	}
	for _, opt := range opts {
		opt.apply(&cfg)
	}
	if err := mq.RequireNonEmptySlice("nameServers", cfg.NameServers); err != nil {
		return nil, err
	}
	if err := mq.RequireNonEmpty("groupId", cfg.GroupId); err != nil {
		return nil, err
	}

	credentials, err := resolveCredentials(cfg.ClientConfig)
	if err != nil {
		return nil, err
	}

	consumerOpts := []consumer.Option{
		consumer.WithGroupName(cfg.GroupId),
		consumer.WithNsResolver(primitive.NewPassthroughResolver(cfg.NameServers)),
		consumer.WithConsumerModel(cfg.ConsumerModel),
		consumer.WithConsumeTimeout(cfg.ConsumeTimeout),
		consumer.WithMaxReconsumeTimes(cfg.MaxReconsumeTimes),
	}
	if credentials != nil {
		consumerOpts = append(consumerOpts, consumer.WithCredentials(*credentials))
	}

	c, err := rocketmq.NewPushConsumer(consumerOpts...)
	if err != nil {
		return nil, fmt.Errorf("create consumer: %w", err)
	}

	return &Consumer{consumer: c}, nil
}

// Subscribe subscribes to topics and consumes messages.
func (c *Consumer) Subscribe(ctx context.Context, topics []string, handler MessageHandler) error {
	if c == nil || c.consumer == nil {
		return fmt.Errorf("consumer is not initialized")
	}
	if err := mq.RequireNonEmptySlice("topics", topics); err != nil {
		return err
	}
	if handler == nil {
		return fmt.Errorf("handler is required")
	}

	for _, topic := range topics {
		if err := c.consumer.Subscribe(topic, consumer.MessageSelector{}, func(ctx context.Context, msgs ...*primitive.MessageExt) (consumer.ConsumeResult, error) {
			for _, msg := range msgs {
				headers := make(map[string]string, len(msg.GetProperties()))
				for k, v := range msg.GetProperties() {
					headers[k] = v
				}

				message := &Message{
					Key:     msg.GetKeys(),
					Value:   msg.Body,
					Headers: headers,
				}
				if err := handler(ctx, message); err != nil {
					return consumer.ConsumeRetryLater, err
				}
			}
			return consumer.ConsumeSuccess, nil
		}); err != nil {
			return fmt.Errorf("subscribe topic %s: %w", topic, err)
		}
	}

	if err := c.consumer.Start(); err != nil {
		return fmt.Errorf("start consumer: %w", err)
	}

	<-ctx.Done()
	return nil
}

// Close closes the consumer.
func (c *Consumer) Close() error {
	if c == nil || c.consumer == nil {
		return nil
	}
	return c.consumer.Shutdown()
}

// Raw returns the underlying RocketMQ consumer.
func (c *Consumer) Raw() rocketmq.PushConsumer {
	if c == nil {
		return nil
	}
	return c.consumer
}
