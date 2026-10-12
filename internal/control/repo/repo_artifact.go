// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package repo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrArtifactConflict 表示相同制品身份已绑定不同内容或存储位置
var ErrArtifactConflict = errors.New("artifact identity is already bound")

// ArtifactQuery 定义项目制品查询范围，分页在业务权限过滤之后应用
type ArtifactQuery struct {
	ProjectID     string
	RunID         string
	JobRunID      string
	Name          string
	Offset        int
	Limit         int
	ExpiredBefore *time.Time
}

// LegacyArtifactJob 仅保留旧作业中展示制品所需的字段
type LegacyArtifactJob struct {
	ProjectID     string
	PipelineID    string
	PipelineRunID string
	JobRunID      string
	ArtifactURIs  string
	CreatedAt     time.Time
}

// IArtifactRepository 定义带完整归属校验的制品仓储
type IArtifactRepository interface {
	Upsert(context.Context, *model.ProjectArtifact) error
	Get(context.Context, string, string) (*model.ProjectArtifact, error)
	List(context.Context, ArtifactQuery) ([]model.ProjectArtifact, int64, error)
	ListLegacyJobs(context.Context, ArtifactQuery) ([]LegacyArtifactJob, error)
	ListExpired(context.Context, string, string, time.Time, int) ([]model.ProjectArtifact, error)
	DeleteVersion(context.Context, *model.ProjectArtifact) (bool, error)
}

// ArtifactRepo 使用 GORM 保存项目制品的不可变身份
type ArtifactRepo struct{ database.IDatabase }

// NewArtifactRepo 创建制品仓储
func NewArtifactRepo(db database.IDatabase) IArtifactRepository { return &ArtifactRepo{IDatabase: db} }

var artifactColumns = []string{"project_artifact.artifact_id", "project_artifact.project_id", "project_artifact.pipeline_id", "project_artifact.pipeline_run_id", "project_artifact.job_run_id", "project_artifact.step_run_id", "project_artifact.name", "project_artifact.storage_type", "project_artifact.storage_id", "project_artifact.storage_fingerprint", "project_artifact.storage_path", "project_artifact.size", "project_artifact.sha256", "project_artifact.media_type", "project_artifact.expires_at", "project_artifact.created_at", "project_artifact.updated_at"}

// Upsert 幂等登记已完成上传的制品，拒绝身份被重新绑定
func (r *ArtifactRepo) Upsert(ctx context.Context, value *model.ProjectArtifact) error {
	return r.Database().WithContext(ctx).Transaction(func(tx *gorm.DB) error { return UpsertArtifact(ctx, tx, value) })
}

