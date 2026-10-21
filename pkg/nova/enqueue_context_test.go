// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package nova

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

type contextEnqueueBroker struct {
	MessageQueueBroker
	send   func(context.Context, string, string, []byte, map[string]string) error
	closed bool
}

func (b *contextEnqueueBroker) ProducerMessage(c context.Context, t, k string, v []byte, h map[string]string) error {
	return b.send(c, t, k, v, h)
}
func (b *contextEnqueueBroker) Close() error { b.closed = true; return nil }
func TestEnqueueContextPropagatesCancellationAndDrains(t *testing.T) {
	for _, kind := range []string{"caller", "queue"} {
		t.Run(kind, func(t *testing.T) {
			lifecycle, stop := context.WithCancel(context.Background())
			defer stop()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered := make(chan struct{})
			exited := make(chan struct{})
			b := &contextEnqueueBroker{send: func(c context.Context, _ string, _ string, _ []byte, _ map[string]string) error {
				close(entered)
				<-c.Done()
				close(exited)
				return c.Err()
			}}
			q := &TaskQueueImpl{ctx: lifecycle, cancel: stop, broker: b, codec: NewJSONCodec()}
			done := make(chan error, 1)
			go func() {
				_, err := q.EnqueueContext(ctx, &Task{Type: "reference", Payload: []byte(`{"fixed":"identity"}`)}, Queue("agent-test"))
				done <- err
			}()
			<-entered
			if kind == "caller" {
				cancel()
			} else {
				stop()
			}
			select {
			case err := <-done:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(time.Second):
				t.Fatal("enqueue did not drain")
			}
			select {
			case <-exited:
			default:
				t.Fatal("broker still executing")
			}
			require.NoError(t, q.Stop())
			require.True(t, b.closed)
		})
	}
}
func TestEnqueueContextUnknownACKPreservesApplicationPayload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var payloads [][]byte
	var taskIDs []string
	var mu sync.Mutex
	b := &contextEnqueueBroker{send: func(c context.Context, topic, key string, v []byte, h map[string]string) error {
		require.Equal(t, "AGENT_TEST", topic)
		require.Equal(t, "reference", h["task_type"])
		var msg TaskMessage
		require.NoError(t, NewJSONCodec().Decode(v, &msg))
		mu.Lock()
		defer mu.Unlock()
		payloads = append(payloads, append([]byte(nil), msg.Task.Payload...))
		taskIDs = append(taskIDs, key)
		if len(payloads) == 1 {
			return context.DeadlineExceeded
		}
		return nil
	}}
	q := &TaskQueueImpl{ctx: ctx, cancel: cancel, broker: b, codec: NewJSONCodec()}
	task := &Task{Type: "reference", Payload: []byte(`{"fixed":"same-reference"}`)}
	result, err := q.EnqueueContext(ctx, task, Queue("agent_test"))
	require.Nil(t, result)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	result, err = q.EnqueueContext(ctx, task, Queue("agent_test"))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, payloads[0], payloads[1])
	require.NotEqual(t, taskIDs[0], taskIDs[1]) // Queue IDs are not execution identity.
}
func TestEnqueueContextRejectsBeforeBrokerAndOrdinaryPathUnchanged(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	b := &contextEnqueueBroker{send: func(c context.Context, _ string, _ string, _ []byte, _ map[string]string) error {
		calls++
		require.Equal(t, ctx, c)
		return nil
	}}
	q := &TaskQueueImpl{ctx: ctx, cancel: cancel, broker: b, codec: NewJSONCodec()}
	expired, stop := context.WithCancel(ctx)
	stop()
	_, err := q.EnqueueContext(expired, &Task{Type: "reference"})
	require.ErrorIs(t, err, context.Canceled)
	_, err = q.EnqueueContext(ctx, &Task{Type: "reference"}, ProcessIn(time.Minute))
	require.Error(t, err)
	_, err = q.EnqueueContext(ctx, nil)
	require.Error(t, err)
	require.Zero(t, calls)
	_, err = q.Enqueue(&Task{Type: "ordinary"})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.False(t, errors.Is(err, context.Canceled))
}
