//go:build sandbox_acceptance

// Copyright 2025 Arcentra Authors.
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

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/namespaces"
	"github.com/google/uuid"
)

// TestContainerdAcceptance 仅在显式选择的具备真实运行时的专用 Linux 主机上执行
// 一旦指定 build tag，缺少许可或能力必须失败，不能用 skip 冒充通过
func TestContainerdAcceptance(t *testing.T) {
	if os.Getenv("ARCENTRA_SANDBOX_ACCEPTANCE") != "1" {
		t.Fatal("set ARCENTRA_SANDBOX_ACCEPTANCE=1 on a dedicated Linux containerd/runc/cgroup-v2 host; acceptance was not run")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("acceptance requires Linux")
	}
	socket := os.Getenv("ARCENTRA_CONTAINERD_SOCKET")
	if socket == "" {
		socket = "/run/containerd/containerd.sock"
	}
	info, err := os.Stat(socket)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("containerd Unix socket is unavailable: %s (%v)", socket, err)
	}
	if _, err := exec.LookPath("runc"); err != nil {
		t.Fatalf("runc must be installed on this host: %v", err)
	}
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		t.Fatalf("acceptance currently requires an accessible cgroup-v2 hierarchy: %v", err)
	}
	image := os.Getenv("ARCENTRA_SANDBOX_TEST_IMAGE")
	if image == "" {
		t.Fatal("set ARCENTRA_SANDBOX_TEST_IMAGE to an operator-approved Alpine-compatible image, preferably pinned by digest")
	}
	namespace := "arcentra-acceptance-" + uuid.NewString()
	sb, err := NewContainerdSandbox(&ContainerdConfig{UnixSocket: socket, Namespace: namespace, DefaultImage: image, NetworkMode: "none"}, log.Logger{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sb.Close(); err != nil {
			t.Errorf("acceptance cleanup failed: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	workspace := t.TempDir()
	if err := os.Chmod(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside-sandbox-marker")
	if err := os.WriteFile(outside, []byte("host-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := sb.Create(ctx, &CreateOptions{Command: []string{"sh", "-c", "trap 'exit 0' TERM INT; while :; do sleep 1; done"}, Env: map[string]string{"BASE": "base", "OVERRIDE": "old"}, WorkingDir: "/workspace", Resources: &Resources{CPU: "500m", Memory: "64Mi"}, Mounts: []Mount{{Source: workspace, Target: "/workspace"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := sb.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	state, _ := sb.getContainer(id)
	task := state.task.(*containerdTask).Task
	verifyCgroupV2Limits(t, task.Pid())
	result, err := sb.Execute(ctx, id, []string{"sh", "-c", `printf '%s|%s|%s|%s:%s' "$BASE" "$OVERRIDE" "$(pwd)" "$(id -u)" "$(id -g)"; printf 'stderr-marker' >&2`}, &ExecuteOptions{Env: map[string]string{"OVERRIDE": "new"}, WorkingDir: "/tmp", User: "1000:1001"})
	if err != nil || result.ExitCode != 0 || result.Stdout != "base|new|/tmp|1000:1001" || result.Stderr != "stderr-marker" {
		t.Fatalf("command/env/cwd/user/stream acceptance failed: %+v %v", result, err)
	}
	result, err = sb.Execute(ctx, id, []string{"sh", "-c", "printf shared-data > result.txt; test ! -e \"$HOST_MARKER\""}, &ExecuteOptions{Env: map[string]string{"HOST_MARKER": outside}})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("workspace/host-path isolation failed: %+v %v", result, err)
	}
	content, err := os.ReadFile(filepath.Join(workspace, "result.txt"))
	if err != nil || string(content) != "shared-data" {
		t.Fatalf("workspace mount failed: %q %v", content, err)
	}
	result, err = sb.Execute(ctx, id, []string{"cat", "/workspace/result.txt"}, nil)
	if err != nil || result.Stdout != "shared-data" {
		t.Fatalf("workspace did not survive steps: %+v %v", result, err)
	}
	for _, kind := range []string{"pid", "net"} {
		host, err := os.Readlink("/proc/self/ns/" + kind)
		if err != nil {
			t.Fatal(err)
		}
		result, err = sb.Execute(ctx, id, []string{"readlink", "/proc/self/ns/" + kind}, nil)
		if err != nil || result.ExitCode != 0 || strings.TrimSpace(result.Stdout) == host {
			t.Fatalf("%s namespace is not distinct: %+v %v", kind, result, err)
		}
	}
	result, err = sb.Execute(ctx, id, []string{"sh", "-c", "ip route show default > /tmp/arcentra-routes && test ! -s /tmp/arcentra-routes"}, nil)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("offline network has a default route: %+v %v", result, err)
	}
	result, err = sb.Execute(ctx, id, []string{"sh", "-c", "exit 23"}, nil)
	if err != nil || result.ExitCode != 23 {
		t.Fatalf("exit status lost: %+v %v", result, err)
	}
	_, err = sb.Execute(ctx, id, []string{"sh", "-c", "sleep 60 & wait"}, &ExecuteOptions{Timeout: 100 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout not propagated: %v", err)
	}
	cancelled, cancelNow := context.WithCancel(ctx)
	cancelNow()
	if err := sb.Remove(cancelled, id); err != nil {
		t.Fatalf("cleanup after canceled context: %v", err)
	}
	if err := sb.Remove(ctx, id); err != nil {
		t.Fatalf("idempotent cleanup: %v", err)
	}
	client := sb.backend.(*containerdBackend).client
	verifyCtx := namespaces.WithNamespace(ctx, namespace)
	if _, err := client.LoadContainer(verifyCtx, id); !errdefs.IsNotFound(err) {
		t.Fatalf("container remains after removal: %v", err)
	}
	if _, err := client.SnapshotService(state.container.(*containerdContainer).snapshotter).Stat(verifyCtx, id+"-snapshot"); !errdefs.IsNotFound(err) {
		t.Fatalf("snapshot remains after removal: %v", err)
	}
	t.Log("Real containerd acceptance passed: execution, output, UID/GID, workspace, private PID/network namespaces, cgroup-v2 limits, timeout and cleanup")
}

func verifyCgroupV2Limits(t *testing.T, pid uint32) {
	t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		t.Fatalf("cannot inspect task cgroup on this host: %v", err)
	}
	var cgroup string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "0::/") {
			cgroup = strings.TrimPrefix(line, "0::")
			break
		}
	}
	if cgroup == "" || filepath.Clean(cgroup) != cgroup || strings.Contains(cgroup, "..") {
		t.Fatalf("unexpected cgroup path: %q", cgroup)
	}
	root := filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(cgroup, "/"))
	for name, want := range map[string]string{"cpu.max": "50000 100000", "memory.max": "67108864"} {
		value, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || strings.Join(strings.Fields(string(value)), " ") != want {
			t.Fatalf("effective %s: got %q, want %q (%v)", name, value, want, err)
		}
	}
}
