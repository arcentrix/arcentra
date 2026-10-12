// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package model

import "time"

// ProjectArtifact 保存已成功上传的制品及不可推断的存储位置绑定
// 此模型只能经过显式 schema migrate 创建，API 绝不在启动时修改数据库
// 位置及存储身份不直接序列化给用户
type ProjectArtifact struct {
	BaseModel
	ArtifactID         string     `gorm:"column:artifact_id;type:varchar(64);uniqueIndex:uk_project_artifact_business_id" json:"artifactId"`
	ProjectID          string     `gorm:"column:project_id;type:varchar(64);index:idx_project_artifact_project" json:"projectId"`
	PipelineID         string     `gorm:"column:pipeline_id;type:varchar(64)" json:"pipelineId"`
	PipelineRunID      string     `gorm:"column:pipeline_run_id;type:varchar(64);index:idx_project_artifact_run" json:"pipelineRunId"`
	JobRunID           string     `gorm:"column:job_run_id;type:varchar(64)" json:"jobRunId"`
	StepRunID          string     `gorm:"column:step_run_id;type:varchar(64)" json:"stepRunId"`
	Name               string     `gorm:"column:name;type:text" json:"name"`
	StorageType        string     `gorm:"column:storage_type;type:varchar(32)" json:"storageType"`
	StorageID          string     `gorm:"column:storage_id;type:varchar(64)" json:"-"`
	StorageFingerprint string     `gorm:"column:storage_fingerprint;type:varchar(64)" json:"-"`
	StoragePath        string     `gorm:"column:storage_path;type:text" json:"-"`
	Size               int64      `gorm:"column:size" json:"size"`
	SHA256             string     `gorm:"column:sha256;type:varchar(64)" json:"-"`
	MediaType          string     `gorm:"column:media_type;type:varchar(255)" json:"mediaType"`
	ExpiresAt          *time.Time `gorm:"column:expires_at;index:idx_project_artifact_expiry" json:"expiresAt,omitempty"`
}

// TableName 返回制品元数据表名
func (ProjectArtifact) TableName() string { return "project_artifact" }
