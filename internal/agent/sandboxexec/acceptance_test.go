//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandboxexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/sandbox"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/defaults"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/namespaces"
	"github.com/google/uuid"
)

// TestSessionContainerdAcceptance 在明确授权的专用主机验证适配器而非生产 Agent 接线
func TestSessionContainerdAcceptance(t *testing.T) {
	if os.Getenv("ARCENTRA_SANDBOX_SESSION_ACCEPTANCE") != "1" {
		t.Fatal("set ARCENTRA_SANDBOX_SESSION_ACCEPTANCE=1 only on an authorized dedicated containerd host; runtime acceptance was not run")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("acceptance requires Linux")
	}
	socket, image := os.Getenv("ARCENTRA_CONTAINERD_SOCKET"), os.Getenv("ARCENTRA_SANDBOX_TEST_IMAGE")
	if socket == "" {
		socket = "/run/containerd/containerd.sock"
	}
	if image == "" {
		t.Fatal("set ARCENTRA_SANDBOX_TEST_IMAGE to an approved Alpine-compatible image, preferably pinned by digest")
	}
	if info, err := os.Stat(socket); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("containerd socket unavailable: %v", err)
	}
	if _, err := exec.LookPath("runc"); err != nil {
		t.Fatalf("runc unavailable: %v", err)
	}
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		t.Fatalf("cgroup-v2 unavailable: %v", err)
	}
	root, err := os.MkdirTemp("", "arcentra-session-acceptance-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	namespace := "arcentra-session-acceptance-" + uuid.NewString()
	backend, err := sandbox.NewContainerdSandbox(&sandbox.ContainerdConfig{UnixSocket: socket, Namespace: namespace, NetworkMode: "none"}, log.Logger{})
	if err != nil {
		_ = os.Remove(root)
		t.Fatal(err)
	}
	var session *Session
	t.Cleanup(func() {
		var cleanupErr error
		if session != nil {
			cleanupErr = session.Close(context.Background())
		}
		cleanupErr = errors.Join(cleanupErr, backend.Close())
		if cleanupErr != nil {
			t.Errorf("runtime cleanup failed; inspect namespace %s and workspace %s: %v", namespace, root, cleanupErr)
			return
		}
		if err := os.Remove(root); err != nil {
			t.Errorf("remove acceptance root: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	session, err = NewSession(ctx, backend, Options{WorkspaceRoot: root, JobRunID: "acceptance", Image: image, Resources: &sandbox.Resources{CPU: "500m", Memory: "64Mi"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("session namespace=%s container=%s workspace=%s", namespace, session.containerID, session.Workspace())
	manager := executor.NewExecutorManager()
	step := request(session, `printf '%s' "$EXPLICIT" > artifact.txt; printf stream-marker; printf stderr-marker >&2; test -z "$ARCENTRA_SESSION_HOST_ONLY"`)
	step.Env = map[string]string{"EXPLICIT": "prepared-output"}
	t.Setenv("ARCENTRA_SESSION_HOST_ONLY", "must-not-be-inherited")
	var output strings.Builder
	step.OutputHandler = func(_, value string) { output.WriteString(value) }
	result, err := manager.ExecuteWithExecutor(ctx, step, session)
	if err != nil || !result.Success || result.Output != "stream-marker" || result.ErrorOutput != "stderr-marker" || !strings.Contains(output.String(), "stream-marker") {
		t.Fatalf("adapter streaming failed: %+v %v", result, err)
	}
	data, err := os.ReadFile(filepath.Join(session.Workspace(), "artifact.txt"))
	if err != nil || string(data) != "prepared-output" {
		t.Fatalf("host artifact boundary failed: %q %v", data, err)
	}
	result, err = manager.ExecuteWithExecutor(ctx, request(session, "cat artifact.txt"), session)
	if err != nil || result.Output != "prepared-output" {
		t.Fatalf("cross-step workspace failed: %+v %v", result, err)
	}
	result, err = manager.ExecuteWithExecutor(ctx, request(session, "exit 17"), session)
	if err == nil || result.ExitCode != 17 {
		t.Fatalf("nonzero exit lost: %+v %v", result, err)
	}
	if _, err := manager.ExecuteWithExecutor(ctx, request(session, "true"), session); err != nil {
		t.Fatalf("ordinary failure poisoned session: %v", err)
	}
	timed := request(session, "sleep 30")
	timed.Options.Timeout = 50 * time.Millisecond
	if _, err := manager.ExecuteWithExecutor(ctx, timed, session); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout lost: %v", err)
	}
	if _, err := session.Execute(ctx, request(session, "true")); err == nil {
		t.Fatal("timed-out session allowed another step")
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if err := session.Close(canceled); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(canceled); err != nil {
		t.Fatalf("repeated close failed: %v", err)
	}
	if _, err := os.Stat(session.Workspace()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace leaked: %v", err)
	}
	client, err := containerd.New(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	inspect := namespaces.WithNamespace(ctx, namespace)
	if _, err := client.LoadContainer(inspect, session.containerID); !errdefs.IsNotFound(err) {
		t.Fatalf("container leaked or inspection failed: %v", err)
	}
	snapshotter, err := client.GetLabel(inspect, defaults.DefaultSnapshotterNSLabel)
	if err != nil {
		t.Fatal(err)
	}
	if snapshotter == "" {
		snapshotter = containerd.DefaultSnapshotter
	}
	if _, err := client.SnapshotService(snapshotter).Stat(inspect, session.containerID+"-snapshot"); !errdefs.IsNotFound(err) {
		t.Fatalf("snapshot leaked or inspection failed: %v", err)
	}
}
