// Copyright 2025 Arcentra Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/namespaces"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const cleanupTimeout = 20 * time.Second
const maxCapturedBytes = 4 * 1024 * 1024

// Containerd 提供显式生命周期的离线 containerd 执行后端
// Agent 仅支持经专用主机验收的显式离线策略，不能作为不可信多租户代码的安全保证
type Containerd struct {
	backend      runtimeBackend
	namespace    string
	config       ContainerdConfig
	mu           sync.RWMutex
	containers   map[string]*containerState
	removedIDs   map[string]struct{}
	removedOrder []string
	creating     sync.WaitGroup
	closing      bool
	closed       bool
	closeMu      sync.Mutex
}

type containerState struct {
	ref          ResourceRef
	container    runtimeContainer
	gate         chan struct{}
	mu           sync.Mutex
	activeCancel context.CancelFunc
	stopping     bool
	task         runtimeTask
	processes    map[string]runtimeProcess
	stopped      bool
	removed      bool
	logs         *boundedBuffer
}

func (s *containerState) acquire(ctx context.Context) error {
	select {
	case s.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *containerState) release() { <-s.gate }
func (s *containerState) cancelActive() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopping = true
	if s.activeCancel != nil {
		s.activeCancel()
	}
}

// ContainerdConfig 定义 containerd 连接与默认执行配置
type ContainerdConfig struct {
	UnixSocket string
	Namespace  string
	// RuntimeRoot 可选的专用 runc 状态目录，由操作者预先配置
	RuntimeRoot string
	// FIFODir 是 Agent 所有的预置私有 IO 目录，不修改系统默认目录
	FIFODir      string
	DefaultImage string
	// NetworkMode 仅支持 none，空值也表示 none
	NetworkMode string
	Resources   *Resources
}

func normalizeConfig(config *ContainerdConfig) (ContainerdConfig, error) {
	if config == nil {
		return ContainerdConfig{}, fmt.Errorf("config is required")
	}
	cfg := *config
	if cfg.FIFODir != "" {
		if err := validateFIFODir(cfg.FIFODir); err != nil {
			return cfg, err
		}
	}
	if cfg.RuntimeRoot != "" && (!filepath.IsAbs(cfg.RuntimeRoot) || filepath.Clean(cfg.RuntimeRoot) != cfg.RuntimeRoot || cfg.RuntimeRoot == "/" || strings.ContainsAny(cfg.RuntimeRoot, "\x00\r\n")) {
		return cfg, fmt.Errorf("runtime root must be a clean absolute non-root path")
	}
	if cfg.UnixSocket == "" {
		cfg.UnixSocket = "/run/containerd/containerd.sock"
	}
	if cfg.Namespace == "" {
		cfg.Namespace = "arcentra"
	}
	if cfg.NetworkMode == "" {
		cfg.NetworkMode = "none"
	}
	if err := validateNetwork(cfg.NetworkMode); err != nil {
		return cfg, err
	}
	if _, err := resourceSpec(cfg.Resources); err != nil {
		return cfg, err
	}
	if cfg.Resources != nil {
		copy := *cfg.Resources
		cfg.Resources = &copy
	}
	return cfg, nil
}

