// Copyright 2026 Arcentra Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package storage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	domain "github.com/arcentrix/arcentra/internal/domain/agent"
	"github.com/arcentrix/arcentra/pkg/telemetry/log"
)

const (
	Minio = "minio"
	S3    = "s3"
	Oss   = "oss"
	Gcs   = "gcs"
	Cos   = "cos"
)

type Storage struct {
	Provider  string `json:"provider,omitempty"`
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
	Endpoint  string `json:"endpoint"`
	Bucket    string `json:"bucket"`
	Region    string `json:"region"`
	UseTLS    bool   `json:"useTLS"`
	BasePath  string `json:"basePath"`
}

type DbProvider struct {
	storageRepo   domain.IStorageRepository
	storageConfig *domain.StorageConfig
}

const defaultPartSize = 5 * 1024 * 1024 // 5MB

type uploadCheckpoint struct {
	UploadID       string  `json:"upload_id"`
	Parts          []int32 `json:"parts"`
	FileSize       int64   `json:"file_size"`
	Key            string  `json:"key"`
	UploadedBytes  int64   `json:"uploaded_bytes"`
	UploadProgress float64 `json:"upload_progress"`
}

type ProgressReader struct {
	reader     io.Reader
	uploaded   int64
	total      int64
	fullPath   string
	provider   string
	onProgress func(uploaded int64)
}

func newProgressReader(reader io.Reader, uploaded, total int64, fullPath, provider string, onProgress func(int64)) *ProgressReader {
	return &ProgressReader{
		reader:     reader,
		uploaded:   uploaded,
		total:      total,
		fullPath:   fullPath,
		provider:   provider,
		onProgress: onProgress,
	}
}

func (pr *ProgressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	if n > 0 {
		pr.uploaded += int64(n)
		if pr.onProgress != nil {
			pr.onProgress(pr.uploaded)
		}
	}
	return n, err
}

func (pr *ProgressReader) LogProgress(progress float64) {
	log.Debugw(
		"upload progress",
		"provider", pr.provider,
		"fullPath", pr.fullPath,
		"progress", progress,
		"uploaded", pr.uploaded,
		"total", pr.total,
	)
}

func NewStorage(s *Storage) (domain.IStorage, error) {
	switch s.Provider {
	case Minio:
		return newMinio(s)
	case S3:
		return newS3(s)
	case Oss:
		return newOSS(s)
	case Gcs:
		return newGCS(s)
	case Cos:
		return newCOS(s)
	default:
		return nil, fmt.Errorf("unsupported storage provider: %s", s.Provider)
	}
}

func NewStorageDBProvider(ctx context.Context, storageRepo domain.IStorageRepository) (*DbProvider, error) {
	storageConfig, err := storageRepo.GetDefault(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get default storage config: %w", err)
	}

	return &DbProvider{
		storageRepo:   storageRepo,
		storageConfig: storageConfig,
	}, nil
}

func (sdp *DbProvider) GetStorageProvider() (domain.IStorage, error) {
	cfg, err := sdp.parseConfig(sdp.storageConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to parse storage config: %w", err)
	}

	cfg.Provider = string(sdp.storageConfig.StorageType)
	return NewStorage(cfg)
}

func (sdp *DbProvider) parseConfig(sc *domain.StorageConfig) (*Storage, error) {
	raw := sc.Config
	var configBytes []byte
	configStr := string(raw)
	if strings.HasPrefix(configStr, "{") {
		configBytes = raw
	} else {
		decoded, err := base64.StdEncoding.DecodeString(configStr)
		if err != nil {
			return nil, fmt.Errorf("invalid config encoding: %w", err)
		}
		configBytes = decoded
	}

	var s Storage
	if err := json.Unmarshal(configBytes, &s); err != nil {
		return nil, fmt.Errorf("failed to parse storage config JSON: %w", err)
	}
	return &s, nil
}

func (sdp *DbProvider) GetStorageConfig() *domain.StorageConfig {
	return sdp.storageConfig
}

func (sdp *DbProvider) RefreshStorageConfig(ctx context.Context) error {
	storageConfig, err := sdp.storageRepo.GetDefault(ctx)
	if err != nil {
		return fmt.Errorf("failed to refresh storage config: %w", err)
	}
	sdp.storageConfig = storageConfig
	return nil
}

func (sdp *DbProvider) GetStorageConfigByID(ctx context.Context, storageID string) (*domain.StorageConfig, error) {
	return sdp.storageRepo.Get(ctx, storageID)
}

func (sdp *DbProvider) GetAllStorageConfigs(ctx context.Context) ([]domain.StorageConfig, error) {
	return sdp.storageRepo.ListEnabled(ctx)
}

func (sdp *DbProvider) SwitchStorageConfig(ctx context.Context, storageID string) error {
	storageConfig, err := sdp.storageRepo.Get(ctx, storageID)
	if err != nil {
		return fmt.Errorf("failed to get storage config by ID %s: %w", storageID, err)
	}

	err = sdp.storageRepo.SetDefault(ctx, storageID)
	if err != nil {
		return fmt.Errorf("failed to set default storage config: %w", err)
	}

	sdp.storageConfig = storageConfig
	return nil
}

func getFullPath(basePath, objectName string) string {
	if basePath == "" {
		return objectName
	}
	basePath = strings.Trim(basePath, "/")
	objectName = strings.TrimPrefix(objectName, "/")
	return filepath.Join(basePath, objectName)
}

func mustJSON(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return b
}
