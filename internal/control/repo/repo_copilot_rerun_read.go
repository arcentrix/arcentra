// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrCopilotRerunNotFound 表示当前已授权请求者没有该恢复请求
var ErrCopilotRerunNotFound = errors.New("whole-job re-execution request not found")

// CopilotRerunReadProjection 在同一事务内生成安全视图，不得写入或执行网络请求
// nil intent means an ephemeral eligibility preview, never reserved identities.
type CopilotRerunReadProjection func(context.Context, *Repositories, *CopilotRerunScope, *model.CopilotDefinitionSnapshot, *CopilotRerunOriginalEvidence, *CopilotRerunExecutionIdentities, *model.CopilotRerunIntent) error

type rerunReadDatabase struct{ tx *gorm.DB }

func (d rerunReadDatabase) Database() *gorm.DB {
	return d.tx.Session(&gorm.Session{NewDB: true}).Clauses(database.Write(), clause.Locking{Strength: "UPDATE"})
}

// Source domain locks serialize original step mutations; the projection uses
// plain step reads and does not acquire a Step lock after Intent. Secret reads
// remain current locking reads. Project-secret writers acquire Org -> Project
// -> authorization -> Secret first; non-project SecretRepo writers are terminal
// single-table mutations and never acquire an intent/domain lock afterward.
func rerunProjectionRepositories(tx *gorm.DB) (*Repositories, func(context.Context) error) {
	view, current := NewScopedAuthorizationView(tx)
	db := rerunReadDatabase{tx}
	view.Secret = NewSecretRepo(db)
	view.StepRun = NewStepRunRepo(copilotPrimaryDatabase{tx})
	view.JobRun = NewJobRunRepo(db)
	return view, current
}

// CopilotRerunPreview 只读验证前瞻快照和当前权限，不持久化临时身份
func (r *Repositories) CopilotRerunPreview(ctx context.Context, owner, project, job string, authorize CopilotRerunAuthorize, projectView CopilotRerunReadProjection) error {
	if r == nil || r.database == nil || authorize == nil || projectView == nil || !historyID(owner) || !historyID(project) || !historyID(job) {
		return ErrCopilotRerunInvalid
	}
	err := executionTransaction(ctx, r.database.Database(), job, func(tx *gorm.DB) error {
		// Authorize before testing terminal state or snapshot existence.
		if err := rerunAuthorizeSourceRead(ctx, tx, owner, project, job, authorize); err != nil {
			return err
		}
		scope, current, err := lockCopilotRerunSource(ctx, tx, job, project, owner, "preview", authorize)
		if err != nil {
			return err
		}
		var rows []model.CopilotDefinitionSnapshot
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("run_id = ?", scope.Run.RunID).Limit(2).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) != 1 {
			return ErrCopilotRerunUnsupported
		}
		snapshot := &rows[0]
		evidence, err := BuildCopilotRerunOriginalEvidence(snapshot, scope.Run, scope.Job, scope.JobCount, time.Now().UTC())
		if err != nil {
			return ErrCopilotRerunUnsupported
		}
		layout, err := ParseCopilotRerunLayout(snapshot.LayoutJSON)
		if err != nil {
			return ErrCopilotRerunUnsupported
		}
		ids, err := NewCopilotRerunExecutionIdentities(layout)
		if err != nil {
			return err
		}
		projection, projectedCurrent := rerunProjectionRepositories(tx)
		if err := projectView(ctx, projection, scope, snapshot, evidence, ids, nil); err != nil {
			return err
		}
		if err := projectedCurrent(ctx); err != nil {
			return err
		}
		return current(ctx)
	})
	return rerunError(err)
}

func rerunAuthorizeSourceRead(ctx context.Context, tx *gorm.DB, owner, project, jobID string, authorize CopilotRerunAuthorize) error {
	job, err := lockExecutionJob(ctx, tx, jobID, executionLockActive)
	if err != nil {
		return ErrCopilotRerunForbidden
	}
	scope := &CopilotRerunScope{Job: job, Run: &model.PipelineRun{}, Pipeline: &model.Pipeline{}, Project: &model.Project{}}
	for _, q := range []struct {
		column, id string
		row        any
	}{{"run_id", job.PipelineRunID, scope.Run}, {"pipeline_id", job.PipelineID, scope.Pipeline}} {
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where(q.column+" = ?", q.id).First(q.row).Error; err != nil {
			return ErrCopilotRerunForbidden
		}
	}
	// The common domain prefix already holds the actual Project. Never acquire
	// a caller-selected foreign Project after those locks: paired wrong-scope
	// requests could otherwise create a cross-domain cycle before denial.
	if scope.Run.ProjectID != project || scope.Pipeline.ProjectID != project {
		return ErrCopilotRerunForbidden
	}
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("project_id = ?", scope.Pipeline.ProjectID).First(scope.Project).Error; err != nil {
		return ErrCopilotRerunForbidden
	}
	if scope.Job.JobRunID != jobID || scope.Run.RunID != job.PipelineRunID || scope.Run.ProjectID != project || scope.Run.PipelineID != job.PipelineID || scope.Pipeline.PipelineID != job.PipelineID || scope.Pipeline.ProjectID != project || scope.Project.ProjectID != project {
		return ErrCopilotRerunForbidden
	}
	view, current := NewScopedAuthorizationView(tx)
	if err := authorize(ctx, view, scope, owner, "inspect"); err != nil {
		return ErrCopilotRerunForbidden
	}
	return current(ctx)
}

