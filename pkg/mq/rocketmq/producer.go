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

package rocketmq

import (
	"context"
	"fmt"

	"github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"github.com/apache/rocketmq-client-go/v2/producer"
	"github.com/arcentrix/arcentra/pkg/mq"
)

// ProducerConfig represents RocketMQ producer configuration.
type ProducerConfig struct {
	ClientConfig `json:",inline" mapstructure:",squash"`
	GroupName    string `json:"groupName" mapstructure:"groupName"`
	Retry        int    `json:"retry" mapstructure:"retry"`
}

// ProducerOption defines optional configuration for ProducerConfig.
type ProducerOption interface {
	apply(*ProducerConfig)
}

type producerOptionFunc func(*ProducerConfig)

func (fn producerOptionFunc) apply(cfg *ProducerConfig) {
	fn(cfg)
}

func WithProducerClientOptions(opts ...ClientOption) ProducerOption {
	return producerOptionFunc(func(cfg *ProducerConfig) {
		for _, opt := range opts {
			opt.apply(&cfg.ClientConfig)
		}
	})
}

func WithProducerGroupName(groupName string) ProducerOption {
	return producerOptionFunc(func(cfg *ProducerConfig) {
		cfg.GroupName = groupName
	})
}

func WithProducerRetry(retry int) ProducerOption {
	return producerOptionFunc(func(cfg *ProducerConfig) {
		cfg.Retry = retry
	})
}

// Producer wraps a RocketMQ producer instance.
type Producer struct {
	producer rocketmq.Producer
}

// NewProducer creates a new RocketMQ producer.
func NewProducer(nameServers []string, opts ...ProducerOption) (*Producer, error) {
	cfg := ProducerConfig{
		ClientConfig: ClientConfig{
			NameServers: nameServers,
		},
		GroupName: "default-producer",
		Retry:     3,
	}
	for _, opt := range opts {
		opt.apply(&cfg)
	}
	if err := mq.RequireNonEmptySlice("nameServers", cfg.NameServers); err != nil {
		return nil, err
	}
	if err := mq.RequireNonEmpty("groupName", cfg.GroupName); err != nil {
		return nil, err
	}

	credentials, err := resolveCredentials(cfg.ClientConfig)
	if err != nil {
		return nil, err
	}

	producerOpts := []producer.Option{
		producer.WithNsResolver(primitive.NewPassthroughResolver(cfg.NameServers)),
		producer.WithGroupName(cfg.GroupName),
		producer.WithRetry(cfg.Retry),
	}
	if credentials != nil {
		producerOpts = append(producerOpts, producer.WithCredentials(*credentials))
	}

	p, err := rocketmq.NewProducer(producerOpts...)
	if err != nil {
		return nil, fmt.Errorf("create producer: %w", err)
	}
	if err := p.Start(); err != nil {
		return nil, fmt.Errorf("start producer: %w", err)
	}

	return &Producer{producer: p}, nil
}

// Send publishes a message to RocketMQ.
func (p *Producer) Send(ctx context.Context, topic string, key string, value []byte, headers map[string]string) error {
	if p == nil || p.producer == nil {
		return fmt.Errorf("producer is not initialized")
	}
	if err := mq.RequireNonEmpty("topic", topic); err != nil {
		return err
	}

	msg := primitive.NewMessage(topic, value)
	if key != "" {
		msg.WithKeys([]string{key})
	}
	for k, v := range headers {
		msg.WithProperty(k, v)
	}

	result, err := p.producer.SendSync(ctx, msg)
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	if result.Status != primitive.SendOK {
		return fmt.Errorf("send message: status=%v", result.Status)
	}
	return nil
}

// Close closes the producer.
func (p *Producer) Close() error {
	if p == nil || p.producer == nil {
		return nil
	}
	return p.producer.Shutdown()
}

// Raw returns the underlying RocketMQ producer.
func (p *Producer) Raw() rocketmq.Producer {
	if p == nil {
		return nil
	}
	return p.producer
}
