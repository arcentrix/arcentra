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

package model

import (
	"gorm.io/datatypes"
)

// StorageConfig 对象存储配置表
type StorageConfig struct {
	BaseModel
	StorageId   string         `gorm:"column:storage_id" json:"storageId"`
	Name        string         `gorm:"column:name" json:"name"`
	StorageType string         `gorm:"column:storage_type" json:"storageType"` // minio/s3/oss/gcs/cos
	Config      datatypes.JSON `gorm:"column:config" json:"config"`
	Description string         `gorm:"column:description" json:"description"`
	IsDefault   int            `gorm:"column:is_default" json:"isDefault"` // 0: not default, 1: default
	IsEnabled   int            `gorm:"column:is_enabled" json:"isEnabled"` // 0: disabled, 1: enabled
}

func (StorageConfig) TableName() string {
	return "t_storage_config"
}

// StorageConfigDetail 存储配置详情（通用结构）
type StorageConfigDetail struct {
	Endpoint  string `json:"endpoint"`
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
	Bucket    string `json:"bucket"`
	Region    string `json:"region"`
	UseTLS    bool   `json:"useTLS"`
	BasePath  string `json:"basePath"`
}

// MinIOConfig MinIO 存储配置
type MinIOConfig struct {
	Endpoint  string `json:"endpoint"`
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
	Bucket    string `json:"bucket"`
	Region    string `json:"region"`
	UseTLS    bool   `json:"useTLS"`
	BasePath  string `json:"basePath"`
}

// S3Config AWS S3 存储配置
type S3Config struct {
	Endpoint  string `json:"endpoint"`
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
	Bucket    string `json:"bucket"`
	Region    string `json:"region"`
	UseTLS    bool   `json:"useTLS"`
	BasePath  string `json:"basePath"`
}

// OSSConfig 阿里云 OSS 存储配置
type OSSConfig struct {
	Endpoint  string `json:"endpoint"`
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
	Bucket    string `json:"bucket"`
	Region    string `json:"region"`
	UseTLS    bool   `json:"useTLS"`
	BasePath  string `json:"basePath"`
}

// GCSConfig Google Cloud Storage 配置
type GCSConfig struct {
	Endpoint  string `json:"endpoint"`
	AccessKey string `json:"accessKey"` // Service Account Key 文件路径
	Bucket    string `json:"bucket"`
	Region    string `json:"region"`
	BasePath  string `json:"basePath"`
}

// COSConfig 腾讯云 COS 存储配置
type COSConfig struct {
	Endpoint  string `json:"endpoint"`
	AccessKey string `json:"accessKey"` // SecretId
	SecretKey string `json:"secretKey"` // SecretKey
	Bucket    string `json:"bucket"`
	Region    string `json:"region"`
	UseTLS    bool   `json:"useTLS"`
	BasePath  string `json:"basePath"`
}
