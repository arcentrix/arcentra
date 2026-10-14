//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/namespaces"
)

// Called only inside the existing, explicitly authorized acceptance test and
// its original three-minute deadline. No separate skip, runner, image or budget.
func verifyOwnedTaskQuiescence(t *testing.T, ctx context.Context, sb *Containerd, user string) {
	t.Helper()
	workspace := t.TempDir()
	if err := os.Chmod(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	newProbe := func() *taskQuiescence {
		q, err := newTaskQuiescence(ctx, sb, CreateOptions{
			User: user, WorkingDir: "/workspace", NetworkMode: "none",
			Resources: &Resources{CPU: "500m", Memory: "64Mi"},
			Mounts:    []Mount{{Source: workspace, Target: "/workspace"}},
		}, pinInitProcess)
		if err != nil {
			t.Fatalf("required owned-task quiescence capability unavailable: %v", err)
		}
		t.Cleanup(func() {
			if err := sb.RemoveOwned(ctx, q.state.ref); err != nil {
				t.Errorf("quiescence cleanup: %v", err)
			}
		})
		return q
	}
	assertAbsent := func(q *taskQuiescence) {
		resources, err := sb.InspectOwned(ctx, q.state.ref)
		if err != nil || resources.ContainerExists || resources.SnapshotExists || resources.TaskExists {
			t.Fatalf("quiescence resources remain: %+v %v", resources, err)
		}
	}
	q := newProbe()
	originalPID := q.pid
	write := `printf 'rootfs' > /tmp/quiescence-rootfs; printf 'tmpfs' > /dev/shm/quiescence-tmpfs; printf 'workspace' > /workspace/quiescence-workspace; readlink /proc/1/ns/pid; exit 23`
	result, err := q.execute(ctx, []string{"sh", "-ec", write}, nil)
	if err != nil || result.ExitCode != 23 || !q.paused || q.invalid {
		t.Fatalf("failed execution did not retain frozen owned task: %+v %v", result, err)
	}
	originalNamespace := strings.TrimSpace(result.Stdout)
	if err = q.verify(namespaces.WithNamespace(ctx, sb.namespace), containerd.Paused, true); err != nil {
		t.Fatal(err)
	}
	read := `test "$(cat /tmp/quiescence-rootfs)" = rootfs; test "$(cat /dev/shm/quiescence-tmpfs)" = tmpfs; test "$(cat /workspace/quiescence-workspace)" = workspace; readlink /proc/1/ns/pid`
	result, err = q.execute(ctx, []string{"sh", "-ec", read}, nil)
	if err != nil || result.ExitCode != 0 || strings.TrimSpace(result.Stdout) != originalNamespace || q.pid != originalPID {
		t.Fatalf("continuation lost init, namespace, rootfs, tmpfs or workspace: %+v %v", result, err)
	}
	if err = q.pin.alive(ctx); err != nil {
		t.Fatalf("continuation replaced the original kernel process: %v", err)
	}

	// Deterministic cancellation already won admission: do not thaw or create
	// a user exec. The marker is in the host-visible workspace for observation.
	result, err = q.execute(ctx, []string{"sh", "-c", "exit 24"}, nil)
	if err != nil || result.ExitCode != 24 || !q.paused {
		t.Fatalf("prepare cancel-wins boundary: %+v %v", result, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = q.execute(canceled, []string{"sh", "-c", "touch /workspace/forbidden-continuation"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel did not win continuation admission: %v", err)
	}
	if _, err = os.Stat(filepath.Join(workspace, "forbidden-continuation")); !os.IsNotExist(err) {
		t.Fatalf("canceled continuation produced a side effect: %v", err)
	}
	assertAbsent(q)

	// Redirect all background IO so exec Delete can complete independently.
	// The child is detached from the exec handle, but remains in its cgroup.
	background := newProbe()
	writer := `(while :; do printf x >> /workspace/quiescence-background; sleep 0.02; done) </dev/null >/dev/null 2>&1 &
while test ! -s /workspace/quiescence-background; do sleep 0.01; done
exit 23`
	result, err = background.execute(ctx, []string{"sh", "-ec", writer}, nil)
	if err == nil || result.ExitCode != 23 || !background.invalid {
		t.Fatalf("detached background descendant admitted continuation: %+v %v", result, err)
	}
	assertAbsent(background)
	before, err := os.ReadFile(filepath.Join(workspace, "quiescence-background"))
	if err != nil || len(before) == 0 {
		t.Fatalf("background child never demonstrably ran: %v", err)
	}
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-timer.C:
	}
	after, err := os.ReadFile(filepath.Join(workspace, "quiescence-background"))
	if err != nil || string(before) != string(after) {
		t.Fatalf("background writer survived owned cleanup: %v", err)
	}
	// Establish an adopted zombie before the failing exec exits. The fixed
	// sleep init deliberately does not reap; cgroup.procs alone omits this PID.
	zombie := newProbe()
	result, err = zombie.execute(ctx, []string{"sh", "-ec", `(sleep 0.05; exit 0) </dev/null >/dev/null 2>&1 & printf '%s' "$!" > /workspace/quiescence-zombie-pid`}, nil)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("create detached zombie fixture: %+v %v", result, err)
	}
	witness := &zombieAcceptancePin{pinnedInit: zombie.pin, task: zombie.state.task, pid: zombie.pid}
	zombie.pin = witness
	observe := `pid=$(cat /workspace/quiescence-zombie-pid)
for attempt in $(seq 1 100); do
  state=$(awk '{print $3}' /proc/$pid/stat)
  if test "$state" = Z; then printf 'verified-zombie:%s:Z' "$pid"; exit 23; fi
  sleep 0.01
done
exit 25`
	result, err = zombie.execute(ctx, []string{"sh", "-ec", observe}, nil)
	if err == nil || result.ExitCode != 23 || !strings.HasPrefix(result.Stdout, "verified-zombie:") || !strings.HasSuffix(result.Stdout, ":Z") || !witness.frozenOnlyInit || !witness.childRejected {
		t.Fatalf("zombie was not independently observed and rejected beyond cgroup enumeration: result=%+v witness=%+v err=%v", result, witness, err)
	}
	assertAbsent(zombie)

	t.Log("Owned-task quiescence capability proven: exact pidfd-pinned init, reaped failed exec, frozen full cgroup, same PID namespace/rootfs/tmpfs/workspace, cancel-wins, detached-child and observed-zombie rejection with verified cleanup; production failed_step remains unavailable")
}

// Records that the frozen runtime PID list had only init, while the separate
// init-children check caught the zombie whose Z state the exec just observed.
type zombieAcceptancePin struct {
	pinnedInit
	task                          runtimeTask
	pid                           uint32
	frozenOnlyInit, childRejected bool
}

func (p *zombieAcceptancePin) childless(ctx context.Context) error {
	status, err := p.task.Status(ctx)
	if err != nil {
		return err
	}
	pids, err := p.task.Pids(ctx)
	if err != nil {
		return err
	}
	p.frozenOnlyInit = status.Status == containerd.Paused && len(pids) == 1 && pids[0].Pid == p.pid
	err = p.pinnedInit.childless(ctx)
	p.childRejected = err != nil && strings.Contains(err.Error(), "pinned init retains child processes")
	return err
}
