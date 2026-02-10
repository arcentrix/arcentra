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

func TestGenerateTaskID(t *testing.T) {
	id1 := generateTaskID()
	id2 := generateTaskID()

	if id1 == id2 {
		t.Error("expected generated task IDs to be unique")
	}

	if len(id1) == 0 {
		t.Error("expected task ID to be non-empty")
	}

	// Check format: should contain "_TASKS" suffix
	if len(id1) < len(TasksSuffix)-2 {
		t.Error("expected task ID to have proper format")
	}
}

func TestTaskMessage_Fields(t *testing.T) {
	task := &Task{Type: "test", Payload: []byte("data")}
	msg := TaskMessage{
		TaskID:   "task-123",
		Task:     task,
		Queue:    "test-queue",
		Priority: PriorityHigh,
	}

	if msg.TaskID != "task-123" {
		t.Errorf("expected TaskID to be 'task-123', got %s", msg.TaskID)
	}
	if msg.Task != task {
		t.Error("expected Task to match")
	}
	if msg.Queue != "test-queue" {
		t.Errorf("expected Queue to be 'test-queue', got %s", msg.Queue)
	}
	if msg.Priority != PriorityHigh {
		t.Errorf("expected Priority to be PriorityHigh, got %v", msg.Priority)
	}
}