// NewContainerdSandbox 连接 containerd 并验证配置，不启动或安装运行时
func NewContainerdSandbox(config *ContainerdConfig, _ log.Logger) (*Containerd, error) {
	cfg, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("containerd sandbox requires Linux")
	}
	client, err := containerd.New(cfg.UnixSocket, containerd.WithTimeout(5*time.Second))
	if err != nil {
		return nil, fmt.Errorf("connect to containerd: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serving, err := client.IsServing(ctx)
	if err != nil || !serving {
		_ = client.Close()
		if err == nil {
			err = fmt.Errorf("containerd is not serving")
		}
		return nil, fmt.Errorf("check containerd: %w", err)
	}
	return newSandbox(cfg, &containerdBackend{client: client, runtimeRoot: cfg.RuntimeRoot, fifoDir: cfg.FIFODir}), nil
}

func newSandbox(config ContainerdConfig, backend runtimeBackend) *Containerd {
	return &Containerd{backend: backend, namespace: config.Namespace, config: config, containers: make(map[string]*containerState), removedIDs: make(map[string]struct{})}
}

// Create 创建容器元数据和 snapshot，必须显式提供 init 命令
func (s *Containerd) Create(ctx context.Context, opts *CreateOptions) (string, error) {
	if opts == nil {
		return "", fmt.Errorf("options are required")
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return "", fmt.Errorf("sandbox is closing")
	}
	s.creating.Add(1)
	s.mu.Unlock()
	defer s.creating.Done()
	image := strings.TrimSpace(opts.Image)
	if image == "" {
		image = strings.TrimSpace(s.config.DefaultImage)
	}
	if image == "" {
		return "", fmt.Errorf("image is required")
	}
	network := opts.NetworkMode
	if network == "" {
		network = s.config.NetworkMode
	}
	if err := validateNetwork(network); err != nil {
		return "", err
	}
	resources := opts.Resources
	if resources == nil {
		resources = s.config.Resources
	}
	options, err := createSpecOptions(opts, resources)
	if err != nil {
		return "", err
	}
	ref := ResourceRef{}
	if opts.Resource != nil {
		ref = *opts.Resource
		if err = s.validateRef(ref); err != nil {
			return "", err
		}
	} else {
		ref, err = s.PrepareResource(ctx, "arcentra-"+uuid.NewString(), uuid.NewString())
		if err != nil {
			return "", err
		}
	}
	labels := maps.Clone(opts.Labels)
	if labels == nil {
		labels = make(map[string]string)
	}
	maps.Copy(labels, ownershipLabels(ref))
	id := ref.ID
	ctx = namespaces.WithNamespace(ctx, s.namespace)
	state := newContainerState(nil, ref)
	if err = state.acquire(ctx); err != nil {
		return "", err
	}
	s.mu.Lock()
	if _, exists := s.containers[id]; exists {
		s.mu.Unlock()
		state.release()
		return "", fmt.Errorf("resource ID already tracked: %w", errdefs.ErrAlreadyExists)
	}
	s.containers[id] = state
	delete(s.removedIDs, id)
	s.removedOrder = slices.DeleteFunc(s.removedOrder, func(previous string) bool { return previous == id })
	s.mu.Unlock()
	container, createErr := s.backend.create(ctx, id, image, options, labels, opts.User != "")
	state.container = container
	if createErr != nil || container == nil {
		state.stopped = true
	}
	if container == nil {
		// backend 返回空句柄表示尚未创建本资源，不得清理预先存在的容器
		s.mu.Lock()
		delete(s.containers, id)
		s.mu.Unlock()
		state.removed = true
		state.release()
		if createErr == nil {
			createErr = fmt.Errorf("runtime returned no container")
		}
		return "", createErr
	}
	state.release()
	if createErr != nil {
		if cleanupErr := s.Remove(ctx, id); cleanupErr != nil {
			return id, errors.Join(createErr, cleanupErr)
		}
		return "", createErr
	}
	return id, nil
}

// Start 启动容器 init 进程，已停止的容器不能复用
func (s *Containerd) Start(ctx context.Context, id string) error {
	state, err := s.getContainer(id)
	if err != nil {
		return err
	}
	ctx = namespaces.WithNamespace(ctx, s.namespace)
	if err = state.acquire(ctx); err != nil {
		return err
	}
	defer state.release()
	state.mu.Lock()
	stopping := state.stopping
	state.mu.Unlock()
	if state.removed || state.stopped || stopping {
		return fmt.Errorf("container is removed or stopped")
	}
	if state.task != nil {
		status, err := state.task.Status(ctx)
		if err != nil {
			return fmt.Errorf("inspect existing init task: %w", err)
		}
		if status.Status != containerd.Running {
			state.stopped = true
			return fmt.Errorf("init task is %s; remove and recreate the container", status.Status)
		}
		return nil
	}
	task, err := state.container.newTask(ctx, state.logs, state.logs)
	if err != nil {
		state.stopped = true
		return fmt.Errorf("create task (Remove required to reconcile uncertain creation): %w", err)
	}
	state.task = task
	if err = task.Start(ctx); err != nil {
		cleanup, cancel := cleanupContext(ctx)
		defer cancel()
		killErr := stopTask(cleanup, task, 0)
		_, deleteErr := task.Delete(cleanup)
		if deleteErr == nil || errdefs.IsNotFound(deleteErr) {
			state.task = nil
			deleteErr = nil
		}
		state.stopped = true
		return errors.Join(fmt.Errorf("start task: %w", err), killErr, deleteErr)
	}
	return nil
}

// Execute 在已启动容器中执行命令并捕获有界输出
// 取消、超时或执行状态不确定时终止容器内全部进程，调用方随后必须 Remove
func (s *Containerd) Execute(ctx context.Context, id string, cmd []string, opts *ExecuteOptions) (result *ExecuteResult, resultErr error) {
	result = &ExecuteResult{StartTime: time.Now(), ExitCode: -1}
	stdout, stderr := &boundedBuffer{}, &boundedBuffer{}
	stdoutStream, stderrStream := &outputStream{capture: stdout}, &outputStream{capture: stderr}
	defer func() {
		result.Stdout, result.Stderr = stdout.String(), stderr.String()
		result.OutputTruncated = stdout.Truncated() || stderr.Truncated()
		resultErr = errors.Join(resultErr, stdoutStream.Error(), stderrStream.Error())
		result.EndTime = time.Now()
		result.Duration = result.EndTime.Sub(result.StartTime)
	}()
	ctx = namespaces.WithNamespace(ctx, s.namespace)
	if opts != nil && opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	state, err := s.getContainer(id)
	if err != nil {
		return result, err
	}
	if err = state.acquire(ctx); err != nil {
		return result, err
	}
	defer state.release()
	if state.removed || state.stopped || state.task == nil {
		return result, fmt.Errorf("container must be started and running before Execute")
	}
	if len(state.processes) != 0 {
		return result, fmt.Errorf("container has an unreaped process; Remove is required")
	}
	runCtx, cancel := context.WithCancel(ctx)
	state.mu.Lock()
	if state.stopping {
		state.mu.Unlock()
		cancel()
		return result, fmt.Errorf("container is stopping")
	}
	state.activeCancel = cancel
	state.mu.Unlock()
	defer func() { state.mu.Lock(); state.activeCancel = nil; state.mu.Unlock(); cancel() }()
	base, err := state.container.spec(runCtx)
	if err != nil {
		return result, fmt.Errorf("read container spec: %w", normalizeExecutionContextError(err))
	}
	processSpec, err := executionSpec(base, cmd, opts)
	if err != nil {
		return result, err
	}
	var stdin io.Reader
	stdoutStream.logs, stderrStream.logs = state.logs, state.logs
	if opts != nil {
		stdin = opts.Stdin
		if opts.Stdout != nil {
			stdoutStream.sink = opts.Stdout
		}
		if opts.Stderr != nil {
			stderrStream.sink = opts.Stderr
		}
	}
	execID := "exec-" + uuid.NewString()
	process, err := state.task.exec(runCtx, execID, processSpec, stdin, stdoutStream, stderrStream)
	if err != nil {
		cleanup, cleanupCancel := cleanupContext(ctx)
		defer cleanupCancel()
		state.stopped = true
		return result, errors.Join(fmt.Errorf("create exec process: %w", normalizeExecutionContextError(err)), stopTask(cleanup, state.task, 0))
	}
	state.processes[execID] = process
	defer func() {
		cleanup, cleanupCancel := cleanupContext(ctx)
		defer cleanupCancel()
		if resultErr != nil {
			// 杀掉整个容器，避免 shell 子进程在步骤取消后继续运行
			stopErr := stopTask(cleanup, state.task, 0)
			state.stopped = true
			resultErr = errors.Join(resultErr, stopErr)
		}
		_, deleteErr := process.Delete(cleanup)
		if deleteErr == nil || errdefs.IsNotFound(deleteErr) {
			delete(state.processes, execID)
		} else {
			resultErr = errors.Join(resultErr, fmt.Errorf("delete exec process: %w", deleteErr))
		}
	}()
	// 必须先订阅退出状态再启动，确保快速退出也可观察
	wait, err := process.Wait(runCtx)
	if err != nil {
		return result, fmt.Errorf("wait exec process: %w", normalizeExecutionContextError(err))
	}
	if err = process.Start(runCtx); err != nil {
		return result, fmt.Errorf("start exec process: %w", normalizeExecutionContextError(err))
	}
	select {
	case <-runCtx.Done():
		return result, runCtx.Err()
	case status, ok := <-wait:
		if runCtx.Err() != nil {
			return result, runCtx.Err()
		}
		if !ok {
			return result, fmt.Errorf("exec exit channel closed without a result")
		}
		code, _, waitErr := status.Result()
		if waitErr != nil {
			return result, fmt.Errorf("wait exec exit: %w", normalizeExecutionContextError(waitErr))
		}
		result.ExitCode = int32(code)
		return result, nil
	}
}

// containerd Wait can report a gRPC cancellation before the local context is
// done. Preserve the RPC error while exposing its standard context sentinel.
// Normalize only execution operations, never successful results or later
// cleanup errors, and do not infer cancellation from an unrelated RPC failure.
func normalizeExecutionContextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}
	switch status.Code(err) {
	case codes.DeadlineExceeded:
		return errors.Join(err, context.DeadlineExceeded)
	case codes.Canceled:
		return errors.Join(err, context.Canceled)
	default:
		return err
	}
}

