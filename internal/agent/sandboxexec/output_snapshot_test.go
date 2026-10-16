// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package sandboxexec

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/arcentrix/arcentra/pkg/sandbox"
)

func TestSessionOutputSnapshotDoesNotWaitForFailedDrainCallback(t *testing.T) {
	entered, release, writerDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	failure := errors.New("output drain timed out")
	session := newTestSession(t, &fakeBackend{execute: func(_ context.Context, _ string, _ []string, opts *sandbox.ExecuteOptions) (*sandbox.ExecuteResult, error) {
		safe.Go(func() { _, _ = io.WriteString(opts.Stdout, "pending"); close(writerDone) })
		<-entered
		return &sandbox.ExecuteResult{ExitCode: 23}, failure
	}})
	req := request(session, "command")
	req.OutputHandler = func(string, string) { close(entered); <-release }
	done := make(chan error, 1)
	safe.Go(func() {
		result, err := session.Execute(context.Background(), req)
		if result.Metadata["outputBytes"] != int64(len("pending")) {
			done <- errors.New("byte snapshot lost pending output")
			return
		}
		done <- err
	})
	select {
	case err := <-done:
		if !errors.Is(err, failure) || IsCommandExit(err) {
			t.Fatalf("drain failure became command exit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("result snapshot waited for blocked caller callback")
	}
	if _, err := session.Execute(context.Background(), request(session, "next")); err == nil {
		t.Fatal("failed output advanced the session")
	}
	unblock()
	<-writerDone
}

func TestSessionOutputByteSnapshotRacesSafelyWithFailedDrain(t *testing.T) {
	for range 20 {
		started, stop, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
		session := newTestSession(t, &fakeBackend{execute: func(_ context.Context, _ string, _ []string, opts *sandbox.ExecuteOptions) (*sandbox.ExecuteResult, error) {
			safe.Go(func() {
				defer close(done)
				close(started)
				for {
					select {
					case <-stop:
						return
					default:
						_, _ = io.WriteString(opts.Stdout, "x")
					}
				}
			})
			<-started
			return &sandbox.ExecuteResult{ExitCode: 0}, context.DeadlineExceeded
		}})
		result, err := session.Execute(context.Background(), request(session, "command"))
		close(stop)
		<-done
		if !errors.Is(err, context.DeadlineExceeded) || IsCommandExit(err) {
			t.Fatalf("concurrent output failure misclassified: %v", err)
		}
		if _, ok := result.Metadata["outputBytes"].(int64); !ok {
			t.Fatal("output byte snapshot is not an immutable count")
		}
	}
}
