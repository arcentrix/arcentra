// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ReconcileJobRetryApproval 仅为认证原 Agent 的固定代次观察或实时准入，不信任客户端审批结论
func (a *AgentServiceImpl) ReconcileJobRetryApproval(ctx context.Context, req *agentv1.ReconcileJobRetryApprovalRequest) (*agentv1.ReconcileJobRetryApprovalResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "retry approval request required")
	}
	agentID, err := authenticatedAgent(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	request, err := retryapproval.ParseRequest(req.RequestJson)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "canonical retry approval request required")
	}
	if request.Identity.AgentID != agentID {
		return nil, status.Error(codes.PermissionDenied, "approval belongs to another Agent")
	}
	claim, _, err := reportClaim(ctx, 3, 3, request.Identity.ClaimID)
	if err != nil {
		return nil, err
	}
	if claim != request.Identity.ClaimID {
		return nil, status.Error(codes.PermissionDenied, "original execution claim required")
	}
	if a.agentService == nil {
		return nil, status.Error(codes.Unavailable, "job repository unavailable")
	}
	store, ok := a.agentService.jobRunRepo.(interface {
		ReconcileJobRetryApproval(context.Context, retryapproval.Request) (*retryapproval.Response, error)
	})
	if !ok {
		return nil, status.Error(codes.Unimplemented, "retry approval unavailable")
	}
	result, err := store.ReconcileJobRetryApproval(ctx, *request)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "retry approval binding or current authority unavailable")
	}
	encoded, err := result.Canonical()
	if err != nil {
		return nil, status.Error(codes.Internal, "invalid retry approval response")
	}
	return &agentv1.ReconcileJobRetryApprovalResponse{ResponseJson: encoded}, nil
}
