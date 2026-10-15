// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandboxexec

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"time"

	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"github.com/google/uuid"
)

// 每个预分配尝试使用独立有界记录；会话主记录永不累计整作业步骤数组
// consumed 仅表示效果可能开始，绝不用于恢复或重放执行
// 所有读写均在 Controller.mu 及相应会话 gate 下完成
type stepAttemptJournal struct {
	RecordID          string                            `json:"recordId"`
	Request           taskpayload.StepAttemptTransition `json:"request"`
	Admission         *taskpayload.StepAttemptAdmission `json:"admission,omitempty"`
	Observation       *taskpayload.StepAttemptAdmission `json:"observation,omitempty"`
	Consumed          bool                              `json:"consumed,omitempty"`
	FinalAcknowledged bool                              `json:"finalAcknowledged,omitempty"`
}
type stepAttemptDocument struct {
	Version           int                `json:"version"`
	ControllerID      string             `json:"controllerId"`
	AgentID           string             `json:"agentId"`
	WorkspaceRoot     string             `json:"workspaceRoot"`
	Namespace         string             `json:"namespace"`
	RuntimeIOIdentity string             `json:"runtimeIoIdentity,omitempty"`
	Attempt           stepAttemptJournal `json:"attempt"`
	Checksum          string             `json:"checksum"`
}

