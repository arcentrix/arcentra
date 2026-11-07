// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"errors"
	"math"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/copilot"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
)

// CopilotRerunService 提供惰性整作业新执行接口，不启动调度器或安装路由
// It is deliberately separate from CopilotService's existing retry-vote API.
type CopilotRerunService struct {
	repos   *repo.Repositories
	review  repo.CopilotRerunReviewBuilder
	secrets *SecretService
}

// NewCopilotRerunService 固定受支持的启动配置，调用方必须另行显式安装路由
func NewCopilotRerunService(repositories *repo.Repositories, policy *retryapproval.Policy, runtime taskqueue.OfflineRuntimePolicy, secrets *SecretService) (*CopilotRerunService, error) {
	if repositories == nil {
		return nil, repo.ErrCopilotRerunUnavailable
	}
	review, err := newCopilotRerunReviewBuilder(policy, runtime, secrets)
	if err != nil {
		return nil, repo.ErrCopilotRerunUnavailable
	}
	key := &SecretService{encryptKey: slices.Clone(secrets.encryptKey)}
	return &CopilotRerunService{repos: repositories, review: review, secrets: key}, nil
}
func (s *CopilotRerunService) require(subject authz.Subject, project string) error {
	if s == nil || s.repos == nil || s.review == nil || s.secrets == nil {
		return repo.ErrCopilotRerunUnavailable
	}
	if subject.IsAgent || !rerunAPIID(subject.UserID) || !rerunAPIID(project) {
		return repo.ErrCopilotRerunForbidden
	}
	return nil
}
func rerunAPIID(id string) bool { return len(id) <= 64 && copilot.ValidID(id) }
func copilotRerunAuthorize(ctx context.Context, view *repo.Repositories, scope *repo.CopilotRerunScope, owner, action string) error {
	if scope == nil || scope.Project == nil || scope.Pipeline == nil || scope.Run == nil || scope.Job == nil || scope.Run.ProjectID != scope.Project.ProjectID || scope.Run.PipelineID != scope.Pipeline.PipelineID || scope.Job.PipelineRunID != scope.Run.RunID || scope.Job.PipelineID != scope.Pipeline.PipelineID {
		return repo.ErrCopilotRerunForbidden
	}
	if err := historyAuthorize(ctx, view, owner, scope.Project, scope.Pipeline.PipelineID); err != nil {
		return repo.ErrCopilotRerunForbidden
	}
	if err := authorizeRetryActor(ctx, view, owner, scope.Project.ProjectID, scope.Pipeline.PipelineID, ""); err != nil {
		return repo.ErrCopilotRerunForbidden
	}
	if action == "prepare" || action == "confirm" {
		if err := authorizeRetryActor(ctx, view, owner, scope.Project.ProjectID, scope.Pipeline.PipelineID, "approval:read"); err != nil {
			return repo.ErrCopilotRerunForbidden
		}
	}
	return nil
}

// Availability 检查当前项目读取权限后提供独立可用性信号
func (s *CopilotRerunService) Availability(ctx context.Context, subject authz.Subject, project string) (CopilotRerunAvailability, error) {
	if err := s.require(subject, project); err != nil {
		return CopilotRerunAvailability{}, err
	}
	if err := s.repos.CopilotRerunReadProject(ctx, subject.UserID, project, historyAuthorize); err != nil {
		return CopilotRerunAvailability{}, err
	}
	return CopilotRerunAvailability{Available: true}, nil
}

// Eligibility 只读审阅支持范围，不持久化或展示临时新执行身份
func (s *CopilotRerunService) Eligibility(ctx context.Context, subject authz.Subject, project, job string) (CopilotRerunEligibility, error) {
	out := CopilotRerunEligibility{Available: true, ReasonCode: "unsupported", ServerTime: time.Now().UTC()}
	if err := s.require(subject, project); err != nil {
		return CopilotRerunEligibility{}, err
	}
	if !rerunAPIID(job) {
		return CopilotRerunEligibility{}, repo.ErrCopilotRerunInvalid
	}
	err := s.repos.CopilotRerunPreview(ctx, subject.UserID, project, job, copilotRerunAuthorize, func(ctx context.Context, view *repo.Repositories, scope *repo.CopilotRerunScope, snapshot *model.CopilotDefinitionSnapshot, evidence *repo.CopilotRerunOriginalEvidence, ids *repo.CopilotRerunExecutionIdentities, _ *model.CopilotRerunIntent) error {
		reviewed, err := s.review(ctx, view, scope, subject.UserID, snapshot, evidence, ids)
		if err != nil {
			return err
		}
		safe, err := s.safeReview(ctx, view, scope, subject.UserID, snapshot, evidence, reviewed)
		if err != nil {
			out.ReasonCode = "review_unavailable"
			return repo.ErrCopilotRerunUnsupported
		}
		out.Review = safe
		return nil
	})
	if errors.Is(err, repo.ErrCopilotRerunUnsupported) {
		return out, nil
	}
	if err != nil {
		return CopilotRerunEligibility{}, err
	}
	out.Eligible = true
	out.ReasonCode = "eligible"
	out.ServerTime = time.Now().UTC()
	return out, nil
}

