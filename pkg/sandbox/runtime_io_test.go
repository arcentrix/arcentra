// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/containers"
	"github.com/opencontainers/runtime-spec/specs-go"
)

func runtimeIOTestRef() ResourceRef {
	return ResourceRef{ID: "arcentra-11111111-1111-4111-8111-111111111111", Owner: "job-claim-owner", Namespace: "arcentra-test", Snapshotter: "fixture"}
}

func runtimeIOTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

func allocateRuntimeIO(t *testing.T, root string, ref ResourceRef) cio.Config {
	t.Helper()
	stream, err := newRuntimeIO(context.Background(), root, ref, nil, io.Discard, io.Discard)(ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	config := stream.Config()
	stream.Cancel()
	stream.Wait()
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	return config
}

func assertRuntimeIOExists(t *testing.T, config cio.Config) {
	t.Helper()
	for _, path := range []string{config.Stdout, config.Stderr} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
			t.Fatalf("expected retained named pipe %q: %v", path, err)
		}
	}
}

func assertRuntimeIOEmpty(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("IO resources leaked: %v %v", entries, err)
	}
}

func TestRuntimeIOUsesDurablePrivateDirectoryForEachCreator(t *testing.T) {
	root, ref := runtimeIOTestRoot(t), runtimeIOTestRef()
	name, _, err := runtimeIOIdentity(ref)
	if err != nil {
		t.Fatal(err)
	}
	var previous string
	for range 2 {
		config := allocateRuntimeIO(t, root, ref)
		for _, path := range []string{config.Stdout, config.Stderr} {
			if !strings.HasPrefix(path, filepath.Join(root, name)+string(filepath.Separator)) {
				t.Fatalf("IO escaped exact-owned private root: %q", path)
			}
		}
		if previous == config.Stdout {
			t.Fatal("creators shared FIFO paths")
		}
		previous = config.Stdout
		// Close cannot prove a failed RPC did not create a process.
		assertRuntimeIOExists(t, config)
	}
	if err := removeRuntimeIO(context.Background(), root, ref); err != nil {
		t.Fatal(err)
	}
	if err := removeRuntimeIO(context.Background(), root, ref); err != nil {
		t.Fatalf("repeated cleanup: %v", err)
	}
	assertRuntimeIOEmpty(t, root)
}

func TestRuntimeIOCrashHelper(t *testing.T) {
	if os.Getenv("ARCENTRA_FIFO_CRASH_HELPER") != "1" {
		return
	}
	root := os.Getenv("ARCENTRA_FIFO_CRASH_ROOT")
	for _, id := range []string{"init", "orphan-exec"} {
		if _, err := newRuntimeIO(context.Background(), root, runtimeIOTestRef(), nil, io.Discard, io.Discard)(id); err != nil {
			t.Fatal(err)
		}
	}
	// Deliberately lose every IO handle without Cancel/Wait/Close.
	os.Exit(0)
}

func TestRuntimeIORecoversAfterClientCrashAndRuntimeMetadataDeletion(t *testing.T) {
	root := runtimeIOTestRoot(t)
	command := exec.Command(os.Args[0], "-test.run=^TestRuntimeIOCrashHelper$")
	command.Env = append(os.Environ(), "ARCENTRA_FIFO_CRASH_HELPER=1", "ARCENTRA_FIFO_CRASH_ROOT="+root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("crash fixture: %v: %s", err, output)
	}
	backend, store, snaps, ref, ctx := recoveryAdapterFixture(t, fixtureTasks{})
	backend.fifoDir = root
	// This is the crash window after metadata/snapshot deletion but before IO
	// cleanup. A completely new sandbox has no remembered IO handle.
	delete(store.records, ref.ID)
	delete(snaps.prepared, ref.ID+"-snapshot")
	cfg, err := normalizeConfig(&ContainerdConfig{Namespace: ref.Namespace, DefaultImage: "fixture:local", FIFODir: root})
	if err != nil {
		t.Fatal(err)
	}
	restarted := newSandbox(cfg, backend)
	if err := restarted.RemoveOwned(ctx, ref); err != nil {
		t.Fatal(err)
	}
	assertRuntimeIOEmpty(t, root)
	if err := restarted.RemoveOwned(ctx, ref); err != nil {
		t.Fatalf("repeated recovered removal: %v", err)
	}
}

