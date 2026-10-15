// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package service

import (
	"context"
	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ReconcileStepAttempt 只允许认证原 Agent 以原作业认领提交有界不可变步骤转换
func (a *AgentServiceImpl) ReconcileStepAttempt(ctx context.Context, req *agentv1.ReconcileStepAttemptRequest) (*agentv1.ReconcileStepAttemptResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "step attempt transition is required")
	}
	agentID, err := authenticatedAgent(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	transition, err := taskqueue.ParseStepAttemptTransition(req.TransitionJson)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "canonical step attempt transition is required")
	}
	if transition.Scope.Identity.AgentID != agentID {
		return nil, status.Error(codes.PermissionDenied, "step attempt belongs to another agent")
	}
	claim, _, err := reportClaim(ctx, 3, 3, transition.Scope.Identity.ClaimID)
	if err != nil {
		return nil, err
	}
	if claim != transition.Scope.Identity.ClaimID {
		return nil, status.Error(codes.PermissionDenied, "original job claim is required")
	}
	if a.agentService == nil {
		return nil, status.Error(codes.Unavailable, "step attempt repository unavailable")
	}
	store, ok := a.agentService.jobRunRepo.(interface {
		ReconcileStepAttempt(context.Context, taskqueue.StepAttemptTransition) (*taskqueue.StepAttemptResult, error)
	})
	if !ok {
		return nil, status.Error(codes.Unimplemented, "step attempt persistence unavailable")
	}
	result, err := store.ReconcileStepAttempt(ctx, *transition)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "step identity, live authority or committed transition cannot be confirmed")
	}
	if result == nil || result.Admission == nil {
		return nil, status.Error(codes.Internal, "invalid step attempt result")
	}
	encoded, err := result.Admission.Canonical()
	if err != nil {
		return nil, status.Error(codes.Internal, "invalid step admission response")
	}
	return &agentv1.ReconcileStepAttemptResponse{AdmissionJson: encoded, ExecutionAllowed: result.ExecutionAllowed}, nil
}
