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

package storage

import (
	"context"
	"mime/multipart"
	"time"
)

type StorageProvider interface {
	PutObject(ctx context.Context, objectName string, file *multipart.FileHeader, contentType string) (string, error)
	GetObject(ctx context.Context, objectName string) ([]byte, error)
	Upload(ctx context.Context, objectName string, file *multipart.FileHeader, contentType string) (string, error)
	Download(ctx context.Context, objectName string) ([]byte, error)
	Delete(ctx context.Context, objectName string) error
	// GetPresignedURL 生成预签名下载链接，expiry 参数指定链接有效期
	GetPresignedURL(ctx context.Context, objectName string, expiry time.Duration) (string, error)
}