// Prepare 创建固定惰性意图，重复恢复身份不刷新期限或分配替换目标
func (s *CopilotRerunService) Prepare(ctx context.Context, subject authz.Subject, project string, in CopilotRerunPrepareRequest) (CopilotRerunIntent, error) {
	if err := s.require(subject, project); err != nil {
		return CopilotRerunIntent{}, err
	}
	if !rerunAPIID(in.RequestID) || len(in.RequestID) < 16 || !rerunAPIID(in.OriginalJobRunID) || !sourceprovenance.ValidBindingDigest(in.SnapshotDigest) {
		return CopilotRerunIntent{}, repo.ErrCopilotRerunInvalid
	}
	saved, err := s.repos.CopilotRerunLookupRequest(ctx, subject.UserID, project, in.RequestID, historyAuthorize)
	if err == nil {
		return s.prepareReplay(ctx, subject, project, in, saved)
	}
	if !errors.Is(err, repo.ErrCopilotRerunNotFound) {
		return CopilotRerunIntent{}, err
	}
	eligibility, err := s.Eligibility(ctx, subject, project, in.OriginalJobRunID)
	if err != nil {
		return CopilotRerunIntent{}, err
	}
	if !eligibility.Eligible || eligibility.Review == nil {
		return CopilotRerunIntent{}, repo.ErrCopilotRerunUnsupported
	}
	if eligibility.Review.SnapshotDigest != in.SnapshotDigest {
		return CopilotRerunIntent{}, repo.ErrCopilotRerunConflict
	}
	expires := time.Now().UTC().Add(repo.CopilotRerunIntentTTL - time.Second).Truncate(time.Millisecond)
	if eligibility.Review.SnapshotExpiresAt.Before(expires) {
		expires = eligibility.Review.SnapshotExpiresAt
	}
	saved, err = s.repos.CopilotRerunPrepare(ctx, repo.CopilotRerunPrepareInput{OwnerUserID: subject.UserID, ProjectID: project, OriginalJobRunID: in.OriginalJobRunID, RequestID: in.RequestID, SnapshotDigest: in.SnapshotDigest, ExpiresAt: expires}, copilotRerunAuthorize, s.protectedReview)
	if err != nil {
		// Reconcile an exact concurrent/lost reply once. Never retry mutation or
		// silently generate another RequestID after an ambiguous commit.
		recovered, lookupErr := s.repos.CopilotRerunLookupRequest(ctx, subject.UserID, project, in.RequestID, historyAuthorize)
		if lookupErr == nil {
			return s.prepareReplay(ctx, subject, project, in, recovered)
		}
		return CopilotRerunIntent{}, err
	}
	return s.Inspect(ctx, subject, project, saved.IntentID)
}
func (s *CopilotRerunService) prepareReplay(ctx context.Context, subject authz.Subject, project string, in CopilotRerunPrepareRequest, saved *model.CopilotRerunIntent) (CopilotRerunIntent, error) {
	if saved == nil || saved.OriginalJobRunID != in.OriginalJobRunID || saved.SnapshotDigest != in.SnapshotDigest {
		return CopilotRerunIntent{}, repo.ErrCopilotRerunConflict
	}
	return s.Inspect(ctx, subject, project, saved.IntentID)
}

