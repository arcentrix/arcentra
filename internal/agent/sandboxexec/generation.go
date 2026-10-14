// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandboxexec

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"time"

	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/google/uuid"
)

// AllocateGeneration 只为当前进程已持久保存的控制面准入分配全新的整作业会话
// 原始作业认领保持不变，独立运行认领决定本代资源身份；恢复启动绝不重放执行
func (c *Controller) AllocateGeneration(ctx context.Context, opts Options, generation taskpayload.JobGeneration) (*Session, error) {
	return c.allocate(ctx, opts, &generation)
}

func copyGeneration(g *taskpayload.JobGeneration) *taskpayload.JobGeneration {
	if g == nil {
		return nil
	}
	copy := *g
	return &copy
}
func copyCleanupReceipt(r *taskpayload.JobGenerationCleanupReceipt) *taskpayload.JobGenerationCleanupReceipt {
	if r == nil {
		return nil
	}
	copy := *r
	return &copy
}
func copyGenerationTransition(t *taskpayload.JobGenerationTransition) *taskpayload.JobGenerationTransition {
	if t == nil {
		return nil
	}
	copy := *t
	copy.CleanupReceipt = copyCleanupReceipt(t.CleanupReceipt)
	return &copy
}

func (c *Controller) validateGenerationAllocation(ctx context.Context, opts Options, generation *taskpayload.JobGeneration) error {
	var previous *journalRecord
	if generation != nil {
		if err := generation.Validate(); err != nil {
			return err
		}
		id, err := uuid.Parse(generation.RuntimeClaimID)
		if err != nil || id.String() != generation.RuntimeClaimID || !generationAllocationAllowed(*generation) ||
			generation.JobRunID != opts.JobRunID || generation.PipelineRunID != opts.PipelineRunID ||
			generation.OriginalClaimID != opts.ExecutionClaimID || generation.AgentID != c.opts.AgentID ||
			generation.RuntimeBindingDigest != opts.RuntimeBindingDigest || generation.NotBeforeUnixMilli > time.Now().UnixMilli() {
			return fmt.Errorf("generation admission does not authorize this exact runtime allocation")
		}
	}
	for _, admission := range c.admissions {
		if admission.Request.Identity.JobRunID != opts.JobRunID && admission.Request.Identity.ClaimID != opts.ExecutionClaimID {
			continue
		}
		if err := c.verifyAdmissionStored(ctx, admission); err != nil {
			return err
		}
		if generation == nil || admission.Request.Identity.JobRunID != opts.JobRunID || admission.Request.Identity.ClaimID != opts.ExecutionClaimID || admission.FinalAcknowledged {
			return fmt.Errorf("sandbox job or execution claim has an unresolved admission")
		}
	}
	for _, record := range c.records {
		if generation != nil && record.Generation != nil && (record.Generation.GenerationID == generation.GenerationID || record.Generation.RuntimeClaimID == generation.RuntimeClaimID) {
			return fmt.Errorf("sandbox generation is already recorded")
		}
		if record.JobRunID != opts.JobRunID && record.ExecutionClaimID != opts.ExecutionClaimID {
			continue
		}
		if generation == nil || record.Generation == nil || record.JobRunID != opts.JobRunID || record.ExecutionClaimID != opts.ExecutionClaimID {
			return fmt.Errorf("sandbox job or execution claim is already recorded; reconcile its outcome before retrying")
		}
		disk, err := c.readJournal(ctx, record.ID+".json")
		if err != nil || disk == nil || !reflect.DeepEqual(*disk, record) {
			return c.poison(fmt.Errorf("generation predecessor journal changed before allocation"))
		}
		if record.FinalAcknowledged || record.Phase != phaseClosed || record.CleanupReceipt == nil || record.Generation.Number >= generation.Number {
			return fmt.Errorf("previous generation is not eligible for a successor")
		}
		if !sameGenerationJob(*record.Generation, *generation) {
			return fmt.Errorf("generation changed immutable execution bindings")
		}
		if _, active := c.sessions[record.ID]; active {
			return fmt.Errorf("previous generation still has a live session")
		}
		if err := c.verifyCleanupAbsent(ctx, record); err != nil {
			return c.poison(err)
		}
		if previous == nil || record.Generation.Number > previous.Generation.Number {
			copy := record
			previous = &copy
		}
	}
	if generation == nil {
		return nil
	}
	if generation.Number == 1 {
		admission, exists := c.admissions[admissionFilenameForGeneration(*generation)]
		if !exists || admission.FinalAcknowledged || admission.Generation == nil || !reflect.DeepEqual(*admission.Generation, *generation) {
			return fmt.Errorf("initial generation has no exact durable admission")
		}
		if previous != nil || generation.PreviousGenerationID != "" {
			return fmt.Errorf("initial generation already exists")
		}
		return nil
	}
	if previous == nil || previous.Generation.Number+1 != generation.Number || previous.Generation.GenerationID != generation.PreviousGenerationID ||
		previous.TransitionIntent == nil || previous.SuccessorAdmission == nil || !reflect.DeepEqual(*previous.SuccessorAdmission, *generation) {
		return fmt.Errorf("successor generation has no exact durable admission and closed predecessor")
	}
	return nil
}

