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

package pipeline

import (
	"context"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/pkg/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/log"
)

// testContextKey is a custom type for context keys in tests
type testContextKey string

func TestNewContextContext(t *testing.T) {
	pipeline := &spec.Pipeline{
		Namespace: "test-pipeline",
		Jobs:      []spec.Job{},
	}
	execCtx := &ExecutionContext{
		Pipeline:      pipeline,
		WorkspaceRoot: "/tmp/workspace",
		Logger:        log.Logger{},
		Env:           make(map[string]string),
	}

	ctx := NewContext(context.Background(), pipeline, execCtx)
	if ctx == nil {
		t.Fatal("NewContextContext returned nil")
	}
	if ctx.PipelineId() != "test-pipeline" {
		t.Errorf("expected pipelineId 'test-pipeline', got %q", ctx.PipelineId())
	}
	if ctx.Pipeline() != pipeline {
		t.Error("pipeline not set correctly")
	}
	// Test initial state is PENDING
	if ctx.Status() != pipelinev1.PipelineStatus_PIPELINE_STATUS_PENDING {
		t.Errorf("expected initial status PENDING, got %v", ctx.Status())
	}
}

func TestSetAndGet(t *testing.T) {
	ctx := NewContext(context.Background(), &spec.Pipeline{}, nil)

	ctx.Set("key1", "value1")
	ctx.Set("key2", 42)
	ctx.Set("key3", true)

	if val, ok := ctx.Get("key1"); !ok || val != "value1" {
		t.Errorf("Get(key1) = %v, %v, want value1, true", val, ok)
	}

	if val, ok := ctx.Get("key2"); !ok || val != 42 {
		t.Errorf("Get(key2) = %v, %v, want 42, true", val, ok)
	}

	if val, ok := ctx.Get("key3"); !ok || val != true {
		t.Errorf("Get(key3) = %v, %v, want true, true", val, ok)
	}

	if _, ok := ctx.Get("nonexistent"); ok {
		t.Error("Get(nonexistent) should return false")
	}
}

func TestMustGet(t *testing.T) {
	ctx := NewContext(context.Background(), &spec.Pipeline{}, nil)
	ctx.Set("key", "value")

	val := ctx.MustGet("key")
	if val != "value" {
		t.Errorf("MustGet(key) = %v, want value", val)
	}

	defer func() {
		if r := recover(); r == nil {
			t.Error("MustGet(nonexistent) should panic")
		}
	}()
	ctx.MustGet("nonexistent")
}

func TestGetString(t *testing.T) {
	ctx := NewContext(context.Background(), &spec.Pipeline{}, nil)
	ctx.Set("str", "hello")
	ctx.Set("int", 42)

	if s := ctx.GetString("str"); s != "hello" {
		t.Errorf("GetString(str) = %q, want hello", s)
	}

	if s := ctx.GetString("int"); s != "" {
		t.Errorf("GetString(int) = %q, want empty string", s)
	}

	if s := ctx.GetString("nonexistent"); s != "" {
		t.Errorf("GetString(nonexistent) = %q, want empty string", s)
	}
}

func TestGetInt(t *testing.T) {
	ctx := NewContext(context.Background(), &spec.Pipeline{}, nil)
	ctx.Set("int", 42)
	ctx.Set("str", "hello")

	if i := ctx.GetInt("int"); i != 42 {
		t.Errorf("GetInt(int) = %d, want 42", i)
	}

	if i := ctx.GetInt("str"); i != 0 {
		t.Errorf("GetInt(str) = %d, want 0", i)
	}
}

func TestGetBool(t *testing.T) {
	ctx := NewContext(context.Background(), &spec.Pipeline{}, nil)
	ctx.Set("bool", true)
	ctx.Set("str", "hello")

	if b := ctx.GetBool("bool"); b != true {
		t.Errorf("GetBool(bool) = %v, want true", b)
	}

	if b := ctx.GetBool("str"); b != false {
		t.Errorf("GetBool(str) = %v, want false", b)
	}
}

