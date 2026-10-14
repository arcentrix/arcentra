package service

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func inputServiceFixture(t *testing.T, path string) (*gorm.DB, *agentv1.GetOrRecordJobInputsRequest) {
	t.Helper()
	ctx := context.Background()
	db := terminalTestDB(t, path, true)
	seedTerminalTestRun(t, db, 1)
	if err := db.WithContext(ctx).AutoMigrate(&model.Agent{}); err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Create(&model.Agent{AgentID: "agent", IsEnabled: 1}).Error; err != nil {
		t.Fatal(err)
	}
	r := repo.NewJobRunRepo(artifactServiceDatabase{db}).(*repo.JobRunRepo)
	source, err := sourceprovenance.NewBinding("pipeline", "run", "job", "git", "https://example.invalid/repo.git", "main")
	if err != nil {
		t.Fatal(err)
	}
	runtime := &taskqueue.RuntimeRequest{Type: taskqueue.RuntimeContainerdOfflineV1, Image: "registry.example/build@sha256:" + strings.Repeat("a", 64), Resources: taskqueue.RuntimeResources{CPU: "500m", Memory: "64Mi"}}
	runtime.BindingDigest, err = taskqueue.RuntimeBindingDigest("pipeline", "run", "job", "agent", runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CaptureRuntimeInputExpectation(ctx, "pipeline", "run", "job", "agent", source, runtime, nil, nil); err != nil {
		t.Fatal(err)
	}
	job, _ := r.GetByJobRunID(ctx, "job")
	if err := r.PatchReportedStatus(ctx, job, map[string]any{"status": 3, "claim_id": "original-claim"}); err != nil {
		t.Fatal(err)
	}
	m, err := taskqueue.NewJobInputManifest(strings.Repeat("a", 40), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := m.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	return db, &agentv1.GetOrRecordJobInputsRequest{AgentId: "agent", PipelineId: "pipeline", PipelineRunId: "run", JobRunId: "job", OriginalClaimId: "original-claim", RuntimeBindingDigest: runtime.BindingDigest, SourceBindingDigest: source.Digest, ManifestJson: encoded}
}

func TestJobInputsGRPCLostAckReopensAndReplaysExactPin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inputs.db")
	db, req := inputServiceFixture(t, path)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-arcentra-claim-id", "original-claim")
	client, stop := terminalTestClient(t, db, func(message proto.Message) error {
		if request, ok := message.(*agentv1.GetOrRecordJobInputsRequest); ok && request.ManifestJson != "" {
			return status.Error(codes.Unavailable, "synthetic lost acknowledgment")
		}
		return nil
	})
	read := proto.Clone(req).(*agentv1.GetOrRecordJobInputsRequest)
	read.ManifestJson = ""
	if response, err := client.GetOrRecordJobInputs(ctx, read); err != nil || response.State != "unknown" {
		t.Fatalf("initial read: %+v %v", response, err)
	}
	if _, err := client.GetOrRecordJobInputs(ctx, req); status.Code(err) != codes.Unavailable {
		t.Fatalf("lost ack: %v", err)
	}
	before := sourceReadJob(t, db)
	stop()
	connection, _ := db.DB()
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	db = terminalTestDB(t, path, false)
	client, _ = terminalTestClient(t, db, nil)
	for _, request := range []*agentv1.GetOrRecordJobInputsRequest{read, req, req} {
		response, err := client.GetOrRecordJobInputs(ctx, request)
		if err != nil || response.State != "pinned" || response.ManifestJson != req.ManifestJson || response.ManifestDigest != before.InputManifestDigest {
			t.Fatalf("reopened replay: %+v %v", response, err)
		}
	}
	if after := sourceReadJob(t, db); !reflect.DeepEqual(before, after) {
		t.Fatal("replay changed persisted record")
	}
	changed := proto.Clone(req).(*agentv1.GetOrRecordJobInputsRequest)
	changed.ManifestJson = strings.Replace(req.ManifestJson, strings.Repeat("a", 40), strings.Repeat("b", 40), 1)
	if _, err := client.GetOrRecordJobInputs(ctx, changed); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("different commit accepted: %v", err)
	}
	if after := sourceReadJob(t, db); !reflect.DeepEqual(before, after) {
		t.Fatal("rejected pin changed record")
	}
}