func generationAllocationAllowed(g taskpayload.JobGeneration) bool {
	return g.ExecutionAllowed && (!g.PreparationOnly || g.Number == 1 && g.InputManifestDigest == "")
}

func sameGenerationJob(a, b taskpayload.JobGeneration) bool {
	return a.PipelineID == b.PipelineID && a.PipelineRunID == b.PipelineRunID && a.JobRunID == b.JobRunID && a.AgentID == b.AgentID &&
		a.OriginalClaimID == b.OriginalClaimID && a.RuntimeBindingDigest == b.RuntimeBindingDigest && a.SourceBindingDigest == b.SourceBindingDigest &&
		a.RetryPlanDigest == b.RetryPlanDigest && (a.InputManifestDigest == "" && a.Number == 1 || a.InputManifestDigest == b.InputManifestDigest)
}

func (c *Controller) validateGenerationRecord(record journalRecord) error {
	g := record.Generation
	if g == nil {
		if record.CleanupReceipt != nil || record.TransitionIntent != nil || record.SuccessorAdmission != nil || record.GenerationObservation != nil || record.FinalAcknowledged {
			return fmt.Errorf("legacy sandbox record cannot contain generation evidence")
		}
		return nil
	}
	if err := g.Validate(); err != nil {
		return err
	}
	if g.RuntimeClaimID != record.ID || g.JobRunID != record.JobRunID || g.PipelineRunID != record.PipelineRunID ||
		g.OriginalClaimID != record.ExecutionClaimID || g.AgentID != c.opts.AgentID || g.RuntimeBindingDigest != record.RuntimeBindingDigest || !generationAllocationAllowed(*g) {
		return fmt.Errorf("sandbox generation ownership does not match its journal")
	}
	terminal := record.Phase == phaseClosed || record.Phase == phaseRecovered
	if record.FinalAcknowledged && !terminal {
		return fmt.Errorf("live generation cannot have final acknowledgement")
	}
	if record.CleanupReceipt != nil {
		if !terminal || !reflect.DeepEqual(*record.CleanupReceipt, c.generationCleanupReceipt(record)) {
			return fmt.Errorf("generation cleanup receipt does not match exact owned resources")
		}
		if err := record.CleanupReceipt.ValidateGeneration(*g); err != nil {
			return err
		}
	}
	if record.Phase == phaseClosed && record.CleanupReceipt == nil {
		return fmt.Errorf("closed generation cleanup receipt is missing")
	}
	if record.TransitionIntent != nil {
		if record.Phase != phaseClosed || record.CleanupReceipt == nil {
			return fmt.Errorf("generation transition precedes verified closure")
		}
		if err := validateGenerationTransition(record, *record.TransitionIntent); err != nil {
			return err
		}
	}
	if record.GenerationObservation != nil {
		if record.TransitionIntent == nil {
			return fmt.Errorf("generation observation has no durable request")
		}
		if err := validateGenerationObservation(*record.TransitionIntent, *record.GenerationObservation); err != nil {
			return err
		}
	}
	if record.SuccessorAdmission != nil {
		if record.TransitionIntent == nil {
			return fmt.Errorf("generation admission has no durable intent")
		}
		if err := validateSuccessorAdmission(record, *record.SuccessorAdmission); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) validateGenerationHistory() error {
	jobs := make(map[string][]journalRecord)
	claims := make(map[string]string)
	generationIDs := make(map[string]bool)
	for _, record := range c.records {
		if job, exists := claims[record.ExecutionClaimID]; exists && job != record.JobRunID {
			return fmt.Errorf("sandbox journal contains duplicate execution claim")
		}
		claims[record.ExecutionClaimID] = record.JobRunID
		jobs[record.JobRunID] = append(jobs[record.JobRunID], record)
		if record.Generation != nil {
			if generationIDs[record.Generation.GenerationID] {
				return fmt.Errorf("sandbox journal contains duplicate generation identity")
			}
			generationIDs[record.Generation.GenerationID] = true
		}
	}
	for _, group := range jobs {
		if len(group) == 1 && group[0].Generation == nil {
			continue
		}
		for _, r := range group {
			if r.Generation == nil {
				return fmt.Errorf("sandbox journal mixes legacy and generation ownership")
			}
		}
		sort.Slice(group, func(i, j int) bool { return group[i].Generation.Number < group[j].Generation.Number })
		for i, record := range group {
			if i == 0 {
				admission, exists := c.admissions[admissionFilenameForGeneration(*record.Generation)]
				if !record.FinalAcknowledged && (!exists || admission.Generation == nil || record.Generation.Number == 1 && !reflect.DeepEqual(*admission.Generation, *record.Generation)) {
					return fmt.Errorf("sandbox generation initial admission is missing")
				}
				if record.Generation.Number != 1 && !record.FinalAcknowledged {
					return fmt.Errorf("sandbox generation predecessor is missing")
				}
			} else {
				previous := group[i-1]
				if !sameGenerationJob(*previous.Generation, *record.Generation) || previous.Generation.Number >= record.Generation.Number {
					return fmt.Errorf("sandbox generation history is inconsistent")
				}
				if !record.FinalAcknowledged && (previous.Generation.Number+1 != record.Generation.Number || previous.Generation.GenerationID != record.Generation.PreviousGenerationID ||
					previous.SuccessorAdmission == nil || !reflect.DeepEqual(*previous.SuccessorAdmission, *record.Generation)) {
					return fmt.Errorf("sandbox generation has no durable predecessor admission")
				}
			}
			if i != len(group)-1 && record.Phase != phaseClosed && record.Phase != phaseRecovered {
				return fmt.Errorf("sandbox journal contains multiple live generations")
			}
		}
	}
	return c.validateAdmissionHistory()
}

func validateGenerationTransition(record journalRecord, request taskpayload.JobGenerationTransition) error {
	if err := request.Validate(); err != nil {
		return err
	}
	g := record.Generation
	if g == nil || request.Operation != "advance" || request.PreviousGenerationID != g.GenerationID || request.Identity.PipelineID != g.PipelineID ||
		request.Identity.PipelineRunID != g.PipelineRunID || request.Identity.JobRunID != g.JobRunID || request.Identity.AgentID != g.AgentID ||
		request.Identity.ClaimID != g.OriginalClaimID || request.Identity.RuntimeBindingDigest != g.RuntimeBindingDigest || request.Identity.SourceBindingDigest != g.SourceBindingDigest ||
		request.RetryPlanDigest != g.RetryPlanDigest || request.InputManifestDigest == "" ||
		g.InputManifestDigest != "" && request.InputManifestDigest != g.InputManifestDigest ||
		request.CleanupReceipt == nil || record.CleanupReceipt == nil || !reflect.DeepEqual(*request.CleanupReceipt, *record.CleanupReceipt) {
		return fmt.Errorf("generation transition does not match retained cleanup and immutable execution bindings")
	}
	return nil
}
func validateSuccessorAdmission(record journalRecord, generation taskpayload.JobGeneration) error {
	if err := generation.Validate(); err != nil {
		return err
	}
	if record.Generation == nil || record.TransitionIntent == nil || !generation.ExecutionAllowed ||
		generation.Number != record.Generation.Number+1 || generation.PreviousGenerationID != record.Generation.GenerationID ||
		generation.GenerationID == record.Generation.GenerationID || generation.RuntimeClaimID == record.Generation.RuntimeClaimID ||
		!sameGenerationJob(*record.Generation, generation) || generation.InputManifestDigest != record.TransitionIntent.InputManifestDigest {
		return fmt.Errorf("successor admission does not match the durable generation transition")
	}
	return nil
}

func (c *Controller) generationRecord(ctx context.Context, request taskpayload.JobGenerationTransition) (journalRecord, error) {
	for _, record := range c.records {
		if record.Generation != nil && record.Generation.GenerationID == request.PreviousGenerationID {
			disk, err := c.readJournal(ctx, record.ID+".json")
			if err != nil || disk == nil || !reflect.DeepEqual(*disk, record) {
				return journalRecord{}, c.poison(fmt.Errorf("generation predecessor journal changed"))
			}
			if record.Phase != phaseClosed || record.FinalAcknowledged {
				return journalRecord{}, fmt.Errorf("generation is not a closed retry predecessor")
			}
			if err := validateGenerationTransition(record, request); err != nil {
				return journalRecord{}, err
			}
			return record, nil
		}
	}
	return journalRecord{}, fmt.Errorf("generation transition predecessor is not recorded")
}

// RecordGenerationIntent 在调用控制面推进 RPC 前保留原封不动的代次请求
// 失败与 ACK 丢失不能删除上一代清理回执或产生另一份请求
func (c *Controller) RecordGenerationIntent(ctx context.Context, request taskpayload.JobGenerationTransition) error {
	if request.Operation == "admit" {
		return c.recordInitialIntent(ctx, request)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return err
	}
	if c.reconciliationPending {
		return fmt.Errorf("startup generation recovery requires explicit reconciliation")
	}
	record, err := c.generationRecord(ctx, request)
	if err != nil {
		return err
	}
	if err := c.verifyCleanupAbsent(ctx, record); err != nil {
		return c.poison(err)
	}
	if record.TransitionIntent != nil {
		if reflect.DeepEqual(*record.TransitionIntent, request) {
			return nil
		}
		return fmt.Errorf("generation transition cannot change its durable request")
	}
	record.TransitionIntent = copyGenerationTransition(&request)
	return c.persist(ctx, record)
}

// RecordGenerationAdmission 在新会话分配前持久保存与原请求对应的控制面响应
// 这是代次清理确认，不是最终作业 ACK，全部原始证据继续保留
func (c *Controller) RecordGenerationAdmission(ctx context.Context, request taskpayload.JobGenerationTransition, generation taskpayload.JobGeneration) error {
	if !generation.ExecutionAllowed {
		return c.recordGenerationObservation(ctx, request, generation)
	}
	if request.Operation == "admit" {
		return c.recordInitialAdmission(ctx, request, generation)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return err
	}
	if c.reconciliationPending {
		return fmt.Errorf("startup generation recovery cannot resume execution")
	}
	record, err := c.generationRecord(ctx, request)
	if err != nil {
		return err
	}
	if record.TransitionIntent == nil || !reflect.DeepEqual(*record.TransitionIntent, request) {
		return fmt.Errorf("generation response has no exact durable request")
	}
	if err := validateSuccessorAdmission(record, generation); err != nil {
		return err
	}
	if err := c.verifyCleanupAbsent(ctx, record); err != nil {
		return c.poison(err)
	}
	if record.SuccessorAdmission != nil {
		if reflect.DeepEqual(*record.SuccessorAdmission, generation) {
			return nil
		}
		return fmt.Errorf("generation admission cannot change its durable response")
	}
	record.SuccessorAdmission = copyGeneration(&generation)
	return c.persist(ctx, record)
}

func (c *Controller) pendingGenerationHandoff() bool {
	for _, admission := range c.admissions {
		if admission.Generation == nil && !admission.FinalAcknowledged {
			return true
		}
	}
	for _, record := range c.records {
		if record.TransitionIntent != nil && record.SuccessorAdmission == nil && !record.FinalAcknowledged {
			return true
		}
	}
	return false
}

func (c *Controller) generationCleanupReceipt(record journalRecord) taskpayload.JobGenerationCleanupReceipt {
	g := record.Generation
	if g == nil {
		return taskpayload.JobGenerationCleanupReceipt{}
	}
	return taskpayload.JobGenerationCleanupReceipt{
		Version: 1, PipelineRunID: record.PipelineRunID, JobRunID: record.JobRunID, AgentID: c.opts.AgentID,
		OriginalClaimID: record.ExecutionClaimID, RuntimeBindingDigest: record.RuntimeBindingDigest,
		GenerationID: g.GenerationID, GenerationBindingDigest: g.BindingDigest, RuntimeClaimID: g.RuntimeClaimID,
		SessionID: record.ID, Namespace: record.Resource.Namespace, ContainerID: record.Resource.ID,
		SnapshotID: record.Resource.ID + "-snapshot", Snapshotter: record.Resource.Snapshotter, ResourceOwnerDigest: record.Resource.Owner,
		WorkspaceID: record.Directory, WorkspaceDevice: record.Device, WorkspaceInode: record.Inode,
		ProcessesAbsent: true, ContainerAbsent: true, SnapshotAbsent: true, WorkspaceAbsent: true,
	}
}

// CleanupReceipt 仅在本代资源已独立验证消失且回执持久化后返回准确的结构化清理证据
// 旧版非代次会话不能生成可用于整作业重试的清理回执
func (s *Session) CleanupReceipt(ctx context.Context) (taskpayload.JobGenerationCleanupReceipt, error) {
	if s.controller == nil {
		return taskpayload.JobGenerationCleanupReceipt{}, fmt.Errorf("legacy session has no durable generation cleanup evidence")
	}
	c := s.controller
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return taskpayload.JobGenerationCleanupReceipt{}, err
	}
	record, ok := c.records[s.recordID]
	if !ok || record.Generation == nil || record.Phase != phaseClosed || record.CleanupReceipt == nil {
		return taskpayload.JobGenerationCleanupReceipt{}, fmt.Errorf("generation cleanup is not durably complete")
	}
	disk, err := c.readJournal(ctx, record.ID+".json")
	if err != nil || disk == nil || !reflect.DeepEqual(*disk, record) {
		return taskpayload.JobGenerationCleanupReceipt{}, c.poison(fmt.Errorf("generation cleanup journal changed"))
	}
	if err := c.verifyCleanupAbsent(ctx, record); err != nil {
		return taskpayload.JobGenerationCleanupReceipt{}, c.poison(err)
	}
	return *record.CleanupReceipt, nil
}

