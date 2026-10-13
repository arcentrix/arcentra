// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package storage

import (
	"context"
	"errors"
	"mime/multipart"
	"time"
)

// ErrStorageUnconfigured 表示尚未配置默认对象存储，不能执行存储操作
var ErrStorageUnconfigured = errors.New("default object storage is not configured")

type unconfiguredStorage struct{}

func (unconfiguredStorage) PutObject(ctx context.Context, _ string, _ *multipart.FileHeader, _ string) (string, error) {
	return "", unconfiguredError(ctx)
}
func (unconfiguredStorage) GetObject(ctx context.Context, _ string) ([]byte, error) {
	return nil, unconfiguredError(ctx)
}
func (unconfiguredStorage) Upload(ctx context.Context, _ string, _ *multipart.FileHeader, _ string) (string, error) {
	return "", unconfiguredError(ctx)
}
func (unconfiguredStorage) Download(ctx context.Context, _ string) ([]byte, error) {
	return nil, unconfiguredError(ctx)
}
func (unconfiguredStorage) Delete(ctx context.Context, _ string) error { return unconfiguredError(ctx) }
func (unconfiguredStorage) GetPresignedURL(ctx context.Context, _ string, _ time.Duration) (string, error) {
	return "", unconfiguredError(ctx)
}
func unconfiguredError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrStorageUnconfigured
}
