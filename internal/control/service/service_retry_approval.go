// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"fmt"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"slices"
)

// ConfigureRetryApprovalAuthorization 在启动时安装所有事务内执行与审批入口共用的实时授权器
func (s *Services) ConfigureRetryApprovalAuthorization() error {
	return repo.SetRetryApprovalAuthorizer(s.repos, func(ctx context.Context, view repo.RetryApprovalAuthorizationView, evidence *retryapproval.Evidence, actorID, action string) error {
		if evidence == nil || view == nil || s.retryApprovalPolicy == nil {
			return repo.ErrApprovalForbidden
		}
		current := s.retryApprovalPolicy
		frozen := evidence.Context.Policy
		if current.Fingerprint != frozen.Fingerprint || current.ID != frozen.ID || current.Mode != frozen.Mode || current.RequiredApproverCount != frozen.RequiredApproverCount || current.TimeoutSeconds != frozen.TimeoutSeconds {
			return repo.ErrApprovalForbidden
		}
		for _, candidate := range frozen.Candidates {
			if candidate == evidence.Context.ActorID || !slices.Contains(current.Candidates, candidate) {
				return repo.ErrApprovalForbidden
			}
		}
		approvalAction := action
		switch action {
		case "requester":
			if actorID != evidence.Context.ActorID {
				return repo.ErrApprovalForbidden
			}
			approvalAction = ""
		case "approval:approve", "approval:reject":
			if actorID == evidence.Context.ActorID || !slices.Contains(frozen.Candidates, actorID) {
				return repo.ErrApprovalForbidden
			}
		default:
			return repo.ErrApprovalForbidden
		}
		return authorizeRetryActor(ctx, view.LiveRepositories(), actorID, evidence.Context.ProjectID, evidence.Context.PipelineID, approvalAction)
	})
}

func (s *ApprovalService) validateRetryDecision(ctx context.Context, value *model.ApprovalRequest, actor, decision string) error {
	validator, ok := s.approvalRepo.(interface {
		ValidateRetryApprovalDecision(context.Context, string, string, string) error
	})
	if !ok {
		return fmt.Errorf("retry approval validator unavailable")
	}
	return validator.ValidateRetryApprovalDecision(ctx, value.ApprovalID, actor, decision)
}

// RetryReview 返回仓储验证的完整安全证据，绝不从可变定义重建
func (s *ApprovalService) RetryReview(ctx context.Context, value *model.ApprovalRequest) (*retryapproval.Review, error) {
	if value == nil {
		return nil, repo.ErrApprovalNotFound
	}
	if value.BindingKind == "" && value.RetryJobRunID == nil && value.Plugin != retryapproval.Plugin {
		return nil, nil
	}
	if value.BindingKind != retryapproval.Kind || value.RetryJobRunID == nil || value.DeploymentReleaseID != nil || value.Plugin != retryapproval.Plugin {
		return nil, repo.ErrApprovalInvalid
	}
	reader, ok := s.approvalRepo.(interface {
		ReadRetryApproval(context.Context, string) (*retryapproval.Review, error)
	})
	if !ok {
		return nil, fmt.Errorf("retry review unavailable")
	}
	return reader.ReadRetryApproval(ctx, value.ApprovalID)
}
