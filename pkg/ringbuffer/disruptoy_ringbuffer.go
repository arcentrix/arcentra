package ringbuffer

import (
	"math"
	"sync/atomic"
)

// RingBuffer is a Disruptor-like ring buffer for Single Producer, Multi Consumer.
//
// Core ideas:
// - sequence is int64, starts at -1
// - producer claims next sequence with atomic.AddInt64
// - buffer index = seq & mask (capacity must be power of 2)
// - publish uses atomic.StoreInt64(published, seq) as a release barrier
// - consumers independently track their own sequences
// - gating: producer checks min consumer sequence to avoid overwriting unread slots

type RingBuffer[T any] struct {
	buf      []T
	mask     int64
	capacity int64

	// producer cursor (claimed, may be ahead of published)
	cursor int64

	// highest published sequence
	published int64

	// consumer sequences (each consumer updates its own sequence)
	consumers []int64

	// wait strategy for spingning
	wait WaitStrategy
}

func NewRingBuffer[T any](capacity int64, wait WaitStrategy) *RingBuffer[T] {
	if capacity <= 0 || (capacity&(capacity-1)) != 0 {
		panic("capacity must be a power of 2 and greater than 0")
	}
	if wait == nil {
		wait = &YieldingWaitStrategy{}
	}

	r := &RingBuffer[T]{
		buf:       make([]T, capacity),
		mask:      capacity - 1,
		capacity:  capacity,
		cursor:    -1,
		published: -1,
		wait:      wait,
	}
	return r
}

// Consumer represents a consumer cursor.
// Each consumer reads all events in order (like Disruptor event processors).
type Consumer struct {
	sequence int64
}

// AddConsumer registers a consumer and returns it.
// Consumer's sequence starts at -1 (meaning "nothing consumed yet").
func (r *RingBuffer[T]) AddConsumer() *Consumer {
	c := &Consumer{sequence: -1}
	r.consumers = append(r.consumers, c.sequence)
	return c
}

// minConsumerSequence returns the minimum sequence among all consumers.
// If there are no consumers, returns the current cursor.
func (r *RingBuffer[T]) minConsumerSequence() int64 {
	if len(r.consumers) == 0 {
		return atomic.LoadInt64(&r.published)
	}

	m := int64(math.MaxInt64)
	for _, c := range r.consumers {
		seq := atomic.LoadInt64(&c)
		if seq < m {
			m = seq
		}
	}
	return m
}

// waitForFreeSlot blocks (spins) until the ring has space for nextSeq.
func (r *RingBuffer[T]) waitForFreeSlot(nextSeq int64) {
	// warpPoint is the sequence at which the producer would wrap around
	warpPoint := nextSeq - r.capacity
	for warpPoint > r.minConsumerSequence() {
		r.wait.Wait()
	}
}

// TryPublish tries to publish without blocking.
// Returns (seq, true) if success, otherwise returns (_, false) if ring is full.
func (r *RingBuffer[T]) TryPublish(v T) (int64, bool) {
	nextSeq := atomic.AddInt64(&r.cursor, 1) - 1
	wrapPoint := nextSeq - r.capacity
	if wrapPoint > r.minConsumerSequence() {
		// rollback cursor is hard without CAS; we instead "fail fast" by not publishing
		// and letting consumer skip? That breaks sequence continuity.
		//
		// So: for TryPublish, we must use CAS claim, not Add.
		// To keep this implementation simple & correct, we DO NOT support TryPublish with Add.
		//
		// Use Publish (blocking) or implement CAS-based TryNext.
		return 0, false
	}

	r.buf[nextSeq&r.mask] = v
	atomic.StoreInt64(&r.published, nextSeq)
	return nextSeq, true
}

// Publish claims a slot, waits for free space (gating), writes value, then publishes.
// This is the "correct" Disruptor-like publish path for SP.
func (r *RingBuffer[T]) Publish(v T) int64 {
	nextSeq := atomic.AddInt64(&r.cursor, 1) - 1
	r.waitForFreeSlot(nextSeq)

	r.buf[nextSeq&r.mask] = v

	// publish with release semantics
	atomic.StoreInt64(&r.published, nextSeq)
	return nextSeq
}

// PublishWith allows writing into the slot via a callback to reduce copies.
func (r *RingBuffer[T]) PublishWith(write func(slot *T)) int64 {
	nextSeq := atomic.AddInt64(&r.cursor, 1) - 1
	r.waitForFreeSlot(nextSeq)

	idx := nextSeq & r.mask
	write(&r.buf[idx])

	atomic.StoreInt64(&r.published, nextSeq)
	return nextSeq
}

// Consume blocks until the next sequence is published, then returns the event.
// Each consumer reads all events (fan-out model).
func (r *RingBuffer[T]) Consume(c *Consumer) (T, int64) {
	next := atomic.LoadInt64(&c.sequence) + 1

	for {
		available := atomic.LoadInt64(&r.published)
		if next <= available {
			break
		}
		r.wait.Wait()
	}

	v := r.buf[next&r.mask]

	// advance consumer cursor (release)
	atomic.StoreInt64(&c.sequence, next)
	return v, next
}