// Stop 停止全部容器进程，优雅退出超时后强制终止
func (s *Containerd) Stop(ctx context.Context, id string, timeout time.Duration) error {
	state, err := s.getContainer(id)
	if err != nil {
		return err
	}
	state.cancelActive()
	ctx = namespaces.WithNamespace(ctx, s.namespace)
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout+cleanupTimeout)
	defer cancel()
	if err = state.acquire(cleanup); err != nil {
		return err
	}
	defer state.release()
	if state.removed {
		return nil
	}
	container, _, inspectErr := s.backend.inspectOwned(cleanup, state.ref)
	if inspectErr != nil {
		return inspectErr
	}
	state.container = container
	if err = state.reconcileTask(cleanup); err != nil {
		return err
	}
	if state.task == nil {
		return nil
	}
	if err = stopTask(cleanup, state.task, timeout); err != nil {
		return err
	}
	state.stopped = true
	return nil
}

func stopTask(ctx context.Context, task runtimeTask, grace time.Duration) error {
	status, err := task.Status(ctx)
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect task before stop: %w", err)
	}
	if status.Status == containerd.Stopped || (status.Status == containerd.Created && task.Pid() == 0) {
		return ensureTaskEmpty(ctx, task)
	}
	if status.Status == containerd.Created {
		grace = 0
	}
	wait, err := task.Wait(ctx)
	if err != nil {
		return fmt.Errorf("wait task before stop: %w", err)
	}
	signal := unix.SIGTERM
	if grace <= 0 {
		signal = unix.SIGKILL
	}
	if err = task.Kill(ctx, signal, containerd.WithKillAll); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("kill task: %w", err)
	}
	if grace > 0 {
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case status, ok := <-wait:
			return errors.Join(exitWaitError(status, ok), ensureTaskEmpty(ctx, task))
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			if err = task.Kill(ctx, unix.SIGKILL, containerd.WithKillAll); err != nil && !errdefs.IsNotFound(err) {
				return fmt.Errorf("force kill task: %w", err)
			}
		}
	}
	select {
	case status, ok := <-wait:
		return errors.Join(exitWaitError(status, ok), ensureTaskEmpty(ctx, task))
	case <-ctx.Done():
		return ctx.Err()
	}
}