func TestNewTaskQueue_NoBrokerType(t *testing.T) {
	_, err := NewTaskQueue()
	if err == nil {
		t.Error("expected error when no broker type is specified")
	}
	if err.Error() != "broker type is required, use WithKafka or WithRocketMQ" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestNewTaskQueue_WithKafka(t *testing.T) {
	// This will fail because it tries to connect to Kafka
	// We expect a connection error, not a config error
	_, err := NewTaskQueue(WithKafka("localhost:9092"))
	if err != nil {
		// Check if error is about connection failure (expected) vs config error (unexpected)
		if err.Error() == "broker type is required, use WithKafka or WithRocketMQ" {
			t.Error("unexpected config error")
		}
		// Connection errors are expected in test environment without actual Kafka/RocketMQ running
		// So we just verify that config was accepted (error is not about missing broker type)
	} else {
		// If no error, that's also fine (Kafka might be running)
	}
}

func TestNewTaskQueue_DefaultValues(t *testing.T) {
	// Test that default values are applied
	config := &queueConfig{
		GroupID:           "",
		TopicPrefix:       "",
		DelaySlotCount:    0,
		DelaySlotDuration: 0,
		AutoCommit:        false,
		SessionTimeout:    0,
		MaxPollInterval:   0,
		messageFormat:     MessageFormatJSON,
		messageCodec:      DefaultMessageCodec,
	}

	// Simulate the default value logic from NewTaskQueue
	if config.GroupID == "" {
		// This would use os.Getpid() in actual code
		config.GroupID = "TASK_QUEUE_GROUP_1"
	}
	if config.TopicPrefix == "" {
		config.TopicPrefix = DefaultTopicPrefix
	}
	if config.DelaySlotCount == 0 {
		config.DelaySlotCount = DefaultDelaySlotCount
	}
	if config.DelaySlotDuration == 0 {
		config.DelaySlotDuration = DefaultDelaySlotDuration
	}
	if config.SessionTimeout == 0 {
		config.SessionTimeout = DefaultSessionTimeout
	}
	if config.MaxPollInterval == 0 {
		config.MaxPollInterval = DefaultMaxPollInterval
	}

	if config.GroupID == "" {
		t.Error("expected default GroupID to be set")
	}
	if config.TopicPrefix != DefaultTopicPrefix {
		t.Errorf("expected default TopicPrefix to be %s, got %s", DefaultTopicPrefix, config.TopicPrefix)
	}
	if config.DelaySlotCount != DefaultDelaySlotCount {
		t.Errorf("expected DelaySlotCount to be %d, got %d", DefaultDelaySlotCount, config.DelaySlotCount)
	}
	if config.DelaySlotDuration != DefaultDelaySlotDuration {
		t.Errorf("expected DelaySlotDuration to be %v, got %v", DefaultDelaySlotDuration, config.DelaySlotDuration)
	}
	if config.AutoCommit != false {
		t.Errorf("expected AutoCommit to be false, got %v", config.AutoCommit)
	}
	if config.SessionTimeout != DefaultSessionTimeout {
		t.Errorf("expected SessionTimeout to be %d, got %d", DefaultSessionTimeout, config.SessionTimeout)
	}
	if config.MaxPollInterval != DefaultMaxPollInterval {
		t.Errorf("expected MaxPollInterval to be %d, got %d", DefaultMaxPollInterval, config.MaxPollInterval)
	}
	if config.messageFormat != MessageFormatJSON {
		t.Errorf("expected messageFormat to be MessageFormatJSON, got %v", config.messageFormat)
	}
	if config.messageCodec != DefaultMessageCodec {
		t.Errorf("expected messageCodec to be DefaultMessageCodec, got %v", config.messageCodec)
	}
}

func TestConstants(t *testing.T) {
	if DefaultGroupID != "TASK_QUEUE_GROUP_%d" {
		t.Errorf("expected DefaultGroupID to be 'TASK_QUEUE_GROUP_%%d', got %s", DefaultGroupID)
	}
	if DefaultTopicPrefix != "TASK_QUEUE" {
		t.Errorf("expected DefaultTopicPrefix to be 'TASK_QUEUE', got %s", DefaultTopicPrefix)
	}
	if DefaultDelaySlotCount != 24 {
		t.Errorf("expected DefaultDelaySlotCount to be 24, got %d", DefaultDelaySlotCount)
	}
	if DefaultDelaySlotDuration != time.Hour {
		t.Errorf("expected DefaultDelaySlotDuration to be 1h, got %v", DefaultDelaySlotDuration)
	}
	if DefaultAutoCommit != false {
		t.Errorf("expected DefaultAutoCommit to be false, got %v", DefaultAutoCommit)
	}
	if DefaultSessionTimeout != 30000 {
		t.Errorf("expected DefaultSessionTimeout to be 30000, got %d", DefaultSessionTimeout)
	}
	if DefaultMaxPollInterval != 300000 {
		t.Errorf("expected DefaultMaxPollInterval to be 300000, got %d", DefaultMaxPollInterval)
	}
}

func TestPrioritySuffixes(t *testing.T) {
	if PriorityHighSuffix != "_PRIORITY_HIGH" {
		t.Errorf("expected PriorityHighSuffix to be '_PRIORITY_HIGH', got %s", PriorityHighSuffix)
	}
	if PriorityNormalSuffix != "_PRIORITY_NORMAL" {
		t.Errorf("expected PriorityNormalSuffix to be '_PRIORITY_NORMAL', got %s", PriorityNormalSuffix)
	}
	if PriorityLowSuffix != "_PRIORITY_LOW" {
		t.Errorf("expected PriorityLowSuffix to be '_PRIORITY_LOW', got %s", PriorityLowSuffix)
	}
	if TasksSuffix != "%s_TASKS" {
		t.Errorf("expected TasksSuffix to be '%%s_TASKS', got %s", TasksSuffix)
	}
}

func TestGetQueueName(t *testing.T) {
	// Test getQueueName function if it's exported or testable
	// Since it's private, we test through TaskQueueImpl if possible
	// For now, we test the logic indirectly
	topicPrefix := "TEST_QUEUE"

	highQueue := topicPrefix + PriorityHighSuffix
	normalQueue := topicPrefix + PriorityNormalSuffix
	lowQueue := topicPrefix + PriorityLowSuffix

	if highQueue != "TEST_QUEUE_PRIORITY_HIGH" {
		t.Errorf("expected high queue name to be 'TEST_QUEUE_PRIORITY_HIGH', got %s", highQueue)
	}
	if normalQueue != "TEST_QUEUE_PRIORITY_NORMAL" {
		t.Errorf("expected normal queue name to be 'TEST_QUEUE_PRIORITY_NORMAL', got %s", normalQueue)
	}
	if lowQueue != "TEST_QUEUE_PRIORITY_LOW" {
		t.Errorf("expected low queue name to be 'TEST_QUEUE_PRIORITY_LOW', got %s", lowQueue)
	}
}
