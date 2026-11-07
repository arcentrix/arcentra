// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"errors"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

// ProtectedCancellationNotification 仅由持久且已授权的取消回执构造
// The existing transport adapter must still verify the persisted exact tuple
// before enqueueing. This is not a user-constructible arbitrary Agent payload.
type ProtectedCancellationNotification struct {
	receipt repo.CopilotRerunTerminalReceipt
	actor   string
	locator repo.CopilotRerunCancellationLocator
}

// Receipt 返回固定回执副本，入队或终态确认均不等于物理清理证明
func (n ProtectedCancellationNotification) Receipt() repo.CopilotRerunTerminalReceipt {
	return n.receipt
}

// ProtectedCancellationNotifier 是显式注入的现有取消传输接口，不安装公共启动接线
type ProtectedCancellationNotifier interface {
	NotifyProtectedCancellation(context.Context, ProtectedCancellationNotification) error
}

// PipelineRunStopReceipt 区分控制器请求、Agent 终态确认和普通执行的请求
type PipelineRunStopReceipt struct {
	RunID                     string `json:"runId"`
	JobRunID                  string `json:"jobRunId,omitempty"`
	Protected                 bool   `json:"protected"`
	State                     string `json:"state"`
	ControllerRequested       bool   `json:"controllerRequested"`
	AgentTerminalAcknowledged bool   `json:"agentTerminalAcknowledged"`
	NotificationStatus        string `json:"notificationStatus"`
	ordinary                  *PipelineRunStopOrdinaryOutcome
}

// PipelineRunStopOrdinaryOutcome 仅在内部保留旧停止接口结果，不能序列化为新回执
type PipelineRunStopOrdinaryOutcome struct {
	Response    *pipelinev1.StopPipelineResponse `json:"-"`
	Err         error                            `json:"-"`
	BodyInvalid bool                             `json:"-"`
}

// OrdinaryOutcome 返回旧接口结果；HTTP 调用方必须保留原有 Operation 和错误映射
func (r PipelineRunStopReceipt) OrdinaryOutcome() *PipelineRunStopOrdinaryOutcome { return r.ordinary }

// PipelineStopBodyValidation 由路由解码器提供，不能来自请求主体的身份字段
// Typed internal callers omit this optional transport-only validation.
type PipelineStopBodyValidation struct{ OrdinaryValid, ProtectedValid bool }

type protectedCancellationRepository interface {
	LocateCopilotRerunCancellation(context.Context, string, string) (*repo.CopilotRerunCancellationLocator, error)
	CopilotRerunCancelUnclaimedAs(context.Context, string, repo.CopilotRerunTargetCancelInput, repo.CopilotRerunAuthorize) (*repo.CopilotRerunTargetCancelReceipt, error)
	CopilotRerunCancelClaimed(context.Context, string, taskqueue.JobInputIdentity, repo.CopilotRerunAuthorize) (*repo.CopilotRerunTerminalReceipt, error)
	InspectCopilotRerunCancellation(context.Context, string, *repo.CopilotRerunCancellationLocator, repo.CopilotRerunAuthorize) (*repo.CopilotRerunTerminalReceipt, error)
}

// ProtectedRunCancellationService 以当前取消者身份关闭固定目标，不改变原始所有者
type ProtectedRunCancellationService struct {
	repos    *repo.Repositories
	backend  protectedCancellationRepository
	notifier ProtectedCancellationNotifier
}

// NewProtectedRunCancellationService 构造惰性服务，缺少通知器不能宣称 Agent 已停止
func NewProtectedRunCancellationService(repositories *repo.Repositories, notifier ProtectedCancellationNotifier) (*ProtectedRunCancellationService, error) {
	if repositories == nil {
		return nil, repo.ErrCopilotRerunUnavailable
	}
	return &ProtectedRunCancellationService{repos: repositories, backend: repositories, notifier: notifier}, nil
}

