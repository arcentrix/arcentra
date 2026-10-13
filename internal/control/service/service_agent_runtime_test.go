package service

import (
	"context"
	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

func TestRuntimeHeartbeatIsAuthenticatedPersistedAndCleared(t *testing.T) {
	db := terminalTestDB(t, ":memory:", false)
	ctx := context.Background()
	if err := db.WithContext(ctx).AutoMigrate(&model.Agent{}); err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Create(&model.Agent{AgentID: "agent", OS: "linux", IsEnabled: 1, Labels: []byte(`{"containerdOfflineV1":"true"}`)}).Error; err != nil {
		t.Fatal(err)
	}
	repository := repo.NewAgentRepo(artifactServiceDatabase{db}, nil)
	service := NewAgentServiceImpl(&AgentService{agentRepo: repository}, nil)
	request := &agentv1.HeartbeatRequest{AgentId: "agent", Status: 1, RuntimeCapabilities: &agentv1.RuntimeCapabilities{ExecutionMode: taskqueue.RuntimeContainerdOfflineV1, ContainerdOfflineV1: true}}
	if _, err := service.Heartbeat(ctx, request); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated report: %v", err)
	}
	auth := agentidentity.WithIdentity(ctx, agentidentity.Identity{ID: "agent"})
	if _, err := service.Heartbeat(auth, request); err != nil {
		t.Fatal(err)
	}
	agent, err := repository.Get(ctx, "agent")
	if err != nil || string(agent.RuntimeCapabilities) != `{"executionMode":"containerd-offline-v1","containerdOfflineV1":true}` {
		t.Fatalf("capability not stored: %+v %v", agent, err)
	}
	if err := repository.Patch(ctx, "agent", map[string]any{"labels": `{"containerdOfflineV1":"false"}`}); err != nil {
		t.Fatal(err)
	}
	agent, _ = repository.Get(ctx, "agent")
	if string(agent.RuntimeCapabilities) != `{"executionMode":"containerd-offline-v1","containerdOfflineV1":true}` {
		t.Fatal("labels changed capabilities")
	}
	request.RuntimeCapabilities = nil
	if _, err := service.Heartbeat(auth, request); err != nil {
		t.Fatal(err)
	}
	agent, _ = repository.Get(ctx, "agent")
	if string(agent.RuntimeCapabilities) != "{}" {
		t.Fatal("old Agent retained stale readiness")
	}
	request.RuntimeCapabilities = &agentv1.RuntimeCapabilities{ExecutionMode: taskqueue.RuntimeContainerdOfflineV1, ContainerdOfflineV1: true}
	request.AgentId = "foreign"
	if _, err := service.Heartbeat(auth, request); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("foreign heartbeat: %v", err)
	}
	request.AgentId = "agent"
	if err := repository.Patch(ctx, "agent", map[string]any{"os": "windows"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Heartbeat(auth, request); err != nil {
		t.Fatal(err)
	}
	agent, _ = repository.Get(ctx, "agent")
	if string(agent.RuntimeCapabilities) != `{"executionMode":"containerd-offline-v1"}` {
		t.Fatal("non-Linux Agent advertised readiness or lost mode")
	}
}
func TestRuntimeReportBindsOriginalClaimAndInspection(t *testing.T) {
	db := terminalTestDB(t, ":memory:", true)
	seedTerminalTestRun(t, db, 1)
	ctx := context.Background()
	repository := repo.NewJobRunRepo(artifactServiceDatabase{db}).(*repo.JobRunRepo)
	runtime := &taskqueue.RuntimeRequest{Type: taskqueue.RuntimeContainerdOfflineV1, Image: "registry.example/build@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Resources: taskqueue.RuntimeResources{CPU: "500m", Memory: "64Mi"}}
	runtime.BindingDigest, _ = taskqueue.RuntimeBindingDigest("pipeline", "run", "job", "agent", runtime)
	if err := repository.CaptureRuntimeExpectation(ctx, "pipeline", "run", "job", "agent", nil, runtime); err != nil {
		t.Fatal(err)
	}
	client, closeClient := terminalTestClient(t, db, nil)
	defer closeClient()
	request := &agentv1.ReportJobRunStatusRequest{AgentId: "agent", JobRunId: "job", Status: 3}
	for _, digest := range []string{"", "forged"} {
		request.RuntimeBindingDigest = digest
		if err := callTerminalReport(client, request, "original-claim"); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("missing/foreign runtime claimed: %v", err)
		}
	}
	job, _ := repository.GetByJobRunID(ctx, "job")
	if job.Status != 2 || job.ClaimID != "" {
		t.Fatal("invalid report claimed execution")
	}
	request.RuntimeBindingDigest = runtime.BindingDigest
	if err := callTerminalReport(client, request, "original-claim"); err != nil {
		t.Fatal(err)
	}
	inspect := &agentv1.InspectJobExecutionRequest{AgentId: "agent", JobRunId: "job", OriginalClaimId: "original-claim", RuntimeBindingDigest: runtime.BindingDigest}
	result, err := client.InspectJobExecution(ctx, inspect)
	if err != nil || result.Status != 3 || result.Terminal {
		t.Fatalf("active inspect: %+v %v", result, err)
	}
	for _, claim := range []string{"", "foreign"} {
		inspect.OriginalClaimId = claim
		if _, err := client.InspectJobExecution(ctx, inspect); err == nil {
			t.Fatal("foreign claim inspected")
		}
	}
	inspect.OriginalClaimId = "original-claim"
	inspect.AgentId = "foreign"
	if _, err := client.InspectJobExecution(ctx, inspect); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("foreign Agent inspected: %v", err)
	}
	service := NewAgentServiceImpl(&AgentService{jobRunRepo: repository}, nil)
	inspect.AgentId = "agent"
	if _, err := service.InspectJobExecution(ctx, inspect); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated inspection: %v", err)
	}
	if err := db.WithContext(ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("status", 6).Error; err != nil {
		t.Fatal(err)
	}
	result, err = client.InspectJobExecution(ctx, inspect)
	if err != nil || !result.Terminal || result.Status != 6 {
		t.Fatalf("terminal inspect: %+v %v", result, err)
	}
}

