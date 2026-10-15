//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandboxexec

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/sandbox"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/namespaces"
	"github.com/google/uuid"
)

const stepRestartAcceptanceFixtureEnv = "ARCENTRA_STEP_RESTART_ACCEPTANCE_CHILD_FIXTURE"

var stepRestartAcceptancePhases = []string{
	"intent", "admission", "consumed", "failed", "successor-intent", "successor-admission", "successor-consumed",
}

type stepRestartAcceptanceSession struct {
	Phase      string
	Options    Options
	Generation taskpayload.JobGeneration
}

type stepRestartAcceptanceFixture struct {
	Config     sandbox.ContainerdConfig
	Controller ControllerOptions
	Sessions   []stepRestartAcceptanceSession
	Receipt    string
}

type stepRestartAcceptanceReceipt struct {
	Resources []activeAcceptanceReceipt
	Attempts  map[string]stepAttemptJournal
}

type stepRestartAcceptanceEvents struct {
	mu     sync.Mutex
	counts map[string]map[string]int
	errors []string
}

func (e *stepRestartAcceptanceEvents) observe(_ context.Context, event sandbox.ContainerAcceptanceEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.counts == nil {
		e.counts = make(map[string]map[string]int)
	}
	if e.counts[event.Resource.ID] == nil {
		e.counts[event.Resource.ID] = make(map[string]int)
	}
	e.counts[event.Resource.ID][event.Operation]++
	if event.Err != nil {
		e.errors = append(e.errors, fmt.Sprintf("%s %s: %v", event.Resource.ID, event.Operation, event.Err))
	}
}

func (e *stepRestartAcceptanceEvents) assert(t *testing.T, ref sandbox.ResourceRef, want map[string]int) {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.errors) != 0 {
		t.Fatalf("real SDK observer recorded failed fixture effects: %v", e.errors)
	}
	for _, operation := range []string{"create", "task-start", "exec-create", "exec-start", "exec-delete", "pause", "resume"} {
		if got := e.counts[ref.ID][operation]; got != want[operation] {
			t.Errorf("%s real SDK %s calls: got %d, want %d", ref.ID, operation, got, want[operation])
		}
	}
}

func (e *stepRestartAcceptanceEvents) assertCleanupOnly(t *testing.T) {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	// Observe every resource, including an unexpected ID; checking just the
	// recorded containers would miss an erroneous newly-created retry session.
	if len(e.counts) != 0 || len(e.errors) != 0 {
		t.Fatalf("restart attempted real SDK execution effects: calls=%v errors=%v", e.counts, e.errors)
	}
}

