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

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/plugin"
)

func TestExecutorManager(t *testing.T) {
	manager := NewExecutorManager()

	// 创建测试执行器
	localExec := &mockExecutor{name: "local", canExecute: func(req *ExecutionRequest) bool {
		return req != nil && req.Step != nil && !req.Step.RunRemotely
	}}
	remoteExec := &mockExecutor{name: "remote", canExecute: func(req *ExecutionRequest) bool {
		return req != nil && req.Step != nil && req.Step.RunRemotely
	}}

	manager.Register(localExec)
	manager.Register(remoteExec)

	// 测试本地执行器选择
	localReq := &ExecutionRequest{
		Step: &StepInfo{
			Name:        "test-step",
			Uses:        "shell",
			RunRemotely: false,
		},
	}
	executor, err := manager.SelectExecutor(localReq)
	if err != nil {
		t.Fatalf("failed to select executor: %v", err)
	}
	if executor.Name() != "local" {
		t.Errorf("expected local executor, got %s", executor.Name())
	}

	// 测试远程执行器选择
	remoteReq := &ExecutionRequest{
		Step: &StepInfo{
			Name:        "test-step",
			Uses:        "shell",
			RunRemotely: true,
		},
	}
	executor, err = manager.SelectExecutor(remoteReq)
	if err != nil {
		t.Fatalf("failed to select executor: %v", err)
	}
	if executor.Name() != "remote" {
		t.Errorf("expected remote executor, got %s", executor.Name())
	}
}

func TestExecutorManager_SelectExecutor_NoExecutorAvailable(t *testing.T) {
	manager := NewExecutorManager()

	req := &ExecutionRequest{
		Step: &StepInfo{
			Name: "test-step",
		},
	}

	_, err := manager.SelectExecutor(req)
	if err == nil {
		t.Error("expected error when no executor available")
	}
}

func TestExecutorManager_Execute(t *testing.T) {
	manager := NewExecutorManager()

	executed := false
	mockExec := &mockExecutor{
		name: "test",
		canExecute: func(req *ExecutionRequest) bool {
			return true
		},
		executeFunc: func(ctx context.Context, req *ExecutionRequest) (*ExecutionResult, error) {
			executed = true
			result := NewExecutionResult("test")
			result.Complete(true, 0, nil)
			return result, nil
		},
	}

	manager.Register(mockExec)

	req := &ExecutionRequest{
		Step: &StepInfo{Name: "test-step"},
	}

	result, err := manager.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !executed {
		t.Error("executor was not executed")
	}
	if !result.Success {
		t.Error("expected success")
	}
}

func TestExecutorManager_ListExecutors(t *testing.T) {
	manager := NewExecutorManager()

	exec1 := &mockExecutor{name: "exec1"}
	exec2 := &mockExecutor{name: "exec2"}
	exec3 := &mockExecutor{name: "exec3"}

	manager.Register(exec1)
	manager.Register(exec2)
	manager.Register(exec3)

	executors := manager.ListExecutors()
	if len(executors) != 3 {
		t.Errorf("expected 3 executors, got %d", len(executors))
	}

	names := make(map[string]bool)
	for _, exec := range executors {
		names[exec.Name()] = true
	}

	if !names["exec1"] || !names["exec2"] || !names["exec3"] {
		t.Error("expected all executors to be listed")
	}
}

func TestExecutorManager_GetExecutor(t *testing.T) {
	manager := NewExecutorManager()

	exec1 := &mockExecutor{name: "exec1"}
	exec2 := &mockExecutor{name: "exec2"}

	manager.Register(exec1)
	manager.Register(exec2)

	found := manager.GetExecutor("exec1")
	if found == nil || found.Name() != "exec1" {
		t.Errorf("expected to find exec1, got %v", found)
	}

	notFound := manager.GetExecutor("nonexistent")
	if notFound != nil {
		t.Errorf("expected nil for nonexistent executor, got %v", notFound)
	}
}

