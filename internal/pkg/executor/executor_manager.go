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
	"fmt"
	"sync"
)

// ExecutorManager 执行器管理器
// 负责管理和选择合适的执行器
type ExecutorManager struct {
	executors []Executor
	mu        sync.RWMutex
}

// NewExecutorManager 创建执行器管理器
func NewExecutorManager() *ExecutorManager {
	return &ExecutorManager{
		executors: make([]Executor, 0),
	}
}

// Register 注册执行器
func (m *ExecutorManager) Register(executor Executor) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.executors = append(m.executors, executor)
}

// SelectExecutor 选择合适的执行器
// 按照注册顺序检查每个执行器是否可以执行
func (m *ExecutorManager) SelectExecutor(req *ExecutionRequest) (Executor, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, executor := range m.executors {
		if executor.CanExecute(req) {
			return executor, nil
		}
	}

	return nil, fmt.Errorf("no executor available for step: %s", req.Step.Name)
}

// Execute 执行 step
// 自动选择合适的执行器并执行
func (m *ExecutorManager) Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResult, error) {
	executor, err := m.SelectExecutor(req)
	if err != nil {
		return nil, err
	}

	return executor.Execute(ctx, req)
}

// ListExecutors 列出所有注册的执行器
func (m *ExecutorManager) ListExecutors() []Executor {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]Executor, len(m.executors))
	copy(result, m.executors)
	return result
}

// GetExecutor 根据名称获取执行器
func (m *ExecutorManager) GetExecutor(name string) Executor {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, executor := range m.executors {
		if executor.Name() == name {
			return executor
		}
	}

	return nil
}
