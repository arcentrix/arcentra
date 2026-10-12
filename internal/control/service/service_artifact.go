// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/storage"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/bytedance/sonic"
)

// 制品错误不包含内部位置、对象键、存储配置或底层凭据
var (
	ErrArtifactNotFound    = errors.New("artifact not found")
	ErrArtifactExpired     = errors.New("artifact has expired")
	ErrArtifactUnavailable = errors.New("artifact content is unavailable")
)

// ArtifactListQuery 定义制品列表的用户过滤与分页
type ArtifactListQuery struct {
	Page, PageSize      int
	RunID, Name, Status string
}

// ArtifactDetail 只暴露展示与授权下载所需的安全字段
type ArtifactDetail struct {
	ArtifactID        string     `json:"artifactId"`
	ProjectID         string     `json:"projectId"`
	PipelineID        string     `json:"pipelineId"`
	PipelineRunID     string     `json:"pipelineRunId"`
	JobRunID          string     `json:"jobRunId"`
	StepRunID         string     `json:"stepRunId"`
	Name              string     `json:"name"`
	Size              int64      `json:"size"`
	SizeKnown         bool       `json:"sizeKnown"`
	MediaType         string     `json:"mediaType"`
	StorageType       string     `json:"storageType"`
	CreatedAt         time.Time  `json:"createdAt"`
	ExpiresAt         *time.Time `json:"expiresAt,omitempty"`
	Status            string     `json:"status"`
	DownloadAvailable bool       `json:"downloadAvailable"`
	UnavailableReason string     `json:"unavailableReason,omitempty"`
}

// ArtifactList 包含过滤后的准确总数及当前页
type ArtifactList struct {
	List     []ArtifactDetail `json:"list"`
	Total    int64            `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"pageSize"`
}

// ArtifactDownload 持有调用方必须关闭的内容流
type ArtifactDownload struct {
	Reader          io.ReadCloser
	Name, MediaType string
	Size            int64
}

// ArtifactService 使用不可变元数据和管理员配置的根目录提供制品读取
type ArtifactService struct {
	artifacts   repo.IArtifactRepository
	storageRepo repo.IStorageRepository
	root        string
	newStorage  func(*storage.Storage) (storage.IStorage, error)
}

// NewArtifactService 创建制品服务，空目录使用管理员环境配置
func NewArtifactService(artifacts repo.IArtifactRepository, storageRepo repo.IStorageRepository, root string) *ArtifactService {
	if root == "" {
		root = artifactutil.Root()
	}
	return &ArtifactService{artifacts: artifacts, storageRepo: storageRepo, root: root, newStorage: storage.NewStorage}
}

// Root 返回当前服务使用的受管理制品根目录
func (s *ArtifactService) Root() string { return s.root }

