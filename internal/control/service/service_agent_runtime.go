// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package service

import (
	"context"
	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
)

func agentRuntimeCapabilities(capabilities *agentv1.RuntimeCapabilities, os string) []byte {
	if capabilities == nil {
		return []byte(`{}`)
	}
	report := taskqueue.AgentRuntimeCapabilities{ExecutionMode: capabilities.ExecutionMode}
	switch capabilities.ExecutionMode {
	case taskqueue.ExecutionModeBaremetal:
	case taskqueue.RuntimeContainerdOfflineV1:
		report.ContainerdOfflineV1 = os == "linux" && capabilities.ContainerdOfflineV1
		report.ContainerdWholeJobRetryV1 = report.ContainerdOfflineV1 && capabilities.ContainerdWholeJobRetryV1
	case "":
		if capabilities.ContainerdOfflineV1 {
			report.ExecutionMode = "unknown"
		}
	default:
		report.ExecutionMode = "unknown"
	}
	encoded, _ := sonic.Marshal(report)
	return encoded
}

func validateJobRuntimeReport(job *model.JobRun, reported string) error {
	if job.RuntimeType == "" && job.RuntimeImage == "" && job.RuntimeCPU == "" && job.RuntimeMemory == "" && job.RuntimeBindingDigest == "" {
		if reported != "" {
			return status.Error(codes.FailedPrecondition, "runtime report does not match a baremetal job")
		}
		return nil
	}
	runtime := &taskqueue.RuntimeRequest{Type: job.RuntimeType, Image: job.RuntimeImage, Resources: taskqueue.RuntimeResources{CPU: job.RuntimeCPU, Memory: job.RuntimeMemory}}
	expected, err := taskqueue.RuntimeBindingDigest(job.PipelineID, job.PipelineRunID, job.JobRunID, job.AgentID, runtime)
	if err != nil || !job.SourceExpectationCaptured || reported == "" || reported != expected || reported != job.RuntimeBindingDigest {
		return status.Error(codes.FailedPrecondition, "runtime report does not match the immutable dispatched job")
	}
	return nil
}

// InspectJobExecution 仅向认证 Agent 返回其原运行时认领的权威状态，不触发执行
func (a *AgentServiceImpl) InspectJobExecution(ctx context.Context, req *agentv1.InspectJobExecutionRequest) (*agentv1.InspectJobExecutionResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "execution identity is required")
	}
	agentID, err := authenticatedAgent(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	if req.JobRunId == "" || req.OriginalClaimId == "" || strings.TrimSpace(req.JobRunId) != req.JobRunId || strings.TrimSpace(req.OriginalClaimId) != req.OriginalClaimId || len(req.OriginalClaimId) > 64 || req.RuntimeBindingDigest == "" {
		return nil, status.Error(codes.InvalidArgument, "exact original runtime execution identity is required")
	}
	if a.agentService == nil {
		return nil, status.Error(codes.Unavailable, "job repository unavailable")
	}
	reader, ok := a.agentService.jobRunRepo.(interface {
		InspectJobExecution(context.Context, string, string, string, string) (int, error)
	})
	if !ok {
		return nil, status.Error(codes.Unimplemented, "runtime execution inspection unavailable")
	}
	current, err := reader.InspectJobExecution(ctx, agentID, req.JobRunId, req.OriginalClaimId, req.RuntimeBindingDigest)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "original runtime execution cannot be confirmed")
	}
	return &agentv1.InspectJobExecutionResponse{Status: int32(current), Terminal: model.IsJobRunTerminal(current)}, nil
}

func (a *AgentServiceImpl) validateRuntimeStepReport(ctx context.Context, step *model.StepRun, claim string, next int) error {
	if step.JobRunID == "" || a.agentService.jobRunRepo == nil {
		return nil
	}
	parent, err := a.agentService.jobRunRepo.GetByJobRunID(ctx, step.JobRunID)
	if err != nil {
		return status.Error(codes.Internal, "load runtime parent failed")
	}
	if parent == nil || parent.RuntimeType == "" && parent.RuntimeImage == "" && parent.RuntimeCPU == "" && parent.RuntimeMemory == "" && parent.RuntimeBindingDigest == "" {
		return nil
	}
	guard, ok := a.agentService.stepRunRepo.(interface {
		ValidateRuntimeStepClaim(context.Context, *model.StepRun, string, int) error
	})
	if !ok {
		return status.Error(codes.Unimplemented, "runtime step claim validation unavailable")
	}
	if err := guard.ValidateRuntimeStepClaim(ctx, step, claim, next); err != nil {
		return status.Error(codes.FailedPrecondition, "runtime step requires the original active job execution")
	}
	return nil
}
