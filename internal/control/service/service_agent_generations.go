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

// ReconcileJobGeneration 只接受认证原 Agent 的有界不可变转换，不接受新策略或执行计划
func (a *AgentServiceImpl) ReconcileJobGeneration(ctx context.Context, req *agentv1.ReconcileJobGenerationRequest) (*agentv1.ReconcileJobGenerationResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "generation transition is required")
	}
	agentID, err := authenticatedAgent(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	transition, err := taskqueue.ParseJobGenerationTransition(req.TransitionJson)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "canonical generation transition is required")
	}
	if transition.Identity.AgentID != agentID {
		return nil, status.Error(codes.PermissionDenied, "generation belongs to another agent")
	}
	claim, _, err := reportClaim(ctx, 3, 3, transition.Identity.ClaimID)
	if err != nil {
		return nil, err
	}
	if claim != transition.Identity.ClaimID {
		return nil, status.Error(codes.PermissionDenied, "original generation claim required")
	}
	if a.agentService == nil {
		return nil, status.Error(codes.Unavailable, "generation repository unavailable")
	}
	store, ok := a.agentService.jobRunRepo.(interface {
		ReconcileJobGeneration(context.Context, taskqueue.JobGenerationTransition) (*taskqueue.JobGeneration, error)
	})
	if !ok {
		return nil, status.Error(codes.Unimplemented, "generation persistence unavailable")
	}
	generation, err := store.ReconcileJobGeneration(ctx, *transition)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "generation identity, live authority or committed transition cannot be confirmed")
	}
	encoded, err := generation.Canonical()
	if err != nil {
		return nil, status.Error(codes.Internal, "invalid generation response")
	}
	return &agentv1.ReconcileJobGenerationResponse{GenerationJson: encoded}, nil
}
