package taskqueue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
)

type fixtureOfflineSession struct {
	workspace                   string
	calls                       *[]string
	startErr, sealErr, closeErr error
}

func (s *fixtureOfflineSession) Name() string                               { return "fixture-container" }
func (s *fixtureOfflineSession) CanExecute(*executor.ExecutionRequest) bool { return true }
func (s *fixtureOfflineSession) Workspace() string                          { return s.workspace }
func (s *fixtureOfflineSession) Start(context.Context) error {
	*s.calls = append(*s.calls, "start")
	return s.startErr
}
func (s *fixtureOfflineSession) Seal(context.Context) error {
	*s.calls = append(*s.calls, "seal")
	return s.sealErr
}
func (s *fixtureOfflineSession) Close(context.Context) error {
	*s.calls = append(*s.calls, "close")
	return s.closeErr
}
func (s *fixtureOfflineSession) Execute(_ context.Context, req *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
	*s.calls = append(*s.calls, "execute")
	result := executor.NewExecutionResult(s.Name())
	result.Complete(true, 0, nil)
	return result, nil
}

type fixtureOfflineRuntime struct {
	session          *fixtureOfflineSession
	claim            string
	acknowledgements int
	ackFailures      int
}

func (r *fixtureOfflineRuntime) Allocate(_ context.Context, _ *taskpayload.JobRunTaskPayload, claim string) (offlineSession, error) {
	r.claim = claim
	*r.session.calls = append(*r.session.calls, "allocate")
	return r.session, nil
}
func (r *fixtureOfflineRuntime) HasJob(string) bool { return false }
func (r *fixtureOfflineRuntime) Acknowledge(_ context.Context, _ string, claim string) error {
	if claim != r.claim {
		return errors.New("claim changed")
	}
	if r.ackFailures > 0 {
		r.ackFailures--
		return errors.New("synthetic journal ACK failure")
	}
	r.acknowledgements++
	return nil
}

func offlineWorkerFixture(t *testing.T) (*config.AgentConfig, *taskpayload.JobRunTaskPayload, *fixtureOfflineRuntime) {
	t.Helper()
	image := "registry.example/approved@sha256:" + strings.Repeat("a", 64)
	conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "sandbox-agent", Mode: "sandbox", Sandbox: config.SandboxConfig{Enable: true, Runtime: "containerd", Containerd: config.ContainerdConfig{Policy: taskpayload.OfflineRuntimePolicy{Enabled: true, AllowedImages: []string{image}, MaxCPU: "1", MaxMemory: "128Mi"}}}}}
	payload := &taskpayload.JobRunTaskPayload{PipelineID: "p", PipelineRunID: "run", JobRunID: t.Name(), AgentID: conf.Agent.ID, Steps: []taskpayload.StepPayload{{Name: "shell", StepRunID: t.Name() + "-step", Uses: "shell", Args: map[string]any{"run": "touch host-fallback-marker"}}}, Runtime: &taskpayload.RuntimeRequest{Type: taskpayload.RuntimeContainerdOfflineV1, Image: image, Resources: taskpayload.RuntimeResources{CPU: "500m", Memory: "64Mi"}}}
	digest, err := taskpayload.RuntimeBindingDigest(payload.PipelineID, payload.PipelineRunID, payload.JobRunID, payload.AgentID, payload.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	payload.Runtime.BindingDigest = digest
	calls := []string{}
	runtime := &fixtureOfflineRuntime{session: &fixtureOfflineSession{workspace: t.TempDir(), calls: &calls}}
	return conf, payload, runtime
}
func TestOfflineWorkerNeverFallsBackToHost(t *testing.T) {
	conf, payload, runtime := offlineWorkerFixture(t)
	manager := executor.NewExecutorManager()
	manager.Register(executor.NewShellExecutor())
	client := &artifactStatusClient{}
	err := executeJobRun(withOfflineJobRuntime(context.Background(), runtime), conf, &agentgrpc.ClientWrapper{AgentClient: client}, manager, payload)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*runtime.session.calls, []string{"allocate", "start", "execute", "seal", "close"}) {
		t.Fatalf("lifecycle: %v", *runtime.session.calls)
	}
	if _, err := os.Stat(filepath.Join(runtime.session.workspace, "host-fallback-marker")); !os.IsNotExist(err) {
		t.Fatal("host shell was selected")
	}
	if runtime.acknowledgements != 1 {
		t.Fatalf("acknowledgements: %d", runtime.acknowledgements)
	}
	for _, report := range client.jobReports {
		if report.RuntimeBindingDigest != payload.Runtime.BindingDigest {
			t.Fatal("runtime binding missing from job report")
		}
	}
}
func TestOfflineWorkerStartupFailureNeverFallsBack(t *testing.T) {
	conf, payload, runtime := offlineWorkerFixture(t)
	runtime.session.startErr = errors.New("fixture runtime unavailable")
	manager := executor.NewExecutorManager()
	manager.Register(executor.NewShellExecutor())
	client := &artifactStatusClient{}
	err := executeJobRun(withOfflineJobRuntime(context.Background(), runtime), conf, &agentgrpc.ClientWrapper{AgentClient: client}, manager, payload)
	if err == nil {
		t.Fatal("runtime failure succeeded")
	}
	for _, call := range *runtime.session.calls {
		if call == "execute" {
			t.Fatal("executed after start failure")
		}
	}
	if got := client.jobReports[len(client.jobReports)-1].Status; got != 5 {
		t.Fatalf("expected failed job, got %d", got)
	}
}
func TestOfflineWorkerRequiresExplicitRuntimeAndReadySession(t *testing.T) {
	conf, payload, _ := offlineWorkerFixture(t)
	if err := validateOfflineJob(context.Background(), conf, payload); err == nil {
		t.Fatal("missing runtime readiness accepted")
	}
	payload.Runtime = nil
	if err := validateOfflineJob(context.Background(), conf, payload); err == nil {
		t.Fatal("sandbox job without explicit runtime accepted")
	}
}
func TestOfflineStagingCannotInterleave(t *testing.T) {
	calls := []string{}
	session := &offlineJobSession{offlineSession: &fixtureOfflineSession{calls: &calls}}
	ctx := context.Background()
	if err := session.beforeStep(ctx, taskpayload.StepPayload{Uses: "builtin:artifacts", Action: "download"}); err != nil {
		t.Fatal(err)
	}
	if err := session.beforeStep(ctx, taskpayload.StepPayload{Uses: "shell"}); err != nil {
		t.Fatal(err)
	}
	if err := session.beforeStep(ctx, taskpayload.StepPayload{Uses: "builtin:artifacts", Action: "download"}); err == nil {
		t.Fatal("host download during commands accepted")
	}
	if err := session.beforeStep(ctx, taskpayload.StepPayload{Uses: "builtin:artifacts", Action: "upload"}); err != nil {
		t.Fatal(err)
	}
	if err := session.beforeStep(ctx, taskpayload.StepPayload{Uses: "shell"}); err == nil {
		t.Fatal("command after seal accepted")
	}
	if !reflect.DeepEqual(calls, []string{"start", "seal"}) {
		t.Fatal(calls)
	}
}

