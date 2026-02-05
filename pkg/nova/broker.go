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
)

// QueueType represents the message queue type
type QueueType string

const (
	QueueTypeKafka    QueueType = "kafka"
	QueueTypeRocketMQ QueueType = "rocketmq"
	QueueTypeRabbitMQ QueueType = "rabbitmq"
)

// MessageQueueBroker is the interface for message queue brokers
// All message queue implementations must implement this interface
type MessageQueueBroker interface {
	// SendMessage sends a single message
	SendMessage(ctx context.Context, topic string, key string, value []byte, headers map[string]string) error

	// SendBatchMessages sends multiple messages in batch
	SendBatchMessages(ctx context.Context, topic string, messages []Message) error

	// Subscribe subscribes to topics and consumes messages
	Subscribe(ctx context.Context, topics []string, handler MessageHandler) error

	// Close closes the connection
	Close() error
}

// Message represents a message structure
type Message struct {
	Key     string
	Value   []byte
	Headers map[string]string
}

// MessageHandler is the function type for message handlers
type MessageHandler func(ctx context.Context, msg *Message) error

// BrokerConfig is the interface for broker configuration
type BrokerConfig interface {
	GetType() QueueType
	GetBootstrapServers() string
	GetGroupID() string
	GetTopicPrefix() string
}
