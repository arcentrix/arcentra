// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/process"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/nova"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"go.uber.org/zap"
)

const controllerCheckoutCredential = "checkout-fixture-token-must-not-persist"

type controllerCheckoutQueued struct {
	job     model.JobRun
	payload taskpayload.JobRunTaskPayload
}

// controllerCheckoutQueue 在真实 Kafka 发布前只读检查控制面保存的源绑定
// JobRun 和 StepRun 仍全部由 Process.Submit 创建，不预置执行记录
type controllerCheckoutQueue struct {
	nova.TaskQueue
	fixture *controllerWorkerFixture
	queued  chan controllerCheckoutQueued
}

func (q *controllerCheckoutQueue) Enqueue(task *nova.Task, options ...nova.Option) (*nova.Result, error) {
	if task != nil && task.Type == taskpayload.TaskTypeJobRun {
		var payload taskpayload.JobRunTaskPayload
		if err := sonic.Unmarshal(task.Payload, &payload); err != nil {
			return nil, err
		}
		job, err := q.fixture.repos.JobRun.GetByJobRunID(q.fixture.ctx, payload.JobRunID)
		if err != nil {
			return nil, err
		}
		if job == nil {
			return nil, fmt.Errorf("job was not persisted before Kafka publication")
		}
		select {
		case q.queued <- controllerCheckoutQueued{job: *job, payload: payload}:
		case <-q.fixture.ctx.Done():
			return nil, q.fixture.ctx.Err()
		}
	}
	return q.TaskQueue.Enqueue(task, options...)
}

func newControllerCheckoutFixture(t *testing.T, loseACK bool) (*controllerWorkerFixture, <-chan controllerCheckoutQueued) {
	t.Helper()
	f := newControllerWorkerFixture(t, loseACK, controllerWorkerTLS(t))
	f.engine.Stop()
	queue := &controllerCheckoutQueue{
		TaskQueue: &controllerWorkerRecordingQueue{TaskQueue: f.producer, fixture: f},
		fixture:   f,
		queued:    make(chan controllerCheckoutQueued, 8),
	}
	f.engine = process.NewProcess(f.repos, nil, queue, nil, &log.Logger{SugaredLogger: zap.NewNop().Sugar()}, nil, nil)
	t.Cleanup(f.engine.Stop)
	return f, queue.queued
}

func controllerCheckoutGit(t *testing.T, ctx context.Context, directory string, args ...string) string {
	t.Helper()
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Checkout Test", "GIT_AUTHOR_EMAIL=checkout@example.invalid", "GIT_COMMITTER_NAME=Checkout Test", "GIT_COMMITTER_EMAIL=checkout@example.invalid")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, output, err)
	}
	return strings.TrimSpace(string(output))
}

func controllerCheckoutRepository(t *testing.T, ctx context.Context) (directory, initial, changed string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	// 所有 Git 操作只使用本地临时仓库和虚构身份，不读取用户 Git 配置或真实凭据
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	directory = t.TempDir()
	controllerCheckoutGit(t, ctx, directory, "init", "--object-format=sha1", "-b", "main")
	writeCommit := func(contents string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(directory, "source.txt"), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		controllerCheckoutGit(t, ctx, directory, "add", "source.txt")
		controllerCheckoutGit(t, ctx, directory, "commit", "-m", contents)
		return controllerCheckoutGit(t, ctx, directory, "rev-parse", "HEAD")
	}
	initial = writeCommit("initial checkout")
	controllerCheckoutGit(t, ctx, directory, "checkout", "-b", "later")
	changed = writeCommit("user-selected checkout")
	controllerCheckoutGit(t, ctx, directory, "checkout", "main")
	if initial == changed || len(initial) != 40 || len(changed) != 40 {
		t.Fatal("local source fixture requires distinct full Git commit IDs")
	}
	return directory, initial, changed
}

func controllerCheckoutSource(directory string) *spec.Source {
	return &spec.Source{Type: "git", Repo: directory, Branch: "main", Auth: &spec.SourceAuth{Username: "checkout-fixture-user", Token: controllerCheckoutCredential}}
}