// init 退出不代表整个容器已静默；必须验证 cgroup 内没有背景进程
func ensureTaskEmpty(ctx context.Context, task runtimeTask) error {
	pids, err := task.Pids(ctx)
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect remaining task processes: %w", err)
	}
	if len(pids) == 0 {
		return nil
	}
	if err = task.Kill(ctx, unix.SIGKILL, containerd.WithKillAll); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("kill remaining task processes: %w", err)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		pids, err = task.Pids(ctx)
		if errdefs.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("verify quiescent task: %w", err)
		}
		if len(pids) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("task processes remain after kill: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func exitWaitError(status containerd.ExitStatus, ok bool) error {
	if !ok {
		return fmt.Errorf("task exit channel closed without a result")
	}
	if err := status.Error(); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("wait task exit: %w", err)
	}
	return nil
}

// Remove 终止进程并删除容器和 snapshot，失败时保留记录以便重试
func (s *Containerd) Remove(ctx context.Context, id string) error {
	state, err := s.getContainer(id)
	if err != nil {
		s.mu.RLock()
		_, removed := s.removedIDs[id]
		s.mu.RUnlock()
		if removed {
			return nil
		}
		return err
	}
	return s.removeState(ctx, id, state)
}

func (s *Containerd) removeState(ctx context.Context, id string, state *containerState) error {
	state.cancelActive()
	cleanup, cancel := cleanupContext(namespaces.WithNamespace(ctx, s.namespace))
	defer cancel()
	if err := state.acquire(cleanup); err != nil {
		return err
	}
	defer state.release()
	// 查找与获得 gate 之间可能已完成删除，旧句柄不得触碰复用该 ID 的新资源
	if state.removed {
		return nil
	}

	container, resources, err := s.backend.inspectOwned(cleanup, state.ref)
	if err != nil {
		return err
	}
	state.container = container
	if !resources.ContainerExists && !resources.SnapshotExists && !resources.TaskExists {
		// Runtime metadata can be gone while a crashed client's owned FIFOs
		// remain. Preserve retry state until exact-owned filesystem cleanup succeeds.
		if state.container != nil {
			if err := state.container.remove(cleanup); err != nil {
				return fmt.Errorf("remove remaining container resources: %w", err)
			}
		}
		s.forgetRemoved(id, state)
		return nil
	}
	if err := state.reconcileTask(cleanup); err != nil {
		return err
	}
	if state.task != nil {
		if err := stopTask(cleanup, state.task, 0); err != nil {
			return err
		}
		state.stopped = true
		var cleanupErr error
		for execID, process := range state.processes {
			if _, err := process.Delete(cleanup); err != nil && !errdefs.IsNotFound(err) {
				cleanupErr = errors.Join(cleanupErr, err)
			} else {
				delete(state.processes, execID)
			}
		}
		if cleanupErr != nil {
			return fmt.Errorf("remove exec processes: %w", cleanupErr)
		}
		if _, err := state.task.Delete(cleanup); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("delete task: %w", err)
		}
		state.task = nil
	}
	if err := state.container.remove(cleanup); err != nil {
		return fmt.Errorf("remove container: %w", err)
	}
	// 删除后的实时查询负责确认没有残留，失败时保留精确身份用于重试
	_, resources, err = s.backend.inspectOwned(cleanup, state.ref)
	if err != nil {
		return err
	}
	if resources.ContainerExists || resources.SnapshotExists || resources.TaskExists {
		return fmt.Errorf("resource removal was not confirmed")
	}
	s.forgetRemoved(id, state)
	return nil
}

