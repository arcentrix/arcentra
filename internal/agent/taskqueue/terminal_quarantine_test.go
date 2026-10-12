// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/bytedance/sonic"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type quarantineTestClient struct {
	agentv1.AgentServiceClient
	reject  map[string]bool
	failure error
	alter   func(*agentidentity.TerminalConflictInfo)
	calls   []string
}

func (c *quarantineTestClient) result(ctx context.Context, kind, runID string, request proto.Message) error {
	c.calls = append(c.calls, kind+"/"+runID)
	if c.failure != nil {
		return c.failure
	}
	if !c.reject[runID] {
		return nil
	}
	md, _ := metadata.FromOutgoingContext(ctx)
	claims := md.Get(executionClaimMetadataKey)
	claim := ""
	if len(claims) > 0 {
		claim = claims[0]
	}
	info := quarantineTestConflict(request, kind, runID, claim)
	if c.alter != nil {
		c.alter(&info)
	}
	return agentidentity.TerminalConflictError(info)
}
func (c *quarantineTestClient) ReportStepRunStatus(ctx context.Context, request *agentv1.ReportStepRunStatusRequest, _ ...grpc.CallOption) (*agentv1.ReportStepRunStatusResponse, error) {
	if err := c.result(ctx, "step", request.StepRunId, request); err != nil {
		return nil, err
	}
	return &agentv1.ReportStepRunStatusResponse{Success: true}, nil
}
func (c *quarantineTestClient) ReportJobRunStatus(ctx context.Context, request *agentv1.ReportJobRunStatusRequest, _ ...grpc.CallOption) (*agentv1.ReportJobRunStatusResponse, error) {
	if err := c.result(ctx, "job", request.JobRunId, request); err != nil {
		return nil, err
	}
	return &agentv1.ReportJobRunStatusResponse{Success: true}, nil
}
func quarantineTestConflict(request proto.Message, kind, runID, claim string) agentidentity.TerminalConflictInfo {
	digest, _ := agentidentity.TerminalPayloadDigest(request)
	jobRunID := "job"
	if kind == "job" {
		jobRunID = runID
	}
	return agentidentity.TerminalConflictInfo{Kind: kind, AgentID: "agent", RunID: runID, ClaimID: claim, ClaimSource: "execution_claim", PayloadDigest: digest, PipelineID: "pipeline", PipelineRunID: "pipeline-run", JobRunID: jobRunID, Attempt: 1, AuthoritativeStatus: 6}
}
func trustQuarantineTestClient(outbox *terminalOutbox) {
	// 此钩子仅测试存储层；生产连接与集成测试通过实际证书验证提供可信状态
	outbox.controllerAuthenticated = func(*agentgrpc.ClientWrapper) bool { return true }
}
func quarantineTestPut(t *testing.T, outbox *terminalOutbox, kind, runID string) terminalRecord {
	t.Helper()
	if err := outbox.put(context.Background(), terminalTestRecord(t, kind, runID)); err != nil {
		t.Fatal(err)
	}
	return outbox.entries[terminalRecordName(kind, runID)].record
}
func quarantineTestReject(t *testing.T, outbox *terminalOutbox, kind, runID string) *quarantineTestClient {
	t.Helper()
	trustQuarantineTestClient(outbox)
	client := &quarantineTestClient{reject: map[string]bool{runID: true}}
	found, err := outbox.deliver(context.Background(), &agentgrpc.ClientWrapper{AgentClient: client}, kind, runID)
	if !found || !terminalQuarantined(err) {
		t.Fatalf("expected durable quarantine: %v %v", found, err)
	}
	return client
}

