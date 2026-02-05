// Copyright 2025 Arcentra Team
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
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	storagemodel "github.com/arcentrix/arcentra/internal/engine/model"
	storagerepo "github.com/arcentrix/arcentra/internal/engine/repo"
	"github.com/arcentrix/arcentra/pkg/log"
)

// 存储类型常量
const (
	StorageMinio = "minio"
	StorageS3    = "s3"
	StorageOSS   = "oss"
	StorageGCS   = "gcs"
	StorageCOS   = "cos"
)

// Storage 存储配置结构
type Storage struct {
	Provider  string
	AccessKey string
	SecretKey string
	Endpoint  string
	Bucket    string
	Region    string
	UseTLS    bool
	BasePath  string
}

// StorageDBProvider 从数据库加载存储配置的提供者
type StorageDBProvider struct {
	storageRepo   storagerepo.IStorageRepository
	storageConfig *storagemodel.StorageConfig
}

const defaultPartSize = 5 * 1024 * 1024 // 5MB

type uploadCheckpoint struct {
	UploadID       string  `json:"upload_id"`
	Parts          []int32 `json:"parts"`
	FileSize       int64   `json:"file_size"`
	Key            string  `json:"key"`
	UploadedBytes  int64   `json:"uploaded_bytes"`  // 已上传字节数
	UploadProgress float64 `json:"upload_progress"` // 上传进度百分比
}

// ProgressReader 统一的进度跟踪 Reader
type ProgressReader struct {
	reader     io.Reader
	uploaded   int64
	total      int64
	fullPath   string
	provider   string // 存储提供商名称（S3, MinIO, OSS, COS, GCS）
	onProgress func(uploaded int64)
}

// newProgressReader 创建新的进度跟踪 Reader
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

// LogProgress 记录上传进度
func (pr *ProgressReader) LogProgress(progress float64) {
	log.Debugw("upload progress", "provider", pr.provider, "fullPath", pr.fullPath, "progress", progress, "uploaded", pr.uploaded, "total", pr.total)
}

// NewStorage 根据配置创建存储提供者实例
func NewStorage(s *Storage) (StorageProvider, error) {
	switch s.Provider {
	case StorageMinio:
		return newMinio(s)
	case StorageS3:
		return newS3(s)
	case StorageOSS:
		return newOSS(s)
	case StorageGCS:
		return newGCS(s)
	case StorageCOS:
		return newCOS(s)
	default:
		return nil, fmt.Errorf("unsupported storage provider: %s", s.Provider)
	}
}

// NewStorageDBProvider 创建从数据库加载存储配置的提供者
func NewStorageDBProvider(storageRepo storagerepo.IStorageRepository) (*StorageDBProvider, error) {
	// 获取默认存储配置
	storageConfig, err := storageRepo.GetDefaultStorageConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get default storage config: %w", err)
	}

	return &StorageDBProvider{
		storageRepo:   storageRepo,
		storageConfig: storageConfig,
	}, nil
}

// GetStorageProvider 获取存储提供者实例
func (sdp *StorageDBProvider) GetStorageProvider() (StorageProvider, error) {
	// 解析存储配置
	config, err := sdp.storageRepo.ParseStorageConfig(sdp.storageConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to parse storage config: %w", err)
	}

	// 根据存储类型创建对应的存储实例
	switch sdp.storageConfig.StorageType {
	case "minio":
		minioConfig, ok := config.(*storagemodel.MinIOConfig)
		if !ok {
			return nil, fmt.Errorf("invalid MinIO config type")
		}
		return sdp.createMinIOStorage(minioConfig)
	case "s3":
		s3Config, ok := config.(*storagemodel.S3Config)
		if !ok {
			return nil, fmt.Errorf("invalid S3 config type")
		}
		return sdp.createS3Storage(s3Config)
	case "oss":
		ossConfig, ok := config.(*storagemodel.OSSConfig)
		if !ok {
			return nil, fmt.Errorf("invalid OSS config type")
		}
		return sdp.createOSSStorage(ossConfig)
	case "gcs":
		gcsConfig, ok := config.(*storagemodel.GCSConfig)
		if !ok {
			return nil, fmt.Errorf("invalid GCS config type")
		}
		return sdp.createGCSStorage(gcsConfig)
	case "cos":
		cosConfig, ok := config.(*storagemodel.COSConfig)
		if !ok {
			return nil, fmt.Errorf("invalid COS config type")
		}
		return sdp.createCOSStorage(cosConfig)
	default:
		return nil, fmt.Errorf("unsupported storage type: %s", sdp.storageConfig.StorageType)
	}
}