// StopPipelineAs 从已认证路由接收真实主体，普通执行继续使用原有停止实现
// The existing public StopPipeline route is deliberately not switched here.
func (s *PipelineServiceImpl) StopPipelineAs(ctx context.Context, subject authz.Subject, req *pipelinev1.StopPipelineRequest, protected *ProtectedRunCancellationService, body ...PipelineStopBodyValidation) (PipelineRunStopReceipt, error) {
	if s == nil {
		return PipelineRunStopReceipt{}, repo.ErrCopilotRerunUnavailable
	}
	return protected.stop(ctx, subject, req, s.StopPipeline, body...)
}

func authorizePipelineCancellation(ctx context.Context, view *repo.Repositories, scope *repo.CopilotRerunScope, actor, action string) error {
	if action != "cancel" || scope == nil || scope.Project == nil || scope.Pipeline == nil || scope.Pipeline.ProjectID != scope.Project.ProjectID {
		return repo.ErrCopilotRerunForbidden
	}
	// Rebuild memberships from the current transaction; cached/request TeamIDs
	// and the intent owner's trigger authority are never cancellation authority.
	subject, err := pollingSubject(ctx, view, actor)
	if err != nil {
		return repo.ErrCopilotRerunForbidden
	}
	authorizer, err := authz.NewLiveAuthorizer(ctx, view)
	if err != nil {
		return repo.ErrCopilotRerunForbidden
	}
	allowed, err := authorizer.Check(ctx, subject, "pipeline:cancel", authz.ResourceRef{Type: "pipeline", ID: scope.Pipeline.PipelineID, ProjectID: scope.Project.ProjectID, OrgID: scope.Project.OrgID})
	if err != nil || !allowed {
		return repo.ErrCopilotRerunForbidden
	}
	return ctx.Err()
}

