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
	"testing"
	"time"
)

func TestNewTimeAggregator(t *testing.T) {
	tests := []struct {
		name       string
		timeWindow time.Duration
		want       time.Duration
	}{
		{
			name:       "valid timeWindow",
			timeWindow: 5 * time.Second,
			want:       5 * time.Second,
		},
		{
			name:       "zero timeWindow uses default",
			timeWindow: 0,
			want:       10 * time.Second,
		},
		{
			name:       "negative timeWindow uses default",
			timeWindow: -1,
			want:       10 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agg := NewTimeAggregator(tt.timeWindow)
			if agg.TimeWindow() != tt.want {
				t.Errorf("expected TimeWindow to be %v, got %v", tt.want, agg.TimeWindow())
			}
			if agg.Size() != 0 {
				t.Errorf("expected initial Size to be 0, got %d", agg.Size())
			}
			// Clean up
			agg.Stop()
		})
	}
}

func TestTimeAggregator_Add(t *testing.T) {
	agg := NewTimeAggregator(10 * time.Second)
	defer agg.Stop()

	task1 := &Task{Type: "test1", Payload: []byte("data1")}
	task2 := &Task{Type: "test2", Payload: []byte("data2")}

	agg.Add(task1)
	if agg.Size() != 1 {
		t.Errorf("expected Size to be 1 after adding first task, got %d", agg.Size())
	}

	agg.Add(task2)
	if agg.Size() != 2 {
		t.Errorf("expected Size to be 2 after adding second task, got %d", agg.Size())
	}
}

func TestTimeAggregator_ShouldFlush(t *testing.T) {
	agg := NewTimeAggregator(100 * time.Millisecond)
	defer agg.Stop()

	// Should not flush immediately
	if agg.ShouldFlush() {
		t.Error("expected ShouldFlush to be false immediately after creation")
	}

	// Add a task
	agg.Add(&Task{Type: "test1", Payload: []byte("data1")})

	// Should not flush before time window
	if agg.ShouldFlush() {
		t.Error("expected ShouldFlush to be false before time window")
	}

	// Wait for time window
	time.Sleep(150 * time.Millisecond)

	// Should flush after time window
	if !agg.ShouldFlush() {
		t.Error("expected ShouldFlush to be true after time window")
	}
}

func TestTimeAggregator_Flush(t *testing.T) {
	agg := NewTimeAggregator(10 * time.Second)
	defer agg.Stop()

	// Flush empty aggregator
	tasks := agg.Flush()
	if tasks != nil {
		t.Errorf("expected Flush to return nil for empty aggregator, got %v", tasks)
	}
	if agg.Size() != 0 {
		t.Errorf("expected Size to be 0 after flushing empty aggregator, got %d", agg.Size())
	}

	// Add tasks and flush
	task1 := &Task{Type: "test1", Payload: []byte("data1")}
	task2 := &Task{Type: "test2", Payload: []byte("data2")}
	task3 := &Task{Type: "test3", Payload: []byte("data3")}

	agg.Add(task1)
	agg.Add(task2)
	agg.Add(task3)

	tasks = agg.Flush()
	if len(tasks) != 3 {
		t.Errorf("expected Flush to return 3 tasks, got %d", len(tasks))
	}
	if agg.Size() != 0 {
		t.Errorf("expected Size to be 0 after flush, got %d", agg.Size())
	}

	// Verify tasks are correct
	if tasks[0] != task1 || tasks[1] != task2 || tasks[2] != task3 {
		t.Error("flushed tasks don't match added tasks")
	}
}

func TestTimeAggregator_Reset(t *testing.T) {
	agg := NewTimeAggregator(10 * time.Second)
	defer agg.Stop()

	// Add some tasks
	agg.Add(&Task{Type: "test1", Payload: []byte("data1")})
	agg.Add(&Task{Type: "test2", Payload: []byte("data2")})

	if agg.Size() != 2 {
		t.Errorf("expected Size to be 2, got %d", agg.Size())
	}

	agg.Reset()
	if agg.Size() != 0 {
		t.Errorf("expected Size to be 0 after Reset, got %d", agg.Size())
	}
}

func TestTimeAggregator_Stop(t *testing.T) {
	agg := NewTimeAggregator(10 * time.Second)

	// Add a task
	agg.Add(&Task{Type: "test1", Payload: []byte("data1")})

	// Stop should work
	agg.Stop()

	// Stop again should be safe
	agg.Stop()

	// Should still be able to access tasks
	if agg.Size() != 1 {
		t.Errorf("expected Size to be 1 after Stop, got %d", agg.Size())
	}
}

func TestTimeAggregator_Size(t *testing.T) {
	agg := NewTimeAggregator(10 * time.Second)
	defer agg.Stop()

	if agg.Size() != 0 {
		t.Errorf("expected initial Size to be 0, got %d", agg.Size())
	}

	for i := 1; i <= 5; i++ {
		agg.Add(&Task{Type: "test", Payload: []byte("data")})
		if agg.Size() != i {
			t.Errorf("expected Size to be %d, got %d", i, agg.Size())
		}
	}
}

func TestTimeAggregator_TimeWindow(t *testing.T) {
	agg := NewTimeAggregator(5 * time.Second)
	defer agg.Stop()

	if agg.TimeWindow() != 5*time.Second {
		t.Errorf("expected TimeWindow to be 5s, got %v", agg.TimeWindow())
	}

	agg2 := NewTimeAggregator(20 * time.Second)
	defer agg2.Stop()

	if agg2.TimeWindow() != 20*time.Second {
		t.Errorf("expected TimeWindow to be 20s, got %v", agg2.TimeWindow())
	}
}