func TestStoreAndRetrieve(t *testing.T) {
	ctx := NewContext(context.Background(), &spec.Pipeline{}, nil)

	ctx.Store("temp1", "data1")
	ctx.Store("temp2", 100)

	if val, ok := ctx.Retrieve("temp1"); !ok || val != "data1" {
		t.Errorf("Retrieve(temp1) = %v, %v, want data1, true", val, ok)
	}

	if val, ok := ctx.Retrieve("temp2"); !ok || val != 100 {
		t.Errorf("Retrieve(temp2) = %v, %v, want 100, true", val, ok)
	}
}

func TestAbort(t *testing.T) {
	ctx := NewContext(context.Background(), &spec.Pipeline{}, nil)

	if ctx.IsAborted() {
		t.Error("context should not be aborted initially")
	}

	ctx.Abort()
	if !ctx.IsAborted() {
		t.Error("context should be aborted after Abort()")
	}
	// Test that Abort transitions to CANCELED state
	if ctx.Status() != pipelinev1.PipelineStatus_PIPELINE_STATUS_CANCELLED {
		t.Errorf("expected status CANCELED after Abort(), got %v", ctx.Status())
	}

	ctx2 := NewContext(context.Background(), &spec.Pipeline{}, nil)
	err := context.Canceled
	ctx2.AbortWithError(err)
	if !ctx2.IsAborted() {
		t.Error("context should be aborted after AbortWithError()")
	}
	if ctx2.AbortError() != err {
		t.Error("abort error not set correctly")
	}
	// Test that AbortWithError transitions to CANCELED state
	if ctx2.Status() != pipelinev1.PipelineStatus_PIPELINE_STATUS_CANCELLED {
		t.Errorf("expected status CANCELED after AbortWithError(), got %v", ctx2.Status())
	}
}

func TestError(t *testing.T) {
	ctx := NewContext(context.Background(), &spec.Pipeline{}, nil)

	err1 := context.Canceled
	err2 := context.DeadlineExceeded

	ctx.Error(err1)
	ctx.Error(err2)

	errors := ctx.Errors()
	if len(errors) != 2 {
		t.Errorf("expected 2 errors, got %d", len(errors))
	}
}

func TestCurrentJobAndStep(t *testing.T) {
	ctx := NewContext(context.Background(), &spec.Pipeline{}, nil)

	job := &spec.Job{Name: "test-job"}
	step := &spec.Step{Name: "test-step"}

	ctx.SetCurrentJob(job, 0)
	ctx.SetCurrentStep(step, 1)

	if ctx.CurrentJob() != job {
		t.Error("current job not set correctly")
	}
	if ctx.CurrentStep() != step {
		t.Error("current step not set correctly")
	}
	if ctx.JobIndex() != 0 {
		t.Errorf("job index = %d, want 0", ctx.JobIndex())
	}
	if ctx.StepIndex() != 1 {
		t.Errorf("step index = %d, want 1", ctx.StepIndex())
	}
}

func TestMiddleware(t *testing.T) {
	ctx := NewContext(context.Background(), &spec.Pipeline{}, nil)

	callOrder := make([]int, 0)

	middleware1 := func(c *Context) {
		callOrder = append(callOrder, 1)
		c.Next()
		callOrder = append(callOrder, 4)
	}

	middleware2 := func(c *Context) {
		callOrder = append(callOrder, 2)
		c.Next()
		callOrder = append(callOrder, 3)
	}

	ctx.Use(middleware1, middleware2)
	ctx.Next()

	expected := []int{1, 2, 3, 4}
	if len(callOrder) != len(expected) {
		t.Errorf("call order length = %d, want %d", len(callOrder), len(expected))
	}
	for i, v := range expected {
		if i < len(callOrder) && callOrder[i] != v {
			t.Errorf("callOrder[%d] = %d, want %d", i, callOrder[i], v)
		}
	}
}

