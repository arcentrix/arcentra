package service

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func sourceServiceFixture(t *testing.T) (*gorm.DB, agentv1.AgentServiceClient, *agentv1.ReportJobRunStatusRequest) {
	t.Helper()
	db := terminalTestDB(t, filepath.Join(t.TempDir(), "source.db"), true)
	seedTerminalTestRun(t, db, 2)
	binding, err := sourceprovenance.NewBinding("pipeline", "run", "job", "git", "https://example/Owner/repo.git", "main")
	if err != nil {
		t.Fatal(err)
	}
	repository := repo.NewJobRunRepo(artifactServiceDatabase{db}).(*repo.JobRunRepo)
	if err := repository.CaptureSourceExpectation(context.Background(), "pipeline", "run", "job", "agent", binding); err != nil {
		t.Fatal(err)
	}
	client, _ := terminalTestClient(t, db, nil)
	if err := callTerminalReport(client, &agentv1.ReportJobRunStatusRequest{AgentId: "agent", JobRunId: "job", Status: 3}, "original-claim"); err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(context.Background()).Model(&model.StepRun{}).Where("job_run_id = ?", "job").Update("status", 4).Error; err != nil {
		t.Fatal(err)
	}
	request := terminalTestRequest("job").(*agentv1.ReportJobRunStatusRequest)
	request.SourceCheckout = &agentv1.SourceCheckout{BindingDigest: binding.Digest, ResolvedCommit: strings.Repeat("a", 40)}
	return db, client, request
}
func sourceReadJob(t *testing.T, db *gorm.DB) *model.JobRun {
	t.Helper()
	job, err := repo.NewJobRunRepo(artifactServiceDatabase{db}).GetByJobRunID(context.Background(), "job")
	if err != nil || job == nil {
		t.Fatalf("read: %+v %v", job, err)
	}
	return job
}

