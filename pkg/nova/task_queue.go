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
	"time"
)

// Task represents a task to be processed
type Task struct {
	Type    string // Task type
	Payload []byte // Task payload
}

// Priority represents task priority
type Priority int

const (
	PriorityHigh   Priority = 3 // High priority
	PriorityNormal Priority = 2 // Normal priority
	PriorityLow    Priority = 1 // Low priority
)

// TaskOpts contains options for task enqueueing
type TaskOpts struct {
	Priority  Priority  // Task priority
	ProcessAt time.Time // Scheduled processing time
	Queue     string    // Custom queue name
}

// Option is the interface for task options
type Option interface {
	apply(*TaskOpts)
}

type optionFunc func(*TaskOpts)

func (f optionFunc) apply(o *TaskOpts) { f(o) }

// PriorityOpt sets the task priority
func PriorityOpt(p Priority) Option {
	return optionFunc(func(o *TaskOpts) { o.Priority = p })
}

// ProcessAt sets the scheduled processing time
func ProcessAt(t time.Time) Option {
	return optionFunc(func(o *TaskOpts) { o.ProcessAt = t })
}

// ProcessIn sets the scheduled processing time relative to now
func ProcessIn(d time.Duration) Option {
	return optionFunc(func(o *TaskOpts) { o.ProcessAt = time.Now().Add(d) })
}

// Queue sets the custom queue name
func Queue(name string) Option {
	return optionFunc(func(o *TaskOpts) { o.Queue = name })
}

// Result represents the result of enqueueing a task
type Result struct {
	ID       string    // Task ID
	Queue    string    // Queue name
	Priority Priority  // Priority
	ETA      time.Time // Estimated time of arrival
}

// IConsumer is the interface for consuming tasks (enqueueing)
type IConsumer interface {
	// Enqueue enqueues a single task
	Enqueue(task *Task, opts ...Option) (*Result, error)
	// EnqueueBatch enqueues multiple tasks
	EnqueueBatch(tasks []*Task, opts ...Option) (*Result, error)
}

// IProducer is the interface for producing tasks (processing)
type IProducer interface {
	// Start starts the consumer with a single task handler
	Start(handler IHandler) error
	// StartBatch starts the consumer with a batch handler and aggregator
	StartBatch(handler IBatchHandler, agg IAggregator) error
	// Stop stops the consumer
	Stop() error
}

// IHandler is the interface for processing a single task
type IHandler interface {
	// ProcessTask processes a single task
	ProcessTask(ctx context.Context, task *Task) error
}

// HandlerFunc is a function type that implements IHandler
type HandlerFunc func(ctx context.Context, task *Task) error

// ProcessTask implements IHandler interface
func (f HandlerFunc) ProcessTask(ctx context.Context, t *Task) error { return f(ctx, t) }

// IBatchHandler is the interface for processing batches of tasks
type IBatchHandler interface {
	// ProcessBatch processes a batch of tasks
	ProcessBatch(ctx context.Context, tasks []*Task) error
}

// IAggregator is the interface for aggregating tasks
type IAggregator interface {
	// Add adds a task to the aggregator
	Add(task *Task)
	// ShouldFlush checks if the aggregator should flush
	ShouldFlush() bool
	// Flush flushes the aggregator and returns all tasks
	Flush() []*Task
}

// TaskQueue is the main interface for task queue operations
type TaskQueue interface {
	IConsumer
	IProducer
}