func (c *Controller) journalCount() int {
	return len(c.records) + len(c.admissions) + len(c.stepAttempts)
}
func (c *Controller) reservedJournalCount() int {
	total := c.journalCount()
	for _, r := range c.records {
		if r.ReservedStepAttempts > 0 {
			used := 0
			for _, a := range c.stepAttempts {
				if a.RecordID == r.ID {
					used++
				}
			}
			if r.ReservedStepAttempts > used {
				total += r.ReservedStepAttempts - used
			}
		}
	}
	return total
}
func stepAttemptFilename(t taskpayload.StepAttemptTransition) string {
	body, _ := sonic.Marshal([]string{t.Scope.Identity.ClaimID, t.Scope.StepRunID})
	return "step-" + uuid.NewSHA1(uuid.NameSpaceOID, body).String() + ".json"
}
func (c *Controller) stepAttemptDocument(a stepAttemptJournal) stepAttemptDocument {
	return stepAttemptDocument{Version: stepAttemptJournalVersion, ControllerID: c.opts.ControllerID, AgentID: c.opts.AgentID, WorkspaceRoot: c.opts.WorkspaceRoot, Namespace: c.backend.Namespace(), RuntimeIOIdentity: c.runtimeIOIdentity, Attempt: a}
}
func encodeStepAttempt(d stepAttemptDocument) ([]byte, error) {
	d.Checksum = ""
	body, err := sonic.Marshal(d)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	d.Checksum = hex.EncodeToString(sum[:])
	return sonic.Marshal(d)
}
func (c *Controller) readStepAttempt(ctx context.Context, name string) (*stepAttemptJournal, error) {
	data, err := c.readJournalBytes(ctx, name)
	if err != nil {
		return nil, err
	}
	var d stepAttemptDocument
	if sonic.Unmarshal(data, &d) != nil {
		return nil, fmt.Errorf("invalid step attempt journal encoding")
	}
	canonical, err := encodeStepAttempt(d)
	if err != nil || !bytes.Equal(canonical, data) {
		return nil, fmt.Errorf("step attempt journal checksum or canonical encoding mismatch")
	}
	expected := c.stepAttemptDocument(d.Attempt)
	expected.Checksum = d.Checksum
	if !reflect.DeepEqual(d, expected) || stepAttemptFilename(d.Attempt.Request) != name {
		return nil, fmt.Errorf("step attempt journal owner or identity mismatch")
	}
	if err := c.validateStepAttempt(d.Attempt); err != nil {
		return nil, err
	}
	return &d.Attempt, nil
}
func (c *Controller) validateStepAttempt(a stepAttemptJournal) error {
	if err := a.Request.Validate(); err != nil {
		return err
	}
	if a.Request.Operation == "inspect" || a.Request.Scope.Identity.AgentID != c.opts.AgentID || a.RecordID != a.Request.Scope.RuntimeClaimID {
		return fmt.Errorf("step attempt journal has invalid owner")
	}
	if a.Admission != nil {
		if err := a.Admission.ValidateTransition(a.Request); err != nil {
			return err
		}
	}
	if a.Observation != nil {
		if err := a.Observation.ValidateTransition(a.Request); err != nil {
			return err
		}
	}
	if a.Consumed && a.Admission == nil {
		return fmt.Errorf("step consumed without an exact admission")
	}
	return nil
}
func (c *Controller) verifyStepAttemptStored(ctx context.Context, a stepAttemptJournal) error {
	disk, err := c.readStepAttempt(ctx, stepAttemptFilename(a.Request))
	if err != nil || disk == nil || !reflect.DeepEqual(*disk, a) {
		return c.poison(fmt.Errorf("step attempt journal changed: %w", err))
	}
	return nil
}
func (c *Controller) persistStepAttempt(ctx context.Context, a stepAttemptJournal) error {
	if err := c.validateStepAttempt(a); err != nil {
		return err
	}
	name := stepAttemptFilename(a.Request)
	if old, ok := c.stepAttempts[name]; ok {
		if err := c.verifyStepAttemptStored(ctx, old); err != nil {
			return err
		}
	} else {
		if c.journalCount() >= c.opts.MaxRecords {
			return fmt.Errorf("sandbox journal record quota exceeded")
		}
		r, ok := c.records[a.RecordID]
		used := 0
		for _, existing := range c.stepAttempts {
			if existing.RecordID == a.RecordID {
				used++
			}
		}
		if !ok || used >= r.ReservedStepAttempts {
			return fmt.Errorf("step attempt exceeds reserved immutable plan capacity")
		}
		if _, err := c.journal.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			return c.poison(fmt.Errorf("step attempt journal collided with existing path"))
		}
	}
	data, err := encodeStepAttempt(c.stepAttemptDocument(a))
	if err != nil {
		return err
	}
	if err := c.writeJournalData(ctx, name, data); err != nil {
		return c.poison(err)
	}
	c.stepAttempts[name] = a
	return nil
}
func sessionIdentity(r journalRecord) taskpayload.StepAttemptSession {
	return taskpayload.StepAttemptSession{SessionID: r.ID, SessionIncarnationNonce: r.StepSessionNonce, Namespace: r.Resource.Namespace, ContainerID: r.Resource.ID, SnapshotID: r.Resource.ID + "-snapshot", Snapshotter: r.Resource.Snapshotter, ResourceOwnerDigest: r.Resource.Owner, WorkspaceID: r.Directory, WorkspaceDevice: r.Device, WorkspaceInode: r.Inode}
}
func validateStepScope(r journalRecord, scope taskpayload.StepAttemptIdentity) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	g := r.Generation
	if r.StepSessionNonce == "" || g == nil || g.Number != 1 || scope.Session != sessionIdentity(r) || scope.GenerationID != g.GenerationID || scope.GenerationBindingDigest != g.BindingDigest || scope.RuntimeClaimID != g.RuntimeClaimID || scope.RetryPlanDigest != g.RetryPlanDigest || g.InputManifestDigest != "" && scope.InputManifestDigest != g.InputManifestDigest || scope.Identity.PipelineID != g.PipelineID || scope.Identity.PipelineRunID != g.PipelineRunID || scope.Identity.JobRunID != g.JobRunID || scope.Identity.AgentID != g.AgentID || scope.Identity.ClaimID != g.OriginalClaimID || scope.Identity.RuntimeBindingDigest != g.RuntimeBindingDigest || scope.Identity.SourceBindingDigest != g.SourceBindingDigest {
		return fmt.Errorf("step attempt changed original generation or live resource identity")
	}
	return nil
}
func (c *Controller) validateStepAttemptHistory() error {
	if c.reservedJournalCount() > c.opts.MaxRecords {
		return fmt.Errorf("step audit reservation exceeds journal quota")
	}
	for _, r := range c.records {
		used := 0
		for _, a := range c.stepAttempts {
			if a.RecordID == r.ID {
				used++
			}
		}
		if used > r.ReservedStepAttempts {
			return fmt.Errorf("step audit exceeds reserved plan size")
		}
	}
	for _, a := range c.stepAttempts {
		r, ok := c.records[a.RecordID]
		if !ok {
			if a.FinalAcknowledged {
				continue
			}
			return fmt.Errorf("step attempt has no exact owning session")
		}
		if err := validateStepScope(r, a.Request.Scope); err != nil {
			return err
		}
		if a.FinalAcknowledged && !r.FinalAcknowledged {
			return fmt.Errorf("step audit acknowledged before final job outcome")
		}
		if a.Request.Operation == "advance" && !r.FinalAcknowledged {
			predecessor := a.Request
			predecessor.Scope.StepRunID = a.Request.PreviousStepRunID
			old, exists := c.stepAttempts[stepAttemptFilename(predecessor)]
			if !exists || !old.Consumed || old.Admission == nil || old.Admission.AdmissionID != a.Request.PreviousAdmissionID || old.Admission.BindingDigest != a.Request.PreviousAdmissionBindingDigest {
				return fmt.Errorf("step audit predecessor is missing or inconsistent")
			}
		}
	}
	for _, r := range c.records {
		if r.ActiveStepAttempt != "" && !r.FinalAcknowledged {
			a, ok := c.stepAttempts[r.ActiveStepAttempt]
			if !ok || a.RecordID != r.ID {
				return fmt.Errorf("active step audit is absent")
			}
		}
	}
	return nil
}
func (c *Controller) pendingStepAttemptHandoff() bool {
	for _, a := range c.stepAttempts {
		if a.Admission == nil && !a.FinalAcknowledged {
			return true
		}
	}
	return false
}
func (c *Controller) acknowledgeStepAttempts(ctx context.Context, claim string) error {
	var names []string
	for name, a := range c.stepAttempts {
		if a.Request.Scope.Identity.ClaimID == claim {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		a := c.stepAttempts[name]
		if err := c.verifyStepAttemptStored(ctx, a); err != nil {
			return err
		}
		if !a.FinalAcknowledged {
			a.FinalAcknowledged = true
			if err := c.persistStepAttempt(ctx, a); err != nil {
				return err
			}
		}
	}
	for _, name := range names {
		if err := c.verifyRoots(ctx); err != nil {
			return err
		}
		if err := c.fault(ctx, "step.remove"); err != nil {
			return err
		}
		if err := c.journal.Remove(name); err != nil {
			return err
		}
		if err := c.syncJournal(ctx); err != nil {
			return err
		}
		delete(c.stepAttempts, name)
	}
	return nil
}

// StepAttemptSession 返回已持久化的原会话身份，不返回或恢复运行时证明
func (s *Session) StepAttemptSession(ctx context.Context) (taskpayload.StepAttemptSession, error) {
	if !s.stepRetry || s.controller == nil {
		return taskpayload.StepAttemptSession{}, fmt.Errorf("session does not support live failed-step retry")
	}
	if err := s.acquire(ctx); err != nil {
		return taskpayload.StepAttemptSession{}, err
	}
	defer s.release()
	r, err := s.durableRecord(ctx)
	if err != nil {
		return taskpayload.StepAttemptSession{}, err
	}
	if err := s.verifyWorkspace(); err != nil {
		return taskpayload.StepAttemptSession{}, err
	}
	if _, err := s.controller.verifyRecordWorkspace(r, false); err != nil {
		return taskpayload.StepAttemptSession{}, err
	}
	result := sessionIdentity(r)
	return result, result.Validate(r.ID)
}

func cloneStepTransition(t taskpayload.StepAttemptTransition) taskpayload.StepAttemptTransition {
	if t.Boundary != nil {
		copy := *t.Boundary
		t.Boundary = &copy
	}
	return t
}
func (s *Session) checkStepLifecycle(ctx context.Context, allowSealed bool) error {
	if !s.stepRetry || s.controller == nil {
		return fmt.Errorf("session does not support live failed-step retry")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refused || s.draining || s.cleaned || s.closing && !(allowSealed && s.sealed) {
		return fmt.Errorf("live failed-step session is closing")
	}
	return nil
}

// RecordStepAttemptIntent 在控制面 CAS 前持久保留唯一请求，原样重发不创建第二次执行权
func (s *Session) RecordStepAttemptIntent(ctx context.Context, t taskpayload.StepAttemptTransition) error {
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer s.release()
	if err := s.checkStepLifecycle(ctx, s.allowSealedStep(t.Scope)); err != nil {
		return err
	}
	if _, err := s.plannedStep(t.Scope); err != nil {
		return err
	}
	r, err := s.durableRecord(ctx)
	if err != nil {
		return err
	}
	if err := validateStepScope(r, t.Scope); err != nil {
		return err
	}
	if err := t.Validate(); err != nil {
		return err
	}
	if t.Operation == "inspect" {
		return fmt.Errorf("inspection never grants execution")
	}
	if t.Operation == "advance" {
		if s.boundaryAudit == nil || !reflect.DeepEqual(t.Boundary, s.boundaryAudit) || s.failedAdmission == nil || s.failedAdmission.AdmissionID != t.PreviousAdmissionID {
			return fmt.Errorf("retry intent lacks its exact original live boundary")
		}
		if err := s.checkPausedBoundary(ctx, *s.failedAdmission); err != nil {
			return err
		}
	} else if s.failedBoundary != nil {
		return fmt.Errorf("paused failed command cannot be bypassed by another logical step")
	}
	c := s.controller
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return err
	}
	if c.reconciliationPending {
		return fmt.Errorf("recovered step intents cannot execute")
	}
	name := stepAttemptFilename(t)
	if old, ok := c.stepAttempts[name]; ok {
		if err := c.verifyStepAttemptStored(ctx, old); err != nil {
			return err
		}
		if reflect.DeepEqual(old.Request, t) && !old.FinalAcknowledged && r.ActiveStepAttempt == name {
			return nil
		}
		return fmt.Errorf("step intent cannot change or return to a superseded attempt")
	}
	if r.ActiveStepAttempt != "" {
		old, ok := c.stepAttempts[r.ActiveStepAttempt]
		if !ok || old.Admission == nil {
			return fmt.Errorf("previous step admission is unresolved")
		}
		if old.Request.Scope.InputManifestDigest != t.Scope.InputManifestDigest {
			return fmt.Errorf("step input manifest cannot change")
		}
		if t.Operation == "admit" && t.Scope.StepIndex <= old.Request.Scope.StepIndex {
			return fmt.Errorf("logical step admission cannot move backward")
		}
		if t.Operation == "advance" && (!old.Consumed || old.Admission.AdmissionID != t.PreviousAdmissionID) {
			return fmt.Errorf("retry does not follow the current consumed admission")
		}
	}
	a := stepAttemptJournal{RecordID: r.ID, Request: cloneStepTransition(t)}
	if err := c.persistStepAttempt(ctx, a); err != nil {
		return err
	}
	r.ActiveStepAttempt = name
	return c.persist(ctx, r)
}

// RecordStepAttemptAdmission 保存原请求的唯一控制面响应，历史观察不能调用本入口
func (s *Session) RecordStepAttemptAdmission(ctx context.Context, t taskpayload.StepAttemptTransition, a taskpayload.StepAttemptAdmission) error {
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer s.release()
	if err := s.checkStepLifecycle(ctx, s.allowSealedStep(t.Scope)); err != nil {
		return err
	}
	if _, err := s.plannedStep(t.Scope); err != nil {
		return err
	}
	r, err := s.durableRecord(ctx)
	if err != nil {
		return err
	}
	if err := a.ValidateTransition(t); err != nil {
		return err
	}
	if err := validateStepScope(r, a.Scope); err != nil {
		return err
	}
	c := s.controller
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return err
	}
	name := stepAttemptFilename(t)
	old, ok := c.stepAttempts[name]
	if c.reconciliationPending || !ok || r.ActiveStepAttempt != name || !reflect.DeepEqual(old.Request, t) || old.FinalAcknowledged {
		return fmt.Errorf("step response has no active exact durable intent")
	}
	if err := c.verifyStepAttemptStored(ctx, old); err != nil {
		return err
	}
	if old.Observation != nil {
		return fmt.Errorf("denied step observation cannot become execution authority")
	}
	if old.Admission != nil {
		if *old.Admission == a {
			return nil
		}
		return fmt.Errorf("step admission cannot change")
	}
	copy := a
	old.Admission = &copy
	return c.persistStepAttempt(ctx, old)
}

