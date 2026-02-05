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
	"testing"
	"time"
)

func TestNewTimerWheel(t *testing.T) {
	tests := []struct {
		name      string
		slotCount int
		tickMs    int64
		wantSlots int
		wantTick  int64
	}{
		{
			name:      "valid parameters",
			slotCount: 100,
			tickMs:    500,
			wantSlots: 100,
			wantTick:  500,
		},
		{
			name:      "zero slotCount uses default",
			slotCount: 0,
			tickMs:    1000,
			wantSlots: 3600,
			wantTick:  1000,
		},
		{
			name:      "zero tickMs uses default",
			slotCount: 100,
			tickMs:    0,
			wantSlots: 100,
			wantTick:  1000,
		},
		{
			name:      "both zero use defaults",
			slotCount: 0,
			tickMs:    0,
			wantSlots: 3600,
			wantTick:  1000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tw := NewTimerWheel(tt.slotCount, tt.tickMs)
			defer tw.Stop()

			if tw.GetSlotCount() != tt.wantSlots {
				t.Errorf("expected slotCount to be %d, got %d", tt.wantSlots, tw.GetSlotCount())
			}
			if tw.GetTickMs() != tt.wantTick {
				t.Errorf("expected tickMs to be %d, got %d", tt.wantTick, tw.GetTickMs())
			}
			if tw.Size() != 0 {
				t.Errorf("expected initial Size to be 0, got %d", tw.Size())
			}
		})
	}
}

func TestTimerWheel_StartStop(t *testing.T) {
	tw := NewTimerWheel(100, 1000)

	// Start
	tw.Start()

	// Start again should be safe
	tw.Start()

	// Stop
	tw.Stop()

	// Stop again should be safe
	tw.Stop()
}

func TestTimerWheel_Add(t *testing.T) {
	tw := NewTimerWheel(100, 1000)
	defer tw.Stop()
	tw.Start()

	var callbackCalled bool
	var callbackTask *Task

	task := &Task{Type: "test", Payload: []byte("data")}
	callback := func(t *Task) {
		callbackCalled = true
		callbackTask = t
	}

	// Add task with zero delay (should execute immediately)
	tw.Add(task, 0, callback)

	// Wait a bit for callback
	time.Sleep(50 * time.Millisecond)

	if !callbackCalled {
		t.Error("expected callback to be called for zero delay")
	}
	if callbackTask != task {
		t.Error("callback task doesn't match")
	}

	// Add task with negative delay (should execute immediately)
	callbackCalled = false
	tw.Add(task, -1, callback)
	time.Sleep(50 * time.Millisecond)

	if !callbackCalled {
		t.Error("expected callback to be called for negative delay")
	}
}

func TestTimerWheel_AddAt(t *testing.T) {
	// Use smaller tick interval (100ms) for faster testing
	tw := NewTimerWheel(100, 100)
	defer tw.Stop()
	tw.Start()

	var callbackCalled bool
	var mu sync.Mutex

	task := &Task{Type: "test", Payload: []byte("data")}
	callback := func(t *Task) {
		mu.Lock()
		defer mu.Unlock()
		callbackCalled = true
	}

	// Add task scheduled for future (200ms from now)
	futureTime := time.Now().Add(200 * time.Millisecond)
	tw.AddAt(task, futureTime, callback)

	if tw.Size() != 1 {
		t.Errorf("expected Size to be 1, got %d", tw.Size())
	}

	// Wait for execution (tick interval is 100ms, so we need at least 2 ticks = 200ms + buffer)
	time.Sleep(350 * time.Millisecond)

	mu.Lock()
	called := callbackCalled
	mu.Unlock()

	if !called {
		t.Error("expected callback to be called after scheduled time")
	}
}

func TestTimerWheel_Size(t *testing.T) {
	tw := NewTimerWheel(100, 1000)
	defer tw.Stop()
	tw.Start()

	if tw.Size() != 0 {
		t.Errorf("expected initial Size to be 0, got %d", tw.Size())
	}

	task1 := &Task{Type: "test1", Payload: []byte("data1")}
	task2 := &Task{Type: "test2", Payload: []byte("data2")}

	tw.Add(task1, 5000, nil)
	if tw.Size() != 1 {
		t.Errorf("expected Size to be 1, got %d", tw.Size())
	}

	tw.Add(task2, 10000, nil)
	if tw.Size() != 2 {
		t.Errorf("expected Size to be 2, got %d", tw.Size())
	}
}

func TestTimerWheel_GetTickMs(t *testing.T) {
	tw := NewTimerWheel(100, 500)
	defer tw.Stop()

	if tw.GetTickMs() != 500 {
		t.Errorf("expected GetTickMs to return 500, got %d", tw.GetTickMs())
	}

	tw2 := NewTimerWheel(200, 1000)
	defer tw2.Stop()

	if tw2.GetTickMs() != 1000 {
		t.Errorf("expected GetTickMs to return 1000, got %d", tw2.GetTickMs())
	}
}

func TestTimerWheel_GetSlotCount(t *testing.T) {
	tw := NewTimerWheel(100, 1000)
	defer tw.Stop()

	if tw.GetSlotCount() != 100 {
		t.Errorf("expected GetSlotCount to return 100, got %d", tw.GetSlotCount())
	}

	tw2 := NewTimerWheel(200, 1000)
	defer tw2.Stop()

	if tw2.GetSlotCount() != 200 {
		t.Errorf("expected GetSlotCount to return 200, got %d", tw2.GetSlotCount())
	}
}