// createMinIOStorage 创建 MinIO 存储实例
func (sdp *StorageDBProvider) createMinIOStorage(config *storagemodel.MinIOConfig) (StorageProvider, error) {
	storage := &Storage{
		Provider:  StorageMinio,
		Endpoint:  config.Endpoint,
		AccessKey: config.AccessKey,
		SecretKey: config.SecretKey,
		Bucket:    config.Bucket,
		Region:    config.Region,
		UseTLS:    config.UseTLS,
		BasePath:  config.BasePath,
	}
	return NewStorage(storage)
}

// createS3Storage 创建 S3 存储实例
func (sdp *StorageDBProvider) createS3Storage(config *storagemodel.S3Config) (StorageProvider, error) {
	storage := &Storage{
		Provider:  StorageS3,
		Endpoint:  config.Endpoint,
		AccessKey: config.AccessKey,
		SecretKey: config.SecretKey,
		Bucket:    config.Bucket,
		Region:    config.Region,
		UseTLS:    config.UseTLS,
		BasePath:  config.BasePath,
	}
	return NewStorage(storage)
}

// createOSSStorage 创建 OSS 存储实例
func (sdp *StorageDBProvider) createOSSStorage(config *storagemodel.OSSConfig) (StorageProvider, error) {
	storage := &Storage{
		Provider:  StorageOSS,
		Endpoint:  config.Endpoint,
		AccessKey: config.AccessKey,
		SecretKey: config.SecretKey,
		Bucket:    config.Bucket,
		Region:    config.Region,
		UseTLS:    config.UseTLS,
		BasePath:  config.BasePath,
	}
	return NewStorage(storage)
}

// createGCSStorage 创建 GCS 存储实例
func (sdp *StorageDBProvider) createGCSStorage(config *storagemodel.GCSConfig) (StorageProvider, error) {
	storage := &Storage{
		Provider:  StorageGCS,
		Endpoint:  config.Endpoint,
		AccessKey: config.AccessKey,
		Bucket:    config.Bucket,
		Region:    config.Region,
		BasePath:  config.BasePath,
	}
	return NewStorage(storage)
}

// createCOSStorage 创建 COS 存储实例
func (sdp *StorageDBProvider) createCOSStorage(config *storagemodel.COSConfig) (StorageProvider, error) {
	storage := &Storage{
		Provider:  StorageCOS,
		Endpoint:  config.Endpoint,
		AccessKey: config.AccessKey,
		SecretKey: config.SecretKey,
		Bucket:    config.Bucket,
		Region:    config.Region,
		UseTLS:    config.UseTLS,
		BasePath:  config.BasePath,
	}
	return NewStorage(storage)
}

// GetStorageConfig 获取当前存储配置
func (sdp *StorageDBProvider) GetStorageConfig() *storagemodel.StorageConfig {
	return sdp.storageConfig
}

// RefreshStorageConfig 刷新存储配置（从数据库重新加载）
func (sdp *StorageDBProvider) RefreshStorageConfig() error {
	storageConfig, err := sdp.storageRepo.GetDefaultStorageConfig()
	if err != nil {
		return fmt.Errorf("failed to refresh storage config: %w", err)
	}
	sdp.storageConfig = storageConfig
	return nil
}

// GetStorageConfigByID 根据ID获取存储配置
func (sdp *StorageDBProvider) GetStorageConfigByID(storageID string) (*storagemodel.StorageConfig, error) {
	return sdp.storageRepo.GetStorageConfigByID(storageID)
}

// GetAllStorageConfigs 获取所有存储配置
func (sdp *StorageDBProvider) GetAllStorageConfigs() ([]storagemodel.StorageConfig, error) {
	return sdp.storageRepo.GetEnabledStorageConfigs()
}

// SwitchStorageConfig 切换存储配置
func (sdp *StorageDBProvider) SwitchStorageConfig(storageID string) error {
	storageConfig, err := sdp.storageRepo.GetStorageConfigByID(storageID)
	if err != nil {
		return fmt.Errorf("failed to get storage config by ID %s: %w", storageID, err)
	}

	// 设置为默认配置
	err = sdp.storageRepo.SetDefaultStorageConfig(storageID)
	if err != nil {
		return fmt.Errorf("failed to set default storage config: %w", err)
	}

	// 更新当前配置
	sdp.storageConfig = storageConfig
	return nil
}

// getFullPath 组合 BasePath 和 objectName，返回完整的对象路径
func getFullPath(basePath, objectName string) string {
	if basePath == "" {
		return objectName
	}
	// 清理路径，避免双斜杠
	basePath = strings.Trim(basePath, "/")
	objectName = strings.TrimPrefix(objectName, "/")
	return filepath.Join(basePath, objectName)
}

// mustJSON 将对象序列化为 JSON 并返回字节切片
func mustJSON(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return b
}
