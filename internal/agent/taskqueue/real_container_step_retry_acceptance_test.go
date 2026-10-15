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
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent/sandboxexec"
	controlconfig "github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/process"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/sandbox"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	ckafka "github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/namespaces"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// containerAcceptanceStepRuntime 只替换关闭的生产分配门禁，保留真实持久控制器和实时后端
// 普通构建没有此适配器；恢复、队列、处理器、收据和所有效果仍走实际实现
type containerAcceptanceStepRuntime struct{ *durableOfflineRuntime }

func (r *containerAcceptanceStepRuntime) AllocateStepRetryGeneration(ctx context.Context, payload *taskpayload.JobRunTaskPayload, claim string, generation taskpayload.JobGeneration) (offlineSession, error) {
	if os.Getenv("ARCENTRA_CONTAINERD_ACCEPTANCE") != "1" || os.Getenv("ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE") != "1" || taskpayload.FailedStepRuntimeAccepted {
		return nil, fmt.Errorf("closed-gate allocation adapter requires exact dedicated acceptance")
	}
	if r == nil || r.durableOfflineRuntime == nil {
		return nil, fmt.Errorf("acceptance runtime unavailable")
	}
	r.drainMu.Lock()
	defer r.drainMu.Unlock()
	if !r.Ready() || !r.allowFailedStepRetry || payload == nil || payload.Runtime == nil || payload.Runtime.Image != os.Getenv("ARCENTRA_SANDBOX_TEST_IMAGE") || payload.Retry == nil || payload.Retry.Mode != "failed_step" || generation.Number != 1 {
		return nil, fmt.Errorf("acceptance requires the exact live failed-step original generation")
	}
	return r.controller.AllocateStepRetryGeneration(ctx, sandboxexec.Options{
		JobRunID: payload.JobRunID, PipelineRunID: payload.PipelineRunID, ExecutionClaimID: claim,
		RuntimeBindingDigest: payload.Runtime.BindingDigest, Image: payload.Runtime.Image,
		Resources: &sandbox.Resources{CPU: payload.Runtime.Resources.CPU, Memory: payload.Runtime.Resources.Memory}, User: r.user,
		StepAttemptSlots: len(allJobAttemptIDs(payload)), StepRetryPlan: payload.RetryPlan,
	}, generation)
}

// 镜像独立步骤日志的完整版本化格式，绝不将未知 JSON 当作可忽略记录
type containerAcceptanceStepDocument struct {
	Version           int    `json:"version"`
	ControllerID      string `json:"controllerId"`
	AgentID           string `json:"agentId"`
	WorkspaceRoot     string `json:"workspaceRoot"`
	Namespace         string `json:"namespace"`
	RuntimeIOIdentity string `json:"runtimeIoIdentity,omitempty"`
	Attempt           struct {
		RecordID          string                            `json:"recordId"`
		Request           taskpayload.StepAttemptTransition `json:"request"`
		Admission         *taskpayload.StepAttemptAdmission `json:"admission,omitempty"`
		Observation       *taskpayload.StepAttemptAdmission `json:"observation,omitempty"`
		Consumed          bool                              `json:"consumed,omitempty"`
		FinalAcknowledged bool                              `json:"finalAcknowledged,omitempty"`
	} `json:"attempt"`
	Checksum string `json:"checksum"`
}

func validateContainerAcceptanceStepAttempt(root, name string, data []byte) (*containerAcceptanceStepDocument, error) {
	var d containerAcceptanceStepDocument
	if err := sonic.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	checksum := d.Checksum
	d.Checksum = ""
	body, err := sonic.Marshal(d)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	if checksum != hex.EncodeToString(sum[:]) {
		return nil, fmt.Errorf("step observer checksum mismatch")
	}
	d.Checksum = checksum
	canonical, err := sonic.Marshal(d)
	if err != nil || !bytes.Equal(data, canonical) {
		return nil, fmt.Errorf("unknown or noncanonical step document")
	}
	ownerData, err := os.ReadFile(filepath.Join(root, "owner.json"))
	if err != nil {
		return nil, err
	}
	var owner struct {
		ControllerID      string `json:"controllerID"`
		AgentID           string `json:"agentID"`
		WorkspaceRoot     string `json:"workspaceRoot"`
		Namespace         string `json:"namespace"`
		RuntimeIOIdentity string `json:"runtimeIOIdentity"`
	}
	if err := sonic.Unmarshal(ownerData, &owner); err != nil {
		return nil, err
	}
	a := d.Attempt
	if d.Version != 3 || d.ControllerID != owner.ControllerID || d.AgentID != owner.AgentID || d.WorkspaceRoot != owner.WorkspaceRoot || d.Namespace != owner.Namespace || d.RuntimeIOIdentity != owner.RuntimeIOIdentity || a.RecordID != a.Request.Scope.RuntimeClaimID || a.Request.Scope.Identity.AgentID != d.AgentID || a.Request.Operation == "inspect" {
		return nil, fmt.Errorf("step document changed original owner")
	}
	if err := a.Request.Validate(); err != nil {
		return nil, err
	}
	identity, _ := sonic.Marshal([]string{a.Request.Scope.Identity.ClaimID, a.Request.Scope.StepRunID})
	if name != "step-"+uuid.NewSHA1(uuid.NameSpaceOID, identity).String()+".json" {
		return nil, fmt.Errorf("step document filename changed")
	}
	for _, admission := range []*taskpayload.StepAttemptAdmission{a.Admission, a.Observation} {
		if admission != nil {
			if err := admission.ValidateTransition(a.Request); err != nil {
				return nil, err
			}
		}
	}
	if a.Consumed && a.Admission == nil {
		return nil, fmt.Errorf("consumed step lacks exact admission")
	}
	return &d, nil
}

func containerAcceptanceStepDocumentFor(root string, request taskpayload.StepAttemptTransition) (*containerAcceptanceStepDocument, error) {
	identity, _ := sonic.Marshal([]string{request.Scope.Identity.ClaimID, request.Scope.StepRunID})
	name := "step-" + uuid.NewSHA1(uuid.NameSpaceOID, identity).String() + ".json"
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		return nil, err
	}
	return validateContainerAcceptanceStepAttempt(root, name, data)
}

type containerAcceptanceStepRuntimeEvents struct {
	mu         sync.Mutex
	events     []sandbox.ContainerAcceptanceEvent
	loseResume string
	lostResume bool
}

func (o *containerAcceptanceStepRuntimeEvents) observer() *sandbox.ContainerAcceptanceObserver {
	return &sandbox.ContainerAcceptanceObserver{
		Observe: func(_ context.Context, event sandbox.ContainerAcceptanceEvent) {
			o.mu.Lock()
			defer o.mu.Unlock()
			o.events = append(o.events, event)
		},
		AfterResume: func(_ context.Context, event sandbox.ContainerAcceptanceEvent) error {
			o.mu.Lock()
			defer o.mu.Unlock()
			if event.Err == nil && event.Resource.ID == o.loseResume && !o.lostResume {
				o.lostResume = true
				return status.Error(codes.Unavailable, "acceptance lost actual Resume acknowledgement")
			}
			return nil
		},
	}
}
func (o *containerAcceptanceStepRuntimeEvents) counts(ref sandbox.ResourceRef) (map[string]int, uint32, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	counts := map[string]int{}
	var pid uint32
	for _, event := range o.events {
		if event.Resource != ref {
			continue
		}
		counts[event.Operation]++
		if event.Err != nil {
			return nil, 0, event.Err
		}
		if event.PID != 0 {
			if pid != 0 && event.PID != pid {
				return nil, 0, fmt.Errorf("actual runtime replaced pinned init")
			}
			pid = event.PID
		}
	}
	return counts, pid, nil
}

// containerAcceptanceFailedStepRetry 复用原有八分钟闭环、数据库、Kafka、S3 与专用 daemon
// 已接受的普通和整作业路径先完成；只为关闭门禁的候选重建测试组装，不扩展生产开关
func containerAcceptanceFailedStepRetry(t *testing.T, f *controllerWorkerFixture, cfg *realContainerSettings, store *containerAcceptanceStorage, publisher executor.LogPublisher, retained map[string]string) {
	t.Helper()
	containerAcceptanceDrainWithAudit(t, f, retained)
	if err := f.worker.Stop(); err != nil {
		t.Fatal(err)
	}
	f.engine.Stop()
	policy := f.conf.Agent.Sandbox.Containerd.Policy.Clone()
	policy.AllowFailedStepRetry = true
	f.conf.Agent.Sandbox.Containerd.Policy = policy
	var processConfig *controlconfig.AppConfig
	if taskpayload.FailedStepRuntimeAccepted {
		processConfig = &controlconfig.AppConfig{Pipeline: controlconfig.PipelineConfig{OfflineRuntime: policy}}
	}
	f.engine = process.NewProcess(f.repos, nil, &controllerWorkerRecordingQueue{TaskQueue: f.producer, fixture: f}, nil, &log.Logger{SugaredLogger: zap.NewNop().Sugar()}, processConfig, nil)
	t.Cleanup(f.engine.Stop)
	if !taskpayload.FailedStepRuntimeAccepted {
		if policy.ValidateProduction() == nil || ValidateExecutionConfig(f.conf) == nil {
			t.Fatal("failed-step production gate must remain closed")
		}
		if err := f.engine.ConfigureContainerAcceptancePolicy(policy); err != nil {
			t.Fatal(err)
		}
	}
	events := &containerAcceptanceStepRuntimeEvents{}
	manager := executor.NewExecutorManager()
	manager.Register(executor.NewShellExecutor())
	manager.SetLogPublisher(publisher)
	queue, err := startContainerAcceptanceWorkerObserved(f.ctx, f.conf, f.client, manager, events.observer())
	if err != nil {
		t.Fatal(err)
	}
	f.worker = queue.(*workerQueues)
	if !f.worker.OfflineRuntimeReady() || !taskpayload.FailedStepRuntimeAccepted && f.worker.FailedStepRetryReady() {
		t.Fatal("candidate readiness widened production advertisement")
	}
	if !t.Run("same-live-session-immutable-inputs-ack-loss", func(t *testing.T) { containerAcceptanceStepRetrySuccess(t, f, cfg, store, events, retained) }) {
		return
	}
	for _, scenario := range []string{"earlier-background-refused", "adopted-zombie-refused", "cancel-before-successor", "uncertain-resume-cleans"} {
		if !t.Run(scenario, func(t *testing.T) { containerAcceptanceStepRetryRefusal(t, f, cfg, store, events, retained, scenario) }) {
			return
		}
	}
	containerAcceptanceDrainWithAudit(t, f, retained)
	containerAcceptanceStepQueueDrained(t, f)
	entries, err := os.ReadDir(f.conf.Agent.Sandbox.Containerd.JournalDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "step-") && strings.HasSuffix(entry.Name(), ".json") {
			t.Fatal("consumed failed-step journal survived exact completion acknowledgement")
		}
	}
	t.Log("real failed-step candidate: original generation/session/PID/workspace, retained failed partial writes, exact ACK replays, immutable inputs/artifact provenance, background/zombie refusal, cancel-wins and uncertain Resume cleanup; production gate unchanged")
}