func TestTerminalQuarantinePreservesReceiptAndRestartDedup(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "reports")
	outbox := openTestTerminalOutbox(t, directory)
	original := quarantineTestPut(t, outbox, "step", "conflict-step")
	client := quarantineTestReject(t, outbox, "step", "conflict-step")
	saved := outbox.entries[terminalRecordName("step", "conflict-step")].record
	if saved.Checksum != original.Checksum || !bytes.Equal(saved.Payload, original.Payload) || saved.ClaimID != original.ClaimID || saved.Binding != original.Binding {
		t.Fatal("quarantine changed original immutable receipt")
	}
	if saved.Quarantine == nil || saved.Quarantine.Conflict.AuthoritativeStatus != 6 {
		t.Fatal("structured conflict not saved")
	}
	size := outbox.bytes
	if err := outbox.put(ctx, original); err != nil {
		t.Fatal(err)
	}
	if outbox.bytes != size {
		t.Fatal("same receipt duplicate increased disk accounting")
	}
	if err := outbox.close(); err != nil {
		t.Fatal(err)
	}
	outbox = openTestTerminalOutbox(t, directory)
	defer outbox.close()
	wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
	if err := outbox.replay(ctx, wrapper); err != nil {
		t.Fatal(err)
	}
	conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "agent"}, Grpc: config.GrpcConfig{ServerAddr: "controller:9090"}}
	found, err := replayTerminalForTask(withTerminalOutbox(ctx, outbox), wrapper, conf, "step", "conflict-step")
	if !found || !terminalQuarantined(err) || acknowledgeCompleted(err) != nil {
		t.Fatalf("quarantined task redelivery not handled safely: %v %v", found, err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("quarantined receipt re-sent after restart: %v", client.calls)
	}
	changed := original
	changed.ClaimID = "another-claim"
	if err := outbox.put(ctx, changed); err == nil {
		t.Fatal("quarantined identity replaced by another claim")
	}
	quarantineTestPut(t, outbox, "step", "unrelated-step")
	if err := outbox.replay(ctx, wrapper); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 2 || client.calls[1] != "step/unrelated-step" || !outbox.has("step", "conflict-step") || outbox.has("step", "unrelated-step") {
		t.Fatalf("unrelated replay blocked or quarantine lost: %v", client.calls)
	}
}

func TestTerminalQuarantineDependencyIsExplicitLocalDisposition(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "reports")
	outbox := openTestTerminalOutbox(t, directory)
	originalStep := quarantineTestPut(t, outbox, "step", "step")
	job := terminalTestRecord(t, "job", "job")
	job.Dependencies = []string{"step"}
	if err := outbox.put(ctx, job); err != nil {
		t.Fatal(err)
	}
	originalJob := outbox.entries[terminalRecordName("job", "job")].record
	trustQuarantineTestClient(outbox)
	client := &quarantineTestClient{reject: map[string]bool{"step": true}}
	wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
	found, err := outbox.deliver(ctx, wrapper, "job", "job")
	if !found || !terminalQuarantined(err) {
		t.Fatalf("dependency quarantine not propagated: %v", err)
	}
	q := outbox.entries[terminalRecordName("job", "job")].record.Quarantine
	if q == nil || q.Disposition != terminalDependencyDisposition || q.Conflict != nil || q.Dependency.RunID != "step" || q.Dependency.ReceiptChecksum != originalStep.Checksum {
		t.Fatalf("parent fabricated controller rejection: %+v", q)
	}
	if len(client.calls) != 1 || client.calls[0] != "step/step" {
		t.Fatalf("unacknowledged step allowed parent SUCCESS: %v", client.calls)
	}
	if outbox.entries[terminalRecordName("job", "job")].record.Checksum != originalJob.Checksum {
		t.Fatal("dependency changed parent receipt")
	}
	if err := outbox.close(); err != nil {
		t.Fatal(err)
	}
	outbox = openTestTerminalOutbox(t, directory)
	defer outbox.close()
	if err := outbox.replay(ctx, wrapper); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 1 {
		t.Fatal("quarantined dependency replayed")
	}
}

