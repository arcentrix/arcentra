// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/polling"
	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PollingAuthorizationView 只提供在当前事务中持有锁的实时权限读取
type PollingAuthorizationView interface{ LiveRepositories() *Repositories }

// PollingAuthorizer 使用当前人类身份检查项目读取/更新和每个目标的读取/触发权限
type PollingAuthorizer func(context.Context, PollingAuthorizationView, string, string, []string) error

var pollingAuthorizers sync.Map

// SetPollingAuthorizer 安装指定数据库的可信实时鉴权器，nil 使新激活和准入失败关闭
func SetPollingAuthorizer(repositories *Repositories, authorizer PollingAuthorizer) error {
	if repositories == nil || repositories.database == nil || repositories.database.Database() == nil {
		return fmt.Errorf("polling database unavailable")
	}
	db, e := repositories.database.Database().DB()
	if e != nil {
		return e
	}
	if authorizer == nil {
		pollingAuthorizers.Delete(db)
	} else {
		pollingAuthorizers.Store(db, authorizer)
	}
	return nil
}

// BaselineInput 提交显式非执行基线，已观察未确认数据仅在明确许可时丢弃
type BaselineInput struct {
	ProjectID        string
	ActorID          string
	ExpectedRevision uint64
	Scope            polling.Scope
	Cursor           polling.Cursor
	StartedAt        time.Time
	CompletedAt      time.Time
	DiscardPending   bool
}

// EnableInput 仅启用已审阅基线中的精确现有流水线列表
type EnableInput struct {
	ProjectID        string
	ActorID          string
	ExpectedRevision uint64
	BaselineID       string
	PipelineIDs      []string
}

// DisableInput 停止新准入而不取消已准入运行
type DisableInput struct {
	ProjectID        string
	ActorID          string
	ExpectedRevision uint64
}

// CaptureObservationsInput 在任何定义读取前保存本次完整差异观察
type CaptureObservationsInput struct {
	ProjectID            string
	ExpectedRevision     uint64
	ActivationGeneration uint64
	Batch                polling.ObservedBatch
}

// CaptureBatchInput 在运行创建前冻结本代际的完整归一化批次
type CaptureBatchInput struct {
	ProjectID            string
	ExpectedRevision     uint64
	ActivationGeneration uint64
	Batch                polling.Batch
}

// AcknowledgeBatchInput 只推进全部目标已有持久化终结证据的批次
type AcknowledgeBatchInput struct {
	ProjectID            string
	ExpectedRevision     uint64
	ActivationGeneration uint64
	BatchID              string
}

// PauseInput 记录粘性失败关闭原因并保留全部游标与批次证据
type PauseInput struct {
	ProjectID            string
	ExpectedRevision     uint64
	ActivationGeneration uint64
	Reason               string
}

// ISCMPollingRepository 定义无提供方调用的轮询状态事务边界
type ISCMPollingRepository interface {
	GetByProject(context.Context, string) (*model.SCMPollingStream, error)
	ListEnabled(context.Context) ([]*model.SCMPollingStream, error)
	Baseline(context.Context, BaselineInput) (*model.SCMPollingStream, error)
	Enable(context.Context, EnableInput) (*model.SCMPollingStream, error)
	Disable(context.Context, DisableInput) (*model.SCMPollingStream, error)
	CaptureObservations(context.Context, CaptureObservationsInput) (*model.SCMPollingStream, error)
	CaptureBatch(context.Context, CaptureBatchInput) (*model.SCMPollingStream, error)
	AcknowledgeBatch(context.Context, AcknowledgeBatchInput) (*model.SCMPollingStream, error)
	Pause(context.Context, PauseInput) (*model.SCMPollingStream, error)
	ScheduleRetry(context.Context, string, uint64, uint64, time.Time, string) (*model.SCMPollingStream, error)
	MarkUnresolved(context.Context, string, uint64, uint64) (*model.SCMPollingStream, error)
	RecordAttempt(context.Context, string, uint64, uint64) (*model.SCMPollingStream, error)
}

