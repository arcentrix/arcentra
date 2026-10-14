//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandboxexec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/sandbox"
	"github.com/bytedance/sonic"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/namespaces"
	"github.com/google/uuid"
)

const activeAcceptanceFixtureEnv = "ARCENTRA_DURABLE_ACCEPTANCE_CHILD_FIXTURE"

type activeAcceptanceFixture struct {
	Config     sandbox.ContainerdConfig
	Controller ControllerOptions
	Session    Options
	Receipt    string
}

type activeAcceptanceReceipt struct {
	Resource  sandbox.ResourceRef
	Workspace string
}

// A separate Agent process exits without any deferred cleanup. A new client must
// recover the still-running container using only the durable journal and exact refs.
func TestDurableControllerActiveRecoveryContainerdAcceptance(t *testing.T) {
	if input := os.Getenv(activeAcceptanceFixtureEnv); input != "" {
		createActiveAcceptanceFixture(t, input)
		return
	}
	config, opts, image := durableAcceptanceConfig(t, "active-restart")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	backend, err := sandbox.NewContainerdSandbox(config, log.Logger{})
	if err != nil {
		t.Fatal(err)
	}
	client, err := containerd.New(config.UnixSocket)
	if err != nil {
		_ = backend.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close acceptance inspector: %v", err)
		}
	})
	var controller *Controller
	var childStarted bool
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), cleanupTimeout)
		defer stop()
		if controller == nil && childStarted {
			// Re-run the production exact recovery path on assertion failures too.
			controller, err = OpenController(cleanup, backend, opts)
			if err != nil {
				t.Errorf("active fixture recovery failed; preserve journal %s: %v", opts.JournalRoot, err)
			}
		}
		if controller != nil {
			if err := controller.Close(cleanup); err != nil {
				t.Errorf("close recovered controller: %v", err)
			}
		}
		if err := backend.Close(); err != nil {
			t.Errorf("exact acceptance resource cleanup: %v", err)
		}
	})
	user := fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid())
	foreignRef, err := backend.PrepareResource(ctx, "arcentra-"+uuid.NewString(), "active-recovery-foreign-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Create(ctx, &sandbox.CreateOptions{
		Resource: &foreignRef, Image: image, User: user, NetworkMode: "none", WorkingDir: "/",
		Command:   []string{"sh", "-ec", "trap 'exit 0' TERM INT; while :; do sleep 1; done"},
		Resources: &sandbox.Resources{CPU: "500m", Memory: "64Mi"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := backend.Start(ctx, foreignRef.ID); err != nil {
		t.Fatal(err)
	}
	foreignWorkspace := filepath.Join(opts.WorkspaceRoot, "unrecorded-foreign-workspace")
	if err := os.Mkdir(foreignWorkspace, 0o700); err != nil {
		t.Fatal(err)
	}
	foreignMarker := filepath.Join(foreignWorkspace, "must-survive")
	if err := os.WriteFile(foreignMarker, []byte("foreign-workspace-untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	handoff := t.TempDir()
	fixture := activeAcceptanceFixture{
		Config: *config, Controller: opts, Session: acceptanceSessionOptions(t, image, user),
		Receipt: filepath.Join(handoff, "receipt.json"),
	}
	input := filepath.Join(handoff, "fixture.json")
	data, err := sonic.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDurableControllerActiveRecoveryContainerdAcceptance$", "-test.v")
	command.Env = append(os.Environ(), activeAcceptanceFixtureEnv+"="+input)
	childStarted = true
	output, childErr := command.CombinedOutput()
	if childErr != nil {
		t.Fatalf("active Agent fixture did not reach its intentional process exit: %v\n%s", childErr, output)
	}
	var receipt activeAcceptanceReceipt
	data, err = os.ReadFile(fixture.Receipt)
	if err != nil || sonic.Unmarshal(data, &receipt) != nil {
		t.Fatalf("active fixture receipt is unavailable: %v", err)
	}
	if receipt.Resource.Namespace != config.Namespace || receipt.Resource.ID == foreignRef.ID || receipt.Resource.Owner == "" || receipt.Resource.Snapshotter == "" || receipt.Workspace != filepath.Join(opts.WorkspaceRoot, "job-"+strings.TrimPrefix(receipt.Resource.ID, "arcentra-")) {
		t.Fatalf("inconsistent active fixture identity: %+v", receipt)
	}
	inspect := namespaces.WithNamespace(ctx, receipt.Resource.Namespace)
	active, err := client.LoadContainer(inspect, receipt.Resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	activeTask, err := active.Task(inspect, nil)
	if err != nil {
		t.Fatal(err)
	}
	status, err := activeTask.Status(inspect)
	if err != nil || status.Status != containerd.Running || activeTask.Pid() == 0 {
		t.Fatalf("child process exit did not leave a running runtime to recover: %+v %v", status, err)
	}
	writerPath := filepath.Join(receipt.Workspace, "background.txt")
	if err := waitForAcceptanceWriter(ctx, writerPath); err != nil {
		t.Fatal(err)
	}
	// Wrong refs must neither kill a task nor poison a later correct recovery.
	wrong := receipt.Resource
	wrong.Owner = "different-owner-" + uuid.NewString()
	if _, err := backend.InspectOwned(ctx, wrong); !errdefs.IsFailedPrecondition(err) {
		t.Fatalf("wrong owner inspection was not rejected: %v", err)
	}
	if err := backend.SealOwned(ctx, wrong); err == nil {
		t.Fatal("wrong owner sealed the live fixture")
	}
	if err := backend.RemoveOwned(ctx, wrong); err == nil {
		t.Fatal("wrong owner removed the live fixture")
	}
	// Simulate an externally changed ownership label only on this exact fixture.
	// Restore the known fixture label before our cleanup, never a foreign resource.
	const ownerLabel = "io.arcentra.sandbox.owner"
	labels, err := active.Labels(inspect)
	if err != nil || labels[ownerLabel] != receipt.Resource.Owner {
		t.Fatalf("cannot establish fixture owner before mutation: %v", err)
	}
	injectedOwner := "acceptance-conflicting-owner-" + uuid.NewString()
	if _, err := active.SetLabels(inspect, map[string]string{ownerLabel: injectedOwner}); err != nil {
		t.Fatal(err)
	}
	restored := false
	restore := func() error {
		if restored {
			return nil
		}
		cleanup, stop := context.WithTimeout(context.Background(), cleanupTimeout)
		defer stop()
		cleanup = namespaces.WithNamespace(cleanup, receipt.Resource.Namespace)
		labels, err := active.Labels(cleanup)
		if err != nil || labels[ownerLabel] != injectedOwner {
			return fmt.Errorf("fixture owner changed beyond injected test value; preserve evidence: %v", err)
		}
		_, err = active.SetLabels(cleanup, map[string]string{ownerLabel: receipt.Resource.Owner})
		restored = err == nil
		return err
	}
	t.Cleanup(func() {
		if err := restore(); err != nil {
			t.Errorf("restore exact ownership-conflict fixture: %v", err)
		}
	})
	rejected, err := OpenController(ctx, backend, opts)
	if err == nil || rejected != nil {
		controller = rejected
		t.Fatal("startup accepted a live container whose ownership differs from its journal")
	}
	status, err = activeTask.Status(inspect)
	if err != nil || status.Status != containerd.Running {
		t.Fatalf("ownership-conflict startup stopped an unverified task: %+v %v", status, err)
	}
	if _, err := client.SnapshotService(receipt.Resource.Snapshotter).Stat(inspect, receipt.Resource.ID+"-snapshot"); err != nil {
		t.Fatalf("ownership-conflict startup removed the snapshot: %v", err)
	}
	if err := waitForAcceptanceWriter(ctx, writerPath); err != nil {
		t.Fatal("ownership-conflict startup stopped or deleted the active workspace writer: ", err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	controller, err = OpenController(ctx, backend, opts)
	if err != nil {
		t.Fatal(err)
	}
	if controller.Ready() || !containsAcceptanceRecovery(controller.Recoveries(), fixture.Session.PipelineRunID, fixture.Session.JobRunID, fixture.Session.ExecutionClaimID, fixture.Session.RuntimeBindingDigest) || len(controller.Recoveries()) != 1 {
		t.Fatalf("active recovery lost original pending claim: ready=%v recoveries=%+v", controller.Ready(), controller.Recoveries())
	}
	assertAcceptanceRemoved(t, ctx, client, receipt.Resource, receipt.Workspace)
	if _, err := controller.Allocate(ctx, acceptanceSessionOptions(t, image, user)); err == nil {
		t.Fatal("unacknowledged interrupted execution admitted a new job")
	}
	foreignState, err := backend.InspectOwned(ctx, foreignRef)
	if err != nil || !foreignState.ContainerExists || !foreignState.SnapshotExists || !foreignState.TaskExists {
		t.Fatalf("active recovery touched foreign resources: %+v %v", foreignState, err)
	}
	foreignResult, err := backend.Execute(ctx, foreignRef.ID, []string{"sh", "-ec", "printf foreign-still-running"}, &sandbox.ExecuteOptions{User: user})
	if err != nil || foreignResult == nil || foreignResult.Stdout != "foreign-still-running" || foreignResult.ExitCode != 0 {
		t.Fatalf("active recovery stopped the foreign task: %+v %v", foreignResult, err)
	}
	marker, err := os.ReadFile(foreignMarker)
	if err != nil || string(marker) != "foreign-workspace-untouched" {
		t.Fatalf("active recovery removed or changed an unrecorded workspace: %q %v", marker, err)
	}
	if err := backend.RemoveOwned(ctx, receipt.Resource); err != nil {
		t.Fatalf("recovered exact cleanup is not idempotent: %v", err)
	}
	t.Logf("verified live Agent-process restart recovery, ownership-conflict refusal, foreign runtime/workspace preservation and exact task/snapshot/workspace cleanup; pending claim retained in %s", opts.JournalRoot)
}

func createActiveAcceptanceFixture(t *testing.T, input string) {
	t.Helper()
	if os.Getenv("ARCENTRA_DURABLE_SESSION_ACCEPTANCE") != "1" || os.Geteuid() == 0 || os.Getegid() == 0 {
		t.Fatal("the crash fixture requires explicit authorization and a nonroot Agent process")
	}
	var fixture activeAcceptanceFixture
	data, err := os.ReadFile(input)
	if err != nil || sonic.Unmarshal(data, &fixture) != nil {
		t.Fatalf("invalid private crash-fixture input: %v", err)
	}
	if fixture.Config.UnixSocket != os.Getenv("ARCENTRA_CONTAINERD_SOCKET") || fixture.Config.Namespace != os.Getenv("ARCENTRA_DURABLE_SESSION_NAMESPACE") || fixture.Config.RuntimeRoot != os.Getenv("ARCENTRA_CONTAINERD_RUNC_ROOT") || fixture.Config.FIFODir != os.Getenv("ARCENTRA_CONTAINERD_FIFO_DIR") || fixture.Session.Image != os.Getenv("ARCENTRA_SANDBOX_TEST_IMAGE") || filepath.Dir(fixture.Controller.JournalRoot) != os.Getenv("ARCENTRA_DURABLE_SESSION_JOURNAL_ROOT") || filepath.Dir(fixture.Controller.WorkspaceRoot) != os.Getenv("ARCENTRA_DURABLE_SESSION_WORKSPACE_ROOT") {
		t.Fatal("crash fixture escaped the approved acceptance environment")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	backend, err := sandbox.NewContainerdSandbox(&fixture.Config, log.Logger{})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := OpenController(ctx, backend, fixture.Controller)
	if err != nil {
		t.Fatal(err)
	}
	session, err := controller.Allocate(ctx, fixture.Session)
	if err != nil {
		t.Fatal(err)
	}
	data, err = sonic.Marshal(activeAcceptanceReceipt{Resource: session.resourceRef, Workspace: session.Workspace()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.Receipt, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := session.Start(ctx); err != nil {
		t.Fatal(err)
	}
	command := `(while :; do printf 'tick\n' >> /workspace/background.txt; sleep 0.05; done) >/dev/null 2>&1 &
printf live-agent-fixture`
	result, err := session.Execute(ctx, requestForDurableAcceptance(session, command))
	if err != nil || result == nil || result.Output != "live-agent-fixture" {
		t.Fatalf("active child command failed: %+v %v", result, err)
	}
	if err := waitForAcceptanceWriter(ctx, filepath.Join(session.Workspace(), "background.txt")); err != nil {
		t.Fatal(err)
	}
	// No Session.Close, Controller.Close or backend.Close: process death is the
	// failure being accepted. The parent owns exact journal-driven recovery.
	os.Exit(0)
}
