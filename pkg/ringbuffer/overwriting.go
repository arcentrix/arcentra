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

// OverwritingRingBuffer is a fixed-capacity ring buffer that overwrites the oldest element when full.
//
// Time complexity:
//   - Push: O(1)
//   - Snapshot: O(n) where n = Len()
//   - Latest: O(1)
//   - Len, Cap, Reset: O(1)
//
// Not thread-safe. Caller must synchronize Push, Snapshot, Latest, Reset, and Resize
// (e.g. hold an external mutex when using from multiple goroutines).
//
// Suitable for: FSM history, metrics sliding window, recent logs, pipeline events.
type OverwritingRingBuffer[T any] struct {
	buf      []T
	capacity int
	// count is the total number of elements ever pushed (monotonic).
	count uint64
}

// NewOverwritingRingBuffer returns a new ring buffer with the given capacity.
// Capacity must be greater than zero.
func NewOverwritingRingBuffer[T any](capacity int) *OverwritingRingBuffer[T] {
	if capacity <= 0 {
		panic("OverwritingRingBuffer capacity must be > 0")
	}
	return &OverwritingRingBuffer[T]{
		buf:      make([]T, capacity),
		capacity: capacity,
	}
}

// Cap returns the buffer capacity. O(1).
func (r *OverwritingRingBuffer[T]) Cap() int {
	return r.capacity
}

// Len returns the current number of elements in the buffer (0 until first fill, then at most capacity). O(1).
func (r *OverwritingRingBuffer[T]) Len() int {
	if r.count < uint64(r.capacity) {
		return int(r.count)
	}
	return r.capacity
}

// Push appends v. If the buffer is full, the oldest element is overwritten. O(1), branch-free.
func (r *OverwritingRingBuffer[T]) Push(v T) {
	idx := int(r.count % uint64(r.capacity))
	r.buf[idx] = v
	r.count++
}

// Snapshot returns a new slice containing all elements in chronological order (oldest first). O(n).
// Returns nil if the buffer is empty.
func (r *OverwritingRingBuffer[T]) Snapshot() []T {
	n := r.Len()
	if n == 0 {
		return nil
	}
	out := make([]T, n)
	if r.count <= uint64(r.capacity) {
		copy(out, r.buf[:n])
		return out
	}
	start := int(r.count % uint64(r.capacity))
	copy(out, r.buf[start:])
	copy(out[r.capacity-start:], r.buf[:start])
	return out
}

// Latest returns the most recently pushed element and true, or zero value and false if the buffer is empty. O(1).
func (r *OverwritingRingBuffer[T]) Latest() (T, bool) {
	if r.Len() == 0 {
		var zero T
		return zero, false
	}
	idx := int((r.count - 1) % uint64(r.capacity))
	return r.buf[idx], true
}

// Reset clears the buffer. Element count becomes 0; next Push will write at index 0. O(1).
func (r *OverwritingRingBuffer[T]) Reset() {
	r.count = 0
}

// Resize changes capacity to size, keeping the last min(Len(), size) elements in order. O(n).
func (r *OverwritingRingBuffer[T]) Resize(size int) {
	if size <= 0 {
		size = 1
	}
	old := r.Snapshot()
	r.buf = make([]T, size)
	r.capacity = size
	r.count = 0
	if len(old) > size {
		old = old[len(old)-size:]
	}
	for _, v := range old {
		r.Push(v)
	}
}
