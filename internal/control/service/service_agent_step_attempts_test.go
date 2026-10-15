package service_test

import (
	"context"
	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func serviceStepInitial(t *testing.T, db *gorm.DB, client agentv1.AgentServiceClient, initial taskqueue.JobGenerationTransition, manifest *taskqueue.JobInputManifest) taskqueue.StepAttemptTransition {
	t.Helper()
	g, err := reconcileGeneration(t, client, initial)
	if err != nil {
		t.Fatal(err)
	}
	pin := generationServicePin(t, client, initial, manifest)
	var job model.JobRun
	if err := db.WithContext(context.Background()).Where("job_run_id = ?", "job").First(&job).Error; err != nil {
		t.Fatal(err)
	}
	plan, err := taskqueue.ParseJobRetryPlan(job.RetryPlan)
	if err != nil {
		t.Fatal(err)
	}
	planned := plan.Steps[0]
	s := taskqueue.StepAttemptIdentity{Identity: initial.Identity, GenerationID: g.GenerationID, GenerationBindingDigest: g.BindingDigest, RuntimeClaimID: g.RuntimeClaimID, InputManifestDigest: pin, RetryPlanDigest: job.RetryPlanDigest, Session: taskqueue.StepAttemptSession{SessionID: g.RuntimeClaimID, SessionIncarnationNonce: uuid.NewString(), Namespace: "fixture", ContainerID: "arcentra-" + g.RuntimeClaimID, SnapshotID: "arcentra-" + g.RuntimeClaimID + "-snapshot", Snapshotter: "overlayfs", ResourceOwnerDigest: strings.Repeat("a", 64), WorkspaceID: "job-" + g.RuntimeClaimID, WorkspaceDevice: 1, WorkspaceInode: 2}, LogicalStepRunID: "step-1", StepRunID: "step-1", StepName: planned.Name, StepIndex: planned.StepIndex, DefinitionDigest: planned.DefinitionDigest, Attempt: 1, StepClaimID: agentidentity.StepClaimID("original", "step-1")}
	r := taskqueue.StepAttemptTransition{Version: 1, Operation: "admit", Scope: s}
	r.RequestDigest, _ = r.Digest()
	return r
}
func reconcileServiceStep(t *testing.T, client agentv1.AgentServiceClient, request taskqueue.StepAttemptTransition) (*taskqueue.StepAttemptAdmission, bool, error) {
	t.Helper()
	body, err := request.Canonical()
	if err != nil {
		return nil, false, err
	}
	response, err := client.ReconcileStepAttempt(generationRPCContext("original"), &agentv1.ReconcileStepAttemptRequest{AgentId: "agent", TransitionJson: body})
	if err != nil {
		return nil, false, err
	}
	a, err := taskqueue.ParseStepAttemptAdmission(response.AdmissionJson)
	return a, response.ExecutionAllowed, err
}
func serviceStepReport(a *taskqueue.StepAttemptAdmission, state int32) *agentv1.ReportStepRunStatusRequest {
	r := &agentv1.ReportStepRunStatusRequest{AgentId: "agent", StepRunId: a.Scope.StepRunID, GenerationId: a.Scope.GenerationID, GenerationBindingDigest: a.Scope.GenerationBindingDigest, StepAttemptAdmissionDigest: a.BindingDigest}
	r.Status = 3
	if state == 5 {
		r.Status = 5
		r.ExitCode = 9
		r.RetryableShellExit = true
	}
	if state == 4 {
		r.Status = 4
	}
	return r
}
func serviceStepAdvance(t *testing.T, a *taskqueue.StepAttemptAdmission, failure *agentv1.ReportStepRunStatusRequest) taskqueue.StepAttemptTransition {
	t.Helper()
	receipt, err := agentidentity.TerminalReceiptDigest(failure, a.Scope.StepClaimID, "pipeline", "run", "job")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	boundary := &taskqueue.PausedStepBoundary{Version: 1, Scope: a.Scope, BoundaryNonce: uuid.NewString(), BoundarySequence: 1, FailedAdmissionID: a.AdmissionID, FailedAdmissionBindingDigest: a.BindingDigest, FailedStepReceiptDigest: receipt, CapturedUnixMilli: now.UnixMilli(), ExpiresUnixMilli: now.Add(taskqueue.PausedStepBoundaryLifetime).UnixMilli(), FailedExecExitObserved: true, ExecReaped: true, TaskPaused: true, ExactLiveInitOnly: true, Childless: true}
	scope := a.Scope
	scope.StepRunID = "step-2"
	scope.StepClaimID = agentidentity.StepClaimID("original", "step-2")
	scope.Attempt = 2
	request := taskqueue.StepAttemptTransition{Version: 1, Operation: "advance", Scope: scope, PreviousStepRunID: a.Scope.StepRunID, PreviousStepClaimID: a.Scope.StepClaimID, PreviousAdmissionID: a.AdmissionID, PreviousAdmissionBindingDigest: a.BindingDigest, FailedStepReceiptDigest: receipt, Boundary: boundary}
	request.RequestDigest, _ = request.Digest()
	return request
}
func TestStepAttemptAuthenticatedLostACKReopenAndHistoricalReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "step.db")
	db, initial, manifest := generationServiceFixture(t, path, "failed_step")
	var lose atomic.Bool
	lose.Store(true)
	client, _, stop := generationClient(t, db, func(p proto.Message) error {
		if r, ok := p.(*agentv1.ReconcileStepAttemptRequest); ok {
			request, _ := taskqueue.ParseStepAttemptTransition(r.TransitionJson)
			if request.Operation == "advance" && lose.CompareAndSwap(true, false) {
				return status.Error(codes.Unavailable, "synthetic lost ACK")
			}
		}
		return nil
	})
	request := serviceStepInitial(t, db, client, initial, manifest)
	first, allowed, err := reconcileServiceStep(t, client, request)
	if err != nil || !allowed {
		t.Fatal(err)
	}
	running := serviceStepReport(first, 3)
	if _, err := client.ReportStepRunStatus(generationRPCContext(first.Scope.StepClaimID), running); err != nil {
		t.Fatal(err)
	}
	failure := serviceStepReport(first, 5)
	if _, err := client.ReportStepRunStatus(generationRPCContext(first.Scope.StepClaimID), failure); err != nil {
		t.Fatal(err)
	}
	advance := serviceStepAdvance(t, first, failure)
	if _, _, err := reconcileServiceStep(t, client, advance); status.Code(err) != codes.Unavailable {
		t.Fatalf("missing lost ACK: %v", err)
	}
	var before model.StepRun
	db.WithContext(context.Background()).Where("step_run_id = ?", "step-2").First(&before)
	stop()
	sqlDB, _ := db.DB()
	_ = sqlDB.Close()
	db = generationTestDB(t, path, false)
	client, _, _ = generationClient(t, db, nil)
	second, allowed, err := reconcileServiceStep(t, client, advance)
	if err != nil || !allowed {
		t.Fatalf("reopen same live Agent admission ACK: %v", err)
	}
	body, _ := second.Canonical()
	if body != before.AttemptAdmission {
		t.Fatal("controller reopen changed immutable admission")
	}
	inspect := request
	inspect.Operation = "inspect"
	inspect.Scope = second.Scope
	inspect.RequestDigest, _ = inspect.Digest()
	observed, allowed, err := reconcileServiceStep(t, client, inspect)
	if err != nil || allowed || !reflect.DeepEqual(observed, second) {
		t.Fatal("inspect created execution permission")
	}
	if _, err := client.ReportStepRunStatus(generationRPCContext(first.Scope.StepClaimID), failure); err != nil {
		t.Fatal("exact historical terminal ACK lost", err)
	}
	changed := proto.Clone(failure).(*agentv1.ReportStepRunStatusRequest)
	changed.StepAttemptAdmissionDigest = second.BindingDigest
	if _, err := client.ReportStepRunStatus(generationRPCContext(first.Scope.StepClaimID), changed); err == nil {
		t.Fatal("historical terminal ignored changed admission digest")
	}
	if _, err := client.ReportStepRunStatus(generationRPCContext(first.Scope.StepClaimID), running); err == nil {
		t.Fatal("historical RUNNING replay executed")
	}
	success := serviceStepReport(second, 4)
	if _, err := client.ReportStepRunStatus(generationRPCContext(second.Scope.StepClaimID), success); err == nil {
		t.Fatal("unstarted SUCCESS accepted")
	}
	running = serviceStepReport(second, 3)
	if _, err := client.ReportStepRunStatus(generationRPCContext(second.Scope.StepClaimID), running); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReportStepRunStatus(generationRPCContext(second.Scope.StepClaimID), success); err != nil {
		t.Fatal(err)
	}
	final := &agentv1.ReportJobRunStatusRequest{AgentId: "agent", JobRunId: "job", Status: 4, RuntimeBindingDigest: initial.Identity.RuntimeBindingDigest, GenerationId: second.Scope.GenerationID, GenerationBindingDigest: second.Scope.GenerationBindingDigest}
	if _, err := client.ReportJobRunStatus(generationRPCContext("original"), final); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.WithContext(context.Background()).Model(&model.JobRunGeneration{}).Count(&count)
	if count != 1 {
		t.Fatal("failed_step created extra job generation")
	}
	old, allowed, err := reconcileServiceStep(t, client, advance)
	if err != nil || allowed || !reflect.DeepEqual(old, second) {
		t.Fatal("completed job renewed retry permission")
	}
}
func TestStepAttemptGRPCExactAuthenticationAndFreshRunningReACK(t *testing.T) {
	db, initial, manifest := generationServiceFixture(t, ":memory:", "failed_step")
	client, _, _ := generationClient(t, db, nil)
	request := serviceStepInitial(t, db, client, initial, manifest)
	body, _ := request.Canonical()
	rpc := &agentv1.ReconcileStepAttemptRequest{AgentId: "agent", TransitionJson: body}
	for _, md := range []metadata.MD{nil, metadata.Pairs("authorization", "Bearer wrong", "x-arcentra-claim-id", "original"), metadata.Pairs("authorization", "Bearer generation-fixture-token"), metadata.Pairs("authorization", "Bearer generation-fixture-token", "x-arcentra-claim-id", request.Scope.RuntimeClaimID), metadata.Pairs("authorization", "Bearer generation-fixture-token", "x-arcentra-claim-id", "original", "x-arcentra-claim-id", "original")} {
		if _, err := client.ReconcileStepAttempt(metadata.NewOutgoingContext(context.Background(), md), rpc); err == nil {
			t.Fatal("invalid original Agent/claim authenticated")
		}
	}
	foreign := proto.Clone(rpc).(*agentv1.ReconcileStepAttemptRequest)
	foreign.AgentId = "other"
	if _, err := client.ReconcileStepAttempt(generationRPCContext("original"), foreign); err == nil {
		t.Fatal("foreign Agent authenticated")
	}
	a, _, err := reconcileServiceStep(t, client, request)
	if err != nil {
		t.Fatal(err)
	}
	running := serviceStepReport(a, 3)
	for range 2 {
		if _, err := client.ReportStepRunStatus(generationRPCContext(a.Scope.StepClaimID), running); err != nil {
			t.Fatal(err)
		}
	}
	jobs := repo.NewJobRunRepo(generationServiceDatabase{db}).(*repo.JobRunRepo)
	if err := jobs.CancelAgentJobIfCurrent(context.Background(), "job", 3, map[string]any{"status": 6}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReportStepRunStatus(generationRPCContext(a.Scope.StepClaimID), running); err == nil {
		t.Fatal("same-claim RUNNING reACK ignored cancellation")
	}
	replay, allowed, err := reconcileServiceStep(t, client, request)
	if err != nil || allowed || !reflect.DeepEqual(replay, a) {
		t.Fatal("cancelled admission gained execution permission")
	}
}
