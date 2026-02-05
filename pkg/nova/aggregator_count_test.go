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
)

func TestNewCountAggregator(t *testing.T) {
	tests := []struct {
		name    string
		maxSize int
		want    int
	}{
		{
			name:    "valid maxSize",
			maxSize: 50,
			want:    50,
		},
		{
			name:    "zero maxSize uses default",
			maxSize: 0,
			want:    100,
		},
		{
			name:    "negative maxSize uses default",
			maxSize: -1,
			want:    100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agg := NewCountAggregator(tt.maxSize)
			if agg.MaxSize() != tt.want {
				t.Errorf("expected MaxSize to be %d, got %d", tt.want, agg.MaxSize())
			}
			if agg.Size() != 0 {
				t.Errorf("expected initial Size to be 0, got %d", agg.Size())
			}
		})
	}
}

func TestCountAggregator_Add(t *testing.T) {
	agg := NewCountAggregator(10)

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

func TestCountAggregator_ShouldFlush(t *testing.T) {
	agg := NewCountAggregator(3)

	// Should not flush when below threshold
	if agg.ShouldFlush() {
		t.Error("expected ShouldFlush to be false when below threshold")
	}

	// Add tasks up to threshold
	agg.Add(&Task{Type: "test1", Payload: []byte("data1")})
	agg.Add(&Task{Type: "test2", Payload: []byte("data2")})
	if agg.ShouldFlush() {
		t.Error("expected ShouldFlush to be false when below threshold")
	}

	// Add task to reach threshold
	agg.Add(&Task{Type: "test3", Payload: []byte("data3")})
	if !agg.ShouldFlush() {
		t.Error("expected ShouldFlush to be true when threshold reached")
	}

	// Should still flush when above threshold
	agg.Add(&Task{Type: "test4", Payload: []byte("data4")})
	if !agg.ShouldFlush() {
		t.Error("expected ShouldFlush to be true when above threshold")
	}
}

func TestCountAggregator_Flush(t *testing.T) {
	agg := NewCountAggregator(10)

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

func TestCountAggregator_Reset(t *testing.T) {
	agg := NewCountAggregator(10)

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

func TestCountAggregator_Size(t *testing.T) {
	agg := NewCountAggregator(10)

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

func TestCountAggregator_MaxSize(t *testing.T) {
	agg := NewCountAggregator(50)
	if agg.MaxSize() != 50 {
		t.Errorf("expected MaxSize to be 50, got %d", agg.MaxSize())
	}

	agg2 := NewCountAggregator(100)
	if agg2.MaxSize() != 100 {
		t.Errorf("expected MaxSize to be 100, got %d", agg2.MaxSize())
	}
}