func validateGenerationObservation(request taskpayload.JobGenerationTransition, generation taskpayload.JobGeneration) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if err := generation.Validate(); err != nil {
		return err
	}
	i := request.Identity
	if generation.ExecutionAllowed || generation.PreparationOnly && (generation.Number != 1 || generation.InputManifestDigest != "") || generation.PipelineID != i.PipelineID || generation.PipelineRunID != i.PipelineRunID ||
		generation.JobRunID != i.JobRunID || generation.AgentID != i.AgentID || generation.OriginalClaimID != i.ClaimID ||
		generation.RuntimeBindingDigest != i.RuntimeBindingDigest || generation.SourceBindingDigest != i.SourceBindingDigest || generation.RetryPlanDigest != request.RetryPlanDigest {
		return fmt.Errorf("generation observation does not match the exact unresolved request")
	}
	return nil
}

func (c *Controller) recordGenerationObservation(ctx context.Context, request taskpayload.JobGenerationTransition, generation taskpayload.JobGeneration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return err
	}
	if err := validateGenerationObservation(request, generation); err != nil {
		return err
	}
	if request.Operation == "admit" {
		a, exists := c.admissions[admissionFilename(request)]
		if !exists || !reflect.DeepEqual(a.Request, request) {
			return fmt.Errorf("generation observation has no exact durable intent")
		}
		if a.Observation != nil && !reflect.DeepEqual(*a.Observation, generation) {
			return fmt.Errorf("generation observation changed; explicit reconciliation required")
		}
		a.Observation = copyGeneration(&generation)
		return c.persistAdmission(ctx, a)
	}
	record, err := c.generationRecord(ctx, request)
	if err != nil {
		return err
	}
	if record.TransitionIntent == nil || !reflect.DeepEqual(*record.TransitionIntent, request) {
		return fmt.Errorf("generation observation has no exact durable intent")
	}
	if record.GenerationObservation != nil && !reflect.DeepEqual(*record.GenerationObservation, generation) {
		return fmt.Errorf("generation observation changed; explicit reconciliation required")
	}
	record.GenerationObservation = copyGeneration(&generation)
	return c.persist(ctx, record)
}
