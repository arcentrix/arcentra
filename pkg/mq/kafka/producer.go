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
type ProducerOption func(*ProducerConfig)

func WithProducerOptions(opts ...Option) ProducerOption {
	return func(conf *ProducerConfig) {
		for _, opt := range opts {
			opt(&conf.Config)
		}
	}
}

func WithProducerAcks(acks string) ProducerOption {
	return func(conf *ProducerConfig) {
		conf.Acks = acks
	}
}

func WithProducerRetries(retries int) ProducerOption {
	return func(conf *ProducerConfig) {
		conf.Retries = retries
	}
}

func WithProducerCompression(compression string) ProducerOption {
	return func(conf *ProducerConfig) {
		conf.Compression = compression
	}
}

// Normalize sets default values for empty fields.
func (c *ProducerConfig) Normalize() {
	if c.Acks == "" {
		c.Acks = "all"
	}
	if c.Retries == 0 {
		c.Retries = 3
	}
	if c.Compression == "" {
		c.Compression = "snappy"
	}
}

// Producer wraps a Kafka producer instance.
type Producer struct {
	producer *kafka.Producer
}

// NewProducer creates a new Kafka producer.
func NewProducer(bootstrapServers string, clientID string, opts ...ProducerOption) (*Producer, error) {
	conf := ProducerConfig{
		Config: Config{
			BootstrapServers: bootstrapServers,
		},
		Acks:        "all",
		Retries:     3,
		Compression: "snappy",
	}
	for _, opt := range opts {
		opt(&conf)
	}

	config, err := baseConfig(conf.Config)
	if err != nil {
		return nil, err
	}
	clientIDStr, err := baseClientID(clientID)
	if err != nil {
		return nil, err
	}
	_ = config.SetKey("client.id", clientIDStr)
	_ = config.SetKey("acks", conf.Acks)
	_ = config.SetKey("retries", conf.Retries)
	_ = config.SetKey("compression.type", conf.Compression)

	producer, err := kafka.NewProducer(config)
	if err != nil {
		return nil, fmt.Errorf("create producer: %w", err)
	}

	return &Producer{producer: producer}, nil
}

// Producer publishes a message to Kafka.
func (p *Producer) Producer(ctx context.Context, topic string, key string, value []byte, headers map[string]string) error {
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