func TestExecutionResult(t *testing.T) {
	result := NewExecutionResult("test-executor")
	if result.ExecutorName != "test-executor" {
		t.Errorf("expected executor name 'test-executor', got '%s'", result.ExecutorName)
	}
	if result.StartTime.IsZero() {
		t.Error("expected start time to be set")
	}
	if result.Metadata == nil {
		t.Error("expected metadata map to be initialized")
	}

	result.Complete(true, 0, nil)
	if !result.Success {
		t.Error("expected success to be true")
	}
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
	if result.Duration == 0 {
		t.Error("expected duration to be set")
	}
	if result.EndTime.IsZero() {
		t.Error("expected end time to be set")
	}

	result.WithOutput("stdout", "stderr")
	if result.Output != "stdout" {
		t.Errorf("expected output 'stdout', got '%s'", result.Output)
	}
	if result.ErrorOutput != "stderr" {
		t.Errorf("expected error output 'stderr', got '%s'", result.ErrorOutput)
	}

	result.WithMetadata("key1", "value1")
	if result.Metadata["key1"] != "value1" {
		t.Errorf("expected metadata key1='value1', got '%v'", result.Metadata["key1"])
	}

	result.WithMetadata("key2", 123)
	if result.Metadata["key2"] != 123 {
		t.Errorf("expected metadata key2=123, got '%v'", result.Metadata["key2"])
	}
}

func TestExecutionResult_CompleteWithError(t *testing.T) {
	result := NewExecutionResult("test")
	testErr := errors.New("test error")
	result.Complete(false, 1, testErr)

	if result.Success {
		t.Error("expected success to be false")
	}
	if result.ExitCode != 1 {
		t.Errorf("expected exit code 1, got %d", result.ExitCode)
	}
	if result.Error != "test error" {
		t.Errorf("expected error 'test error', got '%s'", result.Error)
	}
}

func TestNewExecutionRequest(t *testing.T) {
	step := &StepInfo{Name: "test-step"}
	job := &JobInfo{Name: "test-job"}
	pipeline := &PipelineInfo{Namespace: "test-ns"}

	req := NewExecutionRequest(step, job, pipeline)

	if req.Step != step {
		t.Error("step not set correctly")
	}
	if req.Job != job {
		t.Error("job not set correctly")
	}
	if req.Pipeline != pipeline {
		t.Error("pipeline not set correctly")
	}
	if req.Env == nil {
		t.Error("env map not initialized")
	}
	if req.Options == nil {
		t.Error("options not initialized")
	}
	if req.Options.Extra == nil {
		t.Error("options extra map not initialized")
	}
}

func TestUnifiedExecutor_Name(t *testing.T) {
	exec := NewUnifiedExecutor(nil, nil, log.Logger{})
	if exec.Name() != "unified" {
		t.Errorf("expected name 'unified', got '%s'", exec.Name())
	}
}

