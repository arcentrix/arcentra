// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func claimValidatorSchedule(t *testing.T, f *rerunSealFixture) (*model.CopilotRerunSchedule, []byte) {
	t.Helper()
	sealed, err := f.build(t)
	require.NoError(t, err)
	b := sealed.Binding
	digest, err := b.Digest()
	require.NoError(t, err)
	plain, err := json.Marshal(sealed.Payload)
	require.NoError(t, err)
	now := time.Now()
	return &model.CopilotRerunSchedule{ScheduleID: b.ScheduleID, IntentID: f.intent.IntentID, OwnerUserID: f.intent.OwnerUserID, ProjectID: f.intent.ProjectID, PipelineID: f.intent.PipelineID, RunID: b.RunID, JobRunID: b.JobRunID, AgentID: b.AgentID, State: repo.CopilotRerunScheduleSealed, SealID: &b.SealID, SealDigest: digest, PayloadDigest: b.PayloadDigest, CipherVersion: b.CipherVersion, CiphertextDigest: b.CiphertextDigest, SecretVersionsDigest: b.SecretVersionsDigest, PayloadCiphertext: sealed.Ciphertext, ExpiresAt: f.intent.ExpiresAt, SealedAt: &now}, plain
}
func callClaimValidator(f *rerunSealFixture, validator repo.CopilotRerunClaimValidator, schedule *model.CopilotRerunSchedule) (*repo.CopilotRerunClaimMaterial, error) {
	var result *repo.CopilotRerunClaimMaterial
	err := f.db.Transaction(func(tx *gorm.DB) error {
		view, current := repo.NewScopedAuthorizationView(tx)
		var err error
		result, err = validator(context.Background(), view, f.scope, f.snapshot, f.evidence, f.ids, f.intent, f.agent, schedule)
		if err != nil {
			return err
		}
		return current(context.Background())
	})
	return result, err
}
func TestCopilotRerunClaimValidatorOriginalBytesAndFrozenConfiguration(t *testing.T) {
	f := newRerunSealFixture(t)
	schedule, plain := claimValidatorSchedule(t, f)
	ciphertext := bytes.Clone(schedule.PayloadCiphertext)
	validator, err := newCopilotRerunClaimValidator(f.policy, rerunSnapshotPolicy(), f.key)
	require.NoError(t, err)
	// Startup inputs are copied; live eligibility is still read on each callback.
	f.key.encryptKey[0] ^= 1
	f.policy.Candidates[0] = "mutated-after-construction"
	for _, state := range []string{repo.CopilotRerunScheduleSealed, repo.CopilotRerunScheduleClaimed} {
		schedule.State = state
		got, err := callClaimValidator(f, validator, schedule)
		require.NoError(t, err)
		require.Equal(t, plain, got.PayloadJSON)
		require.Equal(t, f.intent.ReviewedRunContextDigest, got.Reviewed.RunContextDigest)
		require.Equal(t, ciphertext, schedule.PayloadCiphertext)
		got.PayloadJSON[0] = 'x'
	}
	require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "reviewer-grant").Update("is_enabled", 0).Error)
	got, err := callClaimValidator(f, validator, schedule)
	require.Error(t, err)
	require.Nil(t, got)
}
func TestCopilotRerunClaimValidatorRejectsDrift(t *testing.T) {
	for _, kind := range []string{"policy", "source", "definition-config", "new-run", "step-id", "snapshot-cipher", "dispatch-cipher", "seal-digest", "payload-digest", "schedule-agent", "schedule-owner", "schedule-intent", "schedule-run", "schedule-expiry", "cancel", "completed", "pending", "missing-seal", "wrong-key", "owner-revoked"} {
		t.Run(kind, func(t *testing.T) {
			f := newRerunSealFixture(t)
			schedule, _ := claimValidatorSchedule(t, f)
			switch kind {
			case "policy":
				f.policy.TimeoutSeconds++
			case "source":
				f.evidence.SourceCommit = strings.Repeat("f", 40)
			case "definition-config":
				f.scope.Pipeline.PipelineFilePath = "other.yml"
			case "new-run":
				f.ids.RunID = "11111111-1111-4111-8111-111111111111"
			case "step-id":
				f.ids.Steps[0].Attempts[0] = "11111111-1111-4111-8111-111111111111"
			case "snapshot-cipher":
				f.snapshot.PayloadCiphertext[0] ^= 1
			case "dispatch-cipher":
				schedule.PayloadCiphertext[0] ^= 1
			case "seal-digest":
				schedule.SealDigest = "v1:" + strings.Repeat("a", 64)
			case "payload-digest":
				schedule.PayloadDigest = "v1:" + strings.Repeat("a", 64)
			case "schedule-agent":
				schedule.AgentID = "foreign"
			case "schedule-owner":
				schedule.OwnerUserID = "foreign"
			case "schedule-intent":
				schedule.IntentID = "foreign"
			case "schedule-run":
				schedule.RunID = "11111111-1111-4111-8111-111111111111"
			case "schedule-expiry":
				schedule.ExpiresAt = schedule.ExpiresAt.Add(time.Second)
			case "cancel":
				schedule.CancelRequested = true
			case "completed":
				now := time.Now()
				schedule.CompletedAt = &now
			case "pending":
				schedule.State = repo.CopilotRerunSchedulePending
			case "missing-seal":
				schedule.SealID = nil
			case "wrong-key":
				f.key.encryptKey[0] ^= 1
			case "owner-revoked":
				require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Update("is_enabled", 0).Error)
			}
			validator, err := newCopilotRerunClaimValidator(f.policy, rerunSnapshotPolicy(), f.key)
			require.NoError(t, err)
			got, err := callClaimValidator(f, validator, schedule)
			require.Error(t, err)
			require.Nil(t, got)
		})
	}
}
func TestCopilotRerunClaimValidatorConfigurationFailsClosed(t *testing.T) {
	f := newRerunSealFixture(t)
	for _, kind := range []string{"key", "policy", "runtime", "approval"} {
		t.Run(kind, func(t *testing.T) {
			key, policy, runtime := f.key, f.policy, rerunSnapshotPolicy()
			switch kind {
			case "key":
				key = nil
			case "policy":
				policy = nil
			case "runtime":
				runtime.Enabled = false
			case "approval":
				runtime.AllowWholeJobRetryApproval = false
			}
			validator, err := newCopilotRerunClaimValidator(policy, runtime, key)
			require.Error(t, err)
			require.Nil(t, validator)
		})
	}
}

