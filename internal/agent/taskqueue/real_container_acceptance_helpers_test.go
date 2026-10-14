//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/agent/sandboxexec"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/internal/shared/storage"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/arcentrix/arcentra/pkg/sandbox"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/namespaces"
)

type realContainerSettings struct {
	stack                                                *realStackSettings
	socket, image, namespace, root, runtimeRoot, fifoDir string
}

func realContainerEnvironment(getenv func(string) string) (*realContainerSettings, error) {
	enabled, required := getenv("ARCENTRA_CONTAINERD_ACCEPTANCE"), getenv("ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE")
	if enabled == "" && required == "" {
		return nil, nil
	}
	if enabled != "1" || required != "1" {
		return nil, fmt.Errorf("both container acceptance flags must explicitly equal 1")
	}
	stack, err := realStackEnvironment(getenv)
	if err != nil {
		return nil, err
	}
	if stack == nil || getenv("ARCENTRA_REQUIRE_REAL_STACK") != "1" {
		return nil, fmt.Errorf("container acceptance requires the mandatory real stack; mocks are forbidden")
	}
	cfg := &realContainerSettings{stack: stack}
	for name, field := range map[string]*string{"ARCENTRA_CONTAINERD_SOCKET": &cfg.socket, "ARCENTRA_SANDBOX_TEST_IMAGE": &cfg.image, "ARCENTRA_TEST_CONTAINERD_NAMESPACE": &cfg.namespace, "ARCENTRA_TEST_CONTAINERD_ROOT": &cfg.root, "ARCENTRA_CONTAINERD_RUNC_ROOT": &cfg.runtimeRoot, "ARCENTRA_CONTAINERD_FIFO_DIR": &cfg.fifoDir} {
		*field = strings.TrimSpace(getenv(name))
		if *field == "" {
			return nil, fmt.Errorf("explicit %s is required", name)
		}
	}
	if !strings.HasPrefix(cfg.namespace, "arcentra-containerjob-acceptance-") || strings.ContainsAny(cfg.namespace, "/\\\r\n") {
		return nil, fmt.Errorf("dedicated arcentra-containerjob-acceptance-* namespace required")
	}
	for _, path := range []string{cfg.root, cfg.socket, cfg.runtimeRoot, cfg.fifoDir} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return nil, fmt.Errorf("clean absolute dedicated paths required")
		}
	}
	if cfg.socket == "/run/containerd/containerd.sock" || cfg.socket == "/var/run/containerd/containerd.sock" || cfg.runtimeRoot == "/run/containerd/runc" || cfg.runtimeRoot == "/var/run/containerd/runc" || cfg.fifoDir == "/run/containerd/fifo" || cfg.fifoDir == "/var/run/containerd/fifo" {
		return nil, fmt.Errorf("host default containerd, runc or FIFO paths are forbidden in acceptance")
	}
	policy := taskpayload.OfflineRuntimePolicy{Enabled: true, AllowedImages: []string{cfg.image}, MaxCPU: "500m", MaxMemory: "64Mi"}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// startContainerAcceptanceWorker 只替换关闭门禁之前的测试组装，使用实际处理器、队列、持久日志和认证恢复
