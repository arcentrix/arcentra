// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/gorm"
)

type copilotPrimaryDatabase struct{ db *gorm.DB }

func (d copilotPrimaryDatabase) Database() *gorm.DB {
	return d.db.Session(&gorm.Session{NewDB: true}).Clauses(database.Write())
}

// CopilotPrimaryRepositories 创建无缓存且强制主库读取的仓储，不回退至副本或旧快照
func (r *Repositories) CopilotPrimaryRepositories(ctx context.Context) (*Repositories, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r == nil || r.database == nil {
		return nil, errors.New("primary source repositories unavailable")
	}
	db := r.database.Database()
	if db == nil {
		return nil, errors.New("primary source repositories unavailable")
	}
	primary := copilotPrimaryDatabase{db}
	return &Repositories{
		database: primary,
		User:     NewUserRepo(primary, nil), Team: NewTeamRepo(primary), TeamMember: NewTeamMemberRepo(primary),
		Project: NewProjectRepo(primary), Pipeline: NewPipelineRepo(primary), JobRun: NewJobRunRepo(primary), StepRun: NewStepRunRepo(primary),
		Secret: NewSecretRepo(primary), Permission: NewPermissionRepo(primary), Role: NewRoleRepo(primary), RoleGrant: NewRoleGrantRepo(primary),
		Artifact: NewArtifactRepo(primary), Storage: NewStorageRepo(primary, nil), Log: NewLogRepository(primary.Database()), AuditLog: NewAuditLogRepo(primary),
	}, nil
}

// ICopilotSourceReader 提供受 SQL 数量上限约束的源作业与历史步骤读取
// 多取一条用于可靠检测截断，调用者不能忽略超限结果
// 返回数据只能用于授权后的内部验证与脱敏
// 不提供通用查询或数据库句柄
type ICopilotSourceReader interface {
	ListCopilotSourceJobs(context.Context, string) ([]*model.JobRun, error)
	ListCopilotSourceSteps(context.Context, string, []string) ([]*model.StepRun, error)
}

// ListCopilotSourceJobs 最多读取指定运行的 101 条作业元数据
func (r *JobRunRepo) ListCopilotSourceJobs(ctx context.Context, runID string) ([]*model.JobRun, error) {
	var jobs []*model.JobRun
	err := r.Database().WithContext(ctx).Select("job_run_id", "pipeline_id", "pipeline_run_id", "job_name", "status", "agent_id", "claim_id", "cancellation_claim_id", "terminal_report_digest", "source_expectation_captured", "source_type", "source_repository", "source_ref", "source_binding_digest", "source_resolved_commit", "source_provenance_state").Where("pipeline_run_id = ?", runID).Order("created_at ASC, job_run_id ASC").Limit(101).Find(&jobs).Error
	return jobs, err
}

// ListCopilotSourceSteps 最多读取三个选定作业的 201 条历史步骤脱敏信息
func (r *JobRunRepo) ListCopilotSourceSteps(ctx context.Context, runID string, jobIDs []string) ([]*model.StepRun, error) {
	if len(jobIDs) == 0 || len(jobIDs) > 3 {
		return nil, errors.New("invalid source jobs")
	}
	var steps []*model.StepRun
	err := r.Database().WithContext(ctx).Select("step_run_id", "pipeline_id", "pipeline_run_id", "job_run_id", "env", "secrets").Where("pipeline_run_id = ? AND job_run_id IN ?", runID, jobIDs).Order("step_run_id ASC").Limit(201).Find(&steps).Error
	return steps, err
}

// ICopilotSecretValueReader 对业务标识执行唯一读取，防止其它作用域的重名记录遮蔽应脱敏的值
type ICopilotSecretValueReader interface {
	GetCopilotSecretValue(context.Context, string) (string, error)
}

// GetCopilotSecretValue 在数据库实际排序规则下最多取两行，只有唯一且精确匹配时才返回密文
func (r *SecretRepo) GetCopilotSecretValue(ctx context.Context, secretID string) (string, error) {
	var values []model.Secret
	err := r.Database().WithContext(ctx).Select("secret_id", "secret_value").Where("secret_id = ?", secretID).Limit(2).Find(&values).Error
	if err != nil {
		return "", err
	}
	if len(values) != 1 || values[0].SecretID != secretID || len(values[0].SecretValue) > 128*1024 {
		return "", errors.New("unique secret value unavailable")
	}
	return values[0].SecretValue, nil
}
