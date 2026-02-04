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
	"container/list"
	"sync"
	"time"

	"github.com/arcentrix/arcade/pkg/safe"
)

// TimerWheel is a timer wheel for managing delayed tasks
type TimerWheel struct {
	slots       []*list.List // Time slots
	slotCount   int          // Number of slots
	tickMs      int64        // Time interval for each slot in milliseconds
	currentSlot int          // Current slot index
	mu          sync.RWMutex
	ticker      *time.Ticker
	stopCh      chan struct{}
	wg          sync.WaitGroup
}

// TimerTask represents a timer wheel task
type TimerTask struct {
	Task      *Task
	DelayMs   int64       // Delay time in milliseconds
	ExecuteAt time.Time   // Execution time
	Callback  func(*Task) // Callback function
}

// NewTimerWheel creates a new timer wheel
// slotCount: number of slots
// tickMs: time interval for each slot in milliseconds
func NewTimerWheel(slotCount int, tickMs int64) *TimerWheel {
	if slotCount <= 0 {
		slotCount = 3600 // Default 3600 slots (1 hour, one slot per second)
	}
	if tickMs <= 0 {
		tickMs = 1000 // Default 1 second
	}

	tw := &TimerWheel{
		slots:       make([]*list.List, slotCount),
		slotCount:   slotCount,
		tickMs:      tickMs,
		currentSlot: 0,
		stopCh:      make(chan struct{}),
	}

	// Initialize all slots
	for i := 0; i < slotCount; i++ {
		tw.slots[i] = list.New()
	}

	return tw
}

// Start starts the timer wheel
func (tw *TimerWheel) Start() {
	tw.mu.Lock()
	if tw.ticker != nil {
		tw.mu.Unlock()
		return // Already started
	}

	tw.ticker = time.NewTicker(time.Duration(tw.tickMs) * time.Millisecond)
	tickerC := tw.ticker.C
	tw.wg.Add(1)
	tw.mu.Unlock()

	go tw.run(tickerC)
}

// Stop stops the timer wheel
func (tw *TimerWheel) Stop() {
	tw.mu.Lock()
	defer tw.mu.Unlock()

	if tw.ticker == nil {
		return
	}

	// Close stopCh to signal goroutine to stop
	select {
	case <-tw.stopCh:
		// Already closed
	default:
		close(tw.stopCh)
	}

	tw.ticker.Stop()
	tw.ticker = nil
	tw.wg.Wait()

	// Create new stopCh for potential restart
	tw.stopCh = make(chan struct{})
}

// Add adds a delayed task
func (tw *TimerWheel) Add(task *Task, delayMs int64, callback func(*Task)) {
	if delayMs <= 0 {
		// Execute immediately
		if callback != nil {
			callback(task)
		}
		return
	}

	tw.mu.Lock()
	defer tw.mu.Unlock()

	// Calculate which slot to place the task in
	ticks := delayMs / tw.tickMs
	targetSlot := (tw.currentSlot + int(ticks%int64(tw.slotCount))) % tw.slotCount

	timerTask := &TimerTask{
		Task:      task,
		DelayMs:   delayMs,
		ExecuteAt: time.Now().Add(time.Duration(delayMs) * time.Millisecond),
		Callback:  callback,
	}

	tw.slots[targetSlot].PushBack(timerTask)
}

// AddAt executes a task at the specified time
func (tw *TimerWheel) AddAt(task *Task, executeAt time.Time, callback func(*Task)) {
	now := time.Now()
	if executeAt.Before(now) || executeAt.Equal(now) {
		// Execute immediately
		if callback != nil {
			callback(task)
		}
		return
	}

	delayMs := executeAt.Sub(now).Milliseconds()
	tw.Add(task, delayMs, callback)
}

// run is the timer wheel running loop
func (tw *TimerWheel) run(tickerC <-chan time.Time) {
	defer tw.wg.Done()

	for {
		select {
		case <-tickerC:
			tw.tick()
		case <-tw.stopCh:
			return
		}
	}
}

// tick is the timer wheel tick
func (tw *TimerWheel) tick() {
	tw.mu.Lock()
	defer tw.mu.Unlock()

	// Get all tasks in current slot
	slot := tw.slots[tw.currentSlot]
	tasksToExecute := make([]*TimerTask, 0)

	// Iterate through current slot to find tasks that need to be executed
	for e := slot.Front(); e != nil; {
		timerTask := e.Value.(*TimerTask)
		next := e.Next()

		// Check if execution time has arrived
		if time.Now().After(timerTask.ExecuteAt) || time.Now().Equal(timerTask.ExecuteAt) {
			tasksToExecute = append(tasksToExecute, timerTask)
			slot.Remove(e)
		}

		e = next
	}

	// Execute tasks (execute callbacks outside lock to avoid blocking timer wheel)
	if len(tasksToExecute) > 0 {
		safe.Go(func() {
			for _, timerTask := range tasksToExecute {
				if timerTask.Callback != nil {
					timerTask.Callback(timerTask.Task)
				}
			}
		})
	}

	// Move to next slot
	tw.currentSlot = (tw.currentSlot + 1) % tw.slotCount
}

// Size returns the number of tasks currently in the timer wheel
func (tw *TimerWheel) Size() int {
	tw.mu.RLock()
	defer tw.mu.RUnlock()

	total := 0
	for _, slot := range tw.slots {
		total += slot.Len()
	}
	return total
}

// GetTickMs returns the time interval for each slot in milliseconds
func (tw *TimerWheel) GetTickMs() int64 {
	tw.mu.RLock()
	defer tw.mu.RUnlock()
	return tw.tickMs
}

// GetSlotCount returns the number of slots
func (tw *TimerWheel) GetSlotCount() int {
	tw.mu.RLock()
	defer tw.mu.RUnlock()
	return tw.slotCount
}