func TestRuntimeIOPreservesForeignOrUnverifiedRuntime(t *testing.T) {
	for _, kind := range []string{"live-task", "foreign-container", "foreign-snapshot", "inspection-error", "snapshot-delete-error"} {
		t.Run(kind, func(t *testing.T) {
			backend, store, snaps, ref, ctx := recoveryAdapterFixture(t, fixtureTasks{exists: kind == "live-task"})
			root := runtimeIOTestRoot(t)
			config := allocateRuntimeIO(t, root, ref)
			backend.fifoDir = root
			switch kind {
			case "foreign-container":
				store.records[ref.ID].Labels[ownerLabel] = "another-owner"
			case "foreign-snapshot":
				snaps.labels[ref.ID+"-snapshot"][ownerLabel] = "another-owner"
			case "inspection-error":
				snaps.statErr = errors.New("unavailable")
			case "snapshot-delete-error":
				snaps.removeErr = errors.New("busy")
			}
			handle := &containerdContainer{client: backend.client, id: ref.ID, snapshotter: ref.Snapshotter, ref: ref, fifoDir: root}
			if err := handle.remove(ctx); err == nil {
				t.Fatal("unverified cleanup succeeded")
			}
			assertRuntimeIOExists(t, config)
		})
	}
}

func TestRuntimeIOCleanupPreservesCorruptionWithoutPartialDeletion(t *testing.T) {
	for _, kind := range []string{"marker-missing", "marker-malformed", "marker-foreign", "marker-oversized", "marker-symlink", "marker-hardlink", "marker-mode", "marker-fifo", "directory-symlink", "directory-mode", "child-directory-symlink", "stream-symlink", "stream-regular", "stream-hardlink", "unknown-entry"} {
		t.Run(kind, func(t *testing.T) {
			root, ref := runtimeIOTestRoot(t), runtimeIOTestRef()
			config := allocateRuntimeIO(t, root, ref)
			second := allocateRuntimeIO(t, root, ref)
			name, data, _ := runtimeIOIdentity(ref)
			marker := filepath.Join(root, name+".owner")
			resourceDir := filepath.Join(root, name)
			outside := filepath.Join(runtimeIOTestRoot(t), "preserved")
			if err := os.WriteFile(outside, []byte("foreign data"), 0600); err != nil {
				t.Fatal(err)
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "marker-missing":
				must(os.Remove(marker))
			case "marker-malformed":
				must(os.WriteFile(marker, []byte("{"), 0600))
			case "marker-foreign":
				other := ref
				other.Owner = "foreign"
				_, foreign, _ := runtimeIOIdentity(other)
				must(os.WriteFile(marker, foreign, 0600))
			case "marker-oversized":
				must(os.WriteFile(marker, append(data, make([]byte, maxRuntimeIOMarkerBytes)...), 0600))
			case "marker-symlink":
				must(os.Remove(marker))
				must(os.Symlink(outside, marker))
			case "marker-hardlink":
				must(os.Link(marker, outside+"-link"))
			case "marker-mode":
				must(os.Chmod(marker, 0644))
			case "marker-fifo":
				must(os.Remove(marker))
				must(syscall.Mkfifo(marker, 0600))
			case "directory-symlink":
				must(os.Rename(resourceDir, resourceDir+"-saved"))
				must(os.Symlink(resourceDir+"-saved", resourceDir))
			case "directory-mode":
				must(os.Chmod(resourceDir, 0755))
			case "child-directory-symlink":
				child := filepath.Dir(config.Stdout)
				must(os.Rename(child, filepath.Join(filepath.Dir(outside), "saved-child")))
				must(os.Symlink(filepath.Base(filepath.Dir(second.Stdout)), child))
			case "stream-symlink":
				must(os.Remove(config.Stdout))
				must(os.Symlink(outside, config.Stdout))
			case "stream-regular":
				must(os.Remove(config.Stdout))
				must(os.WriteFile(config.Stdout, []byte("foreign data"), 0700))
			case "stream-hardlink":
				must(os.Link(config.Stdout, outside+"-link"))
			case "unknown-entry":
				must(os.WriteFile(filepath.Join(resourceDir, "foreign"), []byte("foreign data"), 0600))
			}
			if err := removeRuntimeIO(context.Background(), root, ref); err == nil {
				t.Fatal("corrupted FIFO state accepted")
			}
			assertRuntimeIOExists(t, second)
			if bytes, err := os.ReadFile(outside); err != nil || string(bytes) != "foreign data" {
				t.Fatal("foreign target changed")
			}
			if _, err := newRuntimeIO(context.Background(), root, ref, nil, io.Discard, io.Discard)(ref.ID); err == nil {
				t.Fatal("creation adopted corrupted existing state")
			}
		})
	}
}

