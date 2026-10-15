// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandboxexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
)

func (s *Session) durableRecord(ctx context.Context) (journalRecord, error) {
	c := s.controller
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return journalRecord{}, err
	}
	record, ok := c.records[s.recordID]
	if !ok {
		return journalRecord{}, c.poison(fmt.Errorf("sandbox session journal disappeared"))
	}
	disk, err := c.readJournal(ctx, s.recordID+".json")
	if err != nil {
		return journalRecord{}, c.poison(err)
	}
	if disk == nil || !reflect.DeepEqual(*disk, record) {
		return journalRecord{}, c.poison(fmt.Errorf("sandbox session journal changed"))
	}
	return record, nil
}

func (s *Session) advance(ctx context.Context, phase string) error {
	c := s.controller
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return err
	}
	record, ok := c.records[s.recordID]
	if !ok {
		return c.poison(fmt.Errorf("sandbox session journal disappeared"))
	}
	record.Phase = phase
	if phase == phaseClosed && record.Generation != nil {
		receipt := c.generationCleanupReceipt(record)
		if err := receipt.ValidateGeneration(*record.Generation); err != nil {
			return c.poison(err)
		}
		record.CleanupReceipt = &receipt
	}
	return c.persist(ctx, record)
}

func (s *Session) blockController(err error) error {
	if err != nil {
		c := s.controller
		c.mu.Lock()
		c.runtimeBlocked[s.recordID] = err
		c.mu.Unlock()
	}
	return err
}

// 即使日志已经中毒，也可用创建前记住的不可变身份停止精确所有权资源
// 这里只停止进程，绝不删除、确认业务终态或开放产物访问
func (s *Session) stopOwnedOnFailure(ctx context.Context, cause error) error {
	s.mu.Lock()
	s.refused = true
	s.closing = true
	if s.activeCancel != nil {
		s.activeCancel()
	}
	s.mu.Unlock()
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	state, err := s.controller.backend.InspectOwned(stopCtx, s.resourceRef)
	if err == nil && state.ContainerExists {
		err = s.controller.backend.SealOwned(stopCtx, s.resourceRef)
	}
	return s.blockController(errors.Join(cause, err))
}

// Start 在可信输入暂存后创建并启动持久化会话，失败时保留原句柄供 Close 清理
// 不能启动已经封存、开始执行或曾经开始创建的会话，不会透明重跑
func (s *Session) Start(ctx context.Context) error {
	if s.controller == nil {
		return fmt.Errorf("legacy NewSession already starts its runtime; use Controller.Allocate for staged startup")
	}
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer s.release()
	s.mu.Lock()
	unavailable := s.closing || s.ready || s.sealed || s.cleaned
	var startupCancel context.CancelFunc
	if !unavailable {
		ctx, startupCancel = context.WithCancel(ctx)
		s.activeCancel = startupCancel
	}
	s.mu.Unlock()
	if unavailable {
		return fmt.Errorf("sandbox session cannot be started")
	}
	defer func() { startupCancel(); s.mu.Lock(); s.activeCancel = nil; s.mu.Unlock() }()
	record, err := s.durableRecord(ctx)
	if err != nil {
		return s.stopOwnedOnFailure(ctx, err)
	}
	if record.Phase != phaseAllocated {
		return fmt.Errorf("sandbox startup was already attempted; cleanup is required")
	}
	if err := s.verifyWorkspace(); err != nil {
		return s.stopOwnedOnFailure(ctx, err)
	}
	if _, err := s.controller.verifyRecordWorkspace(record, false); err != nil {
		return s.stopOwnedOnFailure(ctx, err)
	}
	if err := s.advance(ctx, phaseCreating); err != nil {
		return s.stopOwnedOnFailure(ctx, err)
	}
	id, err := s.createRuntime(ctx, record)
	if id != "" && id != record.Resource.ID || err == nil && id != record.Resource.ID {
		cause := fmt.Errorf("sandbox backend returned an inconsistent container identity; operator review required")
		evidenceCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		if persistErr := s.advance(evidenceCtx, phaseIdentityConflict); persistErr != nil {
			return persistErr
		}
		c := s.controller
		c.mu.Lock()
		_ = c.poison(cause)
		c.mu.Unlock()
		return cause
	}
	if err != nil {
		return s.stopOwnedOnFailure(ctx, fmt.Errorf("create durable sandbox: %w", err))
	}
	if err := s.advance(ctx, phaseCreated); err != nil {
		return s.stopOwnedOnFailure(ctx, err)
	}
	if err := s.advance(ctx, phaseStarting); err != nil {
		return s.stopOwnedOnFailure(ctx, err)
	}
	if err := s.startRuntime(ctx, id); err != nil {
		return s.stopOwnedOnFailure(ctx, fmt.Errorf("start durable sandbox: %w", err))
	}
	if err := s.advance(ctx, phaseRunning); err != nil {
		return s.stopOwnedOnFailure(ctx, err)
	}
	s.mu.Lock()
	s.ready = true
	s.mu.Unlock()
	return nil
}