func (r *fixtureOfflineRuntime) Ready() bool { return true }

func TestOfflineWorkerRequiresExplicitOperatorPolicy(t *testing.T) {
	conf, _, _ := offlineWorkerFixture(t)
	if err := ValidateExecutionConfig(conf); err != nil {
		t.Fatalf("accepted bounded worker policy rejected: %v", err)
	}
	conf.Agent.Sandbox.Containerd.Policy.Enabled = false
	if err := ValidateExecutionConfig(conf); err == nil {
		t.Fatal("disabled runtime policy enabled a worker")
	}
	conf.Agent.Sandbox.Containerd.Policy.Enabled = true
	conf.Agent.Sandbox.Containerd.Policy.AllowedImages = nil
	if err := ValidateExecutionConfig(conf); err == nil {
		t.Fatal("unbounded runtime policy enabled a worker")
	}
}

func TestOfflineWorkerLostTerminalACKRetainsOriginalSession(t *testing.T) {
	conf, payload, runtime := offlineWorkerFixture(t)
	manager := executor.NewExecutorManager()
	manager.Register(executor.NewShellExecutor())
	client := &artifactStatusClient{failJobTerminals: 1}
	ctx := withOfflineJobRuntime(context.Background(), runtime)
	wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
	if err := executeJobRun(ctx, conf, wrapper, manager, payload); err == nil {
		t.Fatal("lost terminal ACK accepted")
	}
	if runtime.acknowledgements != 0 {
		t.Fatal("journal acknowledged before terminal")
	}
	original := append([]string(nil), (*runtime.session.calls)...)
	if err := executeJobRun(ctx, conf, wrapper, manager, payload); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*runtime.session.calls, original) || runtime.acknowledgements != 1 {
		t.Fatal("terminal retry repeated lifecycle or changed claim")
	}
}

