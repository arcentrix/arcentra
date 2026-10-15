// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"context"
	"errors"
	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
	"testing"
	"time"
)

type fakeRetryApprovalClient struct {
	*fakeGenerationClient
	observe      func(*retryapproval.Request) (*retryapproval.Response, error)
	calls        int
	requestBytes []string
}

func (c *fakeRetryApprovalClient) ReconcileJobRetryApproval(ctx context.Context, r *agentv1.ReconcileJobRetryApprovalRequest, _ ...grpc.CallOption) (*agentv1.ReconcileJobRetryApprovalResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	request, err := retryapproval.ParseRequest(r.RequestJson)
	if err != nil {
		return nil, err
	}
	c.calls++
	c.requestBytes = append(c.requestBytes, r.RequestJson)
	response, err := c.observe(request)
	if err != nil {
		return nil, err
	}
	encoded, err := response.Canonical()
	return &agentv1.ReconcileJobRetryApprovalResponse{ResponseJson: encoded}, err
}

func retryApprovalWorkerFixture(t *testing.T) (context.Context, *agentgrpc.ClientWrapper, *generationExecution, *fakeRetryApprovalClient, *fakeGenerationRuntime) {
	t.Helper()
	_, p, r, c := fakeGenerationFixture(t, 1)
	p.RetryApprovalRequired = true
	p.RetryApprovalTimeoutSeconds = 60
	bindFakeGenerationPlan(t, p)
	claim := "original-claim"
	ctx := context.WithValue(context.Background(), receiptJobClaimContextKey{}, receiptJobClaim{runID: p.JobRunID, claimID: claim})
	// 仅配置真实客户端的严格 TLS 路径；RPC 用纯内存桩，不连接任何服务器
	client, err := agentgrpc.NewGrpcClient(agentgrpc.ClientConf{ServerAddr: "127.0.0.1:1", TLS: agentgrpc.ClientTLSConfig{Enabled: true, ServerName: "localhost"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	fixture := &fakeRetryApprovalClient{fakeGenerationClient: c}
	client.AgentClient = fixture
	identity := generationInputIdentity(ctx, p)
	c.identity = identity
	manifest, err := taskqueue.NewJobInputManifest("", p.ArtifactURIs, p.ArtifactReferences)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := manifest.Digest(identity)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := manifest.Canonical()
	c.pinned = &agentv1.GetOrRecordJobInputsResponse{State: "pinned", ManifestJson: encoded, ManifestDigest: digest}
	g := &generationExecution{payload: p, generation: taskqueue.JobGeneration{GenerationID: "00000000-0000-4000-8000-000000000001", BindingDigest: "v1:" + strings.Repeat("a", 64)}, approvalWait: newRetryApprovalWait(ctx)}
	return context.WithValue(ctx, generationContextKey{}, g), client, g, fixture, r
}

func pendingApprovalResponse() *retryapproval.Response {
	return &retryapproval.Response{Version: 1, Required: true, State: "pending", ApprovalID: "approval", EvidenceHash: "v1:" + strings.Repeat("b", 64), ExpiresAtUnixMilli: time.Now().Add(time.Minute).UnixMilli()}
}

func TestRetryApprovalRefusesInsecureControllerBeforeAllocation(t *testing.T) {
	conf, p, r, c := fakeGenerationFixture(t, 1)
	conf.Agent.Sandbox.Containerd.Policy.AllowWholeJobRetryApproval = true
	p.RetryApprovalRequired = true
	p.RetryApprovalTimeoutSeconds = 60
	bindFakeGenerationPlan(t, p)
	err := runFakeGeneration(conf, p, r, c)
	if err == nil || r.allocations != 0 || len(r.sessions) != 0 {
		t.Fatalf("insecure controller reached runtime: %v allocations=%d", err, r.allocations)
	}
}

func TestRetryApprovalWaitNeverRenewsOriginalBudget(t *testing.T) {
	ctx, client, g, f, r := retryApprovalWorkerFixture(t)
	g.approvalWait.deadline = time.Now().Add(250 * time.Millisecond)
	f.observe = func(*retryapproval.Request) (*retryapproval.Response, error) {
		return nil, status.Error(codes.Unavailable, "offline")
	}
	start := time.Now()
	err := waitRetryApproval(ctx, client, g, r.drain)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second || f.calls == 0 {
		t.Fatalf("unbounded wait: %v", err)
	}
	if r.allocations != 0 || len(r.sessions) != 0 {
		t.Fatal("approval wait created runtime resources")
	}
}

func TestRetryApprovalPendingCancellationAndDenial(t *testing.T) {
	for _, name := range []string{"cancel", "reject", "drain"} {
		t.Run(name, func(t *testing.T) {
			ctx, client, g, f, r := retryApprovalWorkerFixture(t)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			fixed := pendingApprovalResponse()
			f.observe = func(*retryapproval.Request) (*retryapproval.Response, error) {
				switch name {
				case "cancel":
					cancel()
				case "reject":
					fixed.State = "rejected"
				case "drain":
					close(r.drain)
				}
				return fixed, nil
			}
			if err := waitRetryApproval(ctx, client, g, r.drain); err == nil {
				t.Fatal("pending or denied wait allowed execution")
			}
			if r.allocations != 0 || len(r.sessions) != 0 {
				t.Fatal("wait executed work")
			}
		})
	}
}

func TestRetryApprovalHistoricalReceiptCannotAuthorizeExecution(t *testing.T) {
	ctx, client, g, f, _ := retryApprovalWorkerFixture(t)
	response := pendingApprovalResponse()
	response.State = "approved"
	f.observe = func(request *retryapproval.Request) (*retryapproval.Response, error) {
		response.Admission = &retryapproval.Admission{Version: 1, Request: *request, ApprovalID: response.ApprovalID, EvidenceHash: response.EvidenceHash, AdmittedAtUnixMilli: time.Now().UnixMilli(), ExpiresAtUnixMilli: response.ExpiresAtUnixMilli}
		return response, nil
	}
	if err := admitRetryApproval(ctx, client); err == nil {
		t.Fatal("historical receipt authorized execution")
	}
	response.ExecutionAllowed = true
	if err := admitRetryApproval(ctx, client); err != nil {
		t.Fatal(err)
	}
	response.ExpiresAtUnixMilli++
	if err := admitRetryApproval(ctx, client); err == nil {
		t.Fatal("renewed deadline accepted")
	}
	_ = g
}

func TestRetryApprovalWholeWorkerPendingOwnsWorkspaceWithoutStarting(t *testing.T) {
	for _, outcome := range []string{"rejected", "cancelled", "expired", "unavailable"} {
		t.Run(outcome, func(t *testing.T) {
			conf, p, r, c := fakeGenerationFixture(t, 1)
			conf.Agent.Sandbox.Containerd.Policy.AllowWholeJobRetryApproval = true
			p.RetryApprovalRequired = true
			p.RetryApprovalTimeoutSeconds = 60
			if outcome == "unavailable" {
				p.RetryApprovalTimeoutSeconds = 1
			}
			bindFakeGenerationPlan(t, p)
			client, err := agentgrpc.NewGrpcClient(agentgrpc.ClientConf{ServerAddr: "127.0.0.1:1", TLS: agentgrpc.ClientTLSConfig{Enabled: true, ServerName: "localhost"}})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithCancel(withOfflineJobRuntime(context.Background(), r))
			defer cancel()
			f := &fakeRetryApprovalClient{fakeGenerationClient: c}
			client.AgentClient = f
			fixed := pendingApprovalResponse()
			if outcome == "expired" {
				fixed.ExpiresAtUnixMilli = time.Now().Add(30 * time.Millisecond).UnixMilli()
			}
			f.observe = func(request *retryapproval.Request) (*retryapproval.Response, error) {
				if request.Operation != "observe" {
					t.Fatal("pending review reached admission")
				}
				if r.allocations != 1 || len(r.sessions) != 1 || r.sessions[0].started || r.sessions[0].executions != 0 {
					t.Fatal("pending review executed or skipped preparation")
				}
				if outcome == "unavailable" {
					return nil, status.Error(codes.Unavailable, "controller unreachable")
				}
				if f.calls == 2 {
					if outcome == "rejected" {
						fixed.State = "rejected"
					}
					if outcome == "cancelled" {
						cancel()
					}
				}
				return fixed, nil
			}
			err = executeJobRun(ctx, conf, client, executor.NewExecutorManager(), p)
			if err == nil || f.calls == 0 {
				t.Fatalf("pending outcome succeeded: %v calls=%d", err, f.calls)
			}
			if len(r.sessions) != 1 || r.sessions[0].started || r.sessions[0].executions != 0 || !r.sessions[0].sealed || !r.sessions[0].closed {
				t.Fatalf("pending cleanup or no-execution invariant violated: %+v", r.sessions)
			}
		})
	}
}

func TestRetryApprovalAdmissionACKLossReplaysOnlyExactRequest(t *testing.T) {
	for _, outcome := range []string{"ack_loss", "revoked", "cancelled", "unavailable", "child_deadline"} {
		t.Run(outcome, func(t *testing.T) {
			conf, p, r, c := fakeGenerationFixture(t, 1)
			conf.Agent.Sandbox.Containerd.Policy.AllowWholeJobRetryApproval = true
			p.RetryApprovalRequired = true
			p.RetryApprovalTimeoutSeconds = 60
			bindFakeGenerationPlan(t, p)
			client, err := agentgrpc.NewGrpcClient(agentgrpc.ClientConf{ServerAddr: "127.0.0.1:1", TLS: agentgrpc.ClientTLSConfig{Enabled: true, ServerName: "localhost"}})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithCancel(withOfflineJobRuntime(context.Background(), r))
			defer cancel()
			f := &fakeRetryApprovalClient{fakeGenerationClient: c}
			client.AgentClient = f
			fixed := pendingApprovalResponse()
			fixed.State = "approved"
			var committed *retryapproval.Admission
			admits := 0
			f.observe = func(request *retryapproval.Request) (*retryapproval.Response, error) {
				if request.Operation == "observe" {
					response := *fixed
					response.Admission = nil
					response.ExecutionAllowed = false
					return &response, nil
				}
				admits++
				if len(r.sessions) != 1 || r.sessions[0].started || r.sessions[0].executions != 0 {
					t.Fatal("unconfirmed admission started execution")
				}
				if committed == nil {
					committed = &retryapproval.Admission{Version: 1, Request: *request, ApprovalID: fixed.ApprovalID, EvidenceHash: fixed.EvidenceHash, AdmittedAtUnixMilli: time.Now().UnixMilli(), ExpiresAtUnixMilli: fixed.ExpiresAtUnixMilli}
				}
				if committed.Request != *request {
					t.Fatal("ACK replay changed committed request")
				}
				if admits == 1 {
					if outcome == "cancelled" {
						time.AfterFunc(25*time.Millisecond, cancel)
					}
					if outcome == "child_deadline" {
						return nil, status.Error(codes.DeadlineExceeded, "committed reply lost")
					}
					return nil, status.Error(codes.Unavailable, "committed reply lost")
				}
				if outcome == "revoked" {
					return nil, status.Error(codes.PermissionDenied, "voter revoked after original admission")
				}
				if outcome == "unavailable" {
					return nil, status.Error(codes.Unavailable, "still unreachable")
				}
				response := *fixed
				response.Admission = committed
				response.ExecutionAllowed = true
				return &response, nil
			}
			err = executeJobRun(ctx, conf, client, executor.NewExecutorManager(), p)
			allowed := outcome == "ack_loss" || outcome == "child_deadline"
			if allowed {
				if err != nil || admits != 2 || len(r.sessions) != 1 || !r.sessions[0].started || r.sessions[0].starts != 1 || r.sessions[0].executions != 1 {
					t.Fatalf("exact ACK replay failed: %v admits=%d sessions=%+v", err, admits, r.sessions)
				}
			} else {
				expected := 2
				if outcome == "cancelled" {
					expected = 1
				}
				if outcome == "unavailable" {
					expected = 3
				}
				if err == nil || admits != expected || len(r.sessions) != 1 || r.sessions[0].started || r.sessions[0].executions != 0 || !r.sessions[0].closed {
					t.Fatalf("unsafe replay: %v admits=%d sessions=%+v", err, admits, r.sessions)
				}
			}
			var original string
			for _, raw := range f.requestBytes {
				req, e := retryapproval.ParseRequest(raw)
				if e != nil {
					t.Fatal(e)
				}
				if req.Operation != "admit" {
					continue
				}
				if original == "" {
					original = raw
				} else if raw != original {
					t.Fatal("canonical admit bytes changed between transmission attempts")
				}
			}
		})
	}
}
