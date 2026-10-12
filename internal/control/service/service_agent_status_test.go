package service

import (
	"context"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

func TestAgentExecutionStatusBoundaries(t *testing.T) {
	for _, tc := range []struct {
		current, next int
		step, valid   bool
	}{
		{2, 3, false, true}, {3, 4, false, true}, {3, 5, false, true}, {3, 6, false, true}, {3, 7, false, true},
		{3, 8, true, true}, {3, 8, false, false}, {4, 3, false, false}, {6, 4, false, false}, {4, 4, false, true}, {2, 0, false, false},
	} {
		err := validateReportedStatus(tc.current, tc.next, tc.step)
		if (err == nil) != tc.valid {
			t.Errorf("%+v: %v", tc, err)
		}
	}
	if _, err := authenticatedAgent(context.Background(), "a"); err == nil {
		t.Fatal("missing authentication accepted")
	}
	ctx := agentidentity.WithIdentity(context.Background(), agentidentity.Identity{ID: "a"})
	if _, err := authenticatedAgent(ctx, "b"); err == nil {
		t.Fatal("another agent accepted")
	}
	if _, err := authenticatedAgent(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	start := time.Unix(100, 0)
	updates := executionStatusUpdates(4, "", 0, 103, &start)
	if updates["duration"] != int64(3000) {
		t.Fatalf("unexpected duration: %v", updates)
	}
}

func TestRunningClaimRetryRequiresSameAttempt(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-arcentra-claim-id", "attempt-a"))
	if claim, retry, err := reportClaim(ctx, 2, 3, ""); err != nil || retry || claim != "attempt-a" {
		t.Fatalf("initial claim: %s %v %v", claim, retry, err)
	}
	if _, retry, err := reportClaim(ctx, 3, 3, "attempt-a"); err != nil || !retry {
		t.Fatalf("lost response retry rejected: %v", err)
	}
	if _, _, err := reportClaim(ctx, 3, 3, "attempt-b"); err == nil {
		t.Fatal("another attempt took over running job")
	}
	if _, _, err := reportClaim(context.Background(), 2, 3, ""); err == nil {
		t.Fatal("missing claim accepted")
	}
	if _, _, err := reportClaim(ctx, 3, 4, "attempt-b"); err == nil {
		t.Fatal("stale attempt reported terminal state")
	}
}

func TestTerminalDigestPreservesMapsAndEveryOutcomeField(t *testing.T) {
	request := &agentv1.ReportStepRunStatusRequest{AgentId: "agent", StepRunId: "step", Status: 5, ExitCode: 1, ErrorMessage: "failed", StartTime: 2200000000, EndTime: 2200000001, Metrics: map[string]string{"workspace": "local", "precision": "9007199254740993"}}
	original, err := terminalReportDigest(request, "claim", "pipeline", "run", "job")
	if err != nil {
		t.Fatal(err)
	}
	reordered := proto.Clone(request).(*agentv1.ReportStepRunStatusRequest)
	reordered.Metrics = map[string]string{"precision": "9007199254740993", "workspace": "local"}
	if digest, err := terminalReportDigest(reordered, "claim", "pipeline", "run", "job"); err != nil || digest != original {
		t.Fatalf("map order changed digest: %s %v", digest, err)
	}
	for _, change := range []func(*agentv1.ReportStepRunStatusRequest){
		func(r *agentv1.ReportStepRunStatusRequest) { r.AgentId += "other" },
		func(r *agentv1.ReportStepRunStatusRequest) { r.StepRunId += "other" },
		func(r *agentv1.ReportStepRunStatusRequest) { r.Status = 4 },
		func(r *agentv1.ReportStepRunStatusRequest) { r.ExitCode++ },
		func(r *agentv1.ReportStepRunStatusRequest) { r.ErrorMessage += "other" },
		func(r *agentv1.ReportStepRunStatusRequest) { r.StartTime++ },
		func(r *agentv1.ReportStepRunStatusRequest) { r.EndTime++ },
		func(r *agentv1.ReportStepRunStatusRequest) { r.Metrics["precision"] = "9007199254740992" },
	} {
		changed := proto.Clone(request).(*agentv1.ReportStepRunStatusRequest)
		change(changed)
		if digest, err := terminalReportDigest(changed, "claim", "pipeline", "run", "job"); err != nil || digest == original {
			t.Fatalf("changed outcome digest unchanged: %s %v", digest, err)
		}
	}
	for _, identity := range [][]string{{"other", "pipeline", "run", "job"}, {"claim", "other", "run", "job"}, {"claim", "pipeline", "other", "job"}, {"claim", "pipeline", "run", "other"}} {
		if digest, err := terminalReportDigest(request, identity[0], identity[1:]...); err != nil || digest == original {
			t.Fatalf("changed identity digest unchanged: %s %v", digest, err)
		}
	}
	if request.Metrics["precision"] != "9007199254740993" {
		t.Fatal("request map was mutated")
	}
}

func TestTerminalClaimRequiresOneOriginalIdentity(t *testing.T) {
	for _, claims := range [][]string{nil, {""}, {"one", "two"}, {strings.Repeat("a", 65)}} {
		ctx := metadata.NewIncomingContext(context.Background(), metadata.MD{"x-arcentra-claim-id": claims})
		if _, _, err := reportClaim(ctx, 2, 8, ""); err == nil {
			t.Fatalf("invalid terminal claim accepted: %v", claims)
		}
	}
}

func TestTerminalDigestBindsCompleteArtifactContract(t *testing.T) {
	request := &agentv1.ReportJobRunStatusRequest{
		AgentId: "agent", JobRunId: "job", Status: 4, ArtifactStorageId: "store", ArtifactStorageFingerprint: strings.Repeat("a", 64),
		ArtifactUris: map[string]string{"output": "artifacts/run/job/step/output"},
		Artifacts:    []*agentv1.ArtifactResult{{StepRunId: "step", Name: "output", StorageKey: "artifacts/run/job/step/output", Size: 1<<54 + 3, MediaType: "application/octet-stream", ExpiresAt: 2300000000, Sha256: strings.Repeat("b", 64)}},
	}
	original, err := terminalReportDigest(request, "claim", "pipeline", "run", "job")
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*agentv1.ReportJobRunStatusRequest){
		func(r *agentv1.ReportJobRunStatusRequest) { r.ArtifactStorageId += "other" },
		func(r *agentv1.ReportJobRunStatusRequest) { r.ArtifactStorageFingerprint = strings.Repeat("c", 64) },
		func(r *agentv1.ReportJobRunStatusRequest) { r.ArtifactUris["output"] += "other" },
		func(r *agentv1.ReportJobRunStatusRequest) { r.Artifacts[0].StepRunId += "other" },
		func(r *agentv1.ReportJobRunStatusRequest) { r.Artifacts[0].Name += "other" },
		func(r *agentv1.ReportJobRunStatusRequest) { r.Artifacts[0].StorageKey += "other" },
		func(r *agentv1.ReportJobRunStatusRequest) { r.Artifacts[0].Size++ },
		func(r *agentv1.ReportJobRunStatusRequest) { r.Artifacts[0].MediaType = "text/plain" },
		func(r *agentv1.ReportJobRunStatusRequest) { r.Artifacts[0].ExpiresAt++ },
		func(r *agentv1.ReportJobRunStatusRequest) { r.Artifacts[0].Sha256 = strings.Repeat("c", 64) },
		func(r *agentv1.ReportJobRunStatusRequest) { r.Artifacts = nil },
	} {
		changed := proto.Clone(request).(*agentv1.ReportJobRunStatusRequest)
		change(changed)
		if digest, err := terminalReportDigest(changed, "claim", "pipeline", "run", "job"); err != nil || digest == original {
			t.Fatalf("changed artifact contract kept receipt: %s %v", digest, err)
		}
	}
}
