//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/sandbox"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// containerAcceptanceAdmission 精确镜像无资源的首次准入文件，仅供真实验收旁观校验
// 单独识别该版本化类型，不把未知 JSON 当作可忽略的工作区记录
type containerAcceptanceAdmission struct {
	Version           int    `json:"version"`
	ControllerID      string `json:"controllerID"`
	AgentID           string `json:"agentID"`
	WorkspaceRoot     string `json:"workspaceRoot"`
	Namespace         string `json:"namespace"`
	RuntimeIOIdentity string `json:"runtimeIOIdentity,omitempty"`
	Admission         struct {
		Request           taskpayload.JobGenerationTransition `json:"request"`
		Generation        *taskpayload.JobGeneration          `json:"generation,omitempty"`
		Observation       *taskpayload.JobGeneration          `json:"observation,omitempty"`
		FinalAcknowledged bool                                `json:"finalAcknowledged,omitempty"`
	} `json:"admission"`
	Checksum string `json:"checksum"`
}

func validateContainerAcceptanceAdmission(root, name string, data []byte) error {
	var document containerAcceptanceAdmission
	if err := sonic.Unmarshal(data, &document); err != nil {
		return err
	}
	checksum := document.Checksum
	document.Checksum = ""
	body, err := sonic.Marshal(document)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	if checksum != hex.EncodeToString(sum[:]) {
		return fmt.Errorf("acceptance admission checksum mismatch")
	}
	document.Checksum = checksum
	canonical, err := sonic.Marshal(document)
	if err != nil || !bytes.Equal(data, canonical) {
		return fmt.Errorf("acceptance admission is not an exact known canonical document")
	}
	ownerData, err := os.ReadFile(filepath.Join(root, "owner.json"))
	if err != nil {
		return err
	}
	var owner struct {
		ControllerID      string `json:"controllerID"`
		AgentID           string `json:"agentID"`
		WorkspaceRoot     string `json:"workspaceRoot"`
		Namespace         string `json:"namespace"`
		RuntimeIOIdentity string `json:"runtimeIOIdentity"`
	}
	if err := sonic.Unmarshal(ownerData, &owner); err != nil {
		return err
	}
	request := document.Admission.Request
	if document.Version != 2 || document.ControllerID != owner.ControllerID || document.AgentID != owner.AgentID || document.WorkspaceRoot != owner.WorkspaceRoot || document.Namespace != owner.Namespace || document.RuntimeIOIdentity != owner.RuntimeIOIdentity || request.Operation != "admit" || request.Identity.AgentID != owner.AgentID {
		return fmt.Errorf("acceptance admission lost exact controller/runtime identity")
	}
	if err := request.Validate(); err != nil {
		return err
	}
	identity, _ := sonic.Marshal(request.Identity)
	if name != "admission-"+uuid.NewSHA1(uuid.NameSpaceOID, identity).String()+".json" {
		return fmt.Errorf("acceptance admission filename changed")
	}
	for _, generation := range []*taskpayload.JobGeneration{document.Admission.Generation, document.Admission.Observation} {
		if generation == nil {
			continue
		}
		if err := generation.Validate(); err != nil {
			return err
		}
		if generation.PipelineID != request.Identity.PipelineID || generation.PipelineRunID != request.Identity.PipelineRunID || generation.SourceBindingDigest != request.Identity.SourceBindingDigest || generation.JobRunID != request.Identity.JobRunID || generation.OriginalClaimID != request.Identity.ClaimID || generation.AgentID != request.Identity.AgentID || generation.RuntimeBindingDigest != request.Identity.RuntimeBindingDigest || generation.RetryPlanDigest != request.RetryPlanDigest {
			return fmt.Errorf("acceptance admission response changed execution owner")
		}
	}
	if generation := document.Admission.Generation; generation != nil && (generation.Number != 1 || generation.PreviousGenerationID != "" || generation.InputManifestDigest != request.InputManifestDigest || !generation.ExecutionAllowed) {
		return fmt.Errorf("acceptance admission is not the exact initial response")
	}
	if observed := document.Admission.Observation; observed != nil && observed.ExecutionAllowed {
		return fmt.Errorf("acceptance inspection unexpectedly carries execution permission")
	}
	return nil
}

