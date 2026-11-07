//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	controlconfig "github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/model"
	controlservice "github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/nova"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	ckafka "github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This observer calls the real TLS/HMAC RPC first. Only the selected newly
// confirmed job's authorized response is discarded; request nonce/payload is
// neither saved nor logged. It models lost delivery before Agent adoption.
type terminalRerunLostClaim struct {
	agentv1.AgentServiceClient
	mu                     sync.Mutex
	job                    string
	drop                   bool
	delivered, rejected    int
	claim                  string
	rejectedNewClaim       bool
	oldIncarnation         [32]byte
	rejectedNewIncarnation bool
}

func (c *terminalRerunLostClaim) activate(job string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if job == "" || c.job != "" {
		return false
	}
	c.job, c.drop = job, true
	return true
}

func (c *terminalRerunLostClaim) ClaimAndFetchCopilotRerun(ctx context.Context, in *agentv1.ClaimAndFetchCopilotRerunRequest, opts ...grpc.CallOption) (*agentv1.ClaimAndFetchCopilotRerunResponse, error) {
	out, err := c.AgentServiceClient.ClaimAndFetchCopilotRerun(ctx, in, opts...)
	c.mu.Lock()
	defer c.mu.Unlock()
	if in.GetReference().GetJobRunId() != c.job {
		return out, err
	}
	if err != nil {
		if status.Code(err) != codes.FailedPrecondition {
			return out, err
		}
		c.rejectedNewIncarnation = len(in.AdmissionNonce) == 32 && sha256.Sum256(in.AdmissionNonce) != c.oldIncarnation
		c.rejectedNewClaim = c.claim != "" && in.ClaimId != "" && in.ClaimId != c.claim
		c.rejected++
		return out, err
	}
	if out == nil || out.Receipt == nil {
		return out, err
	}
	if len(in.AdmissionNonce) != 32 {
		return nil, status.Error(codes.FailedPrecondition, "fixture invalid admission size")
	}
	digest := sha256.Sum256(in.AdmissionNonce)
	if c.delivered > 0 && digest != c.oldIncarnation {
		return nil, status.Error(codes.FailedPrecondition, "fixture observed changed incarnation")
	}
	c.oldIncarnation = digest
	c.delivered++
	if c.claim != "" && c.claim != out.Receipt.ClaimId {
		return nil, status.Error(codes.FailedPrecondition, "fixture observed changed claim")
	}
	c.claim = out.Receipt.ClaimId
	if c.drop {
		return nil, status.Error(codes.Unavailable, "acceptance lost claim delivery")
	}
	return out, nil
}

