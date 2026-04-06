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

import (
	"testing"
	"time"
)

func TestWithKafkaGroupID(t *testing.T) {
	groupID := "test-group-id"
	opt := WithKafkaGroupID(groupID)
	config := &KafkaConfig{}
	opt.apply(config)

	if config.GroupID != groupID {
		t.Errorf("expected GroupID to be '%s', got %s", groupID, config.GroupID)
	}
}

func TestWithKafkaTopicPrefix(t *testing.T) {
	prefix := "test-prefix"
	opt := WithKafkaTopicPrefix(prefix)
	config := &KafkaConfig{}
	opt.apply(config)

	if config.TopicPrefix != prefix {
		t.Errorf("expected TopicPrefix to be '%s', got %s", prefix, config.TopicPrefix)
	}
}

func TestWithKafkaAutoCommit(t *testing.T) {
	tests := []struct {
		name       string
		autoCommit bool
		expected   bool
	}{
		{
			name:       "enable auto commit",
			autoCommit: true,
			expected:   true,
		},
		{
			name:       "disable auto commit",
			autoCommit: false,
			expected:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opt := WithKafkaAutoCommit(tt.autoCommit)
			config := &KafkaConfig{}
			opt.apply(config)

			if config.AutoCommit != tt.expected {
				t.Errorf("expected AutoCommit to be %v, got %v", tt.expected, config.AutoCommit)
			}
		})
	}
}

func TestWithKafkaSessionTimeout(t *testing.T) {
	timeout := 60000
	opt := WithKafkaSessionTimeout(timeout)
	config := &KafkaConfig{}
	opt.apply(config)

	if config.SessionTimeout != timeout {
		t.Errorf("expected SessionTimeout to be %d, got %d", timeout, config.SessionTimeout)
	}
}

func TestWithKafkaMaxPollInterval(t *testing.T) {
	interval := 600000
	opt := WithKafkaMaxPollInterval(interval)
	config := &KafkaConfig{}
	opt.apply(config)

	if config.MaxPollInterval != interval {
		t.Errorf("expected MaxPollInterval to be %d, got %d", interval, config.MaxPollInterval)
	}
}

func TestWithKafkaDelaySlots(t *testing.T) {
	count := 48
	duration := 30 * time.Minute
	opt := WithKafkaDelaySlots(count, duration)
	config := &KafkaConfig{}
	opt.apply(config)

	if config.DelaySlotCount != count {
		t.Errorf("expected DelaySlotCount to be %d, got %d", count, config.DelaySlotCount)
	}
	if config.DelaySlotDuration != duration {
		t.Errorf("expected DelaySlotDuration to be %v, got %v", duration, config.DelaySlotDuration)
	}
}

func TestWithKafkaAuth(t *testing.T) {
	securityProtocol := "SASL_SSL"
	saslMechanism := "PLAIN"
	username := "test-user"
	password := "test-password"

	opt := WithKafkaAuth(securityProtocol, saslMechanism, username, password)
	config := &KafkaConfig{}
	opt.apply(config)

	if config.SecurityProtocol != securityProtocol {
		t.Errorf("expected SecurityProtocol to be '%s', got %s", securityProtocol, config.SecurityProtocol)
	}
	if config.SASLMechanism != saslMechanism {
		t.Errorf("expected SASLMechanism to be '%s', got %s", saslMechanism, config.SASLMechanism)
	}
	if config.SASLUsername != username {
		t.Errorf("expected SASLUsername to be '%s', got %s", username, config.SASLUsername)
	}
	if config.SASLPassword != password {
		t.Errorf("expected SASLPassword to be '%s', got %s", password, config.SASLPassword)
	}
}

func TestWithKafkaSSL(t *testing.T) {
	caFile := "/path/to/ca.crt"
	certFile := "/path/to/cert.crt"
	keyFile := "/path/to/key.key"
	password := "ssl-password"

	opt := WithKafkaSSL(caFile, certFile, keyFile, password)
	config := &KafkaConfig{}
	opt.apply(config)

	if config.SSLCAFile != caFile {
		t.Errorf("expected SSLCAFile to be '%s', got %s", caFile, config.SSLCAFile)
	}
	if config.SSLCertFile != certFile {
		t.Errorf("expected SSLCertFile to be '%s', got %s", certFile, config.SSLCertFile)
	}
	if config.SSLKeyFile != keyFile {
		t.Errorf("expected SSLKeyFile to be '%s', got %s", keyFile, config.SSLKeyFile)
	}
	if config.SSLPassword != password {
		t.Errorf("expected SSLPassword to be '%s', got %s", password, config.SSLPassword)
	}
}

func TestNewKafkaConfig(t *testing.T) {
	bootstrapServers := "localhost:9092"
	config := NewKafkaConfig(bootstrapServers)

	if config.BootstrapServers != bootstrapServers {
		t.Errorf("expected BootstrapServers to be '%s', got %s", bootstrapServers, config.BootstrapServers)
	}
	if config.DelaySlotCount != DefaultDelaySlotCount {
		t.Errorf("expected DelaySlotCount to be %d, got %d", DefaultDelaySlotCount, config.DelaySlotCount)
	}
	if config.DelaySlotDuration != DefaultDelaySlotDuration {
		t.Errorf("expected DelaySlotDuration to be %v, got %v", DefaultDelaySlotDuration, config.DelaySlotDuration)
	}
	if config.AutoCommit != DefaultAutoCommit {
		t.Errorf("expected AutoCommit to be %v, got %v", DefaultAutoCommit, config.AutoCommit)
	}
	if config.SessionTimeout != DefaultSessionTimeout {
		t.Errorf("expected SessionTimeout to be %d, got %d", DefaultSessionTimeout, config.SessionTimeout)
	}
	if config.MaxPollInterval != DefaultMaxPollInterval {
		t.Errorf("expected MaxPollInterval to be %d, got %d", DefaultMaxPollInterval, config.MaxPollInterval)
	}
}
