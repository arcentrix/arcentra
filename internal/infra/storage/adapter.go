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
	"mime/multipart"

	agentcase "github.com/arcentrix/arcentra/internal/case/agent"
	domain "github.com/arcentrix/arcentra/internal/domain/agent"
)

// FileUploaderAdapter adapts the IStorage interface to the
// case/agent.IFileUploader interface expected by UploadUseCase.
type FileUploaderAdapter struct {
	storageRepo domain.IStorageRepository
}

func NewFileUploaderAdapter(storageRepo domain.IStorageRepository) *FileUploaderAdapter {
	return &FileUploaderAdapter{storageRepo: storageRepo}
}

func (a *FileUploaderAdapter) Upload(
	ctx context.Context,
	storageID, objectPath, contentType string,
	file *multipart.FileHeader,
) (string, error) {
	config, err := a.resolveConfig(ctx, storageID)
	if err != nil {
		return "", err
	}

	provider, err := a.newProvider(config)
	if err != nil {
		return "", err
	}

	return provider.Upload(ctx, objectPath, file, contentType)
}

func (a *FileUploaderAdapter) BuildURL(objectPath string, config *domain.StorageConfig) string {
	return objectPath
}

func (a *FileUploaderAdapter) resolveConfig(ctx context.Context, storageID string) (*domain.StorageConfig, error) {
	if storageID != "" {
		return a.storageRepo.Get(ctx, storageID)
	}
	return a.storageRepo.GetDefault(ctx)
}

func (a *FileUploaderAdapter) newProvider(config *domain.StorageConfig) (domain.IStorage, error) {
	s := &Storage{
		Provider: string(config.StorageType),
	}
	return NewStorage(s)
}

var _ agentcase.IFileUploader = (*FileUploaderAdapter)(nil)
