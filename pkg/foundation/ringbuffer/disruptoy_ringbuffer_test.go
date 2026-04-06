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

func TestRingBuffer_NewRingBuffer_InvalidCapacity(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("NewRingBuffer(0, nil) should panic")
		}
	}()
	NewRingBuffer[int](0, nil)
}

func TestRingBuffer_NewRingBuffer_NonPowerOfTwo(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("NewRingBuffer(3, nil) should panic")
		}
	}()
	NewRingBuffer[int](3, nil)
}

func TestRingBuffer_PublishAndConsume(t *testing.T) {
	rb := NewRingBuffer[int](4, nil)
	c := rb.AddConsumer()

	rb.Publish(10)
	rb.Publish(20)
	rb.Publish(30)
	rb.Publish(40)

	// Producer publishes at seq -1,0,1,2 so published=2; consumer can read seq 0,1,2 (3 items)
	var vals []int
	for i := 0; i < 3; i++ {
		v, seq := rb.Consume(c)
		if seq != int64(i) {
			t.Errorf("Consume #%d: seq=%d, want %d", i+1, seq, i)
		}
		vals = append(vals, v)
	}
	got := make(map[int]bool)
	for _, v := range vals {
		got[v] = true
	}
	if len(got) != 3 {
		t.Errorf("expected 3 distinct values, got %v", vals)
	}
	for _, v := range vals {
		if v != 10 && v != 20 && v != 30 && v != 40 {
			t.Errorf("unexpected value %d", v)
		}
	}
}

func TestRingBuffer_PublishWith(t *testing.T) {
	rb := NewRingBuffer[int](4, nil)
	c := rb.AddConsumer()

	rb.PublishWith(func(slot *int) { *slot = 100 })
	rb.PublishWith(func(slot *int) { *slot = 200 })

	// First published slot is seq -1, second is seq 0; consumer reads seq 0 first
	v, seq := rb.Consume(c)
	if seq != 0 {
		t.Errorf("Consume seq=%d, want 0", seq)
	}
	if v != 200 {
		t.Errorf("Consume #1: got %v, want 200 (seq 0 slot)", v)
	}
}

func TestRingBuffer_TryPublish(t *testing.T) {
	rb := NewRingBuffer[int](4, nil)
	_ = rb.AddConsumer()

	// TryPublish succeeds while producer has room (gating depends on consumer progress)
	for i := 0; i < 4; i++ {
		seq, ok := rb.TryPublish(i)
		if !ok {
			t.Fatalf("TryPublish(%d): got ok=false", i)
		}
		if seq != int64(i)-1 {
			// First seq is -1, then 0, 1, 2
			t.Errorf("TryPublish(%d): seq=%d", i, seq)
		}
	}
}

func TestRingBuffer_MultipleConsumers(t *testing.T) {
	rb := NewRingBuffer[string](4, nil)
	c1 := rb.AddConsumer()
	c2 := rb.AddConsumer()

	rb.Publish("a")
	rb.Publish("b")

	// Both consumers read seq 0 (same slot); fan-out model
	v1, _ := rb.Consume(c1)
	v2, _ := rb.Consume(c2)
	if v1 != "b" || v2 != "b" {
		t.Errorf("first consume: c1=%v c2=%v, want both b (seq 0)", v1, v2)
	}
}

func TestRingBuffer_NilWaitStrategyUsesDefault(t *testing.T) {
	rb := NewRingBuffer[int](4, nil)
	if rb == nil {
		t.Fatal("NewRingBuffer with nil wait should not return nil")
	}
	c := rb.AddConsumer()
	rb.Publish(1)
	rb.Publish(2)
	v, _ := rb.Consume(c)
	if v != 2 {
		t.Errorf("got %v, want 2 (seq 0)", v)
	}
}
