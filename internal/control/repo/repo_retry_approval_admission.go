// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package repo

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RetryApprovalAuthorizationView 只提供与准入事务绑定的当前权限读取
// 所有读取持有行锁至提交，禁止服务获取原始数据库或传入预先计算的授权结果
type RetryApprovalAuthorizationView interface{ LiveRepositories() *Repositories }

// RetryApprovalAuthorizer 在执行锁内校验请求者或指定投票动作及当前操作者策略
type RetryApprovalAuthorizer func(context.Context, RetryApprovalAuthorizationView, *retryapproval.Evidence, string, string) error

var retryApprovalAuthorizers sync.Map // *sql.DB -> RetryApprovalAuthorizer

// SetRetryApprovalAuthorizer 安装仅作用于指定数据库的可信实时权限校验器
// nil 会卸载校验器，之后所有需要审批的新增执行一律失败关闭
func SetRetryApprovalAuthorizer(repositories *Repositories, authorizer RetryApprovalAuthorizer) error {
	if repositories == nil || repositories.database == nil {
		return fmt.Errorf("retry approval database unavailable")
	}
	db, err := repositories.database.Database().DB()
	if err != nil {
		return err
	}
	if authorizer == nil {
		retryApprovalAuthorizers.Delete(db)
	} else {
		retryApprovalAuthorizers.Store(db, authorizer)
	}
	return nil
}
func retryApprovalAuthorizer(tx *gorm.DB) (RetryApprovalAuthorizer, error) {
	db, err := tx.DB()
	if err != nil {
		return nil, err
	}
	value, ok := retryApprovalAuthorizers.Load(db)
	if !ok {
		return nil, fmt.Errorf("retry approval authorizer unavailable")
	}
	authorize, ok := value.(RetryApprovalAuthorizer)
	if !ok || authorize == nil {
		return nil, fmt.Errorf("retry approval authorizer unavailable")
	}
	return authorize, nil
}

// 执行锁始终先锁 job，再锁 run/agent/generation 和 approval
// failed_step CAS 在 job/generation 后按 step_index/attempt 锁定完整账本
// 历史或 inspect 回执不取共享 run/agent 锁；仅活动执行路径随后锁 run/agent/approval
// 输入生产者校验先取 pipeline/project，再取注册制品、生产者 job/step 锁
// 共享 run/agent 与生产者 job 的跨作业反向等待仍可能触发数据库死锁回滚
// 因此这里仅保证同作业线性化及当前读取，不宣称整个授权图具有全局无环锁序
// 权限锁固定为 pipeline -> project -> 排序的全部用户 -> 全部成员关系 -> 团队
// 此后才进入统一的 permission -> binding -> role -> grant 当前读取
// 决策与取消必须先锁 job；部署审批仍保留独立的 release-first 路径
// FOR UPDATE 是 MySQL REPEATABLE READ 下的 current read，不使用早先一致性快照
// 权限撤销先提交则读取被撤销值；准入先持锁则撤销等待其提交
type retryApprovalView struct {
	tx      *gorm.DB
	expires time.Time
}

func (v *retryApprovalView) query(ctx context.Context) *gorm.DB {
	return v.tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"})
}
func (v *retryApprovalView) LiveRepositories() *Repositories {
	return &Repositories{
		User: &retryApprovalUsers{view: v}, Project: &retryApprovalProjects{view: v}, Pipeline: &retryApprovalPipelines{view: v}, TeamMember: &retryApprovalMembers{view: v}, Team: &retryApprovalTeams{view: v}, Permission: &retryApprovalPermissions{view: v}, RoleGrant: &retryApprovalGrants{view: v}, Role: &retryApprovalRoles{view: v},
	}
}

// 在读取共享权限目录前锁定全部参与者，避免请求者/投票人交叉造成锁环
func (v *retryApprovalView) lockSubjects(ctx context.Context, evidence *retryapproval.Evidence, actors []string) error {
	repositories := v.LiveRepositories()
	pipeline, err := repositories.Pipeline.Get(ctx, evidence.Context.PipelineID)
	if err != nil {
		return err
	}
	if pipeline.ProjectID != evidence.Context.ProjectID {
		return ErrApprovalForbidden
	}
	if _, err := repositories.Project.Get(ctx, evidence.Context.ProjectID); err != nil {
		return err
	}
	slices.Sort(actors)
	actors = slices.Compact(actors)
	reader := repositories.User.(IExecutionActorReader)
	for _, actor := range actors {
		if _, err := reader.GetExecutionActor(ctx, actor); err != nil {
			return err
		}
	}
	var teams []string
	for _, actor := range actors {
		members, err := repositories.TeamMember.ListUserTeams(ctx, actor)
		if err != nil {
			return err
		}
		if len(members) > 1000 {
			return ErrApprovalForbidden
		}
		for _, member := range members {
			if member.UserID != actor || member.TeamID == "" {
				return ErrApprovalForbidden
			}
			teams = append(teams, member.TeamID)
		}
	}
	slices.Sort(teams)
	teams = slices.Compact(teams)
	if len(teams) > 0 {
		if _, err := repositories.Team.BatchGet(ctx, teams); err != nil {
			return err
		}
	}
	return nil
}

