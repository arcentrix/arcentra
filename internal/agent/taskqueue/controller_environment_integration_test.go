// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/internal/shared/storage"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
)

// TestControllerWorkerEnvironmentResolution 验证真实控制面分发和 Agent Shell 仅解析各层环境一次
// 快速场景使用 SQLite、Kafka 协议模拟及内存制品存储；真实服务验收复用同一断言
func TestControllerWorkerEnvironmentResolution(t *testing.T) {
	f := newControllerWorkerFixture(t, false, controllerWorkerTLS(t))
	controllerWorkerAssertEnvironment(t, f, f.store, artifactRPCReference())
}

func controllerWorkerAssertEnvironment(t *testing.T, f *controllerWorkerFixture, store storage.IStreamingStorage, reference taskpayload.ArtifactReference) {
	t.Helper()
	const base = "controller-agent-artifact"
	const jobValue = base + "-job"
	const stepValue = jobValue + "-step"
	const proofPath = "dist/environment.txt"
	const command = `test "$ROUNDTRIP_VALUE" = '${{ROUNDTRIP_VALUE}}'; mkdir -p dist; printf '%s|%s\n' "$ROUNDTRIP_VALUE" '${{ROUNDTRIP_VALUE}}' >> dist/environment.txt`
	condition := func(value string) string {
		return fmt.Sprintf("ROUNDTRIP_VALUE == '%s' && env.ROUNDTRIP_VALUE == ROUNDTRIP_VALUE && variables.ROUNDTRIP_VALUE == '%s'", value, base)
	}
	first := controllerWorkerShell("step-environment", command)
	first.Env = map[string]string{"ROUNDTRIP_VALUE": "${{ROUNDTRIP_VALUE}}-step"}
	first.When = condition(stepValue)
	sibling := controllerWorkerShell("sibling-environment", command)
	sibling.When = condition(jobValue)
	run := f.submit(&spec.Job{
		Name: "environment-resolution",
		Env:  map[string]string{"ROUNDTRIP_VALUE": "${{ROUNDTRIP_VALUE}}-job"},
		Steps: []*spec.Step{first, sibling, {
			Name: "publish-environment", Uses: "builtin:artifacts", Action: "upload", RunOnAgent: true,
			Args: spec.MapAsStruct(map[string]any{"paths": []any{proofPath}}),
		}},
	})
	completed := f.waitRun(run.RunID, model.PipelineStatusSuccess)
	if completed.CompletedJobs != 1 || completed.FailedJobs != 0 || completed.EndTime == nil {
		t.Fatalf("environment run did not complete successfully: %+v", completed)
	}
	jobs, err := f.repos.JobRun.ListByPipelineRunID(f.ctx, run.RunID)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("environment run jobs=%d: %v", len(jobs), err)
	}
	job := jobs[0]
	if job.Status != model.JobRunStatusSuccess || job.AgentID != f.conf.Agent.ID {
		t.Fatalf("environment job did not execute on the selected Agent: %+v", job)
	}
	// 检查实际 Process 发布的载荷，不能用测试自行构造的任务替代分发路径
	f.mu.Lock()
	queued := f.tasks[job.JobRunID]
	f.mu.Unlock()
	var payload taskpayload.JobRunTaskPayload
	if err := sonic.Unmarshal(queued.Payload, &payload); err != nil {
		t.Fatalf("decode dispatched environment job: %v", err)
	}
	if payload.Env["ROUNDTRIP_VALUE"] != jobValue || payload.Variables["ROUNDTRIP_VALUE"] != base || len(payload.Steps) != 3 {
		t.Fatalf("dispatched job/base environment changed: %+v", payload)
	}
	for index, value := range []string{stepValue, jobValue, jobValue} {
		step := payload.Steps[index]
		if step.Env["ROUNDTRIP_VALUE"] != value {
			t.Fatalf("step %s environment=%q want=%q", step.Name, step.Env["ROUNDTRIP_VALUE"], value)
		}
		if index < 2 && (step.Args["run"] != strings.ReplaceAll(command, "${{ROUNDTRIP_VALUE}}", value) || step.When != condition(value)) {
			t.Fatalf("step %s arguments or raw condition disagree with its environment: args=%v when=%q", step.Name, step.Args, step.When)
		}
	}
	steps, count, err := f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: run.RunID, PageSize: 100})
	if err != nil || count != 3 || len(steps) != 3 {
		t.Fatalf("environment run steps=%d: %v", count, err)
	}
	uploadID := ""
	for _, step := range steps {
		if step.Status != 4 || step.Attempt != 1 || step.AgentID != f.conf.Agent.ID || step.ExitCode == nil || *step.ExitCode != 0 {
			t.Fatalf("environment step failed, skipped, or lost execution identity: %+v", step)
		}
		if step.Name == "publish-environment" {
			uploadID = step.StepRunID
		}
	}
	want := []byte(stepValue + "|" + stepValue + "\n" + jobValue + "|" + jobValue + "\n")
	workspace := filepath.Join(f.conf.Agent.WorkspaceDir, run.RunID, job.JobRunID)
	if got := artifactTestRead(t, workspace, proofPath); got != string(want) {
		t.Fatalf("Agent environment proof=%q want=%q", got, want)
	}
	entries, count, err := f.repos.Artifact.List(f.ctx, repo.ArtifactQuery{ProjectID: f.projectID, RunID: run.RunID, JobRunID: job.JobRunID, Limit: 10})
	if err != nil || count != 1 || len(entries) != 1 || uploadID == "" {
		t.Fatalf("environment artifact metadata count=%d upload=%q: %v", count, uploadID, err)
	}
	entry := entries[0]
	digest := fmt.Sprintf("%x", sha256.Sum256(want))
	key := fmt.Sprintf("artifacts/%s/%s/%s/%s/%s", run.RunID, job.JobRunID, uploadID, digest, proofPath)
	if entry.ArtifactID != artifactutil.ID(f.projectID, run.RunID, job.JobRunID, uploadID, proofPath) || entry.StepRunID != uploadID || entry.Name != proofPath || entry.StorageID != reference.StorageID || entry.StorageFingerprint != reference.Fingerprint || entry.StoragePath != key || entry.SHA256 != digest || entry.Size != int64(len(want)) {
		t.Fatalf("environment proof lost its producing identity or exact bytes: %+v", entry)
	}
	reader, err := store.DownloadStream(f.ctx, key)
	if err != nil {
		t.Fatalf("download environment proof from supplied object store: %v", err)
	}
	got, readErr := io.ReadAll(io.LimitReader(reader, int64(len(want)+1)))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(got, want) {
		t.Fatalf("stored environment proof=%q want=%q read=%v close=%v", got, want, readErr, closeErr)
	}
	f.wait("environment terminal receipts acknowledged", func() bool {
		f.worker.reports.mu.Lock()
		defer f.worker.reports.mu.Unlock()
		return len(f.worker.reports.entries) == 0
	})
	f.assertReceipt("job", job.JobRunID, job.ClaimID, job.TerminalReportDigest, run.RunID, job.JobRunID)
	for _, step := range steps {
		f.assertReceipt("step", step.StepRunID, step.ClaimID, step.TerminalReportDigest, run.RunID, job.JobRunID)
	}
}
