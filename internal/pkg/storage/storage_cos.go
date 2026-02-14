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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/tencentyun/cos-go-sdk-v5"
)

type COSStorage struct {
	Client *cos.Client
	s      *Storage
}

func newCOS(s *Storage) (*COSStorage, error) {
	// 解析 Endpoint，腾讯云 COS 需要 bucket URL
	u, err := url.Parse(s.Endpoint)
	if err != nil {
		return nil, err
	}

	// 如果 Endpoint 不包含 bucket，则添加
	if s.Bucket != "" && u.Host != "" {
		u, _ = url.Parse("https://" + s.Bucket + "." + u.Host)
	}

	b := &cos.BaseURL{BucketURL: u}
	client := cos.NewClient(b, &http.Client{
		Transport: &cos.AuthorizationTransport{
			SecretID:  s.AccessKey,
			SecretKey: s.SecretKey,
		},
	})

	return &COSStorage{
		Client: client,
		s:      s,
	}, nil
}

func (c *COSStorage) GetObject(ctx context.Context, objectName string) ([]byte, error) {
	fullPath := getFullPath(c.s.BasePath, objectName)
	resp, err := c.Client.Object.Get(context.Background(), fullPath, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (c *COSStorage) PutObject(ctx context.Context, objectName string, file *multipart.FileHeader, contentType string) (string, error) {
	src, err := file.Open()
	if err != nil {
		return "", err
	}
	defer func() { _ = src.Close() }()

	fullPath := getFullPath(c.s.BasePath, objectName)
	opt := &cos.ObjectPutOptions{
		ObjectPutHeaderOptions: &cos.ObjectPutHeaderOptions{
			ContentType: contentType,
		},
	}

	_, err = c.Client.Object.Put(context.Background(), fullPath, src, opt)
	if err != nil {
		return "", err
	}
	return fullPath, nil
}

func (c *COSStorage) Upload(ctx context.Context, objectName string, file *multipart.FileHeader, contentType string) (string, error) {
	src, err := file.Open()
	if err != nil {
		return "", err
	}
	defer func() { _ = src.Close() }()

	fullPath := getFullPath(c.s.BasePath, objectName)
	fileSize := file.Size

	// 小文件直接PutObject
	if fileSize <= defaultPartSize {
		opt := &cos.ObjectPutOptions{
			ObjectPutHeaderOptions: &cos.ObjectPutHeaderOptions{
				ContentType: contentType,
			},
		}
		_, err = c.Client.Object.Put(context.Background(), fullPath, src, opt)
		if err == nil {
			log.Debugw("COS upload completed", "fullPath", fullPath, "fileSize", fileSize)
		}
		return fullPath, err
	}

	// 否则分片上传
	checkpointPath := filepath.Join(os.TempDir(), fullPath+".upload.json")
	var checkpoint uploadCheckpoint

	// 如果有断点记录则加载
	if data, readErr := os.ReadFile(checkpointPath); readErr == nil {
		_ = json.Unmarshal(data, &checkpoint)
	}

	// 初始化分片上传
	if checkpoint.UploadID == "" {
		result, _, initErr := c.Client.Object.InitiateMultipartUpload(context.Background(), fullPath, &cos.InitiateMultipartUploadOptions{
			ObjectPutHeaderOptions: &cos.ObjectPutHeaderOptions{
				ContentType: contentType,
			},
		})
		if initErr != nil {
			return "", initErr
		}
		checkpoint = uploadCheckpoint{
			UploadID: result.UploadID,
			Key:      fullPath,
			FileSize: fileSize,
		}
		_ = os.WriteFile(checkpointPath, mustJSON(checkpoint), 0o644)
	}

	var completedParts []cos.Object
	partNumber := 1
	buf := make([]byte, defaultPartSize)
	uploadedBytes := checkpoint.UploadedBytes // 从断点恢复已上传字节数

	for {
		n, readErr := src.Read(buf)
		if n == 0 {
			break
		}

		// 如果已上传过该分片则跳过
		skipPart := false
		for _, p := range checkpoint.Parts {
			if p == int32(partNumber) {
				skipPart = true
				break
			}
		}

		if skipPart {
			uploadedBytes += int64(n)
		} else {
			resp, uploadErr := c.Client.Object.UploadPart(
				context.Background(),
				fullPath,
				checkpoint.UploadID,
				partNumber,
				bytes.NewReader(buf[:n]),
				&cos.ObjectUploadPartOptions{
					ContentLength: int64(n),
				},
			)
			if uploadErr != nil {
				return "", uploadErr
			}

			etag := resp.Header.Get("ETag")
			completedParts = append(completedParts, cos.Object{
				PartNumber: partNumber,
				ETag:       etag,
			})
			checkpoint.Parts = append(checkpoint.Parts, int32(partNumber))
			uploadedBytes += int64(n)

			// 更新进度信息
			checkpoint.UploadedBytes = uploadedBytes
			checkpoint.UploadProgress = float64(uploadedBytes) / float64(fileSize) * 100
			_ = os.WriteFile(checkpointPath, mustJSON(checkpoint), 0o644)

			// 记录上传进度日志
			// log.Debug("COS upload progress: %s - %.2f%% (%d/%d bytes)",
			// 	fullPath, checkpoint.UploadProgress, uploadedBytes, fileSize)
		}

		partNumber++
		if readErr == io.EOF {
			break
		}
	}

	// 完成分片上传
	opt := &cos.CompleteMultipartUploadOptions{
		Parts: completedParts,
	}
	_, _, err = c.Client.Object.CompleteMultipartUpload(
		context.Background(),
		fullPath,
		checkpoint.UploadID,
		opt,
	)
	if err == nil {
		log.Debugw("COS upload completed", "fullPath", fullPath, "fileSize", fileSize)
		_ = os.Remove(checkpointPath) // 成功则删除断点文件
	}
	return fullPath, err
}

func (c *COSStorage) Download(ctx context.Context, objectName string) ([]byte, error) {
	fullPath := getFullPath(c.s.BasePath, objectName)
	resp, err := c.Client.Object.Get(context.Background(), fullPath, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	return io.ReadAll(resp.Body)
}

func (c *COSStorage) Delete(ctx context.Context, objectName string) error {
	fullPath := getFullPath(c.s.BasePath, objectName)
	_, err := c.Client.Object.Delete(context.Background(), fullPath)
	return err
}

func (c *COSStorage) GetPresignedURL(ctx context.Context, objectName string, expiry time.Duration) (string, error) {
	fullPath := getFullPath(c.s.BasePath, objectName)

	presignedURL, err := c.Client.Object.GetPresignedURL(
		context.Background(),
		http.MethodGet,
		fullPath,
		c.s.AccessKey,
		c.s.SecretKey,
		expiry,
		nil,
	)
	if err != nil {
		return "", err
	}

	return presignedURL.String(), nil
}
