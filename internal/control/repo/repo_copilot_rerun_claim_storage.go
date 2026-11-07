// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/arcentrix/arcentra/internal/control/model"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// Storage is locked after domain/Agent rows and before Schedule/Intent. Existing
// storage mutation transactions take only storage rows, never domain locks.
// Config contains credentials: keep this read silent and return only identity.
func lockCopilotRerunClaimStorage(ctx context.Context, tx *gorm.DB, job *model.JobRun, in CopilotRerunClaimInput, replay bool) (string, error) {
	if !rerunClaimStorageIdentityValid(in.ArtifactStorageID, in.ArtifactStorageFingerprint) {
		return "", ErrCopilotRerunInvalid
	}
	if replay {
		if job.ArtifactStorageID != in.ArtifactStorageID || job.ArtifactStorageFingerprint != in.ArtifactStorageFingerprint {
			return "", ErrCopilotRerunConflict
		}
	} else if job.ArtifactStorageID != "" || job.ArtifactStorageFingerprint != "" || job.ArtifactStorageType != "" {
		return "", ErrCopilotRerunConflict
	}
	if in.ArtifactStorageID == "" {
		if job.ArtifactStorageType != "" {
			return "", ErrCopilotRerunConflict
		}
		return "", nil
	}
	config, err := storageTarget(tx.WithContext(ctx).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Clauses(clause.Locking{Strength: "UPDATE"}), in.ArtifactStorageID)
	if err != nil {
		if errors.Is(err, ErrStorageConfigNotFound) {
			return "", ErrCopilotRerunForbidden
		}
		if errors.Is(err, ErrStorageConfigConflict) || errors.Is(err, ErrStorageConfigID) {
			return "", ErrCopilotRerunConflict
		}
		return "", err
	}
	var detail model.StorageConfigDetail
	if json.Unmarshal(config.Config, &detail) != nil || !rerunClaimStorageTypeValid(config.StorageType) {
		return "", ErrCopilotRerunConflict
	}
	fingerprint := artifactutil.StorageFingerprint(config.StorageType, detail.Endpoint, detail.Bucket, detail.Region, detail.BasePath, detail.UseTLS)
	if fingerprint != in.ArtifactStorageFingerprint || (replay && job.ArtifactStorageType != config.StorageType) {
		return "", ErrCopilotRerunConflict
	}
	return config.StorageType, nil
}

// Match the artifact executor's explicit builtin/action dispatch, never shell text.
func validateCopilotRerunOutputStorage(payload *taskqueue.JobRunTaskPayload, in CopilotRerunClaimInput) error {
	if !rerunClaimStorageIdentityValid(in.ArtifactStorageID, in.ArtifactStorageFingerprint) {
		return ErrCopilotRerunInvalid
	}
	for _, step := range payload.Steps {
		if strings.EqualFold(strings.TrimSpace(step.Uses), "builtin:artifacts") && strings.EqualFold(strings.TrimSpace(step.Action), "upload") && in.ArtifactStorageID == "" {
			return ErrCopilotRerunForbidden
		}
	}
	return nil
}

func rerunClaimStorageTypeValid(kind string) bool {
	switch kind {
	case "s3", "minio", "oss", "gcs", "cos":
		return true
	}
	return false
}
