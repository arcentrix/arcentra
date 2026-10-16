// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package sandbox

import (
	"bytes"
	"context"
	"io"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/containerd/fifo"
)

type runtimeIOGatedWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	output  bytes.Buffer
}

func (w *runtimeIOGatedWriter) Write(p []byte) (int, error) {
	w.once.Do(func() {
		close(w.entered)
		<-w.release
	})
	return w.output.Write(p)
}

// This uses the actual SDK FIFO reader without a daemon or runtime privilege.
// containerd v1.7.31 process.Delete calls Cancel before Wait. The exit event
// does not establish that its independent FIFO reader has consumed all bytes.
func TestRuntimeIOCancelBeforeDrainDropsUnreadTail(t *testing.T) {
	for _, cancelBeforeDrain := range []bool{true, false} {
		name := "drain-before-cancel"
		if cancelBeforeDrain {
			name = "sdk-delete-cancel-before-wait"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			sink := &runtimeIOGatedWriter{entered: make(chan struct{}), release: make(chan struct{})}
			stream, err := newRuntimeIO(ctx, runtimeIOTestRoot(t), runtimeIOTestRef(), nil, sink, nil)("synthetic-exec")
			if err != nil {
				t.Fatal(err)
			}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(sink.release) }) }
			t.Cleanup(func() {
				release()
				stream.Cancel()
				stream.Wait()
				_ = stream.Close()
			})
			writer, err := fifo.OpenFifo(ctx, stream.Config().Stdout, syscall.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			const first, tail = "step-retry-enter\n", "step-failed-second\n"
			if _, err := io.WriteString(writer, first); err != nil {
				t.Fatal(err)
			}
			select {
			case <-sink.entered:
			case <-ctx.Done():
				t.Fatal("FIFO reader did not reach the gated sink")
			}
			// The first Write is still blocked in the sink. This second write
			// completes into the kernel FIFO, then the producer closes normally.
			if _, err := io.WriteString(writer, tail); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if cancelBeforeDrain {
				stream.Cancel()
			}
			release()
			done := make(chan struct{})
			safe.Go(func() { stream.Wait(); close(done) })
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("FIFO reader did not finish after the writer closed")
			}
			want := first + tail
			if cancelBeforeDrain {
				want = first
			}
			if got := sink.output.String(); got != want {
				t.Fatalf("cancelBeforeDrain=%t captured=%q want=%q", cancelBeforeDrain, got, want)
			}
		})
	}
}

// A child may keep the FIFO write end open after the observed shell exits.
// Normal EOF waiting cannot complete in that state; canceling the reader must
// release Wait without requiring the child to cooperate or close its writer.
func TestRuntimeIOCancelUnblocksDrainWithWriterStillOpen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sink := &runtimeIOGatedWriter{entered: make(chan struct{}), release: make(chan struct{})}
	stream, err := newRuntimeIO(ctx, runtimeIOTestRoot(t), runtimeIOTestRef(), nil, sink, nil)("synthetic-exec")
	if err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(sink.release) }) }
	t.Cleanup(func() {
		release()
		stream.Cancel()
		stream.Wait()
		_ = stream.Close()
	})
	writer, err := fifo.OpenFifo(ctx, stream.Config().Stdout, syscall.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	const first = "step-retry-enter\n"
	if _, err := io.WriteString(writer, first); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sink.entered:
	case <-ctx.Done():
		t.Fatal("FIFO reader did not reach the gated sink")
	}
	done := make(chan struct{})
	safe.Go(func() { stream.Wait(); close(done) })
	release()
	select {
	case <-done:
		t.Fatal("EOF reported while the producer still holds its FIFO writer")
	default:
	}
	stream.Cancel()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("cancel did not release FIFO Wait with the producer still open")
	}
	if got := sink.output.String(); got != first {
		t.Fatalf("already-read output changed: %q", got)
	}
}