func (s *Session) consumeStepAttempt(ctx context.Context, a taskpayload.StepAttemptAdmission, external bool) error {
	if _, err := s.plannedStep(a.Scope); err != nil {
		return err
	}
	if err := s.checkStepLifecycle(ctx, external && s.allowSealedStep(a.Scope)); err != nil {
		return err
	}
	r, err := s.durableRecord(ctx)
	if err != nil {
		return err
	}
	if err := validateStepScope(r, a.Scope); err != nil {
		return err
	}
	if err := a.Validate(); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	if now < a.NotBeforeUnixMilli || now >= a.ExpiresUnixMilli {
		return fmt.Errorf("step admission is delayed or expired")
	}
	if external && s.failedBoundary != nil {
		return fmt.Errorf("non-shell work cannot resume a paused failed command")
	}
	if !external && a.Scope.Attempt > 1 {
		if s.failedAdmission == nil || s.failedAdmission.AdmissionID != a.PreviousAdmissionID {
			return fmt.Errorf("retry lacks its original live failure")
		}
		if err := s.checkPausedBoundary(ctx, *s.failedAdmission); err != nil {
			return err
		}
	}
	if err := s.verifyWorkspace(); err != nil {
		return err
	}
	if _, err := s.controller.verifyRecordWorkspace(r, false); err != nil {
		return err
	}
	c := s.controller
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return err
	}
	old, ok := c.stepAttempts[r.ActiveStepAttempt]
	if c.reconciliationPending || !ok || old.Admission == nil || old.Observation != nil || *old.Admission != a || old.Consumed || old.FinalAcknowledged {
		return fmt.Errorf("step admission is stale, consumed or not durably acknowledged")
	}
	if err := c.verifyStepAttemptStored(ctx, old); err != nil {
		return err
	}
	old.Consumed = true
	return c.persistStepAttempt(ctx, old)
}