func (s *ProtectedRunCancellationService) stop(ctx context.Context, subject authz.Subject, req *pipelinev1.StopPipelineRequest, ordinary func(context.Context, *pipelinev1.StopPipelineRequest) (*pipelinev1.StopPipelineResponse, error), body ...PipelineStopBodyValidation) (PipelineRunStopReceipt, error) {
	if s == nil || s.repos == nil || s.backend == nil {
		return PipelineRunStopReceipt{}, repo.ErrCopilotRerunUnavailable
	}
	if subject.IsAgent || !rerunAPIID(subject.UserID) {
		return PipelineRunStopReceipt{}, repo.ErrCopilotRerunForbidden
	}
	if req == nil || !rerunAPIID(req.GetPipelineId()) || !rerunAPIID(req.GetRunId()) {
		return PipelineRunStopReceipt{}, repo.ErrCopilotRerunInvalid
	}
	validation := PipelineStopBodyValidation{OrdinaryValid: true, ProtectedValid: true}
	if len(body) > 1 {
		return PipelineRunStopReceipt{}, repo.ErrCopilotRerunInvalid
	}
	if len(body) == 1 {
		validation = body[0]
	}
	hint, err := s.backend.LocateCopilotRerunCancellation(ctx, req.GetPipelineId(), req.GetRunId())
	if errors.Is(err, repo.ErrCopilotRerunForbidden) {
		// Preserve the legacy missing/cross-pipeline 404 after current authority on
		// the requested pipeline. This branch never invokes the generic engine.
		view, authErr := s.authorizeOrdinary(ctx, subject.UserID, req.GetPipelineId())
		if authErr != nil {
			return PipelineRunStopReceipt{}, authErr
		}
		if !validation.OrdinaryValid {
			return ordinaryStopOutcome(nil, nil, true), nil
		}
		run, readErr := view.Pipeline.GetRun(ctx, req.GetRunId())
		if readErr != nil {
			return ordinaryStopOutcome(&pipelinev1.StopPipelineResponse{Success: false, Message: "run not found", Error: &pipelinev1.Error{Code: 404, Message: readErr.Error(), Type: "not_found"}}, nil, false), nil
		}
		if run == nil || run.PipelineID != req.GetPipelineId() {
			return ordinaryStopOutcome(&pipelinev1.StopPipelineResponse{Success: false, Message: "pipeline run not found", Error: &pipelinev1.Error{Code: 404, Message: "pipeline run not found", Type: "not_found"}}, nil, false), nil
		}
		return PipelineRunStopReceipt{}, repo.ErrCopilotRerunConflict
	}
	if err != nil {
		if errors.Is(err, repo.ErrCopilotRerunConflict) {
			return PipelineRunStopReceipt{}, repo.ErrCopilotRerunForbidden
		}
		return PipelineRunStopReceipt{}, repo.ErrCopilotRerunUnavailable
	}
	if hint == nil {
		return PipelineRunStopReceipt{}, repo.ErrCopilotRerunUnavailable
	}
	if !hint.Protected {
		if _, err := s.authorizeOrdinary(ctx, subject.UserID, req.GetPipelineId()); err != nil {
			return PipelineRunStopReceipt{}, err
		}
		if !validation.OrdinaryValid {
			return ordinaryStopOutcome(nil, nil, true), nil
		}
		if ordinary == nil {
			return PipelineRunStopReceipt{}, repo.ErrCopilotRerunUnavailable
		}
		response, err := ordinary(ctx, req)
		return ordinaryStopOutcome(response, err, false), nil
	}
	if !validation.ProtectedValid {
		if _, err := s.authorizeOrdinary(ctx, subject.UserID, req.GetPipelineId()); err != nil {
			return PipelineRunStopReceipt{}, err
		}
		return PipelineRunStopReceipt{}, repo.ErrCopilotRerunInvalid
	}
	return s.cancel(ctx, subject.UserID, hint)
}
func ordinaryStopOutcome(response *pipelinev1.StopPipelineResponse, err error, bodyInvalid bool) PipelineRunStopReceipt {
	return PipelineRunStopReceipt{ordinary: &PipelineRunStopOrdinaryOutcome{Response: response, Err: err, BodyInvalid: bodyInvalid}}
}
func (s *ProtectedRunCancellationService) authorizeOrdinary(ctx context.Context, actor, pipelineID string) (*repo.Repositories, error) {
	view, err := s.repos.CopilotPrimaryRepositories(ctx)
	if err != nil {
		return nil, repo.ErrCopilotRerunUnavailable
	}
	pipeline, err := view.Pipeline.Get(ctx, pipelineID)
	if err != nil || pipeline == nil || pipeline.PipelineID != pipelineID {
		return nil, repo.ErrCopilotRerunForbidden
	}
	project, err := view.Project.Get(ctx, pipeline.ProjectID)
	if err != nil || project == nil || authorizePipelineCancellation(ctx, view, &repo.CopilotRerunScope{Pipeline: pipeline, Project: project}, actor, "cancel") != nil {
		return nil, repo.ErrCopilotRerunForbidden
	}
	return view, nil
}