func TestJobSourceReceiptValidReplayAndChangedEvidence(t *testing.T) {
	db, client, request := sourceServiceFixture(t)
	for range 2 {
		if err := callTerminalReport(client, request, "original-claim"); err != nil {
			t.Fatal(err)
		}
	}
	job := sourceReadJob(t, db)
	if job.SourceResolvedCommit != request.SourceCheckout.ResolvedCommit || job.SourceProvenanceState != "reported" || job.TerminalReportDigest == "" {
		t.Fatalf("evidence not committed %+v", job)
	}
	for _, change := range []func(*agentv1.ReportJobRunStatusRequest){func(r *agentv1.ReportJobRunStatusRequest) { r.SourceCheckout.ResolvedCommit = strings.Repeat("b", 40) }, func(r *agentv1.ReportJobRunStatusRequest) { r.SourceCheckout = nil }, func(r *agentv1.ReportJobRunStatusRequest) {
		r.SourceCheckout.BindingDigest = "v1:" + strings.Repeat("f", 64)
	}} {
		changed := proto.Clone(request).(*agentv1.ReportJobRunStatusRequest)
		change(changed)
		if err := callTerminalReport(client, changed, "original-claim"); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("changed source accepted: %v", err)
		}
		after := sourceReadJob(t, db)
		if after.SourceResolvedCommit != job.SourceResolvedCommit || after.TerminalReportDigest != job.TerminalReportDigest {
			t.Fatal("conflicting report changed source")
		}
	}
}
func TestJobSourceReceiptRejectsForeignClaimsUnexpectedAndMalformedEvidence(t *testing.T) {
	for _, kind := range []string{"wrong-agent", "wrong-claim", "unclaimed", "running", "unexpected", "digest", "short", "uppercase", "64-character-valid"} {
		t.Run(kind, func(t *testing.T) {
			db, client, request := sourceServiceFixture(t)
			claim := "original-claim"
			want := codes.FailedPrecondition
			switch kind {
			case "wrong-agent":
				request.AgentId = "other"
				want = codes.PermissionDenied
			case "wrong-claim":
				claim = "foreign"
				want = codes.PermissionDenied
			case "unclaimed":
				if err := db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(map[string]any{"claim_id": "", "status": 2}).Error; err != nil {
					t.Fatal(err)
				}
				want = codes.PermissionDenied
			case "running":
				request.Status = 3
				want = codes.InvalidArgument
			case "unexpected":
				if err := db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(map[string]any{"source_expectation_captured": false, "source_binding_digest": ""}).Error; err != nil {
					t.Fatal(err)
				}
			case "digest":
				request.SourceCheckout.BindingDigest = "v1:" + strings.Repeat("f", 64)
			case "short":
				request.SourceCheckout.ResolvedCommit = "abcd123"
				want = codes.InvalidArgument
			case "uppercase":
				request.SourceCheckout.ResolvedCommit = strings.Repeat("A", 40)
				want = codes.InvalidArgument
			case "64-character-valid":
				request.SourceCheckout.ResolvedCommit = strings.Repeat("a", 64)
				want = codes.OK
			}
			err := callTerminalReport(client, request, claim)
			if status.Code(err) != want {
				t.Fatalf("want %s got %v", want, err)
			}
			job := sourceReadJob(t, db)
			if want != codes.OK && (job.SourceResolvedCommit != "" || job.SourceProvenanceState != "unknown" || job.TerminalReportDigest != "") {
				t.Fatal("rejected evidence persisted")
			}
		})
	}
}
func TestJobSourceOldAgentAndCloneFailureStayUnknown(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			db, client, request := sourceServiceFixture(t)
			request.SourceCheckout = nil
			if legacy {
				if err := db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(map[string]any{"source_expectation_captured": false, "source_type": "", "source_repository": "", "source_ref": "", "source_binding_digest": ""}).Error; err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err := callTerminalReport(client, request, "original-claim"); err != nil {
					t.Fatal(err)
				}
			}
			job := sourceReadJob(t, db)
			if job.SourceResolvedCommit != "" || job.SourceProvenanceState != "unknown" || job.TerminalReportDigest == "" {
				t.Fatal("old receipt lost compatibility or inferred evidence")
			}
		})
	}
}
func TestJobSourceAtomicWriteFailureAndCancellationReceipt(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprint(cancel), func(t *testing.T) {
			db, client, request := sourceServiceFixture(t)
			if cancel {
				r := repo.NewJobRunRepo(artifactServiceDatabase{db}).(*repo.JobRunRepo)
				if err := r.CancelAgentJobIfCurrent(context.Background(), "job", 3, map[string]any{"status": 6, "error_message": "authoritative cancel"}); err != nil {
					t.Fatal(err)
				}
				request.Status = 6
			}
			name := "source-receipt-rollback"
			if err := db.Callback().Update().After("gorm:update").Register(name, func(tx *gorm.DB) {
				if tx.Statement.Table == "job_run" {
					tx.AddError(fmt.Errorf("injected failure"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err := callTerminalReport(client, request, "original-claim"); status.Code(err) != codes.Internal {
				t.Fatalf("failed commit ack: %v", err)
			}
			job := sourceReadJob(t, db)
			if job.TerminalReportDigest != "" || job.SourceResolvedCommit != "" || job.SourceProvenanceState != "unknown" {
				t.Fatal("partial source receipt")
			}
			if err := db.Callback().Update().Remove(name); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := callTerminalReport(client, request, "original-claim"); err != nil {
					t.Fatal(err)
				}
			}
			job = sourceReadJob(t, db)
			if job.SourceResolvedCommit != request.SourceCheckout.ResolvedCommit || job.SourceProvenanceState != "reported" {
				t.Fatal("missing retry evidence")
			}
			if cancel && (job.Status != 6 || job.ErrorMessage != "authoritative cancel") {
				t.Fatal("cancel result overwritten")
			}
		})
	}
}
func TestJobSourceReplayRejectsInconsistentStoredEvidence(t *testing.T) {
	for _, field := range []string{"source_resolved_commit", "source_provenance_state"} {
		t.Run(field, func(t *testing.T) {
			db, client, request := sourceServiceFixture(t)
			if err := callTerminalReport(client, request, "original-claim"); err != nil {
				t.Fatal(err)
			}
			value := strings.Repeat("b", 40)
			if field == "source_provenance_state" {
				value = "unknown"
			}
			if err := db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update(field, value).Error; err != nil {
				t.Fatal(err)
			}
			if err := callTerminalReport(client, request, "original-claim"); status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("inconsistent evidence acknowledged %v", err)
			}
		})
	}
}

func TestJobSourceAndArtifactsCommitInOneTransaction(t *testing.T) {
	db, client, request := sourceServiceFixture(t)
	storageID, fingerprint, fileSHA := "store", strings.Repeat("c", 64), strings.Repeat("d", 64)
	if err := db.WithContext(context.Background()).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(map[string]any{"artifact_storage_id": storageID, "artifact_storage_fingerprint": fingerprint, "artifact_storage_type": "s3"}).Error; err != nil {
		t.Fatal(err)
	}
	key := "artifacts/run/job/step/" + fileSHA + "/output.txt"
	request.ArtifactStorageId = storageID
	request.ArtifactStorageFingerprint = fingerprint
	request.ArtifactUris = map[string]string{"output.txt": key}
	request.Artifacts = []*agentv1.ArtifactResult{{StepRunId: "step", Name: "output.txt", StorageKey: key, Sha256: fileSHA, Size: 7, MediaType: "application/octet-stream"}}
	name := "source-artifact-terminal-failure"
	if err := db.Callback().Update().After("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "job_run" {
			tx.AddError(fmt.Errorf("injected final job write failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := callTerminalReport(client, request, "original-claim"); status.Code(err) != codes.Internal {
		t.Fatalf("failed transaction accepted %v", err)
	}
	var count int64
	if err := db.WithContext(context.Background()).Model(&model.ProjectArtifact{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	job := sourceReadJob(t, db)
	if count != 0 || job.SourceResolvedCommit != "" || job.SourceProvenanceState != "unknown" || job.TerminalReportDigest != "" || job.Status != 3 {
		t.Fatalf("partial artifact+source result: %d %+v", count, job)
	}
	if err := db.Callback().Update().Remove(name); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := callTerminalReport(client, request, "original-claim"); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.WithContext(context.Background()).Model(&model.ProjectArtifact{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	job = sourceReadJob(t, db)
	if count != 1 || job.SourceResolvedCommit != request.SourceCheckout.ResolvedCommit || job.SourceProvenanceState != "reported" || job.TerminalReportDigest == "" || job.Status != 5 {
		t.Fatalf("missing atomic result: %d %+v", count, job)
	}
}