func (v *retryApprovalView) current(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !v.expires.IsZero() && !time.Now().Before(v.expires) {
		return fmt.Errorf("authorization grant expired during admission")
	}
	return nil
}

// 接口嵌入仅兼容现有鉴权器，外观只实现所需读取，不携带数据库或写入实现
type retryApprovalUsers struct {
	IUserRepository
	view *retryApprovalView
}

func (r *retryApprovalUsers) GetExecutionActor(ctx context.Context, id string) (*model.User, error) {
	var values []model.User
	if err := r.view.query(ctx).Select("user_id", "is_enabled").Where("user_id = ?", id).Limit(2).Find(&values).Error; err != nil {
		return nil, err
	}
	if len(values) != 1 || values[0].UserID != id {
		return nil, ErrApprovalForbidden
	}
	return &values[0], nil
}

type retryApprovalProjects struct {
	IProjectRepository
	view *retryApprovalView
}

func (r *retryApprovalProjects) Get(ctx context.Context, id string) (*model.Project, error) {
	var v model.Project
	err := r.view.query(ctx).Select("project_id", "org_id", "is_enabled", "status").Where("project_id = ?", id).First(&v).Error
	return &v, err
}

type retryApprovalPipelines struct {
	IPipelineRepository
	view *retryApprovalView
}

func (r *retryApprovalPipelines) Get(ctx context.Context, id string) (*model.Pipeline, error) {
	var v model.Pipeline
	err := r.view.query(ctx).Select("pipeline_id", "project_id", "is_enabled", "is_deleted").Where("pipeline_id = ? AND is_deleted = ?", id, 0).First(&v).Error
	return &v, err
}

type retryApprovalMembers struct {
	ITeamMemberRepository
	view *retryApprovalView
}

func (r *retryApprovalMembers) ListUserTeams(ctx context.Context, id string) ([]model.TeamMember, error) {
	var v []model.TeamMember
	err := r.view.query(ctx).Select("team_id", "user_id").Where("user_id = ?", id).Order("team_id ASC").Find(&v).Error
	return v, err
}

type retryApprovalTeams struct {
	ITeamRepository
	view *retryApprovalView
}

func (r *retryApprovalTeams) BatchGet(ctx context.Context, ids []string) ([]*model.Team, error) {
	var v []*model.Team
	err := r.view.query(ctx).Select("team_id", "is_enabled").Where("team_id IN ?", ids).Order("team_id ASC").Find(&v).Error
	return v, err
}

type retryApprovalPermissions struct {
	IPermissionRepository
	view *retryApprovalView
}

func (r *retryApprovalPermissions) ListPermissions(ctx context.Context) ([]model.Permission, error) {
	var v []model.Permission
	err := r.view.query(ctx).Select("permission_id", "resource_type", "action", "scope_type").Order("permission_id ASC").Find(&v).Error
	return v, err
}
func (r *retryApprovalPermissions) ListAllRoleBindings(ctx context.Context) ([]model.RolePermissionBinding, error) {
	var v []model.RolePermissionBinding
	err := r.view.query(ctx).Select("role_id", "permission_id").Order("role_id ASC, permission_id ASC").Find(&v).Error
	return v, err
}

type retryApprovalGrants struct {
	IRoleGrantRepository
	view *retryApprovalView
}

func (r *retryApprovalGrants) ListAllEnabled(ctx context.Context) ([]model.RoleGrant, error) {
	var v []model.RoleGrant
	err := r.view.query(ctx).Select("grant_id", "subject_type", "subject_id", "role_id", "scope_type", "scope_id", "expires_at", "is_enabled").Where("is_enabled = ?", 1).Order("grant_id ASC").Find(&v).Error
	now := time.Now()
	for _, g := range v {
		if g.ExpiresAt != nil && g.ExpiresAt.After(now) && (r.view.expires.IsZero() || g.ExpiresAt.Before(r.view.expires)) {
			r.view.expires = *g.ExpiresAt
		}
	}
	return v, err
}

type retryApprovalRoles struct {
	IRoleRepository
	view *retryApprovalView
}

func (r *retryApprovalRoles) BatchGet(ctx context.Context, ids []string) ([]model.Role, error) {
	var v []model.Role
	err := r.view.query(ctx).Select("role_id", "is_enabled").Where("role_id IN ?", ids).Order("role_id ASC").Find(&v).Error
	return v, err
}

