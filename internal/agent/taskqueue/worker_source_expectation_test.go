// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type sourceExpectationClient struct {
	*immutableInputClient
	expectation  *taskpayload.SourceCheckoutExpectation
	alter        func(*agentv1.GetOrRecordJobInputsResponse)
	beforeRecord func()
}

func (c *sourceExpectationClient) GetOrRecordJobInputs(ctx context.Context, r *agentv1.GetOrRecordJobInputsRequest, opts ...grpc.CallOption) (*agentv1.GetOrRecordJobInputsResponse, error) {
	i := taskpayload.JobInputIdentity{PipelineID: r.PipelineId, PipelineRunID: r.PipelineRunId, JobRunID: r.JobRunId, AgentID: r.AgentId, ClaimID: r.OriginalClaimId, RuntimeBindingDigest: r.RuntimeBindingDigest, SourceBindingDigest: r.SourceBindingDigest}
	digest, err := c.expectation.Digest(i)
	if err != nil || digest != r.SourceExpectationDigest {
		return nil, fmt.Errorf("expectation mismatch")
	}
	if r.ManifestJson != "" {
		m, err := taskpayload.ParseJobInputManifest(r.ManifestJson)
		if err != nil {
			return nil, err
		}
		if err = c.expectation.ValidateObservation(m); err != nil {
			return nil, err
		}
		if c.beforeRecord != nil {
			c.beforeRecord()
		}
	}
	response, err := c.immutableInputClient.GetOrRecordJobInputs(ctx, r, opts...)
	if err != nil {
		return nil, err
	}
	response = proto.Clone(response).(*agentv1.GetOrRecordJobInputsResponse)
	response.SourceExpectationJson, _ = c.expectation.Canonical()
	response.SourceExpectationDigest = digest
	if c.alter != nil {
		c.alter(response)
	}
	return response, nil
}
func TestSourceExpectationAgentChecksOutOriginalBeforeFirstObservation(t *testing.T) {
	repository, first, _ := sourceTestRepository(t, "expected-source")
	ctx, conf, payload, base := inputAgentFixture(t, repository)
	payload.RetryApprovalRequired = true
	payload.SourceExpectation = &taskpayload.SourceCheckoutExpectation{Version: 1, ScheduleID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", SealID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", ExpectedSourceCommit: first}
	workspace := t.TempDir()
	client := &sourceExpectationClient{immutableInputClient: base, expectation: payload.SourceExpectation}
	client.beforeRecord = func() {
		if _, err := os.Stat(filepath.Join(workspace, ".git")); err != nil {
			t.Fatal("proposal preceded checkout", err)
		}
		if got := sourceTestGit(t, workspace, "rev-parse", "HEAD"); got != first {
			t.Fatalf("observed %s want %s", got, first)
		}
	}
	checkout, err := checkoutJobInputs(ctx, &agentgrpc.ClientWrapper{AgentClient: client}, conf, payload, workspace)
	if err != nil || checkout == nil || checkout.ResolvedCommit != first {
		t.Fatal(checkout, err)
	}
	if len(base.requests) != 2 || base.requests[0].ManifestJson != "" || base.requests[1].ManifestJson == "" || payload.Source.Commit != "" {
		t.Fatal("expectation was confused with observation")
	}
}
func TestSourceExpectationAgentRejectsPartialOrForeignResponsesBeforeCheckout(t *testing.T) {
	for name, alter := range map[string]func(*agentv1.GetOrRecordJobInputsResponse){"missing": func(r *agentv1.GetOrRecordJobInputsResponse) {
		r.SourceExpectationJson = ""
		r.SourceExpectationDigest = ""
	}, "missing_digest": func(r *agentv1.GetOrRecordJobInputsResponse) { r.SourceExpectationDigest = "" }, "wrong_identity": func(r *agentv1.GetOrRecordJobInputsResponse) {
		r.SourceExpectationDigest = "v1:" + strings.Repeat("f", 64)
	}, "wrong_seal": func(r *agentv1.GetOrRecordJobInputsResponse) {
		r.SourceExpectationJson = strings.ReplaceAll(r.SourceExpectationJson, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	}} {
		t.Run(name, func(t *testing.T) {
			repository, _, commit := sourceTestRepository(t, name)
			ctx, conf, payload, base := inputAgentFixture(t, repository)
			payload.RetryApprovalRequired = true
			payload.SourceExpectation = &taskpayload.SourceCheckoutExpectation{Version: 1, ScheduleID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", SealID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", ExpectedSourceCommit: commit}
			client := &sourceExpectationClient{immutableInputClient: base, expectation: payload.SourceExpectation, alter: alter}
			workspace := t.TempDir()
			if _, err := checkoutJobInputs(ctx, &agentgrpc.ClientWrapper{AgentClient: client}, conf, payload, workspace); err == nil {
				t.Fatal("accepted inconsistent response")
			}
			entries, _ := os.ReadDir(workspace)
			if len(entries) != 0 || len(base.requests) != 1 || base.pinned != nil {
				t.Fatal("invalid expectation performed checkout/observation")
			}
		})
	}
}
func TestSourceExpectationAgentUnavailableCommitNeverFallsBack(t *testing.T) {
	repository, _, _ := sourceTestRepository(t, "missing-commit")
	ctx, conf, payload, base := inputAgentFixture(t, repository)
	payload.RetryApprovalRequired = true
	payload.SourceExpectation = &taskpayload.SourceCheckoutExpectation{Version: 1, ScheduleID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", SealID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", ExpectedSourceCommit: strings.Repeat("f", 40)}
	client := &sourceExpectationClient{immutableInputClient: base, expectation: payload.SourceExpectation}
	if _, err := checkoutJobInputs(ctx, &agentgrpc.ClientWrapper{AgentClient: client}, conf, payload, t.TempDir()); err == nil {
		t.Fatal("missing commit accepted")
	}
	if len(base.requests) != 1 || base.pinned != nil {
		t.Fatal("failed checkout created observation")
	}
}

func TestSourceExpectationAgentRejectsOrdinaryUnexpectedExtension(t *testing.T) {
	e := &taskpayload.SourceCheckoutExpectation{Version: 1, ScheduleID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", SealID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", ExpectedSourceCommit: strings.Repeat("c", 40)}
	body, _ := e.Canonical()
	for _, r := range []*agentv1.GetOrRecordJobInputsResponse{{State: "unknown", SourceExpectationJson: body}, {State: "unknown", SourceExpectationDigest: "v1:" + strings.Repeat("a", 64)}, {State: "unknown", SourceExpectationJson: body, SourceExpectationDigest: "v1:" + strings.Repeat("a", 64)}} {
		if verifySourceExpectationResponse(r, taskpayload.JobInputIdentity{}, nil) == nil {
			t.Fatal("ordinary payload accepted source expectation")
		}
	}
}
func TestSourceExpectationAgentUnsupportedRPCDoesNotCheckout(t *testing.T) {
	repository, _, commit := sourceTestRepository(t, "unsupported-expected")
	ctx, conf, payload, base := inputAgentFixture(t, repository)
	payload.RetryApprovalRequired = true
	payload.SourceExpectation = &taskpayload.SourceCheckoutExpectation{Version: 1, ScheduleID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", SealID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", ExpectedSourceCommit: commit}
	base.failure = status.Error(codes.Unimplemented, "unsupported")
	workspace := t.TempDir()
	if _, err := checkoutJobInputs(ctx, &agentgrpc.ClientWrapper{AgentClient: base}, conf, payload, workspace); err == nil {
		t.Fatal("unsupported RPC accepted")
	}
	entries, _ := os.ReadDir(workspace)
	if len(entries) != 0 || len(base.requests) != 1 || base.pinned != nil {
		t.Fatal("unsupported RPC fell back to checkout")
	}
}