// 当门禁独立开放后，同一验收自动改用未修改的生产 StartWorker
func startContainerAcceptanceWorker(ctx context.Context, conf *config.AgentConfig, client *agentgrpc.ClientWrapper, manager *executor.Manager) (_ nova.TaskQueue, resultErr error) {
	if taskpayload.OfflineRuntimeAccepted {
		return StartWorker(ctx, conf, client, manager)
	}
	if os.Getenv("ARCENTRA_CONTAINERD_ACCEPTANCE") != "1" || os.Getenv("ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE") != "1" {
		return nil, fmt.Errorf("explicit container acceptance flags required")
	}
	cfg := conf.Agent.Sandbox.Containerd
	if err := validateOfflineIORoot(cfg.FIFODir, conf.Agent.WorkspaceDir, cfg.JournalDir); err != nil {
		return nil, err
	}
	if runtime.GOOS != "linux" || os.Geteuid() == 0 || os.Getegid() == 0 || cfg.User != fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid()) {
		return nil, fmt.Errorf("unprivileged matching Linux UID:GID required")
	}
	if conf.Agent.Mode != "sandbox" || !conf.Agent.Sandbox.Enable || conf.Agent.Sandbox.Runtime != "containerd" || cfg.Network != "none" || cfg.Image != "" || cfg.Resources.CPU != "" || cfg.Resources.Memory != "" {
		return nil, fmt.Errorf("explicit offline production-shaped configuration required")
	}
	if err := cfg.Policy.Validate(); err != nil {
		return nil, err
	}
	if len(cfg.Policy.AllowedImages) != 1 || cfg.Policy.AllowedImages[0] != os.Getenv("ARCENTRA_SANDBOX_TEST_IMAGE") {
		return nil, fmt.Errorf("single approved acceptance image required")
	}
	binding, err := client.ControllerIdentityBinding()
	if err != nil {
		return nil, err
	}
	// This must still fail: the test is not evidence that production startup is enabled.
	if err := ValidateExecutionConfig(conf); err == nil {
		return nil, fmt.Errorf("production runtime gate unexpectedly accepted this test configuration")
	}
	// Verify the remaining production startup constraints independently of the
	// deliberately closed runtime gate; the real handler still receives sandbox mode.
	validationConf := *conf
	validationConf.Agent.Mode = "baremetal"
	validationConf.Agent.Sandbox = config.SandboxConfig{}
	if err := ValidateExecutionConfig(&validationConf); err != nil {
		return nil, err
	}
	reports, err := newTerminalOutbox(ctx, filepath.Join(conf.Agent.WorkspaceDir, ".terminal-report-outbox"), conf.Agent.ID, conf.Grpc.ServerAddr)
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			_ = reports.close()
		}
	}()
	if err := reports.replayBound(ctx, client, conf); err != nil {
		return nil, err
	}
	backend, err := sandbox.NewContainerdSandbox(&sandbox.ContainerdConfig{UnixSocket: cfg.UnixSocket, Namespace: cfg.Namespace, NetworkMode: "none", RuntimeRoot: cfg.RuntimeRoot, FIFODir: cfg.FIFODir}, log.Logger{})
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			_ = backend.Close()
		}
	}()
	controller, err := sandboxexec.OpenController(ctx, backend, sandboxexec.ControllerOptions{JournalRoot: cfg.JournalDir, WorkspaceRoot: conf.Agent.WorkspaceDir, ControllerID: binding, AgentID: conf.Agent.ID, MaxRecords: 1000})
	if err != nil {
		return nil, err
	}
	offline := &durableOfflineRuntime{backend: backend, controller: controller, user: cfg.User}
	defer func() {
		if resultErr != nil {
			_ = offline.Close(context.Background())
		}
	}()
	if err := confirmOfflineRecoveries(ctx, controller, conf, client); err != nil {
		return nil, err
	}
	if !offline.Ready() {
		return nil, fmt.Errorf("real runtime recovery did not become ready")
	}
	workerCtx := withOfflineJobRuntime(ctx, offline)
	qcfg := conf.TaskQueue
	codec, err := taskpayload.ResolveMessageCodec(qcfg.MessageFormat, qcfg.MessageCodec)
	if err != nil {
		return nil, err
	}
	options := []nova.QueueOption{nova.WithMessageCodec(codec), nova.WithTopicPrefix(taskpayload.AgentTopicPrefix(conf.Agent.ID)), nova.WithGroupID("arcentra-agent-" + conf.Agent.ID), nova.WithKafka(conf.MessageQueue.Kafka.BootstrapServers, nova.WithKafkaAuth("PLAINTEXT", "", "", ""), nova.WithKafkaAutoCommit(false), nova.WithKafkaSessionTimeout(qcfg.SessionTimeout), nova.WithKafkaMaxPollInterval(qcfg.MaxPollInterval), nova.WithKafkaDelaySlots(qcfg.DelaySlotCount, time.Duration(qcfg.DelaySlotDuration)*time.Second))}
	queue, err := nova.NewTaskQueue(options...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			_ = queue.Stop()
		}
	}()
	controlOptions := append([]nova.QueueOption{}, options...)
	controlOptions = append(controlOptions, nova.WithTopicPrefix(taskpayload.AgentControlTopicPrefix(conf.Agent.ID)), nova.WithGroupID("arcentra-agent-control-"+conf.Agent.ID))
	control, err := nova.NewTaskQueue(controlOptions...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			_ = control.Stop()
		}
	}()
	if err := control.Start(nova.HandlerFunc(func(_ context.Context, task *nova.Task) error {
		if task == nil || task.Type != taskpayload.TaskTypeJobCancel {
			return nil
		}
		var payload taskpayload.JobCancelTaskPayload
		if err := sonic.Unmarshal(task.Payload, &payload); err != nil {
			return err
		}
		if payload.AgentID != conf.Agent.ID || payload.JobRunID == "" {
			return fmt.Errorf("invalid cancellation target")
		}
		CancelJobRun(payload.JobRunID)
		return nil
	})); err != nil {
		return nil, err
	}
	if err := queue.Start(newWorkerHandler(workerCtx, conf, client, manager, reports)); err != nil {
		return nil, err
	}
	replayCtx, cancel := context.WithCancel(workerCtx)
	result := &workerQueues{offline: offline, TaskQueue: queue, control: control, reports: reports, cancelReports: cancel, reportsDone: make(chan struct{})}
	safe.Go(func() {
		defer close(result.reportsDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			_ = reports.replayBound(replayCtx, client, conf)
			select {
			case <-replayCtx.Done():
				return
			case <-ticker.C:
			}
		}
	})
	return result, nil
}