// UpsertArtifact 在调用方事务中校验数据库归属并幂等写入制品
func UpsertArtifact(ctx context.Context, tx *gorm.DB, value *model.ProjectArtifact) error {
	if value == nil || tx == nil {
		return gorm.ErrInvalidData
	}
	// SQLite 的时间列可能按文本比较，统一 UTC 避免偏移量改变过期顺序
	// 使用副本，不修改调用方共享的时间值
	if value.ExpiresAt != nil {
		expires := value.ExpiresAt.UTC()
		value.ExpiresAt = &expires
	}
	if len(value.Name) > 1024 {
		return fmt.Errorf("artifact name is too long")
	}
	if err := artifactutil.Name(value.Name); err != nil {
		return err
	}
	for _, id := range []string{value.PipelineID, value.PipelineRunID, value.JobRunID, value.StepRunID} {
		if err := artifactutil.Identity(id); err != nil {
			return err
		}
	}
	if value.Size < 0 || artifactutil.Digest(value.SHA256) != nil || artifactutil.Name(value.StoragePath) != nil {
		return fmt.Errorf("invalid artifact metadata")
	}
	var pipeline model.Pipeline
	if err := tx.WithContext(ctx).Select("pipeline_id", "project_id").Where("pipeline_id = ? AND is_deleted = ?", value.PipelineID, 0).First(&pipeline).Error; err != nil {
		return err
	}
	if value.ProjectID == "" {
		value.ProjectID = pipeline.ProjectID
	}
	if value.ProjectID != pipeline.ProjectID || artifactutil.Identity(value.ProjectID) != nil {
		return fmt.Errorf("artifact project mismatch")
	}
	var project model.Project
	if err := tx.WithContext(ctx).Select("project_id").Where("project_id = ?", value.ProjectID).First(&project).Error; err != nil {
		return err
	}
	var run model.PipelineRun
	if err := tx.WithContext(ctx).Select("run_id", "pipeline_id").Where("run_id = ? AND pipeline_id = ?", value.PipelineRunID, value.PipelineID).First(&run).Error; err != nil {
		return err
	}
	var job model.JobRun
	if err := tx.WithContext(ctx).Select("job_run_id").Where("job_run_id = ? AND pipeline_run_id = ? AND pipeline_id = ?", value.JobRunID, value.PipelineRunID, value.PipelineID).First(&job).Error; err != nil {
		return err
	}
	var step model.StepRun
	if err := tx.WithContext(ctx).Select("step_run_id").Where("step_run_id = ? AND job_run_id = ? AND pipeline_run_id = ? AND pipeline_id = ?", value.StepRunID, value.JobRunID, value.PipelineRunID, value.PipelineID).First(&step).Error; err != nil {
		return err
	}
	wantID := artifactutil.ID(value.ProjectID, value.PipelineRunID, value.JobRunID, value.StepRunID, value.Name)
	if value.ArtifactID == "" {
		value.ArtifactID = wantID
	}
	if value.ArtifactID != wantID {
		return fmt.Errorf("artifact identity mismatch")
	}
	if value.StorageType == "local" {
		expected, _ := artifactutil.LocalPath(value.ProjectID, value.PipelineRunID, value.JobRunID, value.StepRunID, value.Name)
		if value.StoragePath != expected || value.StorageID != "" || value.StorageFingerprint != "" {
			return fmt.Errorf("artifact location mismatch")
		}
	} else {
		expected, err := artifactutil.RemotePath(value.PipelineRunID, value.JobRunID, value.StepRunID, value.SHA256, value.Name)
		if err != nil || value.StoragePath != expected || artifactutil.Identity(value.StorageID) != nil || artifactutil.Digest(value.StorageFingerprint) != nil {
			return fmt.Errorf("artifact storage binding is required")
		}
	}
	switch value.StorageType {
	case "local", "s3", "minio", "oss", "gcs", "cos":
	default:
		return fmt.Errorf("unsupported artifact storage type")
	}
	// DoNothing 避免并发重试覆盖既有身份，再读取并逐字段校验
	if err := tx.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "artifact_id"}}, DoNothing: true}).Create(value).Error; err != nil {
		return err
	}
	var current model.ProjectArtifact
	if err := tx.WithContext(ctx).Select(artifactColumns).Where("artifact_id = ?", value.ArtifactID).First(&current).Error; err != nil {
		return err
	}
	expiryEqual := current.ExpiresAt == nil && value.ExpiresAt == nil || current.ExpiresAt != nil && value.ExpiresAt != nil && current.ExpiresAt.Equal(*value.ExpiresAt)
	if current.ProjectID != value.ProjectID || current.PipelineID != value.PipelineID || current.PipelineRunID != value.PipelineRunID || current.JobRunID != value.JobRunID || current.StepRunID != value.StepRunID || current.Name != value.Name || current.StorageType != value.StorageType || current.StorageID != value.StorageID || current.StorageFingerprint != value.StorageFingerprint || current.StoragePath != value.StoragePath || current.Size != value.Size || current.SHA256 != value.SHA256 || current.MediaType != value.MediaType || !expiryEqual {
		return ErrArtifactConflict
	}
	value.BaseModel = current.BaseModel
	return nil
}

func (r *ArtifactRepo) scoped(ctx context.Context, query ArtifactQuery) *gorm.DB {
	tx := r.Database().WithContext(ctx).Model(&model.ProjectArtifact{}).
		Joins("JOIN pipeline ON pipeline.pipeline_id = project_artifact.pipeline_id AND pipeline.project_id = project_artifact.project_id AND pipeline.is_deleted = 0").
		Joins("JOIN project ON project.project_id = project_artifact.project_id").
		Joins("JOIN pipeline_run ON pipeline_run.run_id = project_artifact.pipeline_run_id AND pipeline_run.pipeline_id = project_artifact.pipeline_id").
		Joins("JOIN job_run ON job_run.job_run_id = project_artifact.job_run_id AND job_run.pipeline_run_id = project_artifact.pipeline_run_id AND job_run.pipeline_id = project_artifact.pipeline_id").
		Joins("JOIN step_run ON step_run.step_run_id = project_artifact.step_run_id AND step_run.job_run_id = project_artifact.job_run_id AND step_run.pipeline_run_id = project_artifact.pipeline_run_id AND step_run.pipeline_id = project_artifact.pipeline_id").
		Where("project_artifact.project_id = ?", query.ProjectID)
	if query.RunID != "" {
		tx = tx.Where("project_artifact.pipeline_run_id = ?", query.RunID)
	}
	if query.JobRunID != "" {
		tx = tx.Where("project_artifact.job_run_id = ?", query.JobRunID)
	}
	if query.ExpiredBefore != nil {
		tx = tx.Where("project_artifact.expires_at IS NOT NULL AND project_artifact.expires_at <= ?", query.ExpiredBefore.UTC())
	}
	// LIKE 特殊字符转义，用户输入始终作为数据传递
	if query.Name != "" {
		escaped := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(query.Name)
		tx = tx.Where("project_artifact.name LIKE ? ESCAPE '!'", "%"+escaped+"%")
	}
	return tx
}