// Called before the primary journey joins its dispatcher. Each auxiliary job
// has its own fixed intent, identity, evidence and zero-command assertions.
func terminalRerunCancellationAndRestart(t *testing.T, f *controllerWorkerFixture, services *controlservice.Services, store cache.ICache, conf *controlconfig.AppConfig, httpFixture *terminalRerunHTTP, owner, originalJob, snapshotDigest string, publisher executor.LogPublisher, lifecycle *containerApprovalLifecycle, events *containerAcceptanceStepRuntimeEvents, retained map[string]string, reportReadiness func()) map[string]any {
	t.Helper()
	cancelActor := terminalRerunCancelActor(t, f)
	cancelHTTP := newTerminalRerunHTTP(t, f, services, store, conf, []string{cancelActor})
	prepare := func() controlservice.CopilotRerunIntent {
		if !f.worker.OfflineRuntimeReady() {
			t.Fatal("auxiliary worker not ready")
		}
		reportReadiness()
		var out controlservice.CopilotRerunIntent
		httpFixture.request(t, f.ctx, owner, "POST", "/projects/"+f.projectID+"/copilot/rerun-intents/", controlservice.CopilotRerunPrepareRequest{RequestID: id.GetUild(), OriginalJobRunID: originalJob, SnapshotDigest: snapshotDigest}, &out)
		return out
	}
	confirm := func(in controlservice.CopilotRerunIntent) controlservice.CopilotRerunIntent {
		var out controlservice.CopilotRerunIntent
		httpFixture.request(t, f.ctx, owner, "POST", "/projects/"+f.projectID+"/copilot/rerun-intents/"+in.IntentID+"/confirm", controlservice.CopilotRerunConfirmRequest{ExpectedVersion: in.Version, IntentDigest: in.IntentDigest, ReviewedRunContextDigest: in.ReviewedRunContextDigest, AcknowledgeNewExecution: true}, &out)
		return out
	}
	stop := func(run string) controlservice.PipelineRunStopReceipt {
		var out controlservice.PipelineRunStopReceipt
		cancelHTTP.request(t, f.ctx, cancelActor, "POST", "/pipelines/"+f.pipelineID+"/runs/"+run+"/stop", map[string]string{"reason": "synthetic terminal rerun cancellation acceptance"}, &out)
		if !out.Protected || !out.ControllerRequested || out.RunID != run {
			t.Fatal("actual Stop route did not return protected cancellation evidence")
		}
		return out
	}
	cancelled := confirm(prepare())
	approval := terminalRerunWaitApproval(t, f, cancelled.NewJobRunID)
	if approval.Status != model.ApprovalStatusPending || approval.ApprovedCount != 0 {
		t.Fatal("auxiliary cancellation execution was approved unexpectedly")
	}
	pending := containerApprovalPending(t, f, cancelled.NewJobRunID)
	lifecycle.requireNone(t, pending.Record.Resource.ID)
	stop(cancelled.NewRunID)
	f.wait("actual cancelled Agent terminal acknowledgement", func() bool {
		job := terminalRerunReadJob(t, f, cancelled.NewJobRunID)
		return job.Status == model.JobRunStatusCancelled && job.TerminalReportDigest != ""
	})
	containerAcceptanceRemoved(t, f, pending)
	containerAcceptanceDrainWithAudit(t, f, retained)
	cancelReceipt := stop(cancelled.NewRunID)
	if !cancelReceipt.AgentTerminalAcknowledged {
		t.Fatal("real cancelled claim acknowledgement was not observed by Stop inspection")
	}
	if counts, _, err := events.counts(pending.Record.Resource); err != nil || counts["exec-start"] != 0 || counts["task-start"] != 0 {
		t.Fatal("cancelled pending approval started user code")
	}
	terminalRerunNoArtifacts(t, f, cancelled.NewJobRunID)

	// Drop all bounded old-incarnation fetch responses after their real commit.
	// No Allocate/session exists in this window, and no old terminal ACK is made up.
	events.mu.Lock()
	eventCount := len(events.events)
	events.mu.Unlock()
	lost := prepare()
	observed, ok := f.client.AgentClient.(*terminalRerunLostClaim)
	if !ok {
		t.Fatal("claim observer was not installed before worker startup")
	}
	if !observed.activate(lost.NewJobRunID) {
		t.Fatal("claim observer already consumed its exact target")
	}
	t.Cleanup(func() { observed.mu.Lock(); observed.oldIncarnation = [32]byte{}; observed.mu.Unlock() })
	lost = confirm(lost)
	f.wait("bounded real claim delivery loss", func() bool { observed.mu.Lock(); defer observed.mu.Unlock(); return observed.delivered >= 3 })
	before := terminalRerunReadJob(t, f, lost.NewJobRunID)
	if before.ClaimID == "" || before.TerminalReportDigest != "" || before.CancellationClaimID != "" || before.ExpectedSourceCommit == "" || before.SourceResolvedCommit != "" || before.InputManifestState != "unknown" || before.InputManifestDigest != "" || before.RetryApprovalID != "" || before.RetryApprovalEvidenceHash != "" {
		t.Fatal("lost delivery must preserve actual unacknowledged claim")
	}
	terminalRerunNoJournal(t, f, lost.NewJobRunID)
	var gate model.CopilotRerunAdmissionGate
	if err := f.db.Where("job_run_id = ?", lost.NewJobRunID).First(&gate).Error; err != nil || gate.ClaimID != before.ClaimID {
		t.Fatal("real claim did not commit before lost response")
	}
	f.mu.Lock()
	reference, exists := f.rerunReferences[lost.NewJobRunID]
	f.mu.Unlock()
	if !exists || reference.AgentID != f.conf.Agent.ID {
		t.Fatal("exact original reference missing")
	}
	if err := f.worker.Stop(); err != nil {
		t.Fatal("old worker did not drain")
	}
	terminalRerunNoJournal(t, f, lost.NewJobRunID)
	afterStop := terminalRerunReadJob(t, f, lost.NewJobRunID)
	if !reflect.DeepEqual(*before, *afterStop) {
		t.Fatal("before-adoption worker shutdown fabricated execution evidence")
	}
	observed.mu.Lock()
	observed.drop = false
	deliveredBefore, rejectedBefore := observed.delivered, observed.rejected
	observed.mu.Unlock()
	f.mu.Lock()
	serverFailuresBefore := f.rerunClaimFailures[lost.NewJobRunID]
	f.mu.Unlock()
	manager := executor.NewExecutorManager()
	manager.Register(executor.NewShellExecutor())
	manager.SetLogPublisher(publisher)
	observer := events.observer()
	observer.AfterResume = nil
	queue, err := startContainerAcceptanceWorkerObserved(f.ctx, f.conf, f.client, manager, observer)
	if err != nil {
		t.Fatal(err)
	}
	f.worker = queue.(*workerQueues)
	if !f.worker.OfflineRuntimeReady() {
		t.Fatal("restarted production worker is not ready")
	}
	reportReadiness()
	raw, err := json.Marshal(reference)
	if err != nil {
		t.Fatal(err)
	}
	contextQueue, ok := f.producer.(interface {
		EnqueueContext(context.Context, *nova.Task, ...nova.Option) (*nova.Result, error)
	})
	if !ok {
		t.Fatal("actual publisher lacks bounded context")
	}
	if result, err := contextQueue.EnqueueContext(f.ctx, &nova.Task{Type: taskpayload.TaskTypeCopilotRerunReference, Payload: raw}, nova.Queue(taskpayload.AgentQueue(f.conf.Agent.ID))); err != nil || result == nil {
		t.Fatal("exact replay publication outcome uncertain")
	}
	f.wait("new incarnation rejects old committed claim", func() bool {
		observed.mu.Lock()
		defer observed.mu.Unlock()
		return observed.rejected > rejectedBefore && observed.rejectedNewClaim && observed.rejectedNewIncarnation
	})
	terminalRerunQueueDrained(t, f)
	f.mu.Lock()
	serverFailuresAfter := f.rerunClaimFailures[lost.NewJobRunID]
	f.mu.Unlock()
	if serverFailuresAfter <= serverFailuresBefore {
		t.Fatal("restart did not reach authenticated controller claim validation")
	}
	observed.mu.Lock()
	deliveredAfter := observed.delivered
	observed.mu.Unlock()
	if deliveredAfter != deliveredBefore {
		t.Fatal("new incarnation fetched the old sealed payload")
	}
	terminalRerunNoJournal(t, f, lost.NewJobRunID)
	terminalRerunNoArtifacts(t, f, lost.NewJobRunID)
	events.mu.Lock()
	afterEvents := len(events.events)
	events.mu.Unlock()
	if afterEvents != eventCount {
		t.Fatal("lost-adoption/restart performed new runtime SDK operations")
	}
	afterReplay := terminalRerunReadJob(t, f, lost.NewJobRunID)
	var gateAfter model.CopilotRerunAdmissionGate
	if err := f.db.Where("gate_id = ?", gate.GateID).First(&gateAfter).Error; err != nil || !reflect.DeepEqual(gate, gateAfter) || !reflect.DeepEqual(*before, *afterReplay) {
		t.Fatal("restart adopted or rewrote old admission evidence")
	}
	unresolved := stop(lost.NewRunID)
	if unresolved.AgentTerminalAcknowledged {
		t.Fatal("new incarnation must not manufacture old Agent terminal acknowledgement")
	}
	terminalRerunControlDrained(t, f)
	finalLost := terminalRerunReadJob(t, f, lost.NewJobRunID)
	if finalLost.TerminalReportDigest != "" || finalLost.ClaimID != before.ClaimID || finalLost.CancellationClaimID != before.ClaimID {
		t.Fatal("control-only cleanup fabricated an old execution acknowledgement")
	}
	terminalRerunNoJournal(t, f, lost.NewJobRunID)
	return map[string]any{"cancelledRunId": cancelled.NewRunID, "cancelledJobRunId": cancelled.NewJobRunID, "cancelledAgentTerminalAcknowledged": true, "cancelledCommandStarts": 0, "lostResponseRunId": lost.NewRunID, "lostResponseJobRunId": lost.NewJobRunID, "lostResponseClaimId": before.ClaimID, "restartSameAgentId": f.conf.Agent.ID, "restartIncarnationChanged": true, "restartPayloadAdoptions": 0, "restartCommandStarts": 0, "restartControllerCancellationRequested": true, "restartAgentTerminalAcknowledged": false, "restartBoundary": "lost-response-before-adoption", "restartServerRejections": serverFailuresAfter - serverFailuresBefore}
}

