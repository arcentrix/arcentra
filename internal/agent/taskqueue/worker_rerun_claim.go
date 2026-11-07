// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/internal/shared/storage"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// Only the manager can mint a token registered against its exact entry. A token
// is consumed before all preparation, and cannot be imported after restart.
type rerunStartToken struct {
	owner     *rerunIncarnation
	handle    *rerunAdmissionHandle
	artifacts *rerunArtifactStorage
}

func (m *rerunIncarnation) takeStartToken(h *rerunAdmissionHandle) (*rerunStartToken, error) {
	if err := m.takeStart(h); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, err := m.entryLocked(h)
	if err != nil {
		return nil, err
	}
	t := &rerunStartToken{owner: m, handle: h, artifacts: e.artifactStorage}
	e.startToken = t
	return t, nil
}
func (t *rerunStartToken) consume(p *taskpayload.JobRunTaskPayload) (string, error) {
	if t == nil || t.owner == nil || t.owner.mu == nil || p == nil {
		return "", errRerunIncarnationInvalid
	}
	m := t.owner
	m.mu.Lock()
	defer m.mu.Unlock()
	e, err := m.entryLocked(t.handle)
	if err != nil || e.startToken != t || e.startConsumed || e.phase != rerunStartOwned {
		return "", errRerunIncarnationInvalid
	}
	e.startConsumed = true
	id, fingerprint := "", ""
	if t.artifacts != nil {
		id, fingerprint = t.artifacts.reference.StorageID, t.artifacts.reference.Fingerprint
	}
	if id != e.binding.ArtifactStorageID || fingerprint != e.binding.ArtifactStorageFingerprint || (id != "" && (t.artifacts == nil || t.artifacts.instance == nil)) {
		return "", errRerunIncarnationInvalid
	}
	body, err := json.Marshal(p)
	if err != nil || taskpayload.RerunPayloadBytesDigest(body) != e.binding.PayloadDigest {
		return "", errRerunIncarnationInvalid
	}
	return e.claimID, nil
}
func rerunBinding(ref taskpayload.RerunClaimReference) rerunAdmissionBinding {
	return rerunAdmissionBinding{AgentID: ref.AgentID, PipelineID: ref.PipelineID, RunID: ref.RunID, JobRunID: ref.JobRunID, GateID: ref.GateID, ScheduleID: ref.ScheduleID, SealID: ref.SealID, SealDigest: ref.SealDigest, PayloadDigest: ref.PayloadDigest}
}
func rerunWireReference(ref taskpayload.RerunClaimReference) *agentv1.CopilotRerunClaimReference {
	return &agentv1.CopilotRerunClaimReference{AgentId: ref.AgentID, PipelineId: ref.PipelineID, RunId: ref.RunID, JobRunId: ref.JobRunID, GateId: ref.GateID, ScheduleId: ref.ScheduleID, SealId: ref.SealID, SealDigest: ref.SealDigest, PayloadDigest: ref.PayloadDigest}
}
func verifyRerunWireReceipt(ref taskpayload.RerunClaimReference, claim string, response *agentv1.ClaimAndFetchCopilotRerunResponse) error {
	if response == nil || response.Receipt == nil || response.Receipt.Reference == nil {
		return errRerunIncarnationUnconfirmed
	}
	r := response.Receipt.Reference
	actual := taskpayload.RerunClaimReference{AgentID: r.AgentId, PipelineID: r.PipelineId, RunID: r.RunId, JobRunID: r.JobRunId, GateID: r.GateId, ScheduleID: r.ScheduleId, SealID: r.SealId, SealDigest: r.SealDigest, PayloadDigest: r.PayloadDigest}
	if actual != ref || response.Receipt.ClaimId != claim || response.Receipt.ClaimedAtUnixMilli <= 0 {
		return errRerunIncarnationUnconfirmed
	}
	return nil
}

