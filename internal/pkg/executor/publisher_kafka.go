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

package executor

import (
	"context"
	"fmt"

	"github.com/arcentrix/arcentra/pkg/mq/kafka"
	"github.com/bytedance/sonic"
)

// KafkaPublisher publishes CloudEvents to Kafka.
type KafkaPublisher struct {
	producer *kafka.Producer
	topic    string
}

// NewKafkaPublisher creates a new KafkaPublisher.
func NewKafkaPublisher(bootstrapServers string, topic string, opts ...kafka.ProducerOption) (*KafkaPublisher, error) {
	if topic == "" {
		return nil, fmt.Errorf("kafka topic is required")
	}
	producer, err := kafka.NewProducer(bootstrapServers, opts...)
	if err != nil {
		return nil, err
	}
	return &KafkaPublisher{
		producer: producer,
		topic:    topic,
	}, nil
}

// Publish sends event to Kafka.
func (p *KafkaPublisher) Publish(ctx context.Context, event map[string]any) error {
	if p == nil || p.producer == nil {
		return fmt.Errorf("kafka publisher is not initialized")
	}
	payload, err := sonic.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	eventId := ""
	if value, ok := event["id"].(string); ok {
		eventId = value
	}
	return p.producer.Send(ctx, p.topic, eventId, payload, nil)
}

// Close closes the publisher.
func (p *KafkaPublisher) Close() error {
	if p == nil || p.producer == nil {
		return nil
	}
	p.producer.Close()
	return nil
}