func TestWithTimeout(t *testing.T) {
	ctx := NewContext(context.Background(), &spec.Pipeline{}, nil)

	newCtx, cancel := ctx.WithTimeout(100 * time.Millisecond)
	defer cancel()

	select {
	case <-newCtx.Done():
		t.Error("context should not be done immediately")
	case <-time.After(50 * time.Millisecond):
		// Expected
	}

	select {
	case <-newCtx.Done():
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Error("context should be done after timeout")
	}
}

func TestWithCancel(t *testing.T) {
	ctx := NewContext(context.Background(), &spec.Pipeline{}, nil)

	newCtx, cancel := ctx.WithCancel()

	select {
	case <-newCtx.Done():
		t.Error("context should not be done before cancel")
	default:
		// Expected
	}

	cancel()

	select {
	case <-newCtx.Done():
		// Expected
	default:
		t.Error("context should be done after cancel")
	}
}

func TestDuration(t *testing.T) {
	ctx := NewContext(context.Background(), &spec.Pipeline{}, nil)

	time.Sleep(10 * time.Millisecond)
	duration := ctx.Duration()
	if duration < 10*time.Millisecond {
		t.Errorf("duration = %v, want at least 10ms", duration)
	}

	ctx.SetEndTime(time.Now())
	duration2 := ctx.Duration()
	if duration2 < duration {
		t.Error("duration should not decrease after setting end time")
	}
}

func TestToMap(t *testing.T) {
	pipeline := &spec.Pipeline{
		Namespace: "test-pipeline",
		Jobs:      []spec.Job{},
	}
	ctx := NewContext(context.Background(), pipeline, nil)
	ctx.SetBuildId("build-123")
	ctx.SetProjectId("project-456")
	ctx.Set("customKey", "customValue")

	m := ctx.ToMap()
	if m["pipelineId"] != "test-pipeline" {
		t.Errorf("pipelineId = %v, want test-pipeline", m["pipelineId"])
	}
	if m["buildId"] != "build-123" {
		t.Errorf("buildId = %v, want build-123", m["buildId"])
	}
	if m["projectId"] != "project-456" {
		t.Errorf("projectId = %v, want project-456", m["projectId"])
	}
	// Test that status is included in ToMap
	if m["status"] != string(pipelinev1.PipelineStatus_PIPELINE_STATUS_PENDING) {
		t.Errorf("status = %v, want %v", m["status"], pipelinev1.PipelineStatus_PIPELINE_STATUS_PENDING)
	}
}

func TestWithContext(t *testing.T) {
	ctx1 := NewContext(context.Background(), &spec.Pipeline{}, nil)
	ctx1.Set("key1", "value1")
	ctx1.SetBuildId("build-1")
	// Transition to RUNNING state
	_ = ctx1.TransitionTo(pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING)

	ctxKey := testContextKey("ctxKey")
	ctx2 := context.WithValue(context.Background(), ctxKey, "ctxValue")
	ctx3 := ctx1.WithContext(ctx2)

	if ctx3.BuildId() != ctx1.BuildId() {
		t.Error("buildId should be preserved")
	}

	if val, ok := ctx3.Get("key1"); !ok || val != "value1" {
		t.Error("keys should be preserved")
	}

	if ctx3.Value(ctxKey) != "ctxValue" {
		t.Error("context values should be preserved")
	}

	// Test that state is preserved
	if ctx3.Status() != pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING {
		t.Errorf("expected status RUNNING, got %v", ctx3.Status())
	}
}