// handleRerunReference never publishes new work. Its caller acknowledges only
// receipt of this reference notification, not claim completion or execution.
// After the bounded retry budget, claimed/unknown remains unknown. A dispatcher
// must never manufacture another Agent/claim; only still-unclaimed schedules may
// notify again with the same immutable reference. Outbox contents are untouched.
func handleRerunReference(ctx context.Context, conf *config.AgentConfig, client *agentgrpc.ClientWrapper, manager *executor.Manager, reports *terminalOutbox, incarnation *rerunIncarnation, body []byte) (resultErr error) {
	// Callback panics must not leak private payload/nonce through queue logging.
	// The manager's own deferred cleanup preserves pending or started-unknown.
	defer func() {
		if recover() != nil {
			resultErr = errRerunIncarnationUnconfirmed
		}
	}()

	if client == nil || !client.ControllerAuthenticated() || client.AgentClient == nil {
		return errRerunIncarnationUnconfirmed
	}
	ref, err := taskpayload.ParseRerunClaimReference(body)
	if err != nil || conf == nil || !conf.Agent.Sandbox.Containerd.TerminalRerunEnabled || conf.Agent.ID != ref.AgentID || incarnation == nil || reports == nil {
		return errRerunIncarnationInvalid
	}
	if err := reports.replayBound(ctx, client, conf); err != nil {
		return errRerunIncarnationUnconfirmed
	}
	runtime := offlineRuntimeFromContext(ctx)
	if runtime == nil || !runtime.Ready() {
		return errRerunIncarnationUnconfirmed
	}
	handle, err := reserveRerunStorage(incarnation, *ref)
	if err != nil {
		return err
	}
	payload, err := fetchRerunClaim(ctx, conf, client, incarnation, handle, *ref)
	if err != nil {
		return err
	}
	token, err := incarnation.takeStartToken(handle)
	if err != nil {
		return err
	}
	// A panic/error after ownership never restores start eligibility. Existing
	// runtime journals and terminal outbox retain their normal recovery semantics.
	known := false
	defer func() { _ = incarnation.finish(handle, known) }()
	err = executeJobRunBody(ctx, conf, client, manager, payload, token)
	var completed *completedExecutionError
	known = err == nil || errors.As(err, &completed)
	return acknowledgeCompleted(err)
}

