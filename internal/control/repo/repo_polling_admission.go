// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/polling"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var pollingRunColumns = []string{"run_id", "pipeline_id", "project_id", "inbox_actor_id", "inbox_origin", "request_id", "trigger_type", "triggered_by", "definition_commit_sha", "definition_path", "branch", "commit_sha", "env", "status", "start_time", "end_time", "allow_deployments", "deployment_actor_id", "deployment_plan", "deployment_plan_hash", "deployment_target_fingerprint", "retry_approval_context", "retry_approval_context_digest", "polling_context", "polling_context_digest"}

func rejectPollingRunUpdates(updates map[string]any) error {
	for k := range updates {
		if strings.HasPrefix(strings.ReplaceAll(strings.ToLower(k), "_", ""), "polling") {
			return fmt.Errorf("polling run context is immutable")
		}
	}
	return nil
}

// IsPollingPipelineRun 检查完整轮询身份但不授予执行权限
func IsPollingPipelineRun(run *model.PipelineRun) bool {
	_, e := pollingRunContext(run)
	return e == nil
}
func pollingRunContext(run *model.PipelineRun) (*polling.RunContext, error) {
	if run == nil || !emptyRunInboxBinding(run) || run.TriggerType != int(pipelinev1.TriggerType_TRIGGER_TYPE_EVENT) || run.TriggeredBy != polling.Actor || run.AllowDeployments || run.DeploymentActorID != "" || run.DeploymentPlan != "" || run.DeploymentPlanHash != "" || run.DeploymentTargetFingerprint != "" || run.RetryApprovalContext != "" || run.RetryApprovalContextDigest != "" {
		return nil, polling.ErrConflict
	}
	c, e := polling.ParseRunContext(run.PollingContext)
	if e != nil {
		return nil, e
	}
	d, e := c.Digest()
	if e != nil || d != run.PollingContextDigest || c.RunID != run.RunID || c.Target.PipelineID != run.PipelineID || c.Target.RequestID != run.RequestID || c.Target.DefinitionCommitSHA != run.DefinitionCommitSha || c.Target.DefinitionPath != run.DefinitionPath || c.Target.SourceBranch != run.Branch || c.Target.SourceSHA != run.CommitSha {
		return nil, polling.ErrConflict
	}
	return c, nil
}
func samePollingRunIdentity(a, b *model.PipelineRun) bool {
	return IsPollingPipelineRun(a) && IsPollingPipelineRun(b) && a.RunID == b.RunID && a.PipelineID == b.PipelineID && a.RequestID == b.RequestID && a.PollingContext == b.PollingContext && a.PollingContextDigest == b.PollingContextDigest && a.DefinitionCommitSha == b.DefinitionCommitSha && a.DefinitionPath == b.DefinitionPath && a.Branch == b.Branch && a.CommitSha == b.CommitSha && sameAutomaticEnvironment(a.Env, b.Env)
}
func lockPollingAuthority(ctx context.Context, tx *gorm.DB, c *polling.RunContext) (*model.SCMPollingStream, *polling.Batch, error) {
	pipelines, e := lockPollingPipelines(ctx, tx, []string{c.Target.PipelineID}, c.Scope.ProjectID)
	if e != nil {
		return nil, nil, e
	}
	p, e := lockPollingProject(ctx, tx, c.Scope.ProjectID)
	if e != nil {
		return nil, nil, e
	}
	s, e := lockPollingStream(ctx, tx, c.Scope.ProjectID)
	if e != nil {
		return nil, nil, e
	}
	if e := requirePollingActive(s, c.ActivationGeneration); e != nil {
		return nil, nil, e
	}
	if s.BaselineID != c.BaselineID || s.BaselineCutoff != c.BaselineCutoff || s.StreamID != c.StreamID || s.ScopeDigest != c.ScopeDigest || s.SelectionDigest != c.SelectionDigest || s.EnabledBy != c.ActorID {
		return nil, nil, polling.ErrConflict
	}
	scope, e := pollingScope(s)
	if e != nil || scope != c.Scope {
		return nil, nil, polling.ErrConflict
	}
	if e := pollingReady(p, scope); e != nil {
		return nil, nil, e
	}
	selection, e := polling.ParseSelection(s.SelectionJSON)
	if e != nil {
		return nil, nil, e
	}
	d, e := selection.Digest()
	if e != nil || d != c.SelectionDigest {
		return nil, nil, polling.ErrConflict
	}
	bound, ok := selection.Find(c.Target.PipelineID)
	actual, e := pollingSelection(pipelines, scope)
	if e != nil || !ok || bound != actual.Pipelines[0] || bound.DefinitionPath != c.Target.DefinitionPath {
		return nil, nil, polling.ErrConflict
	}
	batch, e := polling.ParseBatchForScope(s.PendingBatchJSON, scope)
	if e != nil {
		return nil, nil, e
	}
	if batch.ID != c.BatchID || batch.Revision != c.BatchRevision || batch.Generation != c.ActivationGeneration || batch.ScopeDigest != c.ScopeDigest || batch.SelectionDigest != c.SelectionDigest {
		return nil, nil, polling.ErrConflict
	}
	for _, t := range batch.Targets {
		if t == c.Target {
			return s, batch, nil
		}
	}
	return nil, nil, polling.ErrConflict
}
func pollingReceipt(batch *polling.Batch, pipelineID, requestID string) (polling.TargetResult, bool) {
	for _, r := range batch.Results {
		if r.PipelineID == pipelineID && r.RequestID == requestID {
			return r, true
		}
	}
	return polling.TargetResult{}, false
}
func recordPollingReceipt(ctx context.Context, tx *gorm.DB, s *model.SCMPollingStream, batch *polling.Batch, run *model.PipelineRun, outcome polling.Outcome) error {
	if existing, ok := pollingReceipt(batch, run.PipelineID, run.RequestID); ok {
		if existing.RunID != run.RunID || existing.Outcome != outcome {
			return polling.ErrConflict
		}
		return nil
	}
	batch.Results = append(batch.Results, polling.TargetResult{PipelineID: run.PipelineID, RequestID: run.RequestID, RunID: run.RunID, Outcome: outcome})
	scope, e := pollingScope(s)
	if e != nil {
		return e
	}
	body, e := batch.CanonicalForScope(scope)
	if e != nil {
		return e
	}
	return updatePollingStream(ctx, tx, s, map[string]any{"pending_batch_json": body, "last_poll_error": "", "next_poll_at": nil})
}
func pollingDuplicateOutcome(stored *model.PipelineRun, c *polling.RunContext, batch *polling.Batch) polling.Outcome {
	old, e := pollingRunContext(stored)
	if e != nil || stored.PipelineID != c.Target.PipelineID || stored.RequestID != c.Target.RequestID || old.ScopeDigest != c.ScopeDigest || old.Target.ObservationID != c.Target.ObservationID {
		return polling.OutcomeUnresolved
	}
	if stored.StartTime != nil && stored.Status >= model.PipelineStatusRunning && stored.Status <= model.PipelineStatusPaused {
		return polling.OutcomeAdmitted
	}
	if r, ok := pollingReceipt(batch, stored.PipelineID, stored.RequestID); ok && r.RunID == stored.RunID && r.Outcome == polling.OutcomeRejected && stored.Status == model.PipelineStatusFailed && stored.StartTime == nil && stored.EndTime != nil {
		return polling.OutcomeRejected
	}
	return polling.OutcomeUnresolved
}