func TestRuntimeStepsNeverEscapeThroughLegacyFetchOrIndependentClaims(t *testing.T) {
	db := terminalTestDB(t, ":memory:", true)
	seedTerminalTestRun(t, db, 1)
	ctx := context.Background()
	repository := repo.NewJobRunRepo(artifactServiceDatabase{db}).(*repo.JobRunRepo)
	runtime := &taskqueue.RuntimeRequest{Type: taskqueue.RuntimeContainerdOfflineV1, Image: "registry.example/build@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Resources: taskqueue.RuntimeResources{CPU: "500m", Memory: "64Mi"}}
	runtime.BindingDigest, _ = taskqueue.RuntimeBindingDigest("pipeline", "run", "job", "agent", runtime)
	if err := db.WithContext(ctx).Model(&model.StepRun{}).Where("step_run_id = ?", "step").Updates(map[string]any{"args": `{"run":"echo runtime-secret"}`, "env": `{"SECRET":"runtime-only"}`}).Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.CaptureRuntimeExpectation(ctx, "pipeline", "run", "job", "agent", nil, runtime); err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Create(&model.StepRun{StepRunID: "legacy-step", AgentID: "agent", Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	client, closeClient := terminalTestClient(t, db, nil)
	defer closeClient()
	response, err := client.FetchStepRun(ctx, &agentv1.FetchStepRunRequest{AgentId: "agent", MaxStepRuns: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.StepRuns) != 1 || response.StepRuns[0].StepRunId != "legacy-step" {
		t.Fatalf("legacy fetch exposed runtime command/environment: %+v", response)
	}
	streamed, err := (&StreamServiceImpl{mysql: db}).fetchAgentStepRuns(ctx, "agent", 50)
	if err != nil || len(streamed) != 1 || streamed[0].StepRunId != "legacy-step" {
		t.Fatalf("legacy stream exposed runtime command/environment: %+v %v", streamed, err)
	}
	stepReport := &agentv1.ReportStepRunStatusRequest{AgentId: "agent", StepRunId: "step", Status: 3}
	if err := callTerminalReport(client, stepReport, "old-agent-standalone"); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("runtime step claimed before original parent: %v", err)
	}
	var stored model.StepRun
	if err := db.WithContext(ctx).Where("step_run_id = ?", "step").First(&stored).Error; err != nil || stored.ClaimID != "" || stored.Status != 2 {
		t.Fatalf("failed standalone claim mutated runtime step: %+v %v", stored, err)
	}
	jobReport := &agentv1.ReportJobRunStatusRequest{AgentId: "agent", JobRunId: "job", Status: 3, RuntimeBindingDigest: runtime.BindingDigest}
	if err := callTerminalReport(client, jobReport, "parent-claim"); err != nil {
		t.Fatal(err)
	}
	if err := callTerminalReport(client, stepReport, "old-agent-standalone"); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("runtime step accepted independent claim: %v", err)
	}
	childClaim := agentidentity.StepClaimID("parent-claim", "step")
	if err := callTerminalReport(client, stepReport, childClaim); err != nil {
		t.Fatalf("original full-job step claim rejected: %v", err)
	}
	if err := repository.UpdateByJobRunID(ctx, "job", map[string]any{"status": 6}); err != nil {
		t.Fatal(err)
	}
	if err := callTerminalReport(client, stepReport, childClaim); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("runtime step reactivated after parent cancellation: %v", err)
	}
	if err := callTerminalReport(client, &agentv1.ReportStepRunStatusRequest{AgentId: "agent", StepRunId: "legacy-step", Status: 3}, "legacy-independent"); err != nil {
		t.Fatalf("baremetal standalone compatibility changed: %v", err)
	}
}

func TestRuntimeCapabilitiesPreserveModeBeforeReadiness(t *testing.T) {
	for _, ready := range []bool{false, true} {
		got := agentRuntimeCapabilities(&agentv1.RuntimeCapabilities{ExecutionMode: taskqueue.RuntimeContainerdOfflineV1, ContainerdOfflineV1: ready}, "linux")
		var decoded taskqueue.AgentRuntimeCapabilities
		if err := sonic.Unmarshal(got, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.ExecutionMode != taskqueue.RuntimeContainerdOfflineV1 || decoded.ContainerdOfflineV1 != ready {
			t.Fatal("execution mode conflated with readiness")
		}
	}
	for _, mode := range []string{"future", "Containerd-offline-v1", " baremetal"} {
		var decoded taskqueue.AgentRuntimeCapabilities
		_ = sonic.Unmarshal(agentRuntimeCapabilities(&agentv1.RuntimeCapabilities{ExecutionMode: mode}, "linux"), &decoded)
		if decoded.ExecutionMode != "unknown" {
			t.Fatal("unknown execution mode downgraded to baremetal")
		}
	}
}
