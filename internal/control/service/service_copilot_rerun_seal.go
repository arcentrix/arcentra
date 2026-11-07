// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	sharedpipeline "github.com/arcentrix/arcentra/internal/shared/pipeline/materialize"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

// Seal alone creates a new encrypted payload. Current review and deterministic
// materialization are shared with claim validation, which never reseals.
func newCopilotRerunSealBuilder(config *retryapproval.Policy, runtime taskqueue.OfflineRuntimePolicy, secrets *SecretService) (repo.CopilotRerunSealBuilder, error) {
	materialize, err := newCopilotRerunMaterializer(config, runtime, secrets)
	if err != nil {
		return nil, err
	}
	cipher, err := newCopilotRerunCipher(secrets.encryptKey)
	if err != nil {
		return nil, repo.ErrCopilotRerunUnsupported
	}
	return func(ctx context.Context, view *repo.Repositories, scope *repo.CopilotRerunScope, snapshot *model.CopilotDefinitionSnapshot, evidence *repo.CopilotRerunOriginalEvidence, ids *repo.CopilotRerunExecutionIdentities, intent *model.CopilotRerunIntent, agent *model.Agent, sealID string) (*repo.CopilotRerunSealedPayload, error) {
		fail := func() (*repo.CopilotRerunSealedPayload, error) { return nil, repo.ErrCopilotRerunUnsupported }
		material, err := materialize(ctx, view, scope, snapshot, evidence, ids, intent, agent, sealID)
		if err != nil {
			return nil, err
		}
		payload := material.Payload

		payloadDigest, err := repo.CopilotRerunPayloadDigest(payload)
		if err != nil {
			return fail()
		}
		identity := &repo.CopilotRerunSealBinding{Version: 1, SealID: sealID, ScheduleID: ids.ScheduleID, RunID: ids.RunID, JobRunID: ids.JobRunID, AgentID: agent.AgentID, CipherVersion: 1, SecretVersionsDigest: repo.CopilotRerunEmptySecretVersionsDigest(), ExpiresAtUnixMilli: intent.ExpiresAt.UnixMilli()}
		metadata, err := repo.BindCopilotRerunSealPayload(identity, payloadDigest)
		if err != nil {
			return fail()
		}
		content, err := metadata.ContentBinding()
		if err != nil {
			return fail()
		}
		plain, err := json.Marshal(payload)
		if err != nil {
			return fail()
		}
		encrypted, err := cipher.seal(copilotRerunDispatchCipherBinding(intent.ProjectID, intent.PipelineID, metadata, content), plain)
		if err != nil {
			return fail()
		}
		final, err := repo.FinalizeCopilotRerunSeal(metadata, content, encrypted)
		if err != nil || ctx.Err() != nil {
			return fail()
		}
		return &repo.CopilotRerunSealedPayload{Reviewed: material.Reviewed, Payload: payload, Binding: final, Ciphertext: encrypted}, nil
	}, nil
}

func copilotRerunDispatchCipherBinding(project, pipeline string, b *repo.CopilotRerunSealBinding, content string) copilotRerunCipherBinding {
	return copilotRerunCipherBinding{Version: 1, Role: "dispatch", ProjectID: project, PipelineID: pipeline, RunID: b.RunID, ObjectID: b.SealID, ContentBinding: content}
}

// Strict private integrity check, not an authorization or payload-disclosure API.
func openCopilotRerunDispatch(project, pipeline string, b *repo.CopilotRerunSealBinding, encrypted []byte, key *SecretService) (*taskqueue.JobRunTaskPayload, error) {
	payload, _, err := openCopilotRerunDispatchBytes(project, pipeline, b, encrypted, key)
	return payload, err
}

// Returns original authenticated canonical plaintext; validation serialization
// is never substituted for these bytes at a disclosure boundary.
func openCopilotRerunDispatchBytes(project, pipeline string, b *repo.CopilotRerunSealBinding, encrypted []byte, key *SecretService) (*taskqueue.JobRunTaskPayload, []byte, error) {
	fail := func() (*taskqueue.JobRunTaskPayload, []byte, error) { return nil, nil, errCopilotRerunCipher }
	if b == nil || key == nil {
		return fail()
	}
	content, err := b.ContentBinding()
	if err != nil {
		return fail()
	}
	metadata := *b
	metadata.CiphertextDigest = ""
	final, err := repo.FinalizeCopilotRerunSeal(&metadata, content, encrypted)
	if err != nil || final.CiphertextDigest != b.CiphertextDigest {
		return fail()
	}
	if _, err = b.Digest(); err != nil {
		return fail()
	}
	cipher, err := newCopilotRerunCipher(key.encryptKey)
	if err != nil {
		return fail()
	}
	plain, err := cipher.open(copilotRerunDispatchCipherBinding(project, pipeline, b, content), encrypted)
	if err != nil {
		return fail()
	}
	var payload taskqueue.JobRunTaskPayload
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&payload) != nil || decoder.Decode(new(any)) != io.EOF {
		return fail()
	}
	canonical, err := json.Marshal(&payload)
	if err != nil || !bytes.Equal(canonical, plain) || taskqueue.ValidateJobRetryPlan(&payload) != nil {
		return fail()
	}
	digest, err := repo.CopilotRerunPayloadDigest(&payload)
	if err != nil || digest != b.PayloadDigest || payload.PipelineID != pipeline || payload.PipelineRunID != b.RunID || payload.JobRunID != b.JobRunID || payload.AgentID != b.AgentID || payload.SourceExpectation == nil || payload.SourceExpectation.SealID != b.SealID || payload.SourceExpectation.ScheduleID != b.ScheduleID {
		return fail()
	}
	if _, err = payload.SourceExpectation.Canonical(); err != nil {
		return fail()
	}
	return &payload, plain, nil
}

func copilotRerunSealAgentEligible(agent *model.Agent, job *pipelinev1.Job) bool {
	if agent == nil || job == nil || agent.IsEnabled != 1 || (agent.Status != 1 && agent.Status != 4) || agent.LastHeartbeat == nil || time.Since(*agent.LastHeartbeat) > 2*time.Minute || agent.OS != "linux" {
		return false
	}
	var caps taskqueue.AgentRuntimeCapabilities
	if json.Unmarshal(agent.RuntimeCapabilities, &caps) != nil || caps.ExecutionMode != taskqueue.RuntimeContainerdOfflineV1 || !caps.ContainerdOfflineV1 || !caps.ContainerdWholeJobRetryV1 || !caps.ContainerdRetryApprovalV1 || !caps.CopilotWholeJobRerunPreparationV1 {
		return false
	}
	labels := map[string]string{}
	if len(agent.Labels) > 0 && json.Unmarshal(agent.Labels, &labels) != nil {
		return false
	}
	if labels == nil {
		labels = map[string]string{}
	}
	labels["os"], labels["arch"] = agent.OS, agent.Arch
	for _, step := range job.Steps {
		if step == nil || !sharedpipeline.MatchesAgentSelector(labels, step.AgentSelector) {
			return false
		}
	}
	return true
}
