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
	"github.com/arcentrix/arcentra/pkg/sandbox"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/google/uuid"
)

// TestDurableControllerContainerdAcceptance 仅在明确授权的非 root 专用主机验证持久生命周期
// 编译测试必须使用 -run '^$'，实际运行另需独立开关及全部显式环境参数
func TestDurableControllerContainerdAcceptance(t *testing.T) {
	if os.Getenv("ARCENTRA_DURABLE_SESSION_ACCEPTANCE") != "1" {
		t.Fatal("durable acceptance was not run: set ARCENTRA_DURABLE_SESSION_ACCEPTANCE=1 only after explicit dedicated-host authorization")
	}
	if runtime.GOOS != "linux" || os.Geteuid() == 0 || os.Getegid() == 0 {
		t.Fatal("durable acceptance requires an unprivileged nonzero Linux Agent UID:GID; do not use sudo")
	}
	required := func(name string) string {
		t.Helper()
		value := os.Getenv(name)
		if strings.TrimSpace(value) == "" {
			t.Fatalf("durable acceptance requires explicit %s", name)
		}
		return value
	}
	socket := required("ARCENTRA_CONTAINERD_SOCKET")
	image := required("ARCENTRA_SANDBOX_TEST_IMAGE")
	namespace := required("ARCENTRA_DURABLE_SESSION_NAMESPACE")
	if !strings.HasPrefix(namespace, "arcentra-durable-acceptance-") {
		t.Fatal("ARCENTRA_DURABLE_SESSION_NAMESPACE must name a pre-authorized dedicated arcentra-durable-acceptance-* namespace")
	}
	if info, err := os.Lstat(socket); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("explicit containerd socket is unavailable or is not a socket: %v", err)
	}
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		t.Fatalf("dedicated acceptance host requires cgroup v2: %v", err)
	}
	controllerHash := sha256.Sum256([]byte("https://durable-acceptance.controller.invalid|verified-tls-test-binding-v1"))
	opts := ControllerOptions{
		JournalRoot:   required("ARCENTRA_DURABLE_SESSION_JOURNAL_ROOT"),
		WorkspaceRoot: required("ARCENTRA_DURABLE_SESSION_WORKSPACE_ROOT"),
		ControllerID:  hex.EncodeToString(controllerHash[:]), AgentID: "durable-acceptance-agent", MaxRecords: 16,
	}
	user := fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid())
	runID, jobID, claimID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	request := &taskqueue.RuntimeRequest{Type: taskqueue.RuntimeContainerdOfflineV1, Image: image, Resources: taskqueue.RuntimeResources{CPU: "500m", Memory: "64Mi"}}
	digest, err := taskqueue.RuntimeBindingDigest("durable-acceptance-pipeline", runID, jobID, opts.AgentID, request)
	if err != nil {
		t.Fatalf("acceptance requires an immutable approved image@sha256 and valid runtime request: %v", err)
	}
	config := &sandbox.ContainerdConfig{UnixSocket: socket, Namespace: namespace, NetworkMode: "none"}
	backend, err := sandbox.NewContainerdSandbox(config, log.Logger{})
	if err != nil {
		t.Fatal(err)
	}
	foreignBackend, err := sandbox.NewContainerdSandbox(config, log.Logger{})
	if err != nil {
		_ = backend.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var controller *Controller
	var session *Session
	var foreignRef sandbox.ResourceRef
	var journalValidated bool
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
	if controller.Ready() || len(recoveries) != 1 || recoveries[0].JobRunID != jobID || recoveries[0].ExecutionClaimID != claimID || recoveries[0].RuntimeBindingDigest != digest || !recoveries[0].CleanupComplete {
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