// LookupRequest 通过当前登录者的固定请求身份恢复未知准备结果
func (s *CopilotRerunService) LookupRequest(ctx context.Context, subject authz.Subject, project, request string) (CopilotRerunIntent, error) {
	if err := s.require(subject, project); err != nil {
		return CopilotRerunIntent{}, err
	}
	if !rerunAPIID(request) || len(request) < 16 {
		return CopilotRerunIntent{}, repo.ErrCopilotRerunInvalid
	}
	saved, err := s.repos.CopilotRerunLookupRequest(ctx, subject.UserID, project, request, historyAuthorize)
	if err != nil {
		return CopilotRerunIntent{}, err
	}
	return s.Inspect(ctx, subject, project, saved.IntentID)
}

// Inspect 重新授权并返回持久回执，绝不执行准备、确认或调度
// Existing history access rules still deny archived/disabled scopes. Durable
// identities remain fixed; clients must retain recovery IDs without preparing
// a replacement while the outcome cannot be read under current authority.
func (s *CopilotRerunService) Inspect(ctx context.Context, subject authz.Subject, project, intent string) (CopilotRerunIntent, error) {
	if err := s.require(subject, project); err != nil {
		return CopilotRerunIntent{}, err
	}
	var safe *CopilotRerunReview
	reviewStatus := "unavailable"
	row, err := s.repos.CopilotRerunInspectView(ctx, subject.UserID, project, intent, copilotRerunAuthorize, func(ctx context.Context, view *repo.Repositories, scope *repo.CopilotRerunScope, snapshot *model.CopilotDefinitionSnapshot, evidence *repo.CopilotRerunOriginalEvidence, ids *repo.CopilotRerunExecutionIdentities, saved *model.CopilotRerunIntent) error {
		if !time.Now().Before(saved.ExpiresAt) || !time.Now().Before(snapshot.ExpiresAt) {
			reviewStatus = "expired"
			return nil
		}
		current, err := s.review(ctx, view, scope, subject.UserID, snapshot, evidence, ids)
		if err != nil {
			return nil
		}
		if current.RunContextDigest != saved.ReviewedRunContextDigest || current.AdmissionPolicyDigest != saved.ReviewedAdmissionPolicyDigest || current.RunContext != saved.ReviewedRunContext || current.ExpectedSourceCommit != saved.ExpectedSourceCommit {
			reviewStatus = "changed"
			return nil
		}
		safe, err = s.safeReview(ctx, view, scope, subject.UserID, snapshot, evidence, current)
		if err == nil {
			reviewStatus = "current"
		}
		return nil
	})
	if err != nil {
		return CopilotRerunIntent{}, err
	}
	now := time.Now().UTC()
	out := CopilotRerunIntent{IntentID: row.IntentID, RequestID: row.RequestID, ProjectID: row.ProjectID, PipelineID: row.PipelineID, OriginalRunID: row.OriginalRunID, OriginalJobRunID: row.OriginalJobRunID, NewRunID: row.NewRunID, NewJobRunID: row.NewJobRunID, SnapshotDigest: row.SnapshotDigest, IntentDigest: row.IntentDigest, ReviewedRunContextDigest: row.ReviewedRunContextDigest, Version: row.Version, State: row.State, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt, ConfirmedAt: row.ConfirmedAt, CancelledAt: row.CancelledAt, ServerTime: now, Review: safe, ReviewStatus: reviewStatus}
	if row.State == repo.CopilotRerunPrepared && !now.Before(row.ExpiresAt) {
		out.State = "expired"
		out.Review = nil
		out.ReviewStatus = "expired"
	}
	out.CanConfirm = out.State == repo.CopilotRerunPrepared && out.ReviewStatus == "current" && out.Review != nil
	out.CanCancelPrepared = out.State == repo.CopilotRerunPrepared
	return out, nil
}

