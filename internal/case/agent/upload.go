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
	"fmt"
	"mime/multipart"

	"github.com/arcentrix/arcentra/internal/domain/agent"
)

// IFileUploader abstracts the object-storage upload operation.
// Implemented by infrastructure (e.g. MinIO, S3 adapter).
type IFileUploader interface {
	Upload(ctx context.Context, storageID, objectPath, contentType string, file *multipart.FileHeader) (uploadedPath string, err error)
	BuildURL(objectPath string, config *agent.StorageConfig) string
}

// UploadUseCase handles file uploads to object storage.
type UploadUseCase struct {
	storageRepo agent.IStorageRepository
	uploader    IFileUploader
}

func NewUploadUseCase(repo agent.IStorageRepository, uploader IFileUploader) *UploadUseCase {
	return &UploadUseCase{storageRepo: repo, uploader: uploader}
}

func (uc *UploadUseCase) Execute(ctx context.Context, input UploadFileInput, file *multipart.FileHeader) (*UploadFileOutput, error) {
	if file == nil {
		return nil, fmt.Errorf("file is required")
	}

	var storageConfig *agent.StorageConfig
	var err error
	if input.StorageID != "" {
		storageConfig, err = uc.storageRepo.Get(ctx, input.StorageID)
	} else {
		storageConfig, err = uc.storageRepo.GetDefault(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("get storage config: %w", err)
	}
	if !storageConfig.IsEnabled {
		return nil, fmt.Errorf("storage config is disabled")
	}

	uploadedPath, err := uc.uploader.Upload(ctx, storageConfig.StorageID, input.CustomPath, input.ContentType, file)
	if err != nil {
		return nil, fmt.Errorf("upload file: %w", err)
	}

	fileURL := uc.uploader.BuildURL(uploadedPath, storageConfig)

	return &UploadFileOutput{
		ObjectName:   uploadedPath,
		FileURL:      fileURL,
		OriginalName: file.Filename,
		Size:         file.Size,
		ContentType:  input.ContentType,
		StorageID:    storageConfig.StorageID,
		StorageType:  string(storageConfig.StorageType),
	}, nil
}
