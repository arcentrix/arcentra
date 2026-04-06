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

package nova

import "time"

// KafkaOption is the interface for Kafka-specific configuration options
type KafkaOption interface {
	apply(*KafkaConfig)
}

type kafkaOptionFunc func(*KafkaConfig)

func (f kafkaOptionFunc) apply(c *KafkaConfig) {
	f(c)
}

// WithKafkaClientProgramName sets the Kafka client program name
func WithKafkaClientProgramName(programName string) KafkaOption {
	return kafkaOptionFunc(func(c *KafkaConfig) {
		c.ClientProgram = programName
	})
}

// WithKafkaGroupID sets the Kafka consumer group ID
func WithKafkaGroupID(groupID string) KafkaOption {
	return kafkaOptionFunc(func(c *KafkaConfig) {
		c.GroupID = groupID
	})
}

// WithKafkaTopicPrefix sets the Kafka topic prefix
func WithKafkaTopicPrefix(prefix string) KafkaOption {
	return kafkaOptionFunc(func(c *KafkaConfig) {
		c.TopicPrefix = prefix
	})
}

// WithKafkaAutoCommit sets the Kafka auto-commit setting
func WithKafkaAutoCommit(autoCommit bool) KafkaOption {
	return kafkaOptionFunc(func(c *KafkaConfig) {
		c.AutoCommit = autoCommit
	})
}

// WithKafkaSessionTimeout sets the Kafka session timeout in milliseconds
func WithKafkaSessionTimeout(timeout int) KafkaOption {
	return kafkaOptionFunc(func(c *KafkaConfig) {
		c.SessionTimeout = timeout
	})
}

// WithKafkaMaxPollInterval sets the Kafka maximum poll interval in milliseconds
func WithKafkaMaxPollInterval(interval int) KafkaOption {
	return kafkaOptionFunc(func(c *KafkaConfig) {
		c.MaxPollInterval = interval
	})
}

// WithKafkaDelaySlots sets the Kafka delay slot configuration
func WithKafkaDelaySlots(count int, duration time.Duration) KafkaOption {
	return kafkaOptionFunc(func(c *KafkaConfig) {
		c.DelaySlotCount = count
		c.DelaySlotDuration = duration
	})
}

// WithKafkaAuth sets the Kafka authentication configuration
func WithKafkaAuth(securityProtocol, saslMechanism, username, password string) KafkaOption {
	return kafkaOptionFunc(func(c *KafkaConfig) {
		c.SecurityProtocol = securityProtocol
		c.SASLMechanism = saslMechanism
		c.SASLUsername = username
		c.SASLPassword = password
	})
}

// WithKafkaSSL sets the Kafka SSL configuration
func WithKafkaSSL(caFile, certFile, keyFile, password string) KafkaOption {
	return kafkaOptionFunc(func(c *KafkaConfig) {
		c.SSLCAFile = caFile
		c.SSLCertFile = certFile
		c.SSLKeyFile = keyFile
		c.SSLPassword = password
	})
}
