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
	"time"
)

func TestRocketMQDelayTopicFormat(t *testing.T) {
	// Test delay topic format constant
	if rocketmqDelayTopicFormat != "%s_DELAY_%d" {
		t.Errorf("expected rocketmqDelayTopicFormat to be '%%s_DELAY_%%d', got %s", rocketmqDelayTopicFormat)
	}
}

func TestRocketMQDelayMessage_Fields(t *testing.T) {
	now := time.Now()
	task := &Task{Type: "test", Payload: []byte("data")}
	executeAt := now.Add(1 * time.Hour)

	msg := RocketMQDelayMessage{
		TaskID:      "task-123",
		Task:        task,
		TargetTopic: "target-topic",
		TargetQueue: "target-queue",
		Priority:    PriorityHigh,
		ExecuteAt:   executeAt,
		CreatedAt:   now,
	}

	if msg.TaskID != "task-123" {
		t.Errorf("expected TaskID to be 'task-123', got %s", msg.TaskID)
	}
	if msg.Task != task {
		t.Error("expected Task to match")
	}
	if msg.TargetTopic != "target-topic" {
		t.Errorf("expected TargetTopic to be 'target-topic', got %s", msg.TargetTopic)
	}
	if msg.TargetQueue != "target-queue" {
		t.Errorf("expected TargetQueue to be 'target-queue', got %s", msg.TargetQueue)
	}
	if msg.Priority != PriorityHigh {
		t.Errorf("expected Priority to be PriorityHigh, got %v", msg.Priority)
	}
	if !msg.ExecuteAt.Equal(executeAt) {
		t.Errorf("expected ExecuteAt to be %v, got %v", executeAt, msg.ExecuteAt)
	}
	if !msg.CreatedAt.Equal(now) {
		t.Errorf("expected CreatedAt to be %v, got %v", now, msg.CreatedAt)
	}
}

func TestRocketMQTaskMessage_Fields(t *testing.T) {
	task := &Task{Type: "test", Payload: []byte("data")}
	msg := RocketMQTaskMessage{
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