// List 在权限过滤后分页，并将无法证明位置绑定的旧制品明确标为不可下载
func (s *ArtifactService) List(ctx context.Context, projectID string, query ArtifactListQuery, allow func(string) (bool, error)) (*ArtifactList, error) {
	if s == nil || s.artifacts == nil {
		return nil, ErrArtifactUnavailable
	}
	if query.Page < 1 {
		query.Page = 1
	}
	if query.PageSize < 1 {
		query.PageSize = 20
	}
	if query.PageSize > 100 {
		query.PageSize = 100
	}
	if query.Status != "" && query.Status != "available" && query.Status != "expired" && query.Status != "unavailable" {
		return nil, fmt.Errorf("invalid artifact status")
	}
	rows := make([]ArtifactDetail, 0)
	registered := make(map[string]bool)
	seenArtifactIDs := make(map[string]bool)
	seenLegacyJobs := make(map[string]bool)
	permitted := make(map[string]bool)
	check := func(pipelineID string) (bool, error) {
		if v, ok := permitted[pipelineID]; ok {
			return v, nil
		}
		if allow == nil {
			return false, fmt.Errorf("artifact authorization is required")
		}
		ok, err := allow(pipelineID)
		if err == nil {
			permitted[pipelineID] = ok
		}
		return ok, err
	}
	now := time.Now()
	for offset := 0; ; offset += 100 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		values, total, err := s.artifacts.List(ctx, repo.ArtifactQuery{ProjectID: projectID, RunID: query.RunID, Name: query.Name, Offset: offset, Limit: 100})
		if err != nil {
			return nil, err
		}
		for i := range values {
			value := &values[i]
			// OFFSET 扫描期间的新插入可能让前页记录再次出现，身份去重保持计数准确
			if seenArtifactIDs[value.ArtifactID] {
				continue
			}
			seenArtifactIDs[value.ArtifactID] = true
			registered[value.JobRunID+"\x00"+value.Name] = true
			allowed, err := check(value.PipelineID)
			if err != nil {
				return nil, err
			}
			if !allowed {
				continue
			}
			detail := s.detail(ctx, value, now)
			if query.Status == "" || query.Status == detail.Status {
				rows = append(rows, detail)
			}
		}
		if len(values) == 0 || int64(offset+len(values)) >= total {
			break
		}
	}
	for offset := 0; ; offset += 100 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		jobs, err := s.artifacts.ListLegacyJobs(ctx, repo.ArtifactQuery{ProjectID: projectID, RunID: query.RunID, Offset: offset, Limit: 100})
		if err != nil {
			return nil, err
		}
		for _, job := range jobs {
			if seenLegacyJobs[job.JobRunID] {
				continue
			}
			seenLegacyJobs[job.JobRunID] = true
			allowed, err := check(job.PipelineID)
			if err != nil {
				return nil, err
			}
			if !allowed {
				continue
			}
			// 对象键不返回、不解析为 URL，也不推断当前默认存储
			var names map[string]string
			if err := sonic.UnmarshalString(job.ArtifactURIs, &names); err != nil {
				continue
			}
			for name := range names {
				if artifactutil.Name(name) != nil || registered[job.JobRunID+"\x00"+name] || !strings.Contains(strings.ToLower(name), strings.ToLower(query.Name)) {
					continue
				}
				if query.Status != "" && query.Status != "unavailable" {
					continue
				}
				legacyID := artifactutil.ID(projectID, job.PipelineRunID, job.JobRunID, "legacy", name)
				if seenArtifactIDs[legacyID] {
					continue
				}
				seenArtifactIDs[legacyID] = true
				rows = append(rows, ArtifactDetail{ArtifactID: legacyID, ProjectID: projectID, PipelineID: job.PipelineID, PipelineRunID: job.PipelineRunID, JobRunID: job.JobRunID, Name: name, MediaType: "application/octet-stream", StorageType: "unknown", CreatedAt: job.CreatedAt, Status: "unavailable", UnavailableReason: "Storage identity was not recorded for this older artifact"})
			}
		}
		if len(jobs) < 100 {
			break
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].ArtifactID < rows[j].ArtifactID
		}
		return rows[i].CreatedAt.After(rows[j].CreatedAt)
	})
	result := &ArtifactList{List: make([]ArtifactDetail, 0), Total: int64(len(rows)), Page: query.Page, PageSize: query.PageSize}
	offset := int64(query.Page-1) * int64(query.PageSize)
	if offset < int64(len(rows)) {
		end := offset + int64(query.PageSize)
		if end > int64(len(rows)) {
			end = int64(len(rows))
		}
		result.List = rows[offset:end]
	}
	return result, nil
}

func (s *ArtifactService) detail(ctx context.Context, value *model.ProjectArtifact, now time.Time) ArtifactDetail {
	item := ArtifactDetail{ArtifactID: value.ArtifactID, ProjectID: value.ProjectID, PipelineID: value.PipelineID, PipelineRunID: value.PipelineRunID, JobRunID: value.JobRunID, StepRunID: value.StepRunID, Name: value.Name, Size: value.Size, SizeKnown: true, MediaType: safeArtifactMediaType(value.MediaType), StorageType: value.StorageType, CreatedAt: value.CreatedAt, ExpiresAt: value.ExpiresAt, Status: "available", DownloadAvailable: true}
	if value.ExpiresAt != nil && !value.ExpiresAt.After(now) {
		item.Status = "expired"
		item.DownloadAvailable = false
		return item
	}
	var err error
	if value.StorageType == "local" {
		var file *os.File
		file, err = s.openLocal(ctx, value)
		if file != nil {
			_ = file.Close()
		}
	} else {
		_, err = s.storageConfig(ctx, value)
	}
	if err != nil {
		item.Status = "unavailable"
		item.DownloadAvailable = false
		item.UnavailableReason = "Artifact content or its original storage is unavailable"
	}
	return item
}

// Get 只返回数据库完整归属链校验通过的元数据
func (s *ArtifactService) Get(ctx context.Context, projectID, artifactID string) (*model.ProjectArtifact, error) {
	if s == nil || s.artifacts == nil {
		return nil, ErrArtifactUnavailable
	}
	value, err := s.artifacts.Get(ctx, projectID, artifactID)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, ErrArtifactNotFound
	}
	return value, nil
}

func (s *ArtifactService) openLocal(ctx context.Context, value *model.ProjectArtifact) (*os.File, error) {
	expected, err := artifactutil.LocalPath(value.ProjectID, value.PipelineRunID, value.JobRunID, value.StepRunID, value.Name)
	if err != nil || expected != value.StoragePath {
		return nil, ErrArtifactUnavailable
	}
	file, err := artifactutil.OpenLocal(ctx, s.root, expected)
	if err != nil {
		return nil, ErrArtifactUnavailable
	}
	info, err := file.Stat()
	if err != nil || info.Size() != value.Size {
		_ = file.Close()
		return nil, ErrArtifactUnavailable
	}
	return file, nil
}

