// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestCopilotHistoryMaintenanceLifecycle(t *testing.T) {
	var absent *CopilotHistoryMaintenanceService
	absent.Start(context.Background())
	absent.Stop()
	if !errors.Is(absent.Tick(context.Background()), ErrCopilotHistoryUnavailable) {
		t.Fatal("nil maintenance must be unavailable")
	}
	s := NewCopilotHistoryMaintenanceService(nil)
	s.Start(nil)
	if s.done != nil {
		t.Fatal("nil context started maintenance")
	}
	// Cancellation prevents diagnostic noise and proves Start/Stop can join an
	// interrupted worker without any model, SCM or execution dependency.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for round := 0; round < 3; round++ {
		s.Start(ctx)
		first := s.done
		s.Start(ctx)
		if first == nil || s.done != first {
			t.Fatal("duplicate Start replaced the worker")
		}
		var callers sync.WaitGroup
		for n := 0; n < 8; n++ {
			callers.Add(1)
			go func() { defer callers.Done(); s.Stop() }()
		}
		finished := make(chan struct{})
		go func() { callers.Wait(); close(finished) }()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Fatal("maintenance stop failed to join")
		}
		s.mu.Lock()
		pending := s.done != nil || s.cancel != nil
		s.mu.Unlock()
		if pending {
			t.Fatal("joined worker retained lifecycle state")
		}
	}
}