// Confirm 只创建固定新目标，始终需要新的独立审批后才能执行用户命令
func (s *CopilotRerunService) Confirm(ctx context.Context, subject authz.Subject, project, intent string, in CopilotRerunConfirmRequest) (CopilotRerunIntent, error) {
	if err := s.require(subject, project); err != nil {
		return CopilotRerunIntent{}, err
	}
	if !in.AcknowledgeNewExecution || !rerunMutationValid(in.ExpectedVersion, in.IntentDigest, in.ReviewedRunContextDigest) {
		return CopilotRerunIntent{}, repo.ErrCopilotRerunInvalid
	}
	current, err := s.Inspect(ctx, subject, project, intent)
	if err != nil {
		return CopilotRerunIntent{}, err
	}
	// A previously committed exact confirmation is a receipt replay, never fresh authority.
	if current.State != repo.CopilotRerunConfirmed && !current.CanConfirm {
		if current.State == "expired" {
			return CopilotRerunIntent{}, repo.ErrCopilotRerunExpired
		}
		return CopilotRerunIntent{}, repo.ErrCopilotRerunConflict
	}
	_, err = s.repos.CopilotRerunConfirm(ctx, repo.CopilotRerunConfirmInput{OwnerUserID: subject.UserID, ProjectID: project, IntentID: intent, ExpectedVersion: in.ExpectedVersion, ExpectedIntentDigest: in.IntentDigest, ExpectedReviewedRunContextDigest: in.ReviewedRunContextDigest}, copilotRerunAuthorize, s.protectedReview)
	if err != nil {
		return CopilotRerunIntent{}, err
	}
	return s.Inspect(ctx, subject, project, intent)
}

// CancelPrepared 仅以精确版本取消尚未产生新执行的意图
func (s *CopilotRerunService) CancelPrepared(ctx context.Context, subject authz.Subject, project, intent string, in CopilotRerunCancelRequest) (CopilotRerunIntent, error) {
	if err := s.require(subject, project); err != nil {
		return CopilotRerunIntent{}, err
	}
	if !rerunMutationValid(in.ExpectedVersion, in.IntentDigest, in.ReviewedRunContextDigest) {
		return CopilotRerunIntent{}, repo.ErrCopilotRerunInvalid
	}
	current, err := s.Inspect(ctx, subject, project, intent)
	if err != nil {
		return CopilotRerunIntent{}, err
	}
	if current.State == "expired" {
		return CopilotRerunIntent{}, repo.ErrCopilotRerunExpired
	}
	if current.State == repo.CopilotRerunPrepared {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, current.ExpiresAt)
		defer cancel()
	}
	_, err = s.repos.CopilotRerunCancelPrepared(ctx, repo.CopilotRerunConfirmInput{OwnerUserID: subject.UserID, ProjectID: project, IntentID: intent, ExpectedVersion: in.ExpectedVersion, ExpectedIntentDigest: in.IntentDigest, ExpectedReviewedRunContextDigest: in.ReviewedRunContextDigest}, copilotRerunAuthorize)
	if err != nil {
		return CopilotRerunIntent{}, err
	}
	return s.Inspect(ctx, subject, project, intent)
}
func rerunMutationValid(version uint64, intent, review string) bool {
	return version > 0 && version < math.MaxInt64 && sourceprovenance.ValidBindingDigest(intent) && sourceprovenance.ValidBindingDigest(review)
}

