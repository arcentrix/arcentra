// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package bootstrap

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/router"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/require"
)

type testTerminalLifecycle struct {
	start func(context.Context) error
	stop  func(context.Context) error
}

func (l testTerminalLifecycle) StartTerminalRerun(ctx context.Context) error { return l.start(ctx) }
func (l testTerminalLifecycle) StopTerminalRerun(ctx context.Context) error  { return l.stop(ctx) }

func TestTerminalRerunEnabledMissingServicesFailsBeforeRouter(t *testing.T) {
	conf := &config.AppConfig{}
	conf.Copilot.Execution.TerminalRerunEnabled = true
	for _, r := range []*router.Router{nil, {}} {
		app, cleanup, err := NewApp(r, nil, nil, nil, nil, nil, conf, nil, nil, nil, nil)
		require.Error(t, err)
		require.Nil(t, app)
		require.Nil(t, cleanup)
	}
}
func TestTerminalRerunStartupFailurePrecedesListeners(t *testing.T) {
	app := testController(t, true)
	var listened, cleaned atomic.Bool
	app.HTTPApp.Hooks().OnListen(func(fiber.ListenData) error { listened.Store(true); return nil })
	sentinel := errors.New("bounded startup failure")
	lifecycle := testTerminalLifecycle{start: func(context.Context) error { return sentinel }, stop: func(context.Context) error { return nil }}
	err := runContextWithTerminalRerun(context.Background(), app, func() { cleaned.Store(true) }, time.Second, lifecycle)
	require.ErrorIs(t, err, sentinel)
	require.False(t, listened.Load())
	require.False(t, app.GrpcServer.Ready())
	require.True(t, cleaned.Load())
}
func TestTerminalRerunJoinTimeoutClosesAdmissionAndPreservesDependencies(t *testing.T) {
	app := testController(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	address := make(chan string, 1)
	app.HTTPApp.Hooks().OnListen(func(d fiber.ListenData) error { address <- net.JoinHostPort(d.Host, d.Port); return nil })
	joinEntered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	var cleaned atomic.Bool
	lifecycle := testTerminalLifecycle{start: func(context.Context) error { return nil }, stop: func(context.Context) error { close(joinEntered); <-release; return nil }}
	done := make(chan error, 1)
	go func() {
		done <- runContextWithTerminalRerun(ctx, app, func() { cleaned.Store(true) }, 150*time.Millisecond, lifecycle)
	}()
	var addr string
	select {
	case addr = <-address:
	case <-time.After(time.Second):
		t.Fatal("listener not started")
	}
	cancel()
	select {
	case <-joinEntered:
	case <-time.After(time.Second):
		t.Fatal("join not entered")
	}
	require.True(t, app.ShutdownMgr.IsShuttingDown())
	conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
	if conn != nil {
		_ = conn.Close()
	}
	require.Error(t, err, "new listener admission must close before blocked join")
	select {
	case err = <-done:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("shutdown exceeded bound")
	}
	require.False(t, cleaned.Load(), "dependencies released before unconfirmed join")
}