func TestCopilotRerunClaimValidatorRejectsAuthenticatedDifferentMaterial(t *testing.T) {
	f := newRerunSealFixture(t)
	schedule, _ := claimValidatorSchedule(t, f)
	binding := &repo.CopilotRerunSealBinding{Version: 1, SealID: *schedule.SealID, ScheduleID: schedule.ScheduleID, RunID: schedule.RunID, JobRunID: schedule.JobRunID, AgentID: schedule.AgentID, PayloadDigest: schedule.PayloadDigest, CipherVersion: schedule.CipherVersion, CiphertextDigest: schedule.CiphertextDigest, SecretVersionsDigest: schedule.SecretVersionsDigest, ExpiresAtUnixMilli: schedule.ExpiresAt.UnixMilli()}
	payload, err := openCopilotRerunDispatch(f.intent.ProjectID, f.intent.PipelineID, binding, schedule.PayloadCiphertext, f.key)
	require.NoError(t, err)
	// An internally coherent authenticated payload is still not a substitute for
	// the original frozen definition/current reviewed material.
	payload.Env["UNREVIEWED"] = "changed"
	payload.RetryPlan, err = taskqueue.NewJobRetryPlan(payload)
	require.NoError(t, err)
	payload.RetryPlanDigest, err = payload.RetryPlan.Digest(payload.PipelineID, payload.PipelineRunID, payload.JobRunID, payload.AgentID, payload.Runtime.BindingDigest)
	require.NoError(t, err)
	binding.PayloadDigest, err = repo.CopilotRerunPayloadDigest(payload)
	require.NoError(t, err)
	binding.CiphertextDigest = ""
	content, err := binding.ContentBinding()
	require.NoError(t, err)
	plain, err := json.Marshal(payload)
	require.NoError(t, err)
	cipher, err := newCopilotRerunCipher(f.key.encryptKey)
	require.NoError(t, err)
	encrypted, err := cipher.seal(copilotRerunDispatchCipherBinding(f.intent.ProjectID, f.intent.PipelineID, binding, content), plain)
	require.NoError(t, err)
	final, err := repo.FinalizeCopilotRerunSeal(binding, content, encrypted)
	require.NoError(t, err)
	schedule.PayloadCiphertext = encrypted
	schedule.PayloadDigest = final.PayloadDigest
	schedule.CiphertextDigest = final.CiphertextDigest
	schedule.SealDigest, err = final.Digest()
	require.NoError(t, err)
	_, err = openCopilotRerunDispatch(f.intent.ProjectID, f.intent.PipelineID, final, encrypted, f.key)
	require.NoError(t, err)
	validator, err := newCopilotRerunClaimValidator(f.policy, rerunSnapshotPolicy(), f.key)
	require.NoError(t, err)
	got, err := callClaimValidator(f, validator, schedule)
	require.ErrorIs(t, err, repo.ErrCopilotRerunConflict)
	require.Nil(t, got)
}

func TestCopilotRerunClaimValidatorRetainsOriginalUploadPayload(t *testing.T) {
	f := newRerunSealFixtureWithSpec(t, nil, func(parsed *pipelinev1.Spec) {
		parsed.Jobs[0].Steps[0].Action = "run"
		parsed.Jobs[0].Steps = append(parsed.Jobs[0].Steps, &pipelinev1.Step{Name: "publish", Uses: "builtin:artifacts", Action: "upload", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"paths": []any{"dist/result.bin"}})})
	})
	schedule, original := claimValidatorSchedule(t, f)
	validator, err := newCopilotRerunClaimValidator(f.policy, rerunSnapshotPolicy(), f.key)
	require.NoError(t, err)
	got, err := callClaimValidator(f, validator, schedule)
	require.NoError(t, err)
	require.Equal(t, original, got.PayloadJSON)
	var payload taskqueue.JobRunTaskPayload
	require.NoError(t, json.Unmarshal(got.PayloadJSON, &payload))
	require.Equal(t, "run", payload.Steps[0].Action)
	require.Equal(t, "builtin:artifacts", payload.Steps[1].Uses)
	require.Equal(t, []any{"dist/result.bin"}, payload.Steps[1].Args["paths"])
}