func controllerCheckoutExpectation(t *testing.T, f *controllerWorkerFixture, queued <-chan controllerCheckoutQueued, runID, directory string) controllerCheckoutQueued {
	t.Helper()
	var snapshot controllerCheckoutQueued
	select {
	case snapshot = <-queued:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	job, source := snapshot.job, snapshot.payload.Source
	hash := sha256.New()
	for _, value := range []string{"arcentra-source-binding-v1", f.pipelineID, runID, job.JobRunID, "git", directory, "main"} {
		_, _ = hash.Write(binary.BigEndian.AppendUint64(nil, uint64(len(value))))
		_, _ = hash.Write([]byte(value))
	}
	expectedDigest := "v1:" + hex.EncodeToString(hash.Sum(nil))
	if source == nil || !job.SourceExpectationCaptured || job.SourceType != "git" || job.SourceRepository != directory || job.SourceRef != "main" || job.SourceBindingDigest != expectedDigest || job.SourceBindingDigest != source.BindingDigest {
		t.Fatalf("queue publication did not preserve expected source binding: job=%+v source=%+v", job, source)
	}
	if job.PipelineRunID != runID || job.PipelineID != f.pipelineID || job.AgentID != f.conf.Agent.ID || job.Status != model.JobRunStatusQueued || source.Repo != directory || source.Branch != "main" || source.Commit != "" {
		t.Fatalf("queued binding lost controller/Agent identity: %+v", job)
	}
	if job.SourceResolvedCommit != "" || job.SourceProvenanceState != "unknown" {
		t.Fatalf("queue-time expectation invented observed checkout evidence: %+v", job)
	}
	if source.Auth["token"] != controllerCheckoutCredential {
		t.Fatal("credential sentinel did not exercise source authentication transport")
	}
	controllerCheckoutNoCredential(t, job)
	return snapshot
}

func controllerCheckoutNoCredential(t *testing.T, job model.JobRun) {
	t.Helper()
	// 源绑定字段不会暴露在普通 JSON DTO 中，检查结构体全部字段而非仅序列化 JSON
	data := fmt.Sprintf("%+v", job)
	for _, sentinel := range []string{controllerCheckoutCredential, "checkout-fixture-user"} {
		if strings.Contains(data, sentinel) {
			t.Fatalf("source authentication was persisted in JobRun: %s", sentinel)
		}
	}
}

func controllerCheckoutAssertEvidence(t *testing.T, f *controllerWorkerFixture, expected controllerCheckoutQueued, initial string) *model.JobRun {
	t.Helper()
	job, err := f.repos.JobRun.GetByJobRunID(f.ctx, expected.job.JobRunID)
	if err != nil {
		t.Fatal(err)
	}
	if job == nil || !job.SourceExpectationCaptured || job.SourceType != expected.job.SourceType || job.SourceRepository != expected.job.SourceRepository || job.SourceRef != expected.job.SourceRef || job.SourceBindingDigest != expected.job.SourceBindingDigest || job.SourceResolvedCommit != initial || job.SourceProvenanceState != "reported" {
		t.Fatalf("initial checkout evidence or queued expectation changed: %+v", job)
	}
	controllerCheckoutNoCredential(t, *job)
	f.mu.Lock()
	receipts := append([]controllerWorkerReceipt(nil), f.receipts["job/"+job.JobRunID]...)
	f.mu.Unlock()
	if len(receipts) == 0 {
		t.Fatal("no authenticated gRPC terminal checkout report was accepted")
	}
	for _, receipt := range receipts {
		request, ok := receipt.message.(*agentv1.ReportJobRunStatusRequest)
		if !ok || request.GetSourceCheckout() == nil || request.GetSourceCheckout().GetBindingDigest() != expected.job.SourceBindingDigest || request.GetSourceCheckout().GetResolvedCommit() != initial {
			t.Fatalf("terminal replay changed checkout evidence: %v", receipt.message)
		}
		if bytes.Contains(receipt.payload, []byte(controllerCheckoutCredential)) || bytes.Contains(receipt.payload, []byte("checkout-fixture-user")) {
			t.Fatal("source credentials reached durable terminal report payload")
		}
	}
	f.assertReceipt("job", job.JobRunID, job.ClaimID, job.TerminalReportDigest, job.PipelineRunID, job.JobRunID)
	return job
}

func controllerCheckoutDrain(f *controllerWorkerFixture) {
	f.t.Helper()
	f.wait("checkout terminal outbox drain", func() bool {
		f.worker.reports.mu.Lock()
		defer f.worker.reports.mu.Unlock()
		return len(f.worker.reports.entries) == 0
	})
}

// TestControllerWorkerCheckoutProvenanceRoundTrip 使用真实控制面、Git、Shell、TLS gRPC 和 SQLite
// 仅 Kafka broker 使用 librdkafka MockCluster，制品传输使用已有内存存储替身
func TestControllerWorkerCheckoutProvenanceRoundTrip(t *testing.T) {
	for _, loseACK := range []bool{false, true} {
		name := "success"
		if loseACK {
			name = "committed_terminal_ack_loss"
		}
		t.Run(name, func(t *testing.T) {
			f, queued := newControllerCheckoutFixture(t, loseACK)
			directory, initial, changed := controllerCheckoutRepository(t, f.ctx)
			command := fmt.Sprintf("test \"$(git rev-parse HEAD)\" = %s && printf x >> executions && git fetch origin %s && git checkout --detach %s && test \"$(git rev-parse HEAD)\" = %s && mkdir -p dist && git rev-parse HEAD > dist/changed-head", initial, changed, changed, changed)
			run := f.submit(&spec.Job{Name: "checkout", Source: controllerCheckoutSource(directory), Steps: []*spec.Step{
				controllerWorkerShell("change-head", command),
				{Name: "publish", Uses: "builtin:artifacts", Action: "upload", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"paths": []any{"dist"}})},
			}})
			expected := controllerCheckoutExpectation(t, f, queued, run.RunID, directory)
			f.waitRun(run.RunID, model.PipelineStatusSuccess)
			controllerCheckoutDrain(f)
			job := controllerCheckoutAssertEvidence(t, f, expected, initial)
			workspace := filepath.Join(f.conf.Agent.WorkspaceDir, run.RunID, job.JobRunID)
			if got := controllerCheckoutGit(t, f.ctx, workspace, "rev-parse", "HEAD"); got != changed {
				t.Fatalf("user step did not change checkout HEAD: %s", got)
			}
			if got := artifactTestRead(t, workspace, "executions"); got != "x" {
				t.Fatalf("checkout command execution count=%q", got)
			}
			f.mu.Lock()
			replayCount, lostACKs := len(f.receipts["job/"+job.JobRunID]), f.lostACKs
			duplicate := f.tasks[job.JobRunID]
			f.mu.Unlock()
			if loseACK && (lostACKs != 1 || replayCount < 2) {
				t.Fatalf("committed terminal ACK loss did not replay immutable evidence: lost=%d receipts=%d", lostACKs, replayCount)
			}
			if !loseACK && lostACKs != 0 {
				t.Fatalf("unexpected ACK loss: %d", lostACKs)
			}
			if len(duplicate.Payload) == 0 {
				t.Fatal("original Process Kafka task was not captured")
			}
			if _, err := f.producer.Enqueue(&duplicate, nova.Queue(taskpayload.AgentQueue(f.conf.Agent.ID))); err != nil {
				t.Fatal(err)
			}
			// 同一 Kafka 分区的后继作业和提交位点证明重复任务已消费，没有只等待任意延迟
			next := f.submit(&spec.Job{Name: "after-duplicate", Steps: []*spec.Step{controllerWorkerShell("healthy", "printf x > executions")}})
			f.waitRun(next.RunID, model.PipelineStatusSuccess)
			assertControllerQueueCommitted(t, f, 3)
			controllerCheckoutDrain(f)
			actual := controllerCheckoutAssertEvidence(t, f, expected, initial)
			if !reflect.DeepEqual(job, actual) {
				t.Fatalf("duplicate Kafka delivery mutated accepted checkout record: before=%+v after=%+v", job, actual)
			}
			if got := artifactTestRead(t, workspace, "executions"); got != "x" {
				t.Fatalf("duplicate Kafka task reran checkout shell: %q", got)
			}
			if got := controllerCheckoutGit(t, f.ctx, workspace, "rev-parse", "HEAD"); got != changed {
				t.Fatalf("duplicate task replaced user-selected checkout: %s", got)
			}
			f.store.mu.Lock()
			uploads := f.store.uploads
			f.store.mu.Unlock()
			if uploads != 1 {
				t.Fatalf("duplicate task repeated artifact side effect: uploads=%d", uploads)
			}
		})
	}
}