func TestOfflineWorkerCleanupFailureIsNotSuccess(t *testing.T) {
	conf, payload, runtime := offlineWorkerFixture(t)
	runtime.session.closeErr = errors.New("fixture cleanup failure")
	manager := executor.NewExecutorManager()
	manager.Register(executor.NewShellExecutor())
	client := &artifactStatusClient{}
	if err := executeJobRun(withOfflineJobRuntime(context.Background(), runtime), conf, &agentgrpc.ClientWrapper{AgentClient: client}, manager, payload); err == nil {
		t.Fatal("cleanup failure accepted")
	}
	if client.jobReports[len(client.jobReports)-1].Status != 5 {
		t.Fatal("cleanup failure left successful job")
	}
}

func TestOfflineWorkerTerminalReplayFreezesRuntimeBindingAndClaim(t *testing.T) {
	conf, payload, runtime := offlineWorkerFixture(t)
	conf.Grpc.ServerAddr = "synthetic-controller"
	reports, err := newTerminalOutbox(context.Background(), t.TempDir(), conf.Agent.ID, conf.Grpc.ServerAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer reports.close()
	ctx := withTerminalOutbox(withOfflineJobRuntime(context.Background(), runtime), reports)
	manager := executor.NewExecutorManager()
	manager.Register(executor.NewShellExecutor())
	client := &artifactStatusClient{failJobTerminals: 1}
	wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
	if err := executeJobRun(ctx, conf, wrapper, manager, payload); err == nil {
		t.Fatal("ACK loss was not exercised")
	}
	originalCalls := append([]string(nil), (*runtime.session.calls)...)
	if err := reports.replayBound(ctx, wrapper, conf); err != nil {
		t.Fatal(err)
	}
	// A concurrently successful delivery can evict the process-local claim cache.
	completeExecutionClaim(ctx, "job", payload.JobRunID)
	if err := executeJobRun(ctx, conf, wrapper, manager, payload); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*runtime.session.calls, originalCalls) {
		t.Fatal("replay repeated runtime lifecycle")
	}
	for _, report := range client.jobReports {
		if report.RuntimeBindingDigest != payload.Runtime.BindingDigest {
			t.Fatal("replay changed runtime binding")
		}
	}
	if runtime.acknowledgements != 1 {
		t.Fatal("original claim was not retained for cleanup acknowledgement")
	}
}

type nilOfflineRuntime struct{ *fixtureOfflineRuntime }

func (r nilOfflineRuntime) Allocate(context.Context, *taskpayload.JobRunTaskPayload, string) (offlineSession, error) {
	return nil, nil
}
func TestOfflineWorkerRejectsNilSuccessfulAllocation(t *testing.T) {
	conf, payload, runtime := offlineWorkerFixture(t)
	manager := executor.NewExecutorManager()
	manager.Register(executor.NewShellExecutor())
	client := &artifactStatusClient{}
	if err := executeJobRun(withOfflineJobRuntime(context.Background(), nilOfflineRuntime{runtime}), conf, &agentgrpc.ClientWrapper{AgentClient: client}, manager, payload); err == nil {
		t.Fatal("nil allocation succeeded")
	}
	if len(*runtime.session.calls) != 0 {
		t.Fatal("nil allocation executed")
	}
	for _, report := range client.stepReports {
		if report.Status == 4 {
			t.Fatal("step succeeded without a session")
		}
	}
}