func (s *CopilotRerunService) safeReview(ctx context.Context, view *repo.Repositories, scope *repo.CopilotRerunScope, owner string, snapshot *model.CopilotDefinitionSnapshot, evidence *repo.CopilotRerunOriginalEvidence, reviewed *repo.CopilotRerunReviewedTarget) (*CopilotRerunReview, error) {
	if err := authorizeRetryActor(ctx, view, owner, scope.Project.ProjectID, scope.Pipeline.PipelineID, "approval:read"); err != nil {
		return nil, repo.ErrCopilotRerunForbidden
	}
	selected, err := retryapproval.ParseRunContext(reviewed.RunContext)
	if err != nil {
		return nil, repo.ErrCopilotRerunUnsupported
	}
	if !safeRerunRepository(snapshot.DefinitionRepository) || !safeRerunSourceRepository(evidence.Source.Repository) {
		return nil, repo.ErrCopilotRerunUnsupported
	}
	out := &CopilotRerunReview{ProjectID: snapshot.ProjectID, PipelineID: snapshot.PipelineID, OriginalRunID: scope.Run.RunID, OriginalJobRunID: scope.Job.JobRunID, OriginalRunStatus: scope.Run.Status, OriginalJobStatus: scope.Job.Status, JobName: snapshot.JobName, RetryMode: "whole_job", SnapshotDigest: snapshot.BindingDigest, SnapshotExpiresAt: snapshot.ExpiresAt, Definition: CopilotRerunDefinitionReview{snapshot.DefinitionRepository, snapshot.DefinitionPath, snapshot.DefinitionRevision, snapshot.ResolvedSpecDigest}, Source: CopilotRerunSourceReview{evidence.Source.Repository, evidence.Source.Ref, evidence.SourceCommit}, Policy: CopilotRerunPolicyReview{selected.Policy.ID, selected.Policy.Fingerprint, selected.Policy.Mode, selected.Policy.RequiredApproverCount, slices.Clone(selected.Policy.Candidates), selected.Policy.TimeoutSeconds}, RetryBudget: CopilotRerunRetryBudget{reviewed.Layout.Retry.MaxAttempts, reviewed.Layout.Retry.Delay, slices.Clone(reviewed.Layout.Retry.AllowedExitCodes)}}
	steps, total, err := view.StepRun.List(ctx, repo.StepRunFilter{PipelineID: snapshot.PipelineID, PipelineRunID: scope.Run.RunID, Page: 1, PageSize: 200})
	if err != nil || len(steps) > 200 || int64(len(steps)) != total {
		return nil, repo.ErrCopilotRerunUnsupported
	}
	pointers := []*model.StepRun{{Env: scope.Job.Env}}
	for i := range steps {
		if steps[i].PipelineID != snapshot.PipelineID || steps[i].PipelineRunID != scope.Run.RunID || steps[i].JobRunID != scope.Job.JobRunID {
			return nil, repo.ErrCopilotRerunUnsupported
		}
		pointers = append(pointers, &steps[i])
	}
	local := &CopilotService{repos: view, secrets: s.secrets, registry: copilot.NewRegistry(copilot.Config{})}
	redactor, err := local.redactor(ctx, scope.Project, scope.Run, pointers)
	if err != nil {
		return nil, repo.ErrCopilotRerunUnsupported
	}
	raw, err := sonic.Marshal(out)
	if err != nil {
		return nil, repo.ErrCopilotRerunUnsupported
	}
	var value any
	if sonic.Unmarshal(raw, &value) != nil || !rerunRedactionSafe(value, redactor) {
		return nil, repo.ErrCopilotRerunUnsupported
	}
	return out, nil
}
func safeRerunRepository(raw string) bool {
	if raw == "" || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "?#") || !rerunDisplayTextSafe(raw) {
		return false
	}
	// This is a disclosure guard, not checkout authorization. Transport/pinning
	// remain enforced by the existing prospective snapshot and native executor.
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw) == raw
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http" || u.Scheme == "git" || u.Scheme == "ssh") && u.Host != "" && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}

func rerunDisplayTextSafe(raw string) bool {
	return utf8.ValidString(raw) && strings.IndexFunc(raw, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}) < 0
}

// Source eligibility follows native anonymous checkout transports. Local host
// paths and SSH may be safe definition display identities but are not admitted
// as an offline execution source by this API.
func safeRerunSourceRepository(raw string) bool {
	if !safeRerunRepository(raw) || filepath.IsAbs(raw) {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "git")
}

func rerunRedactionSafe(v any, r *copilot.Redactor) bool {
	switch value := v.(type) {
	case string:
		return rerunDisplayTextSafe(value) && r.Redact(value) == value
	case []any:
		for _, item := range value {
			if !rerunRedactionSafe(item, r) {
				return false
			}
		}
	case map[string]any:
		for key, item := range value {
			if !rerunDisplayTextSafe(key) || r.Redact(key) != key || !rerunRedactionSafe(item, r) {
				return false
			}
		}
	}
	return true
}

// Every fresh mutation rechecks protected review availability inside the same
// authoritative transaction as candidate selection and intent creation/CAS.
func (s *CopilotRerunService) protectedReview(ctx context.Context, view *repo.Repositories, scope *repo.CopilotRerunScope, owner string, snapshot *model.CopilotDefinitionSnapshot, evidence *repo.CopilotRerunOriginalEvidence, ids *repo.CopilotRerunExecutionIdentities) (*repo.CopilotRerunReviewedTarget, error) {
	reviewed, err := s.review(ctx, view, scope, owner, snapshot, evidence, ids)
	if err != nil {
		return nil, err
	}
	metadata, err := repo.CopilotRerunReviewRepositories(view)
	if err != nil {
		return nil, err
	}
	if _, err := s.safeReview(ctx, metadata, scope, owner, snapshot, evidence, reviewed); err != nil {
		return nil, err
	}
	return reviewed, nil
}
