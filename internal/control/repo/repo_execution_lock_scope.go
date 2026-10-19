// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"fmt"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// executionScopeProof is transaction-local evidence that the shared identity
// prefix preceded this Job lock. It is not an authorization or liveness grant.
var errExecutionParentMissing = errors.New("execution parent job missing")

type executionScopeProof struct {
	pool                        gorm.ConnPool
	job, run, pipeline, project string
}

const executionScopeProofKey = "arcentra.execution.scope.prefix"

type executionLockMode int

const (
	executionLockActive executionLockMode = iota
	executionLockTerminalReceipt
	executionLockInspect
	executionLockLegacy
)

func executionLegacyNoShared(job *model.JobRun) bool {
	return job != nil && emptyRuntime(job) && job.RetryPlan == "" && job.RetryPlanDigest == "" && emptyRetryApproval(job) && emptyJobGeneration(job) && emptyJobInputs(job)
}
func requireExecutionScope(tx *gorm.DB, job *model.JobRun) error {
	if tx == nil || job == nil {
		return fmt.Errorf("execution scope prefix required")
	}
	value, ok := tx.Statement.Settings.Load(executionScopeProofKey)
	proof, valid := value.(executionScopeProof)
	if !ok || !valid || proof.pool != tx.Statement.ConnPool || proof.job != job.JobRunID || proof.run != job.PipelineRunID || proof.pipeline != job.PipelineID || proof.project == "" {
		return fmt.Errorf("execution scope prefix required")
	}
	return nil
}

type executionScopeLocator struct {
	pool             gorm.ConnPool
	job              model.JobRun
	pipeline         model.Pipeline
	project          model.Project
	jobErr, scopeErr error
}

const executionScopeLocatorKey = "arcentra.execution.scope.locator"

// Plain locator reads happen before BEGIN so they cannot establish an old
// REPEATABLE READ snapshot ahead of a contended exact historical receipt.
func executionTransaction(ctx context.Context, db *gorm.DB, id string, fn func(*gorm.DB) error) error {
	if db == nil || db.Statement == nil {
		return fmt.Errorf("execution database required")
	}
	if _, nested := db.Statement.ConnPool.(gorm.TxCommitter); nested {
		return fmt.Errorf("nested execution transaction forbidden")
	}
	locator := executionScopeLocator{}
	if id != "" {
		locator.jobErr = db.WithContext(ctx).Select(jobSnapshotColumns).Where("job_run_id = ?", id).First(&locator.job).Error
		if errors.Is(locator.jobErr, gorm.ErrRecordNotFound) {
			locator.jobErr = errExecutionParentMissing
		}
		if locator.jobErr == nil {
			locator.scopeErr = db.WithContext(ctx).Select("pipeline_id", "project_id").Where("pipeline_id = ?", locator.job.PipelineID).First(&locator.pipeline).Error
			if locator.scopeErr == nil {
				locator.scopeErr = db.WithContext(ctx).Select("project_id", "org_id").Where("project_id = ?", locator.pipeline.ProjectID).First(&locator.project).Error
			}
		}
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locator.pool = tx.Statement.ConnPool
		tx.Statement.Settings.Store(executionScopeLocatorKey, locator)
		return fn(tx)
	})
}
func executionStepJobID(step *model.StepRun) string {
	if step == nil {
		return ""
	}
	return step.JobRunID
}
func executionApprovalJobID(approval *model.ApprovalRequest) string {
	if approval == nil || approval.RetryJobRunID == nil {
		return ""
	}
	return *approval.RetryJobRunID
}