func containerAcceptanceStepSubmit(t *testing.T, f *controllerWorkerFixture, cfg *realContainerSettings, jobs ...*spec.Job) *model.PipelineRun {
	t.Helper()
	// The test advertises only to its authenticated synthetic Agent. Production
	// FailedStepRetryReady remains false until the separately reviewed gate opens.
	ctx, cancel := f.client.WithTimeoutAndAuth(f.ctx)
	response, err := f.client.AgentClient.Heartbeat(ctx, &agentv1.HeartbeatRequest{AgentId: f.conf.Agent.ID, Status: agentv1.AgentStatus_AGENT_STATUS_IDLE, Timestamp: time.Now().Unix(), RuntimeCapabilities: &agentv1.RuntimeCapabilities{ContainerdOfflineV1: f.worker.OfflineRuntimeReady(), ContainerdWholeJobRetryV1: true, ContainerdFailedStepRetryV1: true, ExecutionMode: taskpayload.RuntimeContainerdOfflineV1}})
	cancel()
	if err != nil || response == nil || !response.Success {
		t.Fatalf("candidate authenticated heartbeat: %v", err)
	}
	run := &model.PipelineRun{RunID: id.GetUild(), PipelineID: f.pipelineID, Status: model.PipelineStatusPending, Env: `{}`, TotalJobs: len(jobs)}
	if err := f.repos.Pipeline.CreateRun(f.ctx, run); err != nil {
		t.Fatal(err)
	}
	pipeline := &spec.Pipeline{Namespace: "container-failed-step-acceptance", Runtime: &spec.Runtime{Type: taskpayload.RuntimeContainerdOfflineV1, Image: cfg.image, Resources: &spec.Resources{CpuLimit: "500m", MemoryLimit: "64Mi"}}, Jobs: jobs}
	if err := f.engine.Submit(run, pipeline); err != nil {
		t.Fatalf("Process.Submit failed-step candidate: %v", err)
	}
	return run
}

func containerAcceptanceStepOwner(root string, scope taskpayload.StepAttemptIdentity) (containerAcceptanceJournal, error) {
	records, err := containerAcceptanceRecords(root)
	if err != nil {
		return containerAcceptanceJournal{}, err
	}
	for _, owner := range records {
		r := owner.Record
		if r.JobRunID != scope.Identity.JobRunID {
			continue
		}
		if r.Generation == nil || r.Generation.Number != 1 || r.Generation.GenerationID != scope.GenerationID || r.Generation.BindingDigest != scope.GenerationBindingDigest || r.Generation.RuntimeClaimID != scope.RuntimeClaimID || r.RunID != scope.Identity.PipelineRunID || r.Binding != scope.Identity.RuntimeBindingDigest {
			return containerAcceptanceJournal{}, fmt.Errorf("step owner changed fixed generation or runtime binding")
		}
		want := taskpayload.StepAttemptSession{SessionID: r.SessionID, SessionIncarnationNonce: r.StepSessionNonce, Namespace: r.Resource.Namespace, ContainerID: r.Resource.ID, SnapshotID: r.Resource.ID + "-snapshot", Snapshotter: r.Resource.Snapshotter, ResourceOwnerDigest: r.Resource.Owner, WorkspaceID: r.Directory, WorkspaceDevice: r.Device, WorkspaceInode: r.Inode}
		if scope.Session != want {
			return containerAcceptanceJournal{}, fmt.Errorf("step owner changed live session/workspace identity")
		}
		return owner, nil
	}
	return containerAcceptanceJournal{}, fmt.Errorf("exact failed-step owner missing")
}

func containerAcceptancePausedTask(ctx context.Context, cfg *realContainerSettings, ref sandbox.ResourceRef) (uint32, error) {
	client, err := containerd.New(cfg.socket)
	if err != nil {
		return 0, err
	}
	defer client.Close()
	ctx = namespaces.WithNamespace(ctx, cfg.namespace)
	container, err := client.LoadContainer(ctx, ref.ID)
	if err != nil {
		return 0, err
	}
	task, err := container.Task(ctx, nil)
	if err != nil {
		return 0, err
	}
	state, err := task.Status(ctx)
	if err != nil || state.Status != containerd.Paused {
		return 0, fmt.Errorf("successor CAS did not observe paused exact task: %+v %v", state, err)
	}
	pids, err := task.Pids(ctx)
	if err != nil || len(pids) != 1 || pids[0].Pid != task.Pid() {
		return 0, fmt.Errorf("successor CAS did not observe only original init: %+v %v", pids, err)
	}
	return task.Pid(), nil
}

func containerAcceptanceReadExact(workspace, name, want string) error {
	data, err := os.ReadFile(filepath.Join(workspace, name))
	if err != nil || string(data) != want {
		return fmt.Errorf("retained %s = %q, want %q: %v", name, data, want, err)
	}
	return nil
}

type containerAcceptanceStepObservation struct {
	mu                                           sync.Mutex
	err                                          error
	owner                                        containerAcceptanceJournal
	scope                                        taskpayload.StepAttemptIdentity
	manifest, digest                             string
	requests, responses                          []string
	uploads                                      map[string]containerAcceptanceJournal
	downloads                                    map[string]int
	lostFailure, lostCAS, lostJob, advanced      bool
	pausedPID                                    uint32
	failedReceiptDeliveries                      int
	secondTerminalSeen, failedTwice, proofExists bool
	successorStepID                              string
}

func (o *containerAcceptanceStepObservation) fail(err error) {
	if err != nil {
		o.mu.Lock()
		if o.err == nil {
			o.err = err
		}
		o.mu.Unlock()
	}
}

func (o *containerAcceptanceStepObservation) failure() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.err
}

func containerAcceptanceStepPinnedInputs(job *model.JobRun, scope taskpayload.StepAttemptIdentity, commit string) error {
	if job == nil {
		return fmt.Errorf("pinned job is missing")
	}
	identity := taskpayload.JobInputIdentity{PipelineID: job.PipelineID, PipelineRunID: job.PipelineRunID, JobRunID: job.JobRunID, AgentID: job.AgentID, ClaimID: job.ClaimID, RuntimeBindingDigest: job.RuntimeBindingDigest, SourceBindingDigest: job.SourceBindingDigest}
	if identity != scope.Identity || job.InputManifestState != "pinned" {
		return fmt.Errorf("original input identity or pinned state changed")
	}
	manifest, err := taskpayload.ParseJobInputManifest(job.InputManifest)
	if err != nil {
		return fmt.Errorf("parse canonical pinned inputs: %w", err)
	}
	digest, err := manifest.Digest(identity)
	if err != nil || digest != job.InputManifestDigest || digest != scope.InputManifestDigest || manifest.SourceCommit != commit {
		return fmt.Errorf("canonical input digest or original source pin changed: %v", err)
	}
	// Input pinning precedes all logical admissions. SourceResolvedCommit is
	// separate terminal provenance, populated only by the final job receipt.
	if job.SourceResolvedCommit != "" || job.SourceProvenanceState != "unknown" || model.IsJobRunTerminal(job.Status) {
		return fmt.Errorf("pre-execution pin was confused with terminal provenance: status=%d provenance=%q", job.Status, job.SourceProvenanceState)
	}
	return nil
}

func containerAcceptanceStepCommittedAdmission(row *model.StepRun, req *agentv1.ReconcileStepAttemptRequest, response *agentv1.ReconcileStepAttemptResponse) error {
	if req == nil || response == nil || row == nil {
		return fmt.Errorf("actual CAS request, response or committed row missing")
	}
	tr, err := taskpayload.ParseStepAttemptTransition(req.TransitionJson)
	if err != nil {
		return fmt.Errorf("parse canonical CAS request: %w", err)
	}
	a, err := taskpayload.ParseStepAttemptAdmission(response.AdmissionJson)
	if err != nil {
		return fmt.Errorf("parse canonical CAS admission: %w", err)
	}
	if err := a.ValidateTransition(*tr); err != nil {
		return fmt.Errorf("CAS admission binding: %w", err)
	}
	if !response.ExecutionAllowed || req.AgentId != tr.Scope.Identity.AgentID || row.StepRunID != tr.Scope.StepRunID || row.JobRunID != tr.Scope.Identity.JobRunID || row.PipelineRunID != tr.Scope.Identity.PipelineRunID {
		return fmt.Errorf("actual CAS permission or committed row identity changed")
	}
	// These columns are MEDIUMTEXT, not MySQL JSON. Preserve exact canonical
	// bytes as well as validated identities and the independent digest column.
	if row.AttemptAdmission != response.AdmissionJson || row.AttemptAdmissionRequest != req.TransitionJson || row.AttemptAdmissionRequestDigest != tr.RequestDigest {
		return fmt.Errorf("committed CAS bytes/digest differ: admission=%t request=%t digest=%t", row.AttemptAdmission == response.AdmissionJson, row.AttemptAdmissionRequest == req.TransitionJson, row.AttemptAdmissionRequestDigest == tr.RequestDigest)
	}
	return nil
}