// SCMPollingRepo 实现显式激活和有界批次的持久化
type SCMPollingRepo struct{ database.IDatabase }

// NewSCMPollingRepo 创建轮询状态仓库，不产生基线或授权
func NewSCMPollingRepo(db database.IDatabase) ISCMPollingRepository {
	return &SCMPollingRepo{IDatabase: db}
}

var pollingStreamColumns = []string{"last_attempt_at", "pending_observation_json", "baseline_cutoff", "last_observation_count", "last_target_count", "last_completed_at", "next_poll_at", "last_poll_error", "id", "created_at", "updated_at", "stream_id", "project_id", "scope_json", "scope_digest", "revision", "state", "activation_generation", "enabled_by", "enabled_at", "disabled_by", "disabled_at", "pause_reason", "baseline_id", "baseline_by", "baseline_started_at", "baseline_completed_at", "baseline_digest", "baseline_count", "cursor_json", "selection_json", "selection_digest", "pending_batch_json"}
var pollingPipelineColumns = []string{"pipeline_id", "project_id", "repo_url", "default_branch", "pipeline_file_path", "is_enabled", "is_deleted"}
var pollingProjectColumns = []string{"project_id", "org_id", "repo_type", "repo_url", "default_branch", "auth_type", "trigger_mode", "is_enabled", "status"}

const pollingCycleStreamLimit = 32

func (r *SCMPollingRepo) GetByProject(ctx context.Context, id string) (*model.SCMPollingStream, error) {
	var v model.SCMPollingStream
	e := r.Database().WithContext(ctx).Clauses(database.Write()).Select(pollingStreamColumns).Where("project_id = ?", id).First(&v).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if e == nil && v.ProjectID != id {
		return nil, polling.ErrConflict
	}
	return &v, e
}

// ListEnabled 按最久未尝试顺序有界读取，每次 RecordAttempt 将已尝试流移至队尾，避免截断后饿死后续流
func (r *SCMPollingRepo) ListEnabled(ctx context.Context) ([]*model.SCMPollingStream, error) {
	var v []*model.SCMPollingStream
	e := r.Database().WithContext(ctx).Clauses(database.Write()).Select(pollingStreamColumns).Where("state = ? AND (next_poll_at IS NULL OR next_poll_at <= ?)", polling.StateEnabled, time.Now()).Order("last_attempt_at ASC, stream_id ASC").Limit(pollingCycleStreamLimit).Find(&v).Error
	return v, e
}