func TestUnifiedExecutor_CanExecute(t *testing.T) {
	mockPluginMgr := &mockPluginManager{}
	mockRemoteExec := &mockRemoteExecutor{}

	tests := []struct {
		name          string
		pluginManager *mockPluginManager
		remoteExec    *mockRemoteExecutor
		req           *ExecutionRequest
		want          bool
	}{
		{
			name:          "local execution with plugin manager",
			pluginManager: mockPluginMgr,
			remoteExec:    nil,
			req: &ExecutionRequest{
				Step: &StepInfo{RunRemotely: false},
			},
			want: true,
		},
		{
			name:          "remote execution with remote executor",
			pluginManager: nil,
			remoteExec:    mockRemoteExec,
			req: &ExecutionRequest{
				Step: &StepInfo{RunRemotely: true},
			},
			want: true,
		},
		{
			name:          "local execution without plugin manager",
			pluginManager: nil,
			remoteExec:    nil,
			req: &ExecutionRequest{
				Step: &StepInfo{RunRemotely: false},
			},
			want: false,
		},
		{
			name:          "remote execution without remote executor",
			pluginManager: mockPluginMgr,
			remoteExec:    nil,
			req: &ExecutionRequest{
				Step: &StepInfo{RunRemotely: true},
			},
			want: false,
		},
		{
			name:          "nil request",
			pluginManager: mockPluginMgr,
			remoteExec:    mockRemoteExec,
			req:           nil,
			want:          false,
		},
		{
			name:          "nil step",
			pluginManager: mockPluginMgr,
			remoteExec:    mockRemoteExec,
			req:           &ExecutionRequest{Step: nil},
			want:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var pm *plugin.Manager
			if tt.pluginManager != nil {
				pm = tt.pluginManager.toPluginManager()
			}
			var re RemoteExecutor
			if tt.remoteExec != nil {
				re = tt.remoteExec
			}
			exec := NewUnifiedExecutor(pm, re, log.Logger{})
			got := exec.CanExecute(tt.req)
			if got != tt.want {
				t.Errorf("CanExecute() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUnifiedExecutor_Execute_NilStep(t *testing.T) {
	exec := NewUnifiedExecutor(nil, nil, log.Logger{})
	req := &ExecutionRequest{Step: nil}

	result, err := exec.Execute(context.Background(), req)
	if err == nil {
		t.Error("expected error for nil step")
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
	if result.Success {
		t.Error("expected failure")
	}
}

func TestUnifiedExecutor_ExecuteRemotely(t *testing.T) {
	mockRemote := &mockRemoteExecutor{
		executeFunc: func(ctx context.Context, req *RemoteExecutionRequest) (*RemoteExecutionResult, error) {
			return &RemoteExecutionResult{
				Success:   true,
				ExitCode:  0,
				StartTime: time.Now(),
				EndTime:   time.Now().Add(time.Second),
				Metrics:   map[string]string{"key": "value"},
			}, nil
		},
	}

	exec := NewUnifiedExecutor(nil, mockRemote, log.Logger{})
	req := &ExecutionRequest{
		Step: &StepInfo{
			Name:        "test-step",
			Uses:        "test-plugin",
			Action:      "test-action",
			RunRemotely: true,
			RemoteSelector: &RemoteSelector{
				MatchLabels: map[string]string{"env": "prod"},
			},
		},
		Job:       &JobInfo{Name: "test-job"},
		Pipeline:  &PipelineInfo{Namespace: "test-ns"},
		Workspace: "/tmp/workspace",
		Env:       map[string]string{"KEY": "value"},
	}

	result, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Error("expected success")
	}
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
	if result.Metadata["key"] != "value" {
		t.Error("expected metadata to be set")
	}
}

func TestUnifiedExecutor_ExecuteRemotely_NoRemoteExecutor(t *testing.T) {
	exec := NewUnifiedExecutor(nil, nil, log.Logger{})
	req := &ExecutionRequest{
		Step: &StepInfo{
			Name:        "test-step",
			RunRemotely: true,
		},
	}

	result, err := exec.Execute(context.Background(), req)
	if err == nil {
		t.Error("expected error when remote executor is nil")
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
	if result.Success {
		t.Error("expected failure")
	}
}

func TestUnifiedExecutor_ExecuteLocally(t *testing.T) {
	mockPlugin := &mockPlugin{
		name: "test-plugin",
		executeFunc: func(action string, params, opts json.RawMessage) (json.RawMessage, error) {
			result := map[string]any{
				"success":   true,
				"exit_code": 0,
				"stdout":    "test output",
				"stderr":    "",
			}
			return json.Marshal(result)
		},
	}

	mockPluginMgr := &mockPluginManager{
		plugins: map[string]plugin.Plugin{
			"test-plugin": mockPlugin,
		},
	}

	exec := NewUnifiedExecutor(mockPluginMgr.toPluginManager(), nil, log.Logger{})
	req := &ExecutionRequest{
		Step: &StepInfo{
			Name:        "test-step",
			Uses:        "test-plugin",
			Action:      "Execute",
			RunRemotely: false,
			Args:        map[string]any{"key": "value"},
		},
		Job:       &JobInfo{Name: "test-job"},
		Pipeline:  &PipelineInfo{Namespace: "test-ns"},
		Workspace: "/tmp/workspace",
		Env:       map[string]string{"KEY": "value"},
	}

	result, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Error("expected success")
	}
	if result.Output != "test output" {
		t.Errorf("expected output 'test output', got '%s'", result.Output)
	}
}

func TestUnifiedExecutor_ExecuteLocally_NoPluginManager(t *testing.T) {
	exec := NewUnifiedExecutor(nil, nil, log.Logger{})
	req := &ExecutionRequest{
		Step: &StepInfo{
			Name:        "test-step",
			Uses:        "test-plugin",
			RunRemotely: false,
		},
	}

	result, err := exec.Execute(context.Background(), req)
	if err == nil {
		t.Error("expected error when plugin manager is nil")
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
	if result.Success {
		t.Error("expected failure")
	}
}

func TestUnifiedExecutor_ExecuteLocally_PluginNotFound(t *testing.T) {
	mockPluginMgr := &mockPluginManager{
		plugins: map[string]plugin.Plugin{},
	}

	exec := NewUnifiedExecutor(mockPluginMgr.toPluginManager(), nil, log.Logger{})
	req := &ExecutionRequest{
		Step: &StepInfo{
			Name:        "test-step",
			Uses:        "nonexistent-plugin",
			RunRemotely: false,
		},
	}

	result, err := exec.Execute(context.Background(), req)
	if err == nil {
		t.Error("expected error when plugin not found")
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
	if result.Success {
		t.Error("expected failure")
	}
}

func TestPluginExecutor_Name(t *testing.T) {
	mockPluginMgr := &mockPluginManager{}
	exec := NewPluginExecutor(mockPluginMgr.toPluginManager(), log.Logger{})
	if exec.Name() != "plugin" {
		t.Errorf("expected name 'plugin', got '%s'", exec.Name())
	}
}

func TestPluginExecutor_CanExecute(t *testing.T) {
	mockPluginMgr := &mockPluginManager{}
	exec := NewPluginExecutor(mockPluginMgr.toPluginManager(), log.Logger{})

	tests := []struct {
		name string
		req  *ExecutionRequest
		want bool
	}{
		{
			name: "local execution",
			req: &ExecutionRequest{
				Step: &StepInfo{RunRemotely: false},
			},
			want: true,
		},
		{
			name: "remote execution",
			req: &ExecutionRequest{
				Step: &StepInfo{RunRemotely: true},
			},
			want: false,
		},
		{
			name: "nil request",
			req:  nil,
			want: false,
		},
		{
			name: "nil step",
			req:  &ExecutionRequest{Step: nil},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := exec.CanExecute(tt.req)
			if got != tt.want {
				t.Errorf("CanExecute() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPluginExecutor_Execute(t *testing.T) {
	mockPlugin := &mockPlugin{
		name: "test-plugin",
		executeFunc: func(action string, params, opts json.RawMessage) (json.RawMessage, error) {
			result := map[string]any{
				"success":   true,
				"exit_code": 0,
				"stdout":    "plugin output",
				"stderr":    "",
			}
			return json.Marshal(result)
		},
	}

	mockPluginMgr := &mockPluginManager{
		plugins: map[string]plugin.Plugin{
			"test-plugin": mockPlugin,
		},
	}

	exec := NewPluginExecutor(mockPluginMgr.toPluginManager(), log.Logger{})
	req := &ExecutionRequest{
		Step: &StepInfo{
			Name:        "test-step",
			Uses:        "test-plugin",
			Action:      "Execute",
			RunRemotely: false,
			Args:        map[string]any{"key": "value"},
		},
		Workspace: "/tmp/workspace",
		Env:       map[string]string{"KEY": "value"},
		Options: &ExecutionOptions{
			Timeout: 30 * time.Second,
		},
	}

	result, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Error("expected success")
	}
	if result.Output != "plugin output" {
		t.Errorf("expected output 'plugin output', got '%s'", result.Output)
	}
}

func TestPluginExecutor_Execute_NilStep(t *testing.T) {
	mockPluginMgr := &mockPluginManager{}
	exec := NewPluginExecutor(mockPluginMgr.toPluginManager(), log.Logger{})
	req := &ExecutionRequest{Step: nil}

	result, err := exec.Execute(context.Background(), req)
	if err == nil {
		t.Error("expected error for nil step")
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
	if result.Success {
		t.Error("expected failure")
	}
}

func TestPluginExecutor_Execute_PluginNotFound(t *testing.T) {
	mockPluginMgr := &mockPluginManager{
		plugins: map[string]plugin.Plugin{},
	}

	exec := NewPluginExecutor(mockPluginMgr.toPluginManager(), log.Logger{})
	req := &ExecutionRequest{
		Step: &StepInfo{
			Name:        "test-step",
			Uses:        "nonexistent-plugin",
			RunRemotely: false,
		},
	}

	result, err := exec.Execute(context.Background(), req)
	if err == nil {
		t.Error("expected error when plugin not found")
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
	if result.Success {
		t.Error("expected failure")
	}
}

func TestPluginExecutor_Execute_HTTPExecution(t *testing.T) {
	mockPluginMgr := &mockPluginManager{
		plugins: map[string]plugin.Plugin{},
	}

	exec := NewPluginExecutor(mockPluginMgr.toPluginManager(), log.Logger{})

	// 测试 HTTP 执行：args 中包含 url 字段
	req := &ExecutionRequest{
		Step: &StepInfo{
			Name:        "http-step",
			Uses:        "http-plugin",
			RunRemotely: false,
			Args: map[string]any{
				"url":    "https://api.example.com/test",
				"method": "GET",
			},
		},
		Workspace: "/tmp/workspace",
		Env:       map[string]string{"KEY": "value"},
	}

	// HTTP 执行会失败（因为没有真实的 HTTP 服务器），但应该走 HTTP 执行路径
	result, err := exec.Execute(context.Background(), req)
	// HTTP 执行可能会失败（连接错误），但应该尝试执行 HTTP 请求
	// 检查是否返回了 HTTP 相关的错误或结果
	if err == nil && result != nil {
		// 如果成功，应该是 HTTP 执行的结果
		if result.ExecutorName != "http" {
			t.Errorf("expected executor name 'http', got '%s'", result.ExecutorName)
		}
	} else if err != nil {
		// HTTP 执行失败是预期的（没有真实服务器），但错误应该来自 HTTP 执行器
		if result != nil && result.ExecutorName != "http" {
			t.Errorf("expected executor name 'http', got '%s'", result.ExecutorName)
		}
	}
}

func TestPluginExecutor_Execute_ShellExecution(t *testing.T) {
	mockPlugin := &mockPlugin{
		name: "shell-plugin",
		executeFunc: func(action string, params, opts json.RawMessage) (json.RawMessage, error) {
			result := map[string]any{
				"success":   true,
				"exit_code": 0,
				"stdout":    "shell output",
				"stderr":    "",
			}
			return json.Marshal(result)
		},
	}

	mockPluginMgr := &mockPluginManager{
		plugins: map[string]plugin.Plugin{
			"shell-plugin": mockPlugin,
		},
	}

	exec := NewPluginExecutor(mockPluginMgr.toPluginManager(), log.Logger{})

	// 测试 Shell 执行：args 中不包含 url 字段
	req := &ExecutionRequest{
		Step: &StepInfo{
			Name:        "shell-step",
			Uses:        "shell-plugin",
			Action:      "Execute",
			RunRemotely: false,
			Args: map[string]any{
				"command": "echo hello",
			},
		},
		Workspace: "/tmp/workspace",
		Env:       map[string]string{"KEY": "value"},
	}

	result, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Error("expected success")
	}
	if result.ExecutorName != "plugin" {
		t.Errorf("expected executor name 'plugin', got '%s'", result.ExecutorName)
	}
	if result.Output != "shell output" {
		t.Errorf("expected output 'shell output', got '%s'", result.Output)
	}
}

func TestPluginExecutor_Execute_HTTPExecution_EmptyURL(t *testing.T) {
	mockPlugin := &mockPlugin{
		name: "test-plugin",
		executeFunc: func(action string, params, opts json.RawMessage) (json.RawMessage, error) {
			result := map[string]any{
				"success":   true,
				"exit_code": 0,
				"stdout":    "plugin output",
			}
			return json.Marshal(result)
		},
	}

	mockPluginMgr := &mockPluginManager{
		plugins: map[string]plugin.Plugin{
			"test-plugin": mockPlugin,
		},
	}

	exec := NewPluginExecutor(mockPluginMgr.toPluginManager(), log.Logger{})

	// 测试：url 字段为空字符串时，应该使用 Shell 执行
	req := &ExecutionRequest{
		Step: &StepInfo{
			Name:        "test-step",
			Uses:        "test-plugin",
			RunRemotely: false,
			Args: map[string]any{
				"url": "", // 空字符串，应该使用 Shell 执行
			},
		},
	}

	result, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExecutorName != "plugin" {
		t.Errorf("expected executor name 'plugin' (Shell execution), got '%s'", result.ExecutorName)
	}
}

func TestPipelineAdapter_ExecuteStep(t *testing.T) {
	executed := false
	mockExec := &mockExecutor{
		name: "test",
		canExecute: func(req *ExecutionRequest) bool {
			return true
		},
		executeFunc: func(ctx context.Context, req *ExecutionRequest) (*ExecutionResult, error) {
			executed = true
			result := NewExecutionResult("test")
			result.Complete(true, 0, nil)
			return result, nil
		},
	}

	manager := NewExecutorManager()
	manager.Register(mockExec)

	adapter := NewPipelineAdapter(manager, log.Logger{})

	step := &StepInfo{Name: "test-step"}
	job := &JobInfo{Name: "test-job"}
	pipeline := &PipelineInfo{Namespace: "test-ns"}

	result, err := adapter.ExecuteStep(
		context.Background(),
		pipeline,
		job,
		step,
		"/tmp/workspace",
		map[string]string{"KEY": "value"},
		nil,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !executed {
		t.Error("executor was not executed")
	}
	if !result.Success {
		t.Error("expected success")
	}
}

func TestPipelineAdapter_ExecuteStep_WithOptions(t *testing.T) {
	manager := NewExecutorManager()
	mockExec := &mockExecutor{
		name: "test",
		canExecute: func(req *ExecutionRequest) bool {
			return true
		},
		executeFunc: func(ctx context.Context, req *ExecutionRequest) (*ExecutionResult, error) {
			if req.Options == nil {
				t.Error("expected options to be set")
			}
			result := NewExecutionResult("test")
			result.Complete(true, 0, nil)
			return result, nil
		},
	}
	manager.Register(mockExec)

	adapter := NewPipelineAdapter(manager, log.Logger{})

	options := &ExecutionOptions{
		Timeout:         30 * time.Second,
		ContinueOnError: true,
		RetryCount:      3,
	}

	result, err := adapter.ExecuteStep(
		context.Background(),
		&PipelineInfo{Namespace: "test-ns"},
		&JobInfo{Name: "test-job"},
		&StepInfo{Name: "test-step"},
		"/tmp/workspace",
		nil,
		options,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Error("expected success")
	}
}

func TestConvertPipelineInfo(t *testing.T) {
	info := ConvertPipelineInfo("test-ns", "v1.0.0", map[string]string{"key": "value"})

	if info.Namespace != "test-ns" {
		t.Errorf("expected namespace 'test-ns', got '%s'", info.Namespace)
	}
	if info.Version != "v1.0.0" {
		t.Errorf("expected version 'v1.0.0', got '%s'", info.Version)
	}
	if info.Variables["key"] != "value" {
		t.Errorf("expected variable key='value', got '%s'", info.Variables["key"])
	}
}

func TestConvertJobInfo(t *testing.T) {
	job := ConvertJobInfo("test-job", "test description", map[string]string{"KEY": "value"}, 3, "5s")

	if job.Name != "test-job" {
		t.Errorf("expected name 'test-job', got '%s'", job.Name)
	}
	if job.Description != "test description" {
		t.Errorf("expected description 'test description', got '%s'", job.Description)
	}
	if job.Env["KEY"] != "value" {
		t.Errorf("expected env KEY='value', got '%s'", job.Env["KEY"])
	}
	if job.Retry == nil {
		t.Fatal("expected retry info to be set")
	}
	if job.Retry.MaxAttempts != 3 {
		t.Errorf("expected max attempts 3, got %d", job.Retry.MaxAttempts)
	}
	if job.Retry.Delay != "5s" {
		t.Errorf("expected delay '5s', got '%s'", job.Retry.Delay)
	}
}

func TestConvertJobInfo_NoRetry(t *testing.T) {
	job := ConvertJobInfo("test-job", "", nil, 0, "")

	if job.Retry != nil {
		t.Error("expected retry info to be nil when maxAttempts is 0")
	}
}

func TestConvertStepInfo(t *testing.T) {
	selector := &RemoteSelector{
		MatchLabels: map[string]string{"env": "prod"},
	}

	step := ConvertStepInfo(
		"test-step",
		"test-plugin",
		"test-action",
		map[string]any{"key": "value"},
		map[string]string{"KEY": "value"},
		"30s",
		true,
		selector,
	)

	if step.Name != "test-step" {
		t.Errorf("expected name 'test-step', got '%s'", step.Name)
	}
	if step.Uses != "test-plugin" {
		t.Errorf("expected uses 'test-plugin', got '%s'", step.Uses)
	}
	if step.Action != "test-action" {
		t.Errorf("expected action 'test-action', got '%s'", step.Action)
	}
	if step.Args["key"] != "value" {
		t.Errorf("expected args key='value', got '%v'", step.Args["key"])
	}
	if step.Env["KEY"] != "value" {
		t.Errorf("expected env KEY='value', got '%s'", step.Env["KEY"])
	}
	if step.Timeout != "30s" {
		t.Errorf("expected timeout '30s', got '%s'", step.Timeout)
	}
	if !step.RunRemotely {
		t.Error("expected RunRemotely to be true")
	}
	if step.RemoteSelector != selector {
		t.Error("expected RemoteSelector to be set")
	}
}

// mockExecutor 用于测试的 mock 执行器
type mockExecutor struct {
	name        string
	canExecute  func(*ExecutionRequest) bool
	executeFunc func(context.Context, *ExecutionRequest) (*ExecutionResult, error)
}

func (m *mockExecutor) Name() string {
	return m.name
}

func (m *mockExecutor) CanExecute(req *ExecutionRequest) bool {
	if m.canExecute != nil {
		return m.canExecute(req)
	}
	return false
}

func (m *mockExecutor) Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResult, error) {
	if m.executeFunc != nil {
		return m.executeFunc(ctx, req)
	}
	result := NewExecutionResult(m.name)
	result.Complete(true, 0, nil)
	return result, nil
}

// mockRemoteExecutor 用于测试的 mock 远程执行器
type mockRemoteExecutor struct {
	executeFunc func(context.Context, *RemoteExecutionRequest) (*RemoteExecutionResult, error)
}

func (m *mockRemoteExecutor) ExecuteStepRemotely(ctx context.Context, req *RemoteExecutionRequest) (*RemoteExecutionResult, error) {
	if m.executeFunc != nil {
		return m.executeFunc(ctx, req)
	}
	return &RemoteExecutionResult{
		Success:   true,
		ExitCode:  0,
		StartTime: time.Now(),
		EndTime:   time.Now(),
	}, nil
}

// mockPlugin 用于测试的 mock plugin
type mockPlugin struct {
	name        string
	executeFunc func(string, json.RawMessage, json.RawMessage) (json.RawMessage, error)
}

func (m *mockPlugin) Name() string {
	if m.name != "" {
		return m.name
	}
	return "mock-plugin"
}

func (m *mockPlugin) Description() string {
	return "mock plugin for testing"
}

func (m *mockPlugin) Version() string {
	return "1.0.0"
}

func (m *mockPlugin) Type() plugin.PluginType {
	return plugin.TypeCustom
}

func (m *mockPlugin) Author() string {
	return "test-author"
}

func (m *mockPlugin) Repository() string {
	return "https://github.com/test/mock-plugin"
}

func (m *mockPlugin) Init(config json.RawMessage) error {
	return nil
}

func (m *mockPlugin) Execute(action string, params, opts json.RawMessage) (json.RawMessage, error) {
	if m.executeFunc != nil {
		return m.executeFunc(action, params, opts)
	}
	return json.Marshal(map[string]any{"success": true})
}

func (m *mockPlugin) Cleanup() error {
	return nil
}

// mockPluginManager 用于测试的 mock plugin manager
type mockPluginManager struct {
	plugins map[string]plugin.Plugin
}

func (m *mockPluginManager) toPluginManager() *plugin.Manager {
	// 创建一个真实的 plugin.Manager 并注册 mock plugins
	pm := plugin.NewManager(nil)
	if m.plugins != nil {
		for name, p := range m.plugins {
			if err := pm.RegisterPlugin(name, p, nil); err != nil {
				// 如果注册失败，继续尝试下一个
				continue
			}
		}
	}
	return pm
}