// 回调仅记录错误，由当前子测试等待者报错，避免 RPC goroutine 或父测试调用 FailNow
func containerAcceptanceStepWait(ctx context.Context, failure func() error, ready func(context.Context) (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := failure(); err != nil {
			return fmt.Errorf("failed-step observer: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		pollCtx, pollCancel := context.WithTimeout(ctx, 2*time.Second)
		done, err := ready(pollCtx)
		pollCancel()
		if observed := failure(); observed != nil {
			return fmt.Errorf("failed-step observer: %w", observed)
		}
		if err != nil || done {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func containerAcceptanceStepWaitRun(t *testing.T, f *controllerWorkerFixture, runID string, wanted int, failure func() error, diagnostics ...func()) {
	t.Helper()
	err := containerAcceptanceStepWait(f.ctx, failure, func(ctx context.Context) (bool, error) {
		run, err := f.repos.Pipeline.GetRun(ctx, runID)
		if err != nil || run == nil {
			return false, fmt.Errorf("read pipeline %s: %v", runID, err)
		}
		if run.Status == wanted {
			return true, nil
		}
		if run.Status == model.PipelineStatusFailed || run.Status == model.PipelineStatusSuccess || run.Status == model.PipelineStatusCancelled {
			return false, fmt.Errorf("pipeline status=%d want=%d", run.Status, wanted)
		}
		return false, nil
	})
	if err == nil {
		return
	}
	for _, diagnostic := range diagnostics {
		diagnostic()
	}
	// Bounded synthetic-fixture diagnostics: redacted errors, no structured
	// payloads or arbitrary log content.
	ctx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
	defer cancel()
	diagnosticCtx := withTerminalSecrets(ctx, f.conf, nil, nil)
	jobs, queryErr := f.repos.JobRun.ListByPipelineRunID(ctx, runID)
	if queryErr != nil {
		t.Logf("failed-step diagnostic query: %v", queryErr)
	}
	for _, job := range jobs[:min(len(jobs), 6)] {
		t.Logf("failed-step job=%s status=%d generation=%d inputs=%s provenance=%s error=%s", job.JobRunID, job.Status, job.CurrentGeneration, job.InputManifestState, job.SourceProvenanceState, containerAcceptanceStepDiagnosticError(diagnosticCtx, job.ErrorMessage))
	}
	steps, _, queryErr := f.repos.StepRun.List(ctx, repo.StepRunFilter{PipelineRunID: runID, PageSize: 24})
	if queryErr != nil {
		t.Logf("failed-step attempt diagnostic query: %v", queryErr)
	}
	ids := make([]string, 0, len(steps))
	for _, step := range steps {
		exit := "none"
		if step.ExitCode != nil {
			exit = fmt.Sprint(*step.ExitCode)
		}
		t.Logf("failed-step attempt=%s index=%d number=%d status=%d exit=%s admitted=%t retryable=%t receipt=%t error=%s", step.StepRunID, step.StepIndex, step.Attempt, step.Status, exit, step.AttemptAdmission != "", step.RetryableShellExit, step.TerminalReportDigest != "", containerAcceptanceStepDiagnosticError(diagnosticCtx, step.ErrorMessage))
		ids = append(ids, step.StepRunID)
	}
	if len(ids) > 0 {
		logs, queryErr := f.repos.Log.ListByStepRunIDs(ctx, ids, 0, 1000)
		if queryErr != nil {
			t.Logf("failed-step marker diagnostic query: %v", queryErr)
		}
		counts, logErr := containerAcceptanceStepMarkerCounts(logs, f.conf.Agent.ID)
		if logErr != nil {
			t.Logf("failed-step marker reconstruction: %v", logErr)
		}
		for _, marker := range containerAcceptanceStepDiagnosticMarkers {
			t.Logf("failed-step synthetic marker=%s count=%d", marker, counts[marker])
		}
	}
	t.Fatalf("failed-step pipeline %s: %v", runID, err)
}

var containerAcceptanceStepDiagnosticMarkers = []string{
	"step-earlier-once", "step-retry-enter", "step-retry-state-checked", "step-failed-first",
	"step-retry-partials-checked", "step-failed-second", "step-downstream-proof",
}

var errContainerAcceptanceStepLogGap = errors.New("synthetic marker log positions are incomplete")

// 日志正文是任意 Write 分片；按各步骤行号重建流后只统计完整白名单行
func containerAcceptanceStepMarkerCounts(logs []*model.StepRunLog, agentID string) (map[string]int, error) {
	ordered := append([]*model.StepRunLog(nil), logs...)
	for _, entry := range ordered {
		if entry == nil || entry.StepRunID == "" || entry.LogID == "" || entry.AgentID != agentID || entry.LineNumber <= 0 {
			return nil, fmt.Errorf("synthetic log lost persisted identity/ordering")
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].StepRunID != ordered[j].StepRunID {
			return ordered[i].StepRunID < ordered[j].StepRunID
		}
		return ordered[i].LineNumber < ordered[j].LineNumber
	})
	streams := map[string]*strings.Builder{}
	lastLine := map[string]int32{}
	bytes := 0
	for _, entry := range ordered {
		if lastLine[entry.StepRunID] == entry.LineNumber {
			return nil, fmt.Errorf("synthetic log repeated an immutable line identity")
		}
		if lastLine[entry.StepRunID]+1 != entry.LineNumber {
			return nil, errContainerAcceptanceStepLogGap
		}
		lastLine[entry.StepRunID] = entry.LineNumber
		bytes += len(entry.Content)
		if bytes > 1024*1024 {
			return nil, fmt.Errorf("synthetic marker diagnostic exceeds bounded log size")
		}
		if entry.Stream == "stdout" {
			if streams[entry.StepRunID] == nil {
				streams[entry.StepRunID] = &strings.Builder{}
			}
			streams[entry.StepRunID].WriteString(entry.Content)
		}
	}
	counts := map[string]int{}
	for _, stream := range streams {
		lines := strings.Split(stream.String(), "\n")
		for _, line := range lines[:len(lines)-1] {
			for _, marker := range containerAcceptanceStepDiagnosticMarkers {
				if line == marker {
					counts[marker]++
				}
			}
		}
	}
	return counts, nil
}

// 只输出固定类别和摘要，不输出错误中的命令、环境、凭据或任意日志正文
func containerAcceptanceStepErrorSummary(message string) string {
	if message == "" {
		return "none"
	}
	lower := strings.ToLower(message)
	var classes []string
	for _, item := range []struct{ fragment, class string }{
		{"resume does not match exact failed step successor and command", "resume-command-binding"},
		{"processes other than", "non-init-process"}, {"child processes", "init-children"},
		{"pidfd", "init-pin"}, {"quiescence", "quiescence"}, {"resume", "resume"},
		{"boundary", "boundary"}, {"admission", "admission"}, {"consum", "consumption"},
		{"seal", "sealing"}, {"artifact", "artifact"}, {"upload", "upload"},
		{"snapshot", "snapshot"}, {"workspace", "workspace"}, {"journal", "journal"},
		{"log persistence", "log-persistence"}, {"exit code", "command-exit"},
		{"exec process", "exec-process"}, {"permission", "permission"}, {"not found", "not-found"},
		{"deadline", "deadline"}, {"timeout", "timeout"}, {"cancel", "cancel"},
		{"attempt not admitted", "unused-attempt"},
	} {
		if strings.Contains(lower, item.fragment) {
			classes = append(classes, item.class)
		}
	}
	if len(classes) == 0 {
		classes = append(classes, "unclassified")
	}
	return fmt.Sprintf("classes=%s bytes=%d sha256=%x", strings.Join(classes, ","), len(message), sha256.Sum256([]byte(message)))
}

// 仅供完全合成的本验收失败诊断；重用终态脱敏，限制长度并拒绝展开结构化载荷
func containerAcceptanceStepDiagnosticError(ctx context.Context, message string) string {
	summary := containerAcceptanceStepErrorSummary(message)
	if message == "" || len(message) > 8192 || strings.ContainsAny(message, "{}") {
		return summary
	}
	data, err := encodeSafeTerminalPayload(ctx, &agentv1.ReportStepRunStatusRequest{ErrorMessage: message})
	if err != nil {
		return summary + " detail=redaction-unavailable"
	}
	var redacted agentv1.ReportStepRunStatusRequest
	if proto.Unmarshal(data, &redacted) != nil {
		return summary + " detail=redaction-unavailable"
	}
	text := []rune(redacted.ErrorMessage)
	if len(text) > 768 {
		text = append(text[:768], []rune("[truncated]")...)
	}
	return summary + fmt.Sprintf(" detail=%q", string(text))
}

func containerAcceptanceStepRuntimeDiagnostic(t *testing.T, events *containerAcceptanceStepRuntimeEvents, o *containerAcceptanceStepObservation) {
	t.Helper()
	o.mu.Lock()
	owner := o.owner
	t.Logf("failed-step observer: failedReceipts=%d advanceRequests=%d advanceResponses=%d lostFailure=%t lostCAS=%t lostJob=%t sourceAdvanced=%t secondTerminalSeen=%t failedCountXX=%t proofExists=%t", o.failedReceiptDeliveries, len(o.requests), len(o.responses), o.lostFailure, o.lostCAS, o.lostJob, o.advanced, o.secondTerminalSeen, o.failedTwice, o.proofExists)
	o.mu.Unlock()
	if owner.Record == nil {
		return
	}
	events.mu.Lock()
	defer events.mu.Unlock()
	counts := map[string]int{}
	for _, event := range events.events {
		if event.Resource != owner.Record.Resource {
			continue
		}
		counts[event.Operation]++
		if event.Err != nil {
			t.Logf("failed-step SDK operation=%s error=%s", event.Operation, containerAcceptanceStepErrorSummary(event.Err.Error()))
		}
	}
	t.Logf("failed-step actual SDK counts=%v", counts)
}

func containerAcceptanceStepRetrySuccess(t *testing.T, f *controllerWorkerFixture, cfg *realContainerSettings, store *containerAcceptanceStorage, events *containerAcceptanceStepRuntimeEvents, retained map[string]string) {
	t.Helper()
	o := &containerAcceptanceStepObservation{uploads: map[string]containerAcceptanceJournal{}, downloads: map[string]int{}}
	sourceURL, initialCommit, changedCommit, advanceBranch := containerAcceptanceMutableRetrySource(t, f.ctx)
	store.mu.Lock()
	initialUploads := store.sealedUploads
	store.afterUpload = func(_ context.Context, key string, owner containerAcceptanceJournal) (resultErr error) {
		defer func() { o.fail(resultErr) }()
		o.mu.Lock()
		defer o.mu.Unlock()
		if _, exists := o.uploads[key]; exists {
			return fmt.Errorf("failed-step repeated S3 upload")
		}
		if owner.Record.Generation != nil && (owner.Record.Generation.Number != 1 || owner.Record.SessionID != o.scope.Session.SessionID || owner.Record.StepSessionNonce != o.scope.Session.SessionIncarnationNonce) {
			return fmt.Errorf("output replaced original live generation/session")
		}
		o.uploads[key] = owner
		return nil
	}
	store.beforeDownload = func(ctx context.Context, key string) (resultErr error) {
		defer func() { o.fail(resultErr) }()
		records, err := containerAcceptanceRecords(f.conf.Agent.Sandbox.Containerd.JournalDir)
		if err != nil {
			return err
		}
		var target containerAcceptanceJournal
		for _, record := range records {
			if record.Record.Phase == "allocated" {
				if target.Record != nil {
					return fmt.Errorf("ambiguous failed-step download owner")
				}
				target = record
			}
		}
		if target.Record == nil {
			return fmt.Errorf("input download must precede container startup")
		}
		job, err := f.repos.JobRun.GetByJobRunID(ctx, target.Record.JobRunID)
		if err != nil || job == nil || job.InputManifestState != "pinned" {
			return fmt.Errorf("download lacks persisted immutable inputs: %v", err)
		}
		manifest, err := taskpayload.ParseJobInputManifest(job.InputManifest)
		if err != nil {
			return err
		}
		if job.JobName == "step-consumer" && manifest.SourceCommit != initialCommit {
			return fmt.Errorf("input download changed original Git SHA")
		}
		o.mu.Lock()
		defer o.mu.Unlock()
		if _, ok := o.uploads[key]; !ok {
			return fmt.Errorf("download lacks observed registered upstream upload")
		}
		o.downloads[job.JobName+"/"+key]++
		return nil
	}
	store.mu.Unlock()
	f.mu.Lock()
	f.observeRequest = func(ctx context.Context, raw any) {
		req, ok := raw.(*agentv1.ReconcileStepAttemptRequest)
		if !ok {
			return
		}
		tr, err := taskpayload.ParseStepAttemptTransition(req.TransitionJson)
		if err != nil {
			o.fail(err)
			return
		}
		owner, err := containerAcceptanceStepOwner(f.conf.Agent.Sandbox.Containerd.JournalDir, tr.Scope)
		if err != nil {
			o.fail(err)
			return
		}
		document, err := containerAcceptanceStepDocumentFor(f.conf.Agent.Sandbox.Containerd.JournalDir, *tr)
		if err != nil || !reflect.DeepEqual(document.Attempt.Request, *tr) {
			o.fail(fmt.Errorf("CAS preceded exact durable intent: %v", err))
			return
		}
		job, err := f.repos.JobRun.GetByJobRunID(ctx, tr.Scope.Identity.JobRunID)
		if err != nil {
			o.fail(fmt.Errorf("read CAS input pin: %w", err))
			return
		}
		if err := containerAcceptanceStepPinnedInputs(job, tr.Scope, initialCommit); err != nil {
			o.fail(fmt.Errorf("observe %s step %s input pin: %w", tr.Operation, tr.Scope.StepRunID, err))
			return
		}
		o.mu.Lock()
		if o.owner.Record == nil {
			o.owner, o.scope, o.manifest, o.digest = owner, tr.Scope, job.InputManifest, job.InputManifestDigest
		}
		mismatch := o.scope.Session != tr.Scope.Session || o.scope.GenerationID != tr.Scope.GenerationID || o.scope.GenerationBindingDigest != tr.Scope.GenerationBindingDigest || o.scope.Identity != tr.Scope.Identity || o.manifest != job.InputManifest || o.digest != job.InputManifestDigest
		if tr.Operation == "advance" {
			if o.failedReceiptDeliveries != 2 {
				o.err = errors.Join(o.err, fmt.Errorf("advance preceded second committed failed terminal delivery: %d", o.failedReceiptDeliveries))
			}
			o.requests = append(o.requests, req.TransitionJson)
			o.successorStepID = tr.Scope.StepRunID
		}
		o.mu.Unlock()
		if mismatch {
			o.fail(fmt.Errorf("admission changed fixed owner, generation or input bytes"))
			return
		}
		if tr.Operation != "advance" {
			return
		}
		previous, err := f.repos.StepRun.GetByStepRunID(ctx, tr.PreviousStepRunID)
		if err != nil || previous == nil || previous.Status != model.JobRunStatusFailed || previous.TerminalReportDigest != tr.FailedStepReceiptDigest || tr.Boundary == nil || !previous.RetryableShellExit {
			o.fail(fmt.Errorf("successor CAS preceded exact acknowledged eligible failure: %v", err))
			return
		}
		pid, err := containerAcceptancePausedTask(ctx, cfg, owner.Record.Resource)
		if err != nil {
			o.fail(err)
			return
		}
		workspace := filepath.Join(f.conf.Agent.WorkspaceDir, owner.Record.Directory)
		for name, want := range map[string]string{"earlier-count": "x", "failed-count": "x", "failed-partial": "retained", "source.txt": "initial checkout", "seed/input.txt": "upstream-input"} {
			if err := containerAcceptanceReadExact(workspace, name, want); err != nil {
				o.fail(err)
				return
			}
		}
		head, err := containerAcceptanceRetryHead(ctx, workspace)
		if err != nil || head != initialCommit {
			o.fail(fmt.Errorf("failed boundary changed staged checkout: %s %v", head, err))
			return
		}
		o.mu.Lock()
		first := !o.advanced
		o.pausedPID = pid
		o.mu.Unlock()
		if first {
			if err := advanceBranch(ctx); err != nil {
				o.fail(err)
				return
			}
			o.mu.Lock()
			o.advanced = true
			o.mu.Unlock()
		}
	}
	f.afterStepAttempt = func(ctx context.Context, req *agentv1.ReconcileStepAttemptRequest, response *agentv1.ReconcileStepAttemptResponse, callErr error) error {
		// Unexpected service/observer failures must reach the subtest waiter;
		// only the deliberate committed advance ACK loss remains retryable.
		if callErr != nil {
			o.fail(fmt.Errorf("actual ReconcileStepAttempt: %w", callErr))
			return nil
		}
		tr, err := taskpayload.ParseStepAttemptTransition(req.TransitionJson)
		if err != nil {
			o.fail(fmt.Errorf("parse observed CAS request: %w", err))
			return o.failure()
		}
		row, err := f.repos.StepRun.GetByStepRunID(ctx, tr.Scope.StepRunID)
		if err == nil {
			err = containerAcceptanceStepCommittedAdmission(row, req, response)
		}
		if err != nil {
			o.fail(fmt.Errorf("observe %s step %s committed CAS: %w", tr.Operation, tr.Scope.StepRunID, err))
			return o.failure()
		}
		o.mu.Lock()
		defer o.mu.Unlock()
		if o.err != nil {
			return o.err
		}
		if tr.Operation != "advance" {
			return nil
		}
		o.responses = append(o.responses, response.AdmissionJson)
		if !o.lostCAS {
			o.lostCAS = true
			return status.Error(codes.Unavailable, "acceptance lost committed failed-step CAS ACK")
		}
		return nil
	}
	f.observeTerminal = func(_ context.Context, raw proto.Message, _ any, callErr error) {
		if callErr != nil {
			return
		}
		if req, ok := raw.(*agentv1.ReportStepRunStatusRequest); ok {
			o.mu.Lock()
			owner, successor := o.owner, o.successorStepID
			o.mu.Unlock()
			if owner.Record != nil && successor != "" && req.StepRunId == successor {
				workspace := filepath.Join(f.conf.Agent.WorkspaceDir, owner.Record.Directory)
				count, countErr := os.ReadFile(filepath.Join(workspace, "failed-count"))
				proof, proofErr := os.Stat(filepath.Join(workspace, "proof", "result.txt"))
				o.mu.Lock()
				o.secondTerminalSeen = true
				o.failedTwice = countErr == nil && string(count) == "xx"
				o.proofExists = proofErr == nil && proof.Mode().IsRegular()
				o.mu.Unlock()
			}
		}
		if req, ok := raw.(*agentv1.ReportStepRunStatusRequest); ok && req.RetryableShellExit {
			o.mu.Lock()
			o.failedReceiptDeliveries++
			lose := !o.lostFailure
			o.lostFailure = true
			o.mu.Unlock()
			if lose {
				f.mu.Lock()
				f.loseStepTerminalACK = true
				f.mu.Unlock()
			}
		}
		if req, ok := raw.(*agentv1.ReportJobRunStatusRequest); ok && len(req.Artifacts) > 0 {
			o.mu.Lock()
			lose := req.JobRunId == o.scope.Identity.JobRunID && !o.lostJob
			if lose {
				o.lostJob = true
			}
			o.mu.Unlock()
			if lose {
				f.mu.Lock()
				f.loseProducerACK = true
				f.mu.Unlock()
			}
		}
	}
	f.mu.Unlock()
	defer func() {
		store.mu.Lock()
		store.afterUpload, store.beforeDownload = nil, nil
		store.mu.Unlock()
		f.mu.Lock()
		f.observeRequest, f.observeTerminal, f.afterStepAttempt = nil, nil, nil
		f.mu.Unlock()
	}()
	first := `set -eu
 test "$(pwd)" = /workspace
 test "$(ls /sys/class/net)" = lo
 test "$(cat source.txt)" = 'initial checkout'
 test "$(cat seed/input.txt)" = upstream-input
 printf x >> earlier-count
 printf rootfs > /tmp/step-earlier
 printf tmpfs > /dev/shm/step-earlier
 readlink /proc/1/ns/pid > original-pid-namespace
 stat -c '%d:%i' /workspace > original-workspace-identity
 printf 'step-earlier-once\n'`
	want := "earlier=x;failed=xx;source=" + initialCommit + ";seed=upstream-input;partial=retained"
	retry := fmt.Sprintf(`set -eu
 printf 'step-retry-enter\n'
 test "$(cat earlier-count)" = x
 test "$(cat /tmp/step-earlier)" = rootfs
 test "$(cat /dev/shm/step-earlier)" = tmpfs
 test "$(readlink /proc/1/ns/pid)" = "$(cat original-pid-namespace)"
 test "$(stat -c '%%d:%%i' /workspace)" = "$(cat original-workspace-identity)"
 test "$(cat source.txt)" = 'initial checkout'
 cmp seed/input.txt .artifacts/step-seed/seed/input.txt
 printf 'step-retry-state-checked\n'
 printf x >> failed-count
 if test "$(cat failed-count)" = x; then
   printf retained > failed-partial
   printf failed-rootfs > /tmp/step-failed
   printf failed-tmpfs > /dev/shm/step-failed
   printf 'step-failed-first\n'
   exit 9
 fi
 test "$(cat failed-count)" = xx
 test "$(cat failed-partial)" = retained
 test "$(cat /tmp/step-failed)" = failed-rootfs
 test "$(cat /dev/shm/step-failed)" = failed-tmpfs
 printf 'step-retry-partials-checked\n'
 mkdir proof
 printf '%%s' '%s' > proof/result.txt
 printf 'step-failed-second\n'`, want)
	run := containerAcceptanceStepSubmit(t, f, cfg,
		&spec.Job{Name: "step-seed", Steps: []*spec.Step{controllerWorkerShell("seed", "mkdir seed; printf upstream-input > seed/input.txt"), containerAcceptanceArtifactStep("publish-seed", "upload", map[string]any{"paths": []any{"seed/input.txt"}})}},
		&spec.Job{Name: "step-consumer", DependsOn: []string{"step-seed"}, Source: &spec.Source{Type: "git", Repo: sourceURL, Branch: "main"}, Retry: &spec.Retry{Mode: "failed_step", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}, Delay: "100ms"}, Steps: []*spec.Step{
			containerAcceptanceArtifactStep("fetch", "download", map[string]any{"job": "step-seed", "paths": []any{"seed/*"}}), controllerWorkerShell("earlier", first), controllerWorkerShell("retry-command", retry), containerAcceptanceArtifactStep("publish", "upload", map[string]any{"paths": []any{"proof/result.txt"}})}},
		&spec.Job{Name: "step-downstream", DependsOn: []string{"step-consumer"}, Steps: []*spec.Step{containerAcceptanceArtifactStep("fetch-proof", "download", map[string]any{"job": "step-consumer", "paths": []any{"proof/*"}}), controllerWorkerShell("verify-proof", "test \"$(cat proof/result.txt)\" = '"+want+"'; cmp proof/result.txt .artifacts/step-consumer/proof/result.txt; printf 'step-downstream-proof\\n'")}},
	)
	containerAcceptanceStepWaitRun(t, f, run.RunID, model.PipelineStatusSuccess, o.failure, func() { containerAcceptanceStepRuntimeDiagnostic(t, events, o) })
	containerAcceptanceDrainWithAudit(t, f, retained)
	o.mu.Lock()
	observedErr := o.err
	owner, scope, pausedPID := o.owner, o.scope, o.pausedPID
	ackProof := o.lostFailure && o.lostCAS && o.lostJob && o.advanced && len(o.requests) == 2 && len(o.responses) == 2 && o.requests[0] == o.requests[1] && o.responses[0] == o.responses[1]
	o.mu.Unlock()
	if observedErr != nil {
		t.Fatal(observedErr)
	}
	if !ackProof || initialCommit == changedCommit {
		t.Fatal("exact terminal/CAS replay or mutable-source challenge was not observed")
	}
	consumer, err := f.repos.JobRun.GetByJobRunID(f.ctx, scope.Identity.JobRunID)
	if err != nil || consumer == nil || consumer.CurrentGeneration != 1 || consumer.CurrentGenerationID != scope.GenerationID || consumer.SourceResolvedCommit != initialCommit || consumer.InputManifest != o.manifest || consumer.InputManifestDigest != o.digest {
		t.Fatalf("failed-step changed pinned job provenance: %+v %v", consumer, err)
	}
	containerAcceptanceBoundJob(t, f, consumer, model.JobRunStatusSuccess)
	var generations []model.JobRunGeneration
	if err := f.db.WithContext(f.ctx).Select("generation_id", "number", "generation_json", "next_generation_id").Where("job_run_id = ?", consumer.JobRunID).Find(&generations).Error; err != nil || len(generations) != 1 || generations[0].Number != 1 || generations[0].NextGenerationID != "" {
		t.Fatalf("step retry created another job generation: %+v %v", generations, err)
	}
	steps, count, err := f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: run.RunID, PageSize: 100})
	if err != nil || count != 12 {
		t.Fatalf("failed-step immutable attempt slots=%d: %v", count, err)
	}
	var publication, failedAttempt *model.StepRun
	admitted, failed := 0, 0
	for _, step := range steps {
		if step.JobRunID != consumer.JobRunID {
			continue
		}
		if step.TerminalReportDigest == "" {
			t.Fatal("attempt lost durable terminal receipt")
		}
		f.assertReceipt("step", step.StepRunID, taskpayload.StepAttemptClaimID(consumer.ClaimID, step.StepRunID), step.TerminalReportDigest, run.RunID, consumer.JobRunID)
		if step.AttemptAdmission == "" {
			if step.Status != model.JobRunStatusSkipped || step.Attempt != 2 {
				t.Fatal("unused future slot became current or successful")
			}
			continue
		}
		admitted++
		a, err := taskpayload.ParseStepAttemptAdmission(step.AttemptAdmission)
		if err != nil || a.Scope.Session != scope.Session || a.Scope.GenerationID != scope.GenerationID || a.Scope.InputManifestDigest != scope.InputManifestDigest {
			t.Fatalf("persisted attempt changed original session: %v", err)
		}
		if step.Name == "retry-command" && step.Attempt == 1 {
			failedAttempt = &step
			failed++
			if step.Status != model.JobRunStatusFailed || step.ExitCode == nil || *step.ExitCode != 9 || !step.RetryableShellExit {
				t.Fatal("allowed first failure lost typed provenance")
			}
		} else if step.Status != model.JobRunStatusSuccess {
			t.Fatalf("admitted attempt failed: %+v", step)
		}
		if step.Name == "publish" {
			if step.Attempt != 1 {
				t.Fatal("publication selected unused future slot")
			}
			publication = &step
		}
	}
	if admitted != 5 || failed != 1 || publication == nil {
		t.Fatalf("expected 5 admitted attempts and one eligible failure, got %d/%d", admitted, failed)
	}
	containerAcceptanceExactStepReplay(t, f, "step", failedAttempt.StepRunID, failedAttempt.ClaimID, failedAttempt.TerminalReportDigest, run.RunID, consumer.JobRunID)
	containerAcceptanceExactStepReplay(t, f, "job", consumer.JobRunID, consumer.ClaimID, consumer.TerminalReportDigest, run.RunID, consumer.JobRunID)
	counts, pid, err := events.counts(owner.Record.Resource)
	if err != nil || pid == 0 || pid != pausedPID || counts["create"] != 1 || counts["task-start"] != 1 || counts["exec-create"] != 3 || counts["exec-start"] != 3 || counts["exec-delete"] != 3 || counts["pause"] != 1 || counts["resume"] != 1 {
		t.Fatalf("actual runtime duplicated effects or changed init: %v pid=%d paused=%d err=%v", counts, pid, pausedPID, err)
	}
	var ids []string
	for _, step := range steps {
		ids = append(ids, step.StepRunID)
	}
	wantedMarkers := map[string]int{"step-earlier-once": 1, "step-failed-first": 1, "step-failed-second": 1, "step-downstream-proof": 1, "step-retry-enter": 2, "step-retry-state-checked": 2, "step-retry-partials-checked": 1}
	if err := containerAcceptanceStepWait(f.ctx, o.failure, func(ctx context.Context) (bool, error) {
		logs, err := f.repos.Log.ListByStepRunIDs(ctx, ids, 0, 1000)
		if err != nil {
			return false, err
		}
		markers, err := containerAcceptanceStepMarkerCounts(logs, f.conf.Agent.ID)
		if errors.Is(err, errContainerAcceptanceStepLogGap) {
			return false, nil // Kafka log persistence may still be catching up.
		}
		if err != nil {
			return false, err
		}
		complete := true
		for marker, want := range wantedMarkers {
			if markers[marker] > want {
				return false, fmt.Errorf("command output %s repeated: %d want=%d", marker, markers[marker], want)
			}
			complete = complete && markers[marker] == want
		}
		return complete, nil
	}); err != nil {
		t.Fatalf("exact synthetic command log counts: %v", err)
	}
	rows, total, err := f.repos.Artifact.List(f.ctx, repo.ArtifactQuery{ProjectID: f.projectID, RunID: run.RunID, Limit: 20})
	if err != nil || total != 2 {
		t.Fatalf("failed-step artifact registrations=%d: %v", total, err)
	}
	for _, row := range rows {
		expected := "upstream-input"
		o.mu.Lock()
		uploaded, ok := o.uploads[row.StoragePath]
		consumerDownloads, downstreamDownloads := o.downloads["step-consumer/"+row.StoragePath], o.downloads["step-downstream/"+row.StoragePath]
		o.mu.Unlock()
		if !ok || uploaded.Record.JobRunID != row.JobRunID {
			t.Fatal("artifact registration lacks exact observed producer")
		}
		if row.JobRunID == consumer.JobRunID {
			expected = want
			if row.StepRunID != publication.StepRunID || uploaded.Record.SessionID != scope.Session.SessionID || consumerDownloads != 0 || downstreamDownloads != 2 {
				t.Fatal("artifact used unused retry slot, wrong session or repeated download")
			}
		} else if consumerDownloads != 2 || downstreamDownloads != 1 {
			t.Fatal("retry re-fetched immutable seed or downstream input changed")
		}
		if row.Size != int64(len(expected)) || row.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(expected))) || row.StorageFingerprint == "" {
			t.Fatal("artifact integrity/provenance changed")
		}
		reader, err := store.S3Storage.DownloadStream(f.ctx, row.StoragePath)
		if err != nil {
			t.Fatal(err)
		}
		actual, readErr := io.ReadAll(io.LimitReader(reader, 1<<20))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || string(actual) != expected {
			t.Fatalf("actual S3 bytes changed: %v %v", readErr, closeErr)
		}
		containerAcceptanceRemoved(t, f, uploaded)
	}
	store.mu.Lock()
	uploads := store.sealedUploads - initialUploads
	store.mu.Unlock()
	if uploads != 2 {
		t.Fatalf("ACK loss duplicated S3 publication: %d", uploads)
	}
	f.mu.Lock()
	duplicate := f.tasks[consumer.JobRunID]
	f.mu.Unlock()
	if _, err := f.producer.Enqueue(&duplicate, nova.Queue(taskpayload.AgentQueue(f.conf.Agent.ID))); err != nil {
		t.Fatal(err)
	}
	// A subsequent real Kafka job is a FIFO barrier, making duplicate-delivery
	// absence observable rather than a timing-only sleep.
	barrier := containerAcceptanceStepSubmit(t, f, cfg, &spec.Job{Name: "step-after-duplicate", Steps: []*spec.Step{controllerWorkerShell("barrier", "true")}})
	containerAcceptanceStepWaitRun(t, f, barrier.RunID, model.PipelineStatusSuccess, o.failure)
	containerAcceptanceDrainWithAudit(t, f, retained)
	after, err := f.repos.JobRun.GetByJobRunID(f.ctx, consumer.JobRunID)
	if err != nil || !reflect.DeepEqual(consumer, after) {
		t.Fatal("duplicate Kafka delivery mutated completed step job")
	}
	afterCounts, _, err := events.counts(owner.Record.Resource)
	if err != nil || !reflect.DeepEqual(counts, afterCounts) {
		t.Fatal("duplicate Kafka delivery repeated an original runtime effect")
	}
	t.Log("earlier=1, failed-command=2, generation=1, session=1, init=1, Resume=1, publication=1; exact failed terminal/CAS/job ACK replay and duplicate Kafka delivery retained immutable source/input/artifact provenance")
}

