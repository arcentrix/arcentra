// Copyright 2026 Arcentra Authors.
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

package ringbuffer

import (
	"testing"
)

func TestOverwritingRingBuffer_PushAndSnapshot(t *testing.T) {
	r := NewOverwritingRingBuffer[int](3)
	if r.Len() != 0 || r.Cap() != 3 {
		t.Fatalf("Len=%d Cap=%d", r.Len(), r.Cap())
	}

	r.Push(1)
	r.Push(2)
	snap := r.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 elements, got %d", len(snap))
	}
	if snap[0] != 1 || snap[1] != 2 {
		t.Errorf("expected [1 2], got %v", snap)
	}
}

func TestOverwritingRingBuffer_Overwrite(t *testing.T) {
	r := NewOverwritingRingBuffer[int](3)
	r.Push(1)
	r.Push(2)
	r.Push(3)
	r.Push(4)
	r.Push(5)

	snap := r.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("expected 3 elements after overwrite, got %d", len(snap))
	}
	// Oldest to newest: 3, 4, 5
	if snap[0] != 3 || snap[1] != 4 || snap[2] != 5 {
		t.Errorf("expected [3 4 5], got %v", snap)
	}
}

func TestOverwritingRingBuffer_SnapshotOrder(t *testing.T) {
	r := NewOverwritingRingBuffer[string](2)
	r.Push("a")
	r.Push("b")
	r.Push("c")

	snap := r.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2, got %d", len(snap))
	}
	if snap[0] != "b" || snap[1] != "c" {
		t.Errorf("expected [b c], got %v", snap)
	}
}

func TestOverwritingRingBuffer_Latest(t *testing.T) {
	r := NewOverwritingRingBuffer[int](3)
	if _, ok := r.Latest(); ok {
		t.Error("Latest should return false when empty")
	}

	r.Push(10)
	v, ok := r.Latest()
	if !ok || v != 10 {
		t.Errorf("Latest() = %v, %v; want 10, true", v, ok)
	}

	r.Push(20)
	r.Push(30)
	v, ok = r.Latest()
	if !ok || v != 30 {
		t.Errorf("Latest() = %v, %v; want 30, true", v, ok)
	}

	r.Push(40)
	v, ok = r.Latest()
	if !ok || v != 40 {
		t.Errorf("Latest() = %v, %v; want 40, true", v, ok)
	}
}

func TestOverwritingRingBuffer_Reset(t *testing.T) {
	r := NewOverwritingRingBuffer[int](3)
	r.Push(1)
	r.Push(2)
	r.Reset()

	if r.Len() != 0 {
		t.Errorf("Len() after Reset = %d, want 0", r.Len())
	}
	if snap := r.Snapshot(); snap != nil {
		t.Errorf("Snapshot() after Reset = %v, want nil", snap)
	}
	if _, ok := r.Latest(); ok {
		t.Error("Latest() after Reset should return false")
	}

	r.Push(10)
	snap := r.Snapshot()
	if len(snap) != 1 || snap[0] != 10 {
		t.Errorf("after Reset and Push(10), Snapshot() = %v", snap)
	}
}

func TestOverwritingRingBuffer_Resize(t *testing.T) {
	r := NewOverwritingRingBuffer[int](5)
	r.Push(1)
	r.Push(2)
	r.Push(3)
	r.Resize(2)

	snap := r.Snapshot()
	if r.Cap() != 2 || len(snap) != 2 {
		t.Fatalf("Cap=%d len(snap)=%d", r.Cap(), len(snap))
	}
	if snap[0] != 2 || snap[1] != 3 {
		t.Errorf("expected [2 3], got %v", snap)
	}
}

func TestOverwritingRingBuffer_EmptySnapshot(t *testing.T) {
	r := NewOverwritingRingBuffer[int](2)
	if snap := r.Snapshot(); snap != nil {
		t.Errorf("Snapshot() on empty buffer = %v, want nil", snap)
	}
}

func BenchmarkOverwritingRingBuffer_Push(b *testing.B) {
	r := NewOverwritingRingBuffer[int](1024)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Push(i)
	}
}

func BenchmarkOverwritingRingBuffer_Snapshot(b *testing.B) {
	r := NewOverwritingRingBuffer[int](1024)
	for i := 0; i < 1024; i++ {
		r.Push(i)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.Snapshot()
	}
}

func BenchmarkOverwritingRingBuffer_Latest(b *testing.B) {
	r := NewOverwritingRingBuffer[int](1024)
	for i := 0; i < 1024; i++ {
		r.Push(i)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Latest()
	}
}
