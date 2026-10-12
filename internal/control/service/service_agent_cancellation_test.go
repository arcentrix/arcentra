package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestControllerCancellationReceiptsRemainExactAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cancellation.db")
	db := terminalTestDB(t, path, true)
	seedTerminalTestRun(t, db, 3)
	if err := db.WithContext(ctx).Create(&model.StepRun{StepRunID: "unstarted", JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run", AgentID: "agent", Status: 2, Attempt: 2, StepIndex: 1, PreviousStepRunID: "step"}).Error; err != nil {
		t.Fatal(err)
	}
	cancelTime := time.Now().UTC().Truncate(time.Second)
	jobs := repo.NewJobRunRepo(artifactServiceDatabase{db}).(*repo.JobRunRepo)
	if err := jobs.CancelAgentJobIfCurrent(ctx, "job", 3, map[string]any{"status": 6, "error_message": "controller cancelled", "end_time": cancelTime}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, claim string
		request     proto.Message
	}{
		{"started", "original-claim", &agentv1.ReportStepRunStatusRequest{AgentId: "agent", StepRunId: "step", Status: steprunv1.StepRunStatus_STEP_RUN_STATUS_CANCELLED, ErrorMessage: "agent observed cancellation", EndTime: cancelTime.Unix() + 1}},
		{"unstarted", agentidentity.StepClaimID("original-claim", "unstarted"), &agentv1.ReportStepRunStatusRequest{AgentId: "agent", StepRunId: "unstarted", Status: steprunv1.StepRunStatus_STEP_RUN_STATUS_CANCELLED, EndTime: cancelTime.Unix() + 1}},
		{"job", "original-claim", &agentv1.ReportJobRunStatusRequest{AgentId: "agent", JobRunId: "job", Status: 6, ErrorMessage: "agent observed cancellation", EndTime: cancelTime.Unix() + 1}},
	}
	client, stop := terminalTestClient(t, db, nil)
	for _, test := range tests {
		for _, bad := range []string{"late_success", "wrong_claim", "wrong_agent"} {
			req := proto.Clone(test.request)
			claim, want := test.claim, codes.FailedPrecondition
			if bad == "wrong_claim" {
				claim, want = "unrelated-claim", codes.PermissionDenied
			}
			switch r := req.(type) {
			case *agentv1.ReportJobRunStatusRequest:
				if bad == "late_success" {
					r.Status = 4
					r.ArtifactUris = map[string]string{"late": "untrusted"}
				}
				if bad == "wrong_agent" {
					r.AgentId = "other"
					want = codes.PermissionDenied
				}
			case *agentv1.ReportStepRunStatusRequest:
				if bad == "late_success" {
					r.Status = steprunv1.StepRunStatus_STEP_RUN_STATUS_SUCCESS
				}
				if bad == "wrong_agent" {
					r.AgentId = "other"
					want = codes.PermissionDenied
				}
			}
			if err := callTerminalReport(client, req, claim); status.Code(err) != want {
				t.Fatalf("%s %s: %v", test.name, bad, err)
			}
		}
	}
	// 控制消息可能尚未到达 Agent；原认领的重试得到可验证的取消拒绝，而不是执行成功
	for _, test := range tests[:2] {
		req := proto.Clone(test.request).(*agentv1.ReportStepRunStatusRequest)
		req.Status = steprunv1.StepRunStatus_STEP_RUN_STATUS_RUNNING
		err := callTerminalReport(client, req, test.claim)
		if !agentidentity.IsCancelledStepClaim(err, req.StepRunId, test.claim) {
			t.Fatalf("%s cancelled claim rejection lost exact scope: %v", test.name, err)
		}
	}
	stop()
	client, stop = terminalTestClient(t, db, func(proto.Message) error {
		return status.Error(codes.Unavailable, "cancellation ACK lost after commit")
	})
	for _, test := range tests {
		if err := callTerminalReport(client, test.request, test.claim); status.Code(err) != codes.Unavailable {
			t.Fatalf("%s lost ACK: %v", test.name, err)
		}
	}
	stop()
	sqlDB, _ := db.DB()
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	db = terminalTestDB(t, path, false)
	client, _ = terminalTestClient(t, db, nil)
	for _, test := range tests {
		for range 2 {
			if err := callTerminalReport(client, proto.Clone(test.request), test.claim); err != nil {
				t.Fatalf("%s duplicate cancellation after restart: %v", test.name, err)
			}
		}
		changed := proto.Clone(test.request)
		switch r := changed.(type) {
		case *agentv1.ReportJobRunStatusRequest:
			r.ErrorMessage = "changed cancellation"
		case *agentv1.ReportStepRunStatusRequest:
			r.ErrorMessage = "changed cancellation"
		}
		if err := callTerminalReport(client, changed, test.claim); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("%s conflicting cancellation accepted: %v", test.name, err)
		}
	}
	job, err := repo.NewJobRunRepo(artifactServiceDatabase{db}).GetByJobRunID(ctx, "job")
	if err != nil || job.Status != 6 || job.ClaimID != "original-claim" || job.CancellationClaimID != "original-claim" || len(job.TerminalReportDigest) != 64 || job.ErrorMessage != "controller cancelled" || !job.EndTime.Equal(cancelTime) || job.ArtifactURIs != "" {
		t.Fatalf("cancellation receipt changed result: %+v %v", job, err)
	}
	for _, stepID := range []string{"step", "unstarted"} {
		step, err := repo.NewStepRunRepo(artifactServiceDatabase{db}).GetByStepRunID(ctx, stepID)
		if err != nil || step.Status != 6 || step.CancellationClaimID != "original-claim" || len(step.TerminalReportDigest) != 64 {
			t.Fatalf("step cancellation receipt lost: %+v %v", step, err)
		}
		if stepID == "unstarted" && step.ClaimID != "" {
			t.Fatal("unstarted cancellation manufactured an execution claim")
		}
	}
}

func TestLegacyCancellationWithoutProvenanceRemainsRejected(t *testing.T) {
	db := terminalTestDB(t, filepath.Join(t.TempDir(), "legacy.db"), true)
	seedTerminalTestRun(t, db, 6)
	client, _ := terminalTestClient(t, db, nil)
	for _, req := range []proto.Message{
		&agentv1.ReportJobRunStatusRequest{AgentId: "agent", JobRunId: "job", Status: 6},
		&agentv1.ReportStepRunStatusRequest{AgentId: "agent", StepRunId: "step", Status: steprunv1.StepRunStatus_STEP_RUN_STATUS_CANCELLED},
	} {
		if err := callTerminalReport(client, req, "original-claim"); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("legacy cancellation accepted: %v", err)
		}
		if step, ok := req.(*agentv1.ReportStepRunStatusRequest); ok {
			step.Status = steprunv1.StepRunStatus_STEP_RUN_STATUS_RUNNING
			err := callTerminalReport(client, step, "original-claim")
			if status.Code(err) != codes.FailedPrecondition || agentidentity.IsCancelledStepClaim(err, step.StepRunId, "original-claim") {
				t.Fatalf("legacy cancellation produced typed cancellation authority: %v", err)
			}
		}
	}
}