// Seal 取消活动步骤并验证全部容器进程停止，保留工作目录供可信宿主上传制品
// 失败时禁止输出读取和后续执行，调用方必须保留证据并重试清理
func (s *Session) Seal(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	if s.activeCancel != nil {
		s.activeCancel()
	}
	s.mu.Unlock()
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if err := s.acquire(cleanupCtx); err != nil {
		if s.controller != nil {
			return s.stopOwnedOnFailure(ctx, fmt.Errorf("wait for sandbox sealing: %w", err))
		}
		return fmt.Errorf("wait for sandbox sealing: %w", err)
	}
	defer s.release()
	if s.cleaned {
		return fmt.Errorf("sandbox session is already closed")
	}
	if s.sealed {
		return nil
	}
	if s.controller == nil {
		// 旧版最小后端没有独立封存操作，移除运行时同样保留宿主工作目录
		if err := s.backend.Remove(cleanupCtx, s.containerID); err != nil {
			return fmt.Errorf("seal legacy sandbox: %w", err)
		}
		s.removed = true
		s.sealed = true
		return nil
	}
	record, err := s.durableRecord(cleanupCtx)
	if err != nil {
		return s.stopOwnedOnFailure(ctx, err)
	}
	if _, err := s.controller.verifyRecordWorkspace(record, false); err != nil {
		return s.stopOwnedOnFailure(ctx, err)
	}
	if err := s.advance(cleanupCtx, phaseSealing); err != nil {
		return s.stopOwnedOnFailure(ctx, err)
	}
	if record.Phase != phaseAllocated {
		if err := s.controller.backend.SealOwned(cleanupCtx, record.Resource); err != nil {
			return s.stopOwnedOnFailure(ctx, fmt.Errorf("seal durable sandbox: %w", err))
		}
	}
	if err := s.advance(cleanupCtx, phaseSealed); err != nil {
		return s.stopOwnedOnFailure(ctx, err)
	}
	s.sealed = true
	return nil
}

func (s *Session) closeDurable(ctx context.Context) error {
	s.mu.Lock()
	s.refused = true
	s.closing = true
	if s.activeCancel != nil {
		s.activeCancel()
	}
	s.mu.Unlock()
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if err := s.acquire(cleanupCtx); err != nil {
		return s.stopOwnedOnFailure(ctx, fmt.Errorf("wait for durable sandbox cleanup: %w", err))
	}
	defer s.release()
	if s.cleaned {
		return nil
	}
	record, err := s.durableRecord(cleanupCtx)
	if err != nil {
		return s.stopOwnedOnFailure(ctx, err)
	}
	allowMissing := record.Phase == phaseRemoving
	if _, err := s.controller.verifyRecordWorkspace(record, allowMissing); err != nil {
		return s.stopOwnedOnFailure(ctx, err)
	}
	if err := s.advance(cleanupCtx, phaseRemoving); err != nil {
		return s.stopOwnedOnFailure(ctx, err)
	}
	if err := s.controller.backend.RemoveOwned(cleanupCtx, record.Resource); err != nil {
		return s.stopOwnedOnFailure(ctx, fmt.Errorf("remove durable sandbox: %w", err))
	}
	s.removed = true
	if _, err := s.controller.verifyRecordWorkspace(record, allowMissing); err != nil {
		return s.stopOwnedOnFailure(ctx, err)
	}
	if err := s.controller.fault(cleanupCtx, "workspace.remove"); err != nil {
		return s.stopOwnedOnFailure(ctx, err)
	}
	if err := s.controller.workspaceRoot.RemoveAll(record.Directory); err != nil {
		return s.stopOwnedOnFailure(ctx, fmt.Errorf("remove recorded workspace: %w", err))
	}
	if err := s.controller.fault(cleanupCtx, "workspace.sync"); err != nil {
		return s.stopOwnedOnFailure(ctx, err)
	}
	if err := s.controller.workspaceDir.Sync(); err != nil {
		return s.stopOwnedOnFailure(ctx, fmt.Errorf("persist workspace removal: %w", err))
	}
	if err := s.controller.verifyCleanupAbsent(cleanupCtx, record); err != nil {
		return s.stopOwnedOnFailure(ctx, err)
	}
	if err := s.advance(cleanupCtx, phaseClosed); err != nil {
		return s.stopOwnedOnFailure(ctx, err)
	}
	s.cleaned = true
	c := s.controller
	c.mu.Lock()
	delete(c.sessions, s.recordID)
	delete(c.runtimeBlocked, s.recordID)
	c.mu.Unlock()
	return nil
}

// verifyCleanupAbsent 在破坏性清理后独立检查所有精确资源及目录均已消失
// RemoveOwned 的成功返回值本身不构成允许下一代执行的证据
func (c *Controller) verifyCleanupAbsent(ctx context.Context, record journalRecord) error {
	if err := c.fault(ctx, "cleanup.verify"); err != nil {
		return err
	}
	if err := c.verifyRoots(ctx); err != nil {
		return err
	}
	state, err := c.backend.InspectOwned(ctx, record.Resource)
	if err != nil {
		return fmt.Errorf("verify cleaned sandbox ownership: %w", err)
	}
	if state.TaskExists || state.ContainerExists || state.SnapshotExists {
		return fmt.Errorf("sandbox owned runtime resources remain after cleanup")
	}
	if _, err := c.workspaceRoot.Lstat(record.Directory); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("sandbox workspace absence is not confirmed")
	}
	return c.verifyRoots(ctx)
}
