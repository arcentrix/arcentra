// Copyright 2025 Arcentra Authors.
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

package agent

import (
	"context"
	"mime/multipart"
	"time"
)

// IAgentRepository defines persistence operations for Agent entities.
type IAgentRepository interface {
	Create(ctx context.Context, agent *Agent) error
	Get(ctx context.Context, agentID string) (*Agent, error)
	Update(ctx context.Context, agent *Agent) error
	Patch(ctx context.Context, agentID string, updates map[string]any) error
	Delete(ctx context.Context, agentID string) error
	List(ctx context.Context, page, size int) ([]Agent, int64, error)
	Statistics(ctx context.Context) (total, online, offline int64, err error)
}

// IStorage defines the storage capability port for object storage operations.
type IStorage interface {
	PutObject(ctx context.Context, objectName string, file *multipart.FileHeader, contentType string) (string, error)
	GetObject(ctx context.Context, objectName string) ([]byte, error)
	Upload(ctx context.Context, objectName string, file *multipart.FileHeader, contentType string) (string, error)
	Download(ctx context.Context, objectName string) ([]byte, error)
	Delete(ctx context.Context, objectName string) error
	GetPresignedURL(ctx context.Context, objectName string, expiry time.Duration) (string, error)
}

// IStorageRepository defines persistence operations for StorageConfig entities.
type IStorageRepository interface {
	GetDefault(ctx context.Context) (*StorageConfig, error)
	Get(ctx context.Context, storageID string) (*StorageConfig, error)
	ListEnabled(ctx context.Context) ([]StorageConfig, error)
	ListByType(ctx context.Context, storageType StorageType) ([]StorageConfig, error)
	Create(ctx context.Context, config *StorageConfig) error
	Update(ctx context.Context, config *StorageConfig) error
	Delete(ctx context.Context, storageID string) error
	SetDefault(ctx context.Context, storageID string) error
}