func containerAcceptanceStepRetryRefusal(t *testing.T, f *controllerWorkerFixture, cfg *realContainerSettings, store *containerAcceptanceStorage, events *containerAcceptanceStepRuntimeEvents, retained map[string]string, scenario string) {
	t.Helper()
	containerAcceptanceDrainWithAudit(t, f, retained)
	var mu sync.Mutex
	var owner containerAcceptanceJournal
	var observationErr error
	var terminalWitness bool
	var advanceCount int
	committed := make(chan taskpayload.StepAttemptTransition, 1)
	fail := func(err error) {
		if err != nil {
			mu.Lock()
			if observationErr == nil {
				observationErr = err
			}
			mu.Unlock()
		}
	}
	failureErr := func() error {
		mu.Lock()
		defer mu.Unlock()
		return observationErr
	}
	store.mu.Lock()
	uploadsBefore := store.sealedUploads
	store.mu.Unlock()
	f.mu.Lock()
	f.observeRequest = func(_ context.Context, raw any) {
		req, ok := raw.(*agentv1.ReconcileStepAttemptRequest)
		if !ok {
			return
		}
		tr, err := taskpayload.ParseStepAttemptTransition(req.TransitionJson)
		if err != nil {
			fail(err)
			return
		}
		record, err := containerAcceptanceStepOwner(f.conf.Agent.Sandbox.Containerd.JournalDir, tr.Scope)
		if err != nil {
			fail(err)
			return
		}
		mu.Lock()
		if owner.Record == nil {
			owner = record
		}
		if owner.Record.Resource != record.Record.Resource || owner.Record.StepSessionNonce != record.Record.StepSessionNonce {
			observationErr = errors.Join(observationErr, fmt.Errorf("refusal scenario replaced original session"))
		}
		if tr.Operation == "advance" {
			advanceCount++
		}
		mu.Unlock()
		if scenario == "uncertain-resume-cleans" && tr.Operation == "advance" {
			events.mu.Lock()
			events.loseResume = record.Record.Resource.ID
			events.lostResume = false
			events.mu.Unlock()
		}
	}
	f.afterStepAttempt = func(_ context.Context, req *agentv1.ReconcileStepAttemptRequest, response *agentv1.ReconcileStepAttemptResponse, callErr error) error {
		if callErr != nil {
			fail(fmt.Errorf("actual refusal ReconcileStepAttempt: %w", callErr))
			return nil
		}
		if response == nil {
			fail(fmt.Errorf("actual refusal CAS returned no response"))
			return failureErr()
		}
		tr, err := taskpayload.ParseStepAttemptTransition(req.TransitionJson)
		if err != nil {
			fail(err)
			return err
		}
		if scenario == "cancel-before-successor" && tr.Operation == "advance" {
			select {
			case committed <- *tr:
			default:
			}
		}
		return nil
	}
	f.observeTerminal = func(_ context.Context, raw proto.Message, _ any, callErr error) {
		req, ok := raw.(*agentv1.ReportStepRunStatusRequest)
		if !ok || callErr != nil || int32(req.Status) != int32(model.JobRunStatusFailed) {
			return
		}
		mu.Lock()
		record := owner
		mu.Unlock()
		if record.Record == nil {
			return
		}
		workspace := filepath.Join(f.conf.Agent.WorkspaceDir, record.Record.Directory)
		if err := containerAcceptanceReadExact(workspace, "failed-count", "x"); err != nil {
			fail(err)
			return
		}
		if err := containerAcceptanceReadExact(workspace, "earlier-count", "x"); err != nil {
			fail(err)
			return
		}
		for _, name := range []string{"successor-effect", "later-effect"} {
			if _, err := os.Stat(filepath.Join(workspace, name)); !errors.Is(err, os.ErrNotExist) {
				fail(fmt.Errorf("refused continuation wrote %s: %v", name, err))
				return
			}
		}
		if scenario == "adopted-zombie-refused" {
			if err := containerAcceptanceReadExact(workspace, "observed-zombie", "Z"); err != nil {
				fail(err)
				return
			}
		}
		if scenario == "earlier-background-refused" {
			before, err := os.ReadFile(filepath.Join(workspace, "background"))
			if err != nil || len(before) == 0 {
				fail(fmt.Errorf("earlier background never actually ran: %v", err))
				return
			}
			// This committed first-failure callback still owns the workspace;
			// runtime cleanup must already have stopped the real writer.
			timer := time.NewTimer(75 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-f.ctx.Done():
				fail(f.ctx.Err())
				return
			case <-timer.C:
			}
			after, err := os.ReadFile(filepath.Join(workspace, "background"))
			if err != nil || !bytes.Equal(before, after) {
				fail(fmt.Errorf("refusal cleanup left background writer running: %v", err))
				return
			}
		}
		mu.Lock()
		terminalWitness = true
		mu.Unlock()
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.observeRequest, f.observeTerminal, f.afterStepAttempt = nil, nil, nil
		f.mu.Unlock()
		events.mu.Lock()
		events.loseResume = ""
		events.mu.Unlock()
	}()
	first := "printf x >> earlier-count"
	failure := `printf x >> failed-count
 if test "$(cat failed-count)" != x; then printf forbidden > successor-effect; exit 0; fi
 exit 9`
	switch scenario {
	case "earlier-background-refused":
		first += `
 (while :; do printf x >> /workspace/background; sleep 0.02; done) </dev/null >/dev/null 2>&1 &
 while test ! -s background; do sleep 0.01; done`
	case "adopted-zombie-refused":
		first += `
 (sleep 0.05; exit 0) </dev/null >/dev/null 2>&1 &
 printf '%s' "$!" > zombie-pid`
		failure = `printf x >> failed-count
 pid=$(cat zombie-pid)
 for attempt in $(seq 1 100); do
   state=$(awk '{print $3}' /proc/$pid/stat)
   if test "$state" = Z; then printf Z > observed-zombie; printf 'failed-step-observed-zombie\n'; exit 9; fi
   sleep 0.01
 done
 exit 25`
	case "cancel-before-successor", "uncertain-resume-cleans":
	default:
		t.Fatalf("unknown refusal scenario %s", scenario)
	}
	delay := "100ms"
	if scenario == "cancel-before-successor" {
		delay = "5s"
	}
	job := &spec.Job{Name: scenario, Retry: &spec.Retry{Mode: "failed_step", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}, Delay: delay}, Steps: []*spec.Step{
		controllerWorkerShell("earlier", first), controllerWorkerShell("retry-command", failure), controllerWorkerShell("later", "printf forbidden > later-effect"), containerAcceptanceArtifactStep("publish", "upload", map[string]any{"paths": []any{"later-effect"}}),
	}}
	run := containerAcceptanceStepSubmit(t, f, cfg, job, containerAcceptanceDependent(scenario))
	wantRun, wantJob := model.PipelineStatusFailed, model.JobRunStatusFailed
	if scenario == "cancel-before-successor" {
		var tr taskpayload.StepAttemptTransition
		if err := containerAcceptanceStepWait(f.ctx, failureErr, func(context.Context) (bool, error) {
			select {
			case tr = <-committed:
				return true, nil
			default:
				return false, nil
			}
		}); err != nil {
			t.Fatalf("wait for committed successor before cancellation: %v", err)
		}
		if err := containerAcceptanceStepWait(f.ctx, failureErr, func(context.Context) (bool, error) {
			document, err := containerAcceptanceStepDocumentFor(f.conf.Agent.Sandbox.Containerd.JournalDir, tr)
			return err == nil && document.Attempt.Admission != nil && !document.Attempt.Consumed, err
		}); err != nil {
			t.Fatalf("successor admission saved before cancel-wins delay: %v", err)
		}
		if err := f.engine.CancelRun(run.RunID); err != nil {
			t.Fatal(err)
		}
		if err := containerAcceptanceStepWait(f.ctx, failureErr, func(context.Context) (bool, error) {
			_, ok := cancelledJobs.Load(tr.Scope.Identity.JobRunID)
			return ok, nil
		}); err != nil {
			t.Fatalf("actual Kafka control cancellation reaches Agent: %v", err)
		}
		wantRun, wantJob = model.PipelineStatusCancelled, model.JobRunStatusCancelled
	}
	containerAcceptanceStepWaitRun(t, f, run.RunID, wantRun, failureErr)
	containerAcceptanceDrainWithAudit(t, f, retained)
	mu.Lock()
	record, actualErr, witness, advances := owner, observationErr, terminalWitness, advanceCount
	mu.Unlock()
	if actualErr != nil {
		t.Fatal(actualErr)
	}
	if record.Record == nil || !witness {
		t.Fatal("refusal did not witness the actual first failed command and preserved workspace")
	}
	completed, err := f.repos.JobRun.GetByJobRunID(f.ctx, record.Record.JobRunID)
	if err != nil {
		t.Fatal(err)
	}
	containerAcceptanceBoundJob(t, f, completed, wantJob)
	containerAcceptanceBlockedDownstream(t, f, completed)
	containerAcceptanceRemoved(t, f, record)
	counts, pid, err := events.counts(record.Record.Resource)
	wantResume, wantAdvance := 0, 0
	if scenario == "cancel-before-successor" {
		wantAdvance = 1
	}
	if scenario == "uncertain-resume-cleans" {
		wantResume, wantAdvance = 1, 1
	}
	if err != nil || pid == 0 || counts["create"] != 1 || counts["task-start"] != 1 || counts["exec-create"] != 2 || counts["exec-start"] != 2 || counts["exec-delete"] != 2 || counts["pause"] != 1 || counts["resume"] != wantResume || advances != wantAdvance {
		t.Fatalf("refused continuation repeated runtime effects: counts=%v advances=%d err=%v", counts, advances, err)
	}
	if scenario == "uncertain-resume-cleans" {
		events.mu.Lock()
		lost := events.lostResume
		events.mu.Unlock()
		if !lost || !strings.Contains(strings.ToLower(completed.ErrorMessage), "resume") {
			t.Fatal("uncertain Resume effect was not surfaced and cleaned")
		}
	}
	if scenario == "earlier-background-refused" && !strings.Contains(strings.ToLower(completed.ErrorMessage), "processes other than") {
		t.Fatalf("background refusal lost cause: %s", completed.ErrorMessage)
	}
	if scenario == "adopted-zombie-refused" && !strings.Contains(strings.ToLower(completed.ErrorMessage), "child processes") {
		t.Fatalf("zombie refusal lost independent child-list cause: %s", completed.ErrorMessage)
	}
	steps, _, err := f.repos.StepRun.List(f.ctx, repo.StepRunFilter{PipelineRunID: run.RunID, PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		if step.JobRunID != completed.JobRunID {
			continue
		}
		if step.TerminalReportDigest == "" {
			t.Fatal("refused job lost an attempt terminal acknowledgement")
		}
		if (step.Name == "later" || step.Name == "publish") && (step.Status == model.JobRunStatusSuccess || step.AttemptAdmission != "") {
			t.Fatal("refused continuation admitted later effect/publication")
		}
		if step.Name == "retry-command" && step.Attempt == 1 && (scenario == "earlier-background-refused" || scenario == "adopted-zombie-refused") && step.RetryableShellExit {
			t.Fatal("unsafe boundary was marked eligible")
		}
	}
	rows, total, err := f.repos.Artifact.List(f.ctx, repo.ArtifactQuery{ProjectID: f.projectID, RunID: run.RunID, Limit: 20})
	store.mu.Lock()
	uploadsAfter := store.sealedUploads
	store.mu.Unlock()
	if err != nil || total != 0 || len(rows) != 0 || uploadsAfter != uploadsBefore {
		t.Fatal("refusal published an artifact")
	}
	t.Logf("%s: earlier=1, failed=1, successor-effects=0, Resume=%d, artifacts=0, exact owned cleanup complete", scenario, wantResume)
}