func TestTerminalQuarantineRequiresAuthenticatedExactConflict(t *testing.T) {
	cases := []struct {
		name    string
		trusted bool
		failure error
		alter   func(*agentidentity.TerminalConflictInfo)
	}{
		{name: "plaintext_typed"},
		{name: "unavailable", trusted: true, failure: status.Error(codes.Unavailable, "private-network-secret")},
		{name: "authentication", trusted: true, failure: status.Error(codes.Unauthenticated, "private-auth-secret")},
		{name: "permission", trusted: true, failure: status.Error(codes.PermissionDenied, "private-auth-secret")},
		{name: "database", trusted: true, failure: status.Error(codes.Internal, "private-db-secret")},
		{name: "generic_precondition", trusted: true, failure: status.Error(codes.FailedPrecondition, "private-error-secret")},
		{name: "wrong_claim", trusted: true, alter: func(i *agentidentity.TerminalConflictInfo) { i.ClaimID = "another-claim" }},
		{name: "wrong_payload", trusted: true, alter: func(i *agentidentity.TerminalConflictInfo) { i.PayloadDigest = strings.Repeat("a", 64) }},
		{name: "wrong_agent", trusted: true, alter: func(i *agentidentity.TerminalConflictInfo) { i.AgentID = "another-agent" }},
		{name: "wrong_run", trusted: true, alter: func(i *agentidentity.TerminalConflictInfo) { i.RunID = "another-run" }},
		{name: "nonterminal", trusted: true, alter: func(i *agentidentity.TerminalConflictInfo) { i.AuthoritativeStatus = 3 }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "reports")
			outbox := openTestTerminalOutbox(t, directory)
			defer outbox.close()
			original := quarantineTestPut(t, outbox, "step", "step")
			if test.trusted {
				trustQuarantineTestClient(outbox)
			}
			client := &quarantineTestClient{reject: map[string]bool{"step": true}, failure: test.failure, alter: test.alter}
			_, err := outbox.deliver(context.Background(), &agentgrpc.ClientWrapper{AgentClient: client}, "step", "step")
			if err == nil || terminalQuarantined(err) {
				t.Fatalf("untrusted or invalid conflict quarantined: %v", err)
			}
			saved := outbox.entries[terminalRecordName("step", "step")].record
			if saved.Quarantine != nil || saved.Checksum != original.Checksum {
				t.Fatal("invalid rejection changed receipt")
			}
			raw, err := os.ReadFile(filepath.Join(directory, terminalRecordName("step", "step")))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "private-") {
				t.Fatal("remote error message persisted")
			}
			if err := outbox.replay(context.Background(), &agentgrpc.ClientWrapper{AgentClient: client}); err == nil {
				t.Fatal("ordinary failure failed open for unrelated work")
			}
		})
	}
}

func TestTerminalQuarantinePersistenceFailuresNeverHandleTask(t *testing.T) {
	for _, mode := range []string{"quota", "file_sync", "directory_sync"} {
		t.Run(mode, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "reports")
			outbox := openTestTerminalOutbox(t, directory)
			original := quarantineTestPut(t, outbox, "step", "step")
			trustQuarantineTestClient(outbox)
			if mode == "quota" {
				outbox.maxBytes = outbox.bytes
			} else {
				outbox.syncFile = func(file *os.File) error {
					info, err := file.Stat()
					if err != nil {
						return err
					}
					if info.IsDir() == (mode == "directory_sync") {
						return errors.New("private-fsync-fault")
					}
					return file.Sync()
				}
			}
			client := &quarantineTestClient{reject: map[string]bool{"step": true}}
			_, err := outbox.deliver(context.Background(), &agentgrpc.ClientWrapper{AgentClient: client}, "step", "step")
			if err == nil || terminalQuarantined(err) || !terminalPersistenceFailed(err) || acknowledgeCompleted(err) == nil {
				t.Fatalf("uncertain marker allowed task ACK: %v", err)
			}
			if strings.Contains(err.Error(), "private-") {
				t.Fatal("failure leaked diagnostic")
			}
			if outbox.entries[terminalRecordName("step", "step")].record.Quarantine != nil {
				t.Fatal("failed marker changed in-memory state")
			}
			if mode == "directory_sync" && outbox.fatalErr == nil {
				t.Fatal("uncertain rename did not halt instance")
			}
			outbox.close()
			recovered := openTestTerminalOutbox(t, directory)
			defer recovered.close()
			record := recovered.entries[terminalRecordName("step", "step")].record
			if record.Checksum != original.Checksum || !bytes.Equal(record.Payload, original.Payload) {
				t.Fatal("failed marker lost original receipt")
			}
			if (record.Quarantine != nil) != (mode == "directory_sync") {
				t.Fatal("restart did not recover atomic old-or-new state")
			}
			if record.Quarantine != nil {
				if err := recovered.replay(context.Background(), &agentgrpc.ClientWrapper{AgentClient: client}); err != nil {
					t.Fatal(err)
				}
				if len(client.calls) != 1 {
					t.Fatal("committed quarantine re-sent after restart")
				}
			}
		})
	}
}