type containerAcceptanceJournal struct {
	Record *struct {
		RunID     string              `json:"pipelineRunID"`
		JobRunID  string              `json:"jobRunID"`
		Claim     string              `json:"executionClaimID"`
		Binding   string              `json:"runtimeBindingDigest"`
		Directory string              `json:"directory"`
		Phase     string              `json:"phase"`
		Resource  sandbox.ResourceRef `json:"resource"`
	} `json:"record"`
}

func containerAcceptanceRecords(root string) ([]containerAcceptanceJournal, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var records []containerAcceptanceJournal
	for _, entry := range entries {
		if entry.Name() == "owner.json" || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			return nil, err
		}
		var record containerAcceptanceJournal
		if err := sonic.Unmarshal(data, &record); err != nil {
			return nil, err
		}
		if record.Record == nil {
			return nil, fmt.Errorf("missing runtime journal record")
		}
		records = append(records, record)
	}
	return records, nil
}

// containerAcceptanceStorage 保留实际 S3 传输，仅在读出制品时旁观封存及后台写进程已停止
// 不模拟容器、不写入工作区，也不修改上传字节
type containerAcceptanceStorage struct {
	*storage.S3Storage
	socket, namespace, journal, workspace string
	mu                                    sync.Mutex
	sealedUploads                         int
	background                            []byte
	refs                                  []sandbox.ResourceRef
}

func (s *containerAcceptanceStorage) UploadStream(ctx context.Context, key string, input io.ReadSeeker, size int64, contentType string) error {
	records, err := containerAcceptanceRecords(s.journal)
	if err != nil {
		return err
	}
	if len(records) != 1 || records[0].Record.Phase != "sealed" {
		return fmt.Errorf("artifact read occurred before exact whole-job sealing")
	}
	record := records[0].Record
	client, err := containerd.New(s.socket)
	if err != nil {
		return err
	}
	defer client.Close()
	runtimeCtx := namespaces.WithNamespace(ctx, s.namespace)
	container, err := client.LoadContainer(runtimeCtx, record.Resource.ID)
	if err != nil {
		return err
	}
	task, err := container.Task(runtimeCtx, nil)
	if err != nil {
		return err
	}
	state, err := task.Status(runtimeCtx)
	if err != nil {
		return err
	}
	if state.Status != containerd.Stopped {
		return fmt.Errorf("artifact upload observed live container task: %s", state.Status)
	}
	pids, err := task.Pids(runtimeCtx)
	if (err != nil && !errdefs.IsNotFound(errdefs.FromGRPC(err))) || len(pids) != 0 {
		return fmt.Errorf("artifact upload observed remaining container processes: count=%d error=%v", len(pids), err)
	}
	writer := filepath.Join(s.workspace, record.Directory, "dist", "background.txt")
	before, readErr := os.ReadFile(writer)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if readErr == nil {
		timer := time.NewTimer(200 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		after, err := os.ReadFile(writer)
		if err != nil {
			return err
		}
		if len(before) == 0 || !bytes.Equal(before, after) {
			return fmt.Errorf("background process continued to mutate sealed artifact")
		}
	}
	if err := s.S3Storage.UploadStream(ctx, key, input, size, contentType); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sealedUploads++
	s.refs = append(s.refs, record.Resource)
	if readErr == nil {
		s.background = bytes.Clone(before)
	}
	return nil
}

func containerAcceptanceSource(t *testing.T, ctx context.Context, beforeFetch ...func() error) (string, string) {
	t.Helper()
	binary, err := exec.LookPath("git")
	if err != nil {
		t.Fatal("actual source staging requires git")
	}
	directory, initial, _ := controllerCheckoutRepository(t, ctx)
	root := t.TempDir()
	controllerCheckoutGit(t, ctx, root, "clone", "--bare", directory, filepath.Join(root, "source.git"))
	handler := &cgi.Handler{Path: binary, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		for _, check := range beforeFetch {
			if err := check(); err != nil {
				http.Error(writer, err.Error(), http.StatusPreconditionFailed)
				return
			}
		}
		handler.ServeHTTP(writer, request)
	}))
	t.Cleanup(server.Close)
	return server.URL + "/source.git", initial
}

func TestContainerAcceptanceSourceUsesRealSmartHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	url, commit := containerAcceptanceSource(t, ctx)
	workspace := t.TempDir()
	if _, err := handleSourceClone(ctx, &taskpayload.SourcePayload{Type: "git", Repo: url, Branch: "main"}, workspace); err != nil {
		t.Fatalf("actual production shallow Git staging over loopback HTTP: %v", err)
	}
	if actual := controllerCheckoutGit(t, ctx, workspace, "rev-parse", "HEAD"); actual != commit {
		t.Fatalf("smart HTTP changed source commit: %s != %s", actual, commit)
	}
}
