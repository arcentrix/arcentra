// Copyright 2025 Arcentra Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at https://www.apache.org/licenses/LICENSE-2.0

package sandbox

import (
	"context"
	"errors"
	"testing"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/snapshots"
)

func recoveryAdapterFixture(t *testing.T, task fixtureTasks) (*containerdBackend, *fixtureContainerStore, *fixtureSnapshotter, ResourceRef, context.Context) {
	t.Helper()
	ref := ResourceRef{ID: "arcentra-11111111-1111-4111-8111-111111111111", Owner: "job-claim-owner", Namespace: "arcentra-test", Snapshotter: "fixture"}
	store := &fixtureContainerStore{records: map[string]containers.Container{ref.ID: {ID: ref.ID, Snapshotter: ref.Snapshotter, SnapshotKey: ref.ID + "-snapshot", Labels: ownershipLabels(ref)}}}
	snaps := &fixtureSnapshotter{prepared: map[string]bool{ref.ID + "-snapshot": true}, labels: map[string]map[string]string{ref.ID + "-snapshot": ownershipLabels(ref)}}
	client, err := containerd.New("", containerd.WithServices(containerd.WithContainerStore(store), containerd.WithSnapshotters(map[string]snapshots.Snapshotter{ref.Snapshotter: snaps}), containerd.WithTaskClient(task)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return &containerdBackend{client: client}, store, snaps, ref, namespaces.WithNamespace(context.Background(), ref.Namespace)
}

func TestCachedContainerMetadataNotFoundStillRemovesOwnedSnapshot(t *testing.T) {
	backend, store, snaps, ref, ctx := recoveryAdapterFixture(t, fixtureTasks{})
	cached, err := backend.client.LoadContainer(ctx, ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	handle := &containerdContainer{container: cached, client: backend.client, id: ref.ID, snapshotter: ref.Snapshotter, ref: ref}
	delete(store.records, ref.ID)
	if err = handle.remove(ctx); err != nil {
		t.Fatal(err)
	}
	if snaps.prepared[ref.ID+"-snapshot"] {
		t.Fatal("cached metadata NotFound leaked snapshot")
	}
	if err = handle.remove(ctx); err != nil {
		t.Fatalf("snapshot-only cleanup not idempotent: %v", err)
	}
}

func TestAdapterValidatesEveryOwnershipLabelBeforeDeletion(t *testing.T) {
	for _, resource := range []string{"container", "snapshot"} {
		for _, label := range []string{"io.arcentra.sandbox", ownerLabel, resourceLabel, namespaceLabel, snapshotterLabel, snapshotLabel} {
			t.Run(resource+"/"+label, func(t *testing.T) {
				backend, store, snaps, ref, ctx := recoveryAdapterFixture(t, fixtureTasks{})
				if resource == "container" {
					store.records[ref.ID].Labels[label] = "unrelated"
				} else {
					snaps.labels[ref.ID+"-snapshot"][label] = "unrelated"
				}
				handle := &containerdContainer{client: backend.client, id: ref.ID, snapshotter: ref.Snapshotter, ref: ref}
				if err := handle.remove(ctx); err == nil {
					t.Fatal("ownership mismatch accepted")
				}
				if !snaps.prepared[ref.ID+"-snapshot"] || len(store.records) != 1 {
					t.Fatal("ownership mismatch deleted a resource")
				}
			})
		}
	}
}

func TestAdapterSnapshotIdentityMismatchFailsClosed(t *testing.T) {
	backend, store, snaps, ref, ctx := recoveryAdapterFixture(t, fixtureTasks{})
	record := store.records[ref.ID]
	record.SnapshotKey = "another-snapshot"
	store.records[ref.ID] = record
	if _, _, err := backend.inspectOwned(ctx, ref); err == nil {
		t.Fatal("metadata linked to a different snapshot was accepted")
	}
	if !snaps.prepared[ref.ID+"-snapshot"] {
		t.Fatal("inspection mutated snapshot")
	}
}

func TestOrphanTaskWithoutMetadataCannotAuthorizeSnapshotRemoval(t *testing.T) {
	backend, store, snaps, ref, ctx := recoveryAdapterFixture(t, fixtureTasks{exists: true})
	delete(store.records, ref.ID)
	handle := &containerdContainer{client: backend.client, id: ref.ID, snapshotter: ref.Snapshotter, ref: ref}
	if err := handle.remove(ctx); err == nil {
		t.Fatal("unverifiable orphan task was ignored")
	}
	if !snaps.prepared[ref.ID+"-snapshot"] {
		t.Fatal("snapshot removed underneath unverified task")
	}
}

func TestSnapshotInspectionAndDeleteFailuresPreserveRetryState(t *testing.T) {
	backend, store, snaps, ref, ctx := recoveryAdapterFixture(t, fixtureTasks{})
	handle := &containerdContainer{client: backend.client, id: ref.ID, snapshotter: ref.Snapshotter, ref: ref}
	snaps.statErr = errors.New("snapshot inspection unavailable")
	if err := handle.remove(ctx); err == nil {
		t.Fatal("snapshot verification failure was swallowed")
	}
	snaps.statErr = nil
	snaps.removeErr = errors.New("snapshot busy")
	if err := handle.remove(ctx); err == nil {
		t.Fatal("snapshot deletion failure was swallowed")
	}
	if len(store.records) != 1 || !snaps.prepared[ref.ID+"-snapshot"] {
		t.Fatal("failed deletion lost identity metadata")
	}
	snaps.removeErr = nil
	if err := handle.remove(ctx); err != nil {
		t.Fatal(err)
	}
	if len(store.records) != 0 || snaps.prepared[ref.ID+"-snapshot"] {
		t.Fatal("retry left resources")
	}
}

func TestSnapshotOnlyRecoveryRejectsForeignSnapshot(t *testing.T) {
	backend, store, snaps, ref, ctx := recoveryAdapterFixture(t, fixtureTasks{})
	delete(store.records, ref.ID)
	snaps.labels[ref.ID+"-snapshot"][ownerLabel] = "foreign-owner"
	handle := &containerdContainer{client: backend.client, id: ref.ID, snapshotter: ref.Snapshotter, ref: ref}
	if err := handle.remove(ctx); err == nil {
		t.Fatal("foreign snapshot-only orphan accepted")
	}
	if !snaps.prepared[ref.ID+"-snapshot"] {
		t.Fatal("foreign snapshot removed")
	}
}
