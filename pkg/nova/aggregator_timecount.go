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
	"sync"
	"time"
)

// TimeCountAggregator aggregates tasks based on both time window and count threshold
// Flushes when either the time window is reached or the task count threshold is reached
type TimeCountAggregator struct {
	maxSize       int           // Maximum task count
	timeWindow    time.Duration // Time window
	tasks         []*Task       // Tasks to be aggregated
	mu            sync.Mutex
	lastFlush     time.Time     // Last flush time
	flushTimer    *time.Timer   // Flush timer
	flushCallback func([]*Task) // Flush callback (optional)
}

// NewTimeCountAggregator creates a new TimeCountAggregator
// maxSize: maximum task count before flushing
// timeWindow: time window duration
func NewTimeCountAggregator(maxSize int, timeWindow time.Duration) *TimeCountAggregator {
	agg := &TimeCountAggregator{
		maxSize:    maxSize,
		timeWindow: timeWindow,
		tasks:      make([]*Task, 0, maxSize),
		lastFlush:  time.Now(),
	}

	// Set timer to trigger flush check when time window expires
	if timeWindow > 0 {
		agg.flushTimer = time.AfterFunc(timeWindow, func() {
			agg.checkTimeWindow()
		})
	}

	return agg
}

// Add adds a task to the aggregator
// Flushes immediately if maxSize is reached
func (a *TimeCountAggregator) Add(task *Task) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.tasks = append(a.tasks, task)

	// Flush immediately if max size is reached
	if len(a.tasks) >= a.maxSize {
		a.flushLocked()
	}
}

// ShouldFlush checks if the aggregator should flush
func (a *TimeCountAggregator) ShouldFlush() bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Check count threshold
	if len(a.tasks) >= a.maxSize {
		return true
	}

	// Check time window
	if a.timeWindow > 0 && time.Since(a.lastFlush) >= a.timeWindow {
		return true
	}

	return false
}

// Flush flushes and returns all tasks
func (a *TimeCountAggregator) Flush() []*Task {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.flushLocked()
}

// flushLocked is an internal flush method (requires lock to be held)
func (a *TimeCountAggregator) flushLocked() []*Task {
	if len(a.tasks) == 0 {
		return nil
	}

	tasks := make([]*Task, len(a.tasks))
	copy(tasks, a.tasks)
	a.tasks = a.tasks[:0]
	a.lastFlush = time.Now()

	// Reset timer
	if a.flushTimer != nil {
		a.flushTimer.Stop()
		if a.timeWindow > 0 {
			a.flushTimer = time.AfterFunc(a.timeWindow, func() {
				a.checkTimeWindow()
			})
		}
	}

	return tasks
}

// checkTimeWindow checks the time window (called in timer callback)
func (a *TimeCountAggregator) checkTimeWindow() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if time.Since(a.lastFlush) >= a.timeWindow && len(a.tasks) > 0 {
		tasks := a.flushLocked()
		if a.flushCallback != nil && len(tasks) > 0 {
			// Call callback outside lock to avoid deadlock
			go a.flushCallback(tasks)
		}
	} else if a.timeWindow > 0 {
		// Reset timer
		a.flushTimer = time.AfterFunc(a.timeWindow, func() {
			a.checkTimeWindow()
		})
	}
}

// SetFlushCallback sets the flush callback
func (a *TimeCountAggregator) SetFlushCallback(callback func([]*Task)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.flushCallback = callback
}

// Reset resets the aggregator
func (a *TimeCountAggregator) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.tasks = a.tasks[:0]
	a.lastFlush = time.Now()

	if a.flushTimer != nil {
		a.flushTimer.Stop()
		if a.timeWindow > 0 {
			a.flushTimer = time.AfterFunc(a.timeWindow, func() {
				a.checkTimeWindow()
			})
		}
	}
}

// Size returns the current task count
func (a *TimeCountAggregator) Size() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.tasks)
}
