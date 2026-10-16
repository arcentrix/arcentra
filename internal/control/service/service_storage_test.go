// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/storage"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/require"
)

func TestStorageEditorPreservesHistoricalArtifactAccess(t *testing.T) {
	artifacts, db, artifact := artifactServiceFixture(t)
	ctx := context.Background()
	require.NoError(t, db.WithContext(ctx).AutoMigrate(&model.StorageConfig{}))
	storageRepo := repo.NewStorageRepo(artifactServiceDatabase{db}, nil)
	configs := NewStorageService(storageRepo)
	original := model.StorageConfigDetail{Endpoint: "https://store.example.invalid", Bucket: "historical", Region: "original", BasePath: "prefix", UseTLS: true, AccessKey: "synthetic-access", SecretKey: "synthetic-secret"}
	raw, err := sonic.Marshal(original)
	require.NoError(t, err)
	_, err = configs.CreateStorageConfig(ctx, &CreateStorageConfigRequest{StorageID: "historical", Name: "Historical", StorageType: "s3", Config: raw, IsDefault: 1})
	require.NoError(t, err)
	other := original
	other.Bucket = "new-target"
	otherJSON, err := sonic.Marshal(other)
	require.NoError(t, err)
	_, err = configs.CreateStorageConfig(ctx, &CreateStorageConfigRequest{StorageID: "new", Name: "New target", StorageType: "s3", Config: otherJSON})
	require.NoError(t, err)
	artifact.StorageType, artifact.StorageID = "s3", "historical"
	artifact.StorageFingerprint = artifactutil.StorageFingerprint("s3", original.Endpoint, original.Bucket, original.Region, original.BasePath, original.UseTLS)
	artifact.StoragePath, err = artifactutil.RemotePath(artifact.PipelineRunID, artifact.JobRunID, artifact.StepRunID, artifact.SHA256, artifact.Name)
	require.NoError(t, err)
	require.NoError(t, db.WithContext(ctx).Model(&model.ProjectArtifact{}).Where("artifact_id = ?", artifact.ArtifactID).Updates(map[string]any{"storage_type": artifact.StorageType, "storage_id": artifact.StorageID, "storage_fingerprint": artifact.StorageFingerprint, "storage_path": artifact.StoragePath}).Error)
	require.NoError(t, configs.SetDefaultStorageConfig(ctx, "new"))
	_, err = configs.UpdateStorageConfig(ctx, &UpdateStorageConfigRequest{StorageID: "historical", Config: []byte(`{"accessKey":"rotated-access","secretKey":"rotated-secret"}`)})
	require.NoError(t, err)
	description := ""
	_, err = configs.UpdateStorageConfig(ctx, &UpdateStorageConfigRequest{StorageID: "historical", Description: &description, Config: []byte(`{"accessKey":"","secretKey":null}`)})
	require.NoError(t, err)
	_, err = configs.UpdateStorageConfig(ctx, &UpdateStorageConfigRequest{StorageID: "historical", Config: []byte(`{"bucket":"redirected"}`)})
	require.ErrorIs(t, err, repo.ErrStorageTargetImmutable)
	artifacts.storageRepo = storageRepo
	var opened int
	artifacts.newStorage = func(config *storage.Storage) (storage.IStorage, error) {
		require.Equal(t, original.Bucket, config.Bucket)
		require.Equal(t, original.Endpoint, config.Endpoint)
		require.Equal(t, original.BasePath, config.BasePath)
		require.Equal(t, "rotated-access", config.AccessKey)
		require.Equal(t, "rotated-secret", config.SecretKey)
		opened++
		return &artifactRemoteStorage{reader: &artifactRemoteReader{Reader: strings.NewReader("payload")}}, nil
	}
	download, err := artifacts.Open(ctx, "project", artifact.ArtifactID)
	require.NoError(t, err)
	data, err := io.ReadAll(download.Reader)
	require.NoError(t, err)
	require.NoError(t, download.Reader.Close())
	require.Equal(t, "payload", string(data))
	require.Equal(t, 1, opened)
	current, err := configs.GetDefaultStorageConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, "new", current.StorageID)
}
