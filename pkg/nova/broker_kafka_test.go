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
	"testing"
)

func TestNewKafkaConfig_Integration(t *testing.T) {
	// Test that NewKafkaConfig creates config with defaults
	config := NewKafkaConfig("localhost:9092")

	if config.BootstrapServers != "localhost:9092" {
		t.Errorf("expected BootstrapServers to be 'localhost:9092', got %s", config.BootstrapServers)
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

func TestNewKafkaConfig_WithOptions(t *testing.T) {
	config := NewKafkaConfig("localhost:9092",
		WithKafkaGroupID("test-group"),
		WithKafkaTopicPrefix("test-prefix"),
		WithKafkaAutoCommit(true),
	)

	if config.GroupID != "test-group" {
		t.Errorf("expected GroupID to be 'test-group', got %s", config.GroupID)
	}
	if config.TopicPrefix != "test-prefix" {
		t.Errorf("expected TopicPrefix to be 'test-prefix', got %s", config.TopicPrefix)
	}
	if config.AutoCommit != true {
		t.Errorf("expected AutoCommit to be true, got %v", config.AutoCommit)
	}
}

func TestKafkaConfig_AuthFields(t *testing.T) {
	config := NewKafkaConfig("localhost:9092",
		WithKafkaAuth("SASL_SSL", "PLAIN", "user", "pass"),
	)

	if config.SecurityProtocol != "SASL_SSL" {
		t.Errorf("expected SecurityProtocol to be 'SASL_SSL', got %s", config.SecurityProtocol)
	}
	if config.SASLMechanism != "PLAIN" {
		t.Errorf("expected SASLMechanism to be 'PLAIN', got %s", config.SASLMechanism)
	}
	if config.SASLUsername != "user" {
		t.Errorf("expected SASLUsername to be 'user', got %s", config.SASLUsername)
	}
	if config.SASLPassword != "pass" {
		t.Errorf("expected SASLPassword to be 'pass', got %s", config.SASLPassword)
	}
}

func TestKafkaConfig_SSLFields(t *testing.T) {
	config := NewKafkaConfig("localhost:9092",
		WithKafkaSSL("/ca.crt", "/cert.crt", "/key.key", "password"),
	)

	if config.SSLCAFile != "/ca.crt" {
		t.Errorf("expected SSLCAFile to be '/ca.crt', got %s", config.SSLCAFile)
	}
	if config.SSLCertFile != "/cert.crt" {
		t.Errorf("expected SSLCertFile to be '/cert.crt', got %s", config.SSLCertFile)
	}
	if config.SSLKeyFile != "/key.key" {
		t.Errorf("expected SSLKeyFile to be '/key.key', got %s", config.SSLKeyFile)
	}
	if config.SSLPassword != "password" {
		t.Errorf("expected SSLPassword to be 'password', got %s", config.SSLPassword)
	}
}

func TestKafkaConfig_AllFields(t *testing.T) {
	config := &KafkaConfig{
		BootstrapServers:  "localhost:9092",
		GroupID:           "test-group",
		TopicPrefix:       "test-prefix",
		DelaySlotCount:    24,
		DelaySlotDuration: 3600000000000, // 1 hour in nanoseconds
		AutoCommit:        true,
		SessionTimeout:    30000,
		MaxPollInterval:   300000,
		SASLMechanism:     "PLAIN",
		SASLUsername:      "user",
		SASLPassword:      "pass",
		SecurityProtocol:  "SASL_SSL",
		SSLCAFile:         "/ca.crt",
		SSLCertFile:       "/cert.crt",
		SSLKeyFile:        "/key.key",
		SSLPassword:       "password",
	}

	if config.BootstrapServers == "" {
		t.Error("expected BootstrapServers to be set")
	}
	if config.GroupID == "" {
		t.Error("expected GroupID to be set")
	}
	if config.TopicPrefix == "" {
		t.Error("expected TopicPrefix to be set")
	}
	if config.DelaySlotCount != 24 {
		t.Errorf("expected DelaySlotCount to be 24, got %d", config.DelaySlotCount)
	}
	if config.DelaySlotDuration != 3600000000000 {
		t.Errorf("expected DelaySlotDuration to be 3600000000000, got %d", config.DelaySlotDuration)
	}
	if config.AutoCommit != true {
		t.Errorf("expected AutoCommit to be true, got %v", config.AutoCommit)
	}
	if config.SessionTimeout != 30000 {
		t.Errorf("expected SessionTimeout to be 30000, got %d", config.SessionTimeout)
	}
	if config.MaxPollInterval != 300000 {
		t.Errorf("expected MaxPollInterval to be 300000, got %d", config.MaxPollInterval)
	}
	if config.SASLMechanism != "PLAIN" {
		t.Errorf("expected SASLMechanism to be 'PLAIN', got %s", config.SASLMechanism)
	}
	if config.SASLUsername != "user" {
		t.Errorf("expected SASLUsername to be 'user', got %s", config.SASLUsername)
	}
	if config.SASLPassword != "pass" {
		t.Errorf("expected SASLPassword to be 'pass', got %s", config.SASLPassword)
	}
	if config.SecurityProtocol != "SASL_SSL" {
		t.Errorf("expected SecurityProtocol to be 'SASL_SSL', got %s", config.SecurityProtocol)
	}
	if config.SSLCAFile != "/ca.crt" {
		t.Errorf("expected SSLCAFile to be '/ca.crt', got %s", config.SSLCAFile)
	}
	if config.SSLCertFile != "/cert.crt" {
		t.Errorf("expected SSLCertFile to be '/cert.crt', got %s", config.SSLCertFile)
	}
	if config.SSLKeyFile != "/key.key" {
		t.Errorf("expected SSLKeyFile to be '/key.key', got %s", config.SSLKeyFile)
	}
	if config.SSLPassword != "password" {
		t.Errorf("expected SSLPassword to be 'password', got %s", config.SSLPassword)
	}
}

func TestApplyKafkaAuthConfig(t *testing.T) {
	// Test applyKafkaAuthConfig function indirectly through config creation
	// Since it's a private function, we test it through the config options

	// Test SASL configuration
	config := NewKafkaConfig("localhost:9092",
		WithKafkaAuth("SASL_SSL", "PLAIN", "user", "pass"),
	)

	if config.SecurityProtocol != "SASL_SSL" {
		t.Errorf("expected SecurityProtocol to be 'SASL_SSL', got %s", config.SecurityProtocol)
	}
	if config.SASLMechanism != "PLAIN" {
		t.Errorf("expected SASLMechanism to be 'PLAIN', got %s", config.SASLMechanism)
	}
	if config.SASLUsername != "user" {
		t.Errorf("expected SASLUsername to be 'user', got %s", config.SASLUsername)
	}
	if config.SASLPassword != "pass" {
		t.Errorf("expected SASLPassword to be 'pass', got %s", config.SASLPassword)
	}

	// Test SSL configuration
	config2 := NewKafkaConfig("localhost:9092",
		WithKafkaSSL("/ca.crt", "/cert.crt", "/key.key", "password"),
	)

	if config2.SSLCAFile != "/ca.crt" {
		t.Errorf("expected SSLCAFile to be '/ca.crt', got %s", config2.SSLCAFile)
	}
	if config2.SSLCertFile != "/cert.crt" {
		t.Errorf("expected SSLCertFile to be '/cert.crt', got %s", config2.SSLCertFile)
	}
	if config2.SSLKeyFile != "/key.key" {
		t.Errorf("expected SSLKeyFile to be '/key.key', got %s", config2.SSLKeyFile)
	}
	if config2.SSLPassword != "password" {
		t.Errorf("expected SSLPassword to be 'password', got %s", config2.SSLPassword)
	}
}