func (s *Containerd) forgetRemoved(id string, state *containerState) {
	state.removed = true
	s.mu.Lock()
	defer s.mu.Unlock()
	// 只有当前 map 中的同一代句柄才可删除记录或写入幂等缓存
	if s.containers[id] != state {
		return
	}
	delete(s.containers, id)
	if _, exists := s.removedIDs[id]; !exists {
		s.removedIDs[id] = struct{}{}
		s.removedOrder = append(s.removedOrder, id)
		// 仅用于进程内短期幂等；长期恢复必须使用显式 ResourceRef
		if len(s.removedOrder) > 1024 {
			delete(s.removedIDs, s.removedOrder[0])
			s.removedOrder = s.removedOrder[1:]
		}
	}
}

// GetLogs 返回内存中至多 4 MiB 的合并日志快照，不支持跟随或时间过滤
func (s *Containerd) GetLogs(_ context.Context, id string, opts *LogOptions) (io.ReadCloser, error) {
	if opts != nil && (opts.Follow || opts.Tail != 0 || !opts.Since.IsZero() || !opts.Until.IsZero() || opts.Timestamps) {
		return nil, fmt.Errorf("log following, tail, timestamps and time filters are unsupported")
	}
	state, err := s.getContainer(id)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader(state.logs.String())), nil
}

