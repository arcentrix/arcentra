//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/sandbox"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"google.golang.org/protobuf/proto"
)

// containerAcceptanceArtifactChain 在原真实验收内验证两个独立容器仅通过已登记 S3 制品传递
// 所有 JobRun 和 StepRun 都由实际 Process.Submit 创建，观察钩子不替换传输或运行时
func containerAcceptanceArtifactChain(t *testing.T, f *controllerWorkerFixture, cfg *realContainerSettings, store *containerAcceptanceStorage) {
	t.Helper()
	sourceURL, initialCommit := containerAcceptanceSource(t, f.ctx, func() error {
		records, err := containerAcceptanceRecords(f.conf.Agent.Sandbox.Containerd.JournalDir)
		if err != nil {
			return err
		}
		allocated := 0
		for _, record := range records {
			if record.Record.Phase == "allocated" {
				state, err := f.worker.offline.backend.InspectOwned(f.ctx, record.Record.Resource)
				if err != nil || state != (sandbox.ResourceState{}) {
					return fmt.Errorf("chain source staging occurred after runtime creation: %+v %v", state, err)
				}
				allocated++
			}
		}
		if allocated != 1 {
			return fmt.Errorf("chain source staging requires one allocated session: %d", allocated)
		}
		return nil
	})
	var mu sync.Mutex
	uploads := make(map[string]containerAcceptanceJournal)
	downloads := make(map[string]int)
	var staged containerAcceptanceJournal
	store.mu.Lock()
	initialUploads := store.sealedUploads
	store.afterUpload = func(_ context.Context, key string, owner containerAcceptanceJournal) error {
		mu.Lock()
		defer mu.Unlock()
		if _, duplicate := uploads[key]; duplicate {
			return fmt.Errorf("chain repeated the same actual S3 upload")
		}
		uploads[key] = owner
		return nil
	}
	store.beforeDownload = func(ctx context.Context, key string) error {
		mu.Lock()
		owner, ok := uploads[key]
		mu.Unlock()
		if !ok || owner.Record == nil {
			return fmt.Errorf("consumer requested an object outside its observed producer")
		}
		producer, err := f.repos.JobRun.GetByJobRunID(ctx, owner.Record.JobRunID)
		if err != nil || producer == nil || producer.Status != model.JobRunStatusSuccess || len(producer.TerminalReportDigest) != 64 || producer.EndTime == nil || producer.ClaimID != owner.Record.Claim || producer.RuntimeBindingDigest != owner.Record.Binding {
			return fmt.Errorf("consumer staged before producer's bound successful terminal receipt: %v", err)
		}
		rows, count, err := f.repos.Artifact.List(ctx, repo.ArtifactQuery{ProjectID: f.projectID, RunID: producer.PipelineRunID, JobRunID: producer.JobRunID, Limit: 20})
		if err != nil || count != 2 {
			return fmt.Errorf("consumer staged before both producer artifacts were registered: count=%d error=%v", count, err)
		}
		registered := false
		for _, row := range rows {
			if row.StoragePath == key && row.StepRunID != "" && row.StorageID != "" && row.StorageFingerprint != "" && len(row.SHA256) == 64 && row.ExpiresAt != nil && row.ExpiresAt.After(time.Now()) {
				registered = true
			}
		}
		if !registered {
			return fmt.Errorf("consumer requested an unregistered or expired object")
		}
		state, err := f.worker.offline.backend.InspectOwned(ctx, owner.Record.Resource)
		if err != nil || state != (sandbox.ResourceState{}) {
			return fmt.Errorf("consumer staging preceded exact producer runtime cleanup: %+v %v", state, err)
		}
		if _, err := os.Lstat(filepath.Join(f.conf.Agent.WorkspaceDir, owner.Record.Directory)); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("consumer inherited producer's uncleared workspace: %v", err)
		}
		records, err := containerAcceptanceRecords(f.conf.Agent.Sandbox.Containerd.JournalDir)
		if err != nil {
			return err
		}
		var consumer containerAcceptanceJournal
		for _, record := range records {
			if record.Record.RunID == producer.PipelineRunID && record.Record.JobRunID != producer.JobRunID {
				if consumer.Record != nil || record.Record.Phase != "allocated" {
					return fmt.Errorf("consumer download occurred after container creation/start")
				}
				consumer = record
			}
		}
		if consumer.Record == nil || consumer.Record.SessionID == "" || consumer.Record.SessionID == owner.Record.SessionID || consumer.Record.Claim == owner.Record.Claim || consumer.Record.Directory == owner.Record.Directory || consumer.Record.Resource.ID == owner.Record.Resource.ID {
			return fmt.Errorf("producer and consumer do not own distinct allocated sessions, claims and workspaces")
		}
		consumerJob, err := f.repos.JobRun.GetByJobRunID(ctx, consumer.Record.JobRunID)
		if err != nil || consumerJob == nil || consumerJob.InputManifestState != "pinned" || consumerJob.ClaimID != consumer.Record.Claim {
			return fmt.Errorf("consumer transfer preceded its original immutable input pin: %v", err)
		}
		manifest, err := taskpayload.ParseJobInputManifest(consumerJob.InputManifest)
		if err != nil || manifest.SourceCommit != initialCommit || len(manifest.Artifacts) != 2 {
			return fmt.Errorf("consumer transfer lost exact source and artifact inputs: %v", err)
		}
		identity := taskpayload.JobInputIdentity{PipelineID: consumerJob.PipelineID, PipelineRunID: consumerJob.PipelineRunID, JobRunID: consumerJob.JobRunID, AgentID: consumerJob.AgentID, ClaimID: consumerJob.ClaimID, RuntimeBindingDigest: consumerJob.RuntimeBindingDigest, SourceBindingDigest: consumerJob.SourceBindingDigest}
		digest, err := manifest.Digest(identity)
		if err != nil || digest != consumerJob.InputManifestDigest {
			return fmt.Errorf("consumer transfer lost immutable execution identity: %v", err)
		}
		pinnedRegistered := false
		for _, input := range manifest.Artifacts {
			for _, row := range rows {
				if input.Key == key && row.StoragePath == key && input.Name == "chain-a/"+row.Name && input.Reference.StorageID == row.StorageID && input.Reference.Fingerprint == row.StorageFingerprint && input.Reference.SHA256 == row.SHA256 && input.Reference.Size != nil && *input.Reference.Size == row.Size && row.ExpiresAt != nil && input.Reference.ExpiresAt == row.ExpiresAt.Unix() {
					pinnedRegistered = true
				}
			}
		}
		if !pinnedRegistered {
			return fmt.Errorf("consumer transferred an object outside its hash/size-bound registered manifest")
		}
		state, err = f.worker.offline.backend.InspectOwned(ctx, consumer.Record.Resource)
		if err != nil || state != (sandbox.ResourceState{}) {
			return fmt.Errorf("consumer runtime existed during host input staging: %+v %v", state, err)
		}
		mu.Lock()
		defer mu.Unlock()
		if staged.Record != nil && !reflect.DeepEqual(staged, consumer) {
			return fmt.Errorf("consumer identity changed between input transfers")
		}
		staged = consumer
		downloads[key]++
		return nil
	}
	store.mu.Unlock()
	t.Cleanup(func() {
		store.mu.Lock()
		store.afterUpload, store.beforeDownload = nil, nil
		store.mu.Unlock()
	})

	// Independently lose the first committed terminal ACK for each job. The service
	// still commits the original receipt, and only the real outbox retries delivery.
	seenTerminal := make(map[string]bool)
	f.mu.Lock()
	initialLostACKs := f.lostACKs
	f.observeTerminal = func(_ context.Context, message proto.Message, _ any, err error) {
		request, ok := message.(*agentv1.ReportJobRunStatusRequest)
		if !ok || err != nil || len(request.Artifacts) == 0 {
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if !seenTerminal[request.JobRunId] {
			seenTerminal[request.JobRunId] = true
			f.loseProducerACK = true
		}
	}
	f.mu.Unlock()
	produce := `test "$(pwd)" = /workspace
 test "$(ls /sys/class/net)" = lo
 test "$(cat source.txt)" = 'initial checkout'
 test "$(cat tolerated-exit)" = x
 printf x >> executions
 printf 'must stay private' > producer-private.txt
 mkdir dist
 printf '\000\001\377container-chain\n\r\200\177' > dist/result.bin
 cp executions dist/executions`
	consume := `test "$(pwd)" = /workspace
 test "$(ls /sys/class/net)" = lo
 test "$(cat source.txt)" = 'initial checkout'
 test ! -e producer-private.txt
 test ! -e tolerated-exit
 test ! -e .artifacts/chain-a/producer-private.txt
 test ! -e executions
 test "$(cat dist/executions)" = x
 printf '\000\001\377container-chain\n\r\200\177' > expected.bin
 cmp expected.bin dist/result.bin
 cmp .artifacts/chain-a/dist/result.bin dist/result.bin
 printf x >> executions
 mkdir proof
 cp dist/result.bin proof/result.bin
 cp executions proof/executions`
	tolerated := controllerWorkerShell("allowed-failure", "printf x > tolerated-exit; exit 9")
	tolerated.ContinueOnError = true
	run := containerAcceptanceSubmit(t, f, cfg,
		&spec.Job{Name: "chain-a", Source: &spec.Source{Type: "git", Repo: sourceURL, Branch: "main"}, Steps: []*spec.Step{
			tolerated,
			controllerWorkerShell("produce", produce),
			containerAcceptanceArtifactStep("publish", "upload", map[string]any{"paths": []any{"dist"}, "expireIn": "7d"}),
		}},
		&spec.Job{Name: "chain-b", Source: &spec.Source{Type: "git", Repo: sourceURL, Branch: "main"}, DependsOn: []string{"chain-a"}, Steps: []*spec.Step{
			containerAcceptanceArtifactStep("fetch", "download", map[string]any{"job": "chain-a", "paths": []any{"dist/*"}}),
			controllerWorkerShell("verify", consume),
			containerAcceptanceArtifactStep("proof", "upload", map[string]any{"paths": []any{"proof"}, "expireIn": "7d"}),
		}},
	)
	completed := f.waitRun(run.RunID, model.PipelineStatusSuccess)
	if completed.TotalJobs != 2 || completed.CompletedJobs != 2 || completed.FailedJobs != 0 {
		t.Fatalf("actual two-job container chain did not complete: %+v", completed)
	}
	jobs, err := f.repos.JobRun.ListByPipelineRunID(f.ctx, run.RunID)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("expected two Process-created chain jobs: %d %v", len(jobs), err)
	}
	byName := make(map[string]*model.JobRun, 2)
	for _, job := range jobs {
		containerAcceptanceBoundJob(t, f, job, model.JobRunStatusSuccess)
		byName[job.JobName] = job
		manifest, err := taskpayload.ParseJobInputManifest(job.InputManifest)
		if err != nil || manifest.SourceCommit != initialCommit || job.SourceResolvedCommit != initialCommit {
			t.Fatalf("chain did not pin the original full source commit: %v", err)
		}
		wantInputs := 0
		if job.JobName == "chain-b" {
			wantInputs = 2
		}
		if len(manifest.Artifacts) != wantInputs {
			t.Fatalf("%s pinned unexpected input artifact count: %d", job.JobName, len(manifest.Artifacts))
		}
	}
	producer, consumer := byName["chain-a"], byName["chain-b"]
	if producer == nil || consumer == nil || producer.ClaimID == consumer.ClaimID || producer.RuntimeBindingDigest == consumer.RuntimeBindingDigest {
		t.Fatal("linear jobs lost their distinct bound execution claims")
	}
	f.wait("both chain receipts replay and exact sessions close", func() bool {
		if f.worker.offline.HasJob(producer.JobRunID) || f.worker.offline.HasJob(consumer.JobRunID) {
			return false
		}
		f.worker.reports.mu.Lock()
		defer f.worker.reports.mu.Unlock()
		for _, job := range jobs {
			if _, pending := f.worker.reports.entries[terminalRecordName("job", job.JobRunID)]; pending {
				return false
			}
		}
		return true
	})
	steps, count, err := f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: run.RunID, PageSize: 20})
	if err != nil || count != 6 {
		t.Fatalf("expected six Process-created chain steps: %d %v", count, err)
	}
	for _, step := range steps {
		wantStatus, wantExit := model.JobRunStatusSuccess, 0
		if step.Name == "allowed-failure" {
			wantStatus, wantExit = model.JobRunStatusFailed, 9
			if step.JobRunID != producer.JobRunID || step.ContinueOnError != 1 {
				t.Fatal("allowed command failure lost its producer policy")
			}
		}
		if int(step.Status) != wantStatus || step.Attempt != 1 || step.PreviousStepRunID != "" || step.ExitCode == nil || *step.ExitCode != wantExit {
			t.Fatalf("invalid chain step result or implicit retry: %+v", step)
		}
		f.assertReceipt("step", step.StepRunID, step.ClaimID, step.TerminalReportDigest, run.RunID, step.JobRunID)
	}
	for _, job := range jobs {
		f.assertReceipt("job", job.JobRunID, job.ClaimID, job.TerminalReportDigest, run.RunID, job.JobRunID)
	}
	f.mu.Lock()
	lostACKs := f.lostACKs - initialLostACKs
	producerReplays, consumerReplays := len(f.receipts["job/"+producer.JobRunID]), len(f.receipts["job/"+consumer.JobRunID])
	producerTask, consumerTask := f.tasks[producer.JobRunID], f.tasks[consumer.JobRunID]
	f.observeTerminal = nil
	f.mu.Unlock()
	if lostACKs != 2 || producerReplays < 2 || consumerReplays < 2 {
		t.Fatalf("both committed job ACK losses were not replayed: losses=%d receipts=%d/%d", lostACKs, producerReplays, consumerReplays)
	}
	var payload taskpayload.JobRunTaskPayload
	if err := sonic.Unmarshal(consumerTask.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	rows, count, err := f.repos.Artifact.List(f.ctx, repo.ArtifactQuery{ProjectID: f.projectID, RunID: run.RunID, Limit: 20})
	if err != nil || count != 4 || len(payload.ArtifactURIs) != 2 || len(payload.ArtifactReferences) != 2 {
		t.Fatalf("chain did not retain exact registered producer inputs: rows=%d inputs=%d %v", count, len(payload.ArtifactURIs), err)
	}
	expected := append([]byte{0, 1, 255}, []byte("container-chain\n\r\x80\x7f")...)
	mu.Lock()
	if staged.Record == nil || staged.Record.JobRunID != consumer.JobRunID || staged.Record.Claim != consumer.ClaimID || staged.Record.Binding != consumer.RuntimeBindingDigest || len(uploads) != 4 || len(downloads) != 2 {
		mu.Unlock()
		t.Fatal("missing independent consumer staging or repeated producer/consumer upload")
	}
	observed := make(map[string]containerAcceptanceJournal, len(uploads))
	for key, owner := range uploads {
		observed[key] = owner
	}
	for key, calls := range downloads {
		// One prefetch and one explicit builtin download, both before Start.
		if calls != 2 {
			mu.Unlock()
			t.Fatalf("input transfer repeated or bypassed explicit staging: %s calls=%d", key, calls)
		}
	}
	mu.Unlock()
	for _, row := range rows {
		wanted := expected
		if strings.HasSuffix(row.Name, "/executions") {
			wanted = []byte("x")
		}
		job := producer
		if strings.HasPrefix(row.Name, "proof/") {
			job = consumer
		}
		owner, ok := observed[row.StoragePath]
		if !ok || owner.Record.JobRunID != job.JobRunID || row.JobRunID != job.JobRunID || row.StepRunID == "" || row.ArtifactID != artifactutil.ID(f.projectID, run.RunID, job.JobRunID, row.StepRunID, row.Name) || row.Size != int64(len(wanted)) || row.SHA256 != fmt.Sprintf("%x", sha256.Sum256(wanted)) {
			t.Fatalf("registered chain artifact lost exact owner or bytes: %+v", row)
		}
		reader, err := store.S3Storage.DownloadStream(f.ctx, row.StoragePath)
		if err != nil {
			t.Fatal(err)
		}
		actual, readErr := io.ReadAll(io.LimitReader(reader, 1<<20))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || !bytes.Equal(actual, wanted) {
			t.Fatalf("actual S3 chain bytes changed: %s %v %v", row.Name, readErr, closeErr)
		}
		if job == producer {
			name := "chain-a/" + row.Name
			ref := payload.ArtifactReferences[name]
			if payload.ArtifactURIs[name] != row.StoragePath || ref.StorageID != row.StorageID || ref.Fingerprint != row.StorageFingerprint || ref.SHA256 != row.SHA256 || ref.Size == nil || *ref.Size != row.Size || row.ExpiresAt == nil || ref.ExpiresAt != row.ExpiresAt.Unix() {
				t.Fatal("Process dispatched an input outside the registered producer/storage identity")
			}
		}
		containerAcceptanceRemoved(t, f, owner)
	}
	// Re-deliver both original Kafka jobs after ACK recovery. Commit proves each
	// duplicate was handled; immutable outcomes and upload counts prove no rerun.
	for _, task := range []nova.Task{producerTask, consumerTask} {
		if _, err := f.producer.Enqueue(&task, nova.Queue(taskpayload.AgentQueue(f.conf.Agent.ID))); err != nil {
			t.Fatal(err)
		}
	}
	assertControllerQueueCommitted(t, f, 14)
	for _, job := range jobs {
		actual, err := f.repos.JobRun.GetByJobRunID(f.ctx, job.JobRunID)
		if err != nil || !reflect.DeepEqual(job, actual) {
			t.Fatalf("duplicate chain task changed accepted job: %v", err)
		}
	}
	actualSteps, _, err := f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: run.RunID, PageSize: 20})
	if err != nil || !reflect.DeepEqual(steps, actualSteps) {
		t.Fatalf("duplicate chain task changed accepted attempts: %v", err)
	}
	store.mu.Lock()
	uploadCount := store.sealedUploads - initialUploads
	store.afterUpload, store.beforeDownload = nil, nil
	store.mu.Unlock()
	if uploadCount != 4 {
		t.Fatalf("ACK loss or duplicate task repeated actual S3 uploads: %d", uploadCount)
	}
	t.Log("real Process-created source-SHA/input-manifest-pinned container A -> hash/size-bound registered S3 artifacts -> fresh container B verified byte-for-byte, including binary data and allowed nonzero shell exit; distinct claims/session/workspaces, seal/cleanup/terminal-before-staging, private-file isolation, both committed ACK losses and duplicate Kafka deliveries verified")
}