func (s *ArtifactService) storageConfig(ctx context.Context, value *model.ProjectArtifact) (*storage.Storage, error) {
	if s == nil || value == nil {
		return nil, ErrArtifactUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	expected, locationErr := artifactutil.RemotePath(value.PipelineRunID, value.JobRunID, value.StepRunID, value.SHA256, value.Name)
	if locationErr != nil || value.StoragePath != expected {
		return nil, ErrArtifactUnavailable
	}
	if s.storageRepo == nil || value.StorageID == "" || value.StorageFingerprint == "" || artifactutil.Name(value.StoragePath) != nil {
		return nil, ErrArtifactUnavailable
	}
	config, err := s.storageRepo.Get(ctx, value.StorageID)
	if err != nil || config == nil || config.IsEnabled != 1 || config.StorageID != value.StorageID || config.StorageType != value.StorageType {
		return nil, ErrArtifactUnavailable
	}
	var detail model.StorageConfigDetail
	if sonic.Unmarshal(config.Config, &detail) != nil {
		return nil, ErrArtifactUnavailable
	}
	fingerprint := artifactutil.StorageFingerprint(config.StorageType, detail.Endpoint, detail.Bucket, detail.Region, detail.BasePath, detail.UseTLS)
	if fingerprint != value.StorageFingerprint {
		return nil, ErrArtifactUnavailable
	}
	return &storage.Storage{Provider: config.StorageType, Endpoint: detail.Endpoint, Bucket: detail.Bucket, Region: detail.Region, BasePath: detail.BasePath, UseTLS: detail.UseTLS, AccessKey: detail.AccessKey, SecretKey: detail.SecretKey}, nil
}

// ResolveStorage 仅打开已持久化且位置指纹匹配的对象存储，供下载和显式清理复用
func (s *ArtifactService) ResolveStorage(ctx context.Context, value *model.ProjectArtifact) (storage.IStorage, error) {
	config, err := s.storageConfig(ctx, value)
	if err != nil {
		return nil, ErrArtifactUnavailable
	}
	if s.newStorage == nil {
		return nil, ErrArtifactUnavailable
	}
	provider, err := s.newStorage(config)
	if err != nil {
		return nil, ErrArtifactUnavailable
	}
	return provider, nil
}

// Open 重新检查过期时间并返回授权后的内容流，绝不生成公开 URL
func (s *ArtifactService) Open(ctx context.Context, projectID, artifactID string) (*ArtifactDownload, error) {
	value, err := s.Get(ctx, projectID, artifactID)
	if err != nil {
		return nil, err
	}
	if value.Size < 0 {
		return nil, ErrArtifactUnavailable
	}
	if value.ExpiresAt != nil && !value.ExpiresAt.After(time.Now()) {
		return nil, ErrArtifactExpired
	}
	var reader io.ReadCloser
	if value.StorageType == "local" {
		file, err := s.openLocal(ctx, value)
		if err != nil {
			return nil, err
		}
		if value.SHA256 != "" {
			digest := sha256.New()
			buffer := make([]byte, 64*1024)
			for {
				if err = ctx.Err(); err != nil {
					break
				}
				var n int
				n, err = file.Read(buffer)
				if n > 0 {
					_, _ = digest.Write(buffer[:n])
				}
				if err != nil {
					break
				}
			}
			if err != io.EOF || !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), value.SHA256) {
				_ = file.Close()
				return nil, ErrArtifactUnavailable
			}
			if _, err = file.Seek(0, io.SeekStart); err != nil {
				_ = file.Close()
				return nil, ErrArtifactUnavailable
			}
		}
		reader = file
	} else {
		provider, err := s.ResolveStorage(ctx, value)
		if err != nil {
			return nil, err
		}
		streaming, ok := provider.(storage.IStreamingStorage)
		if !ok {
			return nil, ErrArtifactUnavailable
		}
		reader, err = streaming.DownloadStream(ctx, value.StoragePath)
		if err != nil {
			if reader != nil {
				_ = reader.Close()
			}
			return nil, ErrArtifactUnavailable
		}
		if reader == nil {
			return nil, ErrArtifactUnavailable
		}
		reader = &artifactContentStream{ReadCloser: reader}
	}
	return &ArtifactDownload{Reader: reader, Name: path.Base(value.Name), MediaType: safeArtifactMediaType(value.MediaType), Size: value.Size}, nil
}

func safeArtifactMediaType(value string) string {
	if value == "" {
		return "application/octet-stream"
	}
	media, _, err := mime.ParseMediaType(value)
	if err != nil {
		return "application/octet-stream"
	}
	return media
}

// artifactContentStream 不让 SDK 的签名 URL、对象键或凭据进入传输层日志
type artifactContentStream struct{ io.ReadCloser }

func (r *artifactContentStream) Read(buffer []byte) (int, error) {
	n, err := r.ReadCloser.Read(buffer)
	return n, safeArtifactStreamError(err)
}
func (r *artifactContentStream) Close() error { return safeArtifactStreamError(r.ReadCloser.Close()) }
func safeArtifactStreamError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, io.EOF):
		return io.EOF
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, io.ErrUnexpectedEOF):
		return io.ErrUnexpectedEOF
	default:
		return ErrArtifactUnavailable
	}
}
