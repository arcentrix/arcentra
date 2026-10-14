// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"context"
	"fmt"
	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"testing"
	"time"
)

// denialGenerationClient 仅模拟通信与当前代次 CAS，实际数据库竞争由 repo/service 集成测试覆盖
type denialGenerationClient struct {
	*fakeRetryApprovalClient
	scenario                                         string
	inspected                                        bool
	inspectCalls, advanceCalls, skips, terminalCalls int
	originalAdvance                                  string
}

func (c *denialGenerationClient) ReconcileJobGeneration(ctx context.Context, req *agentv1.ReconcileJobGenerationRequest, opts ...grpc.CallOption) (*agentv1.ReconcileJobGenerationResponse, error) {
	tr, err := taskpayload.ParseJobGenerationTransition(req.TransitionJson)
	if err != nil {
		return nil, err
	}
	if tr.Operation == "admit" {
		return c.fakeGenerationClient.ReconcileJobGeneration(ctx, req, opts...)
	}
	if tr.Operation == "advance" {
		c.advanceCalls++
		if c.originalAdvance == "" {
			c.originalAdvance = req.TransitionJson
		} else if c.originalAdvance != req.TransitionJson {
			return nil, fmt.Errorf("advance bytes changed")
		}
		switch c.scenario {
		case "network":
			return nil, status.Error(codes.Unavailable, "ambiguous advance")
		case "committed_ack_loss":
			response, err := c.fakeGenerationClient.ReconcileJobGeneration(ctx, req, opts...)
			if err != nil {
				return nil, err
			}
			if c.advanceCalls == 1 {
				return nil, status.Error(codes.Unavailable, "committed advance ACK lost")
			}
			g, _ := taskpayload.ParseJobGeneration(response.GenerationJson)
			g.ExecutionAllowed = false
			body, _ := g.Canonical()
			return &agentv1.ReconcileJobGenerationResponse{GenerationJson: body}, nil
		default:
			return nil, status.Error(codes.FailedPrecondition, "new execution authority denied")
		}
	}
	if tr.Operation != "inspect" {
		return nil, fmt.Errorf("unexpected operation")
	}
	c.inspectCalls++
	if tr.Identity != c.identity || tr.RetryPlanDigest != c.payload.RetryPlanDigest || c.pinned == nil || tr.InputManifestDigest != c.pinned.ManifestDigest || tr.PreviousGenerationID != "" || tr.CleanupReceipt != nil {
		return nil, fmt.Errorf("inspect changed original binding")
	}
	if c.scenario == "inspect_network" {
		return nil, status.Error(codes.Unavailable, "inspection unavailable")
	}
	g := c.runtime.sessions[0].generation
	g.ExecutionAllowed = false
	g.PreparationOnly = false
	switch c.scenario {
	case "foreign_claim":
		g.OriginalClaimID = "foreign"
	case "successor":
		g.PreviousGenerationID = g.GenerationID
		g.GenerationID = id.GetUUID()
		g.Number++
		g.RuntimeClaimID = id.GetUUID()
		g.InputManifestDigest = c.pinned.ManifestDigest
	case "observation_write":
		c.runtime.admissionErr = fmt.Errorf("journal write failed")
	}
	g.BindingDigest, _ = g.Digest()
	encoded, err := g.Canonical()
	c.inspected = true
	return &agentv1.ReconcileJobGenerationResponse{GenerationJson: encoded}, err
}
func (c *denialGenerationClient) ReportStepRunStatus(ctx context.Context, req *agentv1.ReportStepRunStatusRequest, opts ...grpc.CallOption) (*agentv1.ReportStepRunStatusResponse, error) {
	if c.inspected && req.StepRunId != c.payload.Steps[0].StepRunID && req.Status >= 4 {
		c.skips++
		if c.scenario == "late_advance" || c.scenario == "late_advance_after_skip" && c.skips > 1 || c.scenario == "cancel_wins" {
			return nil, status.Error(codes.FailedPrecondition, "current generation or cancellation changed")
		}
	}
	return c.fakeGenerationClient.ReportStepRunStatus(ctx, req, opts...)
}
func (c *denialGenerationClient) ReportJobRunStatus(ctx context.Context, req *agentv1.ReportJobRunStatusRequest, opts ...grpc.CallOption) (*agentv1.ReportJobRunStatusResponse, error) {
	if req.Status >= 4 {
		c.terminalCalls++
		if c.scenario == "terminal_cas" {
			return nil, status.Error(codes.FailedPrecondition, "current generation changed")
		}
		if req.GenerationId != c.runtime.sessions[0].generation.GenerationID || req.GenerationBindingDigest != c.runtime.sessions[0].generation.BindingDigest {
			return nil, fmt.Errorf("terminal lost exact predecessor")
		}
	}
	return c.fakeGenerationClient.ReportJobRunStatus(ctx, req, opts...)
}
func TestRetryApprovalRejectedAdvanceConvergesOnlyConfirmedPredecessor(t *testing.T) {
	for _, scenario := range []string{"denied", "terminal_ack_loss", "network", "inspect_network", "foreign_claim", "successor", "committed_ack_loss", "observation_write", "late_advance", "late_advance_after_skip", "cancel_wins", "terminal_cas"} {
		t.Run(scenario, func(t *testing.T) {
			conf, p, r, base := fakeGenerationFixture(t, 1)
			conf.Agent.Sandbox.Containerd.Policy.AllowWholeJobRetryApproval = true
			p.RetryApprovalRequired = true
			p.RetryApprovalTimeoutSeconds = 60
			bindFakeGenerationPlan(t, p)
			r.execute = failFirstFakeGeneration
			if scenario == "terminal_ack_loss" {
				base.failJobTerminals = 1
			}
			client, err := agentgrpc.NewGrpcClient(agentgrpc.ClientConf{ServerAddr: "127.0.0.1:1", TLS: agentgrpc.ClientTLSConfig{Enabled: true, ServerName: "localhost"}})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			approval := pendingApprovalResponse()
			approval.State = "approved"
			gate := &fakeRetryApprovalClient{fakeGenerationClient: base}
			gate.observe = func(req *retryapproval.Request) (*retryapproval.Response, error) {
				response := *approval
				if req.Operation == "admit" {
					response.ExecutionAllowed = true
					response.Admission = &retryapproval.Admission{Version: 1, Request: *req, ApprovalID: response.ApprovalID, EvidenceHash: response.EvidenceHash, AdmittedAtUnixMilli: time.Now().UnixMilli(), ExpiresAtUnixMilli: response.ExpiresAtUnixMilli}
				}
				return &response, nil
			}
			transport := &denialGenerationClient{fakeRetryApprovalClient: gate, scenario: scenario}
			client.AgentClient = transport
			err = executeJobRun(withOfflineJobRuntime(context.Background(), r), conf, client, executor.NewExecutorManager(), p)
			if err == nil {
				t.Fatal("revoked retry succeeded")
			}
			if r.allocations != 1 || len(r.sessions) != 1 || r.sessions[0].executions != 1 || r.sessions[0].starts != 1 || !r.sessions[0].closed {
				t.Fatal("rejection changed cleanup or executed successor")
			}
			if len(r.intents) != 2 || r.intents[1].Operation != "advance" || r.intents[1].CleanupReceipt == nil {
				t.Fatal("original transition and cleanup evidence lost")
			}

			if scenario == "terminal_ack_loss" {
				if r.acknowledgements != 0 {
					t.Fatal("lost terminal ACK cleared journal")
				}
				beforeIntents, beforeInspects := len(r.intents), transport.inspectCalls
				_ = executeJobRun(withOfflineJobRuntime(context.Background(), r), conf, client, executor.NewExecutorManager(), p)
				if r.acknowledgements != 1 || r.allocations != 1 || r.sessions[0].starts != 1 || r.sessions[0].executions != 1 || len(r.intents) != beforeIntents || transport.inspectCalls != beforeInspects {
					t.Fatal("terminal ACK replay repeated transition or execution")
				}
				var first *agentv1.ReportJobRunStatusRequest
				for _, report := range base.jobReports {
					if report.Status >= 4 {
						if first == nil {
							first = report
						} else if !proto.Equal(first, report) {
							t.Fatal("terminal ACK replay changed immutable report")
						}
					}
				}
				return
			}
			if scenario == "denied" {
				if transport.inspectCalls != 1 || transport.terminalCalls != 1 || r.acknowledgements != 1 {
					t.Fatalf("confirmed denial did not finalize: inspect=%d terminal=%d ACK=%d err=%v", transport.inspectCalls, transport.terminalCalls, r.acknowledgements, err)
				}
				if got := sourceTestTerminal(t, base.artifactStatusClient); got.Status != 5 {
					t.Fatalf("denial reported %v", got.Status)
				}
				if len(r.admissions) != 2 || r.admissions[1].ExecutionAllowed {
					t.Fatal("read-only predecessor observation not retained")
				}
			} else {
				if r.acknowledgements != 0 {
					t.Fatal("unconfirmed convergence erased evidence")
				}
				if scenario == "network" || scenario == "committed_ack_loss" {
					if transport.inspectCalls != 0 {
						t.Fatal("ambiguous or committed result treated as denial")
					}
				}
				if scenario != "terminal_cas" && transport.terminalCalls != 0 {
					t.Fatal("changed or unconfirmed generation finalized")
				}
			}
		})
	}
}