func retryApprovalContext(ctx context.Context, tx *gorm.DB, job *model.JobRun) (*retryapproval.RunContext, error) {
	return retryApprovalContextRead(ctx, tx, job, true)
}
func retryApprovalContextRead(ctx context.Context, tx *gorm.DB, job *model.JobRun, lockRun bool) (*retryapproval.RunContext, error) {
	var plan *taskqueue.JobRetryPlan
	if job.RetryPlan != "" || job.RetryPlanDigest != "" {
		var err error
		plan, err = retryPlanForJob(job)
		if err != nil {
			return nil, err
		}
		if plan.RetryApprovalRequired != job.RetryApprovalRequired {
			return nil, fmt.Errorf("retry plan approval requirement changed")
		}
	} else if !emptyRetryApproval(job) {
		return nil, fmt.Errorf("required retry approval plan is missing")
	}
	var run model.PipelineRun
	query := tx.WithContext(ctx)
	if lockRun {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.Select("run_id", "pipeline_id", "triggered_by", "definition_commit_sha", "definition_path", "retry_approval_context", "retry_approval_context_digest").Where("run_id = ?", job.PipelineRunID).First(&run).Error; err != nil {
		return nil, err
	}
	if run.RunID != job.PipelineRunID || run.PipelineID != job.PipelineID {
		return nil, fmt.Errorf("retry approval run identity changed")
	}
	if run.RetryApprovalContext == "" && run.RetryApprovalContextDigest == "" {
		if job.RetryApprovalRequired || job.RetryApprovalID != "" || job.RetryApprovalEvidenceHash != "" {
			return nil, fmt.Errorf("required approval context is missing")
		}
		return nil, nil
	}
	c, err := retryapproval.ParseRunContext(run.RetryApprovalContext)
	if err != nil {
		return nil, err
	}
	digest, err := c.Digest()
	if err != nil || digest != run.RetryApprovalContextDigest || c.PipelineID != run.PipelineID || c.PipelineRunID != run.RunID || c.ActorID != run.TriggeredBy || c.DefinitionCommitSHA != run.DefinitionCommitSha || c.DefinitionPath != run.DefinitionPath {
		return nil, fmt.Errorf("trusted approval context changed")
	}
	if c.RequiresJob(job.JobName) != job.RetryApprovalRequired {
		return nil, fmt.Errorf("required retry approval marker changed")
	}
	if job.RetryApprovalRequired && (plan == nil || plan.RetryApprovalTimeoutSeconds != c.Policy.TimeoutSeconds) {
		return nil, fmt.Errorf("retry plan approval budget changed")
	}
	if !job.RetryApprovalRequired {
		if job.RetryApprovalID != "" || job.RetryApprovalEvidenceHash != "" {
			return nil, fmt.Errorf("unexpected retry approval binding")
		}
		return nil, nil
	}
	return c, nil
}
func retryApprovalRequired(ctx context.Context, tx *gorm.DB, job *model.JobRun) (bool, error) {
	c, err := retryApprovalContext(ctx, tx, job)
	return c != nil, err
}

func validateRetryApprovalBinding(ctx context.Context, tx *gorm.DB, job *model.JobRun) (*model.ApprovalRequest, *retryapproval.Evidence, error) {
	return validateRetryApprovalBindingRead(ctx, tx, job, true)
}

// lockRun=false 仅用于不可执行的证据查看或已锁定终态投票回执
func validateRetryApprovalBindingRead(ctx context.Context, tx *gorm.DB, job *model.JobRun, lockRun bool) (*model.ApprovalRequest, *retryapproval.Evidence, error) {
	c, err := retryApprovalContextRead(ctx, tx, job, lockRun)
	if err != nil {
		return nil, nil, err
	}
	if c == nil {
		return nil, nil, nil
	}
	if job.RetryApprovalID == "" || job.RetryApprovalEvidenceHash == "" {
		return nil, nil, fmt.Errorf("retry approval binding is missing")
	}
	a, err := lockApproval(tx.WithContext(ctx), job.RetryApprovalID)
	if err != nil {
		return nil, nil, err
	}
	if a.Version < 1 || (a.BindingKind != retryapproval.Kind && a.BindingKind != retryapproval.FailedStepKind) || a.RetryJobRunID == nil || *a.RetryJobRunID != job.JobRunID || a.DeploymentReleaseID != nil || a.ApprovalID != job.RetryApprovalID || a.EvidenceHash != job.RetryApprovalEvidenceHash || a.PipelineRunID != job.PipelineRunID || a.JobName != job.JobName || a.RequestedBy != c.ActorID || a.Plugin != retryapproval.Plugin || a.ExpiresAt == nil || a.StepName != "" || a.Environment != "" || a.CallbackURL != "" {
		return nil, nil, fmt.Errorf("retry approval backlinks changed")
	}
	e, err := retryapproval.ParseEvidence(a.EvidenceJSON)
	if err != nil {
		return nil, nil, err
	}
	if e.Kind != a.BindingKind || retryapproval.ValidateKindPlan(a.BindingKind, &e.RetryPlan) != nil {
		return nil, nil, fmt.Errorf("retry approval kind and plan changed")
	}
	hash, err := e.Digest()
	if err != nil || hash != a.EvidenceHash || e.ApprovalID != a.ApprovalID || e.Identity != generationIdentity(job) || e.JobName != job.JobName || e.RetryPlanDigest != job.RetryPlanDigest || e.InputManifestDigest != job.InputManifestDigest || e.ExpiresAtUnixMilli != a.ExpiresAt.UnixMilli() {
		return nil, nil, fmt.Errorf("retry approval evidence changed")
	}
	cc, _ := c.Canonical()
	ec, _ := e.Context.Canonical()
	plan, err := e.RetryPlan.Canonical()
	if err != nil || cc != ec || plan != job.RetryPlan {
		return nil, nil, fmt.Errorf("retry approval immutable plan changed")
	}
	candidates, err := model.ApprovalCandidates(a)
	if err != nil || !slices.Equal(candidates, c.Policy.Candidates) || a.Mode != c.Policy.Mode || a.RequiredApproverCount != c.Policy.RequiredApproverCount || a.PolicyID != c.Policy.ID {
		return nil, nil, fmt.Errorf("retry approval policy changed")
	}
	if err := validateGenerationInputs(job, e.InputManifestDigest, false); err != nil {
		return nil, nil, err
	}
	inputs, err := e.Inputs.Canonical()
	if err != nil || inputs != job.InputManifest {
		return nil, nil, fmt.Errorf("retry approval pinned inputs changed")
	}
	if a.CreatedAt.UnixMilli() != e.CreatedAtUnixMilli || e.Runtime.Type != job.RuntimeType || e.Runtime.Image != job.RuntimeImage || e.Runtime.Resources.CPU != job.RuntimeCPU || e.Runtime.Resources.Memory != job.RuntimeMemory {
		return nil, nil, fmt.Errorf("retry approval runtime or original creation changed")
	}
	if e.Source == nil {
		if job.SourceType != "" || job.SourceRepository != "" || job.SourceRef != "" {
			return nil, nil, fmt.Errorf("retry approval source absence changed")
		}
	} else if e.Source.Type != job.SourceType || e.Source.Repository != job.SourceRepository || e.Source.Ref != job.SourceRef {
		return nil, nil, fmt.Errorf("retry approval source binding changed")
	}
	if job.SourceResolvedCommit != "" && job.SourceResolvedCommit != e.Inputs.SourceCommit {
		return nil, nil, fmt.Errorf("retry approval source commit changed")
	}
	return a, e, nil
}

func validateRetryApprovalVotes(ctx context.Context, tx *gorm.DB, a *model.ApprovalRequest) ([]model.ApprovalDecision, error) {
	var votes []model.ApprovalDecision
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select(approvalDecisionColumns).Where("approval_id = ?", a.ApprovalID).Order("user_id ASC").Find(&votes).Error; err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	approved, rejected := 0, 0
	for _, v := range votes {
		if v.ApprovalID != a.ApprovalID || v.DecisionID == "" || seen[v.UserID] || !model.IsApprovalCandidate(a, v.UserID) {
			return nil, ErrApprovalInvalid
		}
		seen[v.UserID] = true
		switch v.Decision {
		case model.ApprovalDecisionApprove:
			approved++
		case model.ApprovalDecisionReject:
			rejected++
		default:
			return nil, ErrApprovalInvalid
		}
	}
	if approved != a.ApprovedCount || rejected != a.RejectedCount {
		return nil, ErrApprovalInvalid
	}
	if a.Status == model.ApprovalStatusApproved && (rejected != 0 || approved < a.RequiredApproverCount) {
		return nil, ErrApprovalInvalid
	}
	if a.Status == model.ApprovalStatusPending && (rejected != 0 || approved >= a.RequiredApproverCount) {
		return nil, ErrApprovalInvalid
	}
	if a.Status == model.ApprovalStatusRejected && rejected < 1 {
		return nil, ErrApprovalInvalid
	}
	return votes, nil
}
func validateRetryApprovalAuthority(ctx context.Context, tx *gorm.DB, job *model.JobRun, a *model.ApprovalRequest, e *retryapproval.Evidence, actor, action string) error {
	authorize, err := retryApprovalAuthorizer(tx)
	if err != nil {
		return err
	}
	votes, err := validateRetryApprovalVotes(ctx, tx, a)
	if err != nil {
		return err
	}
	actors := []string{e.Context.ActorID}
	for _, vote := range votes {
		if vote.Decision == model.ApprovalDecisionApprove {
			actors = append(actors, vote.UserID)
		}
	}
	if actor != "" {
		actors = append(actors, actor)
	}
	view := &retryApprovalView{tx: tx}
	if err := view.lockSubjects(ctx, e, actors); err != nil {
		return err
	}
	if err := authorize(ctx, view, e, e.Context.ActorID, "requester"); err != nil {
		return err
	}
	for _, v := range votes {
		if v.Decision == model.ApprovalDecisionApprove {
			if err := authorize(ctx, view, e, v.UserID, "approval:approve"); err != nil {
				return err
			}
		}
	}
	if actor != "" {
		if actor == e.Context.ActorID || !model.IsApprovalCandidate(a, actor) {
			return ErrApprovalForbidden
		}
		if err := authorize(ctx, view, e, actor, action); err != nil {
			return err
		}
	}
	if err := view.current(ctx); err != nil {
		return err
	}
	rememberRetryApprovalAuthorizationExpiry(tx, view.expires)
	if err := validateGenerationLive(ctx, tx, job, generationIdentity(job)); err != nil {
		return err
	}
	if approvalDeadlinePassed(a) {
		return ErrApprovalExpired
	}
	return nil
}

func validateRetryApprovalExecution(ctx context.Context, tx *gorm.DB, job *model.JobRun, receipt bool) error {
	a, e, err := validateRetryApprovalBinding(ctx, tx, job)
	if err != nil {
		return err
	}
	if a == nil {
		return nil
	}
	if a.Status != model.ApprovalStatusApproved {
		return fmt.Errorf("retry approval is not approved")
	}
	if err := validateGenerationLive(ctx, tx, job, generationIdentity(job)); err != nil {
		return err
	}
	if err := validateRegisteredInputArtifacts(ctx, tx, job, &e.Inputs); err != nil {
		return err
	}
	if receipt {
		record, g, err := loadGeneration(ctx, tx, job.CurrentGenerationID)
		if err != nil {
			return err
		}
		if !generationMatchesJob(g, job) || g.Number != job.CurrentGeneration || (record.State != "active" && record.State != "preparing") {
			return fmt.Errorf("approval generation is not current")
		}
		if err := validateRetryApprovalReceipt(record, g, job, a); err != nil {
			return err
		}
	}
	return validateRetryApprovalAuthority(ctx, tx, job, a, e, "", "")
}
func validateRetryApprovalReceipt(record *model.JobRunGeneration, g *taskqueue.JobGeneration, job *model.JobRun, a *model.ApprovalRequest) error {
	receipt, err := retryapproval.ParseAdmission(record.RetryApprovalAdmission)
	if err != nil {
		return err
	}
	digest, err := receipt.Digest()
	if err != nil || digest != record.RetryApprovalAdmissionDigest || receipt.ApprovalID != a.ApprovalID || receipt.EvidenceHash != a.EvidenceHash || receipt.ExpiresAtUnixMilli != a.ExpiresAt.UnixMilli() || receipt.Request.Identity != generationIdentity(job) || receipt.Request.GenerationID != g.GenerationID || receipt.Request.GenerationBindingDigest != g.BindingDigest || receipt.Request.RetryPlanDigest != job.RetryPlanDigest || receipt.Request.InputManifestDigest != job.InputManifestDigest {
		return fmt.Errorf("retry approval execution receipt changed")
	}
	return nil
}

// ReconcileJobRetryApproval 区分只读观察与原工作进程的实时启动准入
// 历史准入回执保持不可变，每次 admit 仍必须重新复核当前权限和期限
func (r *JobRunRepo) ReconcileJobRetryApproval(ctx context.Context, request retryapproval.Request) (*retryapproval.Response, error) {
	if _, err := request.Canonical(); err != nil {
		return nil, err
	}
	var result *retryapproval.Response
	err := r.Database().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.JobRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select(jobSnapshotColumns).Where("job_run_id = ?", request.Identity.JobRunID).First(&job).Error; err != nil {
			return err
		}
		if generationIdentity(&job) != request.Identity || job.RetryPlanDigest != request.RetryPlanDigest || job.InputManifestDigest != request.InputManifestDigest || !job.SourceExpectationCaptured {
			return fmt.Errorf("retry approval request identity changed")
		}
		if _, err := retryPlanForJob(&job); err != nil {
			return err
		}
		if err := validateGenerationInputs(&job, request.InputManifestDigest, false); err != nil {
			return err
		}
		// 终态生产者的迟到审批 RPC 不能在持有 job 锁时等待共享 run
		// 此协议不把已有启动回执作为新的执行权，终态直接拒绝
		if model.IsJobRunTerminal(job.Status) {
			return fmt.Errorf("terminal job cannot request retry approval startup")
		}
		if _, err := retryApprovalRequired(ctx, tx, &job); err != nil {
			return err
		}
		record, g, err := loadGeneration(ctx, tx, request.GenerationID)
		if err != nil {
			return err
		}
		if !generationMatchesJob(g, &job) || g.BindingDigest != request.GenerationBindingDigest {
			return fmt.Errorf("retry approval generation binding changed")
		}
		a, e, err := validateRetryApprovalBinding(ctx, tx, &job)
		if err != nil {
			return err
		}
		result = &retryapproval.Response{Version: 1, State: "ungated"}
		if a == nil {
			return nil
		}
		states := map[int]string{model.ApprovalStatusPending: "pending", model.ApprovalStatusApproved: "approved", model.ApprovalStatusRejected: "rejected", model.ApprovalStatusExpired: "expired", model.ApprovalStatusCanceled: "canceled"}
		state, ok := states[a.Status]
		if !ok {
			return ErrApprovalInvalid
		}
		if approvalDeadlinePassed(a) && (a.Status == model.ApprovalStatusPending || a.Status == model.ApprovalStatusApproved) {
			state = "expired"
		}
		result = &retryapproval.Response{Version: 1, Required: true, State: state, ApprovalID: a.ApprovalID, EvidenceHash: a.EvidenceHash, ExpiresAtUnixMilli: a.ExpiresAt.UnixMilli()}
		if request.Operation == "observe" {
			if state == "pending" || state == "approved" {
				if err := validateGenerationLive(ctx, tx, &job, request.Identity); err != nil {
					return err
				}
				if err := validateRetryApprovalAuthority(ctx, tx, &job, a, e, "", ""); err != nil {
					return err
				}
			}
			return nil
		}
		if job.CurrentGenerationID != g.GenerationID || job.CurrentGeneration != g.Number || (record.State != "active" && record.State != "preparing") || time.Now().UnixMilli() < g.NotBeforeUnixMilli {
			return fmt.Errorf("retry approval startup requires current ready generation")
		}
		if state != "approved" {
			return fmt.Errorf("retry approval startup is not approved")
		}
		if err := validateGenerationLive(ctx, tx, &job, request.Identity); err != nil {
			return err
		}
		if err := validateRegisteredInputArtifacts(ctx, tx, &job, &e.Inputs); err != nil {
			return err
		}
		if err := validateRetryApprovalAuthority(ctx, tx, &job, a, e, "", ""); err != nil {
			return err
		}
		var receipt *retryapproval.Admission
		if record.RetryApprovalAdmission != "" || record.RetryApprovalAdmissionDigest != "" {
			if err := validateRetryApprovalReceipt(record, g, &job, a); err != nil {
				return err
			}
			receipt, err = retryapproval.ParseAdmission(record.RetryApprovalAdmission)
			if err != nil {
				return err
			}
			old, _ := receipt.Request.Canonical()
			now, _ := request.Canonical()
			if old != now {
				return fmt.Errorf("retry approval startup request changed")
			}
		} else {
			receipt = &retryapproval.Admission{Version: 1, Request: request, ApprovalID: a.ApprovalID, EvidenceHash: a.EvidenceHash, AdmittedAtUnixMilli: time.Now().UnixMilli(), ExpiresAtUnixMilli: a.ExpiresAt.UnixMilli()}
			body, err := receipt.Canonical()
			if err != nil {
				return err
			}
			digest, err := receipt.Digest()
			if err != nil {
				return err
			}
			if err := reportedStatusResult(tx.Model(&model.JobRunGeneration{}).Where("generation_id = ? AND state = ? AND retry_approval_admission = ? AND retry_approval_admission_digest = ?", g.GenerationID, record.State, "", "").Updates(map[string]any{"retry_approval_admission": body, "retry_approval_admission_digest": digest, "updated_at": time.Now()})); err != nil {
				return err
			}
		}
		if err := validateGenerationLive(ctx, tx, &job, request.Identity); err != nil {
			return err
		}
		if approvalDeadlinePassed(a) {
			return ErrApprovalExpired
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := checkRetryApprovalAuthorizationExpiry(ctx, tx); err != nil {
			return err
		}
		result.Admission = receipt
		result.ExecutionAllowed = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, err := result.Canonical(); err != nil {
		return nil, err
	}
	return result, nil
}

// cancelPendingRetryApproval 仅取消本作业的待审记录，损坏绑定也不能阻止安全停止
func cancelPendingRetryApproval(ctx context.Context, tx *gorm.DB, job *model.JobRun, reason string) error {
	if !job.RetryApprovalRequired && job.RetryApprovalID == "" && job.RetryApprovalEvidenceHash == "" {
		return nil
	}
	var rows []model.ApprovalRequest
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select(approvalColumns).Where("retry_job_run_id = ?", job.JobRunID).Find(&rows).Error; err != nil {
		return err
	}
	for i := range rows {
		a := &rows[i]
		if a.Status == model.ApprovalStatusPending {
			if err := patchPendingApproval(tx, a, map[string]any{"status": model.ApprovalStatusCanceled, "reason": reason}); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateRetryApprovalCommit 在最终写入后复核期限，失败会回滚整个状态事务
func validateRetryApprovalCommit(ctx context.Context, tx *gorm.DB, job *model.JobRun, next int) error {
	if next != model.JobRunStatusRunning && next != model.JobRunStatusSuccess {
		return nil
	}
	if job.RetryPlan == "" && !job.RetryApprovalRequired && job.RetryApprovalID == "" {
		return nil
	}
	// 作业首次 RUNNING 仅认领私有准备，不授予容器或步骤执行权
	if next == model.JobRunStatusRunning && job.CurrentGenerationID == "" {
		_, err := retryApprovalRequired(ctx, tx, job)
		return err
	}
	a, _, err := validateRetryApprovalBinding(ctx, tx, job)
	if err != nil {
		return err
	}
	if a == nil {
		return nil
	}
	if a.Status != model.ApprovalStatusApproved || approvalDeadlinePassed(a) {
		return ErrApprovalExpired
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if job.Timeout != "" {
		duration, err := time.ParseDuration(job.Timeout)
		if err != nil || duration <= 0 || job.StartTime == nil || !time.Now().Before(job.StartTime.Add(duration)) {
			return fmt.Errorf("original job deadline expired")
		}
	}
	return checkRetryApprovalAuthorizationExpiry(ctx, tx)
}

func lockRetryApprovalJob(ctx context.Context, tx *gorm.DB, initial *model.ApprovalRequest) (*model.JobRun, error) {
	if initial == nil || (initial.BindingKind != retryapproval.Kind && initial.BindingKind != retryapproval.FailedStepKind) || initial.RetryJobRunID == nil || *initial.RetryJobRunID == "" || initial.DeploymentReleaseID != nil {
		return nil, ErrApprovalInvalid
	}
	var job model.JobRun
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select(jobSnapshotColumns).Where("job_run_id = ?", *initial.RetryJobRunID).First(&job).Error; err != nil {
		return nil, err
	}
	if job.RetryApprovalID != initial.ApprovalID || job.RetryApprovalEvidenceHash != initial.EvidenceHash {
		return nil, ErrApprovalConflict
	}
	return &job, nil
}

// ReadRetryApproval 读取准确冻结证据，不返回执行授权或重新解析可变代码
func (r *ApprovalRepo) ReadRetryApproval(ctx context.Context, id string) (*retryapproval.Review, error) {
	initial, err := r.GetByApprovalID(ctx, id)
	if err != nil {
		return nil, err
	}
	var result *retryapproval.Review
	err = r.Database().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, err := lockRetryApprovalJob(ctx, tx, initial)
		if err != nil {
			return err
		}
		a, e, err := validateRetryApprovalBindingRead(ctx, tx, job, false)
		if err != nil {
			return err
		}
		if a == nil {
			return ErrApprovalInvalid
		}
		result = &retryapproval.Review{ApprovalID: a.ApprovalID, RetryJobRunID: job.JobRunID, EvidenceHash: a.EvidenceHash, Status: a.Status, Evidence: e}
		return nil
	})
	return result, err
}

func retryApprovalVoteReplay(ctx context.Context, tx *gorm.DB, a *model.ApprovalRequest, actor, decision string) (bool, error) {
	var old model.ApprovalDecision
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select(approvalDecisionColumns).Where("approval_id = ? AND user_id = ?", a.ApprovalID, actor).First(&old).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if old.Decision != decision {
		return false, ErrApprovalConflict
	}
	if !model.IsApprovalCandidate(a, actor) {
		return false, ErrApprovalForbidden
	}
	return true, nil
}
func validateRetryApprovalDecision(ctx context.Context, tx *gorm.DB, job *model.JobRun, a *model.ApprovalRequest, e *retryapproval.Evidence, actor, decision string) error {
	if decision != model.ApprovalDecisionApprove && decision != model.ApprovalDecisionReject {
		return ErrApprovalInvalid
	}
	if !model.IsApprovalCandidate(a, actor) {
		return ErrApprovalForbidden
	}
	if a.Status == model.ApprovalStatusExpired || approvalDeadlinePassed(a) {
		return ErrApprovalExpired
	}
	if a.Status != model.ApprovalStatusPending {
		return ErrApprovalConflict
	}
	if err := validateGenerationLive(ctx, tx, job, generationIdentity(job)); err != nil {
		return err
	}
	if err := validateRegisteredInputArtifacts(ctx, tx, job, &e.Inputs); err != nil {
		return err
	}
	return validateRetryApprovalAuthority(ctx, tx, job, a, e, actor, "approval:"+decision)
}

// ValidateRetryApprovalDecision 在同一事务当前读取身份和权限，最终决策仍会独立重新校验
func (r *ApprovalRepo) ValidateRetryApprovalDecision(ctx context.Context, id, actor, decision string) error {
	initial, err := r.GetByApprovalID(ctx, id)
	if err != nil {
		return err
	}
	return r.Database().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, err := lockRetryApprovalJob(ctx, tx, initial)
		if err != nil {
			return err
		}
		historical, handled, err := retryApprovalTerminalVote(ctx, tx, job, initial, actor, decision)
		if handled || err != nil {
			_ = historical
			return err
		}
		a, e, err := validateRetryApprovalBinding(ctx, tx, job)
		if err != nil {
			return err
		}
		if a == nil {
			return ErrApprovalInvalid
		}
		replay, err := retryApprovalVoteReplay(ctx, tx, a, actor, decision)
		if err != nil || replay {
			return err
		}
		return validateRetryApprovalDecision(ctx, tx, job, a, e, actor, decision)
	})
}

func (r *ApprovalRepo) decideRetryApproval(ctx context.Context, initial *model.ApprovalRequest, actor, decision, reason string) (*model.ApprovalRequest, error) {
	var result *model.ApprovalRequest
	err := r.Database().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, err := lockRetryApprovalJob(ctx, tx, initial)
		if err != nil {
			return err
		}
		historical, handled, err := retryApprovalTerminalVote(ctx, tx, job, initial, actor, decision)
		if handled || err != nil {
			result = historical
			return err
		}
		a, e, err := validateRetryApprovalBinding(ctx, tx, job)
		if err != nil {
			return err
		}
		if a == nil {
			return ErrApprovalInvalid
		}
		result = a
		replay, err := retryApprovalVoteReplay(ctx, tx, a, actor, decision)
		if err != nil || replay {
			return err
		}
		if err := validateRetryApprovalDecision(ctx, tx, job, a, e, actor, decision); err != nil {
			return err
		}
		vote := &model.ApprovalDecision{DecisionID: uuid.NewString(), ApprovalID: a.ApprovalID, UserID: actor, Decision: decision, Comment: reason}
		if err := tx.WithContext(ctx).Create(vote).Error; err != nil {
			return err
		}
		approved, rejected := a.ApprovedCount, a.RejectedCount
		if decision == model.ApprovalDecisionApprove {
			approved++
		} else {
			rejected++
		}
		next := model.ApprovalStatusPending
		if rejected > 0 {
			next = model.ApprovalStatusRejected
		} else if approved >= a.RequiredApproverCount {
			next = model.ApprovalStatusApproved
		}
		if approvalDeadlinePassed(a) {
			return ErrApprovalExpired
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := patchPendingApproval(tx, a, map[string]any{"status": next, "approved_count": approved, "rejected_count": rejected, "approved_by": actor, "reason": reason}); err != nil {
			return err
		}
		a.Status, a.ApprovedCount, a.RejectedCount, a.ApprovedBy, a.Reason = next, approved, rejected, actor, reason
		if _, err := validateRetryApprovalVotes(ctx, tx, a); err != nil {
			return err
		}
		if err := validateGenerationLive(ctx, tx, job, generationIdentity(job)); err != nil {
			return err
		}
		if approvalDeadlinePassed(a) {
			return ErrApprovalExpired
		}
		return checkRetryApprovalAuthorizationExpiry(ctx, tx)
	})
	if errors.Is(err, ErrApprovalExpired) {
		current, expireErr := r.Expire(ctx, initial.ApprovalID)
		if expireErr != nil {
			return nil, expireErr
		}
		return current, ErrApprovalExpired
	}
	return result, err
}

func rememberRetryApprovalAuthorizationExpiry(tx *gorm.DB, expiry time.Time) {
	if expiry.IsZero() {
		return
	}
	const key = "retryApprovalAuthorizationExpiry"
	if old, ok := tx.Statement.Settings.Load(key); ok {
		if t, valid := old.(time.Time); valid && t.Before(expiry) {
			return
		}
	}
	tx.Statement.Settings.Store(key, expiry)
}
func checkRetryApprovalAuthorizationExpiry(ctx context.Context, tx *gorm.DB) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if old, ok := tx.Statement.Settings.Load("retryApprovalAuthorizationExpiry"); ok {
		if t, valid := old.(time.Time); !valid || !time.Now().Before(t) {
			return fmt.Errorf("authorization expired during commit")
		}
	}
	return nil
}

// 先锁定持久化终态再选择历史分支，任何请求字段均不能将新决策降级为回执
func retryApprovalTerminalVote(ctx context.Context, tx *gorm.DB, job *model.JobRun, initial *model.ApprovalRequest, actor, decision string) (*model.ApprovalRequest, bool, error) {
	a, err := lockApproval(tx.WithContext(ctx), initial.ApprovalID)
	if err != nil {
		return nil, true, err
	}
	if a.Status == model.ApprovalStatusPending {
		return nil, false, nil
	}
	a, _, err = validateRetryApprovalBindingRead(ctx, tx, job, false)
	if err != nil {
		return nil, true, err
	}
	if a == nil {
		return nil, true, ErrApprovalInvalid
	}
	replay, err := retryApprovalVoteReplay(ctx, tx, a, actor, decision)
	if err != nil || replay {
		return a, true, err
	}
	if a.Status == model.ApprovalStatusExpired || approvalDeadlinePassed(a) {
		return a, true, ErrApprovalExpired
	}
	return a, true, ErrApprovalConflict
}