// Get 仅返回项目、流水线、运行、作业及步骤归属一致的制品
func (r *ArtifactRepo) Get(ctx context.Context, projectID, artifactID string) (*model.ProjectArtifact, error) {
	var value model.ProjectArtifact
	err := r.scoped(ctx, ArtifactQuery{ProjectID: projectID}).Select(artifactColumns).Where("project_artifact.artifact_id = ?", artifactID).First(&value).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &value, nil
}

// List 按稳定顺序返回项目制品和匹配总数
func (r *ArtifactRepo) List(ctx context.Context, query ArtifactQuery) ([]model.ProjectArtifact, int64, error) {
	var total int64
	if err := r.scoped(ctx, query).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	limit := query.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	list := make([]model.ProjectArtifact, 0)
	err := r.scoped(ctx, query).Select(artifactColumns).Order("project_artifact.created_at DESC").Order("project_artifact.artifact_id ASC").Offset(query.Offset).Limit(limit).Find(&list).Error
	return list, total, err
}

// ListLegacyJobs 读取未记录可靠存储身份的旧作业，仅用于不可下载的历史展示
func (r *ArtifactRepo) ListLegacyJobs(ctx context.Context, query ArtifactQuery) ([]LegacyArtifactJob, error) {
	tx := r.Database().WithContext(ctx).Model(&model.JobRun{}).
		Joins("JOIN pipeline ON pipeline.pipeline_id = job_run.pipeline_id AND pipeline.is_deleted = 0").
		Joins("JOIN pipeline_run ON pipeline_run.run_id = job_run.pipeline_run_id AND pipeline_run.pipeline_id = job_run.pipeline_id").
		Where("pipeline.project_id = ?", query.ProjectID).
		Where("job_run.artifact_storage_id IS NULL OR job_run.artifact_storage_id = ?", "").
		Where("job_run.artifact_uris IS NOT NULL AND job_run.artifact_uris <> ? AND job_run.artifact_uris <> ?", "", "{}")
	if query.RunID != "" {
		tx = tx.Where("job_run.pipeline_run_id = ?", query.RunID)
	}
	limit := query.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	list := make([]LegacyArtifactJob, 0)
	err := tx.Select("pipeline.project_id", "job_run.pipeline_id", "job_run.pipeline_run_id", "job_run.job_run_id", "job_run.artifact_uris", "job_run.created_at").Order("job_run.created_at DESC").Order("job_run.job_run_id ASC").Offset(query.Offset).Limit(limit).Scan(&list).Error
	return list, err
}

// ListExpired 返回范围内已过期制品，清理调用方必须先停止执行器
func (r *ArtifactRepo) ListExpired(ctx context.Context, projectID, runID string, before time.Time, limit int) ([]model.ProjectArtifact, error) {
	list, _, err := r.List(ctx, ArtifactQuery{ProjectID: projectID, RunID: runID, ExpiredBefore: &before, Limit: limit})
	return list, err
}

// DeleteVersion 仅删除已经成功移除对象且版本仍匹配的元数据
func (r *ArtifactRepo) DeleteVersion(ctx context.Context, value *model.ProjectArtifact) (bool, error) {
	if value == nil || value.ProjectID == "" || value.ArtifactID == "" {
		return false, gorm.ErrInvalidData
	}
	result := r.Database().WithContext(ctx).Where("project_id = ? AND artifact_id = ? AND updated_at = ?", value.ProjectID, value.ArtifactID, value.UpdatedAt).Delete(&model.ProjectArtifact{})
	return result.RowsAffected == 1, result.Error
}