func containerAcceptanceMutableRetrySource(t *testing.T, ctx context.Context) (url, initial, changed string, advance func(context.Context) error) {
	t.Helper()
	binary, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	directory, initial, changed := controllerCheckoutRepository(t, ctx)
	root := t.TempDir()
	bare := filepath.Join(root, "source.git")
	controllerCheckoutGit(t, ctx, root, "clone", "--bare", directory, bare)
	handler := &cgi.Handler{Path: binary, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}}
	server := httptest.NewServer(http.HandlerFunc(handler.ServeHTTP))
	t.Cleanup(server.Close)
	advance = func(ctx context.Context) error {
		cmd := exec.CommandContext(ctx, binary, "--git-dir", bare, "update-ref", "refs/heads/main", changed, initial)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("advance exact local fixture ref: %s: %w", output, err)
		}
		return nil
	}
	return server.URL + "/source.git", initial, changed, advance
}

func containerAcceptanceRetryHead(ctx context.Context, workspace string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", workspace, "rev-parse", "HEAD")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("inspect exact staged Git HEAD: %s: %w", output, err)
	}
	return strings.TrimSpace(string(output)), nil
}

type containerRetryObservation struct {
	mu               sync.Mutex
	err              error
	jobID            string
	staged           map[int]containerAcceptanceJournal
	inputManifest    string
	inputDigest      string
	actualHeads      map[int]string
	uploads          map[string]containerAcceptanceJournal
	downloads        map[string]int
	advanceRequests  []string
	advanceResponses []string
	branchAdvanced   bool
	lostAdvanceACK   bool
	lostJobACK       bool
	commandClaims    map[string]int
}

func (o *containerRetryObservation) fail(err error) {
	if err == nil {
		return
	}
	o.mu.Lock()
	o.err = errors.Join(o.err, err)
	o.mu.Unlock()
}