func terminalRerunNoJournal(t *testing.T, f *controllerWorkerFixture, job string) {
	t.Helper()
	records, err := containerAcceptanceRecords(f.conf.Agent.Sandbox.Containerd.JournalDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		if r.Record != nil && r.Record.JobRunID == job {
			t.Fatal("unadopted claim allocated a runtime journal/session")
		}
	}
}
func terminalRerunCancelActor(t *testing.T, f *controllerWorkerFixture) string {
	t.Helper()
	actor, role, grant, permission := "cancel-"+id.GetUild(), "role-"+id.GetUild(), "grant-"+id.GetUild(), "permission-"+id.GetUild()
	for _, row := range []any{&model.User{UserID: actor, IsEnabled: 1}, &model.Role{RoleID: role, Name: role, IsEnabled: 1}, &model.RoleGrant{GrantID: grant, SubjectType: "user", SubjectID: actor, RoleID: role, ScopeType: "project", ScopeID: f.projectID, IsEnabled: 1}, &model.Permission{PermissionID: permission, ResourceType: "pipeline", Action: "cancel"}, &model.RolePermissionBinding{RoleID: role, PermissionID: permission}} {
		if err := f.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, item := range []struct {
			row           any
			column, value string
		}{{&model.RolePermissionBinding{}, "role_id", role}, {&model.RoleGrant{}, "grant_id", grant}, {&model.Permission{}, "permission_id", permission}, {&model.Role{}, "role_id", role}, {&model.User{}, "user_id", actor}} {
			if err := f.db.Where(item.column+" = ?", item.value).Delete(item.row).Error; err != nil {
				t.Error(err)
			}
		}
	})
	return actor
}