// 锁序固定为排序后的流水线 -> 项目 -> 流 -> 运行 -> 作业 -> 用户/成员/团队 -> 权限目录
// 项目变更只取项目/流后缀，流水线变更取流水线/项目/流；旧重试路径不承诺全局无死锁
func lockPollingProject(ctx context.Context, tx *gorm.DB, id string) (*model.Project, error) {
	var rows []model.Project
	e := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select(pollingProjectColumns).Where("project_id = ?", id).Limit(2).Find(&rows).Error
	if e != nil {
		return nil, e
	}
	if len(rows) != 1 || rows[0].ProjectID != id {
		return nil, polling.ErrConflict
	}
	return &rows[0], nil
}
func lockPollingPipelines(ctx context.Context, tx *gorm.DB, ids []string, projectID string) ([]model.Pipeline, error) {
	ids = append([]string(nil), ids...)
	slices.Sort(ids)
	if len(ids) > polling.MaxPipelines || len(slices.Compact(append([]string(nil), ids...))) != len(ids) {
		return nil, polling.ErrConflict
	}
	out := make([]model.Pipeline, 0, len(ids))
	for _, id := range ids {
		var p model.Pipeline
		e := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select(pollingPipelineColumns).Where("pipeline_id = ?", id).First(&p).Error
		if e != nil {
			return nil, e
		}
		if p.PipelineID != id || p.ProjectID != projectID || p.IsEnabled != 1 || p.IsDeleted != 0 {
			return nil, polling.ErrBlocked
		}
		out = append(out, p)
	}
	return out, nil
}
func lockPollingStream(ctx context.Context, tx *gorm.DB, projectID string) (*model.SCMPollingStream, error) {
	var v model.SCMPollingStream
	e := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select(pollingStreamColumns).Where("project_id = ?", projectID).First(&v).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if v.ProjectID != projectID {
		return nil, polling.ErrConflict
	}
	return &v, nil
}
func pollingReady(p *model.Project, s polling.Scope) error {
	repository, e := polling.CanonicalRepository(p.RepoURL)
	if e != nil || s.Validate() != nil || p.ProjectID != s.ProjectID || p.RepoType != model.RepoTypeGitLab || repository != s.Repository || p.IsEnabled != 1 || p.Status != model.ProjectStatusActive || p.TriggerMode&model.TriggerModeMR == 0 {
		return polling.ErrBlocked
	}
	return nil
}
func pollingScope(s *model.SCMPollingStream) (polling.Scope, error) {
	var scope polling.Scope
	if e := sonic.UnmarshalString(s.ScopeJSON, &scope); e != nil {
		return scope, e
	}
	d, e := scope.Digest()
	if e != nil || d != s.ScopeDigest || scope.ProjectID != s.ProjectID {
		return scope, polling.ErrConflict
	}
	return scope, nil
}
func pollingSelection(pipelines []model.Pipeline, scope polling.Scope) (polling.Selection, error) {
	v := polling.Selection{Version: polling.Version}
	for _, p := range pipelines {
		r, e := polling.CanonicalRepository(p.RepoURL)
		if e != nil || r != scope.Repository {
			return v, polling.ErrBlocked
		}
		v.Pipelines = append(v.Pipelines, polling.PipelineBinding{PipelineID: p.PipelineID, Repository: r, DefaultBranch: p.DefaultBranch, DefinitionPath: p.PipelineFilePath})
	}
	_, e := v.Canonical()
	return v, e
}
func authorizePolling(ctx context.Context, tx *gorm.DB, actor, project string, ids []string) (*retryApprovalView, error) {
	if actor == "" || actor != strings.TrimSpace(actor) || actor == polling.Actor || actor == "cron" || actor == "webhook" {
		return nil, polling.ErrForbidden
	}
	db, e := tx.DB()
	if e != nil {
		return nil, e
	}
	raw, ok := pollingAuthorizers.Load(db)
	if !ok {
		return nil, fmt.Errorf("polling authorizer unavailable")
	}
	authorize, ok := raw.(PollingAuthorizer)
	if !ok || authorize == nil {
		return nil, fmt.Errorf("polling authorizer unavailable")
	}
	view := &retryApprovalView{tx: tx}
	repos := view.LiveRepositories()
	if user, e := repos.User.(IExecutionActorReader).GetExecutionActor(ctx, actor); e != nil {
		if errors.Is(e, ErrApprovalForbidden) {
			return nil, polling.ErrForbidden
		}
		return nil, e
	} else if user.IsEnabled != 1 {
		return nil, polling.ErrForbidden
	}
	members, e := repos.TeamMember.ListUserTeams(ctx, actor)
	if e != nil {
		return nil, e
	}
	if len(members) > 1000 {
		return nil, polling.ErrForbidden
	}
	var teams []string
	for _, m := range members {
		if m.UserID != actor || m.TeamID == "" {
			return nil, polling.ErrForbidden
		}
		teams = append(teams, m.TeamID)
	}
	slices.Sort(teams)
	teams = slices.Compact(teams)
	if len(teams) > 0 {
		if _, e := repos.Team.BatchGet(ctx, teams); e != nil {
			return nil, e
		}
	}
	if e := authorize(ctx, view, actor, project, append([]string(nil), ids...)); e != nil {
		return nil, e
	}
	return view, view.current(ctx)
}
func updatePollingStream(ctx context.Context, tx *gorm.DB, s *model.SCMPollingStream, updates map[string]any) error {
	if s.Revision == math.MaxUint64 || s.ActivationGeneration == math.MaxUint64 {
		return polling.ErrConflict
	}
	updates["revision"] = s.Revision + 1
	updates["updated_at"] = time.Now()
	res := tx.WithContext(ctx).Model(&model.SCMPollingStream{}).Where("stream_id = ? AND revision = ?", s.StreamID, s.Revision).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return polling.ErrConflict
	}
	return nil
}
func refreshPollingStream(ctx context.Context, tx *gorm.DB, s *model.SCMPollingStream) error {
	var fresh model.SCMPollingStream
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select(pollingStreamColumns).Where("stream_id = ?", s.StreamID).First(&fresh).Error; err != nil {
		return err
	}
	*s = fresh
	return nil
}
func checkPollingRevision(s *model.SCMPollingStream, revision uint64) error {
	if s == nil || s.Revision != revision {
		return polling.ErrConflict
	}
	return nil
}
func requirePollingActive(s *model.SCMPollingStream, generation uint64) error {
	if s == nil || s.State != polling.StateEnabled || s.ActivationGeneration != generation || s.EnabledBy == "" {
		return polling.ErrConflict
	}
	return nil
}

