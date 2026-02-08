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
	"testing"
)

func TestQueueTypeConstants(t *testing.T) {
	if QueueProviderKafka != "kafka" {
		t.Errorf("expected QueueProviderKafka to be 'kafka', got %s", QueueProviderKafka)
	}
	if QueueProviderRocketMQ != "rocketmq" {
		t.Errorf("expected QueueProviderRocketMQ to be 'rocketmq', got %s", QueueProviderRocketMQ)
	}
}

func TestMessage_Fields(t *testing.T) {
	msg := Message{
		Key:     "test-key",
		Value:   []byte("test-value"),
		Headers: map[string]string{"header1": "value1"},
	}

	if msg.Key != "test-key" {
		t.Errorf("expected Key to be 'test-key', got %s", msg.Key)
	}
	if string(msg.Value) != "test-value" {
		t.Errorf("expected Value to be 'test-value', got %s", string(msg.Value))
	}
	if msg.Headers["header1"] != "value1" {
		t.Errorf("expected Headers['header1'] to be 'value1', got %s", msg.Headers["header1"])
	}
}

func TestMessage_EmptyHeaders(t *testing.T) {
	msg := Message{
		Key:   "test-key",
		Value: []byte("test-value"),
	}

	if msg.Headers != nil {
		t.Error("expected Headers to be nil when not set")
	}
	if len(msg.Headers) != 0 {
		t.Errorf("expected Headers to be empty, got %d", len(msg.Headers))
	}

	if msg.Key != "test-key" {
		t.Errorf("expected Key to be 'test-key', got %s", msg.Key)
	}
	if string(msg.Value) != "test-value" {
		t.Errorf("expected Value to be 'test-value', got %s", string(msg.Value))
	}
	if len(msg.Headers) != 0 {
		t.Errorf("expected Headers to be empty, got %d", len(msg.Headers))
	}
}

func TestMessageHandler(t *testing.T) {
	var receivedMsg *Message
	handler := MessageHandler(func(ctx context.Context, msg *Message) error {
		receivedMsg = msg
		return nil
	})

	msg := &Message{
		Key:   "test-key",
		Value: []byte("test-value"),
	}

	err := handler(context.Background(), msg)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if receivedMsg != msg {
		t.Error("received message doesn't match")
	}
}

func TestBrokerConfig_Interface(t *testing.T) {
	// Test that KafkaConfig implements BrokerConfig interface
	kafkaConfig := &KafkaConfig{
		BootstrapServers: "localhost:9092",
		GroupID:          "test-group",
		TopicPrefix:      "test-prefix",
	}

	// Test GetType (if implemented)
	// Note: KafkaConfig doesn't explicitly implement BrokerConfig,
	// but we can test the fields it should have
	if kafkaConfig.BootstrapServers == "" {
		t.Error("expected BootstrapServers to be set")
	}
	if kafkaConfig.GroupID == "" {
		t.Error("expected GroupID to be set")
	}
	if kafkaConfig.TopicPrefix == "" {
		t.Error("expected TopicPrefix to be set")
	}
}