// containerAcceptanceWholeJobRetry 在同一已授权 fixture 内验证三作业链及两个新会话代次
// 不添加运行时、宿主权限、网络能力、工作流入口或测试总时间预算
func containerAcceptanceWholeJobRetry(t *testing.T, f *controllerWorkerFixture, cfg *realContainerSettings, store *containerAcceptanceStorage, retainedAudit map[string]string) {
	t.Helper()
	containerAcceptanceDrainWithAudit(t, f, retainedAudit)
	if !f.conf.Agent.Sandbox.Containerd.Policy.AllowWholeJobRetry {
		t.Fatal("retry acceptance must explicitly opt in its fixture policy")
	}
	sourceURL, initialCommit, changedCommit, advanceBranch := containerAcceptanceMutableRetrySource(t, f.ctx)
	observation := &containerRetryObservation{staged: make(map[int]containerAcceptanceJournal), actualHeads: make(map[int]string), uploads: make(map[string]containerAcceptanceJournal), downloads: make(map[string]int), commandClaims: make(map[string]int)}
	store.mu.Lock()
	initialUploads := store.sealedUploads
	store.afterUpload = func(ctx context.Context, key string, owner containerAcceptanceJournal) error {
		observation.mu.Lock()
		defer observation.mu.Unlock()
		if _, exists := observation.uploads[key]; exists {
			return fmt.Errorf("retry acceptance repeated an actual S3 upload")
		}
		if owner.Record.Generation != nil {
			if owner.Record.Generation.Number != 2 || owner.Record.JobRunID != observation.jobID {
				return fmt.Errorf("failed or foreign generation published an artifact")
			}
			old := observation.staged[1]
			if old.Record == nil {
				return fmt.Errorf("winning upload lacks prior generation ownership")
			}
			state, err := f.worker.offline.backend.InspectOwned(ctx, old.Record.Resource)
			if err != nil || state != (sandbox.ResourceState{}) {
				return fmt.Errorf("winning upload preceded old-generation cleanup: %+v %v", state, err)
			}
		}
		observation.uploads[key] = owner
		return nil
	}
	store.beforeDownload = func(ctx context.Context, key string) error {
		observation.mu.Lock()
		producer, ok := observation.uploads[key]
		observation.mu.Unlock()
		if !ok || producer.Record == nil {
			return fmt.Errorf("retry chain fetched an unobserved producer object")
		}
		producerJob, err := f.repos.JobRun.GetByJobRunID(ctx, producer.Record.JobRunID)
		if err != nil || producerJob == nil || producerJob.Status != model.JobRunStatusSuccess || producerJob.TerminalReportDigest == "" {
			return fmt.Errorf("retry input preceded committed successful producer: %v", err)
		}
		rows, count, err := f.repos.Artifact.List(ctx, repo.ArtifactQuery{ProjectID: f.projectID, RunID: producerJob.PipelineRunID, JobRunID: producerJob.JobRunID, Limit: 20})
		if err != nil || count != 1 || rows[0].StoragePath != key {
			return fmt.Errorf("retry input is not the sole registered producer artifact: count=%d %v", count, err)
		}
		state, err := f.worker.offline.backend.InspectOwned(ctx, producer.Record.Resource)
		if err != nil || state != (sandbox.ResourceState{}) {
			return fmt.Errorf("retry input preceded producer cleanup: %+v %v", state, err)
		}
		records, err := containerAcceptanceRecords(f.conf.Agent.Sandbox.Containerd.JournalDir)
		if err != nil {
			return err
		}
		var target containerAcceptanceJournal
		for _, record := range records {
			if record.Record.RunID == producerJob.PipelineRunID && record.Record.Phase == "allocated" {
				if target.Record != nil {
					return fmt.Errorf("more than one retry-chain session is staging")
				}
				target = record
			}
		}
		if target.Record == nil {
			return fmt.Errorf("retry chain input has no exact allocated consumer")
		}
		consumer, err := f.repos.JobRun.GetByJobRunID(ctx, target.Record.JobRunID)
		if err != nil || consumer == nil || consumer.InputManifestState != "pinned" || consumer.ClaimID != target.Record.Claim {
			return fmt.Errorf("retry input preceded exact immutable manifest pin: %v", err)
		}
		manifest, err := taskpayload.ParseJobInputManifest(consumer.InputManifest)
		if err != nil || len(manifest.Artifacts) != 1 {
			return fmt.Errorf("retry consumer manifest has unexpected inputs: %v", err)
		}
		input, row := manifest.Artifacts[0], rows[0]
		if input.Key != key || input.Reference.SHA256 != row.SHA256 || input.Reference.Size == nil || *input.Reference.Size != row.Size || input.Reference.StorageID != row.StorageID || input.Reference.Fingerprint != row.StorageFingerprint || row.ExpiresAt == nil || input.Reference.ExpiresAt != row.ExpiresAt.Unix() {
			return fmt.Errorf("retry input changed registered content/storage metadata")
		}
		state, err = f.worker.offline.backend.InspectOwned(ctx, target.Record.Resource)
		if err != nil || state != (sandbox.ResourceState{}) {
			return fmt.Errorf("retry input staged after runtime creation: %+v %v", state, err)
		}
		if target.Record.Generation != nil {
			g := target.Record.Generation
			if g.Number < 1 || g.Number > 2 || g.OriginalClaimID != consumer.ClaimID || manifest.SourceCommit != initialCommit {
				return fmt.Errorf("retry generation changed original source or claim")
			}
			head, err := containerAcceptanceRetryHead(ctx, filepath.Join(f.conf.Agent.WorkspaceDir, target.Record.Directory))
			if err != nil || head != initialCommit {
				return fmt.Errorf("retry checkout is not original full Git SHA: head=%s %v", head, err)
			}
			observation.mu.Lock()
			defer observation.mu.Unlock()
			if observation.inputManifest != "" && (observation.inputManifest != consumer.InputManifest || observation.inputDigest != consumer.InputManifestDigest) {
				return fmt.Errorf("retry changed pinned manifest bytes or digest")
			}
			if previous := observation.staged[g.Number]; previous.Record != nil && !reflect.DeepEqual(previous, target) {
				return fmt.Errorf("generation changed identity during input transfer")
			}
			if g.Number == 2 {
				first := observation.staged[1]
				if !observation.branchAdvanced || first.Record == nil || first.Record.SessionID == target.Record.SessionID || first.Record.Resource.ID == target.Record.Resource.ID || first.Record.Resource.Owner == target.Record.Resource.Owner || first.Record.Directory == target.Record.Directory {
					return fmt.Errorf("successor did not receive fresh owned resources after branch advance")
				}
				if _, err := os.Lstat(filepath.Join(f.conf.Agent.WorkspaceDir, first.Record.Directory)); !errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("old generation workspace remains during restaging")
				}
			}
			observation.jobID = consumer.JobRunID
			observation.inputManifest = consumer.InputManifest
			observation.inputDigest = consumer.InputManifestDigest
			observation.staged[g.Number] = target
			observation.actualHeads[g.Number] = head
			observation.downloads[key]++
			return nil
		}
		if consumer.JobName != "retry-c" || producerJob.JobName != "retry-b" {
			return fmt.Errorf("unexpected downstream retry-chain target")
		}
		observation.mu.Lock()
		observation.downloads[key]++
		observation.mu.Unlock()
		return nil
	}
	store.mu.Unlock()
	f.mu.Lock()
	initialLostACKs := f.lostACKs
	f.observeRequest = func(ctx context.Context, request any) {
		if step, ok := request.(*agentv1.ReportStepRunStatusRequest); ok && int32(step.Status) == 3 && step.GenerationId != "" {
			row, err := f.repos.StepRun.GetByStepRunID(ctx, step.StepRunId)
			if err != nil {
				observation.fail(err)
				return
			}
			if row == nil {
				observation.fail(fmt.Errorf("generation step activation has no persisted attempt"))
				return
			}
			if row.Name == "retry-command" {
				observation.mu.Lock()
				observation.commandClaims[step.StepRunId]++
				observation.mu.Unlock()
			}
		}
		generation, ok := request.(*agentv1.ReconcileJobGenerationRequest)
		if !ok {
			return
		}
		transition, err := taskpayload.ParseJobGenerationTransition(generation.TransitionJson)
		if err != nil {
			observation.fail(err)
			return
		}
		if transition.Operation != "advance" {
			return
		}
		observation.mu.Lock()
		observation.advanceRequests = append(observation.advanceRequests, generation.TransitionJson)
		firstCall := len(observation.advanceRequests) == 1
		first := observation.staged[1]
		pinned := observation.inputDigest
		observation.mu.Unlock()
		if !firstCall {
			return
		}
		if first.Record == nil || first.Record.Generation == nil || transition.CleanupReceipt == nil || transition.CleanupReceipt.ValidateGeneration(*first.Record.Generation) != nil || transition.InputManifestDigest != pinned || transition.CleanupReceipt.ResourceOwnerDigest != first.Record.Resource.Owner || transition.CleanupReceipt.ContainerID != first.Record.Resource.ID || transition.CleanupReceipt.Namespace != first.Record.Resource.Namespace || transition.CleanupReceipt.Snapshotter != first.Record.Resource.Snapshotter || transition.CleanupReceipt.SnapshotID != first.Record.Resource.ID+"-snapshot" || transition.CleanupReceipt.SessionID != first.Record.SessionID || transition.CleanupReceipt.WorkspaceID != first.Record.Directory || transition.CleanupReceipt.WorkspaceDevice != first.Record.Device || transition.CleanupReceipt.WorkspaceInode != first.Record.Inode {
			observation.fail(fmt.Errorf("advance lacked exact completed-generation cleanup/input evidence"))
			return
		}
		state, err := f.worker.offline.backend.InspectOwned(ctx, first.Record.Resource)
		if err != nil || state != (sandbox.ResourceState{}) {
			observation.fail(fmt.Errorf("advance occurred before exact old runtime removal: %+v %v", state, err))
			return
		}
		if _, err := os.Lstat(filepath.Join(f.conf.Agent.WorkspaceDir, first.Record.Directory)); !errors.Is(err, os.ErrNotExist) {
			observation.fail(fmt.Errorf("advance occurred before old workspace removal"))
			return
		}
		steps, _, err := f.repos.StepRun.List(ctx, repo.StepRunFilter{PipelineRunID: transition.Identity.PipelineRunID, PageSize: 20})
		if err != nil {
			observation.fail(err)
			return
		}
		completed := 0
		for _, step := range steps {
			if step.JobRunID != first.Record.JobRunID || step.Attempt != 1 {
				continue
			}
			want := model.JobRunStatusSuccess
			if step.Name == "retry-command" {
				want = model.JobRunStatusFailed
			} else if step.Name == "publish" {
				want = 8
			}
			if int(step.Status) != want || step.TerminalReportDigest == "" {
				observation.fail(fmt.Errorf("branch move preceded prior-round committed terminal receipts"))
				return
			}
			completed++
		}
		if completed != 3 {
			observation.fail(fmt.Errorf("branch move lacks all prior-round terminal receipts"))
			return
		}
		if err := advanceBranch(ctx); err != nil {
			observation.fail(err)
			return
		}
		observation.mu.Lock()
		observation.branchAdvanced = true
		observation.mu.Unlock()
		store.mu.Lock()
		store.refs = append(store.refs, first.Record.Resource)
		store.mu.Unlock()
	}
	f.afterGeneration = func(_ context.Context, request *agentv1.ReconcileJobGenerationRequest, response *agentv1.ReconcileJobGenerationResponse, callErr error) error {
		transition, err := taskpayload.ParseJobGenerationTransition(request.TransitionJson)
		if err != nil {
			return err
		}
		if transition.Operation != "advance" || callErr != nil {
			return nil
		}
		if response == nil {
			return fmt.Errorf("advance committed without response")
		}
		observation.mu.Lock()
		defer observation.mu.Unlock()
		if observation.err != nil {
			return observation.err
		}
		observation.advanceResponses = append(observation.advanceResponses, response.GenerationJson)
		if !observation.lostAdvanceACK {
			observation.lostAdvanceACK = true
			return status.Error(codes.Unavailable, "acceptance loses exactly one committed generation advance ACK")
		}
		return nil
	}
	f.observeTerminal = func(_ context.Context, message proto.Message, _ any, err error) {
		request, ok := message.(*agentv1.ReportJobRunStatusRequest)
		if !ok || err != nil || len(request.Artifacts) == 0 {
			return
		}
		observation.mu.Lock()
		lose := request.JobRunId == observation.jobID && !observation.lostJobACK
		if lose {
			observation.lostJobACK = true
		}
		observation.mu.Unlock()
		if lose {
			f.mu.Lock()
			f.loseProducerACK = true
			f.mu.Unlock()
		}
	}
	f.mu.Unlock()
	defer func() {
		store.mu.Lock()
		store.afterUpload, store.beforeDownload = nil, nil
		store.mu.Unlock()
		f.mu.Lock()
		f.observeRequest, f.observeTerminal, f.afterGeneration = nil, nil, nil
		f.mu.Unlock()
	}()
	command := fmt.Sprintf(`set -eu
 test "$(pwd)" = /workspace
 test "$(ls /sys/class/net)" = lo
 awk '$2 == "/dev/shm" && $3 == "tmpfs" { found = 1 } END { exit !found }' /proc/mounts
 awk '$2 == "/tmp" { mounted = 1 } END { exit mounted }' /proc/mounts
 test "$(cat source.txt)" = 'initial checkout'
 test "$(cat seed/input.txt)" = 'upstream-input'
 cmp seed/input.txt .artifacts/retry-a/seed/input.txt
 case "$%s" in
 1)
   printf first > failed-workspace-marker
   printf first > /tmp/arcentra-retry-rootfs-marker
   printf first > /dev/shm/arcentra-retry-tmpfs-marker
   (while :; do printf 'tick\n' >> failed-background; sleep 0.05; done) </dev/null >/dev/null 2>&1 &
   while [ ! -s failed-background ]; do sleep 0.01; done
   printf 'retry-command-round-1\n'
   exit 9
   ;;
 2)
   test ! -e failed-workspace-marker
   test ! -e failed-background
   test ! -e /tmp/arcentra-retry-rootfs-marker
   test ! -e /dev/shm/arcentra-retry-tmpfs-marker
   mkdir proof
   printf 'retry-generation-2 %s upstream-input' > proof/result.txt
   printf 'retry-command-round-2\n'
   ;;
 *) exit 88 ;;
 esac`, taskpayload.RuntimeGenerationEnv, initialCommit)
	run := containerAcceptanceSubmit(t, f, cfg,
		&spec.Job{Name: "retry-a", Steps: []*spec.Step{controllerWorkerShell("produce", "mkdir seed; printf upstream-input > seed/input.txt"), containerAcceptanceArtifactStep("seed", "upload", map[string]any{"paths": []any{"seed/input.txt"}, "expireIn": "7d"})}},
		&spec.Job{Name: "retry-b", Source: &spec.Source{Type: "git", Repo: sourceURL, Branch: "main"}, DependsOn: []string{"retry-a"}, Retry: &spec.Retry{Mode: "whole_job", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}, Delay: "100ms"}, Steps: []*spec.Step{
			containerAcceptanceArtifactStep("fetch", "download", map[string]any{"job": "retry-a", "paths": []any{"seed/*"}}), controllerWorkerShell("retry-command", command), containerAcceptanceArtifactStep("publish", "upload", map[string]any{"paths": []any{"proof/result.txt"}, "expireIn": "7d"})}},
		&spec.Job{Name: "retry-c", DependsOn: []string{"retry-b"}, Steps: []*spec.Step{containerAcceptanceArtifactStep("fetch-proof", "download", map[string]any{"job": "retry-b", "paths": []any{"proof/*"}}), controllerWorkerShell("verify-proof", fmt.Sprintf("test \"$(cat proof/result.txt)\" = 'retry-generation-2 %s upstream-input'; cmp proof/result.txt .artifacts/retry-b/proof/result.txt; test ! -e failed-workspace-marker; printf 'retry-downstream-proof\\n'", initialCommit))}},
	)
	completed := f.waitRun(run.RunID, model.PipelineStatusSuccess)
	if completed.TotalJobs != 3 || completed.CompletedJobs != 3 || completed.FailedJobs != 0 {
		t.Fatalf("retry chain did not complete three original jobs: %+v", completed)
	}
	containerAcceptanceDrainWithAudit(t, f, retainedAudit)
	jobs, err := f.repos.JobRun.ListByPipelineRunID(f.ctx, run.RunID)
	if err != nil || len(jobs) != 3 {
		t.Fatalf("retry created unexpected logical jobs: count=%d %v", len(jobs), err)
	}
	byName := map[string]*model.JobRun{}
	for _, job := range jobs {
		byName[job.JobName] = containerAcceptanceBoundJob(t, f, job, model.JobRunStatusSuccess)
		f.assertReceipt("job", job.JobRunID, job.ClaimID, job.TerminalReportDigest, run.RunID, job.JobRunID)
	}
	consumer := byName["retry-b"]
	if consumer == nil || consumer.CurrentGeneration != 2 || consumer.SourceResolvedCommit != initialCommit || initialCommit == changedCommit {
		t.Fatal("retry did not retain two generations of the original pinned logical job")
	}
	steps, count, err := f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: run.RunID, PageSize: 20})
	if err != nil || count != 10 {
		t.Fatalf("retry attempt plan changed: steps=%d %v", count, err)
	}
	var commandIDs []string
	for _, step := range steps {
		want := model.JobRunStatusSuccess
		if step.JobRunID == consumer.JobRunID && step.Attempt == 1 {
			if step.Name == "retry-command" {
				want = model.JobRunStatusFailed
				if step.ExitCode == nil || *step.ExitCode != 9 {
					t.Fatal("first command lost typed exit 9")
				}
			} else if step.Name == "publish" {
				want = 8
			}
		}
		if int(step.Status) != want || step.TerminalReportDigest == "" {
			t.Fatalf("retry step result not committed: %+v", step)
		}
		ownerJob := containerAcceptanceRetryJobByID(jobs, step.JobRunID)
		if ownerJob == nil {
			t.Fatal("retry step belongs to an unexpected logical job")
		}
		claim := agentidentity.StepClaimID(ownerJob.ClaimID, step.StepRunID)
		f.assertReceipt("step", step.StepRunID, claim, step.TerminalReportDigest, run.RunID, step.JobRunID)
		if step.JobRunID == consumer.JobRunID && step.Name == "retry-command" {
			commandIDs = append(commandIDs, step.StepRunID)
			if step.GenerationID == "" || step.GenerationBindingDigest == "" {
				t.Fatal("command receipt lost generation authority")
			}
		}
	}
	if len(commandIDs) != 2 {
		t.Fatalf("expected exactly two command attempts, got %d", len(commandIDs))
	}
	realStackWaitLogs(t, f, steps, map[string]string{"retry-command-round-1": "stdout", "retry-command-round-2": "stdout", "retry-downstream-proof": "stdout"})
	logs, err := f.repos.Log.ListByStepRunIDs(f.ctx, commandIDs, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	markers := map[string]int{}
	for _, row := range logs {
		line := strings.TrimSpace(row.Content)
		if row.Stream == "stdout" && (line == "retry-command-round-1" || line == "retry-command-round-2") {
			markers[line]++
		}
	}
	if markers["retry-command-round-1"] != 1 || markers["retry-command-round-2"] != 1 {
		t.Fatalf("ACK loss duplicated or lost real command execution: %v", markers)
	}
	var generations []model.JobRunGeneration
	if err := f.db.WithContext(f.ctx).Select("generation_id", "job_run_id", "number", "state", "generation_json", "admission_request", "admission_digest", "closure_request", "closure_digest", "next_generation_id").Where("job_run_id = ?", consumer.JobRunID).Order("number").Find(&generations).Error; err != nil || len(generations) != 2 {
		t.Fatalf("expected exactly two durable generations: %d %v", len(generations), err)
	}
	first, err := taskpayload.ParseJobGeneration(generations[0].GenerationJSON)
	if err != nil {
		t.Fatal(err)
	}
	second, err := taskpayload.ParseJobGeneration(generations[1].GenerationJSON)
	if err != nil {
		t.Fatal(err)
	}
	if first.RuntimeClaimID == second.RuntimeClaimID || first.GenerationID == second.GenerationID || first.OriginalClaimID != consumer.ClaimID || second.OriginalClaimID != consumer.ClaimID || second.InputManifestDigest != consumer.InputManifestDigest || generations[0].NextGenerationID != second.GenerationID || generations[0].ClosureDigest != generations[1].AdmissionDigest {
		t.Fatal("generation CAS did not retain original claim, exact inputs and unique runtime owners")
	}
	observation.mu.Lock()
	observationErr := observation.err
	observedReplay := observation.branchAdvanced && observation.lostAdvanceACK && observation.lostJobACK && len(observation.staged) == 2 && observation.actualHeads[1] == initialCommit && observation.actualHeads[2] == initialCommit && len(observation.advanceRequests) == 2 && len(observation.advanceResponses) == 2
	if observedReplay {
		observedReplay = observation.advanceRequests[0] == observation.advanceRequests[1] && observation.advanceResponses[0] == observation.advanceResponses[1]
	}
	commandClaimsValid := len(observation.commandClaims) == 2
	for _, calls := range observation.commandClaims {
		if calls != 1 {
			commandClaimsValid = false
		}
	}
	observedUploads := make(map[string]containerAcceptanceJournal, len(observation.uploads))
	for key, owner := range observation.uploads {
		observedUploads[key] = owner
	}
	observedDownloads := make(map[string]int, len(observation.downloads))
	for key, n := range observation.downloads {
		observedDownloads[key] = n
	}
	observation.mu.Unlock()
	if observationErr != nil {
		t.Fatal(observationErr)
	}
	if !observedReplay {
		t.Fatal("source mutation or exact committed-ACK replay was not observed")
	}
	if !commandClaimsValid {
		t.Fatal("ACK loss duplicated or lost a command activation")
	}
	rows, count, err := f.repos.Artifact.List(f.ctx, repo.ArtifactQuery{ProjectID: f.projectID, RunID: run.RunID, Limit: 20})
	if err != nil || count != 2 || len(observedUploads) != 2 {
		t.Fatalf("failed generation uploaded or winning generation repeated publication: %d %v", count, err)
	}
	winning := 0
	for _, row := range rows {
		owner, ok := observedUploads[row.StoragePath]
		if !ok {
			t.Fatal("registered artifact lacks actual observed upload")
		}
		want := []byte("upstream-input")
		downloads := 4
		if row.JobRunID == consumer.JobRunID {
			winning++
			want = []byte("retry-generation-2 " + initialCommit + " upstream-input")
			downloads = 2
			if owner.Record.Generation == nil || owner.Record.Generation.Number != 2 {
				t.Fatal("output came from failed generation")
			}
			foundWinner := false
			for _, step := range steps {
				if step.StepRunID == row.StepRunID && step.JobRunID == consumer.JobRunID && step.Attempt == 2 && step.Name == "publish" && step.GenerationID == second.GenerationID {
					foundWinner = true
				}
			}
			if !foundWinner {
				t.Fatal("winning artifact registration points at an old or unrelated attempt")
			}
		}
		if observedDownloads[row.StoragePath] != downloads || row.Size != int64(len(want)) || row.SHA256 != fmt.Sprintf("%x", sha256.Sum256(want)) {
			t.Fatal("retry transferred altered bytes or repeated/unexpected input rounds")
		}
		reader, err := store.S3Storage.DownloadStream(f.ctx, row.StoragePath)
		if err != nil {
			t.Fatal(err)
		}
		actual, readErr := io.ReadAll(io.LimitReader(reader, 1<<20))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || !bytes.Equal(actual, want) {
			t.Fatal("downstream proof differs from actual registered S3 bytes")
		}
		containerAcceptanceRemoved(t, f, owner)
	}
	if winning != 1 {
		t.Fatal("retry must publish exactly one winning artifact")
	}
	store.mu.Lock()
	uploads := store.sealedUploads - initialUploads
	store.mu.Unlock()
	if uploads != 2 {
		t.Fatalf("actual S3 upload count=%d want=2", uploads)
	}
	f.mu.Lock()
	lost := f.lostACKs - initialLostACKs
	replays := len(f.receipts["job/"+consumer.JobRunID])
	f.mu.Unlock()
	if lost != 1 || replays < 2 {
		t.Fatal("winning final job ACK loss was not replayed")
	}
	assertControllerQueueCommitted(t, f, 17)
	entries, err := os.ReadDir(f.conf.Agent.Sandbox.Containerd.JournalDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "owner.json" && entry.Name() != ".lock" {
			t.Fatalf("retry journal evidence did not finish final ACK: %s", entry.Name())
		}
	}
	t.Log("real Process A -> retry B -> C: exactly two command generations, fresh workspace/rootfs/tmpfs, stopped background writer, exact cleanup-before-admission, immutable full source SHA/artifact metadata despite post-round-1 branch advance, identical lost advance ACK replay, one winning upload, final ACK replay and exact cleanup verified")
}

