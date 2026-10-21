// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandboxexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/arcentrix/arcentra/pkg/sandbox"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

type durableFake struct {
	fakeBackend
	mu           sync.Mutex
	refs         map[string]sandbox.ResourceRef
	states       map[string]sandbox.ResourceState
	events       []string
	beforeCreate func(context.Context, *sandbox.CreateOptions) error
	seal         func(context.Context, sandbox.ResourceRef) error
	removeOwned  func(context.Context, sandbox.ResourceRef) error
	inspect      func(context.Context, sandbox.ResourceRef) error
}

func newDurableFake() *durableFake {
	return &durableFake{refs: make(map[string]sandbox.ResourceRef), states: make(map[string]sandbox.ResourceState)}
}
func (f *durableFake) event(name string) {
	f.mu.Lock()
	f.events = append(f.events, name)
	f.mu.Unlock()
}
func (f *durableFake) Namespace() string { return "test-isolated-namespace" }
func (f *durableFake) PrepareResource(_ context.Context, id, owner string) (sandbox.ResourceRef, error) {
	f.event("prepare")
	return sandbox.ResourceRef{ID: id, Owner: owner, Namespace: f.Namespace(), Snapshotter: "overlayfs"}, nil
}
func (f *durableFake) Create(ctx context.Context, opts *sandbox.CreateOptions) (string, error) {
	f.event("create")
	if f.beforeCreate != nil {
		if err := f.beforeCreate(ctx, opts); err != nil {
			return "", err
		}
	}
	if opts.Resource == nil {
		return "", errors.New("missing durable identity")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refs[opts.Resource.ID] = *opts.Resource
	f.states[opts.Resource.ID] = sandbox.ResourceState{ContainerExists: true, SnapshotExists: true}
	return opts.Resource.ID, nil
}
func (f *durableFake) Start(_ context.Context, id string) error {
	f.event("start")
	f.mu.Lock()
	defer f.mu.Unlock()
	state := f.states[id]
	state.TaskExists = true
	f.states[id] = state
	return nil
}
func (f *durableFake) Execute(ctx context.Context, id string, command []string, opts *sandbox.ExecuteOptions) (*sandbox.ExecuteResult, error) {
	f.event("execute")
	return f.fakeBackend.Execute(ctx, id, command, opts)
}
func (f *durableFake) InspectOwned(ctx context.Context, ref sandbox.ResourceRef) (sandbox.ResourceState, error) {
	f.event("inspect")
	if f.inspect != nil {
		if err := f.inspect(ctx, ref); err != nil {
			return sandbox.ResourceState{}, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.refs[ref.ID]; ok && !reflect.DeepEqual(existing, ref) {
		return sandbox.ResourceState{}, errors.New("foreign runtime ownership")
	}
	return f.states[ref.ID], nil
}
func (f *durableFake) SealOwned(ctx context.Context, ref sandbox.ResourceRef) error {
	f.event("seal")
	if f.seal != nil {
		if err := f.seal(ctx, ref); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	state := f.states[ref.ID]
	state.TaskExists = false
	f.states[ref.ID] = state
	return nil
}
func (f *durableFake) RemoveOwned(ctx context.Context, ref sandbox.ResourceRef) error {
	f.event("remove-owned")
	if f.removeOwned != nil {
		if err := f.removeOwned(ctx, ref); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.refs, ref.ID)
	delete(f.states, ref.ID)
	return nil
}
func (f *durableFake) Remove(context.Context, string) error {
	return errors.New("durable sessions must not use legacy Remove")
}

func controllerOptions(t *testing.T) ControllerOptions {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	opts := ControllerOptions{JournalRoot: filepath.Join(root, "journal"), WorkspaceRoot: filepath.Join(root, "workspaces"), ControllerID: "controller-one", AgentID: "agent-one", MaxRecords: 16}
	for _, path := range []string{opts.JournalRoot, opts.WorkspaceRoot} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return opts
}
func durableOptions() Options {
	image := "registry.example/build@sha256:" + strings.Repeat("a", 64)
	runtimeRequest := &taskqueue.RuntimeRequest{Type: taskqueue.RuntimeContainerdOfflineV1, Image: image, Resources: taskqueue.RuntimeResources{CPU: "500m", Memory: "64Mi"}}
	digest, _ := taskqueue.RuntimeBindingDigest("pipeline-definition-one", "pipeline-one", "job-one", "agent-one", runtimeRequest)
	return Options{
		JobRunID: "job-one", PipelineRunID: "pipeline-one", ExecutionClaimID: "claim-one", RuntimeBindingDigest: digest,
		Image: image, Resources: &sandbox.Resources{CPU: "500m", Memory: "64Mi"}, User: "1000:1001",
	}
}
func openTestController(t *testing.T, backend *durableFake, opts ControllerOptions) *Controller {
	t.Helper()
	controller, err := OpenController(context.Background(), backend, opts)
	if err != nil {
		t.Fatal(err)
	}
	return controller
}
func allocateTestSession(t *testing.T, c *Controller, opts Options) *Session {
	t.Helper()
	s, err := c.Allocate(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func simulateCrash(t *testing.T, c *Controller) {
	t.Helper()
	if err := c.closeHandles(); err != nil {
		t.Fatal(err)
	}
	c.closed = true
}
func finishController(t *testing.T, c *Controller, s *Session) {
	t.Helper()
	if s != nil {
		if err := s.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDurableSessionStagesBeforeCreateAndSealsBeforeArtifacts(t *testing.T) {
	backend := newDurableFake()
	opts := controllerOptions(t)
	c := openTestController(t, backend, opts)
	sessionOpts := durableOptions()
	sessionOpts.Image = "private-image-label"
	session := allocateTestSession(t, c, sessionOpts)
	if !reflect.DeepEqual(backend.events, []string{"prepare"}) {
		t.Fatalf("allocate touched runtime: %v", backend.events)
	}
	if _, err := session.Execute(context.Background(), request(session, "exit 0")); err == nil {
		t.Fatal("unstarted session executed")
	}
	artifact := filepath.Join(session.Workspace(), "artifact.txt")
	if err := os.WriteFile(artifact, []byte("trusted-input"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend.beforeCreate = func(_ context.Context, create *sandbox.CreateOptions) error {
		record, err := c.readJournal(context.Background(), session.recordID+".json")
		if err != nil {
			return err
		}
		if record.Phase != phaseCreating || record.Resource != *create.Resource || create.User != "1000:1001" {
			return fmt.Errorf("create preceded durable intent or lost identity/user")
		}
		data, err := os.ReadFile(artifact)
		if err != nil || string(data) != "trusted-input" {
			return fmt.Errorf("input was not staged")
		}
		return nil
	}
	if err := session.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.Start(context.Background()); err == nil {
		t.Fatal("repeated start accepted")
	}
	req := request(session, "printf secret-command-marker")
	req.Env["CREDENTIAL"] = "secret-env-marker"
	if _, err := session.Execute(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if err := session.Seal(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.Seal(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Execute(context.Background(), request(session, "after seal")); err == nil {
		t.Fatal("sealed session executed")
	}
	if data, err := os.ReadFile(artifact); err != nil || string(data) != "trusted-input" {
		t.Fatalf("seal removed artifacts: %q %v", data, err)
	}
	for _, name := range []string{journalBindingFile, session.recordID + ".json"} {
		data, err := os.ReadFile(filepath.Join(opts.JournalRoot, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"secret-command-marker", "secret-env-marker", "private-image-label", "trusted-input"} {
			if strings.Contains(string(data), secret) {
				t.Fatalf("journal leaked %q", secret)
			}
		}
	}
	if err := c.Close(context.Background()); err == nil {
		t.Fatal("controller released live session")
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(session.Workspace()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace was not deleted: %v", err)
	}
	if !c.HasJob(sessionOpts.JobRunID) || len(c.Recoveries()) != 1 {
		t.Fatal("cleanup discarded unreported outcome")
	}
	if _, err := c.Allocate(context.Background(), sessionOpts); err == nil {
		t.Fatal("duplicate claim reused")
	}
	sessionOpts.ExecutionClaimID = "different-claim"
	if _, err := c.Allocate(context.Background(), sessionOpts); err == nil {
		t.Fatal("new claim bypassed job tombstone")
	}
	if err := c.AcknowledgeCompletion(context.Background(), "claim-one"); err != nil {
		t.Fatal(err)
	}
	if c.HasJob("job-one") {
		t.Fatal("acknowledged terminal record remains")
	}
	finishController(t, c, nil)
}

func TestDurableStartupRecoversExactResourcesWithoutRerun(t *testing.T) {
	for _, phase := range []string{phaseAllocated, phaseCreating, phaseCreated, phaseStarting, phaseRunning, phaseExecuting, phaseSealing, phaseSealed, phaseRemoving, phaseClosed} {
		t.Run(phase, func(t *testing.T) {
			backend := newDurableFake()
			opts := controllerOptions(t)
			c := openTestController(t, backend, opts)
			session := allocateTestSession(t, c, durableOptions())
			if phase != phaseAllocated && phase != phaseCreating {
				if err := session.Start(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if phase == phaseClosed {
				if err := session.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else if err := session.advance(context.Background(), phase); err != nil {
				t.Fatal(err)
			}
			before := countEvent(backend.events, "start")
			foreign := sandbox.ResourceRef{ID: "unrecorded-resource", Owner: "another-agent", Namespace: backend.Namespace(), Snapshotter: "overlayfs"}
			backend.refs[foreign.ID] = foreign
			backend.states[foreign.ID] = sandbox.ResourceState{ContainerExists: true, TaskExists: true, SnapshotExists: true}
			simulateCrash(t, c)
			recovered := openTestController(t, backend, opts)
			if recovered.Ready() {
				t.Fatal("controller advertised readiness before outcome reconciliation")
			}
			if countEvent(backend.events, "start") != before {
				t.Fatal("startup reran interrupted session")
			}
			if _, ok := backend.refs[foreign.ID]; !ok {
				t.Fatal("recovery touched unrecorded resource")
			}
			records := recovered.Recoveries()
			if len(records) != 1 || records[0].ExecutionClaimID != "claim-one" || records[0].RuntimeBindingDigest != durableOptions().RuntimeBindingDigest || !records[0].CleanupComplete {
				t.Fatalf("recovery identity lost: %+v", records)
			}
			if _, err := os.Stat(session.Workspace()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("workspace remained: %v", err)
			}
			other := durableOptions()
			other.JobRunID = "other-job"
			other.ExecutionClaimID = "other-claim"
			if _, err := recovered.Allocate(context.Background(), other); err == nil {
				t.Fatal("new work entered before recovery acknowledgement")
			}
			if err := recovered.AcknowledgeCompletion(context.Background(), "claim-one"); err != nil {
				t.Fatal(err)
			}
			if !recovered.Ready() {
				t.Fatal("verified terminal acknowledgement did not unblock readiness")
			}
			finishController(t, recovered, nil)
		})
	}
}
func countEvent(events []string, want string) int {
	count := 0
	for _, event := range events {
		if event == want {
			count++
		}
	}
	return count
}

func TestDurableJournalLockBindingAndQuota(t *testing.T) {
	backend := newDurableFake()
	opts := controllerOptions(t)
	opts.MaxRecords = 1
	c := openTestController(t, backend, opts)
	if other, err := OpenController(context.Background(), backend, opts); err == nil || other != nil {
		t.Fatal("exclusive lock not enforced")
	}
	session := allocateTestSession(t, c, durableOptions())
	other := durableOptions()
	other.JobRunID = "job-two"
	other.ExecutionClaimID = "claim-two"
	if _, err := c.Allocate(context.Background(), other); err == nil {
		t.Fatal("journal quota not enforced")
	}
	finishController(t, c, session)
	changed := opts
	changed.AgentID = "another-agent"
	if other, err := OpenController(context.Background(), backend, changed); err == nil || other != nil {
		t.Fatal("foreign Agent reused journal")
	}
	changed = opts
	changed.ControllerID = "another-controller"
	if other, err := OpenController(context.Background(), backend, changed); err == nil || other != nil {
		t.Fatal("foreign controller reused journal")
	}
	reopened := openTestController(t, backend, opts)
	finishController(t, reopened, nil)
}

func TestDurableCorruptionPreflightPreventsAnyRuntimeDeletion(t *testing.T) {
	for _, mutate := range []string{"checksum", "symlink-record", "hardlink-record", "permissions", "pending", "missing-binding", "workspace-symlink", "foreign-runtime"} {
		t.Run(mutate, func(t *testing.T) {
			backend := newDurableFake()
			opts := controllerOptions(t)
			c := openTestController(t, backend, opts)
			session := allocateTestSession(t, c, durableOptions())
			if err := session.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			secondOpts := durableOptions()
			secondOpts.JobRunID = "job-two"
			secondOpts.ExecutionClaimID = "claim-two"
			second := allocateTestSession(t, c, secondOpts)
			if err := second.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			simulateCrash(t, c)
			path := filepath.Join(opts.JournalRoot, session.recordID+".json")
			switch mutate {
			case "checksum":
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				data = []byte(strings.Replace(string(data), "job-one", "job-bad", 1))
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink-record":
				target := filepath.Join(t.TempDir(), "record")
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "hardlink-record":
				if err := os.Link(path, filepath.Join(t.TempDir(), "link")); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "pending":
				if err := os.WriteFile(filepath.Join(opts.JournalRoot, ".pending-interrupted"), []byte("evidence"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing-binding":
				if err := os.Remove(filepath.Join(opts.JournalRoot, journalBindingFile)); err != nil {
					t.Fatal(err)
				}
			case "workspace-symlink":
				if err := os.Remove(session.Workspace()); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), session.Workspace()); err != nil {
					t.Fatal(err)
				}
			case "foreign-runtime":
				ref := backend.refs[session.containerID]
				ref.Owner = "foreign-owner"
				backend.refs[session.containerID] = ref
			}
			before := countEvent(backend.events, "remove-owned")
			if reopened, err := OpenController(context.Background(), backend, opts); err == nil || reopened != nil {
				t.Fatal("unsafe journal accepted")
			}
			if countEvent(backend.events, "remove-owned") != before {
				t.Fatal("destructive recovery preceded complete validation")
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("evidence removed: %v", err)
			}
		})
	}
}

func TestDurableCleanupFailureRetainsWorkspaceAndBlocksNewWork(t *testing.T) {
	backend := newDurableFake()
	opts := controllerOptions(t)
	c := openTestController(t, backend, opts)
	session := allocateTestSession(t, c, durableOptions())
	if err := session.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("runtime removal failed")
	backend.removeOwned = func(context.Context, sandbox.ResourceRef) error { return failure }
	if err := session.Close(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("cleanup failure lost: %v", err)
	}
	if _, err := os.Stat(session.Workspace()); err != nil {
		t.Fatal("workspace removed despite runtime cleanup failure")
	}
	if c.Ready() {
		t.Fatal("cleanup failure advertised ready")
	}
	if err := c.AcknowledgeCompletion(context.Background(), "claim-one"); err == nil {
		t.Fatal("uncleaned claim acknowledged")
	}
	if err := c.Close(context.Background()); err == nil {
		t.Fatal("controller released failed cleanup session")
	}
	backend.removeOwned = nil
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !c.Ready() {
		t.Fatal("retry did not restore known runtime state")
	}
	finishController(t, c, nil)
}

func TestDurableSealFailureDisallowsArtifactPhase(t *testing.T) {
	backend := newDurableFake()
	opts := controllerOptions(t)
	c := openTestController(t, backend, opts)
	session := allocateTestSession(t, c, durableOptions())
	if err := session.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("PID verification failed")
	backend.seal = func(context.Context, sandbox.ResourceRef) error { return failure }
	if err := session.Seal(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("seal failure lost: %v", err)
	}
	if session.sealed || c.Ready() {
		t.Fatal("failed seal admitted artifact phase/readiness")
	}
	if _, err := session.Execute(context.Background(), request(session, "after failed seal")); err == nil {
		t.Fatal("failed seal session reused")
	}
	backend.seal = nil
	if err := session.Seal(context.Background()); err != nil {
		t.Fatal(err)
	}
	finishController(t, c, session)
}

func TestDurablePersistenceFailurePreventsRuntimeCreate(t *testing.T) {
	for _, point := range []string{"record.write", "record.sync", "record.rename", "directory.sync"} {
		t.Run(point, func(t *testing.T) {
			backend := newDurableFake()
			opts := controllerOptions(t)
			c := openTestController(t, backend, opts)
			session := allocateTestSession(t, c, durableOptions())
			c.hook = func(current string) error {
				if current == point {
					return errors.New("simulated durable I/O failure")
				}
				return nil
			}
			if err := session.Start(context.Background()); err == nil {
				t.Fatal("failed journal admitted runtime creation")
			}
			if countEvent(backend.events, "create") != 0 || c.Ready() {
				t.Fatal("runtime side effect preceded durable intent")
			}
			simulateCrash(t, c)
			reopened, err := OpenController(context.Background(), backend, opts)
			if point == "directory.sync" {
				if err != nil {
					t.Fatal(err)
				}
				finishController(t, reopened, nil)
			} else if err == nil {
				finishController(t, reopened, nil)
				t.Fatal("interrupted pending file was ignored")
			}
		})
	}
}

func TestDurableSealCancelsAndWaitsForActiveExecution(t *testing.T) {
	backend := newDurableFake()
	opts := controllerOptions(t)
	c := openTestController(t, backend, opts)
	session := allocateTestSession(t, c, durableOptions())
	if err := session.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	started, finished := make(chan struct{}), make(chan struct{})
	backend.execute = func(ctx context.Context, _ string, _ []string, _ *sandbox.ExecuteOptions) (*sandbox.ExecuteResult, error) {
		close(started)
		<-ctx.Done()
		close(finished)
		return nil, ctx.Err()
	}
	backend.seal = func(ctx context.Context, _ sandbox.ResourceRef) error {
		select {
		case <-finished:
		default:
			t.Error("Seal raced active execution")
		}
		if ctx.Err() != nil {
			t.Error("Seal inherited canceled context")
		}
		return nil
	}
	done := make(chan error, 1)
	safe.Go(func() { _, err := session.Execute(context.Background(), request(session, "sleep")); done <- err })
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := session.Seal(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("execution did not cancel: %v", err)
	}
	if _, err := os.Stat(session.Workspace()); err != nil {
		t.Fatal("Seal deleted workspace")
	}
	finishController(t, c, session)
}

func TestDurablePreallocationCrashDoesNotLoseClaim(t *testing.T) {
	for _, workspaceExists := range []bool{false, true} {
		t.Run(fmt.Sprint(workspaceExists), func(t *testing.T) {
			backend := newDurableFake()
			opts := controllerOptions(t)
			c := openTestController(t, backend, opts)
			session := allocateTestSession(t, c, durableOptions())
			record := c.records[session.recordID]
			record.Phase = phaseIntent
			record.Device = 0
			record.Inode = 0
			if err := c.persist(context.Background(), record); err != nil {
				t.Fatal(err)
			}
			if !workspaceExists {
				if err := os.Remove(session.Workspace()); err != nil {
					t.Fatal(err)
				}
				if err := c.workspaceDir.Sync(); err != nil {
					t.Fatal(err)
				}
			}
			simulateCrash(t, c)
			recovered, err := OpenController(context.Background(), backend, opts)
			if workspaceExists {
				if err == nil {
					finishController(t, recovered, nil)
					t.Fatal("unbound workspace identity guessed during recovery")
				}
				if _, err := os.Stat(session.Workspace()); err != nil {
					t.Fatal("uncertain workspace evidence deleted")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if !recovered.HasJob("job-one") || recovered.Ready() || len(recovered.Recoveries()) != 1 {
					t.Fatal("intent claim silently discarded")
				}
				finishController(t, recovered, nil)
				twice := openTestController(t, backend, opts)
				if !twice.HasJob("job-one") {
					t.Fatal("empty-workspace recovery tombstone was not durable")
				}
				finishController(t, twice, nil)
			}
			if countEvent(backend.events, "remove-owned") != 0 {
				t.Fatal("preallocation recovery attempted runtime deletion")
			}
		})
	}
}

func TestDurableStartupCleanupFailurePreservesRetryEvidence(t *testing.T) {
	backend := newDurableFake()
	opts := controllerOptions(t)
	c := openTestController(t, backend, opts)
	session := allocateTestSession(t, c, durableOptions())
	if err := session.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	simulateCrash(t, c)
	failure := errors.New("cleanup unreachable")
	backend.removeOwned = func(context.Context, sandbox.ResourceRef) error { return failure }
	if reopened, err := OpenController(context.Background(), backend, opts); reopened != nil || !errors.Is(err, failure) {
		t.Fatalf("startup cleanup failure lost: %v %v", reopened, err)
	}
	if _, err := os.Stat(session.Workspace()); err != nil {
		t.Fatal("startup removed workspace while runtime cleanup failed")
	}
	if backend.states[session.containerID].TaskExists {
		t.Fatal("startup cleanup failure left verified-owned processes running")
	}
	if _, err := os.Stat(filepath.Join(opts.JournalRoot, session.recordID+".json")); err != nil {
		t.Fatal("startup removed recovery evidence")
	}
	backend.removeOwned = nil
	reopened := openTestController(t, backend, opts)
	if reopened.Ready() || len(reopened.Recoveries()) != 1 {
		t.Fatal("recovered cleanup discarded outcome")
	}
	finishController(t, reopened, nil)
}

func TestDurableStartupQuotaAndUnsafeRootFailBeforeRuntime(t *testing.T) {
	backend := newDurableFake()
	opts := controllerOptions(t)
	c := openTestController(t, backend, opts)
	first := allocateTestSession(t, c, durableOptions())
	if err := first.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	secondOpts := durableOptions()
	secondOpts.JobRunID = "job-two"
	secondOpts.ExecutionClaimID = "claim-two"
	second := allocateTestSession(t, c, secondOpts)
	if err := second.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	simulateCrash(t, c)
	changed := opts
	changed.MaxRecords = 1
	if reopened, err := OpenController(context.Background(), backend, changed); reopened != nil || err == nil {
		t.Fatal("oversize startup journal admitted")
	}
	if countEvent(backend.events, "remove-owned") != 0 {
		t.Fatal("quota failure deleted runtime")
	}
	if err := os.Chmod(opts.JournalRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if reopened, err := OpenController(context.Background(), backend, opts); reopened != nil || err == nil {
		t.Fatal("nonprivate journal root admitted")
	}
	if err := os.Chmod(opts.JournalRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(opts.JournalRoot, link); err != nil {
		t.Fatal(err)
	}
	changed = opts
	changed.JournalRoot = link
	if reopened, err := OpenController(context.Background(), backend, changed); reopened != nil || err == nil {
		t.Fatal("symlink journal root admitted")
	}
	reopened := openTestController(t, backend, opts)
	finishController(t, reopened, nil)
}

func TestDurableAllocationRejectsRootUserAndUnboundIdentity(t *testing.T) {
	for _, mutate := range []func(*Options){
		func(o *Options) { o.User = "" }, func(o *Options) { o.User = "0:1000" }, func(o *Options) { o.User = "1000:0" }, func(o *Options) { o.User = "root" }, func(o *Options) { o.User = "1000" }, func(o *Options) { o.User = "01000:1001" },
		func(o *Options) { o.ExecutionClaimID = "" }, func(o *Options) { o.RuntimeBindingDigest = "" },
		func(o *Options) { o.RuntimeBindingDigest = strings.Repeat("a", 64) }, func(o *Options) { o.RuntimeBindingDigest = "v1:" + strings.Repeat("A", 64) }, func(o *Options) { o.JobRunID = "../job" }, func(o *Options) { o.PipelineRunID = "../pipeline" },
	} {
		backend := newDurableFake()
		c := openTestController(t, backend, controllerOptions(t))
		opts := durableOptions()
		mutate(&opts)
		if _, err := c.Allocate(context.Background(), opts); err == nil {
			t.Fatalf("unsafe allocation accepted: %+v", opts)
		}
		if len(backend.events) != 0 {
			t.Fatal("unsafe allocation reached runtime")
		}
		finishController(t, c, nil)
	}
}

type wrongIdentityBackend struct{ *durableFake }

func (f *wrongIdentityBackend) Create(ctx context.Context, opts *sandbox.CreateOptions) (string, error) {
	_, err := f.durableFake.Create(ctx, opts)
	return "unexpected-container-id", err
}

func TestDurableRuntimeIdentityConflictRequiresOperatorReview(t *testing.T) {
	backend := &wrongIdentityBackend{newDurableFake()}
	opts := controllerOptions(t)
	c, err := OpenController(context.Background(), backend, opts)
	if err != nil {
		t.Fatal(err)
	}
	session := allocateTestSession(t, c, durableOptions())
	if err := session.Start(context.Background()); err == nil {
		t.Fatal("runtime identity conflict accepted")
	}
	if err := session.Close(context.Background()); err == nil {
		t.Fatal("identity conflict allowed guessed cleanup")
	}
	if c.Ready() {
		t.Fatal("identity conflict advertised ready")
	}
	simulateCrash(t, c)
	if recovered, err := OpenController(context.Background(), backend, opts); err == nil || recovered != nil {
		t.Fatal("restart ignored runtime identity conflict")
	}
	if countEvent(backend.events, "remove-owned") != 0 {
		t.Fatal("identity conflict deleted runtime")
	}
	if _, err := os.Stat(session.Workspace()); err != nil {
		t.Fatal("identity conflict removed workspace evidence")
	}
}

func TestDurableWorkspaceReplacementIsPreserved(t *testing.T) {
	backend := newDurableFake()
	opts := controllerOptions(t)
	c := openTestController(t, backend, opts)
	session := allocateTestSession(t, c, durableOptions())
	if err := session.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	original := session.Workspace() + "-original"
	if err := os.Rename(session.Workspace(), original); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(session.Workspace(), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(session.Workspace(), "evidence")
	if err := os.WriteFile(marker, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Execute(context.Background(), request(session, "exit 0")); err == nil {
		t.Fatal("replacement workspace executed")
	}
	if c.Ready() {
		t.Fatal("replacement workspace advertised ready")
	}
	if err := session.Close(context.Background()); err == nil {
		t.Fatal("replacement workspace deleted")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "preserve" {
		t.Fatal("replacement evidence lost")
	}
	simulateCrash(t, c)
}

func TestDurableFailureStillQuiescesOwnedProcessesAndPreservesEvidence(t *testing.T) {
	for _, failure := range []string{"workspace-permissions", "journal-corruption", "journal-sync"} {
		t.Run(failure, func(t *testing.T) {
			backend := newDurableFake()
			opts := controllerOptions(t)
			c := openTestController(t, backend, opts)
			session := allocateTestSession(t, c, durableOptions())
			if err := session.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "workspace-permissions":
				if err := os.Chmod(session.Workspace(), 0o755); err != nil {
					t.Fatal(err)
				}
			case "journal-corruption":
				if err := os.WriteFile(filepath.Join(opts.JournalRoot, session.recordID+".json"), []byte("corrupt evidence"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "journal-sync":
				c.hook = func(point string) error {
					if point == "record.sync" {
						return errors.New("sync failed")
					}
					return nil
				}
			}
			if err := session.Seal(context.Background()); err == nil {
				t.Fatal("unsafe artifact phase admitted")
			}
			state := backend.states[session.containerID]
			if state.TaskExists || !state.ContainerExists || !state.SnapshotExists {
				t.Fatalf("failed seal did not stop-only exact owned resource: %+v", state)
			}
			if session.sealed || c.Ready() {
				t.Fatal("stop-only fallback advertised artifact/readiness success")
			}
			if err := session.Close(context.Background()); err == nil {
				t.Fatal("unsafe evidence cleanup admitted")
			}
			if countEvent(backend.events, "remove-owned") != 0 {
				t.Fatal("fallback deleted runtime evidence")
			}
			if _, err := os.Stat(session.Workspace()); err != nil {
				t.Fatal("fallback deleted workspace evidence")
			}
			if _, err := os.Stat(filepath.Join(opts.JournalRoot, session.recordID+".json")); err != nil {
				t.Fatal("fallback deleted journal evidence")
			}
			simulateCrash(t, c)
		})
	}
}

func TestDurableStartupUnsafeWorkspaceStopsOnlyVerifiedOwnedProcesses(t *testing.T) {
	backend := newDurableFake()
	opts := controllerOptions(t)
	c := openTestController(t, backend, opts)
	session := allocateTestSession(t, c, durableOptions())
	if err := session.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(session.Workspace(), 0o755); err != nil {
		t.Fatal(err)
	}
	simulateCrash(t, c)
	if recovered, err := OpenController(context.Background(), backend, opts); err == nil || recovered != nil {
		t.Fatal("unsafe workspace recovery succeeded")
	}
	state := backend.states[session.containerID]
	if state.TaskExists || !state.ContainerExists || !state.SnapshotExists {
		t.Fatalf("startup did not stop-only exact known-owned resource: %+v", state)
	}
	if countEvent(backend.events, "remove-owned") != 0 {
		t.Fatal("unsafe startup deleted runtime evidence")
	}
	if _, err := os.Stat(session.Workspace()); err != nil {
		t.Fatal("unsafe startup deleted workspace evidence")
	}
}

func TestDurableFailureFallbackNeverStopsForeignProcesses(t *testing.T) {
	backend := newDurableFake()
	opts := controllerOptions(t)
	c := openTestController(t, backend, opts)
	session := allocateTestSession(t, c, durableOptions())
	if err := session.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(session.Workspace(), 0o755); err != nil {
		t.Fatal(err)
	}
	ref := backend.refs[session.containerID]
	ref.Owner = "foreign-owner"
	backend.refs[session.containerID] = ref
	if err := session.Seal(context.Background()); err == nil {
		t.Fatal("foreign resource seal accepted")
	}
	if countEvent(backend.events, "seal") != 0 || !backend.states[session.containerID].TaskExists {
		t.Fatal("fallback stopped foreign runtime")
	}
	simulateCrash(t, c)
}