func containerAcceptanceArtifactStep(name, action string, args map[string]any) *spec.Step {
	return &spec.Step{Name: name, Uses: "builtin:artifacts", Action: action, RunOnAgent: true, Args: spec.MapAsStruct(args)}
}

func containerAcceptanceDependent(upstream string) *spec.Job {
	return &spec.Job{Name: "blocked-consumer", DependsOn: []string{upstream}, Steps: []*spec.Step{controllerWorkerShell("must-not-run", "printf forbidden > downstream-executed")}}
}

func containerAcceptanceBlockedDownstream(t *testing.T, f *controllerWorkerFixture, producer *model.JobRun) {
	t.Helper()
	jobs, err := f.repos.JobRun.ListByPipelineRunID(f.ctx, producer.PipelineRunID)
	if err != nil || len(jobs) != 1 || jobs[0].JobRunID != producer.JobRunID {
		t.Fatalf("unsuccessful producer allowed downstream execution records: %d %v", len(jobs), err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, task := range f.tasks {
		var payload taskpayload.JobRunTaskPayload
		if err := sonic.Unmarshal(task.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.PipelineRunID == producer.PipelineRunID && payload.JobRunID != producer.JobRunID {
			t.Fatal("unsuccessful producer enqueued a downstream container")
		}
	}
}
