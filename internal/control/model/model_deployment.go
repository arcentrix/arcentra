// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package model

import "time"

// DeploymentEnvironment 保存不可变的项目发布目标，不包含集群地址或凭据
type DeploymentEnvironment struct {
	BaseModel
	EnvironmentID   string `gorm:"column:environment_id;type:varchar(64);uniqueIndex" json:"environmentId"`
	ProjectID       string `gorm:"column:project_id;type:varchar(64);index;uniqueIndex:uk_deployment_target,priority:1" json:"projectId"`
	Name            string `gorm:"column:name;type:varchar(128)" json:"name"`
	TargetRef       string `gorm:"column:target_ref;type:varchar(64);uniqueIndex:uk_deployment_target,priority:2" json:"targetRef"`
	Namespace       string `gorm:"column:namespace;type:varchar(63);uniqueIndex:uk_deployment_target,priority:3" json:"namespace"`
	Deployment      string `gorm:"column:deployment;type:varchar(253);uniqueIndex:uk_deployment_target,priority:4" json:"deployment"`
	Container       string `gorm:"column:container;type:varchar(63)" json:"container"`
	ActiveReleaseID string `gorm:"column:active_release_id;type:varchar(64);default:''" json:"activeReleaseId,omitempty"`
}

// TableName 返回部署环境表名
func (DeploymentEnvironment) TableName() string { return "deployment_environment" }

// DeploymentRelease 保存不可变制品来源和可恢复的部署观察记录
type DeploymentRelease struct {
	BaseModel
	// Origin 区分显式发布与经授权的流水线部署步骤
	Origin            string `gorm:"column:origin;type:varchar(32);not null;default:'manual'" json:"origin"`
	ExecutionJobRunID string `gorm:"column:execution_job_run_id;type:varchar(64)" json:"executionJobRunId,omitempty"`
	// ExecutionStepRunID 使用可空唯一索引，禁止同一执行步骤产生第二个发布
	ExecutionStepRunID *string    `gorm:"column:execution_step_run_id;type:varchar(64);uniqueIndex:uk_deployment_execution_step" json:"executionStepRunId,omitempty"`
	ProducerJobRunID   string     `gorm:"column:producer_job_run_id;type:varchar(64)" json:"producerJobRunId,omitempty"`
	ProducerStepRunID  string     `gorm:"column:producer_step_run_id;type:varchar(64)" json:"producerStepRunId,omitempty"`
	DeploymentPlanHash string     `gorm:"column:deployment_plan_hash;type:varchar(64)" json:"deploymentPlanHash,omitempty"`
	LastRecoveryBy     string     `gorm:"column:last_recovery_by;type:varchar(64)" json:"lastRecoveryBy,omitempty"`
	LastRecoveryAt     *time.Time `gorm:"column:last_recovery_at" json:"lastRecoveryAt,omitempty"`
	Version            int64      `gorm:"column:version;default:1" json:"-"`
	ReleaseID          string     `gorm:"column:release_id;type:varchar(64);uniqueIndex" json:"releaseId"`
	ProjectID          string     `gorm:"column:project_id;type:varchar(64);uniqueIndex:uk_deployment_request,priority:1;index" json:"projectId"`
	RequestID          string     `gorm:"column:request_id;type:varchar(128);uniqueIndex:uk_deployment_request,priority:2" json:"requestId"`
	EnvironmentID      string     `gorm:"column:environment_id;type:varchar(64);index" json:"environmentId"`
	PipelineID         string     `gorm:"column:pipeline_id;type:varchar(64)" json:"pipelineId"`
	PipelineRunID      string     `gorm:"column:pipeline_run_id;type:varchar(64)" json:"pipelineRunId"`
	ArtifactID         string     `gorm:"column:artifact_id;type:varchar(64)" json:"artifactId"`
	TargetFingerprint  string     `gorm:"column:target_fingerprint;type:varchar(64)" json:"-"`
	ArtifactSHA256     string     `gorm:"column:artifact_sha256;type:varchar(64)" json:"artifactSha256"`
	CommitSHA          string     `gorm:"column:commit_sha;type:varchar(64)" json:"commitSha"`
	Image              string     `gorm:"column:image;type:varchar(512)" json:"image"`
	CreatedBy          string     `gorm:"column:created_by;type:varchar(64)" json:"createdBy"`
	Status             string     `gorm:"column:status;type:varchar(32);index" json:"status"`
	Reason             string     `gorm:"column:reason;type:varchar(255)" json:"reason,omitempty"`
	WorkloadUID        string     `gorm:"column:workload_uid;type:varchar(128)" json:"workloadUid,omitempty"`
	BaselineGeneration int64      `gorm:"column:baseline_generation" json:"baselineGeneration,omitempty"`
	AppliedGeneration  int64      `gorm:"column:applied_generation" json:"appliedGeneration,omitempty"`
	ObservedGeneration int64      `gorm:"column:observed_generation" json:"observedGeneration,omitempty"`
	DesiredReplicas    int32      `gorm:"column:desired_replicas" json:"desiredReplicas"`
	UpdatedReplicas    int32      `gorm:"column:updated_replicas" json:"updatedReplicas"`
	AvailableReplicas  int32      `gorm:"column:available_replicas" json:"availableReplicas"`
	LastObservedAt     *time.Time `gorm:"column:last_observed_at" json:"lastObservedAt,omitempty"`
	FinishedAt         *time.Time `gorm:"column:finished_at" json:"finishedAt,omitempty"`
}

// TableName 返回发布记录表名
func (DeploymentRelease) TableName() string { return "deployment_release" }

// IsDeploymentTerminal 判断发布是否已经结束，历史成功不表示当前集群健康
func IsDeploymentTerminal(status string) bool {
	return status == "succeeded" || status == "failed" || status == "superseded" || status == "cancelled"
}
