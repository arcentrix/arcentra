// Copyright 2025 Arcentra Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at https://www.apache.org/licenses/LICENSE-2.0

package sandbox

import (
	"context"
	"fmt"
	"strings"

	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/namespaces"
	"github.com/google/uuid"
)

const (
	ownerLabel       = "io.arcentra.sandbox.owner"
	resourceLabel    = "io.arcentra.sandbox.resource"
	namespaceLabel   = "io.arcentra.sandbox.namespace"
	snapshotterLabel = "io.arcentra.sandbox.snapshotter"
	snapshotLabel    = "io.arcentra.sandbox.snapshot"
)

// ResourceRef 绑定持久日志与特定命名空间中的容器和 snapshot，Owner 不得包含凭据
type ResourceRef struct {
	ID          string `json:"id"`
	Owner       string `json:"owner"`
	Namespace   string `json:"namespace"`
	Snapshotter string `json:"snapshotter"`
}

// ResourceState 表示实时验证所有权后的外部资源存在状态
type ResourceState struct {
	ContainerExists bool `json:"containerExists"`
	SnapshotExists  bool `json:"snapshotExists"`
	TaskExists      bool `json:"taskExists"`
}

// RecoverableSandbox 扩展可持久恢复的精确资源操作，不提供跨作业批量删除
type RecoverableSandbox interface {
	Sandbox
	Namespace() string
	PrepareResource(context.Context, string, string) (ResourceRef, error)
	InspectOwned(context.Context, ResourceRef) (ResourceState, error)
	SealOwned(context.Context, ResourceRef) error
	RemoveOwned(context.Context, ResourceRef) error
}

// Namespace 返回当前运行时命名空间
func (s *Containerd) Namespace() string { return s.namespace }

// PrepareResource 在创建副作用之前解析 snapshotter，供调用方先持久记录完整身份
func (s *Containerd) PrepareResource(ctx context.Context, id, owner string) (ResourceRef, error) {
	if err := validateIdentity(id, owner); err != nil {
		return ResourceRef{}, err
	}
	ctx = namespaces.WithNamespace(ctx, s.namespace)
	snapshotter, err := s.backend.snapshotter(ctx)
	if err != nil {
		return ResourceRef{}, err
	}
	ref := ResourceRef{ID: id, Owner: owner, Namespace: s.namespace, Snapshotter: snapshotter}
	return ref, s.validateRef(ref)
}

func validateIdentity(id, owner string) error {
	parsed, err := uuid.Parse(strings.TrimPrefix(id, "arcentra-"))
	if err != nil || id != "arcentra-"+parsed.String() {
		return fmt.Errorf("resource ID must be arcentra- followed by a canonical UUID")
	}
	if strings.TrimSpace(owner) == "" || len(owner) > 256 || strings.ContainsAny(owner, "\x00\r\n") {
		return fmt.Errorf("resource owner must be a nonempty bounded identifier")
	}
	return nil
}

func (s *Containerd) validateRef(ref ResourceRef) error {
	if err := validateIdentity(ref.ID, ref.Owner); err != nil {
		return err
	}
	if ref.Namespace == "" || ref.Namespace != s.namespace || ref.Snapshotter == "" || len(ref.Snapshotter) > 128 || strings.ContainsAny(ref.Snapshotter, "\x00\r\n/") {
		return fmt.Errorf("resource namespace or snapshotter does not match the sandbox")
	}
	return nil
}

func ownershipLabels(ref ResourceRef) map[string]string {
	return map[string]string{
		"io.arcentra.sandbox": "true", ownerLabel: ref.Owner,
		resourceLabel: ref.ID, namespaceLabel: ref.Namespace,
		snapshotterLabel: ref.Snapshotter, snapshotLabel: ref.ID + "-snapshot",
	}
}