// This ordinary test verifies only the fixture fault boundary; it is not a
// substitute for the actual authenticated RPC/worker/Kafka scenario above.
type terminalRerunClaimFixtureClient struct {
	agentv1.AgentServiceClient
	call func(*agentv1.ClaimAndFetchCopilotRerunRequest) (*agentv1.ClaimAndFetchCopilotRerunResponse, error)
}

func (c terminalRerunClaimFixtureClient) ClaimAndFetchCopilotRerun(_ context.Context, in *agentv1.ClaimAndFetchCopilotRerunRequest, _ ...grpc.CallOption) (*agentv1.ClaimAndFetchCopilotRerunResponse, error) {
	return c.call(in)
}
func TestContainerTerminalRerunLostClaimBoundary(t *testing.T) {
	calls := 0
	realResponse := &agentv1.ClaimAndFetchCopilotRerunResponse{Receipt: &agentv1.CopilotRerunClaimReceipt{ClaimId: "old-claim"}, PayloadJson: []byte(`{"fixture":"private"}`)}
	client := &terminalRerunLostClaim{job: "target", drop: true, AgentServiceClient: terminalRerunClaimFixtureClient{call: func(in *agentv1.ClaimAndFetchCopilotRerunRequest) (*agentv1.ClaimAndFetchCopilotRerunResponse, error) {
		calls++
		if in.ClaimId == "new-claim" {
			return nil, status.Error(codes.FailedPrecondition, "fixture durable incarnation mismatch")
		}
		return realResponse, nil
	}}}
	request := &agentv1.ClaimAndFetchCopilotRerunRequest{Reference: &agentv1.CopilotRerunClaimReference{JobRunId: "other"}, ClaimId: "old-claim", AdmissionNonce: make([]byte, 32)}
	out, err := client.ClaimAndFetchCopilotRerun(context.Background(), request)
	if err != nil || out != realResponse || client.delivered != 0 {
		t.Fatal("fault observer changed another job")
	}
	request.Reference.JobRunId = "target"
	for i := 0; i < 3; i++ {
		out, err = client.ClaimAndFetchCopilotRerun(context.Background(), request)
		if out != nil || status.Code(err) != codes.Unavailable {
			t.Fatal("authorized response was not discarded")
		}
	}
	if calls != 4 || client.delivered != 3 || client.claim != "old-claim" {
		t.Fatal("fault observer skipped underlying claim or changed fixed identity")
	}
	client.drop = false
	request.ClaimId = "new-claim"
	request.AdmissionNonce[0] = 1
	out, err = client.ClaimAndFetchCopilotRerun(context.Background(), request)
	if out != nil || status.Code(err) != codes.FailedPrecondition || client.rejected != 1 || !client.rejectedNewClaim || !client.rejectedNewIncarnation || client.delivered != 3 {
		t.Fatal("restart rejection evidence did not distinguish both fresh process identities")
	}
}