// Cleanup 清理当前已跟踪的容器，保留清理失败的记录并返回错误
func (s *Containerd) Cleanup(ctx context.Context) error {
	s.mu.RLock()
	ids := make([]string, 0, len(s.containers))
	for id := range s.containers {
		ids = append(ids, id)
	}
	s.mu.RUnlock()
	slices.Sort(ids)
	var result error
	for _, id := range ids {
		if err := s.Remove(ctx, id); err != nil {
			result = errors.Join(result, fmt.Errorf("cleanup %s: %w", id, err))
		}
	}
	return result
}

// Close 禁止创建新容器并清理资源，清理失败时可再次调用
func (s *Containerd) Close() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closing = true
	s.mu.Unlock()
	s.creating.Wait()
	if err := s.Cleanup(context.Background()); err != nil {
		return err
	}
	if err := s.backend.close(); err != nil {
		return err
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func (s *Containerd) getContainer(id string) (*containerState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, ok := s.containers[id]
	if !ok {
		return nil, fmt.Errorf("container %s: %w", id, errdefs.ErrNotFound)
	}
	return state, nil
}

func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
}

type boundedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	total  int64
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	b.total += int64(n)
	if remaining := maxCapturedBytes - b.buffer.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buffer.Write(p)
	}
	return n, nil
}
func (b *boundedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buffer.String() }

func (b *boundedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total > maxCapturedBytes
}

func (s *containerState) reconcileTask(ctx context.Context) error {
	if s.task != nil {
		return nil
	}
	task, err := s.container.loadTask(ctx)
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reconcile init task: %w", err)
	}
	s.task = task
	return nil
}

// 保留输出订阅者错误，但继续排空管道，防止日志接收失败阻塞容器
type outputStream struct {
	mu            sync.Mutex
	capture, logs *boundedBuffer
	sink          io.Writer
	err           error
}

func (w *outputStream) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = w.capture.Write(p)
	if w.logs != nil {
		_, _ = w.logs.Write(p)
	}
	if w.sink != nil && w.err == nil {
		n, err := w.sink.Write(p)
		if err == nil && n != len(p) {
			err = io.ErrShortWrite
		}
		if err != nil {
			w.err = fmt.Errorf("output stream: %w", err)
		}
	}
	return len(p), nil
}
func (w *outputStream) Error() error { w.mu.Lock(); defer w.mu.Unlock(); return w.err }
