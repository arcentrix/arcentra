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
	"time"
)

func TestWithKafka(t *testing.T) {
	opt := WithKafka("localhost:9092")
	config := &queueConfig{}
	opt.apply(config)

	if config.Type != QueueTypeKafka {
		t.Errorf("expected Type to be QueueTypeKafka, got %v", config.Type)
	}
	if config.BootstrapServers != "localhost:9092" {
		t.Errorf("expected BootstrapServers to be 'localhost:9092', got %s", config.BootstrapServers)
	}
	if config.kafkaConfig == nil {
		t.Error("expected kafkaConfig to be set")
	}
}

func TestWithRocketMQ(t *testing.T) {
	nameServers := []string{"localhost:9876"}
	opt := WithRocketMQ(nameServers)
	config := &queueConfig{}
	opt.apply(config)

	if config.Type != QueueTypeRocketMQ {
		t.Errorf("expected Type to be QueueTypeRocketMQ, got %v", config.Type)
	}
	if config.BootstrapServers != "localhost:9876" {
		t.Errorf("expected BootstrapServers to be 'localhost:9876', got %s", config.BootstrapServers)
	}
	if config.rocketmqConfig == nil {
		t.Error("expected rocketmqConfig to be set")
	}
}

func TestWithRocketMQ_EmptyNameServers(t *testing.T) {
	opt := WithRocketMQ([]string{})
	config := &queueConfig{}
	opt.apply(config)

	if config.Type != QueueTypeRocketMQ {
		t.Errorf("expected Type to be QueueTypeRocketMQ, got %v", config.Type)
	}
	if config.rocketmqConfig == nil {
		t.Error("expected rocketmqConfig to be set")
	}
}

func TestWithGroupID(t *testing.T) {
	groupID := "test-group"
	opt := WithGroupID(groupID)
	config := &queueConfig{}
	opt.apply(config)

	if config.GroupID != groupID {
		t.Errorf("expected GroupID to be '%s', got %s", groupID, config.GroupID)
	}
}

func TestWithTopicPrefix(t *testing.T) {
	prefix := "test-prefix"
	opt := WithTopicPrefix(prefix)
	config := &queueConfig{}
	opt.apply(config)

	if config.TopicPrefix != prefix {
		t.Errorf("expected TopicPrefix to be '%s', got %s", prefix, config.TopicPrefix)
	}
}

func TestWithDelaySlots(t *testing.T) {
	count := 48
	duration := 30 * time.Minute
	opt := WithDelaySlots(count, duration)
	config := &queueConfig{}
	opt.apply(config)

	if config.DelaySlotCount != count {
		t.Errorf("expected DelaySlotCount to be %d, got %d", count, config.DelaySlotCount)
	}
	if config.DelaySlotDuration != duration {
		t.Errorf("expected DelaySlotDuration to be %v, got %v", duration, config.DelaySlotDuration)
	}
}

func TestWithTaskRecorder(t *testing.T) {
	recorder := &mockTaskRecorder{}
	opt := WithTaskRecorder(recorder)
	config := &queueConfig{}
	opt.apply(config)

	if config.taskRecorder == nil {
		t.Error("expected taskRecorder to be set")
	}
	// Verify it's the same instance by type assertion
	if _, ok := config.taskRecorder.(*mockTaskRecorder); !ok {
		t.Error("expected taskRecorder to be *mockTaskRecorder")
	}
}

func TestWithMessageFormat(t *testing.T) {
	tests := []struct {
		name            string
		format          MessageFormat
		expectedFormat  MessageFormat
		shouldHaveCodec bool
	}{
		{
			name:            "JSON format",
			format:          MessageFormatJSON,
			expectedFormat:  MessageFormatJSON,
			shouldHaveCodec: true,
		},
		{
			name:            "Sonic format",
			format:          MessageFormatSonic,
			expectedFormat:  MessageFormatSonic,
			shouldHaveCodec: true,
		},
		{
			name:            "Blob format",
			format:          MessageFormatBlob,
			expectedFormat:  MessageFormatBlob,
			shouldHaveCodec: true,
		},
		{
			name:            "Protobuf format",
			format:          MessageFormatProtobuf,
			expectedFormat:  MessageFormatProtobuf,
			shouldHaveCodec: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opt := WithMessageFormat(tt.format)
			config := &queueConfig{}
			opt.apply(config)

			if config.messageFormat != tt.expectedFormat {
				t.Errorf("expected messageFormat to be %s, got %s", tt.expectedFormat, config.messageFormat)
			}
			if tt.shouldHaveCodec && config.messageCodec == nil {
				t.Error("expected messageCodec to be set")
			}
			if config.messageCodec != nil && config.messageCodec.Format() != tt.expectedFormat {
				t.Errorf("expected codec format to be %s, got %s", tt.expectedFormat, config.messageCodec.Format())
			}
		})
	}
}

func TestWithMessageCodec(t *testing.T) {
	codec := NewJSONCodec()
	opt := WithMessageCodec(codec)
	config := &queueConfig{}
	opt.apply(config)

	if config.messageCodec != codec {
		t.Error("expected messageCodec to be set")
	}
	if config.messageFormat != MessageFormatJSON {
		t.Errorf("expected messageFormat to be MessageFormatJSON, got %s", config.messageFormat)
	}
}

func TestWithMessageCodec_Nil(t *testing.T) {
	opt := WithMessageCodec(nil)
	config := &queueConfig{}
	opt.apply(config)

	if config.messageCodec != nil {
		t.Error("expected messageCodec to be nil")
	}
}

// mockTaskRecorder is a mock implementation of TaskRecorder for testing
type mockTaskRecorder struct{}

func (m *mockTaskRecorder) Record(ctx context.Context, record *TaskRecord) error {
	return nil
}

func (m *mockTaskRecorder) UpdateStatus(ctx context.Context, taskID string, status TaskStatus, err error) error {
	return nil
}

func (m *mockTaskRecorder) Get(ctx context.Context, taskID string) (*TaskRecord, error) {
	return nil, nil
}

func (m *mockTaskRecorder) ListTaskRecords(ctx context.Context, filter *TaskRecordFilter) ([]*TaskRecord, error) {
	return nil, nil
}

func (m *mockTaskRecorder) Delete(ctx context.Context, taskID string) error {
	return nil
}