// CopilotRerunLookupRequest 仅查找当前所有者和项目的固定恢复身份，不创建请求
func (r *Repositories) CopilotRerunLookupRequest(ctx context.Context, owner, project, request string, authorize CopilotHistoryAuthorize) (*model.CopilotRerunIntent, error) {
	if r == nil || r.database == nil || authorize == nil || !historyID(owner) || !historyID(project) || !historyID(request) {
		return nil, ErrCopilotRerunInvalid
	}
	var found *model.CopilotRerunIntent
	err := r.database.Database().WithContext(ctx).Clauses(database.Write()).Transaction(func(tx *gorm.DB) error {
		p, active, err := lockSecretProject(tx, project)
		if err != nil || !active {
			return ErrCopilotRerunForbidden
		}
		view, current := NewScopedAuthorizationView(tx)
		if err := authorize(ctx, view, owner, p, ""); err != nil {
			return ErrCopilotRerunForbidden
		}
		var rows []model.CopilotRerunIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("owner_user_id = ? AND project_id = ? AND request_id = ?", owner, project, request).Limit(2).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) > 1 {
			return ErrCopilotRerunConflict
		}
		if len(rows) == 1 {
			if rows[0].OwnerUserID != owner || rows[0].ProjectID != project || rows[0].RequestID != request {
				return ErrCopilotRerunForbidden
			}
			found = &rows[0]
		}
		return current(ctx)
	})
	if err != nil {
		return nil, rerunError(err)
	}
	if found == nil {
		return nil, ErrCopilotRerunNotFound
	}
	return found, nil
}

// CopilotRerunInspectView 在原始行锁和权限事务中验证固定回执并投影安全元数据
func (r *Repositories) CopilotRerunInspectView(ctx context.Context, owner, project, intent string, authorize CopilotRerunAuthorize, projectView CopilotRerunReadProjection) (*model.CopilotRerunIntent, error) {
	if projectView == nil {
		return nil, ErrCopilotRerunInvalid
	}
	return r.copilotRerunIntentOperation(ctx, owner, project, intent, "inspect", authorize, func(tx *gorm.DB, scope *CopilotRerunScope, snapshot *model.CopilotDefinitionSnapshot, saved *model.CopilotRerunIntent, ids *CopilotRerunExecutionIdentities, evidence *CopilotRerunOriginalEvidence) error {
		if saved.State == CopilotRerunConfirmed {
			if err := inspectCopilotRerunTuple(ctx, tx, saved, ids, snapshot, evidence); err != nil {
				return err
			}
		} else if saved.State == CopilotRerunPrepared || saved.State == CopilotRerunCancelled {
			if err := inspectAbsentCopilotRerunTuple(ctx, tx, ids); err != nil {
				return err
			}
		} else {
			return ErrCopilotRerunConflict
		}
		projection, current := rerunProjectionRepositories(tx)
		if err := projectView(ctx, projection, scope, snapshot, evidence, ids, saved); err != nil {
			return err
		}
		return current(ctx)
	})
}

// CopilotRerunReadProject 验证可用性读取的当前项目权限，不创建恢复身份
func (r *Repositories) CopilotRerunReadProject(ctx context.Context, owner, project string, authorize CopilotHistoryAuthorize) error {
	if r == nil || r.database == nil || authorize == nil || !historyID(owner) || !historyID(project) {
		return ErrCopilotRerunInvalid
	}
	return rerunError(r.database.Database().WithContext(ctx).Clauses(database.Write()).Transaction(func(tx *gorm.DB) error {
		p, active, err := lockSecretProject(tx, project)
		if err != nil || !active {
			return ErrCopilotRerunForbidden
		}
		view, current := NewScopedAuthorizationView(tx)
		if err := authorize(ctx, view, owner, p, ""); err != nil {
			return ErrCopilotRerunForbidden
		}
		return current(ctx)
	}))
}

// CopilotRerunReviewRepositories 扩充受信任的当前事务视图以读取脱敏元数据
// It accepts only the repository's own scoped authorization view, never a
// caller-selected database or a separately committing secret service.
func CopilotRerunReviewRepositories(view *Repositories) (*Repositories, error) {
	if view == nil {
		return nil, ErrCopilotRerunInvalid
	}
	users, ok := view.User.(*retryApprovalUsers)
	if !ok || users == nil || users.view == nil || users.view.tx == nil {
		return nil, ErrCopilotRerunInvalid
	}
	if _, ok := users.view.tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return nil, ErrCopilotRerunInvalid
	}
	copy := *view
	db := rerunReadDatabase{users.view.tx}
	copy.Secret = NewSecretRepo(db)
	copy.StepRun = NewStepRunRepo(copilotPrimaryDatabase{users.view.tx})
	copy.JobRun = NewJobRunRepo(db)
	return &copy, nil
}