// Baseline 保存非执行基线，绝不创建运行或隐式启用
func (r *SCMPollingRepo) Baseline(ctx context.Context, in BaselineInput) (*model.SCMPollingStream, error) {
	body, e := in.Cursor.Canonical()
	if e != nil {
		return nil, e
	}
	scopeJSON, e := in.Scope.Canonical()
	if e != nil || in.Scope.ProjectID != in.ProjectID {
		return nil, polling.ErrBlocked
	}
	scopeDigest, _ := in.Scope.Digest()
	baselineDigest, _ := in.Cursor.Digest()
	if in.StartedAt.IsZero() || in.CompletedAt.Before(in.StartedAt) {
		return nil, polling.ErrBlocked
	}
	var out *model.SCMPollingStream
	e = r.Database().WithContext(ctx).Clauses(database.Write()).Transaction(func(tx *gorm.DB) error {
		p, e := lockPollingProject(ctx, tx, in.ProjectID)
		if e != nil {
			return e
		}
		if e = pollingReady(p, in.Scope); e != nil {
			return e
		}
		s, e := lockPollingStream(ctx, tx, in.ProjectID)
		if e != nil {
			return e
		}
		if s == nil && in.ExpectedRevision != 0 || s != nil && s.Revision != in.ExpectedRevision {
			return polling.ErrConflict
		}
		if s != nil && s.State == polling.StateEnabled {
			return polling.ErrConflict
		}
		if s != nil && s.PendingObservationJSON != "" && !in.DiscardPending {
			return polling.ErrUnresolved
		}
		if s != nil && s.PendingObservationJSON != "" {
			if _, err := polling.ParseObservedBatch(s.PendingObservationJSON); err != nil {
				return err
			}
		}
		if s != nil && s.PendingBatchJSON != "" {
			if !in.DiscardPending {
				return polling.ErrUnresolved
			}
			batch, e := polling.ParseBatch(s.PendingBatchJSON)
			if e != nil {
				return e
			}
			for _, t := range batch.Targets {
				var runs []model.PipelineRun
				if e := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select(pollingRunColumns).Where("pipeline_id = ? AND request_id = ?", t.PipelineID, t.RequestID).Limit(2).Find(&runs).Error; e != nil {
					return e
				}
				if len(runs) > 1 || len(runs) == 1 && pollingDuplicateOutcome(&runs[0], &polling.RunContext{ScopeDigest: batch.ScopeDigest, Target: t}, batch) == polling.OutcomeUnresolved {
					return polling.ErrUnresolved
				}
			}
		}
		view, e := authorizePolling(ctx, tx, in.ActorID, in.ProjectID, nil)
		if e != nil {
			return e
		}
		baselineID := uuid.NewString()
		if s == nil {
			s = &model.SCMPollingStream{StreamID: uuid.NewString(), ProjectID: in.ProjectID, Revision: 1, ActivationGeneration: 1, State: polling.StateBaselined, ScopeJSON: scopeJSON, ScopeDigest: scopeDigest, BaselineCutoff: in.StartedAt.UTC().Format(time.RFC3339Nano), BaselineID: baselineID, BaselineBy: in.ActorID, BaselineStartedAt: &in.StartedAt, BaselineCompletedAt: &in.CompletedAt, BaselineDigest: baselineDigest, BaselineCount: len(in.Cursor.Observed), CursorJSON: body}
			if e := tx.WithContext(ctx).Create(s).Error; e != nil {
				return e
			}
		} else {
			updates := map[string]any{"next_poll_at": nil, "last_poll_error": "", "last_attempt_at": nil, "last_completed_at": nil, "last_observation_count": 0, "last_target_count": 0, "state": polling.StateBaselined, "activation_generation": s.ActivationGeneration + 1, "scope_json": scopeJSON, "scope_digest": scopeDigest, "baseline_cutoff": in.StartedAt.UTC().Format(time.RFC3339Nano), "baseline_id": baselineID, "baseline_by": in.ActorID, "baseline_started_at": in.StartedAt, "baseline_completed_at": in.CompletedAt, "baseline_digest": baselineDigest, "baseline_count": len(in.Cursor.Observed), "cursor_json": body, "pending_observation_json": "", "pending_batch_json": "", "selection_json": "", "selection_digest": "", "enabled_by": "", "enabled_at": nil, "pause_reason": ""}
			if e := updatePollingStream(ctx, tx, s, updates); e != nil {
				return e
			}
			if e := refreshPollingStream(ctx, tx, s); e != nil {
				return e
			}
		}
		out = s
		return view.current(ctx)
	})
	if e != nil {
		return nil, e
	}
	return out, nil
}

