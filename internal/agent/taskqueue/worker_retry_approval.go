// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"context"
	"fmt"
	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"time"
)

type retryApprovalWait struct {
	deadline     time.Time
	approvalID   string
	evidenceHash string
	expiry       int64
}

func newRetryApprovalWait(ctx context.Context) *retryApprovalWait {
	deadline := time.Now().Add(time.Hour)
	if parent, ok := ctx.Deadline(); ok && parent.Before(deadline) {
		deadline = parent
	}
	return &retryApprovalWait{deadline: deadline}
}

func (w *retryApprovalWait) validate(response *retryapproval.Response) error {
	if response == nil || !response.Required {
		return fmt.Errorf("required retry approval was omitted")
	}
	if w.approvalID == "" {
		w.approvalID = response.ApprovalID
		w.evidenceHash = response.EvidenceHash
		w.expiry = response.ExpiresAtUnixMilli
		if expiry := time.UnixMilli(w.expiry); expiry.Before(w.deadline) {
			w.deadline = expiry
		}
	}
	if response.ApprovalID != w.approvalID || response.EvidenceHash != w.evidenceHash || response.ExpiresAtUnixMilli != w.expiry {
		return fmt.Errorf("retry approval identity or original deadline changed")
	}
	if !time.Now().Before(w.deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func callRetryApproval(ctx context.Context, client *agentgrpc.ClientWrapper, request *retryapproval.Request) (*retryapproval.Response, error) {
	if client == nil || client.AgentClient == nil || !client.ControllerAuthenticated() {
		return nil, fmt.Errorf("retry approval requires server-authenticated TLS")
	}
	encoded, err := request.Canonical()
	if err != nil {
		return nil, err
	}
	rpcCtx, cancel := client.WithTimeoutAndAuth(ctx)
	defer cancel()
	response, err := client.AgentClient.ReconcileJobRetryApproval(withExecutionClaim(rpcCtx, "job", request.Identity.JobRunID), &agentv1.ReconcileJobRetryApprovalRequest{AgentId: request.Identity.AgentID, RequestJson: encoded})
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, fmt.Errorf("missing retry approval response")
	}
	return retryapproval.ParseResponse(response.ResponseJson)
}

func retryApprovalRequest(ctx context.Context, client *agentgrpc.ClientWrapper, g *generationExecution, operation string) (*retryapproval.Request, error) {
	if g == nil || g.payload == nil || !g.payload.RetryApprovalRequired {
		return nil, fmt.Errorf("gated generation context required")
	}
	inputDigest, err := readGenerationInputDigest(ctx, client, g.payload)
	if err != nil {
		return nil, err
	}
	request := &retryapproval.Request{Version: 1, Operation: operation, Identity: generationInputIdentity(ctx, g.payload), GenerationID: g.generation.GenerationID, GenerationBindingDigest: g.generation.BindingDigest, RetryPlanDigest: g.payload.RetryPlanDigest, InputManifestDigest: inputDigest}
	_, err = request.Canonical()
	return request, err
}

// waitRetryApproval 只持有已分配工作目录，固定预算内观察审批，永不启动执行
func waitRetryApproval(ctx context.Context, client *agentgrpc.ClientWrapper, g *generationExecution, draining <-chan struct{}) error {
	if !g.payload.RetryApprovalRequired {
		return nil
	}
	if client == nil || !client.ControllerAuthenticated() {
		return fmt.Errorf("retry approval requires server-authenticated TLS")
	}
	if g.approvalWait == nil {
		return fmt.Errorf("fixed retry approval wait budget required")
	}
	bounded, cancel := context.WithDeadline(ctx, g.approvalWait.deadline)
	defer cancel()
	request, err := retryApprovalRequest(bounded, client, g, "observe")
	if err != nil {
		return err
	}
	for {
		if err := bounded.Err(); err != nil {
			return err
		}
		if !time.Now().Before(g.approvalWait.deadline) {
			return context.DeadlineExceeded
		}
		pollCtx, pollCancel := context.WithDeadline(bounded, g.approvalWait.deadline)
		response, err := callRetryApproval(pollCtx, client, request)
		pollCancel()
		if err == nil {
			if err = g.approvalWait.validate(response); err != nil {
				return err
			}
			if response.Admission != nil || response.ExecutionAllowed {
				return fmt.Errorf("observation unexpectedly grants execution")
			}
			switch response.State {
			case "approved":
				return nil
			case "pending":
			default:
				return fmt.Errorf("retry approval denied: %s", response.State)
			}
		} else if code := status.Code(err); code != codes.Unavailable && code != codes.DeadlineExceeded {
			return err
		}
		delay := 250 * time.Millisecond
		if remaining := time.Until(g.approvalWait.deadline); remaining < delay {
			delay = remaining
		}
		if delay <= 0 {
			return context.DeadlineExceeded
		}
		timer := time.NewTimer(delay)
		select {
		case <-bounded.Done():
			timer.Stop()
			return bounded.Err()
		case <-draining:
			timer.Stop()
			return fmt.Errorf("Agent draining during retry approval")
		case <-timer.C:
		}
	}
}

func admitRetryApproval(ctx context.Context, client *agentgrpc.ClientWrapper) error {
	g := generationFromContext(ctx)
	if g == nil || g.payload == nil || !g.payload.RetryApprovalRequired {
		return nil
	}
	if g.approvalWait == nil {
		return fmt.Errorf("retry approval context unavailable")
	}
	bounded, cancel := context.WithDeadline(ctx, g.approvalWait.deadline)
	defer cancel()
	request, err := retryApprovalRequest(bounded, client, g, "admit")
	if err != nil {
		return err
	}
	// 仅重放同一幂等准入请求的传输，绝不把历史回执视为新执行权限
	var response *retryapproval.Response
	for attempt := 0; attempt < 3; attempt++ {
		if err := bounded.Err(); err != nil {
			return err
		}
		response, err = callRetryApproval(bounded, client, request)
		if err == nil {
			break
		}
		if cause := bounded.Err(); cause != nil {
			return cause
		}
		code := status.Code(err)
		if attempt == 2 || (code != codes.Unavailable && code != codes.DeadlineExceeded) {
			return err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-bounded.Done():
			timer.Stop()
			return bounded.Err()
		case <-timer.C:
		}
	}
	if err := g.approvalWait.validate(response); err != nil {
		return err
	}
	if !response.ExecutionAllowed || response.State != "approved" || response.Admission == nil || response.Admission.Request != *request {
		return fmt.Errorf("fresh exact retry execution admission denied")
	}
	return bounded.Err()
}