func TestStatusManagement(t *testing.T) {
	ctx := NewContext(context.Background(), &spec.Pipeline{}, nil)

	// Test initial status
	if ctx.Status() != pipelinev1.PipelineStatus_PIPELINE_STATUS_PENDING {
		t.Errorf("expected initial status PENDING, got %v", ctx.Status())
	}

	// Test transition to RUNNING
	if err := ctx.TransitionTo(pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING); err != nil {
		t.Errorf("transition to RUNNING failed: %v", err)
	}
	if ctx.Status() != pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING {
		t.Errorf("expected status RUNNING, got %v", ctx.Status())
	}

	// Test transition to SUCCESS
	if err := ctx.TransitionTo(pipelinev1.PipelineStatus_PIPELINE_STATUS_SUCCESS); err != nil {
		t.Errorf("transition to SUCCESS failed: %v", err)
	}
	if ctx.Status() != pipelinev1.PipelineStatus_PIPELINE_STATUS_SUCCESS {
		t.Errorf("expected status SUCCESS, got %v", ctx.Status())
	}

	// Test that endTime is set when entering terminal state
	if ctx.EndTime() == nil {
		t.Error("endTime should be set when entering terminal state")
	}
}

func TestStatusTransitionValidation(t *testing.T) {
	ctx := NewContext(context.Background(), &spec.Pipeline{}, nil)

	// Test invalid transition (PENDING -> SUCCESS is not allowed)
	if err := ctx.TransitionTo(pipelinev1.PipelineStatus_PIPELINE_STATUS_SUCCESS); err == nil {
		t.Error("expected error for invalid transition PENDING -> SUCCESS")
	}

	// Test valid transition
	if err := ctx.TransitionTo(pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING); err != nil {
		t.Errorf("valid transition should succeed: %v", err)
	}

	// Test CanTransitionTo
	if !ctx.CanTransitionTo(pipelinev1.PipelineStatus_PIPELINE_STATUS_SUCCESS) {
		t.Error("should be able to transition from RUNNING to SUCCESS")
	}
	if ctx.CanTransitionTo(pipelinev1.PipelineStatus_PIPELINE_STATUS_PENDING) {
		t.Error("should not be able to transition from RUNNING to PENDING")
	}
}

func TestSetStatus(t *testing.T) {
	ctx := NewContext(context.Background(), &spec.Pipeline{}, nil)

	// Test SetStatus for initialization/recovery
	ctx.SetStatus(pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING)
	if ctx.Status() != pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING {
		t.Errorf("expected status RUNNING after SetStatus, got %v", ctx.Status())
	}

	// SetStatus bypasses validation, so we can set any state
	ctx.SetStatus(pipelinev1.PipelineStatus_PIPELINE_STATUS_SUCCESS)
	if ctx.Status() != pipelinev1.PipelineStatus_PIPELINE_STATUS_SUCCESS {
		t.Errorf("expected status SUCCESS after SetStatus, got %v", ctx.Status())
	}
}

func TestStateMachineHistory(t *testing.T) {
	ctx := NewContext(context.Background(), &spec.Pipeline{}, nil)

	// Perform some transitions
	_ = ctx.TransitionTo(pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING)
	_ = ctx.TransitionTo(pipelinev1.PipelineStatus_PIPELINE_STATUS_SUCCESS)

	// Check history
	history := ctx.StateMachine().History()
	if len(history) != 2 {
		t.Errorf("expected 2 history records, got %d", len(history))
	}

	// Verify first transition
	if history[0].From != pipelinev1.PipelineStatus_PIPELINE_STATUS_PENDING || history[0].To != pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING {
		t.Errorf("unexpected first transition: %v -> %v", history[0].From, history[0].To)
	}

	// Verify second transition
	if history[1].From != pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING || history[1].To != pipelinev1.PipelineStatus_PIPELINE_STATUS_SUCCESS {
		t.Errorf("unexpected second transition: %v -> %v", history[1].From, history[1].To)
	}

	// Check that history is included in ToMap
	m := ctx.ToMap()
	if historyData, ok := m["stateHistory"].([]map[string]any); ok {
		if len(historyData) != 2 {
			t.Errorf("expected 2 history records in ToMap, got %d", len(historyData))
		}
	} else {
		t.Error("stateHistory not found in ToMap or wrong type")
	}
}
