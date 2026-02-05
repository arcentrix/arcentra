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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"time"

	"cloud.google.com/go/storage"
	"github.com/arcentrix/arcentra/pkg/log"
	"google.golang.org/api/option"
)

type GCSStorage struct {
	Client *storage.Client
	Bucket *storage.BucketHandle
	s      *Storage
}

func newGCS(s *Storage) (*GCSStorage, error) {
	var opts []option.ClientOption

	// 如果提供了 AccessKey，将其作为 credentials JSON 文件路径
	if s.AccessKey != "" {
		opts = append(opts, option.WithCredentialsFile(s.AccessKey))
	}

	client, err := storage.NewClient(context.Background(), opts...)
	if err != nil {
		return nil, err
	}

	bucket := client.Bucket(s.Bucket)

	return &GCSStorage{
		Client: client,
		Bucket: bucket,
		s:      s,
	}, nil
}

func (g *GCSStorage) GetObject(ctx context.Context, objectName string) ([]byte, error) {
	fullPath := getFullPath(g.s.BasePath, objectName)
	reader, err := g.Bucket.Object(fullPath).NewReader(ctx)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(reader); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (g *GCSStorage) PutObject(ctx context.Context, objectName string, file *multipart.FileHeader, contentType string) (string, error) {
	src, err := file.Open()
	if err != nil {
		return "", err
	}
	defer src.Close()

	fullPath := getFullPath(g.s.BasePath, objectName)
	writer := g.Bucket.Object(fullPath).NewWriter(ctx)
	writer.ContentType = contentType

	if _, err := io.Copy(writer, src); err != nil {
		writer.Close()
		return "", err
	}

	if err := writer.Close(); err != nil {
		return "", err
	}

	return fullPath, nil
}

func (g *GCSStorage) Upload(ctx context.Context, objectName string, file *multipart.FileHeader, contentType string) (string, error) {
	src, err := file.Open()
	if err != nil {
		return "", err
	}
	defer src.Close()

	fullPath := getFullPath(g.s.BasePath, objectName)
	fileSize := file.Size

	// 小文件直接PutObject
	if fileSize <= defaultPartSize {
		writer := g.Bucket.Object(fullPath).NewWriter(ctx)
		writer.ContentType = contentType

		if _, err := io.Copy(writer, src); err != nil {
			writer.Close()
			return "", err
		}

		if err := writer.Close(); err != nil {
			return "", err
		}
		log.Debugw("GCS upload completed", "fullPath", fullPath, "fileSize", fileSize)
		return fullPath, nil
	}

	// 否则分片上传 (GCS 使用组合对象实现)
	checkpointPath := filepath.Join(os.TempDir(), fullPath+".upload.json")
	var checkpoint uploadCheckpoint

	// 如果有断点记录则加载
	if data, err := os.ReadFile(checkpointPath); err == nil {
		_ = json.Unmarshal(data, &checkpoint)
	}

	if checkpoint.UploadID == "" {
		checkpoint = uploadCheckpoint{
			UploadID: fullPath, // GCS 使用对象名作为标识
			Key:      fullPath,
			FileSize: fileSize,
		}
		_ = os.WriteFile(checkpointPath, mustJSON(checkpoint), 0644)
	}

	// 使用分段写入器，支持断点续传
	writer := g.Bucket.Object(fullPath).NewWriter(ctx)
	writer.ContentType = contentType
	writer.ChunkSize = defaultPartSize // 设置分片大小

	buf := make([]byte, defaultPartSize)
	partNumber := int32(1)

	// 如果有已上传的分片，跳过它们
	uploadedSize := int64(0)
	for _, part := range checkpoint.Parts {
		if part >= partNumber {
			n, _ := src.Read(buf)
			if n > 0 {
				uploadedSize += int64(n)
				partNumber++
			}
		}
	}

	// 创建进度跟踪的 reader
	progressReader := newProgressReader(src, uploadedSize, fileSize, fullPath, "GCS", func(uploaded int64) {
		currentPart := int32(uploaded / defaultPartSize)
		if currentPart > partNumber {
			checkpoint.Parts = append(checkpoint.Parts, currentPart)
			checkpoint.UploadedBytes = uploaded
			checkpoint.UploadProgress = float64(uploaded) / float64(fileSize) * 100
			_ = os.WriteFile(checkpointPath, mustJSON(checkpoint), 0644)

			// 记录上传进度日志
			// log.Debug("GCS upload progress: %s - %.2f%% (%d/%d bytes)",
			// 	fullPath, checkpoint.UploadProgress, uploaded, fileSize)

			partNumber = currentPart
		}
	})

	// 上传剩余数据
	if _, err := io.Copy(writer, progressReader); err != nil {
		writer.Close()
		return "", err
	}

	if err := writer.Close(); err != nil {
		return "", err
	}

	log.Debugw("GCS upload completed", "fullPath", fullPath, "fileSize", fileSize)
	// 成功则删除断点文件
	_ = os.Remove(checkpointPath)
	return fullPath, nil
}

func (g *GCSStorage) Download(ctx context.Context, objectName string) ([]byte, error) {
	fullPath := getFullPath(g.s.BasePath, objectName)
	reader, err := g.Bucket.Object(fullPath).NewReader(ctx)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	return io.ReadAll(reader)
}

func (g *GCSStorage) Delete(ctx context.Context, objectName string) error {
	fullPath := getFullPath(g.s.BasePath, objectName)
	return g.Bucket.Object(fullPath).Delete(ctx)
}

func (g *GCSStorage) GetPresignedURL(ctx context.Context, objectName string, expiry time.Duration) (string, error) {
	fullPath := getFullPath(g.s.BasePath, objectName)

	// GCS 使用 SignedURL 生成预签名链接
	opts := &storage.SignedURLOptions{
		Method:  "GET",
		Expires: time.Now().Add(expiry),
	}

	// 如果提供了 AccessKey（服务账号 JSON 文件路径），使用它进行签名
	if g.s.AccessKey != "" {
		opts.GoogleAccessID = g.s.AccessKey
	}

	signedURL, err := g.Bucket.SignedURL(fullPath, opts)
	if err != nil {
		return "", err
	}

	return signedURL, nil
}