func TestTerminalQuarantineReviewRetainsPermanentDedupAndAudit(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "reports")
	outbox := openTestTerminalOutbox(t, directory)
	original := quarantineTestPut(t, outbox, "step", "step")
	client := quarantineTestReject(t, outbox, "step", "step")
	if operator, err := OpenTerminalQuarantine(ctx, directory, "agent", "controller:9090"); err == nil {
		operator.Close()
		t.Fatal("operator bypassed Agent exclusive lock")
	}
	outbox.close()
	operator, err := OpenTerminalQuarantine(ctx, directory, "agent", "controller:9090")
	if err != nil {
		t.Fatal(err)
	}
	before, err := operator.Inspect(ctx, "step", "step")
	if err != nil {
		t.Fatal(err)
	}
	reviewed, err := operator.MarkReviewed(ctx, "step", "step", "verified_external_effects")
	if err != nil {
		t.Fatal(err)
	}
	if len(reviewed.Reviews) != 1 || reviewed.ReceiptChecksum != before.ReceiptChecksum || reviewed.QuarantineChecksum != before.QuarantineChecksum {
		t.Fatal("review changed evidence")
	}
	repeat, err := operator.MarkReviewed(ctx, "step", "step", "verified_external_effects")
	if err != nil || repeat.Reviews[0].Checksum != reviewed.Reviews[0].Checksum {
		t.Fatalf("review not idempotent: %v", err)
	}
	for _, value := range []string{"investigated", "password=sensitive"} {
		if _, err := operator.MarkReviewed(ctx, "step", "step", value); err == nil {
			t.Fatal("immutable review replaced")
		}
	}
	summaries, err := operator.List(ctx)
	if err != nil || len(summaries) != 1 {
		t.Fatalf("reviewed quarantine hidden: %v", err)
	}
	summaryJSON, _ := sonic.Marshal(summaries)
	if bytes.Contains(summaryJSON, []byte(`"payload":`)) || bytes.Contains(summaryJSON, []byte(`"errorMessage":`)) {
		t.Fatal("operator exposed raw payload")
	}
	operator.Close()
	outbox = openTestTerminalOutbox(t, directory)
	defer outbox.close()
	saved := outbox.entries[terminalRecordName("step", "step")].record
	if saved.Checksum != original.Checksum || len(saved.Quarantine.Reviews) != 1 || !bytes.Equal(saved.Payload, original.Payload) {
		t.Fatal("review failed restart preservation")
	}
	if err := outbox.replay(ctx, &agentgrpc.ClientWrapper{AgentClient: client}); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 1 || !outbox.has("step", "step") {
		t.Fatal("reviewed identity reactivated or deleted")
	}
}

func TestTerminalQuarantineMalformedEvidenceFailsClosed(t *testing.T) {
	for _, mode := range []string{"checksum", "receipt", "time", "conflict", "null", "unknown", "review", "missing_dependency"} {
		t.Run(mode, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "reports")
			outbox := openTestTerminalOutbox(t, directory)
			quarantineTestPut(t, outbox, "step", "step")
			quarantineTestReject(t, outbox, "step", "step")
			kind, runID := "step", "step"
			if mode == "missing_dependency" {
				job := terminalTestRecord(t, "job", "job")
				job.Dependencies = []string{"step"}
				if err := outbox.put(context.Background(), job); err != nil {
					t.Fatal(err)
				}
				_, err := outbox.deliver(context.Background(), nil, "job", "job")
				if !terminalQuarantined(err) {
					t.Fatal(err)
				}
				kind, runID = "job", "job"
			}
			record := outbox.entries[terminalRecordName(kind, runID)].record
			outbox.close()
			switch mode {
			case "checksum":
				record.Quarantine.Checksum = strings.Repeat("0", 64)
			case "receipt":
				record.Quarantine.ReceiptChecksum = strings.Repeat("0", 64)
			case "time":
				record.Quarantine.QuarantinedAt = "not-a-time"
			case "conflict":
				record.Quarantine.Conflict.ClaimID = "other"
			case "review":
				record.Quarantine.Reviews = []TerminalQuarantineReview{{Disposition: "investigated"}}
			case "missing_dependency":
				if err := os.Remove(filepath.Join(directory, terminalRecordName("step", "step"))); err != nil {
					t.Fatal(err)
				}
			}
			raw, _ := sonic.Marshal(record)
			if mode == "null" {
				raw = bytes.Replace(raw, []byte(`"quarantine":{`), []byte(`"quarantine":null,"unrecognized":{`), 1)
			}
			if mode == "unknown" {
				raw = append([]byte(`{"pending":true,`), raw[1:]...)
			}
			if err := os.WriteFile(filepath.Join(directory, terminalRecordName(kind, runID)), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if reopened, err := newTerminalOutbox(context.Background(), directory, "agent", "controller:9090"); err == nil {
				reopened.close()
				t.Fatal("corrupt quarantine accepted")
			}
		})
	}
}