func TestRuntimeIOInterruptedAllocationAndCleanupRemainRecoverable(t *testing.T) {
	for _, kind := range []string{"marker-only", "empty-resource", "empty-io", "partial-io"} {
		t.Run(kind, func(t *testing.T) {
			root, ref := runtimeIOTestRoot(t), runtimeIOTestRef()
			name, marker, _ := runtimeIOIdentity(ref)
			if err := os.WriteFile(filepath.Join(root, name+".owner"), marker, 0600); err != nil {
				t.Fatal(err)
			}
			if kind != "marker-only" {
				if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "empty-io" || kind == "partial-io" {
				child := filepath.Join(root, name, "io-"+strings.Repeat("1", 32))
				if err := os.Mkdir(child, 0700); err != nil {
					t.Fatal(err)
				}
				if kind == "partial-io" {
					if err := syscall.Mkfifo(filepath.Join(child, "stdout"), 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := removeRuntimeIO(context.Background(), root, ref); err != nil {
				t.Fatal(err)
			}
			assertRuntimeIOEmpty(t, root)
		})
	}
}

func TestRuntimeIOAllIdentityFieldsSeparateReusedResourceID(t *testing.T) {
	for _, field := range []string{"id", "owner", "namespace", "snapshotter"} {
		t.Run(field, func(t *testing.T) {
			root, old := runtimeIOTestRoot(t), runtimeIOTestRef()
			fresh := old
			switch field {
			case "id":
				fresh.ID = "arcentra-22222222-2222-4222-8222-222222222222"
			case "owner":
				fresh.Owner = "new-generation"
			case "namespace":
				fresh.Namespace = "new-namespace"
			case "snapshotter":
				fresh.Snapshotter = "new-snapshotter"
			}
			oldConfig := allocateRuntimeIO(t, root, old)
			freshConfig := allocateRuntimeIO(t, root, fresh)
			if err := removeRuntimeIO(context.Background(), root, old); err != nil {
				t.Fatal(err)
			}
			assertRuntimeIOExists(t, freshConfig)
			if _, err := os.Lstat(oldConfig.Stdout); !os.IsNotExist(err) {
				t.Fatal("old generation remains")
			}
			oldConfig = allocateRuntimeIO(t, root, old)
			if err := removeRuntimeIO(context.Background(), root, fresh); err != nil {
				t.Fatal(err)
			}
			assertRuntimeIOExists(t, oldConfig)
		})
	}
}

func TestRuntimeIOCleanupErrorRetainsRemoveOwnedRetryState(t *testing.T) {
	backend, store, snaps, ref, ctx := recoveryAdapterFixture(t, fixtureTasks{})
	root := runtimeIOTestRoot(t)
	config := allocateRuntimeIO(t, root, ref)
	backend.fifoDir = root
	delete(store.records, ref.ID)
	delete(snaps.prepared, ref.ID+"-snapshot")
	name, _, _ := runtimeIOIdentity(ref)
	foreign := filepath.Join(root, name, "unexpected")
	if err := os.WriteFile(foreign, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _ := normalizeConfig(&ContainerdConfig{Namespace: ref.Namespace, DefaultImage: "fixture:local", FIFODir: root})
	restarted := newSandbox(cfg, backend)
	if err := restarted.RemoveOwned(ctx, ref); err == nil {
		t.Fatal("cleanup failure lost")
	}
	state := restarted.containers[ref.ID]
	if state == nil || state.removed {
		t.Fatal("cleanup failure lost retry identity")
	}
	assertRuntimeIOExists(t, config)
	if err := os.Remove(foreign); err != nil {
		t.Fatal(err)
	}
	if err := restarted.RemoveOwned(ctx, ref); err != nil {
		t.Fatal(err)
	}
	assertRuntimeIOEmpty(t, root)
}

func TestRuntimeIOOldHandleCannotDeleteNewRuntimeGeneration(t *testing.T) {
	backend, store, snaps, old, ctx := recoveryAdapterFixture(t, fixtureTasks{})
	root := runtimeIOTestRoot(t)
	fresh := old
	fresh.Owner = "new-generation"
	oldConfig, freshConfig := allocateRuntimeIO(t, root, old), allocateRuntimeIO(t, root, fresh)
	store.records[old.ID] = containers.Container{ID: fresh.ID, Snapshotter: fresh.Snapshotter, SnapshotKey: fresh.ID + "-snapshot", Labels: ownershipLabels(fresh)}
	snaps.labels[old.ID+"-snapshot"] = ownershipLabels(fresh)
	handle := &containerdContainer{client: backend.client, ref: old, id: old.ID, fifoDir: root}
	if err := handle.remove(ctx); err == nil {
		t.Fatal("old runtime generation was allowed to delete new")
	}
	assertRuntimeIOExists(t, oldConfig)
	assertRuntimeIOExists(t, freshConfig)
	handle.ref = fresh
	if err := handle.remove(ctx); err != nil {
		t.Fatal(err)
	}
	assertRuntimeIOExists(t, oldConfig)
	if _, err := os.Lstat(freshConfig.Stdout); !os.IsNotExist(err) {
		t.Fatal("fresh generation remains")
	}
}

func TestRuntimeIOLegacyCleanupDoesNotTouchAnyRoot(t *testing.T) {
	root := runtimeIOTestRoot(t)
	config := allocateRuntimeIO(t, root, runtimeIOTestRef())
	if err := removeRuntimeIO(context.Background(), "", ResourceRef{}); err != nil {
		t.Fatal(err)
	}
	assertRuntimeIOExists(t, config)
}

type runtimeIOCaptureTask struct {
	containerd.Task
	configs []cio.Config
}

func (task *runtimeIOCaptureTask) Exec(_ context.Context, id string, _ *specs.Process, creator cio.Creator) (containerd.Process, error) {
	stream, err := creator(id)
	if err != nil {
		return nil, err
	}
	task.configs = append(task.configs, stream.Config())
	stream.Cancel()
	stream.Wait()
	_ = stream.Close()
	return nil, nil
}

type runtimeIOCaptureContainer struct {
	containerd.Container
	task *runtimeIOCaptureTask
}

func (c *runtimeIOCaptureContainer) NewTask(ctx context.Context, creator cio.Creator, _ ...containerd.NewTaskOpts) (containerd.Task, error) {
	_, err := c.task.Exec(ctx, "init", nil, creator)
	return c.task, err
}
func (c *runtimeIOCaptureContainer) Task(_ context.Context, attach cio.Attach) (containerd.Task, error) {
	if attach != nil {
		return nil, errors.New("recovery must not rely on an IO attachment")
	}
	return c.task, nil
}

func TestRuntimeIOPropagatesExactIdentityIntoNewAndRecoveredExec(t *testing.T) {
	root, ref := runtimeIOTestRoot(t), runtimeIOTestRef()
	capture := &runtimeIOCaptureTask{}
	handle := &containerdContainer{container: &runtimeIOCaptureContainer{task: capture}, ref: ref, id: ref.ID, fifoDir: root}
	task, err := handle.newTask(context.Background(), io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = task.exec(context.Background(), "new-exec", nil, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	recovered, err := handle.loadTask(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = recovered.exec(context.Background(), "recovered-exec", nil, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	name, _, _ := runtimeIOIdentity(ref)
	if len(capture.configs) != 3 {
		t.Fatalf("wrong IO allocation count: %d", len(capture.configs))
	}
	for _, config := range capture.configs {
		if !strings.HasPrefix(config.Stdout, filepath.Join(root, name)+"/") {
			t.Fatalf("wrong resource directory: %s", config.Stdout)
		}
		assertRuntimeIOExists(t, config)
	}
}

func TestRuntimeIOCleanupIsBoundedAndPreservesUnrelatedRootEntries(t *testing.T) {
	root, ref := runtimeIOTestRoot(t), runtimeIOTestRef()
	config := allocateRuntimeIO(t, root, ref)
	name, _, _ := runtimeIOIdentity(ref)
	for i := 0; i < maxRuntimeIOSets; i++ {
		child := filepath.Join(root, name, fmt.Sprintf("io-%032x", i))
		if err := os.Mkdir(child, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := removeRuntimeIO(context.Background(), root, ref); err == nil {
		t.Fatal("unbounded cleanup accepted")
	}
	assertRuntimeIOExists(t, config)
	if _, err := newRuntimeIO(context.Background(), root, ref, nil, io.Discard, io.Discard)(ref.ID); err == nil {
		t.Fatal("unbounded allocation accepted")
	}
	// Removing the test's extra children returns the resource to its valid bound.
	for i := 0; i < maxRuntimeIOSets; i++ {
		if err := os.Remove(filepath.Join(root, name, fmt.Sprintf("io-%032x", i))); err != nil {
			t.Fatal(err)
		}
	}
	foreign := filepath.Join(root, "foreign-root-data")
	if err := os.WriteFile(foreign, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := removeRuntimeIO(context.Background(), root, ref); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(foreign); err != nil || string(data) != "preserve" {
		t.Fatal("unrelated root entry was touched")
	}
}

func TestRuntimeIOCancellationAndMarkerLockPreserveState(t *testing.T) {
	root, ref := runtimeIOTestRoot(t), runtimeIOTestRef()
	config := allocateRuntimeIO(t, root, ref)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := removeRuntimeIO(ctx, root, ref); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	if _, err := newRuntimeIO(ctx, root, ref, nil, io.Discard, io.Discard)(ref.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost allocation cancellation: %v", err)
	}
	name, data, _ := runtimeIOIdentity(ref)
	opened, err := openRuntimeIORoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	marker, err := openRuntimeIOMarker(opened, name, data, false)
	if err != nil {
		t.Fatal(err)
	}
	defer marker.Close()
	if err := removeRuntimeIO(context.Background(), root, ref); err == nil {
		t.Fatal("cleanup ignored concurrent resource lock")
	}
	if _, err := newRuntimeIO(context.Background(), root, ref, nil, io.Discard, io.Discard)(ref.ID); err == nil {
		t.Fatal("allocation ignored concurrent resource lock")
	}
	assertRuntimeIOExists(t, config)
}

func TestRuntimeIOFailsClosedWithoutChangingPrivateRoot(t *testing.T) {
	root, ref := runtimeIOTestRoot(t), runtimeIOTestRef()
	missing := filepath.Join(root, "missing")
	link := filepath.Join(root, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"relative", "/", root + "/", missing, link} {
		if _, err := newRuntimeIO(context.Background(), dir, ref, nil, io.Discard, io.Discard)(ref.ID); err == nil {
			t.Fatalf("unsafe directory accepted: %q", dir)
		}
		if err := removeRuntimeIO(context.Background(), dir, ref); err == nil {
			t.Fatalf("unsafe cleanup directory accepted: %q", dir)
		}
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("missing directory was created")
	}
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := newRuntimeIO(context.Background(), root, ref, nil, io.Discard, io.Discard)(ref.ID); err == nil {
		t.Fatal("nonprivate directory accepted")
	}
	if err := removeRuntimeIO(context.Background(), root, ref); err == nil {
		t.Fatal("nonprivate cleanup directory accepted")
	}
	info, _ := os.Stat(root)
	if info.Mode().Perm() != 0755 {
		t.Fatal("existing permissions silently changed")
	}
}
