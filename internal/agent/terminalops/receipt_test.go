//go:build unix

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package terminalops

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	"github.com/arcentrix/arcentra/internal/agent/taskqueue"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/bytedance/sonic"
	"google.golang.org/protobuf/proto"
)

// 固定磁盘格式夹具独立于生产写入器，真实 CLI 重开时必须通过完整校验
// 字段顺序属于现有规范 JSON 格式，升级磁盘格式时也应显式更新此兼容测试
type receiptFixture struct {
	Version      int                `json:"version"`
	Binding      string             `json:"binding"`
	Kind         string             `json:"kind"`
	RunID        string             `json:"runId"`
	ClaimID      string             `json:"claimId"`
	Dependencies []string           `json:"dependencies,omitempty"`
	Payload      []byte             `json:"payload"`
	Checksum     string             `json:"checksum"`
	Quarantine   *quarantineFixture `json:"quarantine,omitempty"`
}

type quarantineFixture struct {
	Version         int                                  `json:"version"`
	Disposition     string                               `json:"disposition"`
	ReceiptChecksum string                               `json:"receiptChecksum"`
	QuarantinedAt   string                               `json:"quarantinedAt"`
	Conflict        *agentidentity.TerminalConflictInfo  `json:"conflict,omitempty"`
	Dependency      *struct{}                            `json:"dependency,omitempty"`
	Checksum        string                               `json:"checksum"`
	Reviews         []taskqueue.TerminalQuarantineReview `json:"reviews,omitempty"`
}

func fixtureJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := sonic.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func fixtureChecksum(t *testing.T, value any) string {
	t.Helper()
	digest := sha256.Sum256(fixtureJSON(t, value))
	return hex.EncodeToString(digest[:])
}

func createQuarantineFixture(t *testing.T, dir string) (string, receiptFixture) {
	t.Helper()
	request := &agentv1.ReportStepRunStatusRequest{
		AgentId: "agent-1", StepRunId: "step-run-1", Status: steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED,
		ExitCode: 42, ErrorMessage: "private-terminal-error-text", StartTime: 1700000000, EndTime: 1700000042,
		Metrics: map[string]string{"private-metric-name": "private-metric-value"},
	}
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	binding := sha256.Sum256([]byte("agent-1\x00controller:9090"))
	receipt := receiptFixture{
		Version: 1, Binding: hex.EncodeToString(binding[:]), Kind: "step", RunID: "step-run-1", ClaimID: "claim-1", Payload: payload,
	}
	receipt.Checksum = fixtureChecksum(t, receipt)
	digest, err := agentidentity.TerminalPayloadDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	quarantine := &quarantineFixture{
		Version: 1, Disposition: "terminal_conflict", ReceiptChecksum: receipt.Checksum, QuarantinedAt: "2026-01-02T03:04:05Z",
		Conflict: &agentidentity.TerminalConflictInfo{
			Kind: "step", AgentID: "agent-1", RunID: "step-run-1", ClaimID: "claim-1", ClaimSource: "execution_claim", PayloadDigest: digest,
			PipelineID: "pipeline-1", PipelineRunID: "pipeline-run-1", JobRunID: "job-run-1", Attempt: 1, StepIndex: 0,
			AuthoritativeStatus: int(steprunv1.StepRunStatus_STEP_RUN_STATUS_CANCELLED),
		},
	}
	quarantine.Checksum = fixtureChecksum(t, quarantine)
	receipt.Quarantine = quarantine
	name := sha256.Sum256([]byte("step\x00step-run-1"))
	path := filepath.Join(dir, hex.EncodeToString(name[:])+".record")
	if err := os.WriteFile(path, fixtureJSON(t, receipt), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, receipt
}

func TestRealQuarantineSummaryAndReviewRetainOriginalReceipt(t *testing.T) {
	dir := existingEmptyOutbox(t)
	path, original := createQuarantineFixture(t, dir)
	before := snapshotDirectory(t, dir)
	for _, action := range []string{"list", "inspect"} {
		args := actualArgs(action, dir)
		if action == "inspect" {
			args = append(args, "--kind", "step", "--run-id", "step-run-1")
		}
		code, out, errOut := runActual(t, args)
		if code != 0 || errOut != "" || !strings.Contains(out, `"runId":"step-run-1"`) {
			t.Fatalf("real %s failed: code=%d stdout=%q stderr=%q", action, code, out, errOut)
		}
		for _, forbidden := range []string{
			"private-terminal-error-text", "private-metric-name", "private-metric-value", `"payload":`, `"errorMessage":`,
		} {
			if strings.Contains(out, forbidden) {
				t.Fatalf("safe %s output contains excluded payload field", action)
			}
		}
		if snapshotDirectory(t, dir) != before {
			t.Fatalf("%s changed journal bytes or permissions", action)
		}
	}
	reviewArgs := actualArgs("mark-reviewed", dir, "--kind", "step", "--run-id", "step-run-1", "--disposition", "investigated")
	code, out, _ := runActual(t, reviewArgs)
	if code != 2 || out != "" || snapshotDirectory(t, dir) != before {
		t.Fatal("review without explicit confirmation changed journal")
	}
	reviewArgs = append(reviewArgs, "--confirm-reviewed")
	code, out, errOut := runActual(t, reviewArgs)
	if code != 0 || errOut != "" || !strings.Contains(out, `"disposition":"investigated"`) {
		t.Fatalf("review failed: code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var reviewed receiptFixture
	if err := sonic.Unmarshal(data, &reviewed); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original.Payload, reviewed.Payload) || original.Checksum != reviewed.Checksum ||
		original.ClaimID != reviewed.ClaimID || original.Quarantine.Checksum != reviewed.Quarantine.Checksum ||
		len(reviewed.Quarantine.Reviews) != 1 {
		t.Fatal("review changed original receipt/evidence or did not retain its tombstone")
	}
	afterReview := snapshotDirectory(t, dir)
	code, repeated, errOut := runActual(t, reviewArgs)
	if code != 0 || repeated != out || errOut != "" || snapshotDirectory(t, dir) != afterReview {
		t.Fatal("same repeated review was not byte-stable and idempotent")
	}
	changed := append([]string(nil), reviewArgs...)
	for i := range changed {
		if changed[i] == "investigated" {
			changed[i] = "verified_external_effects"
		}
	}
	code, out, _ = runActual(t, changed)
	if code != 1 || out != "" || snapshotDirectory(t, dir) != afterReview {
		t.Fatal("different review disposition did not preserve immutable review")
	}
	code, out, _ = runActual(t, actualArgs("inspect", dir, "--kind", "step", "--run-id", "missing-run"))
	if code != 1 || out != "" || snapshotDirectory(t, dir) != afterReview {
		t.Fatal("missing selector was accepted or changed the journal")
	}
}