func terminalRerunControlDrained(t *testing.T, f *controllerWorkerFixture) {
	t.Helper()
	consumer, err := ckafka.NewConsumer(&ckafka.ConfigMap{"bootstrap.servers": f.conf.MessageQueue.Kafka.BootstrapServers, "group.id": "arcentra-agent-control-" + f.conf.Agent.ID, "enable.auto.commit": false})
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	topic := strings.ToUpper(taskpayload.AgentControlQueue(f.conf.Agent.ID))
	_, watermark, err := consumer.QueryWatermarkOffsets(topic, 0, 3000)
	if err != nil || watermark < 1 {
		t.Fatal("actual cancellation watermark unavailable")
	}
	f.wait("actual cancellation control notifications committed", func() bool {
		positions, err := consumer.Committed([]ckafka.TopicPartition{{Topic: &topic, Partition: 0}}, 1000)
		return err == nil && len(positions) == 1 && positions[0].Error == nil && int64(positions[0].Offset) == watermark
	})
	_, final, err := consumer.QueryWatermarkOffsets(topic, 0, 3000)
	if err != nil || final != watermark {
		t.Fatal("cancellation publication changed during drain")
	}
}

func TestContainerTerminalRerunObserverActivationConcurrent(t *testing.T) {
	response := &agentv1.ClaimAndFetchCopilotRerunResponse{}
	observer := &terminalRerunLostClaim{AgentServiceClient: terminalRerunClaimFixtureClient{call: func(*agentv1.ClaimAndFetchCopilotRerunRequest) (*agentv1.ClaimAndFetchCopilotRerunResponse, error) {
		return response, nil
	}}}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				out, err := observer.ClaimAndFetchCopilotRerun(context.Background(), &agentv1.ClaimAndFetchCopilotRerunRequest{Reference: &agentv1.CopilotRerunClaimReference{JobRunId: "other"}})
				if err != nil || out != response {
					t.Error("activation changed another job")
				}
			}
		}()
	}
	if !observer.activate("target") || observer.activate("replacement") {
		t.Fatal("fixture target activation was not once-only")
	}
	wg.Wait()
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if observer.delivered != 0 || observer.rejected != 0 || observer.job != "target" {
		t.Fatal("concurrent activation contaminated exact target evidence")
	}
}

func TestContainerTerminalRerunUnknownIsNotFencing(t *testing.T) {
	observer := &terminalRerunLostClaim{job: "target", claim: "old-claim", oldIncarnation: sha256.Sum256(make([]byte, 32)), AgentServiceClient: terminalRerunClaimFixtureClient{call: func(*agentv1.ClaimAndFetchCopilotRerunRequest) (*agentv1.ClaimAndFetchCopilotRerunResponse, error) {
		return nil, status.Error(codes.Unavailable, "synthetic storage outcome unknown")
	}}}
	nonce := make([]byte, 32)
	nonce[0] = 1
	out, err := observer.ClaimAndFetchCopilotRerun(context.Background(), &agentv1.ClaimAndFetchCopilotRerunRequest{Reference: &agentv1.CopilotRerunClaimReference{JobRunId: "target"}, ClaimId: "new-claim", AdmissionNonce: nonce})
	if out != nil || status.Code(err) != codes.Unavailable || observer.rejected != 0 || observer.rejectedNewClaim || observer.rejectedNewIncarnation {
		t.Fatal("unknown failure was counted as incarnation fencing")
	}
}
