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

	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"github.com/google/uuid"
)

// generationAdmission 持久保存首次准入交接，不声称任何运行时资源已经分配
// 该记录与会话共享独占根和失败关闭规则，直到真实最终作业 ACK 才删除
type generationAdmission struct {
	Request           taskpayload.JobGenerationTransition `json:"request"`
	Generation        *taskpayload.JobGeneration          `json:"generation,omitempty"`
	Observation       *taskpayload.JobGeneration          `json:"observation,omitempty"`
	FinalAcknowledged bool                                `json:"finalAcknowledged,omitempty"`
}
type generationAdmissionDocument struct {
	Version           int                 `json:"version"`
	ControllerID      string              `json:"controllerID"`
	AgentID           string              `json:"agentID"`
	WorkspaceRoot     string              `json:"workspaceRoot"`
	Namespace         string              `json:"namespace"`
	RuntimeIOIdentity string              `json:"runtimeIOIdentity,omitempty"`
	Admission         generationAdmission `json:"admission"`
	Checksum          string              `json:"checksum"`
}

func admissionFilename(request taskpayload.JobGenerationTransition) string {
	identity, _ := sonic.Marshal(request.Identity)
	return "admission-" + uuid.NewSHA1(uuid.NameSpaceOID, identity).String() + ".json"
}
func admissionFilenameForGeneration(g taskpayload.JobGeneration) string {
	return admissionFilename(taskpayload.JobGenerationTransition{Identity: taskpayload.JobInputIdentity{
		PipelineID: g.PipelineID, PipelineRunID: g.PipelineRunID, JobRunID: g.JobRunID, AgentID: g.AgentID, ClaimID: g.OriginalClaimID,
		RuntimeBindingDigest: g.RuntimeBindingDigest, SourceBindingDigest: g.SourceBindingDigest}})
}
func encodeAdmission(d generationAdmissionDocument) ([]byte, error) {
	d.Checksum = ""
	body, err := sonic.Marshal(d)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	d.Checksum = hex.EncodeToString(sum[:])
	return sonic.Marshal(d)
}
func (c *Controller) admissionDocument(a generationAdmission) generationAdmissionDocument {
	return generationAdmissionDocument{Version: generationJournalVersion, ControllerID: c.opts.ControllerID, AgentID: c.opts.AgentID, WorkspaceRoot: c.opts.WorkspaceRoot, Namespace: c.backend.Namespace(), RuntimeIOIdentity: c.runtimeIOIdentity, Admission: a}
}
func (c *Controller) readAdmission(ctx context.Context, name string) (*generationAdmission, error) {
	data, err := c.readJournalBytes(ctx, name)
	if err != nil {
		return nil, err
	}
	var doc generationAdmissionDocument
	if err := sonic.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("invalid generation admission encoding")
	}
	canonical, err := encodeAdmission(doc)
	if err != nil || !bytes.Equal(data, canonical) {
		return nil, fmt.Errorf("generation admission checksum or canonical encoding mismatch")
	}
	expected := c.admissionDocument(doc.Admission)
	if doc.Version != expected.Version || doc.ControllerID != expected.ControllerID || doc.AgentID != expected.AgentID || doc.WorkspaceRoot != expected.WorkspaceRoot || doc.Namespace != expected.Namespace || doc.RuntimeIOIdentity != expected.RuntimeIOIdentity {
		return nil, fmt.Errorf("generation admission belongs to another owner or runtime")
	}
	if err := c.validateAdmission(doc.Admission); err != nil {
		return nil, err
	}
	return &doc.Admission, nil
}
func (c *Controller) validateAdmission(a generationAdmission) error {
	if err := a.Request.Validate(); err != nil {
		return err
	}
	if a.Request.Operation != "admit" || a.Request.Identity.AgentID != c.opts.AgentID {
		return fmt.Errorf("initial generation admission identity is invalid")
	}
	if a.Observation != nil {
		if err := validateGenerationObservation(a.Request, *a.Observation); err != nil {
			return err
		}
	}
	if a.Generation == nil {
		return nil
	}
	g := *a.Generation
	if err := g.Validate(); err != nil {
		return err
	}
	i := a.Request.Identity
	if g.Number != 1 || g.PreviousGenerationID != "" || !generationAllocationAllowed(g) || g.PipelineID != i.PipelineID || g.PipelineRunID != i.PipelineRunID || g.JobRunID != i.JobRunID || g.AgentID != i.AgentID || g.OriginalClaimID != i.ClaimID || g.RuntimeBindingDigest != i.RuntimeBindingDigest || g.SourceBindingDigest != i.SourceBindingDigest || g.RetryPlanDigest != a.Request.RetryPlanDigest || g.InputManifestDigest != a.Request.InputManifestDigest {
		return fmt.Errorf("initial generation response does not match its exact durable intent")
	}
	return nil
}
func (c *Controller) persistAdmission(ctx context.Context, a generationAdmission) error {
	if err := c.validateAdmission(a); err != nil {
		return err
	}
	name := admissionFilename(a.Request)
	if previous, exists := c.admissions[name]; exists {
		disk, err := c.readAdmission(ctx, name)
		if err != nil || disk == nil || !reflect.DeepEqual(*disk, previous) {
			return c.poison(fmt.Errorf("generation admission changed unexpectedly"))
		}
	} else if _, err := c.journal.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		return c.poison(fmt.Errorf("generation admission collided with existing path"))
	}
	data, err := encodeAdmission(c.admissionDocument(a))
	if err != nil {
		return err
	}
	if err := c.writeJournalData(ctx, name, data); err != nil {
		return c.poison(err)
	}
	c.admissions[name] = a
	return nil
}
func (c *Controller) recordInitialIntent(ctx context.Context, request taskpayload.JobGenerationTransition) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return err
	}
	if c.reconciliationPending {
		return fmt.Errorf("startup admission cannot resume execution")
	}
	a := generationAdmission{Request: *copyGenerationTransition(&request)}
	if err := c.validateAdmission(a); err != nil {
		return err
	}
	name := admissionFilename(request)
	if old, exists := c.admissions[name]; exists {
		if err := c.verifyAdmissionStored(ctx, old); err != nil {
			return err
		}
		if reflect.DeepEqual(old.Request, request) && !old.FinalAcknowledged {
			return nil
		}
		return fmt.Errorf("initial generation intent cannot change")
	}
	if len(c.runtimeBlocked) != 0 || c.pendingGenerationHandoff() {
		return fmt.Errorf("offline runtime has unresolved cleanup or admission")
	}
	for _, old := range c.admissions {
		if old.Request.Identity.JobRunID == request.Identity.JobRunID || old.Request.Identity.ClaimID == request.Identity.ClaimID {
			return fmt.Errorf("generation admission job or claim is already recorded")
		}
	}
	for _, old := range c.records {
		if old.JobRunID == request.Identity.JobRunID || old.ExecutionClaimID == request.Identity.ClaimID {
			return fmt.Errorf("generation admission cannot replace a recorded execution")
		}
	}
	if len(c.records)+len(c.admissions) >= c.opts.MaxRecords {
		return fmt.Errorf("sandbox journal record quota exceeded")
	}
	return c.persistAdmission(ctx, a)
}
func (c *Controller) recordInitialAdmission(ctx context.Context, request taskpayload.JobGenerationTransition, generation taskpayload.JobGeneration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return err
	}
	if c.reconciliationPending {
		return fmt.Errorf("startup admission cannot resume execution")
	}
	a, exists := c.admissions[admissionFilename(request)]
	if !exists || a.FinalAcknowledged || !reflect.DeepEqual(a.Request, request) {
		return fmt.Errorf("initial generation response has no exact durable intent")
	}
	if err := c.verifyAdmissionStored(ctx, a); err != nil {
		return err
	}
	if a.Generation != nil {
		if reflect.DeepEqual(*a.Generation, generation) {
			return nil
		}
		return fmt.Errorf("initial generation response cannot change")
	}
	a.Generation = copyGeneration(&generation)
	return c.persistAdmission(ctx, a)
}
func (c *Controller) validateAdmissionHistory() error {
	jobs, claims := map[string]bool{}, map[string]bool{}
	generationIDs, runtimeClaims := map[string]bool{}, map[string]bool{}
	for _, a := range c.admissions {
		identity := a.Request.Identity
		if jobs[identity.JobRunID] || claims[identity.ClaimID] {
			return fmt.Errorf("journal has duplicate initial generation admissions")
		}
		jobs[identity.JobRunID], claims[identity.ClaimID] = true, true
		if a.Generation != nil {
			if generationIDs[a.Generation.GenerationID] || runtimeClaims[a.Generation.RuntimeClaimID] {
				return fmt.Errorf("initial admissions reuse a generation resource identity")
			}
			generationIDs[a.Generation.GenerationID], runtimeClaims[a.Generation.RuntimeClaimID] = true, true
		}
		for _, r := range c.records {
			if r.JobRunID != identity.JobRunID && r.ExecutionClaimID != identity.ClaimID {
				if a.Generation != nil && (a.Generation.RuntimeClaimID == r.ID || r.Generation != nil && a.Generation.GenerationID == r.Generation.GenerationID) {
					return fmt.Errorf("initial admission conflicts with another recorded resource owner")
				}
				continue
			}
			if r.Generation == nil || r.JobRunID != identity.JobRunID || r.ExecutionClaimID != identity.ClaimID || a.Generation == nil || !sameGenerationJob(*a.Generation, *r.Generation) {
				return fmt.Errorf("initial admission conflicts with recorded execution")
			}
		}
	}
	return nil
}
func (c *Controller) removeAdmission(ctx context.Context, name string) error {
	if err := c.verifyRoots(ctx); err != nil {
		return err
	}
	if err := c.fault(ctx, "admission.remove"); err != nil {
		return err
	}
	if err := c.journal.Remove(name); err != nil {
		return err
	}
	if err := c.syncJournal(ctx); err != nil {
		return err
	}
	delete(c.admissions, name)
	return nil
}

func (c *Controller) verifyAdmissionStored(ctx context.Context, admission generationAdmission) error {
	disk, err := c.readAdmission(ctx, admissionFilename(admission.Request))
	if err != nil || disk == nil || !reflect.DeepEqual(*disk, admission) {
		return c.poison(fmt.Errorf("generation admission journal changed"))
	}
	return nil
}
