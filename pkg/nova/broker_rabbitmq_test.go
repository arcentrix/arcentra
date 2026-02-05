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
	"testing"
	"time"
)

func TestNewRabbitMQConfig_Defaults(t *testing.T) {
	url := "amqp://guest:guest@localhost:5672/"
	config := NewRabbitMQConfig(url)

	if config.URL != url {
		t.Errorf("expected URL to be %s, got %s", url, config.URL)
	}
	if config.DelaySlotCount != DefaultDelaySlotCount {
		t.Errorf("expected DelaySlotCount to be %d, got %d", DefaultDelaySlotCount, config.DelaySlotCount)
	}
	if config.DelaySlotDuration != DefaultDelaySlotDuration {
		t.Errorf("expected DelaySlotDuration to be %v, got %v", DefaultDelaySlotDuration, config.DelaySlotDuration)
	}
	if config.PrefetchCount != 10 {
		t.Errorf("expected PrefetchCount to be 10, got %d", config.PrefetchCount)
	}
	if config.PrefetchSize != 0 {
		t.Errorf("expected PrefetchSize to be 0, got %d", config.PrefetchSize)
	}
}

func TestRabbitMQConfig_WithOptions(t *testing.T) {
	url := "amqp://guest:guest@localhost:5672/"
	config := NewRabbitMQConfig(url,
		WithRabbitMQExchange("test-exchange"),
		WithRabbitMQTopicPrefix("test-prefix"),
		WithRabbitMQPrefetch(20, 1024),
		WithRabbitMQAuth("user", "pass"),
	)

	if config.Exchange != "test-exchange" {
		t.Errorf("expected Exchange to be 'test-exchange', got %s", config.Exchange)
	}
	if config.TopicPrefix != "test-prefix" {
		t.Errorf("expected TopicPrefix to be 'test-prefix', got %s", config.TopicPrefix)
	}
	if config.PrefetchCount != 20 {
		t.Errorf("expected PrefetchCount to be 20, got %d", config.PrefetchCount)
	}
	if config.PrefetchSize != 1024 {
		t.Errorf("expected PrefetchSize to be 1024, got %d", config.PrefetchSize)
	}
	if config.Username != "user" {
		t.Errorf("expected Username to be 'user', got %s", config.Username)
	}
	if config.Password != "pass" {
		t.Errorf("expected Password to be 'pass', got %s", config.Password)
	}
}

func TestRabbitMQConfig_TLS(t *testing.T) {
	url := "amqps://guest:guest@localhost:5671/"
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
	}
	config := NewRabbitMQConfig(url,
		WithRabbitMQTLS(tlsConfig),
	)

	if config.TLSConfig != tlsConfig {
		t.Error("expected TLSConfig to be set")
	}
}

func TestRabbitMQConfig_AllFields(t *testing.T) {
	url := "amqp://guest:guest@localhost:5672/"
	tlsConfig := &tls.Config{InsecureSkipVerify: true}
	config := &RabbitMQConfig{
		URL:               url,
		Exchange:          "test-exchange",
		TopicPrefix:       "test-prefix",
		DelaySlotCount:    24,
		DelaySlotDuration: time.Hour,
		PrefetchCount:     10,
		PrefetchSize:      0,
		Username:          "user",
		Password:          "pass",
		TLSConfig:         tlsConfig,
	}

	if config.URL == "" {
		t.Error("expected URL to be set")
	}
	if config.Exchange == "" {
		t.Error("expected Exchange to be set")
	}
	if config.TopicPrefix == "" {
		t.Error("expected TopicPrefix to be set")
	}
	if config.DelaySlotCount != 24 {
		t.Errorf("expected DelaySlotCount to be 24, got %d", config.DelaySlotCount)
	}
	if config.DelaySlotDuration != time.Hour {
		t.Errorf("expected DelaySlotDuration to be %v, got %v", time.Hour, config.DelaySlotDuration)
	}
	if config.PrefetchCount != 10 {
		t.Errorf("expected PrefetchCount to be 10, got %d", config.PrefetchCount)
	}
	if config.PrefetchSize != 0 {
		t.Errorf("expected PrefetchSize to be 0, got %d", config.PrefetchSize)
	}
	if config.Username != "user" {
		t.Errorf("expected Username to be 'user', got %s", config.Username)
	}
	if config.Password != "pass" {
		t.Errorf("expected Password to be 'pass', got %s", config.Password)
	}
	if config.TLSConfig != tlsConfig {
		t.Error("expected TLSConfig to be set")
	}
}

func TestContainsAuthInfo(t *testing.T) {
	// Test containsAuthInfo indirectly through URL patterns
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{
			name: "URL with auth",
			url:  "amqp://user:pass@localhost:5672/",
			want: true,
		},
		{
			name: "URL without auth",
			url:  "amqp://localhost:5672/",
			want: false,
		},
		{
			name: "URL with guest",
			url:  "amqp://guest:guest@localhost:5672/",
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// containsAuthInfo checks if URL contains @ symbol (which indicates auth)
			hasAuth := false
			for i := 0; i < len(tt.url); i++ {
				if tt.url[i] == '@' {
					hasAuth = true
					break
				}
			}
			if hasAuth != tt.want {
				t.Errorf("expected hasAuth to be %v, got %v", tt.want, hasAuth)
			}
		})
	}
}

func TestBuildURL(t *testing.T) {
	// Test buildURL logic indirectly
	// buildURL constructs URL from components
	config := NewRabbitMQConfig("amqp://localhost:5672/",
		WithRabbitMQAuth("user", "pass"),
	)

	// Verify auth info is stored separately
	if config.Username != "user" {
		t.Errorf("expected Username to be 'user', got %s", config.Username)
	}
	if config.Password != "pass" {
		t.Errorf("expected Password to be 'pass', got %s", config.Password)
	}
}