// RecordStepAttemptConsumed 在可信非 Shell 执行效果前一次性持久消费准入
func (s *Session) RecordStepAttemptConsumed(ctx context.Context, a taskpayload.StepAttemptAdmission) error {
	if !s.stepRetry || s.controller == nil {
		return fmt.Errorf("session does not support live step attempts")
	}
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer s.release()
	if err := s.consumeStepAttempt(ctx, a, true); err != nil {
		return s.stopOwnedOnFailure(ctx, err)
	}
	if err := s.verifyConsumedEffect(ctx, a, s.allowSealedStep(a.Scope)); err != nil {
		return s.stopOwnedOnFailure(ctx, err)
	}
	if time.Now().UnixMilli() >= a.ExpiresUnixMilli || !s.jobDeadline.IsZero() && !time.Now().Before(s.jobDeadline) {
		return s.stopOwnedOnFailure(ctx, fmt.Errorf("step admission expired after durable consumption"))
	}
	return nil
}

// RecordStepAttemptObservation 保留被拒绝的不可变响应，只用于审计且永久关闭本会话执行
func (s *Session) RecordStepAttemptObservation(ctx context.Context, t taskpayload.StepAttemptTransition, a taskpayload.StepAttemptAdmission) error {
	if !s.stepRetry || s.controller == nil {
		return fmt.Errorf("session does not support live step attempts")
	}
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer s.release()
	if err := a.ValidateTransition(t); err != nil {
		return err
	}
	r, err := s.durableRecord(ctx)
	if err != nil {
		return err
	}
	if err := validateStepScope(r, a.Scope); err != nil {
		return err
	}
	c := s.controller
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return err
	}
	old, ok := c.stepAttempts[stepAttemptFilename(t)]
	if !ok || r.ActiveStepAttempt != stepAttemptFilename(t) || !reflect.DeepEqual(old.Request, t) || old.FinalAcknowledged {
		return fmt.Errorf("denied step observation lacks exact durable intent")
	}
	if old.Observation != nil && *old.Observation != a {
		return fmt.Errorf("denied step observation changed")
	}
	copy := a
	old.Observation = &copy
	if err := c.persistStepAttempt(ctx, old); err != nil {
		return err
	}
	s.mu.Lock()
	s.closing = true
	s.refused = true
	if s.activeCancel != nil {
		s.activeCancel()
	}
	s.mu.Unlock()
	return nil
}