// CreatePollingRun 仅为持久化冻结目标建立新的可信运行，重复 PENDING 从不重新提交
func (r *PipelineRepo) CreatePollingRun(ctx context.Context, run *model.PipelineRun) (polling.CreationOutcome, error) {
	out := polling.CreationOutcome{Outcome: polling.OutcomeUnresolved}
	c, e := pollingRunContext(run)
	if e != nil {
		return out, e
	}
	if run.ProjectID != "" || run.Status != model.PipelineStatusPending || run.StartTime != nil || run.EndTime != nil {
		return out, polling.ErrConflict
	}
	if e := normalizeJSONFields(map[string]*string{"env": &run.Env}, pipelineRunJSONDefaults); e != nil {
		return out, e
	}
	var denied error
	e = r.Database().WithContext(ctx).Clauses(database.Write()).Transaction(func(tx *gorm.DB) error {
		s, batch, e := lockPollingAuthority(ctx, tx, c)
		if e != nil {
			return e
		}
		var rows []model.PipelineRun
		if e := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select(pollingRunColumns).Where("pipeline_id = ? AND request_id = ?", run.PipelineID, run.RequestID).Limit(2).Find(&rows).Error; e != nil {
			return e
		}
		if len(rows) > 1 {
			return polling.ErrConflict
		}
		if len(rows) == 1 {
			out.RunID = rows[0].RunID
			out.Outcome = pollingDuplicateOutcome(&rows[0], c, batch)
			if out.Outcome != polling.OutcomeUnresolved {
				return recordPollingReceipt(ctx, tx, s, batch, &rows[0], out.Outcome)
			}
			return nil
		}
		view, e := authorizePolling(ctx, tx, c.ActorID, c.Scope.ProjectID, []string{run.PipelineID})
		if e != nil {
			if errors.Is(e, polling.ErrForbidden) {
				denied = e
				return pausePollingStream(ctx, tx, s, "authorization_denied")
			}
			return e
		}
		if createErr := tx.WithContext(ctx).Create(run).Error; createErr != nil {
			// 唯一键冲突后重新读取确定赢家，但创建错误从不授权本次调度
			var winner model.PipelineRun
			readErr := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select(pollingRunColumns).Where("pipeline_id = ? AND request_id = ?", run.PipelineID, run.RequestID).First(&winner).Error
			return errors.Join(createErr, readErr)
		}
		out = polling.CreationOutcome{Outcome: polling.OutcomeCreated, RunID: run.RunID, CreatedNow: true}
		return view.current(ctx)
	})
	if e != nil {
		return polling.CreationOutcome{Outcome: polling.OutcomeUnresolved}, e
	}
	if denied != nil {
		return polling.CreationOutcome{Outcome: polling.OutcomeUnresolved}, denied
	}
	return out, nil
}

