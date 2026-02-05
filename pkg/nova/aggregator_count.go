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
	"sync"
)

// CountAggregator aggregates tasks based on count threshold
// Flushes when the task count reaches the threshold
type CountAggregator struct {
	maxSize int     // Maximum task count
	tasks   []*Task // Tasks to be aggregated
	mu      sync.Mutex
}

// NewCountAggregator creates a new CountAggregator
// maxSize: maximum task count before flushing (default: 100 if <= 0)
func NewCountAggregator(maxSize int) *CountAggregator {
	if maxSize <= 0 {
		maxSize = 100 // Default value
	}
	return &CountAggregator{
		maxSize: maxSize,
		tasks:   make([]*Task, 0, maxSize),
	}
}

// Add adds a task to the aggregator
func (a *CountAggregator) Add(task *Task) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.tasks = append(a.tasks, task)
}

// ShouldFlush checks if the aggregator should flush (reached count threshold)
func (a *CountAggregator) ShouldFlush() bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	return len(a.tasks) >= a.maxSize
}

// Flush flushes and returns all tasks
func (a *CountAggregator) Flush() []*Task {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.tasks) == 0 {
		return nil
	}

	tasks := make([]*Task, len(a.tasks))
	copy(tasks, a.tasks)
	a.tasks = a.tasks[:0]

	return tasks
}

// Reset resets the aggregator
func (a *CountAggregator) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.tasks = a.tasks[:0]
}

// Size returns the current task count
func (a *CountAggregator) Size() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.tasks)
}

// MaxSize returns the maximum task count
func (a *CountAggregator) MaxSize() int {
	return a.maxSize
}
