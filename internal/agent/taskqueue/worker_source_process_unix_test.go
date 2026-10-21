//go:build unix

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/stretchr/testify/require"
)

func TestSourceGitCancellationClosesHTTPHelper(t *testing.T) {
	entered, closed := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(closed) }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := handleSourceClone(ctx, &taskpayload.SourcePayload{Type: "git", Repo: server.URL + "/repo"}, t.TempDir())
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP helper did not start")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("Git cancellation did not return")
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Git HTTP descendant retained connection after cancellation")
	}
}

func TestSourceGitCancellationStopsLocalHelperChildren(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("Linux procfs required for descendant liveness assertion")
	}
	repository, _, _ := sourceTestRepository(t, "cancel-helper")
	g, err := newSourceGit(&taskpayload.SourcePayload{Repo: repository}, t.TempDir())
	require.NoError(t, err)
	defer g.close()
	// Controlled test-only replacement of upload-pack to force a child process.
	// Production local transport always overrides this option with system Git.
	g.protocol = ""
	pidfile := filepath.Join(t.TempDir(), "pids")
	helper := filepath.Join(t.TempDir(), "upload-pack")
	require.NoError(t, os.WriteFile(helper, []byte(fmt.Sprintf("#!/bin/sh\n/bin/sleep 60 &\nprintf '%%s %%s' \"$$\" \"$!\" > '%s'\nwait\n", pidfile)), 0700))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := g.run(ctx, "clone", "--no-checkout", "--no-local", "--upload-pack="+helper, "--", repository, ".")
		done <- err
	}()
	var pids []string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		body, _ := os.ReadFile(pidfile)
		pids = strings.Fields(string(body))
		if len(pids) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.Len(t, pids, 2, "controlled helper did not start")
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("local Git cancellation did not return")
	}
	for _, raw := range pids {
		pid, err := strconv.Atoi(raw)
		require.NoError(t, err)
		deadline = time.Now().Add(2 * time.Second)
		stopped := false
		for time.Now().Before(deadline) {
			stopped, err = sourceTestProcessStopped(pid, os.ReadFile)
			require.NoError(t, err)
			if stopped {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		require.True(t, stopped, "descendant %d still executes after cancellation", pid)
	}
}

func sourceTestProcessStopped(pid int, readFile func(string) ([]byte, error)) (bool, error) {
	body, err := readFile(fmt.Sprintf("/proc/%d/stat", pid))
	// procfs returns ESRCH if the task exits after ReadFile opens stat but
	// before it reads; like ENOENT, this means the process is no longer live.
	if os.IsNotExist(err) || errors.Is(err, syscall.ESRCH) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	_, tail, _ := strings.Cut(string(body), ") ")
	return strings.HasPrefix(tail, "Z "), nil
}

func TestSourceTestProcessStopped(t *testing.T) {
	const pid = 12345
	const path = "/proc/12345/stat"
	pathError := func(err error) error { return &os.PathError{Op: "read", Path: path, Err: err} }
	permissionErr := pathError(syscall.EACCES)
	ioErr := pathError(syscall.EIO)
	otherErr := errors.New("no such process")
	for _, tt := range []struct {
		name    string
		body    string
		readErr error
		stopped bool
		wantErr error
	}{
		{name: "missing before open", readErr: &os.PathError{Op: "open", Path: path, Err: syscall.ENOENT}, stopped: true},
		{name: "exits during read", readErr: pathError(syscall.ESRCH), stopped: true},
		{name: "wrapped exit during read", readErr: fmt.Errorf("read stat: %w", pathError(syscall.ESRCH)), stopped: true},
		{name: "zombie", body: "12345 (sleep) Z 1 2 3", stopped: true},
		{name: "running survivor", body: "12345 (sleep) R 1 2 3"},
		{name: "sleeping survivor", body: "12345 (sleep) S 1 2 3"},
		{name: "blocked survivor", body: "12345 (sleep) D 1 2 3"},
		{name: "stopped but living survivor", body: "12345 (sleep) T 1 2 3"},
		{name: "empty read", body: ""},
		{name: "malformed read", body: "not a proc stat"},
		{name: "permission denied", readErr: permissionErr, wantErr: permissionErr},
		{name: "io failure", readErr: ioErr, wantErr: ioErr},
		{name: "partial zombie with io failure", body: "12345 (sleep) Z 1 2 3", readErr: ioErr, wantErr: ioErr},
		{name: "error text is not errno", readErr: otherErr, wantErr: otherErr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			stopped, err := sourceTestProcessStopped(pid, func(gotPath string) ([]byte, error) {
				calls++
				require.Equal(t, path, gotPath)
				return []byte(tt.body), tt.readErr
			})
			require.Equal(t, 1, calls)
			require.Equal(t, tt.wantErr, err)
			require.Equal(t, tt.stopped, stopped)
		})
	}
}