func verifyOwnership(labels map[string]string, ref ResourceRef) error {
	for key, expected := range ownershipLabels(ref) {
		if labels[key] != expected {
			return fmt.Errorf("resource ownership mismatch for %s: %w", key, errdefs.ErrFailedPrecondition)
		}
	}
	return nil
}

// InspectOwned 直接查询运行时并逐一验证容器和 snapshot，不依赖内存记录
func (s *Containerd) InspectOwned(ctx context.Context, ref ResourceRef) (ResourceState, error) {
	if err := s.validateRef(ref); err != nil {
		return ResourceState{}, err
	}
	_, state, err := s.backend.inspectOwned(namespaces.WithNamespace(ctx, s.namespace), ref)
	return state, err
}

func (s *Containerd) ownedState(ctx context.Context, ref ResourceRef) (*containerState, error) {
	if err := s.validateRef(ref); err != nil {
		return nil, err
	}
	s.mu.RLock()
	existing := s.containers[ref.ID]
	s.mu.RUnlock()
	if existing != nil {
		if existing.ref != ref {
			return nil, fmt.Errorf("tracked resource ownership mismatch")
		}
		return existing, nil
	}
	// 无内存记录时先验证外部所有权，失败不能污染后续正确的恢复请求
	container, _, err := s.backend.inspectOwned(namespaces.WithNamespace(ctx, s.namespace), ref)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return nil, fmt.Errorf("sandbox is closing")
	}
	if state, ok := s.containers[ref.ID]; ok {
		if state.ref != ref {
			return nil, fmt.Errorf("tracked resource ownership mismatch")
		}
		return state, nil
	}
	state := newContainerState(container, ref)
	state.stopped = true // 恢复的资源仅允许静默与清理，不允许继续执行旧作业
	s.containers[ref.ID] = state
	return state, nil
}

// SealOwned 强制终止并确认全部容器进程消失，保留挂载与 snapshot 供产物传输
func (s *Containerd) SealOwned(ctx context.Context, ref ResourceRef) error {
	cleanup, cancel := cleanupContext(namespaces.WithNamespace(ctx, s.namespace))
	defer cancel()
	state, err := s.ownedState(cleanup, ref)
	if err != nil {
		return err
	}
	return s.sealState(cleanup, state)
}

func (s *Containerd) sealState(ctx context.Context, state *containerState) error {
	state.cancelActive()
	if err := state.acquire(ctx); err != nil {
		return err
	}
	defer state.release()
	if state.removed {
		return fmt.Errorf("cannot seal removed container: %w", errdefs.ErrNotFound)
	}
	if state.quiescence != nil {
		return s.removeStateLocked(ctx, state.ref.ID, state)
	}
	container, resources, err := s.backend.inspectOwned(ctx, state.ref)
	if err != nil {
		return err
	}
	state.container = container
	if !resources.ContainerExists {
		return fmt.Errorf("cannot seal absent container: %w", errdefs.ErrNotFound)
	}
	if err = state.reconcileTask(ctx); err != nil {
		return err
	}
	if state.task != nil {
		if err = stopStateTask(ctx, state, 0); err != nil {
			return err
		}
	}
	state.stopped = true
	return nil
}

// RemoveOwned 验证精确所有权后终止进程并删除资源，确认全部不存在时可幂等成功
func (s *Containerd) RemoveOwned(ctx context.Context, ref ResourceRef) error {
	cleanup, cancel := cleanupContext(namespaces.WithNamespace(ctx, s.namespace))
	defer cancel()
	state, err := s.ownedState(cleanup, ref)
	if err != nil {
		return err
	}
	return s.removeState(cleanup, ref.ID, state)
}

func newContainerState(container runtimeContainer, ref ResourceRef) *containerState {
	return &containerState{container: container, ref: ref, gate: make(chan struct{}, 1), processes: make(map[string]runtimeProcess), logs: &boundedBuffer{}}
}

var _ RecoverableSandbox = (*Containerd)(nil)
