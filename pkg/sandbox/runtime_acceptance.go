//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/oci"
	"github.com/opencontainers/runtime-spec/specs-go"
)

// ContainerAcceptanceEvent 只旁观专用真实运行时已经完成的 SDK 调用，不提供替代结果
// 普通构建没有此类型，事件不包含命令正文或环境变量
// Operation 取 create、task-start、exec-create、exec-start、exec-delete、pause 或 resume
type ContainerAcceptanceEvent struct {
	Operation string
	Resource  ResourceRef
	PID       uint32
	ExecID    string
	Err       error
}

// ContainerAcceptanceObserver 记录真实调用；AfterResume 仅可在真实 Resume 后丢失响应
// 不能伪造成功、跳过底层调用、修改执行参数或创建可恢复的活动能力
type ContainerAcceptanceObserver struct {
	Observe     func(context.Context, ContainerAcceptanceEvent)
	AfterResume func(context.Context, ContainerAcceptanceEvent) error
}

// ObserveContainerAcceptanceRuntime 只在专用验收环境为尚未使用的真实后端安装旁观器
// 构建标签隔离整个入口；生产构建及任何 fake 后端均不能使用此注入
func ObserveContainerAcceptanceRuntime(s *Containerd, observer ContainerAcceptanceObserver) error {
	if os.Getenv("ARCENTRA_CONTAINERD_ACCEPTANCE") != "1" || os.Getenv("ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE") != "1" || os.Geteuid() == 0 || os.Getegid() == 0 {
		return fmt.Errorf("unprivileged explicitly authorized container acceptance required")
	}
	if s == nil {
		return fmt.Errorf("acceptance runtime unavailable")
	}
	jobNamespace := strings.HasPrefix(s.namespace, "arcentra-containerjob-acceptance-") && s.namespace == os.Getenv("ARCENTRA_TEST_CONTAINERD_NAMESPACE")
	durableNamespace := os.Getenv("ARCENTRA_DURABLE_SESSION_ACCEPTANCE") == "1" && strings.HasPrefix(s.namespace, "arcentra-durable-acceptance-") && s.namespace == os.Getenv("ARCENTRA_DURABLE_SESSION_NAMESPACE")
	if (!jobNamespace && !durableNamespace) || s.config.UnixSocket != os.Getenv("ARCENTRA_CONTAINERD_SOCKET") || s.config.RuntimeRoot != os.Getenv("ARCENTRA_CONTAINERD_RUNC_ROOT") || s.config.FIFODir != os.Getenv("ARCENTRA_CONTAINERD_FIFO_DIR") {
		return fmt.Errorf("observer requires exact dedicated acceptance runtime")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	backend, ok := s.backend.(*containerdBackend)
	if !ok || backend.client == nil || len(s.containers) != 0 || s.closing || s.closed {
		return fmt.Errorf("observer must wrap an unused actual containerd SDK exactly once")
	}
	s.backend = &acceptanceRuntimeBackend{runtimeBackend: backend, observer: observer}
	return nil
}

type acceptanceRuntimeBackend struct {
	runtimeBackend
	observer ContainerAcceptanceObserver
}

func (b *acceptanceRuntimeBackend) create(ctx context.Context, id, image string, opts []oci.SpecOpts, labels map[string]string, explicit bool) (runtimeContainer, error) {
	container, err := b.runtimeBackend.create(ctx, id, image, opts, labels, explicit)
	ref := ResourceRef{ID: id, Owner: labels[ownerLabel], Namespace: labels[namespaceLabel], Snapshotter: labels[snapshotterLabel]}
	b.observe(ctx, ContainerAcceptanceEvent{Operation: "create", Resource: ref, Err: err})
	return b.wrap(container, ref), err
}
func (b *acceptanceRuntimeBackend) inspectOwned(ctx context.Context, ref ResourceRef) (runtimeContainer, ResourceState, error) {
	container, state, err := b.runtimeBackend.inspectOwned(ctx, ref)
	return b.wrap(container, ref), state, err
}
func (b *acceptanceRuntimeBackend) wrap(container runtimeContainer, ref ResourceRef) runtimeContainer {
	if container == nil {
		return nil
	}
	return &acceptanceRuntimeContainer{runtimeContainer: container, backend: b, ref: ref}
}
func (b *acceptanceRuntimeBackend) observe(ctx context.Context, event ContainerAcceptanceEvent) {
	if b.observer.Observe != nil {
		b.observer.Observe(ctx, event)
	}
}

type acceptanceRuntimeContainer struct {
	runtimeContainer
	backend *acceptanceRuntimeBackend
	ref     ResourceRef
}

func (c *acceptanceRuntimeContainer) newTask(ctx context.Context, stdout, stderr io.Writer) (runtimeTask, error) {
	task, err := c.runtimeContainer.newTask(ctx, stdout, stderr)
	return c.wrap(task), err
}
func (c *acceptanceRuntimeContainer) loadTask(ctx context.Context) (runtimeTask, error) {
	task, err := c.runtimeContainer.loadTask(ctx)
	return c.wrap(task), err
}
func (c *acceptanceRuntimeContainer) wrap(task runtimeTask) runtimeTask {
	if task == nil {
		return nil
	}
	return &acceptanceRuntimeTask{runtimeTask: task, container: c}
}

type acceptanceRuntimeTask struct {
	runtimeTask
	container *acceptanceRuntimeContainer
}

func (t *acceptanceRuntimeTask) event(operation string, err error) ContainerAcceptanceEvent {
	return ContainerAcceptanceEvent{Operation: operation, Resource: t.container.ref, PID: t.runtimeTask.Pid(), Err: err}
}
func (t *acceptanceRuntimeTask) Start(ctx context.Context) error {
	err := t.runtimeTask.Start(ctx)
	t.container.backend.observe(ctx, t.event("task-start", err))
	return err
}
func (t *acceptanceRuntimeTask) Pause(ctx context.Context) error {
	err := t.runtimeTask.Pause(ctx)
	t.container.backend.observe(ctx, t.event("pause", err))
	return err
}
func (t *acceptanceRuntimeTask) Resume(ctx context.Context) error {
	err := t.runtimeTask.Resume(ctx)
	event := t.event("resume", err)
	t.container.backend.observe(ctx, event)
	if callback := t.container.backend.observer.AfterResume; callback != nil {
		return errors.Join(err, callback(ctx, event))
	}
	return err
}
func (t *acceptanceRuntimeTask) exec(ctx context.Context, id string, spec *specs.Process, stdin io.Reader, stdout, stderr io.Writer) (runtimeProcess, error) {
	process, err := t.runtimeTask.exec(ctx, id, spec, stdin, stdout, stderr)
	event := t.event("exec-create", err)
	event.ExecID = id
	t.container.backend.observe(ctx, event)
	if process == nil {
		return nil, err
	}
	return &acceptanceRuntimeProcess{runtimeProcess: process, task: t, id: id}, err
}

type acceptanceRuntimeProcess struct {
	runtimeProcess
	task *acceptanceRuntimeTask
	id   string
}

func (p *acceptanceRuntimeProcess) observe(ctx context.Context, operation string, err error) {
	event := p.task.event(operation, err)
	event.ExecID = p.id
	p.task.container.backend.observe(ctx, event)
}
func (p *acceptanceRuntimeProcess) Start(ctx context.Context) error {
	err := p.runtimeProcess.Start(ctx)
	p.observe(ctx, "exec-start", err)
	return err
}
func (p *acceptanceRuntimeProcess) Delete(ctx context.Context, opts ...containerd.ProcessDeleteOpts) (*containerd.ExitStatus, error) {
	status, err := p.runtimeProcess.Delete(ctx, opts...)
	p.observe(ctx, "exec-delete", err)
	return status, err
}
