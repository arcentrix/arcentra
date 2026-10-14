package service_test

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/internal/shared/grpc/interceptor"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type generationTokenVerifier struct {
	db      *gorm.DB
	revoked atomic.Bool
}

func (v *generationTokenVerifier) VerifyAgentToken(ctx context.Context, token, hint string) (string, error) {
	if v.revoked.Load() || token != "generation-fixture-token" || (hint != "" && hint != "agent") {
		return "", fmt.Errorf("fixture credential rejected")
	}
	var rows []model.Agent
	if err := v.db.WithContext(ctx).Select("agent_id", "is_enabled").Where("agent_id = ?", "agent").Limit(2).Find(&rows).Error; err != nil {
		return "", err
	}
	if len(rows) != 1 || rows[0].AgentID != "agent" || rows[0].IsEnabled != 1 {
		return "", fmt.Errorf("fixture agent disabled")
	}
	return "agent", nil
}
func generationClient(t *testing.T, db *gorm.DB, after func(proto.Message) error) (agentv1.AgentServiceClient, *generationTokenVerifier, func()) {
	t.Helper()
	verifier := &generationTokenVerifier{db: db}
	interceptor.SetTokenVerifier(verifier)
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer(grpc.ChainUnaryInterceptor(interceptor.AuthUnaryInterceptor(), func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		response, err := handler(ctx, req)
		if err == nil && after != nil {
			err = after(req.(proto.Message))
		}
		if err != nil {
			return nil, err
		}
		return response, nil
	}))
	store := generationServiceDatabase{db}
	agentv1.RegisterAgentServiceServer(server, service.NewAgentServiceImpl(service.NewAgentService(nil, repo.NewStepRunRepo(store), nil, repo.NewJobRunRepo(store)), nil))
	safe.Go(func() { _ = server.Serve(listener) })
	conn, err := grpc.NewClient("passthrough:///generation-fixture", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	stop := func() { _ = conn.Close(); server.Stop(); _ = listener.Close() }
	t.Cleanup(func() { stop(); verifier.revoked.Store(true) })
	return agentv1.NewAgentServiceClient(conn), verifier, stop
}
func generationRPCContext(claim string) context.Context {
	return metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer generation-fixture-token", "x-arcentra-claim-id", claim))
}
func generationServiceFixture(t *testing.T, path string) (*gorm.DB, taskqueue.JobGenerationTransition, *taskqueue.JobInputManifest) {
	t.Helper()
	ctx := context.Background()
	db := generationTestDB(t, path, true)
	if err := db.WithContext(ctx).AutoMigrate(&model.Agent{}, &model.JobRunGeneration{}); err != nil {
		t.Fatal(err)
	}
	for _, v := range []any{&model.Agent{AgentID: "agent", IsEnabled: 1}, &model.PipelineRun{RunID: "run", PipelineID: "pipeline", Status: model.PipelineStatusRunning}, &model.JobRun{JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run", Status: 1}} {
		if err := db.WithContext(ctx).Create(v).Error; err != nil {
			t.Fatal(err)
		}
	}
	policy := &pipelinev1.Retry{Mode: "whole_job", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}}
	policyJSON, _ := sonic.MarshalString(policy)
	for index, id := range []string{"step-1", "step-2"} {
		previous := ""
		if index > 0 {
			previous = "step-1"
		}
		if err := db.WithContext(ctx).Create(&model.StepRun{StepRunID: id, Name: "shell", JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run", Attempt: index + 1, RetryMode: "whole_job", RetryPolicy: policyJSON, PreviousStepRunID: previous, Uses: "shell", Status: 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	runtime := &taskqueue.RuntimeRequest{Type: taskqueue.RuntimeContainerdOfflineV1, Image: "fixture/image@sha256:" + strings.Repeat("a", 64), Resources: taskqueue.RuntimeResources{CPU: "500m", Memory: "64Mi"}}
	runtime.BindingDigest, _ = taskqueue.RuntimeBindingDigest("pipeline", "run", "job", "agent", runtime)
	payload := &taskqueue.JobRunTaskPayload{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", AgentID: "agent", Runtime: runtime, Retry: policy, Steps: []taskqueue.StepPayload{{Name: "shell", StepRunID: "step-1", AttemptRunIDs: []string{"step-1", "step-2"}, Uses: "shell", Args: map[string]any{"run": "exit 9"}}}}
	plan, err := taskqueue.NewJobRetryPlan(payload)
	if err != nil {
		t.Fatal(err)
	}
	jobs := repo.NewJobRunRepo(generationServiceDatabase{db}).(*repo.JobRunRepo)
	if err := jobs.CaptureRuntimeRetryExpectation(ctx, "pipeline", "run", "job", "agent", nil, runtime, nil, nil, plan); err != nil {
		t.Fatal(err)
	}
	job, _ := jobs.GetByJobRunID(ctx, "job")
	if err := jobs.PatchReportedStatus(ctx, job, map[string]any{"status": 3, "claim_id": "original"}); err != nil {
		t.Fatal(err)
	}
	job, _ = jobs.GetByJobRunID(ctx, "job")
	identity := taskqueue.JobInputIdentity{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", AgentID: "agent", ClaimID: "original", RuntimeBindingDigest: runtime.BindingDigest}
	req := taskqueue.JobGenerationTransition{Version: 1, Identity: identity, RetryPlanDigest: job.RetryPlanDigest, Operation: "admit"}
	req.RequestDigest, _ = req.Digest()
	manifest, _ := taskqueue.NewJobInputManifest("", nil, nil)
	return db, req, manifest
}
func reconcileGeneration(t *testing.T, client agentv1.AgentServiceClient, transition taskqueue.JobGenerationTransition) (*taskqueue.JobGeneration, error) {
	t.Helper()
	s, err := transition.Canonical()
	if err != nil {
		return nil, err
	}
	response, err := client.ReconcileJobGeneration(generationRPCContext(transition.Identity.ClaimID), &agentv1.ReconcileJobGenerationRequest{AgentId: transition.Identity.AgentID, TransitionJson: s})
	if err != nil {
		return nil, err
	}
	return taskqueue.ParseJobGeneration(response.GenerationJson)
}
func generationServicePin(t *testing.T, client agentv1.AgentServiceClient, admit taskqueue.JobGenerationTransition, m *taskqueue.JobInputManifest) string {
	t.Helper()
	body, _ := m.Canonical()
	i := admit.Identity
	r, err := client.GetOrRecordJobInputs(generationRPCContext(i.ClaimID), &agentv1.GetOrRecordJobInputsRequest{AgentId: i.AgentID, PipelineId: i.PipelineID, PipelineRunId: i.PipelineRunID, JobRunId: i.JobRunID, OriginalClaimId: i.ClaimID, RuntimeBindingDigest: i.RuntimeBindingDigest, SourceBindingDigest: i.SourceBindingDigest, ManifestJson: body})
	if err != nil {
		t.Fatal(err)
	}
	return r.ManifestDigest
}
func generationServiceFailure(t *testing.T, client agentv1.AgentServiceClient, g *taskqueue.JobGeneration) *agentv1.ReportStepRunStatusRequest {
	t.Helper()
	claim := agentidentity.StepClaimID("original", "step-1")
	request := &agentv1.ReportStepRunStatusRequest{AgentId: "agent", StepRunId: "step-1", Status: 3, GenerationId: g.GenerationID, GenerationBindingDigest: g.BindingDigest}
	if _, err := client.ReportStepRunStatus(generationRPCContext(claim), request); err != nil {
		t.Fatal(err)
	}
	request.Status = 5
	request.ExitCode = 9
	request.RetryableShellExit = true
	if _, err := client.ReportStepRunStatus(generationRPCContext(claim), request); err != nil {
		t.Fatal(err)
	}
	return request
}
func generationServiceAdvance(t *testing.T, admit taskqueue.JobGenerationTransition, g *taskqueue.JobGeneration, pin string, failure *agentv1.ReportStepRunStatusRequest) taskqueue.JobGenerationTransition {
	t.Helper()
	receipt, err := agentidentity.TerminalReceiptDigest(failure, agentidentity.StepClaimID("original", "step-1"), "pipeline", "run", "job")
	if err != nil {
		t.Fatal(err)
	}
	cleanup := &taskqueue.JobGenerationCleanupReceipt{Version: 1, PipelineRunID: "run", JobRunID: "job", AgentID: "agent", OriginalClaimID: "original", RuntimeBindingDigest: g.RuntimeBindingDigest, GenerationID: g.GenerationID, GenerationBindingDigest: g.BindingDigest, RuntimeClaimID: g.RuntimeClaimID, SessionID: g.RuntimeClaimID, Namespace: "fixture", ContainerID: "arcentra-" + g.RuntimeClaimID, SnapshotID: "arcentra-" + g.RuntimeClaimID + "-snapshot", Snapshotter: "fixture", ResourceOwnerDigest: strings.Repeat("a", 64), WorkspaceID: "job-" + g.RuntimeClaimID, WorkspaceDevice: 1, WorkspaceInode: 2, ProcessesAbsent: true, ContainerAbsent: true, SnapshotAbsent: true, WorkspaceAbsent: true}
	req := admit
	req.Operation = "advance"
	req.InputManifestDigest = pin
	req.PreviousGenerationID = g.GenerationID
	req.FailedStepRunID = "step-1"
	req.FailedStepReceiptDigest = receipt
	req.CleanupReceipt = cleanup
	req.RequestDigest, _ = req.Digest()
	return req
}

func TestGenerationAuthenticatedGRPCLostAdvanceACKReopenHistoricalReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "generation.db")
	db, admit, manifest := generationServiceFixture(t, path)
	var lose atomic.Bool
	lose.Store(true)
	client, _, stop := generationClient(t, db, func(p proto.Message) error {
		if req, ok := p.(*agentv1.ReconcileJobGenerationRequest); ok {
			transition, _ := taskqueue.ParseJobGenerationTransition(req.TransitionJson)
			if transition.Operation == "advance" && lose.CompareAndSwap(true, false) {
				return status.Error(codes.Unavailable, "synthetic lost ACK")
			}
		}
		return nil
	})
	first, err := reconcileGeneration(t, client, admit)
	if err != nil {
		t.Fatal(err)
	}
	pin := generationServicePin(t, client, admit, manifest)
	failure := generationServiceFailure(t, client, first)
	advance := generationServiceAdvance(t, admit, first, pin, failure)
	if _, err := reconcileGeneration(t, client, advance); status.Code(err) != codes.Unavailable {
		t.Fatalf("lost ACK: %v", err)
	}
	var before model.JobRun
	if err := db.Where("job_run_id = ?", "job").First(&before).Error; err != nil {
		t.Fatal(err)
	}
	stop()
	sqlDB, _ := db.DB()
	_ = sqlDB.Close()
	db = generationTestDB(t, path, false)
	client, _, _ = generationClient(t, db, nil)
	inspect := admit
	inspect.Operation = "inspect"
	inspect.RequestDigest, _ = inspect.Digest()
	observed, err := reconcileGeneration(t, client, inspect)
	if err != nil || observed.ExecutionAllowed || observed.GenerationID != before.CurrentGenerationID {
		t.Fatalf("restart inspect: %+v %v", observed, err)
	}
	second, err := reconcileGeneration(t, client, advance)
	if err != nil || second.GenerationID != before.CurrentGenerationID || second.Number != 2 {
		t.Fatalf("exact transition replay: %+v %v", second, err)
	}
	if _, err := client.ReportStepRunStatus(generationRPCContext(agentidentity.StepClaimID("original", "step-1")), failure); err != nil {
		t.Fatalf("historical terminal replay rejected: %v", err)
	}
	changed := proto.Clone(failure).(*agentv1.ReportStepRunStatusRequest)
	changed.RetryableShellExit = false
	if _, err := client.ReportStepRunStatus(generationRPCContext(agentidentity.StepClaimID("original", "step-1")), changed); err == nil {
		t.Fatal("changed historical receipt accepted")
	}
	running := &agentv1.ReportStepRunStatusRequest{AgentId: "agent", StepRunId: "step-2", Status: 3, GenerationId: first.GenerationID, GenerationBindingDigest: first.BindingDigest}
	if _, err := client.ReportStepRunStatus(generationRPCContext(agentidentity.StepClaimID("original", "step-2")), running); err == nil {
		t.Fatal("old generation commanded new attempt")
	}
	running.GenerationId = second.GenerationID
	running.GenerationBindingDigest = second.BindingDigest
	if _, err := client.ReportStepRunStatus(generationRPCContext(agentidentity.StepClaimID("original", "step-2")), running); err != nil {
		t.Fatal(err)
	}
	running.Status = steprunv1.StepRunStatus_STEP_RUN_STATUS_SUCCESS
	if _, err := client.ReportStepRunStatus(generationRPCContext(agentidentity.StepClaimID("original", "step-2")), running); err != nil {
		t.Fatal(err)
	}
	final := &agentv1.ReportJobRunStatusRequest{AgentId: "agent", JobRunId: "job", Status: 4, RuntimeBindingDigest: admit.Identity.RuntimeBindingDigest, GenerationId: second.GenerationID, GenerationBindingDigest: second.BindingDigest}
	for range 2 {
		if response, err := client.ReportJobRunStatus(generationRPCContext("original"), final); err != nil || !response.Success {
			t.Fatalf("final receipt: %+v %v", response, err)
		}
	}
	var count int64
	db.Model(&model.JobRunGeneration{}).Count(&count)
	if count != 2 {
		t.Fatal("lost ACK allocated twice")
	}
}

func TestGenerationGRPCRejectsUnauthenticatedRevokedForeignAndDuplicateClaims(t *testing.T) {
	db, admit, _ := generationServiceFixture(t, ":memory:")
	client, verifier, _ := generationClient(t, db, nil)
	body, _ := admit.Canonical()
	req := &agentv1.ReconcileJobGenerationRequest{AgentId: "agent", TransitionJson: body}
	for _, md := range []metadata.MD{nil, metadata.Pairs("authorization", "Bearer wrong", "x-arcentra-claim-id", "original"), metadata.Pairs("authorization", "Bearer generation-fixture-token"), metadata.Pairs("authorization", "Bearer generation-fixture-token", "x-arcentra-claim-id", "other"), metadata.Pairs("authorization", "Bearer generation-fixture-token", "x-arcentra-claim-id", "original", "x-arcentra-claim-id", "original")} {
		if _, err := client.ReconcileJobGeneration(metadata.NewOutgoingContext(context.Background(), md), req); err == nil {
			t.Fatal("invalid authentication or claim accepted")
		}
	}
	foreign := proto.Clone(req).(*agentv1.ReconcileJobGenerationRequest)
	foreign.AgentId = "foreign"
	if _, err := client.ReconcileJobGeneration(generationRPCContext("original"), foreign); err == nil {
		t.Fatal("foreign agent accepted")
	}
	verifier.revoked.Store(true)
	if _, err := client.ReconcileJobGeneration(generationRPCContext("original"), req); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("revoked token accepted: %v", err)
	}
	var count int64
	db.Model(&model.JobRunGeneration{}).Count(&count)
	if count != 0 {
		t.Fatal("rejected request mutated authority")
	}
}

type generationServiceDatabase struct{ db *gorm.DB }

func (d generationServiceDatabase) Database() *gorm.DB { return d.db }
func generationTestDB(t *testing.T, path string, migrate bool) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if migrate {
		if err := db.WithContext(context.Background()).AutoMigrate(&model.Project{}, &model.Pipeline{}, &model.PipelineRun{}, &model.JobRun{}, &model.StepRun{}, &model.ProjectArtifact{}); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestGenerationAuthenticatedGRPCAllSkippedPinnedPreparationSucceeds(t *testing.T) {
	db, admit, manifest := generationServiceFixture(t, ":memory:")
	client, _, _ := generationClient(t, db, nil)
	generation, err := reconcileGeneration(t, client, admit)
	if err != nil {
		t.Fatal(err)
	}
	generationServicePin(t, client, admit, manifest)
	// 使用实际 skip 报告的非零作业开始时间；它不是 RUNNING 准入证据
	for _, id := range []string{"step-1", "step-2"} {
		request := &agentv1.ReportStepRunStatusRequest{AgentId: "agent", StepRunId: id, Status: steprunv1.StepRunStatus_STEP_RUN_STATUS_SKIPPED, ErrorMessage: "step condition is false", StartTime: 2200000000, EndTime: 2200000001, GenerationId: generation.GenerationID, GenerationBindingDigest: generation.BindingDigest}
		response, err := client.ReportStepRunStatus(generationRPCContext(agentidentity.StepClaimID("original", id)), request)
		if err != nil || !response.Success {
			t.Fatalf("skip receipt: %+v %v", response, err)
		}
	}
	var state model.JobRunGeneration
	if err := db.Where("generation_id = ?", generation.GenerationID).First(&state).Error; err != nil || state.State != "preparing" {
		t.Fatalf("skip invented command admission: %+v %v", state, err)
	}
	final := &agentv1.ReportJobRunStatusRequest{AgentId: "agent", JobRunId: "job", Status: 4, StartTime: 2200000000, EndTime: 2200000001, RuntimeBindingDigest: admit.Identity.RuntimeBindingDigest, GenerationId: generation.GenerationID, GenerationBindingDigest: generation.BindingDigest}
	for range 2 {
		response, err := client.ReportJobRunStatus(generationRPCContext("original"), final)
		if err != nil || !response.Success {
			t.Fatalf("all-skipped final acknowledgment: %+v %v", response, err)
		}
	}
	var job model.JobRun
	db.Where("job_run_id = ?", "job").First(&job)
	if job.Status != 4 || job.TerminalReportDigest == "" || job.InputManifestState != "pinned" {
		t.Fatalf("all-skipped final was not committed: %+v", job)
	}
}

func TestGenerationPreparationSuccessRejectsAnythingExceptExactPinnedSkippedRound(t *testing.T) {
	mutations := map[string]func(*gorm.DB){
		"unknown-input": func(db *gorm.DB) {
			db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(map[string]any{"input_manifest_state": "unknown", "input_manifest": "", "input_manifest_digest": ""})
		},
		"success-step": func(db *gorm.DB) { db.Model(&model.StepRun{}).Where("step_run_id = ?", "step-1").Update("status", 4) },
		"failed-step":  func(db *gorm.DB) { db.Model(&model.StepRun{}).Where("step_run_id = ?", "step-1").Update("status", 5) },
		"running-step": func(db *gorm.DB) { db.Model(&model.StepRun{}).Where("step_run_id = ?", "step-1").Update("status", 3) },
		"missing-receipt": func(db *gorm.DB) {
			db.Model(&model.StepRun{}).Where("step_run_id = ?", "step-1").Update("terminal_report_digest", "")
		},
		"wrong-generation": func(db *gorm.DB) {
			db.Model(&model.StepRun{}).Where("step_run_id = ?", "step-1").Update("generation_id", "different")
		},
		"wrong-claim": func(db *gorm.DB) {
			db.Model(&model.StepRun{}).Where("step_run_id = ?", "step-1").Update("claim_id", "different")
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			db, admit, manifest := generationServiceFixture(t, ":memory:")
			client, _, _ := generationClient(t, db, nil)
			generation, err := reconcileGeneration(t, client, admit)
			if err != nil {
				t.Fatal(err)
			}
			generationServicePin(t, client, admit, manifest)
			for _, id := range []string{"step-1", "step-2"} {
				request := &agentv1.ReportStepRunStatusRequest{AgentId: "agent", StepRunId: id, Status: 8, StartTime: 2200000000, EndTime: 2200000001, GenerationId: generation.GenerationID, GenerationBindingDigest: generation.BindingDigest}
				if _, err := client.ReportStepRunStatus(generationRPCContext(agentidentity.StepClaimID("original", id)), request); err != nil {
					t.Fatal(err)
				}
			}
			mutate(db)
			final := &agentv1.ReportJobRunStatusRequest{AgentId: "agent", JobRunId: "job", Status: 4, RuntimeBindingDigest: admit.Identity.RuntimeBindingDigest, GenerationId: generation.GenerationID, GenerationBindingDigest: generation.BindingDigest}
			if _, err := client.ReportJobRunStatus(generationRPCContext("original"), final); err == nil {
				t.Fatal("invalid preparing success accepted")
			}
			var job model.JobRun
			db.Where("job_run_id = ?", "job").First(&job)
			if job.Status != 3 || job.TerminalReportDigest != "" {
				t.Fatal("invalid preparing success changed final state")
			}
		})
	}
}

func TestGenerationPublicStepDeleteRetainsQueuedAndTerminalAttempts(t *testing.T) {
	db, admit, manifest := generationServiceFixture(t, ":memory:")
	client, _, _ := generationClient(t, db, nil)
	stepService := service.NewStepRunServiceImpl(repo.NewStepRunRepo(generationServiceDatabase{db}))
	response, err := stepService.DeleteStepRun(context.Background(), &steprunv1.DeleteStepRunRequest{StepRunId: "step-2"})
	if err != nil || response.Success {
		t.Fatalf("queued delete: %+v %v", response, err)
	}
	generation, err := reconcileGeneration(t, client, admit)
	if err != nil {
		t.Fatal(err)
	}
	generationServicePin(t, client, admit, manifest)
	failure := generationServiceFailure(t, client, generation)
	response, err = stepService.DeleteStepRun(context.Background(), &steprunv1.DeleteStepRunRequest{StepRunId: "step-1"})
	if err != nil || response.Success {
		t.Fatalf("terminal delete: %+v %v", response, err)
	}
	if _, err := client.ReportStepRunStatus(generationRPCContext(agentidentity.StepClaimID("original", "step-1")), failure); err != nil {
		t.Fatalf("rejected delete broke exact replay: %v", err)
	}
}

func TestGenerationAuthenticatedCancellationRejectsStaleFirstReceiptAndPreservesReplay(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(fmt.Sprintf("started-%t", started), func(t *testing.T) {
			db, admit, manifest := generationServiceFixture(t, ":memory:")
			client, _, _ := generationClient(t, db, nil)
			first, err := reconcileGeneration(t, client, admit)
			if err != nil {
				t.Fatal(err)
			}
			pin := generationServicePin(t, client, admit, manifest)
			failure := generationServiceFailure(t, client, first)
			second, err := reconcileGeneration(t, client, generationServiceAdvance(t, admit, first, pin, failure))
			if err != nil {
				t.Fatal(err)
			}
			if started {
				if _, err := client.ReportStepRunStatus(generationRPCContext(agentidentity.StepClaimID("original", "step-2")), &agentv1.ReportStepRunStatusRequest{AgentId: "agent", StepRunId: "step-2", Status: 3, GenerationId: second.GenerationID, GenerationBindingDigest: second.BindingDigest}); err != nil {
					t.Fatal(err)
				}
			}
			jobs := repo.NewJobRunRepo(generationServiceDatabase{db}).(*repo.JobRunRepo)
			job, err := jobs.GetByJobRunID(context.Background(), "job")
			if err != nil {
				t.Fatal(err)
			}
			if err := jobs.FinalizeRetryJobIfCurrent(context.Background(), job, map[string]any{"status": 6, "end_time": time.Now()}); err != nil {
				t.Fatal(err)
			}
			stepRequest := &agentv1.ReportStepRunStatusRequest{AgentId: "agent", StepRunId: "step-2", Status: 6, StartTime: 2200000000, EndTime: 2200000001}
			jobRequest := &agentv1.ReportJobRunStatusRequest{AgentId: "agent", JobRunId: "job", Status: 6, RuntimeBindingDigest: admit.Identity.RuntimeBindingDigest, StartTime: 2200000000, EndTime: 2200000001}
			for _, proof := range []agentidentity.GenerationReport{{}, {ID: first.GenerationID, BindingDigest: first.BindingDigest}, {ID: "foreign", BindingDigest: "v1:" + strings.Repeat("e", 64)}} {
				stepRequest.GenerationId = proof.ID
				stepRequest.GenerationBindingDigest = proof.BindingDigest
				jobRequest.GenerationId = proof.ID
				jobRequest.GenerationBindingDigest = proof.BindingDigest
				if _, err := client.ReportStepRunStatus(generationRPCContext(agentidentity.StepClaimID("original", "step-2")), stepRequest); err == nil {
					t.Fatal("stale first step cancellation receipt accepted")
				}
				if _, err := client.ReportJobRunStatus(generationRPCContext("original"), jobRequest); err == nil {
					t.Fatal("stale first job cancellation receipt accepted")
				}
			}
			stepRequest.GenerationId = second.GenerationID
			stepRequest.GenerationBindingDigest = second.BindingDigest
			jobRequest.GenerationId = second.GenerationID
			jobRequest.GenerationBindingDigest = second.BindingDigest
			for range 2 {
				if response, err := client.ReportStepRunStatus(generationRPCContext(agentidentity.StepClaimID("original", "step-2")), stepRequest); err != nil || !response.Success {
					t.Fatalf("exact cancellation step ACK: %+v %v", response, err)
				}
				if response, err := client.ReportJobRunStatus(generationRPCContext("original"), jobRequest); err != nil || !response.Success {
					t.Fatalf("exact cancellation job ACK: %+v %v", response, err)
				}
			}
			if _, err := client.ReportStepRunStatus(generationRPCContext(agentidentity.StepClaimID("original", "step-1")), failure); err != nil {
				t.Fatalf("historical terminal lost replay after cancellation: %v", err)
			}
		})
	}
}