// StartPollingRunIfPending 在当前激活和实时权限锁内认领运行并记录持久化准入回执
func (r *PipelineRepo) StartPollingRunIfPending(ctx context.Context, run *model.PipelineRun) (polling.AdmissionOutcome, error) {
	return r.transitionPendingPollingRun(ctx, run, true)
}

// RejectPendingPollingRun 仅拒绝身份匹配、没有执行证据的本批次运行并保存明确回执
func (r *PipelineRepo) RejectPendingPollingRun(ctx context.Context, run *model.PipelineRun) (polling.AdmissionOutcome, error) {
	return r.transitionPendingPollingRun(ctx, run, false)
}
func (r *PipelineRepo) transitionPendingPollingRun(ctx context.Context, run *model.PipelineRun, start bool) (polling.AdmissionOutcome, error) {
	out := polling.AdmissionOutcome{Outcome: polling.OutcomeUnresolved}
	c, e := pollingRunContext(run)
	if e != nil {
		return out, e
	}
	var denied error
	e = r.Database().WithContext(ctx).Clauses(database.Write()).Transaction(func(tx *gorm.DB) error {
		s, batch, e := lockPollingAuthority(ctx, tx, c)
		if e != nil {
			return e
		}
		var stored model.PipelineRun
		e = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select(pollingRunColumns).Where("run_id = ?", run.RunID).First(&stored).Error
		if e != nil {
			return e
		}
		if !samePollingRunIdentity(&stored, run) {
			return polling.ErrConflict
		}
		out.RunID = stored.RunID
		if stored.Status != model.PipelineStatusPending || stored.StartTime != nil || stored.EndTime != nil {
			out.Outcome = pollingDuplicateOutcome(&stored, c, batch)
			return nil
		}
		if stored.ProjectID != "" || run.ProjectID != "" {
			return polling.ErrConflict
		}
		var jobs []model.JobRun
		if e := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select("job_run_id").Where("pipeline_run_id = ?", stored.RunID).Limit(1).Find(&jobs).Error; e != nil {
			return e
		}
		if len(jobs) != 0 {
			return nil
		}
		view, e := authorizePolling(ctx, tx, c.ActorID, c.Scope.ProjectID, []string{run.PipelineID})
		if e != nil {
			if errors.Is(e, polling.ErrForbidden) {
				denied = e
				result := tx.WithContext(ctx).Model(&model.PipelineRun{}).Where("run_id = ? AND status = ? AND start_time IS NULL AND end_time IS NULL", stored.RunID, model.PipelineStatusPending).Updates(map[string]any{"status": model.PipelineStatusFailed, "end_time": time.Now()})
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return polling.ErrConflict
				}
				if _, exists := pollingReceipt(batch, stored.PipelineID, stored.RequestID); exists {
					return polling.ErrConflict
				}
				batch.Results = append(batch.Results, polling.TargetResult{PipelineID: stored.PipelineID, RequestID: stored.RequestID, RunID: stored.RunID, Outcome: polling.OutcomeRejected})
				body, err := batch.CanonicalForScope(c.Scope)
				if err != nil {
					return err
				}
				out.Outcome = polling.OutcomeRejected
				return updatePollingStream(ctx, tx, s, map[string]any{"state": polling.StatePaused, "activation_generation": s.ActivationGeneration + 1, "pause_reason": "authorization_denied", "pending_batch_json": body})
			}
			return e
		}
		updates := map[string]any{"status": model.PipelineStatusFailed, "end_time": time.Now()}
		out.Outcome = polling.OutcomeRejected
		if start {
			updates = map[string]any{"status": model.PipelineStatusRunning, "start_time": time.Now(), "project_id": c.Scope.ProjectID}
			out.Outcome = polling.OutcomeAdmitted
		}
		result := tx.WithContext(ctx).Model(&model.PipelineRun{}).Where("run_id = ? AND status = ? AND start_time IS NULL AND end_time IS NULL", stored.RunID, model.PipelineStatusPending).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return polling.ErrConflict
		}
		if e := recordPollingReceipt(ctx, tx, s, batch, &stored, out.Outcome); e != nil {
			return e
		}
		out.ClaimedNow = start
		return view.current(ctx)
	})
	if e != nil {
		return polling.AdmissionOutcome{Outcome: polling.OutcomeUnresolved}, e
	}
	if denied != nil {
		return out, denied
	}
	return out, nil
}