// TestRealContainerStepRetryAcceptanceGate 验证只读日志旁观器与候选适配器不会扩大生产门禁
// 普通 fake 测试不连接任何 daemon、数据库、消息队列或对象存储
func TestRealContainerStepRetryAcceptanceGate(t *testing.T) {
	t.Setenv("ARCENTRA_CONTAINERD_ACCEPTANCE", "")
	t.Setenv("ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE", "")
	if _, err := (&containerAcceptanceStepRuntime{}).AllocateStepRetryGeneration(context.Background(), nil, "", taskpayload.JobGeneration{}); err == nil {
		t.Fatal("candidate allocation escaped explicit acceptance gate")
	}
	if !taskpayload.FailedStepRuntimeAccepted && (&workerQueues{}).FailedStepRetryReady() {
		t.Fatal("candidate advertised production capability")
	}
	conf, payload, runtime, client := fakeStepRetryFixture(t, 1)
	runtime.executeStep = func(taskpayload.StepAttemptAdmission, *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
		return fakeStepExit(0)
	}
	if err := runFakeStepRetry(context.Background(), conf, payload, runtime, client); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 1 {
		t.Fatal("fake observer setup changed immutable admission")
	}
	request, err := taskpayload.ParseStepAttemptTransition(client.requests[0])
	if err != nil {
		t.Fatal(err)
	}
	admission := client.admitted[request.RequestDigest]
	t.Run("synthetic-marker-chunks-preserve-exact-counts", func(t *testing.T) {
		row := func(id, stream, content string, line int32) *model.StepRunLog {
			return &model.StepRunLog{LogID: fmt.Sprintf("%s-%d", id, line), AgentID: "agent", StepRunID: id, Stream: stream, LineNumber: line, Content: content}
		}
		logs := []*model.StepRunLog{
			row("second", "stdout", "step-retry-enter\nstep-failed-second\n", 1),
			row("first", "stdout", "try-enter\nstep-failed-first\n", 3),
			row("first", "stderr", "step-failed-second\n", 2),
			row("first", "stdout", "step-re", 1),
			row("foreign-fragment", "stdout", "step-failed-", 1),
			row("different-step", "stdout", "second\n", 1),
			row("not-exact", "stdout", "prefix step-failed-second\nstep-failed-second", 1),
		}
		counts, err := containerAcceptanceStepMarkerCounts(logs, "agent")
		if err != nil || counts["step-retry-enter"] != 2 || counts["step-failed-first"] != 1 || counts["step-failed-second"] != 1 {
			t.Fatalf("ordered split/coalesced markers changed exact counts: %v %v", counts, err)
		}
		logs = append(logs, row("second", "stdout", "step-failed-second\n", 2))
		counts, err = containerAcceptanceStepMarkerCounts(logs, "agent")
		if err != nil || counts["step-failed-second"] != 2 {
			t.Fatal("repeated execution marker was hidden")
		}
		logs = append(logs, row("second", "stdout", "step-failed-second\n", 2))
		if _, err := containerAcceptanceStepMarkerCounts(logs, "agent"); err == nil {
			t.Fatal("duplicate immutable log position accepted")
		}
		for _, incomplete := range [][]*model.StepRunLog{
			{row("gap", "stdout", "step-failed-", 1), row("gap", "stdout", "second\n", 3)},
			{row("missing-prefix", "stdout", "step-failed-second\n", 2)},
		} {
			if counts, err := containerAcceptanceStepMarkerCounts(incomplete, "agent"); !errors.Is(err, errContainerAcceptanceStepLogGap) || len(counts) != 0 {
				t.Fatal("missing log fragment fabricated a complete execution marker")
			}
		}
	})
	t.Run("bounded-redacted-failure-diagnostics", func(t *testing.T) {
		const secret = "synthetic-private-value"
		ctx := context.WithValue(context.Background(), terminalSecretsContextKey{}, []string{secret})
		message := "resume exact quiescence task: " + secret + " https://user:password@example.test/x?token=query-secret"
		detail := containerAcceptanceStepDiagnosticError(ctx, message)
		if !strings.Contains(detail, "resume exact quiescence task") || !strings.Contains(detail, "[REDACTED]") || strings.Contains(detail, secret) || strings.Contains(detail, "password") || strings.Contains(detail, "query-secret") {
			t.Fatalf("diagnostic lost cause or leaked synthetic credential: %s", detail)
		}
		for _, message := range []string{"{" + secret + "}", strings.Repeat("x", 9000) + secret} {
			if detail := containerAcceptanceStepDiagnosticError(ctx, message); strings.Contains(detail, secret) || strings.Contains(detail, "detail=") {
				t.Fatal("structured or oversized content expanded in diagnostics")
			}
		}
		if detail := containerAcceptanceStepDiagnosticError(ctx, strings.Repeat("x", 1000)); !strings.Contains(detail, "[truncated]") || len(detail) > 1000 {
			t.Fatal("diagnostic bound was lost")
		}
		if summary := containerAcceptanceStepErrorSummary("resume does not match exact failed step successor and command " + secret); !strings.Contains(summary, "resume-command-binding") || strings.Contains(summary, secret) {
			t.Fatal("runtime classification lost exact category or leaked content")
		}
	})
	t.Run("pinned-source-is-not-terminal-provenance", func(t *testing.T) {
		commit := strings.Repeat("a", 40)
		scope := request.Scope
		scope.Identity.SourceBindingDigest = "v1:" + strings.Repeat("b", 64)
		manifest, err := taskpayload.NewJobInputManifest(commit, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := manifest.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		digest, err := manifest.Digest(scope.Identity)
		if err != nil {
			t.Fatal(err)
		}
		scope.InputManifestDigest = digest
		i := scope.Identity
		job := model.JobRun{PipelineID: i.PipelineID, PipelineRunID: i.PipelineRunID, JobRunID: i.JobRunID, AgentID: i.AgentID, ClaimID: i.ClaimID, RuntimeBindingDigest: i.RuntimeBindingDigest, SourceBindingDigest: i.SourceBindingDigest, Status: model.JobRunStatusRunning, InputManifestState: "pinned", InputManifest: encoded, InputManifestDigest: digest, SourceProvenanceState: "unknown"}
		if err := containerAcceptanceStepPinnedInputs(&job, scope, commit); err != nil {
			t.Fatalf("valid pre-execution source pin rejected: %v", err)
		}
		for name, mutate := range map[string]func(*model.JobRun){
			"foreign-agent":        func(j *model.JobRun) { j.AgentID += "-foreign" },
			"foreign-source":       func(j *model.JobRun) { j.SourceBindingDigest = "v1:" + strings.Repeat("c", 64) },
			"unpinned":             func(j *model.JobRun) { j.InputManifestState = "unknown" },
			"digest":               func(j *model.JobRun) { j.InputManifestDigest = "v1:" + strings.Repeat("d", 64) },
			"noncanonical-inputs":  func(j *model.JobRun) { j.InputManifest += " " },
			"premature-provenance": func(j *model.JobRun) { j.SourceResolvedCommit, j.SourceProvenanceState = commit, "reported" },
			"terminal-job":         func(j *model.JobRun) { j.Status = model.JobRunStatusSuccess },
		} {
			t.Run(name, func(t *testing.T) {
				changed := job
				mutate(&changed)
				if err := containerAcceptanceStepPinnedInputs(&changed, scope, commit); err == nil {
					t.Fatal("changed input identity or lifecycle accepted")
				}
			})
		}
		if err := containerAcceptanceStepPinnedInputs(&job, scope, strings.Repeat("e", 40)); err == nil {
			t.Fatal("changed source pin accepted")
		}
	})
	t.Run("committed-cas-exact-bytes-and-digests", func(t *testing.T) {
		body, err := admission.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		req := &agentv1.ReconcileStepAttemptRequest{AgentId: request.Scope.Identity.AgentID, TransitionJson: client.requests[0]}
		response := &agentv1.ReconcileStepAttemptResponse{AdmissionJson: body, ExecutionAllowed: true}
		row := model.StepRun{StepRunID: request.Scope.StepRunID, JobRunID: request.Scope.Identity.JobRunID, PipelineRunID: request.Scope.Identity.PipelineRunID, AttemptAdmissionRequest: req.TransitionJson, AttemptAdmissionRequestDigest: request.RequestDigest, AttemptAdmission: body}
		if err := containerAcceptanceStepCommittedAdmission(&row, req, response); err != nil {
			t.Fatal(err)
		}
		for name, mutate := range map[string]func(*model.StepRun){
			"request-whitespace":  func(r *model.StepRun) { r.AttemptAdmissionRequest += " " },
			"response-whitespace": func(r *model.StepRun) { r.AttemptAdmission += " " },
			"request-digest":      func(r *model.StepRun) { r.AttemptAdmissionRequestDigest = "v1:" + strings.Repeat("f", 64) },
			"row-identity":        func(r *model.StepRun) { r.StepRunID += "-foreign" },
		} {
			t.Run(name, func(t *testing.T) {
				changed := row
				mutate(&changed)
				if err := containerAcceptanceStepCommittedAdmission(&changed, req, response); err == nil {
					t.Fatal("committed bytes, digest or identity mismatch accepted")
				}
			})
		}
		response.ExecutionAllowed = false
		if err := containerAcceptanceStepCommittedAdmission(&row, req, response); err == nil {
			t.Fatal("observation mistaken for permission")
		}
	})
	t.Run("observer-fails-before-terminal-wait", func(t *testing.T) {
		observer := &containerAcceptanceStepObservation{}
		cause := errors.New("synthetic precise observer failure")
		observer.fail(cause)
		observer.fail(errors.New("later error must not replace the first cause"))
		polls := 0
		err := containerAcceptanceStepWait(context.Background(), observer.failure, func(context.Context) (bool, error) {
			polls++
			return false, nil
		})
		if !errors.Is(err, cause) || polls != 0 {
			t.Fatalf("observer failure hidden by terminal polling: polls=%d err=%v", polls, err)
		}
		observer = &containerAcceptanceStepObservation{}
		err = containerAcceptanceStepWait(context.Background(), observer.failure, func(context.Context) (bool, error) {
			observer.fail(cause)
			return true, nil
		})
		if !errors.Is(err, cause) {
			t.Fatalf("terminal success hid a concurrent observer failure: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		observer = &containerAcceptanceStepObservation{}
		if err := containerAcceptanceStepWait(ctx, observer.failure, func(context.Context) (bool, error) { return false, nil }); !errors.Is(err, context.Canceled) {
			t.Fatalf("observer wait lost cancellation bound: %v", err)
		}
	})
	root := t.TempDir()
	var original containerAcceptanceStepDocument
	original.Version, original.ControllerID, original.AgentID, original.WorkspaceRoot, original.Namespace, original.RuntimeIOIdentity = 3, "synthetic-controller", payload.AgentID, "/synthetic-workspace", request.Scope.Session.Namespace, "synthetic-io"
	original.Attempt.RecordID, original.Attempt.Request, original.Attempt.Admission, original.Attempt.Consumed = request.Scope.RuntimeClaimID, *request, &admission, true
	owner, err := sonic.Marshal(map[string]any{"controllerID": original.ControllerID, "agentID": original.AgentID, "workspaceRoot": original.WorkspaceRoot, "namespace": original.Namespace, "runtimeIOIdentity": original.RuntimeIOIdentity})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "owner.json"), owner, 0600); err != nil {
		t.Fatal(err)
	}
	identity, _ := sonic.Marshal([]string{request.Scope.Identity.ClaimID, request.Scope.StepRunID})
	name := "step-" + uuid.NewSHA1(uuid.NameSpaceOID, identity).String() + ".json"
	encode := func(d containerAcceptanceStepDocument) []byte {
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
	valid := encode(original)
	if _, err := validateContainerAcceptanceStepAttempt(root, name, valid); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*containerAcceptanceStepDocument)
	}{
		{"old-version", func(d *containerAcceptanceStepDocument) { d.Version = 2 }},
		{"foreign-agent", func(d *containerAcceptanceStepDocument) { d.AgentID = "foreign" }},
		{"foreign-controller", func(d *containerAcceptanceStepDocument) { d.ControllerID = "foreign" }},
		{"foreign-io", func(d *containerAcceptanceStepDocument) { d.RuntimeIOIdentity = "foreign" }},
		{"foreign-session", func(d *containerAcceptanceStepDocument) { d.Attempt.RecordID = uuid.NewString() }},
		{"consumed-without-admission", func(d *containerAcceptanceStepDocument) { d.Attempt.Admission = nil }},
		{"changed-request", func(d *containerAcceptanceStepDocument) { d.Attempt.Request.Scope.StepName = "foreign" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := original
			tc.change(&d)
			if _, err := validateContainerAcceptanceStepAttempt(root, name, encode(d)); err == nil {
				t.Fatal("observer accepted altered step journal")
			}
		})
	}
	if _, err := validateContainerAcceptanceStepAttempt(root, "step-foreign.json", valid); err == nil {
		t.Fatal("observer accepted foreign filename")
	}
	unknown := append(bytes.Clone(valid[:len(valid)-1]), []byte(`,"futurePermission":true}`)...)
	if _, err := validateContainerAcceptanceStepAttempt(root, name, unknown); err == nil {
		t.Fatal("unknown fields bypassed exact observer")
	}
	if err := os.WriteFile(filepath.Join(root, name), valid, 0600); err != nil {
		t.Fatal(err)
	}
	if records, err := containerAcceptanceRecords(root); err != nil || len(records) != 0 {
		t.Fatalf("known valid step document mistaken for workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, name), unknown, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := containerAcceptanceRecords(root); err == nil {
		t.Fatal("unknown step document silently ignored during actual acceptance")
	}
	ref := sandbox.ResourceRef{ID: "synthetic", Owner: "original"}
	events := &containerAcceptanceStepRuntimeEvents{events: []sandbox.ContainerAcceptanceEvent{{Operation: "task-start", Resource: ref, PID: 1}, {Operation: "exec-start", Resource: ref, PID: 2}}}
	if _, _, err := events.counts(ref); err == nil {
		t.Fatal("runtime event observer accepted replaced init")
	}
}

func containerAcceptanceExactStepReplay(t *testing.T, f *controllerWorkerFixture, kind, runID, claim, digest, pipelineRunID, jobRunID string) {
	t.Helper()
	f.mu.Lock()
	receipts := append([]controllerWorkerReceipt(nil), f.receipts[kind+"/"+runID]...)
	f.mu.Unlock()
	if len(receipts) != 2 || !bytes.Equal(receipts[0].payload, receipts[1].payload) || !proto.Equal(receipts[0].message, receipts[1].message) {
		t.Fatalf("lost %s ACK requires exactly two identical committed terminal messages, got %d", kind, len(receipts))
	}
	// Independently recomputes the original deterministic protobuf digest and
	// checks every replay's original claim, pipeline, run and job binding.
	f.assertReceipt(kind, runID, claim, digest, pipelineRunID, jobRunID)
}

// 比较真实代理分区的最终高水位，避免新增前置场景后固定低阈值误报已提交
func containerAcceptanceStepQueueDrained(t *testing.T, f *controllerWorkerFixture) {
	t.Helper()
	consumer, err := ckafka.NewConsumer(&ckafka.ConfigMap{"bootstrap.servers": f.conf.MessageQueue.Kafka.BootstrapServers, "group.id": "arcentra-agent-" + f.conf.Agent.ID, "enable.auto.commit": false})
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	topic := strings.ToUpper(taskpayload.AgentQueue(f.conf.Agent.ID))
	_, watermark, err := consumer.QueryWatermarkOffsets(topic, 0, 3000)
	if err != nil || watermark < 32 {
		t.Fatalf("final task watermark missing combined scenarios: offset=%d error=%v", watermark, err)
	}
	f.wait("every combined task offset committed", func() bool {
		positions, err := consumer.Committed([]ckafka.TopicPartition{{Topic: &topic, Partition: 0}}, 1000)
		return err == nil && len(positions) == 1 && positions[0].Error == nil && int64(positions[0].Offset) == watermark
	})
	_, finalWatermark, err := consumer.QueryWatermarkOffsets(topic, 0, 3000)
	if err != nil || finalWatermark != watermark {
		t.Fatal("task publication changed while checking final commit watermark")
	}
}