func TestOfflineStagingFailureReceiptHasStableEndTime(t *testing.T) {
	conf, payload, runtime := offlineWorkerFixture(t)
	runtime.session.startErr = errors.New("synthetic start failure")
	conf.Grpc.ServerAddr = "synthetic-controller"
	reports, err := newTerminalOutbox(context.Background(), t.TempDir(), conf.Agent.ID, conf.Grpc.ServerAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer reports.close()
	ctx := withTerminalOutbox(withOfflineJobRuntime(context.Background(), runtime), reports)
	manager := executor.NewExecutorManager()
	manager.Register(executor.NewShellExecutor())
	client := &artifactStatusClient{failStepTerminals: 3}
	wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
	if err := executeJobRun(ctx, conf, wrapper, manager, payload); err == nil {
		t.Fatal("step ACK loss was not exercised")
	}
	var end int64
	for _, report := range client.stepReports {
		if report.Status == 5 {
			end = report.EndTime
			break
		}
	}
	if end == 0 {
		t.Fatal("missing original terminal receipt")
	}
	time.Sleep(1100 * time.Millisecond)
	client.failStepTerminals = 0
	err = executeJobRun(ctx, conf, wrapper, manager, payload)
	var completed *completedExecutionError
	if !errors.As(err, &completed) {
		t.Fatalf("expected delivered original execution failure: %v", err)
	}
	for _, report := range client.stepReports {
		if report.Status == 5 && report.EndTime != end {
			t.Fatal("replay changed terminal time")
		}
	}
}

func TestOfflineWorkerRetainsReceiptAfterJournalACKFailure(t *testing.T) {
	conf, payload, runtime := offlineWorkerFixture(t)
	runtime.ackFailures = 1
	manager := executor.NewExecutorManager()
	manager.Register(executor.NewShellExecutor())
	client := &artifactStatusClient{}
	wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
	ctx := withOfflineJobRuntime(context.Background(), runtime)
	if err := executeJobRun(ctx, conf, wrapper, manager, payload); err == nil {
		t.Fatal("journal ACK failure swallowed")
	}
	calls := append([]string(nil), (*runtime.session.calls)...)
	if err := executeJobRun(ctx, conf, wrapper, manager, payload); err != nil {
		t.Fatal(err)
	}
	if runtime.acknowledgements != 1 || !reflect.DeepEqual(*runtime.session.calls, calls) {
		t.Fatal("journal retry lost identity or repeated execution")
	}
}

func TestOfflinePolicyChangeFailsBoundJobWithoutExecuting(t *testing.T) {
	for _, mode := range []string{"removed-image", "changed-agent-mode"} {
		t.Run(mode, func(t *testing.T) {
			conf, payload, runtime := offlineWorkerFixture(t)
			payload.Timeout = ""
			ctx := context.Background()
			if mode == "removed-image" {
				conf.Agent.Sandbox.Containerd.Policy.AllowedImages = []string{"registry.example/other@sha256:" + strings.Repeat("b", 64)}
				ctx = withOfflineJobRuntime(ctx, runtime)
			} else {
				conf.Agent.Mode = "baremetal"
				conf.Agent.Sandbox.Enable = false
				conf.Agent.Sandbox.Containerd.Policy = taskpayload.OfflineRuntimePolicy{}
			}
			manager := executor.NewExecutorManager()
			manager.Register(executor.NewShellExecutor())
			client := &artifactStatusClient{failJobTerminals: 1}
			wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
			if err := executeJobRun(ctx, conf, wrapper, manager, payload); err == nil {
				t.Fatal("lost failed ACK was not exercised")
			}
			var completed *completedExecutionError
			if err := executeJobRun(ctx, conf, wrapper, manager, payload); !errors.As(err, &completed) {
				t.Fatalf("policy denial did not produce a delivered execution failure: %v", err)
			}
			if len(*runtime.session.calls) != 0 || runtime.acknowledgements != 0 {
				t.Fatal("policy denial allocated a session or acknowledged nonexistent journal")
			}
			if len(client.jobReports) < 3 || client.jobReports[0].Status != 3 {
				t.Fatal("valid bound denial did not claim before reporting")
			}
			for _, report := range client.jobReports {
				if report.RuntimeBindingDigest != payload.Runtime.BindingDigest {
					t.Fatal("policy failure lost runtime binding")
				}
			}
			if client.jobReports[len(client.jobReports)-1].Status != 5 {
				t.Fatal("policy denial did not terminate job")
			}
			for _, report := range client.stepReports {
				if report.Status == 3 || report.Status == 4 {
					t.Fatal("policy denied command was claimed or executed")
				}
			}
		})
	}
}

func TestOfflineForgedBindingCannotClaim(t *testing.T) {
	conf, payload, runtime := offlineWorkerFixture(t)
	payload.Runtime.BindingDigest = "v1:" + strings.Repeat("f", 64)
	client := &artifactStatusClient{}
	if err := executeJobRun(withOfflineJobRuntime(context.Background(), runtime), conf, &agentgrpc.ClientWrapper{AgentClient: client}, executor.NewExecutorManager(), payload); err == nil {
		t.Fatal("forged binding accepted")
	}
	if len(client.jobReports) != 0 || len(*runtime.session.calls) != 0 {
		t.Fatal("forged binding claimed or allocated")
	}
}

type unavailableOfflineRuntime struct{ *fixtureOfflineRuntime }

func (unavailableOfflineRuntime) Ready() bool { return false }
func TestOfflineUnreadyRecoveryDoesNotClaimOrAcknowledge(t *testing.T) {
	conf, payload, runtime := offlineWorkerFixture(t)
	client := &artifactStatusClient{}
	err := executeJobRun(withOfflineJobRuntime(context.Background(), unavailableOfflineRuntime{runtime}), conf, &agentgrpc.ClientWrapper{AgentClient: client}, executor.NewExecutorManager(), payload)
	if err == nil || acknowledgeCompleted(err) == nil {
		t.Fatal("unresolved recovery must retain queued task")
	}
	if len(client.jobReports) != 0 || len(*runtime.session.calls) != 0 {
		t.Fatal("unresolved recovery allowed new claim or execution")
	}
}
