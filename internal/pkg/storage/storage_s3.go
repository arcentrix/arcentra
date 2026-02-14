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
	"errors"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type S3Storage struct {
	Client *s3.Client
	s      *Storage
}

func newS3(s *Storage) (*S3Storage, error) {
	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithCredentialsProvider(credentials.StaticCredentialsProvider{
			Value: aws.Credentials{
				AccessKeyID:     s.AccessKey,
				SecretAccessKey: s.SecretKey,
			},
		}),
		config.WithBaseEndpoint(s.Endpoint),
		config.WithRegion(s.Region))
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(cfg)
	return &S3Storage{Client: client, s: s}, nil
}

func (s *S3Storage) GetObject(ctx context.Context, objectName string) ([]byte, error) {
	fullPath := getFullPath(s.s.BasePath, objectName)
	out, err := s.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.s.Bucket),
		Key:    aws.String(fullPath),
	})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(out.Body)
	return buf.Bytes(), nil
}

func (s *S3Storage) PutObject(ctx context.Context, objectName string, file *multipart.FileHeader, contentType string) (string, error) {
	src, err := file.Open()
	if err != nil {
		return "", err
	}
	defer src.Close()

	fullPath := getFullPath(s.s.BasePath, objectName)
	_, err = s.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.s.Bucket),
		Key:         aws.String(fullPath),
		Body:        src,
		ContentType: aws.String(contentType),
	})
	return fullPath, err
}

func (s *S3Storage) Upload(ctx context.Context, objectName string, file *multipart.FileHeader, contentType string) (string, error) {
	src, err := file.Open()
	if err != nil {
		return "", err
	}
	defer src.Close()

	fullPath := getFullPath(s.s.BasePath, objectName)
	fileSize := file.Size

	// 小文件直接PutObject
	if fileSize <= defaultPartSize {
		_, err = s.Client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      aws.String(s.s.Bucket),
			Key:         aws.String(fullPath),
			Body:        src,
			ContentType: aws.String(contentType),
		})
		if err == nil {
			log.Debugw("S3 upload completed", "fullPath", fullPath, "fileSize", fileSize)
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

	if checkpoint.UploadID == "" {
		createResp, createErr := s.Client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
			Bucket:      aws.String(s.s.Bucket),
			Key:         aws.String(fullPath),
			ContentType: aws.String(contentType),
		})
		if createErr != nil {
			return "", createErr
		}
		checkpoint = uploadCheckpoint{
			UploadID: *createResp.UploadId,
			Key:      fullPath,
			FileSize: fileSize,
		}
		_ = os.WriteFile(checkpointPath, mustJSON(checkpoint), 0644)
	}

	var completedParts []s3types.CompletedPart
	partNumber := int32(1)
	buf := make([]byte, defaultPartSize)
	uploadedBytes := checkpoint.UploadedBytes // 从断点恢复已上传字节数

	for {
		n, readErr := src.Read(buf)
		if n == 0 {
			break
		}

		// 如果已上传过该分片则跳过
		if slices.Contains(checkpoint.Parts, partNumber) {
			uploadedBytes += int64(n)
			partNumber++
			continue
		}

		partOutput, uploadErr := s.Client.UploadPart(ctx, &s3.UploadPartInput{
			Bucket:     aws.String(s.s.Bucket),
			Key:        aws.String(fullPath),
			PartNumber: &partNumber,
			UploadId:   aws.String(checkpoint.UploadID),
			Body:       bytes.NewReader(buf[:n]),
		})
		if uploadErr != nil && !errors.Is(uploadErr, io.EOF) {
			return "", uploadErr
		}

		completedParts = append(completedParts, s3types.CompletedPart{
			ETag:       partOutput.ETag,
			PartNumber: &partNumber,
		})
		checkpoint.Parts = append(checkpoint.Parts, partNumber)
		uploadedBytes += int64(n)

		// 更新进度信息
		checkpoint.UploadedBytes = uploadedBytes
		checkpoint.UploadProgress = float64(uploadedBytes) / float64(fileSize) * 100
		_ = os.WriteFile(checkpointPath, mustJSON(checkpoint), 0644)

		// 记录上传进度日志
		log.Debugw("S3 upload progress", "fullPath", fullPath, "progress", checkpoint.UploadProgress, "uploadedBytes", uploadedBytes, "fileSize", fileSize)

		partNumber++
		if readErr == io.EOF {
			break
		}
	}

	// 完成分片上传
	_, err = s.Client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(s.s.Bucket),
		Key:      aws.String(fullPath),
		UploadId: aws.String(checkpoint.UploadID),
		MultipartUpload: &s3types.CompletedMultipartUpload{
			Parts: completedParts,
		},
	})
	if err == nil {
		// log.Debug("S3 upload completed: %s - 100.00%% (%d bytes)", fullPath, fileSize)
		_ = os.Remove(checkpointPath) // 成功则删除断点文件
	}
	return fullPath, err
}

func (s *S3Storage) Download(ctx context.Context, objectName string) ([]byte, error) {
	fullPath := getFullPath(s.s.BasePath, objectName)
	out, err := s.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.s.Bucket),
		Key:    aws.String(fullPath),
	})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(out.Body)
	return buf.Bytes(), nil
}

func (s *S3Storage) Delete(ctx context.Context, objectName string) error {
	fullPath := getFullPath(s.s.BasePath, objectName)
	_, err := s.Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.s.Bucket),
		Key:    aws.String(fullPath),
	})
	return err
}

func (s *S3Storage) GetPresignedURL(ctx context.Context, objectName string, expiry time.Duration) (string, error) {
	fullPath := getFullPath(s.s.BasePath, objectName)

	presignClient := s3.NewPresignClient(s.Client)
	presignResult, err := presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.s.Bucket),
		Key:    aws.String(fullPath),
	}, s3.WithPresignExpires(expiry))

	if err != nil {
		return "", err
	}

	return presignResult.URL, nil
}
