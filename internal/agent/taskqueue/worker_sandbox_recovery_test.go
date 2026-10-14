package taskqueue

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/agent/sandboxexec"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/grpc"
)

type recoveryControllerFixture struct {
	records      []sandboxexec.Recovery
	acknowledged []string
}

func (c *recoveryControllerFixture) Recoveries() []sandboxexec.Recovery { return c.records }
func (c *recoveryControllerFixture) AcknowledgeCompletion(_ context.Context, claim string) error {
	c.acknowledged = append(c.acknowledged, claim)
	return nil
}

type recoveryAuthorityFixture struct {
	agentv1.AgentServiceClient
	response *agentv1.InspectJobExecutionResponse
	err      error
	requests []*agentv1.InspectJobExecutionRequest
}

func (c *recoveryAuthorityFixture) InspectJobExecution(_ context.Context, req *agentv1.InspectJobExecutionRequest, _ ...grpc.CallOption) (*agentv1.InspectJobExecutionResponse, error) {
	c.requests = append(c.requests, req)
	return c.response, c.err
}

func TestOfflineRecoveryRequiresExactCleanupAndAuthoritativeTerminal(t *testing.T) {
	for _, test := range []struct {
		name     string
		cleanup  bool
		response *agentv1.InspectJobExecutionResponse
		err      error
		accepted bool
	}{
		{name: "interrupted-reconciled", cleanup: true, response: &agentv1.InspectJobExecutionResponse{Status: 5, Terminal: true}, accepted: true},
		{name: "already-success", cleanup: true, response: &agentv1.InspectJobExecutionResponse{Status: 4, Terminal: true}, accepted: true},
		{name: "active", cleanup: true, response: &agentv1.InspectJobExecutionResponse{Status: 3}},
		{name: "invalid-terminal", cleanup: true, response: &agentv1.InspectJobExecutionResponse{Status: 99, Terminal: true}},
		{name: "unconfirmed-cleanup", response: &agentv1.InspectJobExecutionResponse{Status: 5, Terminal: true}},
		{name: "network-or-authorization", cleanup: true, err: errors.New("synthetic denied")},
		{name: "empty-response", cleanup: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller := &recoveryControllerFixture{records: []sandboxexec.Recovery{{PipelineRunID: "run", JobRunID: "job", ExecutionClaimID: "original-claim", RuntimeBindingDigest: "v1:synthetic-binding", CleanupComplete: test.cleanup}}}
			authority := &recoveryAuthorityFixture{response: test.response, err: test.err}
			conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "original-agent"}}
			err := confirmOfflineRecoveries(context.Background(), controller, conf, &agentgrpc.ClientWrapper{AgentClient: authority})
			if (err == nil) != test.accepted {
				t.Fatalf("acceptance=%v err=%v", test.accepted, err)
			}
			if test.accepted && !reflect.DeepEqual(controller.acknowledged, []string{"original-claim"}) {
				t.Fatal("lost original claim")
			}
			if !test.accepted && len(controller.acknowledged) != 0 {
				t.Fatal("unproven recovery acknowledged")
			}
			for _, req := range authority.requests {
				if req.AgentId != "original-agent" || req.JobRunId != "job" || req.OriginalClaimId != "original-claim" || req.RuntimeBindingDigest != "v1:synthetic-binding" {
					t.Fatal("recovery query identity changed")
				}
			}
		})
	}
}

type unusedRecoveryBackend struct{ sandboxexec.DurableBackend }

func (unusedRecoveryBackend) Namespace() string { return "synthetic-runtime" }
func TestOfflineRuntimeAllocationErrorDoesNotReturnTypedNil(t *testing.T) {
	journal, workspace := t.TempDir(), t.TempDir()
	if err := os.Chmod(journal, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	controller, err := sandboxexec.OpenController(context.Background(), unusedRecoveryBackend{}, sandboxexec.ControllerOptions{JournalRoot: journal, WorkspaceRoot: workspace, ControllerID: "controller", AgentID: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close(context.Background())
	runtime := &durableOfflineRuntime{controller: controller, user: "0:0"}
	_, payload, _ := offlineWorkerFixture(t)
	session, err := runtime.Allocate(context.Background(), payload, "claim")
	if err == nil || session != nil {
		t.Fatalf("controller rejection must remain a nil interface, got %T / %v", session, err)
	}
	session, err = runtime.AllocateGeneration(context.Background(), payload, "claim", taskpayload.JobGeneration{})
	if err == nil || session != nil {
		t.Fatalf("generation rejection must remain a nil interface, got %T / %v", session, err)
	}
}

func TestOfflineAcceptedProfileStillRequiresAuthenticatedController(t *testing.T) {
	conf, _, _ := offlineWorkerFixture(t)
	for _, client := range []*agentgrpc.ClientWrapper{nil, {}} {
		runtime, err := newOfflineRuntime(context.Background(), conf, client)
		if err == nil || runtime != nil {
			t.Fatal("accepted profile started without authenticated controller transport")
		}
	}
}

func TestOfflineRecoveryAcknowledgesOriginalClaimOnceAcrossGenerations(t *testing.T) {
	first := sandboxexec.Recovery{PipelineRunID: "run", JobRunID: "job", ExecutionClaimID: "claim", RuntimeBindingDigest: "binding", CleanupComplete: true}
	for _, invalid := range []bool{false, true} {
		second := first
		if invalid {
			second.CleanupComplete = false
		}
		controller := &recoveryControllerFixture{records: []sandboxexec.Recovery{first, second}}
		authority := &recoveryAuthorityFixture{response: &agentv1.InspectJobExecutionResponse{Status: 5, Terminal: true}}
		err := confirmOfflineRecoveries(context.Background(), controller, &config.AgentConfig{Agent: config.AgentInfo{ID: "agent"}}, &agentgrpc.ClientWrapper{AgentClient: authority})
		if invalid {
			if err == nil || len(controller.acknowledged) != 0 || len(authority.requests) != 0 {
				t.Fatal("partial generation cleanup passed group preflight")
			}
		} else if err != nil || len(controller.acknowledged) != 1 || len(authority.requests) != 1 {
			t.Fatalf("original claim was not acknowledged exactly once: %v", err)
		}
	}
}

func TestOfflineRuntimeDrainBarrierIsIdempotentAndImmediate(t *testing.T) {
	runtime := &durableOfflineRuntime{}
	done := runtime.AdmissionDone()
	select {
	case <-done:
		t.Fatal("new runtime already draining")
	default:
	}
	runtime.BeginDrain()
	select {
	case <-done:
	default:
		t.Fatal("drain did not wake pending admission delay")
	}
	runtime.BeginDrain()
	if runtime.Ready() {
		t.Fatal("draining runtime advertised readiness")
	}
	if runtime.AdmissionDone() != done {
		t.Fatal("drain changed the barrier channel")
	}
	if session, err := runtime.AllocateGeneration(context.Background(), nil, "claim", taskpayload.JobGeneration{}); err == nil || session != nil {
		t.Fatal("drain allowed generation allocation")
	}
	if err := runtime.RecordGenerationIntent(context.Background(), taskpayload.JobGenerationTransition{}); err == nil {
		t.Fatal("drain allowed a new admission intent")
	}
}