// AcknowledgeBatch 仅在逐目标验证明确回执后原子推进完整游标
func (r *SCMPollingRepo) AcknowledgeBatch(ctx context.Context, in AcknowledgeBatchInput) (*model.SCMPollingStream, error) {
	// 非锁定读取仅用于确定锁顺序，所有证据稍后重新读取并用 revision 校验
	observed, e := r.GetByProject(ctx, in.ProjectID)
	if e != nil {
		return nil, e
	}
	if e := checkPollingRevision(observed, in.ExpectedRevision); e != nil {
		return nil, e
	}
	beforeScope, e := pollingScope(observed)
	if e != nil {
		return nil, e
	}
	before, e := polling.ParseBatchForScope(observed.PendingBatchJSON, beforeScope)
	if e != nil {
		return nil, e
	}
	ids := make([]string, 0, len(before.Targets))
	seen := map[string]bool{}
	for _, t := range before.Targets {
		if !seen[t.PipelineID] {
			ids = append(ids, t.PipelineID)
			seen[t.PipelineID] = true
		}
	}
	var out *model.SCMPollingStream
	var denied error
	e = r.Database().WithContext(ctx).Clauses(database.Write()).Transaction(func(tx *gorm.DB) error {
		if _, e := lockPollingPipelines(ctx, tx, ids, in.ProjectID); e != nil {
			return e
		}
		p, e := lockPollingProject(ctx, tx, in.ProjectID)
		if e != nil {
			return e
		}
		s, e := lockPollingStream(ctx, tx, in.ProjectID)
		if e != nil {
			return e
		}
		if e := checkPollingRevision(s, in.ExpectedRevision); e != nil {
			return e
		}
		if e := requirePollingActive(s, in.ActivationGeneration); e != nil {
			return e
		}
		scope, e := pollingScope(s)
		if e != nil {
			return e
		}
		if e := pollingReady(p, scope); e != nil {
			return e
		}
		batch, e := polling.ParseBatchForScope(s.PendingBatchJSON, scope)
		if e != nil {
			return e
		}
		if batch.ID != in.BatchID || batch.ID != before.ID || batch.Generation != in.ActivationGeneration || batch.ScopeDigest != s.ScopeDigest || batch.SelectionDigest != s.SelectionDigest {
			return polling.ErrConflict
		}
		for _, target := range batch.Targets {
			receipt, ok := pollingReceipt(batch, target.PipelineID, target.RequestID)
			if !ok {
				return polling.ErrUnresolved
			}
			var run model.PipelineRun
			if e := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select(pollingRunColumns).Where("pipeline_id = ? AND request_id = ?", target.PipelineID, target.RequestID).First(&run).Error; e != nil {
				return e
			}
			c, e := pollingRunContext(&run)
			if e != nil || run.RunID != receipt.RunID || run.PipelineID != target.PipelineID || run.RequestID != target.RequestID || c.ScopeDigest != s.ScopeDigest || c.Target.ObservationID != target.ObservationID {
				return polling.ErrConflict
			}
			if receipt.Outcome == polling.OutcomeAdmitted {
				if run.StartTime == nil || run.Status == model.PipelineStatusPending {
					return polling.ErrUnresolved
				}
			} else if receipt.Outcome == polling.OutcomeRejected {
				if run.Status != model.PipelineStatusFailed || run.StartTime != nil || run.EndTime == nil {
					return polling.ErrUnresolved
				}
			} else {
				return polling.ErrUnresolved
			}
		}
		view, e := authorizePolling(ctx, tx, s.EnabledBy, in.ProjectID, ids)
		if e != nil {
			if errors.Is(e, polling.ErrForbidden) {
				denied = e
				return pausePollingStream(ctx, tx, s, "authorization_denied")
			}
			return e
		}
		cursor, e := batch.NextCursor.Canonical()
		if e != nil {
			return e
		}
		if e := updatePollingStream(ctx, tx, s, map[string]any{"cursor_json": cursor, "pending_batch_json": "", "last_observation_count": batch.ObservationCount, "last_target_count": len(batch.Targets), "last_completed_at": time.Now(), "next_poll_at": nil, "last_poll_error": ""}); e != nil {
			return e
		}
		if e := refreshPollingStream(ctx, tx, s); e != nil {
			return e
		}
		out = s
		return view.current(ctx)
	})
	if e != nil {
		return nil, e
	}
	if denied != nil {
		return nil, denied
	}
	return out, nil
}
