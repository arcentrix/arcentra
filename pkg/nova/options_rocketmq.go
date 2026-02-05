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

	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
)

// RocketMQOption is the interface for RocketMQ-specific configuration options
type RocketMQOption interface {
	apply(*RocketMQConfig)
}

type rocketmqOptionFunc func(*RocketMQConfig)

func (f rocketmqOptionFunc) apply(c *RocketMQConfig) {
	f(c)
}

// WithRocketMQGroupID sets the RocketMQ consumer group ID
func WithRocketMQGroupID(groupID string) RocketMQOption {
	return rocketmqOptionFunc(func(c *RocketMQConfig) {
		c.GroupID = groupID
	})
}

// WithRocketMQTopicPrefix sets the RocketMQ topic prefix
func WithRocketMQTopicPrefix(prefix string) RocketMQOption {
	return rocketmqOptionFunc(func(c *RocketMQConfig) {
		c.TopicPrefix = prefix
	})
}

// WithRocketMQConsumerModel sets the RocketMQ consumer model
func WithRocketMQConsumerModel(model consumer.MessageModel) RocketMQOption {
	return rocketmqOptionFunc(func(c *RocketMQConfig) {
		c.ConsumerModel = model
	})
}

// WithRocketMQConsumeTimeout sets the RocketMQ consume timeout
func WithRocketMQConsumeTimeout(timeout time.Duration) RocketMQOption {
	return rocketmqOptionFunc(func(c *RocketMQConfig) {
		c.ConsumeTimeout = timeout
	})
}

// WithRocketMQMaxReconsumeTimes sets the RocketMQ maximum retry times
func WithRocketMQMaxReconsumeTimes(times int32) RocketMQOption {
	return rocketmqOptionFunc(func(c *RocketMQConfig) {
		c.MaxReconsumeTimes = times
	})
}

// WithRocketMQAuth sets the RocketMQ ACL authentication configuration
func WithRocketMQAuth(accessKey, secretKey string) RocketMQOption {
	return rocketmqOptionFunc(func(c *RocketMQConfig) {
		c.AccessKey = accessKey
		c.SecretKey = secretKey
	})
}

// WithRocketMQCredentials sets the RocketMQ credentials (takes precedence over AccessKey/SecretKey)
func WithRocketMQCredentials(credentials *primitive.Credentials) RocketMQOption {
	return rocketmqOptionFunc(func(c *RocketMQConfig) {
		c.Credentials = credentials
	})
}

// WithRocketMQDelaySlots sets the RocketMQ delay slot configuration
func WithRocketMQDelaySlots(count int, duration time.Duration) RocketMQOption {
	return rocketmqOptionFunc(func(c *RocketMQConfig) {
		c.DelaySlotCount = count
		c.DelaySlotDuration = duration
	})
}
