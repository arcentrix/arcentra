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
	"time"
)

// QueueOption is the interface for queue configuration options
type QueueOption interface {
	apply(*queueConfig)
}

type queueConfig struct {
	Type              QueueType
	BootstrapServers  string
	GroupID           string
	TopicPrefix       string
	DelaySlotCount    int
	DelaySlotDuration time.Duration
	AutoCommit        bool
	SessionTimeout    int
	MaxPollInterval   int
	// Message format configuration
	messageFormat MessageFormat
	messageCodec  MessageCodec
	// Broker-specific configuration
	kafkaConfig    *KafkaConfig
	rocketmqConfig *RocketMQConfig
	// Task recorder (optional)
	taskRecorder TaskRecorder
}

type queueOptionFunc func(*queueConfig)

func (f queueOptionFunc) apply(c *queueConfig) {
	f(c)
}

// WithKafka configures a Kafka broker
func WithKafka(bootstrapServers string, opts ...KafkaOption) QueueOption {
	return queueOptionFunc(func(c *queueConfig) {
		c.Type = QueueTypeKafka
		c.BootstrapServers = bootstrapServers
		c.kafkaConfig = NewKafkaConfig(bootstrapServers, opts...)
	})
}

// WithRocketMQ configures a RocketMQ broker
func WithRocketMQ(nameServers []string, opts ...RocketMQOption) QueueOption {
	return queueOptionFunc(func(c *queueConfig) {
		c.Type = QueueTypeRocketMQ
		if len(nameServers) > 0 {
			c.BootstrapServers = nameServers[0]
		}
		c.rocketmqConfig = NewRocketMQConfig(nameServers, opts...)
	})
}

// WithGroupID sets the consumer group ID
func WithGroupID(groupID string) QueueOption {
	return queueOptionFunc(func(c *queueConfig) {
		c.GroupID = groupID
	})
}

// WithTopicPrefix sets the topic prefix
func WithTopicPrefix(prefix string) QueueOption {
	return queueOptionFunc(func(c *queueConfig) {
		c.TopicPrefix = prefix
	})
}

// WithDelaySlots sets the delay slot configuration
func WithDelaySlots(count int, duration time.Duration) QueueOption {
	return queueOptionFunc(func(c *queueConfig) {
		c.DelaySlotCount = count
		c.DelaySlotDuration = duration
	})
}

// WithTaskRecorder sets the task recorder
func WithTaskRecorder(recorder TaskRecorder) QueueOption {
	return queueOptionFunc(func(c *queueConfig) {
		c.taskRecorder = recorder
	})
}

// WithMessageFormat sets the message format
func WithMessageFormat(format MessageFormat) QueueOption {
	return queueOptionFunc(func(c *queueConfig) {
		c.messageFormat = format
		codec, err := NewMessageCodec(format)
		if err == nil {
			c.messageCodec = codec
		}
	})
}

// WithMessageCodec sets the message codec
func WithMessageCodec(codec MessageCodec) QueueOption {
	return queueOptionFunc(func(c *queueConfig) {
		c.messageCodec = codec
		if codec != nil {
			c.messageFormat = codec.Format()
		}
	})
}

// Broker-specific option functions are defined in options_kafka.go and options_rocketmq.go
