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
	"fmt"
	"sync"

	"github.com/arcentrix/arcentra/pkg/dag"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/safe"
)

// Reconciler reconciles pipeline execution state based on DAG.
// It calculates which tasks can be scheduled, tracks per-task terminal
// states (succeeded / failed / skipped), and coordinates task execution.
type Reconciler struct {
	dag           *dag.DAG
	tasks         map[string]*Task
	taskFramework *TaskFramework
	logger        log.Logger
	mu            sync.RWMutex
	completed     map[string]TaskState
	onCompleted   func() // Callback when task completes to trigger next reconcile
}

// NewReconciler creates a new reconciler
func NewReconciler(
	graph *dag.DAG,
	tasks map[string]*Task,
	taskFramework *TaskFramework,
	logger log.Logger,
) *Reconciler {
	return &Reconciler{
		dag:           graph,
		tasks:         tasks,
		taskFramework: taskFramework,
		logger:        logger,
		completed:     make(map[string]TaskState),
	}
}

// SetOnCompleted sets the callback function to be called when a task completes
func (r *Reconciler) SetOnCompleted(callback func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onCompleted = callback
}

// Reconcile calculates which tasks can be scheduled and starts their execution
// Returns true if there are more tasks to process, false if pipeline is complete
func (r *Reconciler) Reconcile(ctx context.Context) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Only succeeded/skipped tasks unlock downstream dependencies; failed tasks
	// do NOT appear in completedNames so their dependents are never scheduled.
	completedNames := make([]string, 0, len(r.completed))
	for name, state := range r.completed {
		if state == TaskStateSucceeded || state == TaskStateSkipped {
			completedNames = append(completedNames, name)
		}
	}

	// Get schedulable tasks from DAG
	schedulableNodes, err := r.dag.GetSchedulable(completedNames...)
	if err != nil {
		return false, fmt.Errorf("get schedulable tasks: %w", err)
	}

	if len(schedulableNodes) == 0 {
		// Check if all tasks are completed
		if len(r.completed) == len(r.tasks) {
			return false, nil // Pipeline complete
		}
		// No schedulable tasks but pipeline not complete - might be waiting
		return true, nil
	}

	// Start execution for schedulable tasks
	for taskName, node := range schedulableNodes {
		// Get task from tasks map using node name
		// DAG returns defaultNode, not TaskNode, so we lookup by name
		task, exists := r.tasks[node.NodeName()]
		if !exists {
			continue
		}

		// Skip if already processing or completed
		if _, done := r.completed[taskName]; done {
			continue
		}

		// Mark as processing (not completed yet)
		// Start task execution asynchronously
		// Capture loop variables for goroutine
		currentTaskName := taskName
		currentTask := task
		safe.Go(func() {
			if err := r.taskFramework.Execute(ctx, currentTask); err != nil {
				r.logger.Errorw("task execution failed", "task", currentTaskName, "error", err)
			}
			r.markCompleted(currentTaskName, currentTask.State)
		})
	}

	return true, nil
}

// markCompleted records the terminal state of a task.
func (r *Reconciler) markCompleted(taskName string, state TaskState) {
	r.mu.Lock()
	r.completed[taskName] = state
	callback := r.onCompleted
	r.mu.Unlock()

	// Trigger next reconcile if callback is set
	if callback != nil {
		callback()
	}
}

// IsCompleted returns true when no further progress is possible: every task
// either ran to a terminal state or is blocked behind a failed dependency.
func (r *Reconciler) IsCompleted() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.completed) == len(r.tasks) {
		return true
	}

	// Check whether every unfinished task is blocked by a failed ancestor.
	successSet := make([]string, 0, len(r.completed))
	for name, state := range r.completed {
		if state == TaskStateSucceeded || state == TaskStateSkipped {
			successSet = append(successSet, name)
		}
	}
	schedulable, err := r.dag.GetSchedulable(successSet...)
	if err != nil {
		return false
	}
	// Filter out already-completed tasks from schedulable set.
	pending := 0
	for name := range schedulable {
		if _, done := r.completed[name]; !done {
			pending++
		}
	}
	return pending == 0
}

// HasFailures returns true if any task finished with a failed state.
func (r *Reconciler) HasFailures() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, state := range r.completed {
		if state == TaskStateFailed {
			return true
		}
	}
	return false
}

// GetFailedTasks returns the names of all tasks that ended in a failed state.
func (r *Reconciler) GetFailedTasks() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var failed []string
	for name, state := range r.completed {
		if state == TaskStateFailed {
			failed = append(failed, name)
		}
	}
	return failed
}

// GetCompletedTasks returns the names of all tasks that reached a terminal state.
func (r *Reconciler) GetCompletedTasks() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	completed := make([]string, 0, len(r.completed))
	for name := range r.completed {
		completed = append(completed, name)
	}
	return completed
}
