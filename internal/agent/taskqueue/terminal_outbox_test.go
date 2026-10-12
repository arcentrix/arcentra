package taskqueue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/safe"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type terminalCaptureClient struct {
	agentv1.AgentServiceClient
	mu                sync.Mutex
	failStep, failJob bool
	calls             []terminalRecord
}

func (c *terminalCaptureClient) capture(ctx context.Context, kind, runID string, request proto.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	payload, _ := proto.MarshalOptions{Deterministic: true}.Marshal(request)
	md, _ := metadata.FromOutgoingContext(ctx)
	claims := md.Get(executionClaimMetadataKey)
	claim := ""
	if len(claims) > 0 {
		claim = claims[0]
	}
	c.calls = append(c.calls, terminalRecord{Kind: kind, RunID: runID, Payload: payload, ClaimID: claim})
	if kind == "step" && c.failStep || kind == "job" && c.failJob {
		return status.Error(codes.Unavailable, "controller unavailable")
	}
	return nil
}
func (c *terminalCaptureClient) ReportStepRunStatus(ctx context.Context, req *agentv1.ReportStepRunStatusRequest, _ ...grpc.CallOption) (*agentv1.ReportStepRunStatusResponse, error) {
	if req.Status >= 4 {
		if err := c.capture(ctx, "step", req.StepRunId, req); err != nil {
			return nil, err
		}
	}
	return &agentv1.ReportStepRunStatusResponse{Success: true}, nil
}
func (c *terminalCaptureClient) ReportJobRunStatus(ctx context.Context, req *agentv1.ReportJobRunStatusRequest, _ ...grpc.CallOption) (*agentv1.ReportJobRunStatusResponse, error) {
	if req.Status >= 4 {
		if err := c.capture(ctx, "job", req.JobRunId, req); err != nil {
			return nil, err
		}
	}
	return &agentv1.ReportJobRunStatusResponse{Success: true}, nil
}
func terminalTestRecord(t *testing.T, kind, runID string) terminalRecord {
	t.Helper()
	var request proto.Message
	if kind == "step" {
		request = &agentv1.ReportStepRunStatusRequest{AgentId: "agent", StepRunId: runID, Status: steprunv1.StepRunStatus_STEP_RUN_STATUS_SUCCESS, StartTime: 123, EndTime: 456, Metrics: map[string]string{"executor": "shell"}}
	} else {
		request = &agentv1.ReportJobRunStatusRequest{AgentId: "agent", JobRunId: runID, Status: 4, StartTime: 123, EndTime: 456}
	}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return terminalRecord{Kind: kind, RunID: runID, ClaimID: "93d60f5a-84ab-4a6c-83bf-3a353d14d0a6", Payload: payload}
}
func openTestTerminalOutbox(t *testing.T, path string) *terminalOutbox {
	t.Helper()
	outbox, err := newTerminalOutbox(context.Background(), path, "agent", "controller:9090")
	if err != nil {
		t.Fatalf("open terminal outbox: %v: %v", err, errors.Unwrap(err))
	}
	return outbox
}

func TestTerminalOutboxRestartsImmutableReportsInDependencyOrder(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "reports")
	outbox := openTestTerminalOutbox(t, directory)
	step, job := terminalTestRecord(t, "step", "step"), terminalTestRecord(t, "job", "job")
	job.Dependencies = []string{"step"}
	if err := outbox.put(ctx, step); err != nil {
		t.Fatal(err)
	}
	if err := outbox.put(ctx, job); err != nil {
		t.Fatal(err)
	}
	client := &terminalCaptureClient{failStep: true}
	wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
	if found, err := outbox.deliver(ctx, wrapper, "job", "job"); !found || status.Code(err) != codes.Unavailable {
		t.Fatalf("lost dependency failure: %v %v", found, err)
	}
	if len(client.calls) != 1 || client.calls[0].Kind != "step" {
		t.Fatal("job overtook failed dependency")
	}
	if err := outbox.close(); err != nil {
		t.Fatal(err)
	}
	outbox = openTestTerminalOutbox(t, directory)
	defer outbox.close()
	client.failStep = false
	if err := outbox.replay(ctx, wrapper); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 3 || client.calls[1].Kind != "step" || client.calls[2].Kind != "job" {
		t.Fatalf("unexpected replay: %+v", client.calls)
	}
	for i, want := range []terminalRecord{step, step, job} {
		got := client.calls[i]
		if string(got.Payload) != string(want.Payload) || got.ClaimID != want.ClaimID {
			t.Fatal("restart changed immutable request or claim")
		}
	}
	if outbox.has("job", "job") || outbox.has("step", "step") {
		t.Fatal("acknowledged record retained")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("unexpected acknowledged outbox contents: %v", entries)
	}
}

