// Copyright 2025 Arcade Team
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

	"github.com/arcentrix/arcade/pkg/safe"
)

// TimeAggregator aggregates tasks based on time window
// Flushes when the time window is reached
type TimeAggregator struct {
	timeWindow time.Duration // Time window
	tasks      []*Task       // Tasks to be aggregated
	mu         sync.Mutex
	lastFlush  time.Time     // Last flush time
	flushTimer *time.Timer   // Flush timer
	stopCh     chan struct{} // Stop signal
	stopped    bool
}

// NewTimeAggregator creates a new TimeAggregator
// timeWindow: time window duration (default: 10s if <= 0)
func NewTimeAggregator(timeWindow time.Duration) *TimeAggregator {
	if timeWindow <= 0 {
		timeWindow = 10 * time.Second // Default value
	}

	agg := &TimeAggregator{
		timeWindow: timeWindow,
		tasks:      make([]*Task, 0),
		lastFlush:  time.Now(),
		stopCh:     make(chan struct{}),
		stopped:    false,
	}

	// Start timer
	agg.startTimer()

	return agg
}

// startTimer starts the flush timer
func (a *TimeAggregator) startTimer() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.stopped {
		return
	}

	// Stop old timer
	if a.flushTimer != nil {
		a.flushTimer.Stop()
	}

	// Create new timer
	a.flushTimer = time.NewTimer(a.timeWindow)
	safe.Go(func() {
		select {
		case <-a.flushTimer.C:
			a.checkTimeWindow()
		case <-a.stopCh:
			return
		}
	})
}

// Add adds a task to the aggregator
func (a *TimeAggregator) Add(task *Task) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.tasks = append(a.tasks, task)
}

// ShouldFlush checks if the aggregator should flush (time window reached)
func (a *TimeAggregator) ShouldFlush() bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	return time.Since(a.lastFlush) >= a.timeWindow && len(a.tasks) > 0
}

// Flush flushes and returns all tasks
func (a *TimeAggregator) Flush() []*Task {
	a.mu.Lock()
	shouldResetTimer := !a.stopped && len(a.tasks) > 0
	tasks := a.flushLocked()
	a.mu.Unlock()

	// Reset timer outside lock to avoid deadlock
	if shouldResetTimer {
		a.resetTimerAfterFlush()
	}

	return tasks
}

// flushLocked is an internal flush method (requires lock to be held)
func (a *TimeAggregator) flushLocked() []*Task {
	if len(a.tasks) == 0 {
		return nil
	}

	tasks := make([]*Task, len(a.tasks))
	copy(tasks, a.tasks)
	a.tasks = a.tasks[:0]
	a.lastFlush = time.Now()

	return tasks
}

// resetTimerAfterFlush resets the timer after flush (called without lock)
func (a *TimeAggregator) resetTimerAfterFlush() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.stopped {
		return
	}

	// Stop old timer
	if a.flushTimer != nil {
		a.flushTimer.Stop()
	}

	// Create new timer
	a.flushTimer = time.NewTimer(a.timeWindow)
	safe.Go(func() {
		select {
		case <-a.flushTimer.C:
			a.checkTimeWindow()
		case <-a.stopCh:
			return
		}
	})
}

// checkTimeWindow checks the time window (called in timer callback)
// Note: This method only marks that the time window has been reached.
// The actual flush should be handled by the caller through ShouldFlush() and Flush()
func (a *TimeAggregator) checkTimeWindow() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.stopped {
		return
	}

	// Time window reached, ShouldFlush() will return true
	// Caller needs to periodically check ShouldFlush() and call Flush()
}

// Reset resets the aggregator
func (a *TimeAggregator) Reset() {
	a.mu.Lock()
	a.tasks = a.tasks[:0]
	a.lastFlush = time.Now()
	shouldResetTimer := !a.stopped
	a.mu.Unlock()

	// Reset timer outside lock to avoid deadlock
	if shouldResetTimer {
		a.resetTimerAfterFlush()
	}
}

// Stop stops the aggregator
func (a *TimeAggregator) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.stopped {
		return
	}

	a.stopped = true
	close(a.stopCh)

	if a.flushTimer != nil {
		a.flushTimer.Stop()
	}
}

// Size returns the current task count
func (a *TimeAggregator) Size() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.tasks)
}

// TimeWindow returns the time window duration
func (a *TimeAggregator) TimeWindow() time.Duration {
	return a.timeWindow
}