// 消费落盘可能阻塞，返回后必须重新检查身份、当前记录和时间，不能沿用写入前观察
func (s *Session) verifyConsumedEffect(ctx context.Context, a taskpayload.StepAttemptAdmission, allowSealed bool) error {
	if err := s.checkStepLifecycle(ctx, allowSealed); err != nil {
		return err
	}
	now := time.Now()
	if now.UnixMilli() < a.NotBeforeUnixMilli || now.UnixMilli() >= a.ExpiresUnixMilli || !s.jobDeadline.IsZero() && !now.Before(s.jobDeadline) {
		return fmt.Errorf("step admission is not effective after durable consumption")
	}
	r, err := s.durableRecord(ctx)
	if err != nil {
		return err
	}
	if err := validateStepScope(r, a.Scope); err != nil {
		return err
	}
	if err := s.verifyWorkspace(); err != nil {
		return err
	}
	if _, err := s.controller.verifyRecordWorkspace(r, false); err != nil {
		return err
	}
	c := s.controller
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return err
	}
	row, ok := c.stepAttempts[r.ActiveStepAttempt]
	if !ok || !row.Consumed || row.Admission == nil || *row.Admission != a || row.Observation != nil || row.FinalAcknowledged {
		return fmt.Errorf("consumed execution authority changed before effect")
	}
	return c.verifyStepAttemptStored(ctx, row)
}