func TestTerminalOutboxIdentityLockAndImmutability(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "reports")
	outbox := openTestTerminalOutbox(t, directory)
	if duplicate, err := newTerminalOutbox(ctx, directory, "agent", "controller:9090"); err == nil {
		duplicate.close()
		t.Fatal("two writers acquired same directory")
	}
	record := terminalTestRecord(t, "step", "step")
	if err := outbox.put(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.ClaimID = "different-claim"
	if err := outbox.put(ctx, record); err == nil {
		t.Fatal("immutable claim was overwritten")
	}
	if err := outbox.close(); err != nil {
		t.Fatal(err)
	}
	for _, identity := range [][2]string{{"another-agent", "controller:9090"}, {"agent", "different-controller:9090"}} {
		changed, err := newTerminalOutbox(ctx, directory, identity[0], identity[1])
		if err == nil {
			changed.close()
			t.Fatal("changed identity adopted pending records")
		}
	}
	outbox = openTestTerminalOutbox(t, directory)
	defer outbox.close()
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("directory permissions: %v", info.Mode())
	}
	for _, name := range []string{"identity", ".lock", terminalRecordName("step", "step")} {
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("file permissions: %v", info.Mode())
		}
	}
}

func TestTerminalOutboxRejectsUnsafeFilesAndCorruption(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "corrupt", "unexpected", "missing_identity"} {
		t.Run(kind, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "reports")
			outbox := openTestTerminalOutbox(t, directory)
			record := terminalTestRecord(t, "step", "step")
			if err := outbox.put(context.Background(), record); err != nil {
				t.Fatal(err)
			}
			outbox.close()
			name := filepath.Join(directory, terminalRecordName("step", "step"))
			switch kind {
			case "symlink":
				os.Remove(name)
				if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), name); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(name, filepath.Join(t.TempDir(), "alias")); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := os.WriteFile(name, []byte(`{"payload":"private-invalid-content"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "unexpected":
				if err := os.WriteFile(filepath.Join(directory, "unexpected"), []byte("private-content"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing_identity":
				if err := os.Remove(filepath.Join(directory, "identity")); err != nil {
					t.Fatal(err)
				}
			}
			failed, err := newTerminalOutbox(context.Background(), directory, "agent", "controller:9090")
			if err == nil {
				failed.close()
				t.Fatal("unsafe outbox accepted")
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("record contents leaked through error")
			}
			if _, err := os.Lstat(name); err != nil {
				t.Fatalf("invalid committed record was removed: %v", err)
			}
		})
	}
	t.Run("parent_symlink", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Symlink(t.TempDir(), filepath.Join(root, "link")); err != nil {
			t.Fatal(err)
		}
		failed, err := newTerminalOutbox(context.Background(), filepath.Join(root, "link", "reports"), "agent", "controller:9090")
		if err == nil {
			failed.close()
			t.Fatal("symlink parent accepted")
		}
	})
}

func TestTerminalPersistenceFailurePreventsRPCAndQueueAck(t *testing.T) {
	for _, kind := range []string{"quota", "file_sync", "directory_sync"} {
		t.Run(kind, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "reports")
			outbox := openTestTerminalOutbox(t, directory)
			defer outbox.close()
			conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "agent"}, Grpc: config.GrpcConfig{ServerAddr: "controller:9090"}}
			ctx := withTerminalOutbox(context.Background(), outbox)
			client := &terminalCaptureClient{}
			wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
			if kind == "quota" {
				outbox.maxBytes = 1
			} else {
				outbox.syncFile = func(file *os.File) error {
					info, err := file.Stat()
					if err != nil {
						return err
					}
					if info.IsDir() == (kind == "directory_sync") {
						return errors.New("sensitive-path-and-secret")
					}
					return file.Sync()
				}
			}
			err := reportStepRunStatus(ctx, wrapper, conf, "failed-persist-"+kind, steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED, 1, "ordinary failure", time.Unix(123, 0), 456, nil)
			if err == nil || acknowledgeCompleted(err) == nil {
				t.Fatalf("persistence failure allowed ack: %v", err)
			}
			if strings.Contains(err.Error(), "sensitive") {
				t.Fatal("persistence error leaked detail")
			}
			if len(client.calls) != 0 {
				t.Fatal("terminal was sent before durable persistence")
			}
			if kind == "directory_sync" {
				if outbox.fatalErr == nil {
					t.Fatal("uncertain directory commit did not halt writes")
				}
				outbox.close()
				recovered := openTestTerminalOutbox(t, directory)
				defer recovered.close()
				if err := recovered.replay(context.Background(), wrapper); err != nil {
					t.Fatal(err)
				}
				if len(client.calls) != 1 {
					t.Fatal("uncertain committed record not replayed after restart")
				}
			}
		})
	}
}

func TestTerminalQuotaRetryDoesNotRepeatCommand(t *testing.T) {
	conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "agent", WorkspaceDir: t.TempDir()}, Grpc: config.GrpcConfig{ServerAddr: "controller:9090"}}
	outbox := openTestTerminalOutbox(t, filepath.Join(conf.Agent.WorkspaceDir, "reports"))
	defer outbox.close()
	outbox.maxBytes = 1
	ctx := withTerminalOutbox(context.Background(), outbox)
	client := &terminalCaptureClient{}
	wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
	payload := &taskpayload.JobRunTaskPayload{PipelineRunID: "run", JobRunID: "quota-job", Steps: []taskpayload.StepPayload{{StepRunID: "quota-step", Args: map[string]any{"run": "printf x >> executions"}}}}
	if err := executeJobRun(ctx, conf, wrapper, nil, payload); !errors.Is(err, errTerminalOutboxFull) || acknowledgeCompleted(err) == nil {
		t.Fatalf("quota failure not retained: %v", err)
	}
	if len(client.calls) != 0 {
		t.Fatal("quota rejection sent a terminal RPC")
	}
	outbox.maxBytes = terminalOutboxMaxBytes
	if err := executeJobRun(ctx, conf, wrapper, nil, payload); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(conf.Agent.WorkspaceDir, "run", "quota-job", "executions"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "x" {
		t.Fatalf("repeated command: %s", data)
	}
}

func TestTerminalReportRedactsBeforeDiskAndPreservesPayload(t *testing.T) {
	conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "agent"}, Grpc: config.GrpcConfig{ServerAddr: "controller:9090", Token: "private-grpc-token"}}
	outbox := openTestTerminalOutbox(t, filepath.Join(t.TempDir(), "reports"))
	defer outbox.close()
	payload := &taskpayload.StepRunTaskPayload{Env: map[string]string{"secrets.api": "private-api-value"}}
	ctx := withTerminalSecrets(withTerminalOutbox(context.Background(), outbox), conf, nil, payload)
	client := &terminalCaptureClient{failStep: true}
	wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
	err := reportStepRunStatus(ctx, wrapper, conf, "redacted-step", steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED, 7, "private-grpc-token private-api-value https://name:unknown-password@example.com/path?token=unknown-token", time.Unix(123, 0), 456, nil)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected retained delivery failure: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(outbox.root.Name(), terminalRecordName("step", "redacted-step")))
	if err != nil {
		t.Fatal(err)
	}
	var record terminalRecord
	if err := sonic.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	req := &agentv1.ReportStepRunStatusRequest{}
	if err := proto.Unmarshal(record.Payload, req); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-grpc-token", "private-api-value", "unknown-password", "unknown-token"} {
		if strings.Contains(string(record.Payload), secret) {
			t.Fatalf("persisted secret %q", secret)
		}
	}
	if req.ExitCode != 7 || req.StartTime != 123 || req.EndTime != 456 || req.Status != 5 {
		t.Fatal("redaction changed terminal status data")
	}
	if string(record.Payload) != string(client.calls[0].Payload) {
		t.Fatal("sent payload differs from stored payload")
	}
}

func TestTerminalReplayRetainsRejectedRecordsAndHonorsCancellation(t *testing.T) {
	outbox := openTestTerminalOutbox(t, filepath.Join(t.TempDir(), "reports"))
	defer outbox.close()
	if err := outbox.put(context.Background(), terminalTestRecord(t, "step", "step")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &terminalCaptureClient{}
	wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
	if err := outbox.replay(ctx, wrapper); !errors.Is(err, context.Canceled) {
		t.Fatalf("replay ignored cancellation: %v", err)
	}
	if len(client.calls) != 0 || !outbox.has("step", "step") {
		t.Fatal("cancelled replay changed record")
	}
	rejected := &terminalDeliveryError{status.Error(codes.FailedPrecondition, "recovered terminal has no receipt")}
	if acknowledgeCompleted(rejected) == nil {
		t.Fatal("terminal rejection acknowledged task")
	}
}

func TestAgentRejectsAutomaticQueueCommit(t *testing.T) {
	conf := &config.AgentConfig{Agent: config.AgentInfo{Mode: "baremetal"}}
	conf.TaskQueue.AutoCommit = true
	if err := ValidateExecutionConfig(conf); err == nil || !strings.Contains(err.Error(), "autoCommit") {
		t.Fatalf("automatic commit was accepted: %v", err)
	}
}

type rejectedTerminalClient struct {
	agentv1.AgentServiceClient
	code     codes.Code
	negative bool
}

func (c *rejectedTerminalClient) ReportStepRunStatus(context.Context, *agentv1.ReportStepRunStatusRequest, ...grpc.CallOption) (*agentv1.ReportStepRunStatusResponse, error) {
	if c.negative {
		return &agentv1.ReportStepRunStatusResponse{Success: false, Message: "untrusted-provider-secret"}, nil
	}
	if c.code == codes.OK {
		return nil, nil
	}
	return nil, status.Error(c.code, "untrusted-provider-secret")
}
func TestRejectedTerminalResponseRemainsDurable(t *testing.T) {
	for _, test := range []struct {
		name     string
		code     codes.Code
		negative bool
	}{{"permission", codes.PermissionDenied, false}, {"not_found", codes.NotFound, false}, {"precondition", codes.FailedPrecondition, false}, {"negative", codes.OK, true}, {"empty", codes.OK, false}} {
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "reports")
			outbox := openTestTerminalOutbox(t, directory)
			if err := outbox.put(context.Background(), terminalTestRecord(t, "step", "step")); err != nil {
				t.Fatal(err)
			}
			wrapper := &agentgrpc.ClientWrapper{AgentClient: &rejectedTerminalClient{code: test.code, negative: test.negative}}
			_, err := outbox.deliver(context.Background(), wrapper, "step", "step")
			delivery := &terminalDeliveryError{err}
			if err == nil || acknowledgeCompleted(delivery) == nil || !outbox.has("step", "step") {
				t.Fatalf("rejected receipt acknowledged: %v", err)
			}
			if strings.Contains(delivery.Error(), "untrusted-provider-secret") {
				t.Fatal("remote diagnostic leaked")
			}
			outbox.close()
			outbox = openTestTerminalOutbox(t, directory)
			defer outbox.close()
			if !outbox.has("step", "step") {
				t.Fatal("rejected receipt did not survive restart")
			}
		})
	}
}

type blockedTerminalClient struct {
	agentv1.AgentServiceClient
	started chan struct{}
}

func (c *blockedTerminalClient) ReportStepRunStatus(ctx context.Context, _ *agentv1.ReportStepRunStatusRequest, _ ...grpc.CallOption) (*agentv1.ReportStepRunStatusResponse, error) {
	close(c.started)
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestBackgroundReplayCancellationRetainsRecordAndReleasesLock(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "reports")
	outbox := openTestTerminalOutbox(t, directory)
	if err := outbox.put(context.Background(), terminalTestRecord(t, "step", "step")); err != nil {
		t.Fatal(err)
	}
	client := &blockedTerminalClient{started: make(chan struct{})}
	wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	safe.Go(func() { done <- outbox.replay(ctx, wrapper) })
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("replay did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected stop error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("replay did not stop on cancellation")
	}
	if err := outbox.close(); err != nil {
		t.Fatal(err)
	}
	outbox = openTestTerminalOutbox(t, directory)
	defer outbox.close()
	if !outbox.has("step", "step") {
		t.Fatal("cancelled in-flight report was lost")
	}
}

func TestPendingReplayBlocksNewQueueCommandsUntilAcknowledged(t *testing.T) {
	conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "agent", WorkspaceDir: t.TempDir()}, Grpc: config.GrpcConfig{ServerAddr: "controller:9090"}}
	outbox := openTestTerminalOutbox(t, filepath.Join(conf.Agent.WorkspaceDir, "reports"))
	defer outbox.close()
	if err := outbox.put(context.Background(), terminalTestRecord(t, "step", "old-pending-step")); err != nil {
		t.Fatal(err)
	}
	client := &terminalCaptureClient{failStep: true}
	wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
	handler := newWorkerHandler(context.Background(), conf, wrapper, nil, outbox)
	payload := &taskpayload.JobRunTaskPayload{PipelineRunID: "run", JobRunID: "new-job", AgentID: "agent", Steps: []taskpayload.StepPayload{{StepRunID: "new-step", Args: map[string]any{"run": "printf x >> executions"}}}}
	encoded, err := sonic.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	task := &nova.Task{Type: taskpayload.TaskTypeJobRun, Payload: encoded}
	if err := handler.ProcessTask(context.Background(), task); status.Code(err) != codes.Unavailable || acknowledgeCompleted(err) == nil {
		t.Fatalf("blocked historical report permitted queue ACK: %v", err)
	}
	marker := filepath.Join(conf.Agent.WorkspaceDir, "run", "new-job", "executions")
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new command ran with blocked report: %v", err)
	}
	client.failStep = false
	if err := handler.ProcessTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "x" {
		t.Fatalf("new task did not execute once after recovery: %s %v", data, err)
	}
	if outbox.has("step", "old-pending-step") {
		t.Fatal("historical receipt was not acknowledged first")
	}
}
