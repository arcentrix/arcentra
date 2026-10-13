// Copyright 2025 Arcentra Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at https://www.apache.org/licenses/LICENSE-2.0

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/oci"
	"github.com/google/uuid"
)

func TestOwnedResourcesRecoverAfterBackendRestart(t *testing.T) {
	original, b, id := newTestSandbox(t)
	if err := original.Start(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	tracked, _ := original.getContainer(id)
	ref := tracked.ref
	restarted := newSandbox(original.config, b)
	if err := restarted.Remove(context.Background(), id); !errdefs.IsNotFound(err) {
		t.Fatalf("unknown legacy Remove falsely claimed absence: %v", err)
	}
	state, err := restarted.InspectOwned(context.Background(), ref)
	if err != nil || !state.ContainerExists || !state.SnapshotExists || !state.TaskExists {
		t.Fatalf("restart inspection: %+v %v", state, err)
	}
	if err = restarted.RemoveOwned(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	state, err = restarted.InspectOwned(context.Background(), ref)
	if err != nil || state != (ResourceState{}) {
		t.Fatalf("resources survived recovery: %+v %v", state, err)
	}
	if err = restarted.RemoveOwned(context.Background(), ref); err != nil {
		t.Fatalf("exact removal not idempotent: %v", err)
	}
	if b.lookup(id).removed != 1 || b.lookup(id).task.deleted != 1 {
		t.Fatal("restart did not delete task and resources exactly once")
	}
}

func TestOwnedRecoveryRejectsWrongOwnerWithoutPoisoningRetry(t *testing.T) {
	original, b, id := newTestSandbox(t)
	tracked, _ := original.getContainer(id)
	ref := tracked.ref
	restarted := newSandbox(original.config, b)
	wrong := ref
	wrong.Owner = "someone-else"
	if err := restarted.RemoveOwned(context.Background(), wrong); err == nil {
		t.Fatal("wrong ownership accepted")
	}
	if b.lookup(id).removed != 0 || len(restarted.containers) != 0 {
		t.Fatal("failed ownership check mutated resources or recovery map")
	}
	if err := restarted.RemoveOwned(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareAndCreateBindExactIdentity(t *testing.T) {
	sb, b, _ := newTestSandbox(t)
	id := "arcentra-" + uuid.NewString()
	ref, err := sb.PrepareResource(context.Background(), id, "controller-job-claim-digest")
	if err != nil {
		t.Fatal(err)
	}
	created, err := sb.Create(context.Background(), &CreateOptions{Resource: &ref, Command: []string{"sleep", "60"}, Labels: map[string]string{ownerLabel: "forged", snapshotLabel: "other-snapshot"}})
	if err != nil || created != id {
		t.Fatalf("explicit create: %s %v", created, err)
	}
	if err = verifyOwnership(b.lookup(id).labels, ref); err != nil {
		t.Fatal(err)
	}
	if _, err = sb.PrepareResource(context.Background(), "not-a-resource", "owner"); err == nil {
		t.Fatal("invalid explicit ID accepted")
	}
	if _, err = sb.PrepareResource(context.Background(), id, ""); err == nil {
		t.Fatal("empty owner accepted")
	}
	wrong := ref
	wrong.Namespace = "other"
	if _, err = sb.InspectOwned(context.Background(), wrong); err == nil {
		t.Fatal("foreign namespace accepted")
	}
	if err = sb.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSealStopsSuccessfulExecBackgroundChildrenAndRetainsResources(t *testing.T) {
	sb, b, id := newTestSandbox(t)
	if err := sb.Start(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	task := b.lookup(id).task
	task.background = 1
	if result, err := sb.Execute(context.Background(), id, []string{"launch-background"}, nil); err != nil || result.ExitCode != 0 {
		t.Fatalf("successful exec: %+v %v", result, err)
	}
	state, _ := sb.getContainer(id)
	if err := sb.SealOwned(context.Background(), state.ref); err != nil {
		t.Fatal(err)
	}
	if task.background != 0 || !task.killAll || task.deleted != 0 || b.lookup(id).removed != 0 {
		t.Fatal("Seal left descendants or deleted artifact resources")
	}
	if _, err := sb.Execute(context.Background(), id, []string{"true"}, nil); err == nil {
		t.Fatal("sealed container was reused")
	}
	if err := sb.RemoveOwned(context.Background(), state.ref); err != nil {
		t.Fatal(err)
	}
}

func TestStopKillsChildrenAfterInitExited(t *testing.T) {
	sb, b, id := newTestSandbox(t)
	if err := sb.Start(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	task := b.lookup(id).task
	task.background = 1
	task.keepChildrenOnTerm = true
	if err := sb.Stop(context.Background(), id, time.Second); err != nil {
		t.Fatal(err)
	}
	if len(task.signals) != 2 || task.signals[0] != syscall.SIGTERM || task.signals[1] != syscall.SIGKILL || task.background != 0 {
		t.Fatalf("init exit hid descendants: %v", task.signals)
	}
	if err := sb.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSealVerifiesAlreadyStoppedTaskAndFailsClosedOnInspectionError(t *testing.T) {
	sb, b, id := newTestSandbox(t)
	if err := sb.Start(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	task := b.lookup(id).task
	if err := task.fakeProcess.Kill(context.Background(), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	task.background = 1
	state, _ := sb.getContainer(id)
	task.pidsErr = errors.New("cannot inspect cgroup")
	if err := sb.SealOwned(context.Background(), state.ref); err == nil {
		t.Fatal("unverifiable quiescence succeeded")
	}
	if task.deleted != 0 || b.lookup(id).removed != 0 || task.background != 1 {
		t.Fatal("failed seal modified artifact resources")
	}
	task.pidsErr = nil
	if err := sb.SealOwned(context.Background(), state.ref); err != nil {
		t.Fatal(err)
	}
	if task.background != 0 {
		t.Fatal("stopped init caused background processes to be skipped")
	}
	if err := sb.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestQuiescenceWaitIsBounded(t *testing.T) {
	task := &fakeTask{fakeProcess: newFakeProcess(false), pid: 1234, background: 1, stubbornChildren: true}
	task.status = containerd.Stopped
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	if err := stopTask(ctx, task, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unconfirmed quiescence accepted: %v", err)
	}
}

func TestRemoveOwnedAbsentIntentSucceeds(t *testing.T) {
	sb, _, _ := newTestSandbox(t)
	ref, err := sb.PrepareResource(context.Background(), "arcentra-"+uuid.NewString(), "durable-intent")
	if err != nil {
		t.Fatal(err)
	}
	if err = sb.RemoveOwned(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if err = sb.Close(); err != nil {
		t.Fatal(err)
	}
}

type existingResourceBackend struct{ *fakeBackend }

func (b existingResourceBackend) create(context.Context, string, string, []oci.SpecOpts, map[string]string, bool) (runtimeContainer, error) {
	return nil, errdefs.ErrAlreadyExists
}

func TestCreatePreflightConflictCannotDeleteExistingOwnedResource(t *testing.T) {
	original, b, id := newTestSandbox(t)
	tracked, _ := original.getContainer(id)
	ref := tracked.ref
	restarted := newSandbox(original.config, existingResourceBackend{b})
	if _, err := restarted.Create(context.Background(), &CreateOptions{Resource: &ref, Command: []string{"sleep", "60"}}); !errdefs.IsAlreadyExists(err) {
		t.Fatalf("wrong create conflict: %v", err)
	}
	if b.lookup(id).removed != 0 || len(restarted.containers) != 0 {
		t.Fatal("preflight conflict deleted or adopted existing resources")
	}
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStaleRemoveCannotForgetBlockedReplacement(t *testing.T) {
	for _, sameOwner := range []bool{false, true} {
		t.Run(fmt.Sprintf("same-owner-%t", sameOwner), func(t *testing.T) {
			sb, b, id := newTestSandbox(t)
			stale, err := sb.getContainer(id)
			if err != nil {
				t.Fatal(err)
			}
			ref := stale.ref
			if !sameOwner {
				ref.Owner = "replacement-owner"
			}
			if err = sb.Remove(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			b.createHook = func() { close(entered); <-release }
			done := make(chan error, 1)
			safe.Go(func() {
				_, err := sb.Create(context.Background(), &CreateOptions{Resource: &ref, Command: []string{"sleep", "60"}})
				done <- err
			})
			<-entered
			replacement, err := sb.getContainer(id)
			if err != nil || replacement == stale {
				close(release)
				<-done
				t.Fatalf("replacement was not registered: %v", err)
			}
			// 等价于第二个 Remove 已取得旧指针，随后被调度暂停到新 Create 注册之后
			if err = sb.removeState(context.Background(), id, stale); err != nil {
				close(release)
				<-done
				t.Fatal(err)
			}
			current, lookupErr := sb.getContainer(id)
			close(release)
			createErr := <-done
			if lookupErr != nil || current != replacement {
				t.Fatalf("stale Remove forgot replacement: %v", lookupErr)
			}
			if createErr != nil {
				t.Fatal(createErr)
			}
			if err = sb.Start(context.Background(), id); err != nil {
				t.Fatalf("replacement could not start: %v", err)
			}
			if err = sb.Close(); err != nil {
				t.Fatal(err)
			}
			if b.lookup(id).removed != 1 {
				t.Fatal("replacement was left untracked")
			}
		})
	}
}

func TestStaleLifecycleCannotMutateLiveSameOwnerReplacement(t *testing.T) {
	sb, b, id := newTestSandbox(t)
	stale, err := sb.getContainer(id)
	if err != nil {
		t.Fatal(err)
	}
	ref := stale.ref
	if err = sb.Remove(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err = sb.Create(context.Background(), &CreateOptions{Resource: &ref, Command: []string{"sleep", "60"}}); err != nil {
		t.Fatal(err)
	}
	if err = sb.Start(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	replacement, err := sb.getContainer(id)
	if err != nil {
		t.Fatal(err)
	}
	if err = sb.removeState(context.Background(), id, stale); err != nil {
		t.Fatal(err)
	}
	if err = sb.sealState(context.Background(), stale); !errdefs.IsNotFound(err) {
		t.Fatalf("stale Seal reported success: %v", err)
	}
	if current, err := sb.getContainer(id); err != nil || current != replacement {
		t.Fatalf("replacement map entry changed: %v", err)
	}
	task := b.lookup(id).task
	if task.status != containerd.Running || task.deleted != 0 || len(task.signals) != 0 || b.lookup(id).removed != 0 {
		t.Fatal("stale lifecycle action killed replacement")
	}
	if err = sb.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestForgetRemovedPreservesNewGenerationAndTombstoneState(t *testing.T) {
	sb, b, id := newTestSandbox(t)
	stale, err := sb.getContainer(id)
	if err != nil {
		t.Fatal(err)
	}
	if err = sb.Remove(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	ref := stale.ref
	ref.Owner = "new-generation"
	if _, err = sb.Create(context.Background(), &CreateOptions{Resource: &ref, Command: []string{"sleep", "60"}}); err != nil {
		t.Fatal(err)
	}
	replacement, err := sb.getContainer(id)
	if err != nil {
		t.Fatal(err)
	}
	sb.forgetRemoved(id, stale)
	if current, err := sb.getContainer(id); err != nil || current != replacement {
		t.Fatalf("stale forget deleted new generation: %v", err)
	}
	if _, removed := sb.removedIDs[id]; removed {
		t.Fatal("stale forget tombstoned live replacement")
	}
	if b.lookup(id).removed != 0 {
		t.Fatal("replacement was removed")
	}
	if err = sb.Close(); err != nil {
		t.Fatal(err)
	}
}
