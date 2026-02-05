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
	"crypto/tls"
	"time"
)

// RabbitMQOption is the interface for RabbitMQ-specific configuration options
type RabbitMQOption interface {
	apply(*RabbitMQConfig)
}

type rabbitmqOptionFunc func(*RabbitMQConfig)

func (f rabbitmqOptionFunc) apply(c *RabbitMQConfig) {
	f(c)
}

// WithRabbitMQExchange sets the RabbitMQ exchange name
func WithRabbitMQExchange(exchange string) RabbitMQOption {
	return rabbitmqOptionFunc(func(c *RabbitMQConfig) {
		c.Exchange = exchange
	})
}

// WithRabbitMQTopicPrefix sets the RabbitMQ topic prefix
func WithRabbitMQTopicPrefix(prefix string) RabbitMQOption {
	return rabbitmqOptionFunc(func(c *RabbitMQConfig) {
		c.TopicPrefix = prefix
	})
}

// WithRabbitMQPrefetch sets the RabbitMQ prefetch configuration
func WithRabbitMQPrefetch(count, size int) RabbitMQOption {
	return rabbitmqOptionFunc(func(c *RabbitMQConfig) {
		c.PrefetchCount = count
		c.PrefetchSize = size
	})
}

// WithRabbitMQAuth sets the RabbitMQ authentication configuration
func WithRabbitMQAuth(username, password string) RabbitMQOption {
	return rabbitmqOptionFunc(func(c *RabbitMQConfig) {
		c.Username = username
		c.Password = password
	})
}

// WithRabbitMQTLS sets the RabbitMQ TLS configuration
func WithRabbitMQTLS(tlsConfig *tls.Config) RabbitMQOption {
	return rabbitmqOptionFunc(func(c *RabbitMQConfig) {
		c.TLSConfig = tlsConfig
	})
}

// WithRabbitMQDelaySlots sets the RabbitMQ delay slot configuration
func WithRabbitMQDelaySlots(count int, duration time.Duration) RabbitMQOption {
	return rabbitmqOptionFunc(func(c *RabbitMQConfig) {
		c.DelaySlotCount = count
		c.DelaySlotDuration = duration
	})
}