func (s *ProtectedRunCancellationService) cancel(ctx context.Context, actor string, hint *repo.CopilotRerunCancellationLocator) (PipelineRunStopReceipt, error) {
	var receipt *repo.CopilotRerunTerminalReceipt
	var err error
	if hint.Claimed {
		receipt, err = s.backend.CopilotRerunCancelClaimed(ctx, actor, hint.Identity, authorizePipelineCancellation)
	} else {
		var preclaim *repo.CopilotRerunTargetCancelReceipt
		preclaim, err = s.backend.CopilotRerunCancelUnclaimedAs(ctx, actor, hint.Unclaimed, authorizePipelineCancellation)
		if err == nil && preclaim != nil {
			receipt = &repo.CopilotRerunTerminalReceipt{IntentID: preclaim.IntentID, ScheduleID: preclaim.ScheduleID, RunID: preclaim.RunID, JobRunID: preclaim.JobRunID, RunStatus: model.PipelineStatusCancelled, JobStatus: model.JobRunStatusCancelled, CompletedAtUnixMilli: preclaim.CancelledAt.UnixMilli()}
		}
		if errors.Is(err, repo.ErrCopilotRerunConflict) {
			// A claim may have won. The Gate-first transaction has already ended.
			// Relocate once, read-only; no retry of the old CAS and no loop.
			current, lookupErr := s.backend.LocateCopilotRerunCancellation(ctx, hint.PipelineID, hint.RunID)
			if lookupErr == nil && sameCancellationTarget(hint, current) && current.Claimed {
				hint = current
				receipt, err = s.backend.CopilotRerunCancelClaimed(ctx, actor, hint.Identity, authorizePipelineCancellation)
			}
		}
	}
	if err != nil {
		if errors.Is(err, repo.ErrCopilotRerunForbidden) || errors.Is(err, repo.ErrCopilotRerunInvalid) {
			return PipelineRunStopReceipt{}, err
		}
		// An unknown commit can only become a success via an authorized read of
		// its fixed durable receipt. Never retry a mutation after an unknown ACK.
		recovered, readErr := s.backend.InspectCopilotRerunCancellation(ctx, actor, hint, authorizePipelineCancellation)
		if errors.Is(readErr, repo.ErrCopilotRerunForbidden) {
			return PipelineRunStopReceipt{}, readErr
		}
		if readErr != nil {
			return PipelineRunStopReceipt{}, err
		}
		receipt = recovered
	}
	if !cancellationReceiptMatches(hint, receipt) {
		return PipelineRunStopReceipt{}, repo.ErrCopilotRerunConflict
	}
	out := PipelineRunStopReceipt{RunID: receipt.RunID, JobRunID: receipt.JobRunID, Protected: true, State: "cancelled_before_claim", ControllerRequested: true, NotificationStatus: "not_applicable"}
	if !hint.Claimed {
		return out, nil
	}
	out.State = "cancellation_recorded"
	out.AgentTerminalAcknowledged = receipt.AgentTerminalAcknowledged
	out.NotificationStatus = "unavailable"
	if receipt.AgentTerminalAcknowledged {
		out.NotificationStatus = "not_needed"
		return out, nil
	}
	if s.notifier != nil {
		out.NotificationStatus = "unknown"
		if err := s.notifier.NotifyProtectedCancellation(ctx, ProtectedCancellationNotification{receipt: *receipt, actor: actor, locator: *hint}); err == nil {
			out.NotificationStatus = "enqueued"
		}
	}
	return out, nil
}

func sameCancellationTarget(a, b *repo.CopilotRerunCancellationLocator) bool {
	return a != nil && b != nil && a.Protected && b.Protected && a.PipelineID == b.PipelineID && a.RunID == b.RunID && a.JobRunID == b.JobRunID && a.IntentID == b.IntentID && a.ScheduleID == b.ScheduleID
}
func cancellationReceiptMatches(hint *repo.CopilotRerunCancellationLocator, receipt *repo.CopilotRerunTerminalReceipt) bool {
	if hint == nil || receipt == nil || receipt.IntentID != hint.IntentID || receipt.ScheduleID != hint.ScheduleID || receipt.RunID != hint.RunID || receipt.JobRunID != hint.JobRunID || receipt.RunStatus != model.PipelineStatusCancelled || receipt.JobStatus != model.JobRunStatusCancelled || receipt.CompletedAtUnixMilli <= 0 {
		return false
	}
	if hint.Claimed {
		return receipt.AgentID == hint.Identity.AgentID && receipt.ClaimID == hint.Identity.ClaimID
	}
	return receipt.AgentID == "" && receipt.ClaimID == "" && !receipt.AgentTerminalAcknowledged
}
