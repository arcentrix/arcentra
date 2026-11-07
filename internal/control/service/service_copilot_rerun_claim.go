// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

// Internal transaction-local validation only. The repository owns Gate/claim
// fencing and final current-authority/deadline closure. The opt-in Agent claim
// bridge and dispatcher use this validator; construction alone installs neither.
func newCopilotRerunClaimValidator(config *retryapproval.Policy, runtime taskqueue.OfflineRuntimePolicy, secrets *SecretService) (repo.CopilotRerunClaimValidator, error) {
	materialize, err := newCopilotRerunMaterializer(config, runtime, secrets)
	if err != nil {
		return nil, err
	}
	key := &SecretService{encryptKey: slices.Clone(secrets.encryptKey)}
	return func(ctx context.Context, view *repo.Repositories, scope *repo.CopilotRerunScope, snapshot *model.CopilotDefinitionSnapshot, evidence *repo.CopilotRerunOriginalEvidence, ids *repo.CopilotRerunExecutionIdentities, intent *model.CopilotRerunIntent, agent *model.Agent, schedule *model.CopilotRerunSchedule) (*repo.CopilotRerunClaimMaterial, error) {
		if schedule == nil || schedule.SealID == nil || ids == nil || intent == nil || agent == nil ||
			schedule.ScheduleID != ids.ScheduleID || schedule.IntentID != intent.IntentID || schedule.OwnerUserID != intent.OwnerUserID || schedule.ProjectID != intent.ProjectID || schedule.PipelineID != intent.PipelineID || schedule.RunID != ids.RunID || schedule.JobRunID != ids.JobRunID || schedule.AgentID != agent.AgentID || schedule.ExpiresAt.UnixMilli() != intent.ExpiresAt.UnixMilli() || !time.Now().Before(schedule.ExpiresAt) || schedule.CancelRequested || schedule.CompletedAt != nil || (schedule.State != repo.CopilotRerunScheduleSealed && schedule.State != repo.CopilotRerunScheduleClaimed) {
			return nil, repo.ErrCopilotRerunConflict
		}
		binding := &repo.CopilotRerunSealBinding{Version: 1, SealID: *schedule.SealID, ScheduleID: schedule.ScheduleID, RunID: schedule.RunID, JobRunID: schedule.JobRunID, AgentID: schedule.AgentID, PayloadDigest: schedule.PayloadDigest, CipherVersion: schedule.CipherVersion, CiphertextDigest: schedule.CiphertextDigest, SecretVersionsDigest: schedule.SecretVersionsDigest, ExpiresAtUnixMilli: schedule.ExpiresAt.UnixMilli()}
		if repo.ValidateCopilotRerunSealedBinding(schedule, binding) != nil || binding.SecretVersionsDigest != repo.CopilotRerunEmptySecretVersionsDigest() {
			return nil, repo.ErrCopilotRerunConflict
		}
		// Reconstruct expected material only for validation under current policy.
		// Never return these reconstructed bytes or create a replacement ciphertext.
		expected, err := materialize(ctx, view, scope, snapshot, evidence, ids, intent, agent, binding.SealID)
		if err != nil {
			return nil, err
		}
		_, original, err := openCopilotRerunDispatchBytes(intent.ProjectID, intent.PipelineID, binding, schedule.PayloadCiphertext, key)
		if err != nil {
			return nil, repo.ErrCopilotRerunConflict
		}
		canonical, err := json.Marshal(expected.Payload)
		if err != nil || !bytes.Equal(canonical, original) || ctx.Err() != nil {
			return nil, repo.ErrCopilotRerunConflict
		}
		return &repo.CopilotRerunClaimMaterial{Reviewed: expected.Reviewed, PayloadJSON: original}, nil
	}, nil
}