// Enable 原子记录精确选择与持续授权者，不自动执行基线观察
func (r *SCMPollingRepo) Enable(ctx context.Context, in EnableInput) (*model.SCMPollingStream, error) {
	var out *model.SCMPollingStream
	if len(in.PipelineIDs) == 0 {
		return nil, polling.ErrBlocked
	}
	e := r.Database().WithContext(ctx).Clauses(database.Write()).Transaction(func(tx *gorm.DB) error {
		pipelines, e := lockPollingPipelines(ctx, tx, in.PipelineIDs, in.ProjectID)
		if e != nil {
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
		if s.State != polling.StateBaselined || s.BaselineID != in.BaselineID || s.PendingBatchJSON != "" || s.PendingObservationJSON != "" {
			return polling.ErrConflict
		}
		baseline, e := polling.ParseCursor(s.CursorJSON)
		if e != nil {
			return e
		}
		baselineDigest, e := baseline.Digest()
		cutoff, cutoffErr := time.Parse(time.RFC3339Nano, s.BaselineCutoff)
		if e != nil || cutoffErr != nil || cutoff.IsZero() || baselineDigest != s.BaselineDigest || len(baseline.Observed) != s.BaselineCount || s.BaselineBy == "" || s.BaselineStartedAt == nil || s.BaselineCompletedAt == nil || s.BaselineCompletedAt.Before(*s.BaselineStartedAt) {
			return polling.ErrConflict
		}
		scope, e := pollingScope(s)
		if e != nil {
			return e
		}
		if e := pollingReady(p, scope); e != nil {
			return e
		}
		selection, e := pollingSelection(pipelines, scope)
		if e != nil {
			return e
		}
		body, _ := selection.Canonical()
		digest, _ := selection.Digest()
		view, e := authorizePolling(ctx, tx, in.ActorID, in.ProjectID, in.PipelineIDs)
		if e != nil {
			return e
		}
		now := time.Now()
		if e := updatePollingStream(ctx, tx, s, map[string]any{"next_poll_at": nil, "last_poll_error": "", "state": polling.StateEnabled, "activation_generation": s.ActivationGeneration + 1, "enabled_by": in.ActorID, "enabled_at": now, "selection_json": body, "selection_digest": digest, "pause_reason": ""}); e != nil {
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
	return out, nil
}

// Disable 使当前代际失效并保留全部待处理证据
func (r *SCMPollingRepo) Disable(ctx context.Context, in DisableInput) (*model.SCMPollingStream, error) {
	var out *model.SCMPollingStream
	e := r.Database().WithContext(ctx).Clauses(database.Write()).Transaction(func(tx *gorm.DB) error {
		if _, e := lockPollingProject(ctx, tx, in.ProjectID); e != nil {
			return e
		}
		s, e := lockPollingStream(ctx, tx, in.ProjectID)
		if e != nil {
			return e
		}
		if e := checkPollingRevision(s, in.ExpectedRevision); e != nil {
			return e
		}
		view, e := authorizePolling(ctx, tx, in.ActorID, in.ProjectID, nil)
		if e != nil {
			return e
		}
		if e := updatePollingStream(ctx, tx, s, map[string]any{"state": polling.StateDisabled, "activation_generation": s.ActivationGeneration + 1, "disabled_by": in.ActorID, "disabled_at": time.Now(), "pause_reason": ""}); e != nil {
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
	return out, nil
}

// CaptureBatch 将当前扫描的精确目标先持久化，再允许专用运行创建
func (r *SCMPollingRepo) CaptureBatch(ctx context.Context, in CaptureBatchInput) (*model.SCMPollingStream, error) {
	var out *model.SCMPollingStream
	var denied error
	e := r.Database().WithContext(ctx).Clauses(database.Write()).Transaction(func(tx *gorm.DB) error {
		ids := make([]string, 0, len(in.Batch.Targets))
		for _, t := range in.Batch.Targets {
			ids = append(ids, t.PipelineID)
		}
		slices.Sort(ids)
		ids = slices.Compact(ids)
		pipelines, e := lockPollingPipelines(ctx, tx, ids, in.ProjectID)
		if e != nil {
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
		if s.PendingBatchJSON != "" {
			return polling.ErrUnresolved
		}
		scope, e := pollingScope(s)
		if e != nil {
			return e
		}
		if e := pollingReady(p, scope); e != nil {
			return e
		}
		selection, e := polling.ParseSelection(s.SelectionJSON)
		if e != nil {
			return e
		}
		if d, e := selection.Digest(); e != nil || d != s.SelectionDigest {
			return polling.ErrConflict
		}
		for _, p := range pipelines {
			bound, ok := selection.Find(p.PipelineID)
			current, e := pollingSelection([]model.Pipeline{p}, scope)
			if e != nil || !ok || bound != current.Pipelines[0] {
				return polling.ErrConflict
			}
		}
		b := in.Batch
		observed, err := polling.ParseObservedBatch(s.PendingObservationJSON)
		if err != nil {
			return err
		}
		nextCursor, err := b.NextCursor.Canonical()
		if err != nil {
			return err
		}
		observedCursor, err := observed.NextCursor.Canonical()
		if err != nil {
			return err
		}
		if observed.ID != b.ID || observed.ScopeDigest != b.ScopeDigest || observed.Generation != b.Generation || observed.SelectionDigest != b.SelectionDigest || nextCursor != observedCursor || b.ObservationCount != len(observed.Observations) {
			return polling.ErrConflict
		}
		observations := map[string]polling.Observation{}
		for _, o := range observed.Observations {
			observations[o.ObservationID] = o
		}
		for _, target := range b.Targets {
			observation, ok := observations[target.ObservationID]
			if !ok || observation != target.Observation() {
				return polling.ErrConflict
			}
		}
		if len(b.Results) != 0 {
			return polling.ErrConflict
		}
		if b.ScopeDigest != s.ScopeDigest || b.Generation != s.ActivationGeneration || b.SelectionDigest != s.SelectionDigest {
			return polling.ErrConflict
		}
		b.Revision = s.Revision + 1
		for _, t := range b.Targets {
			req, e := polling.EventRequestID(scope, t.ObservationID)
			if e != nil || req != t.RequestID {
				return polling.ErrConflict
			}
			bound, ok := selection.Find(t.PipelineID)
			if !ok || bound.DefinitionPath != t.DefinitionPath {
				return polling.ErrConflict
			}
		}
		body, e := b.Canonical()
		if e != nil {
			return e
		}
		oldCursor, e := polling.ParseCursor(s.CursorJSON)
		if e != nil {
			return e
		}
		oldTime, _ := time.Parse(time.RFC3339Nano, oldCursor.Boundary)
		newTime, _ := time.Parse(time.RFC3339Nano, b.NextCursor.Boundary)
		if newTime.Before(oldTime) {
			return polling.ErrConflict
		}
		view, e := authorizePolling(ctx, tx, s.EnabledBy, in.ProjectID, ids)
		if e != nil {
			if errors.Is(e, polling.ErrForbidden) {
				denied = e
				return pausePollingStream(ctx, tx, s, "authorization_denied")
			}
			return e
		}
		if e := updatePollingStream(ctx, tx, s, map[string]any{"pending_observation_json": "", "pending_batch_json": body, "next_poll_at": nil, "last_poll_error": ""}); e != nil {
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

// Pause 以受限原因使激活粘性失效，不清除游标或批次
func (r *SCMPollingRepo) Pause(ctx context.Context, in PauseInput) (*model.SCMPollingStream, error) {
	if !pollingPauseReason(in.Reason) {
		return nil, polling.ErrBlocked
	}
	var out *model.SCMPollingStream
	e := r.Database().WithContext(ctx).Clauses(database.Write()).Transaction(func(tx *gorm.DB) error {
		if _, e := lockPollingProject(ctx, tx, in.ProjectID); e != nil {
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
		if e := pausePollingStream(ctx, tx, s, in.Reason); e != nil {
			return e
		}
		if e := refreshPollingStream(ctx, tx, s); e != nil {
			return e
		}
		out = s
		return nil
	})
	if e != nil {
		return nil, e
	}
	return out, nil
}
func pollingPauseReason(v string) bool {
	switch v {
	case "authorization_denied", "configuration_changed", "definition_unavailable", "definition_rejected", "provider_incomplete", "provider_unavailable", "recovery_required":
		return true
	}
	return false
}
func pausePollingStream(ctx context.Context, tx *gorm.DB, s *model.SCMPollingStream, reason string) error {
	return updatePollingStream(ctx, tx, s, map[string]any{"state": polling.StatePaused, "activation_generation": s.ActivationGeneration + 1, "pause_reason": reason})
}

// ScheduleRetry 记录提供方短暂故障的有界退避，不改变激活代际或已确认游标
func (r *SCMPollingRepo) ScheduleRetry(ctx context.Context, projectID string, expectedRevision, generation uint64, notBefore time.Time, reason string) (*model.SCMPollingStream, error) {
	switch reason {
	case "provider_rate_limited", "provider_unavailable", "provider_incomplete":
	default:
		return nil, polling.ErrBlocked
	}
	now := time.Now()
	if notBefore.Before(now) || notBefore.After(now.Add(24*time.Hour)) {
		return nil, polling.ErrBlocked
	}
	if notBefore.Before(now.Add(time.Minute)) {
		notBefore = now.Add(time.Minute)
	}
	var out *model.SCMPollingStream
	e := r.Database().WithContext(ctx).Clauses(database.Write()).Transaction(func(tx *gorm.DB) error {
		if _, e := lockPollingProject(ctx, tx, projectID); e != nil {
			return e
		}
		s, e := lockPollingStream(ctx, tx, projectID)
		if e != nil {
			return e
		}
		if e := checkPollingRevision(s, expectedRevision); e != nil {
			return e
		}
		if e := requirePollingActive(s, generation); e != nil {
			return e
		}
		if e := updatePollingStream(ctx, tx, s, map[string]any{"next_poll_at": notBefore, "last_poll_error": reason}); e != nil {
			return e
		}
		if e := refreshPollingStream(ctx, tx, s); e != nil {
			return e
		}
		out = s
		return nil
	})
	if e != nil {
		return nil, e
	}
	return out, nil
}

// MarkUnresolved 记录未确认运行的非授权状态提示，不撤销并发创建者的有效代际
func (r *SCMPollingRepo) MarkUnresolved(ctx context.Context, projectID string, expectedRevision, generation uint64) (*model.SCMPollingStream, error) {
	var out *model.SCMPollingStream
	e := r.Database().WithContext(ctx).Clauses(database.Write()).Transaction(func(tx *gorm.DB) error {
		if _, e := lockPollingProject(ctx, tx, projectID); e != nil {
			return e
		}
		s, e := lockPollingStream(ctx, tx, projectID)
		if e != nil {
			return e
		}
		if e := checkPollingRevision(s, expectedRevision); e != nil {
			return e
		}
		if e := requirePollingActive(s, generation); e != nil {
			return e
		}
		if s.PendingBatchJSON == "" {
			return polling.ErrConflict
		}
		if e := updatePollingMetadata(ctx, tx, s, map[string]any{"last_poll_error": "recovery_required"}); e != nil {
			return e
		}
		if e := refreshPollingStream(ctx, tx, s); e != nil {
			return e
		}
		out = s
		return nil
	})
	if e != nil {
		return nil, e
	}
	return out, nil
}

// RecordAttempt 记录公平轮转元数据，不授予执行权限或改变已确认观察进度
func (r *SCMPollingRepo) RecordAttempt(ctx context.Context, projectID string, expectedRevision, generation uint64) (*model.SCMPollingStream, error) {
	var out *model.SCMPollingStream
	e := r.Database().WithContext(ctx).Clauses(database.Write()).Transaction(func(tx *gorm.DB) error {
		if _, e := lockPollingProject(ctx, tx, projectID); e != nil {
			return e
		}
		s, e := lockPollingStream(ctx, tx, projectID)
		if e != nil {
			return e
		}
		if e := checkPollingRevision(s, expectedRevision); e != nil {
			return e
		}
		if e := requirePollingActive(s, generation); e != nil {
			return e
		}
		if e := updatePollingMetadata(ctx, tx, s, map[string]any{"last_attempt_at": time.Now()}); e != nil {
			return e
		}
		if e := refreshPollingStream(ctx, tx, s); e != nil {
			return e
		}
		out = s
		return nil
	})
	if e != nil {
		return nil, e
	}
	return out, nil
}

// 非授权提示字段不推进语义 revision，避免多个控制器的公平记录持续冲掉有效快照
func updatePollingMetadata(ctx context.Context, tx *gorm.DB, s *model.SCMPollingStream, updates map[string]any) error {
	result := tx.WithContext(ctx).Model(&model.SCMPollingStream{}).Where("stream_id = ? AND revision = ? AND activation_generation = ? AND state = ?", s.StreamID, s.Revision, s.ActivationGeneration, s.State).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 1 {
		return polling.ErrConflict
	}
	return nil
}