func fetchRerunClaim(ctx context.Context, conf *config.AgentConfig, client *agentgrpc.ClientWrapper, incarnation *rerunIncarnation, handle *rerunAdmissionHandle, ref taskpayload.RerunClaimReference) (*taskpayload.JobRunTaskPayload, error) {
	if client == nil || !client.ControllerAuthenticated() || client.AgentClient == nil || conf == nil || conf.Agent.ID != ref.AgentID {
		return nil, errRerunIncarnationUnconfirmed
	}
	incarnation.mu.Lock()
	entry, entryErr := incarnation.entryLocked(handle)
	if entryErr != nil {
		incarnation.mu.Unlock()
		return nil, errRerunIncarnationInvalid
	}
	binding, outputStorage := entry.binding, entry.artifactStorage
	incarnation.mu.Unlock()
	if binding != rerunBindingWithStorage(ref, binding.ArtifactStorageID, binding.ArtifactStorageFingerprint) {
		return nil, errRerunIncarnationInvalid
	}
	var payload *taskpayload.JobRunTaskPayload
	for attempt := 0; attempt < 3; attempt++ {
		retryable := false
		err := incarnation.withClaim(handle, func(window rerunIncarnationRequest) error {
			return window.withNonce(func(claim string, nonce []byte) error {
				rpcCtx, cancel := client.WithTimeoutAndAuth(ctx)
				defer cancel()
				response, err := client.AgentClient.ClaimAndFetchCopilotRerun(rpcCtx, &agentv1.ClaimAndFetchCopilotRerunRequest{Reference: rerunWireReference(ref), ClaimId: claim, AdmissionNonce: nonce, ArtifactStorageId: binding.ArtifactStorageID, ArtifactStorageFingerprint: binding.ArtifactStorageFingerprint})
				if err != nil {
					retryable = grpcstatus.Code(err) == codes.Unavailable || grpcstatus.Code(err) == codes.DeadlineExceeded
					return errRerunIncarnationUnconfirmed
				}
				if err := verifyRerunWireReceipt(ref, claim, response); err != nil {
					return err
				}
				receipt := response.Receipt
				if !(taskpayload.RerunClaimReceipt{Reference: ref, ClaimID: receipt.ClaimId, ClaimedAtUnixMilli: receipt.ClaimedAtUnixMilli, ArtifactStorageID: receipt.ArtifactStorageId, ArtifactStorageFingerprint: receipt.ArtifactStorageFingerprint, ArtifactStorageType: receipt.ArtifactStorageType}).Matches(taskpayload.RerunClaimRequest{Reference: ref, ClaimID: claim, ArtifactStorageID: binding.ArtifactStorageID, ArtifactStorageFingerprint: binding.ArtifactStorageFingerprint}) {
					return errRerunIncarnationUnconfirmed
				}
				decoded, err := taskpayload.ParseRerunClaimPayload(ref, response.PayloadJson)
				if err != nil {
					return errRerunIncarnationUnconfirmed
				}
				if !rerunOutputStorageReady(decoded, outputStorage) {
					return errRerunIncarnationUnconfirmed
				}
				local, structural := offlineJobValidation(ctx, conf, decoded)
				if local != nil || structural != nil {
					return errRerunIncarnationUnconfirmed
				}
				if !client.ControllerAuthenticated() || ctx.Err() != nil {
					return errRerunIncarnationUnconfirmed
				}
				if err := incarnation.verifyReceipt(handle, window, binding, claim); err != nil {
					return err
				}
				payload = decoded
				return nil
			})
		})
		if err == nil {
			return payload, nil
		}
		if !retryable || attempt == 2 {
			return nil, err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, errRerunIncarnationUnconfirmed
		case <-timer.C:
		}
	}
	return nil, fmt.Errorf("rerun admission outcome requires inspection")
}

// Capture the already-loaded instance and immutable public reference together.
// A later default switch cannot redirect this execution to a different store.
type rerunArtifactStorage struct {
	instance  storage.IStorage
	reference taskpayload.ArtifactReference
}

func rerunBindingWithStorage(ref taskpayload.RerunClaimReference, id, fingerprint string) rerunAdmissionBinding {
	b := rerunBinding(ref)
	b.ArtifactStorageID = id
	b.ArtifactStorageFingerprint = fingerprint
	return b
}
func reserveRerunStorage(m *rerunIncarnation, ref taskpayload.RerunClaimReference) (*rerunAdmissionHandle, error) {
	storageMu.RLock()
	captured := &rerunArtifactStorage{instance: storageInstance, reference: storageReference}
	storageMu.RUnlock()
	if taskpayload.ValidateRerunArtifactStorage(captured.reference.StorageID, captured.reference.Fingerprint) != nil || (captured.instance == nil) != (captured.reference.StorageID == "") {
		return nil, errRerunIncarnationInvalid
	}
	h, err := m.reserve(rerunBindingWithStorage(ref, captured.reference.StorageID, captured.reference.Fingerprint))
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, err := m.entryLocked(h)
	if err != nil {
		return nil, err
	}
	if e.artifactStorage == nil {
		e.artifactStorage = captured
	}
	return h, nil
}
func rerunOutputStorageReady(p *taskpayload.JobRunTaskPayload, st *rerunArtifactStorage) bool {
	for _, step := range p.Steps {
		if taskpayload.RerunArtifactUploadStep(step.Uses, step.Action) {
			if st == nil || st.instance == nil || st.reference.StorageID == "" {
				return false
			}
			if _, ok := st.instance.(storage.IStreamingStorage); !ok {
				return false
			}
		}
	}
	return true
}