func TestTerminalQuarantineOperatorOpenDoesNotMutate(t *testing.T) {
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "missing")
	if operator, err := OpenTerminalQuarantine(ctx, missing, "agent", "controller:9090"); err == nil {
		operator.Close()
		t.Fatal("operator initialized missing directory")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("operator created missing directory")
	}
	for _, mode := range []string{"identity", "lock", "pending", "directory_mode", "record_mode"} {
		t.Run(mode, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "reports")
			outbox := openTestTerminalOutbox(t, directory)
			quarantineTestPut(t, outbox, "step", "step")
			quarantineTestReject(t, outbox, "step", "step")
			outbox.close()
			switch mode {
			case "identity":
				if err := os.Remove(filepath.Join(directory, "identity")); err != nil {
					t.Fatal(err)
				}
			case "lock":
				if err := os.Remove(filepath.Join(directory, ".lock")); err != nil {
					t.Fatal(err)
				}
			case "pending":
				if err := os.WriteFile(filepath.Join(directory, ".pending"), []byte("incomplete"), 0600); err != nil {
					t.Fatal(err)
				}
			case "directory_mode":
				if err := os.Chmod(directory, 0755); err != nil {
					t.Fatal(err)
				}
			case "record_mode":
				if err := os.Chmod(filepath.Join(directory, terminalRecordName("step", "step")), 0644); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadDir(directory)
			directoryInfo, _ := os.Stat(directory)
			if operator, err := OpenTerminalQuarantine(ctx, directory, "agent", "controller:9090"); err == nil {
				operator.Close()
				t.Fatal("operator silently repaired unsafe state")
			}
			after, _ := os.ReadDir(directory)
			afterInfo, _ := os.Stat(directory)
			if len(before) != len(after) || directoryInfo.Mode() != afterInfo.Mode() {
				t.Fatal("operator mutated directory")
			}
			for i, item := range before {
				if item.Name() != after[i].Name() {
					t.Fatal("operator created or deleted entry")
				}
			}
		})
	}
}

func TestTerminalQuarantineSuccessfulInspectionDoesNotMutate(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "reports")
	outbox := openTestTerminalOutbox(t, directory)
	quarantineTestPut(t, outbox, "step", "step")
	quarantineTestReject(t, outbox, "step", "step")
	outbox.close()
	type snapshot struct {
		content []byte
		info    os.FileInfo
	}
	capture := func() map[string]snapshot {
		result := make(map[string]snapshot)
		names, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		directoryInfo, err := os.Stat(directory)
		if err != nil {
			t.Fatal(err)
		}
		result["."] = snapshot{info: directoryInfo}
		for _, name := range names {
			path := filepath.Join(directory, name.Name())
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			result[name.Name()] = snapshot{content: raw, info: info}
		}
		return result
	}
	before := capture()
	operator, err := OpenTerminalQuarantine(ctx, directory, "agent", "controller:9090")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.List(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Inspect(ctx, "step", "step"); err != nil {
		t.Fatal(err)
	}
	operator.Close()
	after := capture()
	if len(before) != len(after) {
		t.Fatal("inspection changed directory entries")
	}
	for name, want := range before {
		got, exists := after[name]
		if !exists || !bytes.Equal(want.content, got.content) || want.info.Mode() != got.info.Mode() || !want.info.ModTime().Equal(got.info.ModTime()) || !os.SameFile(want.info, got.info) {
			t.Fatalf("inspection modified %s", name)
		}
	}
}

