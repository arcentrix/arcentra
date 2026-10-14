// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package model

// JobRunGeneration 固定每个控制面授权的整作业代次及原子转换回执
// GenerationJSON 永不改写，ClosureRequest 只在准确后继准入事务写入一次
type JobRunGeneration struct {
	RetryApprovalAdmission       string `gorm:"column:retry_approval_admission;type:mediumtext" json:"-"`
	RetryApprovalAdmissionDigest string `gorm:"column:retry_approval_admission_digest;type:varchar(67);not null;default:''" json:"-"`
	BaseModel
	GenerationID     string `gorm:"column:generation_id;type:varchar(64);uniqueIndex:uk_job_generation_id" json:"generationId"`
	JobRunID         string `gorm:"column:job_run_id;type:varchar(64);not null;uniqueIndex:uk_job_generation_number,priority:1" json:"jobRunId"`
	Number           int    `gorm:"column:number;not null;uniqueIndex:uk_job_generation_number,priority:2" json:"number"`
	State            string `gorm:"column:state;type:varchar(16);not null" json:"state"` // preparing、active、closed 或 aborted
	GenerationJSON   string `gorm:"column:generation_json;type:mediumtext;not null" json:"-"`
	AdmissionRequest string `gorm:"column:admission_request;type:mediumtext;not null" json:"-"`
	AdmissionDigest  string `gorm:"column:admission_digest;type:varchar(67);not null" json:"-"`
	ClosureRequest   string `gorm:"column:closure_request;type:mediumtext;not null;default:('')" json:"-"`
	ClosureDigest    string `gorm:"column:closure_digest;type:varchar(67);not null;default:''" json:"-"`
	NextGenerationID string `gorm:"column:next_generation_id;type:varchar(64);not null;default:''" json:"-"`
}

// TableName 返回有界作业代次记录表名
func (JobRunGeneration) TableName() string { return "job_run_generation" }