// TestLiveStepJournalRestartContainerdAcceptance 验证真实运行时七个中断点仅按准确身份清理
// 独立子进程直接退出；恢复不得 Resume、Exec 或从审计重建活动会话，业务墓碑保留待原认领 ACK
func TestLiveStepJournalRestartContainerdAcceptance(t *testing.T) {
	if input := os.Getenv(stepRestartAcceptanceFixtureEnv); input != "" {
		createStepRestartAcceptanceFixture(t, input)
		return
	}
	config, opts, image := durableAcceptanceConfig(t, "step-restart")
	// This is an explicitly injected acceptance candidate, never the production gate.
	config.EnableLiveStepRetry = true
	opts.MaxRecords = 32 // Seven generation admissions, sessions and two reserved attempts each.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	backend, err := sandbox.NewContainerdSandbox(config, log.Logger{})
	if err != nil {
		t.Fatal(err)
	}
	var observed stepRestartAcceptanceEvents
	if err := sandbox.ObserveContainerAcceptanceRuntime(backend, sandbox.ContainerAcceptanceObserver{Observe: observed.observe}); err != nil {
		_ = backend.Close()
		t.Fatal(err)
	}
	var controller *Controller
	var childStarted bool
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), cleanupTimeout)
		defer stop()
		if controller == nil && childStarted && backend != nil {
			controller, err = OpenController(cleanup, backend, opts)
			if err != nil {
				t.Errorf("step restart exact recovery failed; preserve journal %s: %v", opts.JournalRoot, err)
			}
		}
		if controller != nil {
			if err := controller.Close(cleanup); err != nil {
				t.Errorf("close recovered step controller: %v", err)
			}
		}
		if backend != nil {
			if err := backend.Close(); err != nil {
				t.Errorf("close exact recovery backend: %v", err)
			}
		}
	})
	client, err := containerd.New(config.UnixSocket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close step restart inspector: %v", err)
		}
	})
	user := fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid())
	foreignBackend, err := sandbox.NewContainerdSandbox(config, log.Logger{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := foreignBackend.Close(); err != nil {
			t.Errorf("exact foreign-fixture cleanup: %v", err)
		}
	})
	foreignRef, err := foreignBackend.PrepareResource(ctx, "arcentra-"+uuid.NewString(), "step-restart-foreign-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := foreignBackend.Create(ctx, &sandbox.CreateOptions{
		Resource: &foreignRef, Image: image, User: user, NetworkMode: "none", WorkingDir: "/",
		Command: []string{"tail", "-f", "/dev/null"}, Resources: &sandbox.Resources{CPU: "500m", Memory: "64Mi"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := foreignBackend.Start(ctx, foreignRef.ID); err != nil {
		t.Fatal(err)
	}
	foreignWorkspace := filepath.Join(opts.WorkspaceRoot, "unrecorded-step-restart-workspace")
	if err := os.Mkdir(foreignWorkspace, 0o700); err != nil {
		t.Fatal(err)
	}
	foreignMarker := filepath.Join(foreignWorkspace, "must-survive")
	if err := os.WriteFile(foreignMarker, []byte("foreign-workspace-untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := stepRestartAcceptanceFixture{Config: *config, Controller: opts, Receipt: filepath.Join(t.TempDir(), "receipt.json")}
	for _, phase := range stepRestartAcceptancePhases {
		options := acceptanceSessionOptions(t, image, user)
		options.StepRetryPlan = &taskpayload.JobRetryPlan{
			Version: 2, Policy: &pipelinev1.Retry{Mode: "failed_step", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{7}},
			DefinitionDigest: "v1:" + strings.Repeat("e", 64),
			Steps: []taskpayload.RetryStepPlan{{Name: "restart-shell", Uses: "shell", StepIndex: 0,
				AttemptRunIDs: []string{uuid.NewString(), uuid.NewString()}, DefinitionDigest: "v1:" + strings.Repeat("a", 64)}},
		}
		generation := taskpayload.JobGeneration{Version: 1, PipelineID: "durable-acceptance-pipeline", PipelineRunID: options.PipelineRunID,
			JobRunID: options.JobRunID, AgentID: opts.AgentID, OriginalClaimID: options.ExecutionClaimID, RuntimeBindingDigest: options.RuntimeBindingDigest,
			InputManifestDigest: "v1:" + strings.Repeat("b", 64), GenerationID: uuid.NewString(), RuntimeClaimID: uuid.NewString(), Number: 1, ExecutionAllowed: true}
		generation.RetryPlanDigest, err = options.StepRetryPlan.Digest(generation.PipelineID, generation.PipelineRunID, generation.JobRunID, generation.AgentID, generation.RuntimeBindingDigest)
		if err != nil {
			t.Fatal(err)
		}
		bindGeneration(t, &generation)
		fixture.Sessions = append(fixture.Sessions, stepRestartAcceptanceSession{Phase: phase, Options: options, Generation: generation})
	}
	input := filepath.Join(filepath.Dir(fixture.Receipt), "fixture.json")
	data, err := sonic.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLiveStepJournalRestartContainerdAcceptance$", "-test.v")
	command.Env = append(os.Environ(), stepRestartAcceptanceFixtureEnv+"="+input)
	childStarted = true
	output, childErr := command.CombinedOutput()
	if childErr != nil {
		t.Fatalf("step Agent fixture did not reach intentional process exit: %v\n%s", childErr, output)
	}
	var receipt stepRestartAcceptanceReceipt
	data, err = os.ReadFile(fixture.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := sonic.Unmarshal(data, &receipt); err != nil || len(receipt.Resources) != len(fixture.Sessions) || len(receipt.Attempts) != 10 {
		t.Fatalf("invalid crash-fixture resource/attempt receipt: %v", err)
	}
	audit := make(map[string][]byte, len(receipt.Attempts))
	for name := range receipt.Attempts {
		if filepath.Base(name) != name || !strings.HasPrefix(name, "step-") || !strings.HasSuffix(name, ".json") {
			t.Fatal("invalid step audit receipt filename")
		}
		audit[name], err = os.ReadFile(filepath.Join(opts.JournalRoot, name))
		if err != nil {
			t.Fatal(err)
		}
	}
	for i, candidate := range fixture.Sessions {
		resource := receipt.Resources[i]
		if resource.Resource.ID != "arcentra-"+candidate.Generation.RuntimeClaimID || resource.Resource.Namespace != config.Namespace ||
			resource.Resource.Owner == "" || resource.Resource.Snapshotter == "" || resource.Workspace != filepath.Join(opts.WorkspaceRoot, "job-"+candidate.Generation.RuntimeClaimID) {
			t.Fatalf("%s fixture identity does not match its exact generation: %+v", candidate.Phase, resource)
		}
		assertStepRestartRuntimeState(t, ctx, client, resource.Resource, candidate.Phase)
		wrong := resource.Resource
		wrong.Owner = "different-owner-" + uuid.NewString()
		if _, err := backend.InspectOwned(ctx, wrong); !errdefs.IsFailedPrecondition(err) {
			t.Fatalf("%s wrong owner inspection was not rejected: %v", candidate.Phase, err)
		}
		if err := backend.RemoveOwned(ctx, wrong); err == nil {
			t.Fatalf("%s wrong owner removed a live restart fixture", candidate.Phase)
		}
		assertStepRestartRuntimeState(t, ctx, client, resource.Resource, candidate.Phase)
	}
	controller, err = OpenController(ctx, backend, opts)
	if err != nil {
		t.Fatal(err)
	}
	assertPending := func() {
		t.Helper()
		observed.assertCleanupOnly(t)
		if controller.Ready() || len(controller.sessions) != 0 || len(controller.Recoveries()) != len(fixture.Sessions) || !reflect.DeepEqual(controller.stepAttempts, receipt.Attempts) {
			t.Fatalf("restart reconstructed execution or lost immutable audit: ready=%v sessions=%d recoveries=%d attempts=%d", controller.Ready(), len(controller.sessions), len(controller.Recoveries()), len(controller.stepAttempts))
		}
		for name, before := range audit {
			after, err := os.ReadFile(filepath.Join(opts.JournalRoot, name))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("recovery changed unacknowledged step audit %s: %v", name, err)
			}
		}
	}
	assertPending()
	// A second real client must still see audit-only recovery after the crash.
	if err := controller.Close(ctx); err != nil {
		t.Fatal(err)
	}
	controller = nil
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	backend, err = sandbox.NewContainerdSandbox(config, log.Logger{})
	if err != nil {
		t.Fatal(err)
	}
	if err := sandbox.ObserveContainerAcceptanceRuntime(backend, sandbox.ContainerAcceptanceObserver{Observe: observed.observe}); err != nil {
		t.Fatal(err)
	}
	controller, err = OpenController(ctx, backend, opts)
	if err != nil {
		t.Fatal(err)
	}
	assertPending()
	for i, candidate := range fixture.Sessions {
		t.Run(candidate.Phase, func(t *testing.T) {
			resource, generation := receipt.Resources[i], candidate.Generation
			assertStepRestartAttemptPhase(t, candidate, receipt.Attempts)
			assertAcceptanceRemoved(t, ctx, client, resource.Resource, resource.Workspace)
			if !containsAcceptanceRecovery(controller.Recoveries(), generation.PipelineRunID, generation.JobRunID, generation.OriginalClaimID, generation.RuntimeBindingDigest) {
				t.Fatal("restart lost the original claim's pending cleanup outcome")
			}
			record := controller.records[generation.RuntimeClaimID]
			if record.CleanupReceipt == nil || record.CleanupReceipt.ValidateGeneration(generation) != nil || record.Generation == nil || *record.Generation != generation {
				t.Fatal("restart lost exact generation cleanup evidence")
			}
			if err := controller.AcknowledgeCompletion(ctx, generation.RuntimeClaimID); err == nil {
				t.Fatal("runtime claim was accepted in place of the original completion claim")
			}
			if _, err := controller.AllocateStepRetryGeneration(ctx, candidate.Options, generation); err == nil {
				t.Fatal("recovered step admission reconstructed an executable session")
			}
			if err := backend.RemoveOwned(ctx, resource.Resource); err != nil {
				t.Fatalf("exact cleanup was not idempotent: %v", err)
			}
			observed.assertCleanupOnly(t)
		})
	}
	assertPending()
	foreignState, err := foreignBackend.InspectOwned(ctx, foreignRef)
	if err != nil || !foreignState.ContainerExists || !foreignState.SnapshotExists || !foreignState.TaskExists {
		t.Fatalf("restart touched foreign runtime resources: %+v %v", foreignState, err)
	}
	foreignResult, err := foreignBackend.Execute(ctx, foreignRef.ID, []string{"sh", "-ec", "printf foreign-still-running"}, &sandbox.ExecuteOptions{User: user})
	if err != nil || foreignResult == nil || foreignResult.ExitCode != 0 || foreignResult.Stdout != "foreign-still-running" {
		t.Fatalf("restart stopped the foreign task: %+v %v", foreignResult, err)
	}
	marker, err := os.ReadFile(foreignMarker)
	if err != nil || string(marker) != "foreign-workspace-untouched" {
		t.Fatalf("restart changed the unrecorded workspace: %q %v", marker, err)
	}
	// There is no authoritative control-plane business completion in this runtime
	// fixture. Do not fabricate its ACK: retain all seven original-claim tombstones
	// and ten step audits. Ordinary tests separately cover the local pruning API
	// when its caller supplies the original claim's completion acknowledgement.
	t.Logf("verified seven real process-death phases with zero recovery Resume/Exec/Create/Start, exact cleanup, and foreign preservation; original-claim ACKs remain pending in %s", opts.JournalRoot)
}

func assertStepRestartAttemptPhase(t *testing.T, candidate stepRestartAcceptanceSession, attempts map[string]stepAttemptJournal) {
	t.Helper()
	successor := strings.HasPrefix(candidate.Phase, "successor-")
	wantCount := 1
	if successor {
		wantCount++
	}
	count := 0
	for name, attempt := range attempts {
		if attempt.RecordID != candidate.Generation.RuntimeClaimID {
			continue
		}
		count++
		if name != stepAttemptFilename(attempt.Request) || attempt.FinalAcknowledged || attempt.Observation != nil ||
			attempt.Request.Scope.Identity.ClaimID != candidate.Generation.OriginalClaimID || attempt.Request.Scope.GenerationID != candidate.Generation.GenerationID {
			t.Fatal("crash audit lost exact unacknowledged original generation identity")
		}
		switch attempt.Request.Scope.Attempt {
		case 1:
			wantConsumed := candidate.Phase == "consumed" || candidate.Phase == "failed" || successor
			if attempt.Request.Operation != "admit" || (attempt.Admission != nil) != (candidate.Phase != "intent") || attempt.Consumed != wantConsumed {
				t.Fatalf("%s first attempt did not persist its exact crash phase: %+v", candidate.Phase, attempt)
			}
		case 2:
			if !successor || attempt.Request.Operation != "advance" || attempt.Request.Boundary == nil ||
				(attempt.Admission != nil) != (candidate.Phase != "successor-intent") || attempt.Consumed != (candidate.Phase == "successor-consumed") {
				t.Fatalf("%s successor did not persist its exact crash phase: %+v", candidate.Phase, attempt)
			}
		default:
			t.Fatal("crash audit contains an unplanned attempt")
		}
	}
	if count != wantCount {
		t.Fatalf("%s audit count: got %d, want %d", candidate.Phase, count, wantCount)
	}
}

func assertStepRestartRuntimeState(t *testing.T, ctx context.Context, client *containerd.Client, ref sandbox.ResourceRef, phase string) {
	t.Helper()
	ctx = namespaces.WithNamespace(ctx, ref.Namespace)
	container, err := client.LoadContainer(ctx, ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	task, err := container.Task(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := containerd.Running
	if phase == "failed" || strings.HasPrefix(phase, "successor-") {
		want = containerd.Paused
	}
	status, err := task.Status(ctx)
	if err != nil || status.Status != want || task.Pid() == 0 {
		t.Fatalf("%s process death did not leave the expected actual init: status=%+v want=%s error=%v", phase, status, want, err)
	}
	pids, err := task.Pids(ctx)
	if err != nil || len(pids) != 1 || pids[0].Pid != task.Pid() {
		t.Fatalf("%s fixture does not contain only its real live init: %+v %v", phase, pids, err)
	}
	if _, err := client.SnapshotService(ref.Snapshotter).Stat(ctx, ref.ID+"-snapshot"); err != nil {
		t.Fatalf("%s fixture snapshot is missing before recovery: %v", phase, err)
	}
}

func createStepRestartAcceptanceFixture(t *testing.T, input string) {
	t.Helper()
	if os.Getenv("ARCENTRA_DURABLE_SESSION_ACCEPTANCE") != "1" || os.Geteuid() == 0 || os.Getegid() == 0 {
		t.Fatal("step crash fixture requires explicit authorization and a nonroot Agent process")
	}
	var fixture stepRestartAcceptanceFixture
	data, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := sonic.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Config.UnixSocket != os.Getenv("ARCENTRA_CONTAINERD_SOCKET") || fixture.Config.Namespace != os.Getenv("ARCENTRA_DURABLE_SESSION_NAMESPACE") ||
		fixture.Config.RuntimeRoot != os.Getenv("ARCENTRA_CONTAINERD_RUNC_ROOT") || fixture.Config.FIFODir != os.Getenv("ARCENTRA_CONTAINERD_FIFO_DIR") ||
		!fixture.Config.EnableLiveStepRetry || filepath.Dir(fixture.Controller.JournalRoot) != os.Getenv("ARCENTRA_DURABLE_SESSION_JOURNAL_ROOT") ||
		filepath.Dir(fixture.Controller.WorkspaceRoot) != os.Getenv("ARCENTRA_DURABLE_SESSION_WORKSPACE_ROOT") || len(fixture.Sessions) != len(stepRestartAcceptancePhases) {
		t.Fatal("step crash fixture escaped the approved acceptance environment")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	backend, err := sandbox.NewContainerdSandbox(&fixture.Config, log.Logger{})
	if err != nil {
		t.Fatal(err)
	}
	var observed stepRestartAcceptanceEvents
	if err := sandbox.ObserveContainerAcceptanceRuntime(backend, sandbox.ContainerAcceptanceObserver{Observe: observed.observe}); err != nil {
		t.Fatal(err)
	}
	controller, err := OpenController(ctx, backend, fixture.Controller)
	if err != nil {
		t.Fatal(err)
	}
	var sessions []*Session
	// Allocate all exact identities before creating any unresolved step handoff.
	for i, candidate := range fixture.Sessions {
		if candidate.Phase != stepRestartAcceptancePhases[i] || candidate.Options.Image != os.Getenv("ARCENTRA_SANDBOX_TEST_IMAGE") || candidate.Options.User != fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid()) {
			t.Fatal("step crash fixture changed the approved phase, image or Agent user")
		}
		request := initialGenerationRequest(t, candidate.Generation)
		if err := controller.RecordGenerationIntent(ctx, request); err != nil {
			t.Fatal(err)
		}
		if err := controller.RecordGenerationAdmission(ctx, request, candidate.Generation); err != nil {
			t.Fatal(err)
		}
		session, err := controller.AllocateStepRetryGeneration(ctx, candidate.Options, candidate.Generation)
		if err != nil {
			t.Fatal(err)
		}
		if err := session.Start(ctx); err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, session)
	}
	receipt := stepRestartAcceptanceReceipt{Attempts: controller.stepAttempts}
	for i, candidate := range fixture.Sessions {
		session, phase := sessions[i], candidate.Phase
		request := stepIntent(t, session, candidate.Generation, 0)
		if err := session.RecordStepAttemptIntent(ctx, request); err != nil {
			t.Fatal(err)
		}
		admission := stepAdmission(t, request)
		if phase != "intent" {
			if err := session.RecordStepAttemptAdmission(ctx, request, admission); err != nil {
				t.Fatal(err)
			}
		}
		if phase == "consumed" {
			consumeStepRestartAcceptance(t, ctx, session, admission)
		}
		want := map[string]int{"create": 1, "task-start": 1}
		if phase == "failed" || strings.HasPrefix(phase, "successor-") {
			execution := stepRequest(admission, session)
			execution.Step.Args = map[string]any{"command": "printf failed-once > /workspace/failed-attempt; exit 7"}
			result, err := session.ExecuteStepAttempt(ctx, execution, admission)
			if !IsCommandExit(err) || result == nil || result.ExitCode != 7 {
				t.Fatalf("%s actual failure did not yield a pure exit boundary: %+v %v", phase, result, err)
			}
			if marker, err := os.ReadFile(filepath.Join(session.Workspace(), "failed-attempt")); err != nil || string(marker) != "failed-once" {
				t.Fatalf("%s actual failed command left no workspace evidence: %q %v", phase, marker, err)
			}
			if err := session.HasPausedStepBoundary(ctx, admission); err != nil {
				t.Fatal(err)
			}
			want["exec-create"], want["exec-start"], want["exec-delete"], want["pause"] = 1, 1, 1, 1
			if strings.HasPrefix(phase, "successor-") {
				request = advanceStep(t, session, admission)
				if err := session.RecordStepAttemptIntent(ctx, request); err != nil {
					t.Fatal(err)
				}
				admission = stepAdmission(t, request)
				if phase != "successor-intent" {
					if err := session.RecordStepAttemptAdmission(ctx, request, admission); err != nil {
						t.Fatal(err)
					}
				}
				if phase == "successor-consumed" {
					consumeStepRestartAcceptance(t, ctx, session, admission)
				}
			}
		}
		observed.assert(t, session.resourceRef, want)
		receipt.Resources = append(receipt.Resources, activeAcceptanceReceipt{Resource: session.resourceRef, Workspace: session.Workspace()})
	}
	if t.Failed() {
		return
	}
	data, err = sonic.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.Receipt, data, 0o600); err != nil {
		t.Fatal(err)
	}
	// No cleanup, handle close or synthetic shutdown: this intentionally destroys
	// all in-memory capabilities while actual running/paused tasks outlive the Agent.
	os.Exit(0)
}

func consumeStepRestartAcceptance(t *testing.T, ctx context.Context, session *Session, admission taskpayload.StepAttemptAdmission) {
	t.Helper()
	if err := session.acquire(ctx); err != nil {
		t.Fatal(err)
	}
	// The precise crash window is after the production durable consume operation
	// and before executeLiveStep can call either the real Exec or Resume.
	err := session.consumeStepAttempt(ctx, admission, false)
	session.release()
	if err != nil {
		t.Fatal(err)
	}
}
