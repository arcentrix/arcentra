// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestInitPIDFDTracksOriginalProcessLifetime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Ordinary unprivileged child; no container runtime, daemon or host changes.
	child := exec.CommandContext(ctx, "sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	pin, err := pinInitProcess(ctx, uint32(child.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pin.close() }()
	if err = pin.alive(ctx); err != nil {
		t.Fatal(err)
	}
	// This ordinary test also runs on constrained cloud kernels. Lack of the
	// children file must produce an explicit refusal, never a weaker proof.
	// The separately gated real runtime acceptance requires full visibility.
	_, visibilityErr := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", child.Process.Pid, child.Process.Pid))
	childErr := pin.childless(ctx)
	if (visibilityErr == nil) != (childErr == nil) {
		t.Fatalf("child visibility and proof disagree: visibility=%v proof=%v", visibilityErr, childErr)
	}
	if visibilityErr != nil {
		t.Logf("host cannot support owned-child proof; correctly rejected: %v", childErr)
	}
	if err = child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	if err = pin.alive(ctx); err == nil {
		t.Fatal("pidfd accepted an exited original process")
	}
	if err = pin.close(); err != nil {
		t.Fatal(err)
	}
	if err = pin.alive(ctx); err == nil {
		t.Fatal("closed pidfd accepted")
	}
	canceled, cancelNow := context.WithCancel(ctx)
	cancelNow()
	if _, err = pinInitProcess(canceled, uint32(child.Process.Pid)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled identity acquisition: %v", err)
	}
}

func TestInitChildrenScanCoversEveryThreadAndRejectsZombies(t *testing.T) {
	for _, scenario := range []string{"clean", "main-thread-zombie", "other-thread-child", "missing-children", "too-many-threads"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			threads := 2
			if scenario == "too-many-threads" {
				threads = 129
			}
			for i := 0; i < threads; i++ {
				path := filepath.Join(root, fmt.Sprint(1000+i))
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				if scenario == "missing-children" && i == 1 {
					continue
				}
				children := ""
				if (scenario == "main-thread-zombie" && i == 0) || (scenario == "other-thread-child" && i == 1) {
					children = "2001 "
				}
				if err := os.WriteFile(filepath.Join(path, "children"), []byte(children), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := readInitChildren(context.Background(), root)
			if (err == nil) != (scenario == "clean") {
				t.Fatalf("%s: %v", scenario, err)
			}
		})
	}
}
