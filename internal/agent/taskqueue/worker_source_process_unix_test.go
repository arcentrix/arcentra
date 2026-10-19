//go:build unix

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
			body, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
			if os.IsNotExist(err) {
				stopped = true
				break
			}
			require.NoError(t, err)
			_, tail, _ := strings.Cut(string(body), ") ")
			if strings.HasPrefix(tail, "Z ") {
				stopped = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		require.True(t, stopped, "descendant %d still executes after cancellation", pid)
	}
}
