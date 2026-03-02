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
	"time"

	"github.com/arcentrix/arcentra/internal/pkg/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/dag"
)

// TaskState represents the state of a task
type TaskState string

const (
	TaskStatePending   TaskState = "pending"
	TaskStatePrepared  TaskState = "prepared"
	TaskStateCreated   TaskState = "created"
	TaskStateStarted   TaskState = "started"
	TaskStateQueued    TaskState = "queued"
	TaskStateRunning   TaskState = "running"
	TaskStateSucceeded TaskState = "succeeded"
	TaskStateFailed    TaskState = "failed"
	TaskStateSkipped   TaskState = "skipped"
)

// Task represents a node in the DAG
// Each task corresponds to a job in the pipeline
type Task struct {
	// Name uniquely identifies the task (same as job name)
	Name string
	// Job is the original job specification
	Job *spec.Job
	// State represents the current state of the task
	State TaskState
	// Dependencies are the names of tasks that must complete before this task can run
	Dependencies []string
	// CreatedAt is when the task was created
	CreatedAt time.Time
	// StartedAt is when the task started execution
	StartedAt *time.Time
	// CompletedAt is when the task completed (succeeded or failed)
	CompletedAt *time.Time
	// Error is the error if the task failed
	Error error
	// RetryCount is the number of retry attempts
	RetryCount int
}

// TaskNode implements dag.NamedNode for DAG integration
type TaskNode struct {
	task *Task
}

// NewTaskNode creates a new TaskNode
func NewTaskNode(task *Task) *TaskNode {
	return &TaskNode{task: task}
}

// NodeName returns the unique name of the task node
func (n *TaskNode) NodeName() string {
	return n.task.Name
}

// PrevNodeNames returns the names of dependent tasks
func (n *TaskNode) PrevNodeNames() []string {
	return n.task.Dependencies
}

// Task returns the underlying task
func (n *TaskNode) Task() *Task {
	return n.task
}

// BuildDAG builds a DAG from pipeline jobs
// Jobs with DependsOn field will create dependencies in the DAG
func BuildDAG(jobs []*spec.Job) (*dag.DAG, map[string]*Task, error) {
	tasks := make(map[string]*Task)

	// Create tasks from jobs
	for i := range jobs {
		job := jobs[i]
		if job == nil {
			continue
		}
		task := &Task{
			Name:         job.Name,
			Job:          job,
			State:        TaskStatePending,
			Dependencies: make([]string, 0),
			CreatedAt:    time.Now(),
		}
		tasks[job.Name] = task
	}

	// Build dependencies based on job DependsOn field
	for _, task := range tasks {
		if len(task.Job.DependsOn) > 0 {
			for _, depName := range task.Job.DependsOn {
				if _, exists := tasks[depName]; exists {
					task.Dependencies = append(task.Dependencies, depName)
				}
			}
		}
	}

	// Create nodes with dependencies
	nodes := make([]dag.NamedNode, 0, len(tasks))
	for _, task := range tasks {
		nodes = append(nodes, NewTaskNode(task))
	}

	// Create DAG
	graph, err := dag.New(nodes)
	if err != nil {
		return nil, nil, err
	}

	return graph, tasks, nil
}

// IsReady checks if the task is ready to be scheduled
// A task is ready if all its dependencies are completed
func (t *Task) IsReady(completedTasks map[string]bool) bool {
	if len(t.Dependencies) == 0 {
		return true
	}
	for _, dep := range t.Dependencies {
		if !completedTasks[dep] {
			return false
		}
	}
	return true
}

// IsCompleted checks if the task is in a completed state
func (t *Task) IsCompleted() bool {
	return t.State == TaskStateSucceeded || t.State == TaskStateFailed || t.State == TaskStateSkipped
}

// MarkCompleted marks the task as completed with the given state
func (t *Task) MarkCompleted(state TaskState, err error) {
	now := time.Now()
	t.State = state
	t.CompletedAt = &now
	if err != nil {
		t.Error = err
	}
}
