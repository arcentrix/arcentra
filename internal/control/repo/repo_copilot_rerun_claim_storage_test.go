// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func rerunClaimStorageSeed(t *testing.T, db *gorm.DB, in *CopilotRerunClaimInput) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&model.StorageConfig{}))
	detail := model.StorageConfigDetail{Endpoint: "https://synthetic.invalid", Bucket: "outputs", Region: "test", BasePath: "root", UseTLS: true, AccessKey: "synthetic-key", SecretKey: "synthetic-secret"}
	body, err := json.Marshal(detail)
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.StorageConfig{StorageID: "claim-store", StorageType: "s3", Config: body, IsEnabled: 1}).Error)
	in.ArtifactStorageID = "claim-store"
	in.ArtifactStorageFingerprint = artifactutil.StorageFingerprint("s3", detail.Endpoint, detail.Bucket, detail.Region, detail.BasePath, detail.UseTLS)
}

func TestCopilotRerunClaimPinsStorageAndHistoricalReceipt(t *testing.T) {
	r, saved, in := rerunClaimFixture(t)
	db := r.database.Database()
	ctx := context.Background()
	rerunClaimStorageSeed(t, db, &in)
	got, err := r.CopilotRerunClaim(ctx, in, permitRerun, testRerunClaimValidator)
	require.NoError(t, err)
	require.Equal(t, in.ArtifactStorageID, got.Receipt.ArtifactStorageID)
	require.Equal(t, in.ArtifactStorageFingerprint, got.Receipt.ArtifactStorageFingerprint)
	require.Equal(t, "s3", got.Receipt.ArtifactStorageType)
	var job model.JobRun
	require.NoError(t, db.Where("job_run_id = ?", in.JobRunID).First(&job).Error)
	require.Equal(t, got.Receipt.ArtifactStorageID, job.ArtifactStorageID)
	require.Equal(t, got.Receipt.ArtifactStorageFingerprint, job.ArtifactStorageFingerprint)
	require.Equal(t, "s3", job.ArtifactStorageType)
	body, err := json.Marshal(got.Receipt)
	require.NoError(t, err)
	require.NotContains(t, string(body), "synthetic")
	replay, err := r.CopilotRerunClaim(ctx, in, permitRerun, testRerunClaimValidator)
	require.NoError(t, err)
	require.Equal(t, got, replay)
	changed := in
	changed.ArtifactStorageID = "other-store"
	rejected, err := r.CopilotRerunClaim(ctx, changed, permitRerun, testRerunClaimValidator)
	require.Error(t, err)
	require.Nil(t, rejected)
	require.NoError(t, db.Model(&model.StorageConfig{}).Where("storage_id = ?", in.ArtifactStorageID).Update("is_enabled", 0).Error)
	rejected, err = r.CopilotRerunClaim(ctx, in, permitRerun, testRerunClaimValidator)
	require.Error(t, err)
	require.Nil(t, rejected)
	receipt, err := r.CopilotRerunInspectClaim(ctx, saved.OwnerUserID, saved.ProjectID, saved.IntentID, permitRerun)
	require.NoError(t, err)
	require.Equal(t, got.Receipt, receipt)
}

func TestCopilotRerunClaimStorageRejectsBeforeClaim(t *testing.T) {
	for _, which := range []string{"missing", "disabled", "fingerprint", "partial", "case", "malformed-config", "preexisting-pin", "unsupported-type"} {
		t.Run(which, func(t *testing.T) {
			r, _, in := rerunClaimFixture(t)
			db := r.database.Database()
			rerunClaimStorageSeed(t, db, &in)
			switch which {
			case "missing":
				in.ArtifactStorageID = "missing"
			case "disabled":
				require.NoError(t, db.Model(&model.StorageConfig{}).Where("storage_id = ?", in.ArtifactStorageID).Update("is_enabled", 0).Error)
			case "fingerprint":
				in.ArtifactStorageFingerprint = strings.Repeat("a", 64)
			case "partial":
				in.ArtifactStorageID = ""
			case "case":
				in.ArtifactStorageID = strings.ToUpper(in.ArtifactStorageID)
			case "malformed-config":
				require.NoError(t, db.Model(&model.StorageConfig{}).Where("storage_id = ?", in.ArtifactStorageID).Update("config", `[]`).Error)
			case "unsupported-type":
				require.NoError(t, db.Model(&model.StorageConfig{}).Where("storage_id = ?", in.ArtifactStorageID).Update("storage_type", "unsupported").Error)
			case "preexisting-pin":
				require.NoError(t, db.Model(&model.JobRun{}).Where("job_run_id = ?", in.JobRunID).Update("artifact_storage_id", "forged").Error)
			}
			got, err := r.CopilotRerunClaim(context.Background(), in, permitRerun, testRerunClaimValidator)
			require.Error(t, err)
			require.Nil(t, got)
			var job model.JobRun
			require.NoError(t, db.Where("job_run_id = ?", in.JobRunID).First(&job).Error)
			require.Empty(t, job.ClaimID)
		})
	}
}

func TestCopilotRerunOutputStorageUsesBuiltinDispatch(t *testing.T) {
	for _, uses := range []string{"builtin:artifacts", " BUILTIN:ARTIFACTS "} {
		payload := &taskqueue.JobRunTaskPayload{Steps: []taskqueue.StepPayload{{Uses: uses, Action: " UPLOAD "}}}
		require.Error(t, validateCopilotRerunOutputStorage(payload, CopilotRerunClaimInput{}))
		require.NoError(t, validateCopilotRerunOutputStorage(payload, CopilotRerunClaimInput{ArtifactStorageID: "store", ArtifactStorageFingerprint: strings.Repeat("a", 64)}))
	}
	require.NoError(t, validateCopilotRerunOutputStorage(&taskqueue.JobRunTaskPayload{Steps: []taskqueue.StepPayload{{Uses: "shell", Action: "upload"}}}, CopilotRerunClaimInput{}))
}

func TestCopilotRerunClaimNonceIncludesStorageIdentity(t *testing.T) {
	r, _, in := rerunClaimFixture(t)
	rerunClaimStorageSeed(t, r.database.Database(), &in)
	digest, err := rerunClaimNonceDigest(in)
	require.NoError(t, err)
	changed := in
	changed.ArtifactStorageID = "other-store"
	require.False(t, rerunClaimNonceMatches(digest, changed))
	changed = in
	changed.ArtifactStorageFingerprint = strings.Repeat("a", 64)
	require.False(t, rerunClaimNonceMatches(digest, changed))
	changed = in
	changed.ArtifactStorageID = ""
	changed.ArtifactStorageFingerprint = ""
	require.False(t, rerunClaimNonceMatches(digest, changed))
}
