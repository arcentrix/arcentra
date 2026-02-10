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
	"context"
	"testing"
	"time"
)

func TestPriorityOpt(t *testing.T) {
	opts := &TaskOpts{}
	opt := PriorityOpt(PriorityHigh)
	opt.apply(opts)

	if opts.Priority != PriorityHigh {
		t.Errorf("expected Priority to be PriorityHigh, got %v", opts.Priority)
	}
}

func TestProcessAt(t *testing.T) {
	now := time.Now()
	opts := &TaskOpts{}
	opt := ProcessAt(now)
	opt.apply(opts)

	if !opts.ProcessAt.Equal(now) {
		t.Errorf("expected ProcessAt to be %v, got %v", now, opts.ProcessAt)
	}
}

func TestProcessIn(t *testing.T) {
	duration := 5 * time.Second
	opts := &TaskOpts{}
	opt := ProcessIn(duration)
	opt.apply(opts)

	expected := time.Now().Add(duration)
	// Allow some tolerance for time difference
	diff := opts.ProcessAt.Sub(expected)
	if diff < -100*time.Millisecond || diff > 100*time.Millisecond {
		t.Errorf("expected ProcessAt to be approximately %v, got %v", expected, opts.ProcessAt)
	}
}

func TestQueue(t *testing.T) {
	queueName := "custom-queue"
	opts := &TaskOpts{}
	opt := Queue(queueName)
	opt.apply(opts)

	if opts.Queue != queueName {
		t.Errorf("expected Queue to be %s, got %s", queueName, opts.Queue)
	}
}

func TestHandlerFunc_ProcessTask(t *testing.T) {
	var called bool
	var receivedTask *Task

	handler := HandlerFunc(func(ctx context.Context, task *Task) error {
		called = true
		receivedTask = task
		return nil
	})

	task := &Task{Type: "test", Payload: []byte("data")}
	err := handler.ProcessTask(context.Background(), task)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if !called {
		t.Error("expected handler to be called")
	}
	if receivedTask != task {
		t.Error("received task doesn't match")
	}
}

func TestTaskOpts_DefaultValues(t *testing.T) {
	opts := &TaskOpts{}

	if opts.Priority != 0 {
		t.Errorf("expected default Priority to be 0, got %v", opts.Priority)
	}
	if !opts.ProcessAt.IsZero() {
		t.Errorf("expected default ProcessAt to be zero, got %v", opts.ProcessAt)
	}
	if opts.Queue != "" {
		t.Errorf("expected default Queue to be empty, got %s", opts.Queue)
	}
}

func TestPriorityConstants(t *testing.T) {
	if PriorityHigh != 3 {
		t.Errorf("expected PriorityHigh to be 3, got %d", PriorityHigh)
	}
	if PriorityNormal != 2 {
		t.Errorf("expected PriorityNormal to be 2, got %d", PriorityNormal)
	}
	if PriorityLow != 1 {
		t.Errorf("expected PriorityLow to be 1, got %d", PriorityLow)
	}
}

func TestTask_Fields(t *testing.T) {
	task := &Task{
		Type:    "test-type",
		Payload: []byte("test-payload"),
	}

	if task.Type != "test-type" {
		t.Errorf("expected Type to be 'test-type', got %s", task.Type)
	}
	if string(task.Payload) != "test-payload" {
		t.Errorf("expected Payload to be 'test-payload', got %s", string(task.Payload))
	}
}

func TestResult_Fields(t *testing.T) {
	now := time.Now()
	result := &Result{
		ID:       "task-123",
		Queue:    "test-queue",
		Priority: PriorityHigh,
		ETA:      now,
	}

	if result.ID != "task-123" {
		t.Errorf("expected ID to be 'task-123', got %s", result.ID)
	}
	if result.Queue != "test-queue" {
		t.Errorf("expected Queue to be 'test-queue', got %s", result.Queue)
	}
	if result.Priority != PriorityHigh {
		t.Errorf("expected Priority to be PriorityHigh, got %v", result.Priority)
	}
	if !result.ETA.Equal(now) {
		t.Errorf("expected ETA to be %v, got %v", now, result.ETA)
	}
}