// TestControllerWorkerCheckoutProvenanceCancellation 保留取消前已完成的初始检出证据
func TestControllerWorkerCheckoutProvenanceCancellation(t *testing.T) {
	f, queued := newControllerCheckoutFixture(t, false)
	directory, initial, changed := controllerCheckoutRepository(t, f.ctx)
	command := fmt.Sprintf("test \"$(git rev-parse HEAD)\" = %s && printf x >> executions && git fetch origin %s && git checkout --detach %s && printf x > started; sleep 30 & wait; printf unexpected > after-cancel", initial, changed, changed)
	run := f.submit(&spec.Job{Name: "cancel-checkout", Source: controllerCheckoutSource(directory), Steps: []*spec.Step{
		controllerWorkerShell("wait", command),
		controllerWorkerShell("never", "printf unexpected > later"),
	}})
	expected := controllerCheckoutExpectation(t, f, queued, run.RunID, directory)
	workspace := filepath.Join(f.conf.Agent.WorkspaceDir, run.RunID, expected.job.JobRunID)
	f.wait("source checkout and user HEAD change before cancellation", func() bool {
		_, err := os.Stat(filepath.Join(workspace, "started"))
		return err == nil
	})
	if got := controllerCheckoutGit(t, f.ctx, workspace, "rev-parse", "HEAD"); got != changed {
		t.Fatalf("user step did not change HEAD before cancellation: %s", got)
	}
	if err := f.engine.CancelRun(run.RunID); err != nil {
		t.Fatal(err)
	}
	f.waitRun(run.RunID, model.PipelineStatusCancelled)
	f.wait("cancelled Agent execution releases job", func() bool {
		_, running := runningJobRunCancel.Load(expected.job.JobRunID)
		return !running
	})
	controllerCheckoutDrain(f)
	job := controllerCheckoutAssertEvidence(t, f, expected, initial)
	if job.Status != model.JobRunStatusCancelled {
		t.Fatalf("cancelled checkout job status=%d", job.Status)
	}
	if got := artifactTestRead(t, workspace, "executions"); got != "x" {
		t.Fatalf("cancelled checkout shell execution count=%q", got)
	}
	for _, name := range []string{"after-cancel", "later"} {
		if _, err := os.Stat(filepath.Join(workspace, name)); !os.IsNotExist(err) {
			t.Fatalf("cancelled checkout command continued: %s (%v)", name, err)
		}
	}
	assertControllerQueueCommitted(t, f, 1)
}