func TestTerminalQuarantineReviewPersistenceFailureRecoversAuditOnly(t *testing.T) {
	for _, mode := range []string{"quota", "file_sync", "directory_sync"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			directory := filepath.Join(t.TempDir(), "reports")
			outbox := openTestTerminalOutbox(t, directory)
			original := quarantineTestPut(t, outbox, "step", "step")
			quarantineTestReject(t, outbox, "step", "step")
			outbox.close()
			operator, err := OpenTerminalQuarantine(ctx, directory, "agent", "controller:9090")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "quota" {
				operator.outbox.maxBytes = operator.outbox.bytes
			} else {
				operator.outbox.syncFile = func(file *os.File) error {
					info, err := file.Stat()
					if err != nil {
						return err
					}
					if info.IsDir() == (mode == "directory_sync") {
						return errors.New("private-review-fsync-secret")
					}
					return file.Sync()
				}
			}
			if _, err := operator.MarkReviewed(ctx, "step", "step", "investigated"); err == nil {
				t.Fatal("failed audit persistence reported success")
			} else if strings.Contains(err.Error(), "private-") {
				t.Fatal("audit failure leaked error")
			}
			operator.Close()
			operator, err = OpenTerminalQuarantine(ctx, directory, "agent", "controller:9090")
			if err != nil {
				t.Fatal(err)
			}
			defer operator.Close()
			summary, err := operator.Inspect(ctx, "step", "step")
			if err != nil {
				t.Fatal(err)
			}
			if summary.ReceiptChecksum != original.Checksum || (len(summary.Reviews) == 1) != (mode == "directory_sync") {
				t.Fatal("review crash did not preserve atomic original-or-reviewed evidence")
			}
			if len(operator.outbox.entries) != 1 || operator.outbox.entries[terminalRecordName("step", "step")].record.Quarantine == nil {
				t.Fatal("review crash removed execution dedup")
			}
		})
	}
}

func TestTerminalQuarantinePreservesRedactedReceipt(t *testing.T) {
	conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "agent"}, Grpc: config.GrpcConfig{ServerAddr: "controller:9090", Token: "declared-private-token"}}
	directory := filepath.Join(t.TempDir(), "reports")
	outbox := openTestTerminalOutbox(t, directory)
	defer outbox.close()
	trustQuarantineTestClient(outbox)
	ctx := withTerminalSecrets(withTerminalOutbox(context.Background(), outbox), conf, nil, nil)
	client := &quarantineTestClient{reject: map[string]bool{"redacted-step": true}}
	err := reportStepRunStatus(ctx, &agentgrpc.ClientWrapper{AgentClient: client}, conf, "redacted-step", steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED, 3, "declared-private-token https://user:secret-password@example.com/?token=secret-query", time.Unix(123, 0), 456, nil)
	if !terminalQuarantined(err) {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(directory, terminalRecordName("step", "redacted-step")))
	if err != nil {
		t.Fatal(err)
	}
	var record terminalRecord
	if err := sonic.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"declared-private-token", "secret-password", "secret-query"} {
		if bytes.Contains(raw, []byte(secret)) || bytes.Contains(record.Payload, []byte(secret)) {
			t.Fatal("quarantine persisted unredacted secret")
		}
	}
	var request agentv1.ReportStepRunStatusRequest
	if err := proto.Unmarshal(record.Payload, &request); err != nil {
		t.Fatal(err)
	}
	if request.ExitCode != 3 || request.StartTime != 123 || request.EndTime != 456 || !strings.Contains(request.ErrorMessage, "[REDACTED]") {
		t.Fatal("quarantine altered outcome or missed redaction")
	}
	digest, err := agentidentity.TerminalPayloadDigest(&request)
	if err != nil || record.Quarantine.Conflict.PayloadDigest != digest {
		t.Fatal("conflict not bound to exact redacted request")
	}
}
