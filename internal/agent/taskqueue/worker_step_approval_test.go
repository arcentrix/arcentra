// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"context"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/grpc"
)

type fakeStepApprovalClient struct {
	*fakeStepRetryClient
	approval func(*retryapproval.Request) (*retryapproval.Response, error)
}

func (c *fakeStepApprovalClient) ReconcileJobRetryApproval(_ context.Context, r *agentv1.ReconcileJobRetryApprovalRequest, _ ...grpc.CallOption) (*agentv1.ReconcileJobRetryApprovalResponse, error) {
	request, err := retryapproval.ParseRequest(r.RequestJson)
	if err != nil {
		return nil, err
	}
	response, err := c.approval(request)
	if err != nil {
		return nil, err
	}
	encoded, err := response.Canonical()
	return &agentv1.ReconcileJobRetryApprovalResponse{ResponseJson: encoded}, err
}

func TestFailedStepApprovalWorkerFreshAuthority(t *testing.T) {
	for _, scenario := range []string{"approved", "pending", "revoke-after-failure", "revoke-after-CAS", "next-logical-step", "lost-CAS-ACK-then-revoke", "startup-admission-expires", "startup-approval-expires", "startup-budget-only"} {
		t.Run(scenario, func(t *testing.T) {
			steps := 2
			if scenario == "startup-budget-only" {
				steps = 1
			}
			conf, p, r, c := fakeStepRetryFixture(t, steps)
			p.RetryApprovalRequired = true
			p.RetryApprovalTimeoutSeconds = 60
			bindFakeGenerationPlan(t, p)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			ctx = withOfflineJobRuntime(ctx, r)
			ctx = context.WithValue(ctx, receiptJobClaimContextKey{}, receiptJobClaim{runID: p.JobRunID, claimID: "approval-worker-claim"})
			ctx = context.WithValue(ctx, runtimeBindingContextKey{}, p.Runtime.BindingDigest)
			ctx = withJobExecutionClaim(ctx, p.JobRunID)
			c.identity = generationInputIdentity(ctx, p)
			client, err := agentgrpc.NewGrpcClient(agentgrpc.ClientConf{ServerAddr: "127.0.0.1:1", TLS: agentgrpc.ClientTLSConfig{Enabled: true, ServerName: "localhost"}})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			revoked := false
			admissions := 0
			expiry := time.Now().Add(time.Minute).UnixMilli()
			var startupCtx context.Context
			if strings.HasPrefix(scenario, "startup-") {
				if scenario == "startup-approval-expires" || scenario == "startup-budget-only" {
					expiry = time.Now().Add(100 * time.Millisecond).UnixMilli()
				} else {
					c.mutateAdmission = func(a *taskpayload.StepAttemptAdmission) {
						a.ExpiresUnixMilli = time.Now().Add(100 * time.Millisecond).UnixMilli()
					}
				}
				r.beforeStart = func(ctx context.Context) error {
					startupCtx = ctx
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) > 200*time.Millisecond {
						t.Error("startup was not bounded by immutable start expiry")
					}
					if scenario == "startup-budget-only" {
						return nil
					}
					<-ctx.Done()
					return ctx.Err()
				}
			}
			ac := &fakeStepApprovalClient{fakeStepRetryClient: c}
			ac.approval = func(request *retryapproval.Request) (*retryapproval.Response, error) {
				response := &retryapproval.Response{Version: 1, Required: true, State: "approved", ApprovalID: "approval", EvidenceHash: "v1:" + strings.Repeat("b", 64), ExpiresAtUnixMilli: expiry}
				if scenario == "pending" {
					response.State = "pending"
				}
				if request.Operation == "admit" {
					admissions++
					if revoked {
						response.State = "rejected"
					} else {
						response.ExecutionAllowed = true
						response.Admission = &retryapproval.Admission{AdmittedAtUnixMilli: time.Now().UnixMilli(), Version: 1, Request: *request, ApprovalID: response.ApprovalID, EvidenceHash: response.EvidenceHash, ExpiresAtUnixMilli: expiry}
					}
				}
				return response, nil
			}
			client.AgentClient = ac
			r.executeStep = func(a taskpayload.StepAttemptAdmission, _ *executor.ExecutionRequest) (*executor.ExecutionResult, error) {
				if scenario == "startup-budget-only" {
					time.Sleep(time.Until(time.UnixMilli(expiry)) + 10*time.Millisecond)
					if startupCtx == nil || startupCtx.Err() != context.Canceled || ctx.Err() != nil {
						t.Error("startup bound leaked into command lifetime")
					}
					return fakeStepExit(0)
				}
				if a.Scope.StepIndex == 0 && a.Scope.Attempt == 1 {
					if scenario == "revoke-after-failure" {
						revoked = true
					}
					if scenario == "next-logical-step" {
						revoked = true
						return fakeStepExit(0)
					}
					return fakeStepExit(9)
				}
				return fakeStepExit(0)
			}
			c.afterCAS = func(a taskpayload.StepAttemptAdmission) {
				if (scenario == "revoke-after-CAS" || scenario == "lost-CAS-ACK-then-revoke") && a.Scope.Attempt == 2 {
					revoked = true
					if scenario == "lost-CAS-ACK-then-revoke" {
						c.denyExecution = true
					}
				}
			}
			if scenario == "lost-CAS-ACK-then-revoke" {
				c.lostCASACKs = 1
			}
			if scenario == "revoke-after-CAS" {
				c.delay = 20 * time.Millisecond
			}
			result := executeContainerFailedSteps(ctx, ctx, conf, client, executor.NewExecutorManager(), p, nil, taskpayload.ArtifactReference{}, time.Now())
			if scenario == "startup-budget-only" {
				if result.err != nil || len(r.session.stepCalls) != 1 {
					t.Fatalf("started command lost original budget: %v", result.err)
				}
				return
			}
			if strings.HasPrefix(scenario, "startup-") {
				if result.err == nil || r.session.started || len(r.session.running) != 0 || len(r.session.stepCalls) != 0 || startupCtx == nil {
					t.Fatalf("expired startup reached RUNNING/effect: %v", result.err)
				}
				return
			}
			if scenario == "approved" {
				if result.err != nil || len(r.session.stepCalls) != 3 || admissions != 6 {
					t.Fatalf("approved calls=%d admissions=%d err=%v", len(r.session.stepCalls), admissions, result.err)
				}
				return
			}
			if result.err == nil || result.uncertain {
				t.Fatalf("known denial result err=%v uncertain=%v", result.err, result.uncertain)
			}
			if scenario == "pending" {
				if len(r.session.intents) != 0 || r.session.started {
					t.Fatal("pending approval produced step intent/runtime effect")
				}
				return
			}
			if len(r.session.stepCalls) != 1 {
				t.Fatalf("revocation allowed effects: %d", len(r.session.stepCalls))
			}
			if scenario != "revoke-after-CAS" && scenario != "lost-CAS-ACK-then-revoke" && len(r.session.intents) != 1 {
				t.Fatal("fresh denial wrote successor intent")
			}
			if scenario == "lost-CAS-ACK-then-revoke" {
				if len(c.requests) != 3 || c.requests[1] != c.requests[2] || len(r.session.observed) != 1 {
					t.Fatal("lost CAS replay changed request or discarded historical admission")
				}
			}
			if scenario == "revoke-after-CAS" && len(r.session.admitted) != 2 {
				t.Fatal("known admission evidence lost")
			}
		})
	}
}

func TestFailedStepApprovalOrdinaryEntryStaysClosed(t *testing.T) {
	conf, p, r, c := fakeStepRetryFixture(t, 1)
	conf.Agent.Sandbox.Containerd.Policy.AllowFailedStepRetryApproval = true
	p.RetryApprovalRequired = true
	p.RetryApprovalTimeoutSeconds = 60
	bindFakeGenerationPlan(t, p)
	if err := runFakeStepRetry(context.Background(), conf, p, r, c); err == nil || r.allocations != 0 {
		t.Fatalf("closed approval gate allocated runtime: err=%v allocations=%d", err, r.allocations)
	}
}