func TestJobInputsGRPCRejectsMissingDuplicateAndForeignClaims(t *testing.T) {
	db, req := inputServiceFixture(t, ":memory:")
	client, _ := terminalTestClient(t, db, nil)
	for _, claims := range [][]string{nil, {"other"}, {"original-claim", "original-claim"}, {" original-claim"}} {
		md := metadata.MD{}
		for _, claim := range claims {
			md.Append("x-arcentra-claim-id", claim)
		}
		if _, err := client.GetOrRecordJobInputs(metadata.NewOutgoingContext(context.Background(), md), req); err == nil {
			t.Fatalf("claims accepted: %v", claims)
		}
	}
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-arcentra-claim-id", "original-claim")
	for _, change := range []func(*agentv1.GetOrRecordJobInputsRequest){func(r *agentv1.GetOrRecordJobInputsRequest) { r.AgentId = "foreign" }, func(r *agentv1.GetOrRecordJobInputsRequest) { r.PipelineRunId = "other" }, func(r *agentv1.GetOrRecordJobInputsRequest) { r.ManifestJson += " " }} {
		changed := proto.Clone(req).(*agentv1.GetOrRecordJobInputsRequest)
		change(changed)
		if _, err := client.GetOrRecordJobInputs(ctx, changed); err == nil {
			t.Fatal("invalid identity or encoding accepted")
		}
	}
	if job := sourceReadJob(t, db); job.InputManifestState != "unknown" {
		t.Fatal("rejected RPC fixed a manifest")
	}
}

func TestJobInputsTerminalReceiptCannotContradictPin(t *testing.T) {
	db, req := inputServiceFixture(t, ":memory:")
	client, _ := terminalTestClient(t, db, nil)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-arcentra-claim-id", "original-claim")
	if _, err := client.GetOrRecordJobInputs(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.StepRun{}).Where("job_run_id = ?", "job").Update("status", 4).Error; err != nil {
		t.Fatal(err)
	}
	original := sourceReadJob(t, db)
	terminal := &agentv1.ReportJobRunStatusRequest{AgentId: "agent", JobRunId: "job", Status: 4, RuntimeBindingDigest: req.RuntimeBindingDigest, SourceCheckout: &agentv1.SourceCheckout{BindingDigest: req.SourceBindingDigest, ResolvedCommit: strings.Repeat("b", 40)}}
	if err := callTerminalReport(client, terminal, "original-claim"); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("contradictory proof accepted: %v", err)
	}
	terminal.SourceCheckout = nil
	if err := callTerminalReport(client, terminal, "original-claim"); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("successful checkout omitted proof: %v", err)
	}
	if after := sourceReadJob(t, db); !reflect.DeepEqual(original, after) {
		t.Fatal("rejected proof changed job")
	}
	terminal.SourceCheckout = &agentv1.SourceCheckout{BindingDigest: req.SourceBindingDigest, ResolvedCommit: strings.Repeat("a", 40)}
	for range 2 {
		if err := callTerminalReport(client, terminal, "original-claim"); err != nil {
			t.Fatal(err)
		}
	}
	after := sourceReadJob(t, db)
	if after.SourceResolvedCommit != strings.Repeat("a", 40) || after.InputManifest != original.InputManifest || after.InputManifestDigest != original.InputManifestDigest {
		t.Fatal("terminal proof altered pin")
	}
}

func TestJobInputsTerminalSuccessRequiresNewDispatchPin(t *testing.T) {
	db, req := inputServiceFixture(t, ":memory:")
	client, _ := terminalTestClient(t, db, nil)
	if err := db.Model(&model.StepRun{}).Where("job_run_id = ?", "job").Update("status", 4).Error; err != nil {
		t.Fatal(err)
	}
	request := &agentv1.ReportJobRunStatusRequest{AgentId: "agent", JobRunId: "job", Status: 4, RuntimeBindingDigest: req.RuntimeBindingDigest}
	if err := callTerminalReport(client, request, "original-claim"); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("success without pin: %v", err)
	}
	request.Status = 5
	if err := callTerminalReport(client, request, "original-claim"); err != nil {
		t.Fatalf("pre-checkout failure blocked: %v", err)
	}
	job := sourceReadJob(t, db)
	if job.InputManifestState != "unknown" || job.SourceProvenanceState != "unknown" {
		t.Fatal("failed before checkout fabricated a pin")
	}
}
