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

func TestWithRabbitMQExchange(t *testing.T) {
	exchange := "test-exchange"
	opt := WithRabbitMQExchange(exchange)
	config := &RabbitMQConfig{}
	opt.apply(config)

	if config.Exchange != exchange {
		t.Errorf("expected Exchange to be '%s', got %s", exchange, config.Exchange)
	}
}

func TestWithRabbitMQTopicPrefix(t *testing.T) {
	prefix := "test-prefix"
	opt := WithRabbitMQTopicPrefix(prefix)
	config := &RabbitMQConfig{}
	opt.apply(config)

	if config.TopicPrefix != prefix {
		t.Errorf("expected TopicPrefix to be '%s', got %s", prefix, config.TopicPrefix)
	}
}

func TestWithRabbitMQPrefetch(t *testing.T) {
	count := 20
	size := 1024
	opt := WithRabbitMQPrefetch(count, size)
	config := &RabbitMQConfig{}
	opt.apply(config)

	if config.PrefetchCount != count {
		t.Errorf("expected PrefetchCount to be %d, got %d", count, config.PrefetchCount)
	}
	if config.PrefetchSize != size {
		t.Errorf("expected PrefetchSize to be %d, got %d", size, config.PrefetchSize)
	}
}

func TestWithRabbitMQAuth(t *testing.T) {
	username := "test-user"
	password := "test-password"
	opt := WithRabbitMQAuth(username, password)
	config := &RabbitMQConfig{}
	opt.apply(config)

	if config.Username != username {
		t.Errorf("expected Username to be '%s', got %s", username, config.Username)
	}
	if config.Password != password {
		t.Errorf("expected Password to be '%s', got %s", password, config.Password)
	}
}

func TestWithRabbitMQTLS(t *testing.T) {
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
	}
	opt := WithRabbitMQTLS(tlsConfig)
	config := &RabbitMQConfig{}
	opt.apply(config)

	if config.TLSConfig != tlsConfig {
		t.Error("expected TLSConfig to be set")
	}
}

func TestWithRabbitMQTLS_Nil(t *testing.T) {
	opt := WithRabbitMQTLS(nil)
	config := &RabbitMQConfig{}
	opt.apply(config)

	if config.TLSConfig != nil {
		t.Error("expected TLSConfig to be nil")
	}
}

func TestWithRabbitMQDelaySlots(t *testing.T) {
	count := 48
	duration := 30 * time.Minute
	opt := WithRabbitMQDelaySlots(count, duration)
	config := &RabbitMQConfig{}
	opt.apply(config)

	if config.DelaySlotCount != count {
		t.Errorf("expected DelaySlotCount to be %d, got %d", count, config.DelaySlotCount)
	}
	if config.DelaySlotDuration != duration {
		t.Errorf("expected DelaySlotDuration to be %v, got %v", duration, config.DelaySlotDuration)
	}
}

func TestNewRabbitMQConfig(t *testing.T) {
	url := "amqp://guest:guest@localhost:5672/"
	config := NewRabbitMQConfig(url)

	if config.URL != url {
		t.Errorf("expected URL to be '%s', got %s", url, config.URL)
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

func TestNewRabbitMQConfig_WithOptions(t *testing.T) {
	url := "amqp://guest:guest@localhost:5672/"
	exchange := "test-exchange"
	prefix := "test-prefix"
	username := "test-user"
	password := "test-password"

	config := NewRabbitMQConfig(url,
		WithRabbitMQExchange(exchange),
		WithRabbitMQTopicPrefix(prefix),
		WithRabbitMQAuth(username, password),
	)

	if config.URL != url {
		t.Errorf("expected URL to be '%s', got %s", url, config.URL)
	}
	if config.Exchange != exchange {
		t.Errorf("expected Exchange to be '%s', got %s", exchange, config.Exchange)
	}
	if config.TopicPrefix != prefix {
		t.Errorf("expected TopicPrefix to be '%s', got %s", prefix, config.TopicPrefix)
	}
	if config.Username != username {
		t.Errorf("expected Username to be '%s', got %s", username, config.Username)
	}
	if config.Password != password {
		t.Errorf("expected Password to be '%s', got %s", password, config.Password)
	}
}
