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
	"context"
	"fmt"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
)

// ProducerConfig represents Kafka producer configuration.
type ProducerConfig struct {
	Config      `json:",inline" mapstructure:",squash"`
	Acks        string `json:"acks" mapstructure:"acks"`
	Retries     int    `json:"retries" mapstructure:"retries"`
	Compression string `json:"compression" mapstructure:"compression"`
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
			opt.apply(&cfg.Config)
		}
	})
}

func WithProducerAcks(acks string) ProducerOption {
	return producerOptionFunc(func(cfg *ProducerConfig) {
		cfg.Acks = acks
	})
}

func WithProducerRetries(retries int) ProducerOption {
	return producerOptionFunc(func(cfg *ProducerConfig) {
		cfg.Retries = retries
	})
}

func WithProducerCompression(compression string) ProducerOption {
	return producerOptionFunc(func(cfg *ProducerConfig) {
		cfg.Compression = compression
	})
}

// Producer wraps a Kafka producer instance.
type Producer struct {
	producer *kafka.Producer
}

// NewProducer creates a new Kafka producer.
func NewProducer(bootstrapServers string, clientId string, opts ...ProducerOption) (*Producer, error) {
	cfg := ProducerConfig{
		Config: Config{
			BootstrapServers: bootstrapServers,
		},
		Acks:        "all",
		Retries:     3,
		Compression: "snappy",
	}
	for _, opt := range opts {
		opt.apply(&cfg)
	}
	normalizeProducerConfig(&cfg)

	config, err := buildBaseConfig(cfg.Config)
	if err != nil {
		return nil, err
	}
	clientID, err := buildClientId(clientId)
	if err != nil {
		return nil, err
	}
	_ = config.SetKey("client.id", clientID)
	_ = config.SetKey("acks", cfg.Acks)
	_ = config.SetKey("retries", cfg.Retries)
	_ = config.SetKey("compression.type", cfg.Compression)

	producer, err := kafka.NewProducer(config)
	if err != nil {
		return nil, fmt.Errorf("create producer: %w", err)
	}

	return &Producer{producer: producer}, nil
}

func normalizeProducerConfig(cfg *ProducerConfig) {
	if cfg.Acks == "" {
		cfg.Acks = "all"
	}
	if cfg.Retries == 0 {
		cfg.Retries = 3
	}
	if cfg.Compression == "" {
		cfg.Compression = "snappy"
	}
}

// Send publishes a message to Kafka.
func (p *Producer) Send(ctx context.Context, topic string, key string, value []byte, headers map[string]string) error {
	if p == nil || p.producer == nil {
		return fmt.Errorf("producer is not initialized")
	}
	if topic == "" {
		return fmt.Errorf("topic is required")
	}

	kafkaHeaders := make([]kafka.Header, 0, len(headers))
	for k, v := range headers {
		kafkaHeaders = append(kafkaHeaders, kafka.Header{
			Key:   k,
			Value: []byte(v),
		})
	}

	message := &kafka.Message{
		TopicPartition: kafka.TopicPartition{
			Topic:     &topic,
			Partition: kafka.PartitionAny,
		},
		Key:     []byte(key),
		Value:   value,
		Headers: kafkaHeaders,
	}

	deliveryChan := make(chan kafka.Event, 1)
	if err := p.producer.Produce(message, deliveryChan); err != nil {
		return fmt.Errorf("produce message: %w", err)
	}

	select {
	case e := <-deliveryChan:
		m := e.(*kafka.Message)
		if m.TopicPartition.Error != nil {
			return fmt.Errorf("deliver message: %w", m.TopicPartition.Error)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close closes the producer.
func (p *Producer) Close() {
	if p == nil || p.producer == nil {
		return
	}
	p.producer.Flush(15 * 1000)
	p.producer.Close()
}
