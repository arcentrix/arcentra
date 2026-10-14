//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandboxexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/arcentrix/arcentra/pkg/sandbox"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/containerd/containerd"
	tasks "github.com/containerd/containerd/api/services/tasks/v1"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/namespaces"
	"github.com/google/uuid"
	imagedigest "github.com/opencontainers/go-digest"
)

// TestDurableControllerContainerdAcceptance 仅在明确授权的非 root 专用主机验证持久生命周期
// 编译测试必须使用 -run '^$'，实际运行另需独立开关及全部显式环境参数
func TestDurableControllerContainerdAcceptance(t *testing.T) {
	config, opts, image := durableAcceptanceConfig(t, "sealed")
	socket, namespace := config.UnixSocket, config.Namespace
	user := fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid())
	runID, jobID, claimID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	request := &taskqueue.RuntimeRequest{Type: taskqueue.RuntimeContainerdOfflineV1, Image: image, Resources: taskqueue.RuntimeResources{CPU: "500m", Memory: "64Mi"}}
	digest, err := taskqueue.RuntimeBindingDigest("durable-acceptance-pipeline", runID, jobID, opts.AgentID, request)
	if err != nil {
		t.Fatalf("acceptance requires an immutable approved image@sha256 and valid runtime request: %v", err)
	}
	backend, err := sandbox.NewContainerdSandbox(config, log.Logger{})
	if err != nil {
		t.Fatal(err)
	}
	foreignBackend, err := sandbox.NewContainerdSandbox(config, log.Logger{})
	if err != nil {
		_ = backend.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var controller *Controller
	var session *Session
	var foreignRef sandbox.ResourceRef
	var journalValidated bool
	client, err := containerd.New(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cleanupCancel()
		var ownErr error
		if session != nil {
			ownErr = session.Close(cleanupCtx)
		}
		if controller != nil {
			ownErr = errors.Join(ownErr, controller.Close(cleanupCtx))
		}
		if ownErr == nil && journalValidated {
			ownErr = backend.Close()
		}
		if ownErr != nil {
			t.Errorf("durable acceptance cleanup failed; preserve namespace %s and journal %s: %v", namespace, opts.JournalRoot, ownErr)
		}
		var foreignErr error
		if foreignRef.ID != "" {
			foreignErr = foreignBackend.RemoveOwned(cleanupCtx, foreignRef)
		}
		if foreignErr == nil {
			foreignErr = foreignBackend.Close()
		}
		if foreignErr != nil {
			t.Errorf("exact foreign-fixture cleanup failed; preserve resource %s: %v", foreignRef.ID, foreignErr)
		}
	})
	controller, err = OpenController(ctx, backend, opts)
	if err != nil {
		t.Fatal(err)
	}
	journalValidated = true
	if !controller.Ready() {
		t.Fatal("acceptance journal has unresolved execution outcomes; preserve it for operator reconciliation")
	}
	foreignRef, err = foreignBackend.PrepareResource(ctx, "arcentra-"+uuid.NewString(), "durable-acceptance-foreign-owner-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	_, err = foreignBackend.Create(ctx, &sandbox.CreateOptions{
		Resource: &foreignRef, Image: image, User: user, NetworkMode: "none", WorkingDir: "/",
		Command:   []string{"sh", "-ec", "trap 'exit 0' TERM INT; while :; do sleep 1; done"},
		Resources: &sandbox.Resources{CPU: "500m", Memory: "64Mi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := foreignBackend.Start(ctx, foreignRef.ID); err != nil {
		t.Fatal(err)
	}
	session, err = controller.Allocate(ctx, Options{
		JobRunID: jobID, PipelineRunID: runID, ExecutionClaimID: claimID, RuntimeBindingDigest: digest,
		Image: image, Resources: &sandbox.Resources{CPU: "500m", Memory: "64Mi"}, User: user,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("durable acceptance namespace=%s container=%s foreign=%s workspace=%s", namespace, session.containerID, foreignRef.ID, session.Workspace())
	if err := os.WriteFile(filepath.Join(session.Workspace(), "input.txt"), []byte("staged-before-container"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := session.Start(ctx); err != nil {
		t.Fatal(err)
	}
	command := `test "$(cat /workspace/input.txt)" = staged-before-container
printf '%s' foreground-artifact > /workspace/artifact.txt
(while :; do printf 'tick\n' >> /workspace/background.txt; sleep 0.05; done) >/dev/null 2>&1 &
printf '%s\n' "$!" > /workspace/background.pid
printf '%s' durable-shell-output`
	result, err := session.Execute(ctx, requestForDurableAcceptance(session, command))
	if err != nil || !result.Success || result.Output != "durable-shell-output" {
		t.Fatalf("durable shell failed: %+v %v", result, err)
	}
	writerPath := filepath.Join(session.Workspace(), "background.txt")
	if err := waitForAcceptanceWriter(ctx, writerPath); err != nil {
		t.Fatal(err)
	}
	if err := session.Seal(ctx); err != nil {
		t.Fatal(err)
	}
	assertAcceptanceQuiescent(t, ctx, client, session.resourceRef)
	if err := session.Seal(ctx); err != nil {
		t.Fatalf("Seal is not idempotent: %v", err)
	}
	if _, err := session.Execute(ctx, requestForDurableAcceptance(session, "true")); err == nil {
		t.Fatal("sealed session allowed another command")
	}
	before, err := os.ReadFile(writerPath)
	if err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(250 * time.Millisecond)
	select {
	case <-ctx.Done():
		timer.Stop()
		t.Fatal(ctx.Err())
	case <-timer.C:
	}
	after, err := os.ReadFile(writerPath)
	if err != nil || string(before) != string(after) {
		t.Fatalf("writer continued after verified Seal: before=%d after=%d err=%v", len(before), len(after), err)
	}
	artifact, err := os.ReadFile(filepath.Join(session.Workspace(), "artifact.txt"))
	if err != nil || string(artifact) != "foreground-artifact" {
		t.Fatalf("sealed artifact unavailable: %q %v", artifact, err)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(session.Workspace()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Close did not delete exact workspace: %v", err)
	}
	assertAcceptanceRemoved(t, ctx, client, session.resourceRef, session.Workspace())
	// Active cancellation and deadlines must stop descendants before exact cleanup.
	for _, scenario := range []string{"cancel", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			runDurableCancellationAcceptance(t, ctx, controller, client, image, user, scenario)
		})
	}
	if err := controller.Close(ctx); err != nil {
		t.Fatal(err)
	}
	controller = nil
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	journalValidated = false
	backend, err = sandbox.NewContainerdSandbox(config, log.Logger{})
	if err != nil {
		t.Fatal(err)
	}
	controller, err = OpenController(ctx, backend, opts)
	if err != nil {
		t.Fatal(err)
	}
	journalValidated = true
	recoveries := controller.Recoveries()
	if controller.Ready() || len(recoveries) != 3 || !containsAcceptanceRecovery(recoveries, runID, jobID, claimID, digest) {
		t.Fatalf("restart lost exact pending terminal identity: ready=%v recovery=%+v", controller.Ready(), recoveries)
	}
	foreignState, err := foreignBackend.InspectOwned(ctx, foreignRef)
	if err != nil || !foreignState.ContainerExists || !foreignState.SnapshotExists || !foreignState.TaskExists {
		t.Fatalf("controller recovery touched foreign runtime: %+v %v", foreignState, err)
	}
	foreignResult, err := foreignBackend.Execute(ctx, foreignRef.ID, []string{"sh", "-ec", "printf foreign-fixture-alive"}, &sandbox.ExecuteOptions{User: user})
	if err != nil || foreignResult == nil || foreignResult.ExitCode != 0 || foreignResult.Stdout != "foreign-fixture-alive" {
		t.Fatalf("foreign fixture did not survive exact recovery: %+v %v", foreignResult, err)
	}
	// 此测试没有控制端业务终态；保留墓碑，禁止伪造终态确认来清空日志
	t.Logf("durable lifecycle and foreign isolation verified; terminal tombstone intentionally retained for claim %s", claimID)
}

func requestForDurableAcceptance(session *Session, command string) *executor.ExecutionRequest {
	return &executor.ExecutionRequest{Step: &executor.StepInfo{Uses: "shell", Action: "run", Args: map[string]any{"run": command}}, Workspace: session.Workspace()}
}

func waitForAcceptanceWriter(ctx context.Context, path string) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var firstSize int64 = -1
	for {
		info, err := os.Stat(path)
		if err == nil {
			if firstSize < 0 {
				firstSize = info.Size()
			} else if info.Size() > firstSize {
				return nil
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("background writer did not remain active before Seal: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// Each scenario retains its own journal evidence beneath the explicitly approved roots.
func durableAcceptanceConfig(t *testing.T, scenario string) (*sandbox.ContainerdConfig, ControllerOptions, string) {
	t.Helper()
	if os.Getenv("ARCENTRA_DURABLE_SESSION_ACCEPTANCE") != "1" {
		t.Fatal("durable acceptance was not run: explicit dedicated-host authorization and ARCENTRA_DURABLE_SESSION_ACCEPTANCE=1 are required")
	}
	if runtime.GOOS != "linux" || os.Geteuid() == 0 || os.Getegid() == 0 {
		t.Fatal("durable acceptance requires an unprivileged nonzero Linux UID:GID; do not run the test with sudo")
	}
	required := func(name string) string {
		t.Helper()
		value := os.Getenv(name)
		if strings.TrimSpace(value) == "" {
			t.Fatalf("durable acceptance requires explicit %s", name)
		}
		return value
	}
	socket, image := required("ARCENTRA_CONTAINERD_SOCKET"), required("ARCENTRA_SANDBOX_TEST_IMAGE")
	if !filepath.IsAbs(socket) || filepath.Clean(socket) == "/run/containerd/containerd.sock" || filepath.Clean(socket) == "/var/run/containerd/containerd.sock" {
		t.Fatal("acceptance requires an absolute dedicated daemon socket, never the host default")
	}
	if info, err := os.Lstat(socket); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("explicit containerd socket is unavailable or not a socket: %v", err)
	}
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		t.Fatalf("dedicated acceptance host requires cgroup v2: %v", err)
	}
	_, imageDigest, pinned := strings.Cut(image, "@")
	if !pinned || !strings.HasPrefix(imageDigest, "sha256:") || imagedigest.Digest(imageDigest).Validate() != nil {
		t.Fatal("ARCENTRA_SANDBOX_TEST_IMAGE must be immutable image@sha256:<64 hex characters>")
	}
	namespace := required("ARCENTRA_DURABLE_SESSION_NAMESPACE")
	if !strings.HasPrefix(namespace, "arcentra-durable-acceptance-") {
		t.Fatal("ARCENTRA_DURABLE_SESSION_NAMESPACE must name the dedicated arcentra-durable-acceptance-* namespace")
	}
	privateScenarioRoot := func(name string) string {
		t.Helper()
		base := required(name)
		// Validate the operator roots using the same production trust boundary.
		root, err := openJournalDirectory(context.Background(), base)
		if err != nil {
			t.Fatalf("invalid explicit %s: %v", name, err)
		}
		if err := root.Close(); err != nil {
			t.Fatal(err)
		}
		path, err := os.MkdirTemp(base, scenario+"-")
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	controllerHash := sha256.Sum256([]byte("https://durable-acceptance.controller.invalid|verified-tls-test-binding-v1"))
	opts := ControllerOptions{
		JournalRoot:   privateScenarioRoot("ARCENTRA_DURABLE_SESSION_JOURNAL_ROOT"),
		WorkspaceRoot: privateScenarioRoot("ARCENTRA_DURABLE_SESSION_WORKSPACE_ROOT"),
		ControllerID:  hex.EncodeToString(controllerHash[:]), AgentID: "durable-acceptance-agent", MaxRecords: 16,
	}
	runtimeRoot := required("ARCENTRA_CONTAINERD_RUNC_ROOT")
	if !filepath.IsAbs(runtimeRoot) || filepath.Clean(runtimeRoot) != runtimeRoot || runtimeRoot == "/run/containerd/runc" || runtimeRoot == "/" {
		t.Fatal("ARCENTRA_CONTAINERD_RUNC_ROOT must name the dedicated daemon's absolute private runc state directory")
	}
	return &sandbox.ContainerdConfig{UnixSocket: socket, Namespace: namespace, NetworkMode: "none", RuntimeRoot: runtimeRoot}, opts, image
}

func acceptanceSessionOptions(t *testing.T, image, user string) Options {
	t.Helper()
	options := Options{PipelineRunID: uuid.NewString(), JobRunID: uuid.NewString(), ExecutionClaimID: uuid.NewString(), Image: image, User: user, Resources: &sandbox.Resources{CPU: "500m", Memory: "64Mi"}}
	request := &taskqueue.RuntimeRequest{Type: taskqueue.RuntimeContainerdOfflineV1, Image: image, Resources: taskqueue.RuntimeResources{CPU: "500m", Memory: "64Mi"}}
	var err error
	options.RuntimeBindingDigest, err = taskqueue.RuntimeBindingDigest("durable-acceptance-pipeline", options.PipelineRunID, options.JobRunID, "durable-acceptance-agent", request)
	if err != nil {
		t.Fatal(err)
	}
	return options
}

func containsAcceptanceRecovery(recoveries []Recovery, pipeline, job, claim, digest string) bool {
	for _, recovery := range recoveries {
		if recovery.PipelineRunID == pipeline && recovery.JobRunID == job && recovery.ExecutionClaimID == claim && recovery.RuntimeBindingDigest == digest && recovery.CleanupComplete {
			return true
		}
	}
	return false
}

func assertAcceptanceQuiescent(t *testing.T, ctx context.Context, client *containerd.Client, ref sandbox.ResourceRef) {
	t.Helper()
	ctx = namespaces.WithNamespace(ctx, ref.Namespace)
	container, err := client.LoadContainer(ctx, ref.ID)
	if err != nil {
		t.Fatalf("Seal removed container metadata instead of preserving it: %v", err)
	}
	if _, err := client.SnapshotService(ref.Snapshotter).Stat(ctx, ref.ID+"-snapshot"); err != nil {
		t.Fatalf("Seal removed exact snapshot instead of preserving it: %v", err)
	}
	task, err := container.Task(ctx, nil)
	if err != nil {
		t.Fatalf("Seal removed the task instead of retaining stopped state: %v", err)
	}
	status, err := task.Status(ctx)
	if err != nil || status.Status != containerd.Stopped {
		t.Fatalf("task did not stop: %+v %v", status, err)
	}
	pids, err := task.Pids(ctx)
	if err != nil && !errdefs.IsNotFound(err) || len(pids) != 0 {
		t.Fatalf("container descendants remain after quiescence: %+v %v", pids, err)
	}
}

func assertAcceptanceRemoved(t *testing.T, ctx context.Context, client *containerd.Client, ref sandbox.ResourceRef, workspace string) {
	t.Helper()
	ctx = namespaces.WithNamespace(ctx, ref.Namespace)
	if _, err := client.LoadContainer(ctx, ref.ID); !errdefs.IsNotFound(err) {
		t.Fatalf("exact container remains after cleanup: %v", err)
	}
	if _, err := client.TaskService().Get(ctx, &tasks.GetRequest{ContainerID: ref.ID}); !errdefs.IsNotFound(errdefs.FromGRPC(err)) {
		t.Fatalf("exact task remains after cleanup: %v", err)
	}
	if _, err := client.SnapshotService(ref.Snapshotter).Stat(ctx, ref.ID+"-snapshot"); !errdefs.IsNotFound(err) {
		t.Fatalf("exact snapshot remains after cleanup: %v", err)
	}
	if _, err := os.Lstat(workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("exact workspace remains after cleanup: %v", err)
	}
}

func assertAcceptanceWriterStopped(t *testing.T, path string) {
	t.Helper()
	before, err := os.ReadFile(path)
	if err != nil || len(before) == 0 {
		t.Fatalf("background writer did not produce evidence: %v", err)
	}
	time.Sleep(250 * time.Millisecond)
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("background writer continued after cancellation: before=%d after=%d err=%v", len(before), len(after), err)
	}
}

func runDurableCancellationAcceptance(t *testing.T, ctx context.Context, controller *Controller, client *containerd.Client, image, user, scenario string) {
	t.Helper()
	session, err := controller.Allocate(ctx, acceptanceSessionOptions(t, image, user))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := session.Close(context.Background()); err != nil {
			t.Errorf("%s fixture cleanup failed: %v", scenario, err)
		}
	})
	if err := session.Start(ctx); err != nil {
		t.Fatal(err)
	}
	writer := `(while :; do printf 'tick\n' >> /workspace/background.txt; sleep 0.05; done) >/dev/null 2>&1 &`
	if _, err := session.Execute(ctx, requestForDurableAcceptance(session, writer)); err != nil {
		t.Fatal(err)
	}
	writerPath := filepath.Join(session.Workspace(), "background.txt")
	if err := waitForAcceptanceWriter(ctx, writerPath); err != nil {
		t.Fatal(err)
	}
	step := requestForDurableAcceptance(session, "printf started > /workspace/active-step; sleep 60")
	if scenario == "timeout" {
		step.Options = &executor.ExecutionOptions{Timeout: time.Second}
		if _, err := session.Execute(ctx, step); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline was not propagated: %v", err)
		}
	} else {
		executeCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		done := make(chan error, 1)
		safe.Go(func() { _, err := session.Execute(executeCtx, step); done <- err })
		readyCtx, stopWaiting := context.WithTimeout(ctx, 10*time.Second)
		defer stopWaiting()
		for {
			if _, err := os.Stat(filepath.Join(session.Workspace(), "active-step")); err == nil {
				break
			}
			select {
			case err := <-done:
				t.Fatalf("step returned before active cancellation: %v", err)
			case <-readyCtx.Done():
				t.Fatal("step never reached the live container")
			case <-time.After(10 * time.Millisecond):
			}
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("active cancellation was not propagated: %v", err)
			}
		case <-ctx.Done():
			t.Fatal("active cancellation never returned")
		}
	}
	if data, err := os.ReadFile(filepath.Join(session.Workspace(), "active-step")); err != nil || string(data) != "started" {
		t.Fatalf("%s did not exercise a started container command: %q %v", scenario, data, err)
	}
	assertAcceptanceQuiescent(t, ctx, client, session.resourceRef)
	assertAcceptanceWriterStopped(t, writerPath)
	if _, err := session.Execute(ctx, requestForDurableAcceptance(session, "true")); err == nil {
		t.Fatal("canceled or timed-out session accepted another command")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := session.Close(canceled); err != nil {
		t.Fatalf("cleanup inherited canceled execution context: %v", err)
	}
	if err := session.Close(canceled); err != nil {
		t.Fatalf("repeated exact cleanup failed: %v", err)
	}
	assertAcceptanceRemoved(t, ctx, client, session.resourceRef, session.Workspace())
}