// Locator reads carry no authority. All immutable links are rechecked under
// identity-only P -> optional O -> Q -> R -> J locks, without active-scope filters.
func lockExecutionJob(ctx context.Context, tx *gorm.DB, id string, mode executionLockMode) (*model.JobRun, error) {
	if tx == nil || tx.Statement == nil {
		return nil, fmt.Errorf("execution transaction required")
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return nil, fmt.Errorf("execution transaction required")
	}

	value, ok := tx.Statement.Settings.Load(executionScopeLocatorKey)
	locator, valid := value.(executionScopeLocator)
	if !ok || !valid || locator.pool != tx.Statement.ConnPool {
		return nil, fmt.Errorf("execution locator required")
	}
	if errors.Is(locator.jobErr, errExecutionParentMissing) {
		var current []model.JobRun
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select("job_run_id").Where("job_run_id = ?", id).Limit(2).Find(&current).Error; err != nil {
			return nil, err
		}
		if len(current) == 0 {
			return nil, errExecutionParentMissing
		}
		return nil, fmt.Errorf("execution parent appeared after locator read")
	}
	if locator.jobErr != nil {
		return nil, locator.jobErr
	}
	hint := locator.job
	if hint.JobRunID != id {
		return nil, fmt.Errorf("execution job identity changed")
	}
	narrow := mode == executionLockInspect || (mode == executionLockTerminalReceipt && model.IsJobRunTerminal(hint.Status)) || (mode == executionLockLegacy && executionLegacyNoShared(&hint))
	if narrow {
		var job model.JobRun
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select(jobSnapshotColumns).Where("job_run_id = ?", id).First(&job).Error; err != nil {
			return nil, err
		}
		if job.JobRunID != id || job.PipelineID != hint.PipelineID || job.PipelineRunID != hint.PipelineRunID || (mode == executionLockTerminalReceipt && !model.IsJobRunTerminal(job.Status)) || (mode == executionLockLegacy && !executionLegacyNoShared(&job)) {
			return nil, fmt.Errorf("execution locator changed")
		}
		return &job, nil
	}
	if err := requireExecutionScope(tx, &hint); err == nil {
		var job model.JobRun
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select(jobSnapshotColumns).Where("job_run_id = ?", id).First(&job).Error; err != nil {
			return nil, err
		}
		if err := requireExecutionScope(tx, &job); err != nil {
			return nil, err
		}
		return &job, nil
	}
	q := func() *gorm.DB { return tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}) }
	var pipelines []model.Pipeline
	if err := q().Select("pipeline_id", "project_id").Where("pipeline_id = ?", hint.PipelineID).Limit(2).Find(&pipelines).Error; err != nil {
		return nil, err
	}
	if len(pipelines) != 1 || pipelines[0].PipelineID != hint.PipelineID || pipelines[0].ProjectID == "" {
		return nil, fmt.Errorf("execution pipeline scope unavailable")
	}
	p := pipelines[0]
	if locator.scopeErr != nil {
		return nil, locator.scopeErr
	}
	if locator.pipeline.PipelineID != p.PipelineID || locator.pipeline.ProjectID != p.ProjectID || locator.project.ProjectID != p.ProjectID {
		return nil, fmt.Errorf("execution project locator changed")
	}
	projectLocator := locator.project
	if projectLocator.OrgID != "" {
		var orgs []model.Organization
		if err := q().Select("org_id").Where("org_id = ?", projectLocator.OrgID).Limit(2).Find(&orgs).Error; err != nil {
			return nil, err
		}
		if len(orgs) != 1 || orgs[0].OrgID != projectLocator.OrgID {
			return nil, fmt.Errorf("execution organization identity changed")
		}
	}
	var projects []model.Project
	if err := q().Select("project_id", "org_id").Where("project_id = ?", p.ProjectID).Limit(2).Find(&projects).Error; err != nil {
		return nil, err
	}
	if len(projects) != 1 || projects[0].ProjectID != p.ProjectID || projects[0].OrgID != projectLocator.OrgID {
		return nil, fmt.Errorf("execution project scope changed")
	}
	var runs []model.PipelineRun
	if err := q().Select("run_id", "pipeline_id", "project_id", "triggered_by", "definition_commit_sha", "definition_path", "retry_approval_context", "retry_approval_context_digest").Where("run_id = ?", hint.PipelineRunID).Limit(2).Find(&runs).Error; err != nil {
		return nil, err
	}
	if len(runs) != 1 || runs[0].RunID != hint.PipelineRunID || runs[0].PipelineID != p.PipelineID || (runs[0].ProjectID != "" && runs[0].ProjectID != p.ProjectID) {
		return nil, fmt.Errorf("execution run scope changed")
	}
	if runs[0].ProjectID == "" {
		run := runs[0]
		c, err := retryapproval.ParseRunContext(run.RetryApprovalContext)
		if err != nil {
			return nil, fmt.Errorf("execution run project scope unavailable")
		}
		digest, err := c.Digest()
		if err != nil || digest != run.RetryApprovalContextDigest || c.ProjectID != p.ProjectID || c.PipelineID != p.PipelineID || c.PipelineRunID != run.RunID || c.ActorID != run.TriggeredBy || c.DefinitionCommitSHA != run.DefinitionCommitSha || c.DefinitionPath != run.DefinitionPath {
			return nil, fmt.Errorf("execution run project scope changed")
		}
	}
	var jobs []model.JobRun
	if err := q().Select(jobSnapshotColumns).Where("job_run_id = ?", id).Limit(2).Find(&jobs).Error; err != nil {
		return nil, err
	}
	if len(jobs) != 1 || jobs[0].JobRunID != id || jobs[0].PipelineID != p.PipelineID || jobs[0].PipelineRunID != runs[0].RunID {
		return nil, fmt.Errorf("execution job scope changed")
	}
	tx.Statement.Settings.Store(executionScopeProofKey, executionScopeProof{pool: tx.Statement.ConnPool, job: id, run: runs[0].RunID, pipeline: p.PipelineID, project: p.ProjectID})
	return &jobs[0], nil
}