func containerAcceptanceRetryJobByID(jobs []*model.JobRun, jobID string) *model.JobRun {
	for _, job := range jobs {
		if job.JobRunID == jobID {
			return job
		}
	}
	return nil
}

func testContainerAcceptanceAdmissionObserver(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	document := containerAcceptanceAdmission{Version: 2, ControllerID: "controller-test", AgentID: "agent-test", WorkspaceRoot: filepath.Join(root, "workspaces"), Namespace: "acceptance-test"}
	request := taskpayload.JobGenerationTransition{Version: 1, Operation: "admit", Identity: taskpayload.JobInputIdentity{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", AgentID: document.AgentID, ClaimID: "claim", RuntimeBindingDigest: "v1:" + strings.Repeat("a", 64)}, RetryPlanDigest: "v1:" + strings.Repeat("b", 64)}
	request.RequestDigest, _ = request.Digest()
	document.Admission.Request = request
	owner, err := sonic.Marshal(map[string]string{"controllerID": document.ControllerID, "agentID": document.AgentID, "workspaceRoot": document.WorkspaceRoot, "namespace": document.Namespace})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "owner.json"), owner, 0600); err != nil {
		t.Fatal(err)
	}
	encode := func(d containerAcceptanceAdmission) []byte {
		d.Checksum = ""
		body, err := sonic.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		d.Checksum = hex.EncodeToString(sum[:])
		data, err := sonic.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	identity, _ := sonic.Marshal(request.Identity)
	name := "admission-" + uuid.NewSHA1(uuid.NameSpaceOID, identity).String() + ".json"
	valid := encode(document)
	if err := validateContainerAcceptanceAdmission(root, name, valid); err != nil {
		t.Fatal("valid exact admission rejected", err)
	}
	foreign := document
	foreign.AgentID = "other-agent"
	foreignRequest := document
	foreignRequest.Admission.Request.Identity.ClaimID = "other-claim"
	foreignRequest.Admission.Request.RequestDigest, _ = foreignRequest.Admission.Request.Digest()
	wrongOperation := document
	wrongOperation.Admission.Request.Operation = "inspect"
	wrongOperation.Admission.Request.RequestDigest, _ = wrongOperation.Admission.Request.Digest()
	for _, tc := range []struct {
		name string
		file string
		data []byte
	}{
		{"foreign-owner", name, encode(foreign)},
		{"changed-request", name, encode(foreignRequest)},
		{"not-admission", name, encode(wrongOperation)},
		{"unknown-field", name, append(append([]byte(nil), valid[:len(valid)-1]...), []byte(`,"unknown":true}`)...)},
		{"wrong-filename", "admission-" + uuid.NewString() + ".json", valid},
		{"unexpected-resource-record", name, []byte(`{"record":{"id":"invented"}}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateContainerAcceptanceAdmission(root, tc.file, tc.data); err == nil {
				t.Fatal("ambiguous admission file was ignored")
			}
		})
	}
}
