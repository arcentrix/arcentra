// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

// immutableInputFixtureClient 仅用于直接工作器单元夹具，真实控制面集成仍经过实际 RPC
// 派发身份和制品在构造时深拷贝，认领来自首次成功运行报告，已固定清单不可覆盖
type immutableInputFixtureClient struct {
	*artifactStatusClient
	mu          sync.Mutex
	identity    taskpayload.JobInputIdentity
	expected    string
	expectedErr error
	pinned      *agentv1.GetOrRecordJobInputsResponse
	inputCalls  int
}

func newImmutableInputFixtureClient(payload *taskpayload.JobRunTaskPayload, reports *artifactStatusClient) *immutableInputFixtureClient {
	c := &immutableInputFixtureClient{
		artifactStatusClient: reports,
		identity: taskpayload.JobInputIdentity{
			PipelineID: payload.PipelineID, PipelineRunID: payload.PipelineRunID,
			JobRunID: payload.JobRunID, AgentID: payload.AgentID,
			RuntimeBindingDigest: payload.Runtime.BindingDigest,
		},
	}
	if payload.Source != nil {
		c.identity.SourceBindingDigest = payload.Source.BindingDigest
	}
	manifest, err := taskpayload.NewJobInputManifest("", payload.ArtifactURIs, payload.ArtifactReferences)
	if err == nil {
		c.expected, err = manifest.Canonical()
	}
	c.expectedErr = err
	return c
}

func (c *immutableInputFixtureClient) ReportJobRunStatus(ctx context.Context, req *agentv1.ReportJobRunStatusRequest, opts ...grpc.CallOption) (*agentv1.ReportJobRunStatusResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	md, _ := metadata.FromOutgoingContext(ctx)
	claims := md.Get(executionClaimMetadataKey)
	if req == nil || req.JobRunId != c.identity.JobRunID || req.AgentId != c.identity.AgentID || req.RuntimeBindingDigest != c.identity.RuntimeBindingDigest || len(claims) != 1 || claims[0] == "" {
		return nil, fmt.Errorf("fixture report requires exact dispatched identity and claim")
	}
	if c.identity.ClaimID != "" && c.identity.ClaimID != claims[0] {
		return nil, fmt.Errorf("fixture report changed original execution claim")
	}
	response, err := c.artifactStatusClient.ReportJobRunStatus(ctx, req, opts...)
	if err == nil && req.Status == 3 {
		c.identity.ClaimID = claims[0]
	}
	return response, err
}

func (c *immutableInputFixtureClient) GetOrRecordJobInputs(ctx context.Context, req *agentv1.GetOrRecordJobInputsRequest, _ ...grpc.CallOption) (*agentv1.GetOrRecordJobInputsResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inputCalls++
	if req == nil {
		return nil, fmt.Errorf("fixture immutable input request is required")
	}
	identity := taskpayload.JobInputIdentity{
		PipelineID: req.PipelineId, PipelineRunID: req.PipelineRunId,
		JobRunID: req.JobRunId, AgentID: req.AgentId, ClaimID: req.OriginalClaimId,
		RuntimeBindingDigest: req.RuntimeBindingDigest, SourceBindingDigest: req.SourceBindingDigest,
	}
	md, _ := metadata.FromOutgoingContext(ctx)
	claims := md.Get(executionClaimMetadataKey)
	if identity.Validate() != nil || identity != c.identity || len(claims) != 1 || claims[0] != identity.ClaimID {
		return nil, fmt.Errorf("fixture immutable inputs require original claimed dispatch identity")
	}
	if c.expectedErr != nil {
		return nil, c.expectedErr
	}
	if req.ManifestJson == "" {
		if c.pinned == nil {
			return &agentv1.GetOrRecordJobInputsResponse{State: "unknown"}, nil
		}
		return proto.Clone(c.pinned).(*agentv1.GetOrRecordJobInputsResponse), nil
	}
	manifest, err := taskpayload.ParseJobInputManifest(req.ManifestJson)
	if err != nil {
		return nil, err
	}
	if (identity.SourceBindingDigest != "" && manifest.SourceCommit == "") || (identity.SourceBindingDigest == "" && manifest.SourceCommit != "") {
		return nil, fmt.Errorf("fixture immutable source differs from dispatch")
	}
	artifacts := *manifest
	artifacts.SourceCommit = ""
	encoded, err := artifacts.Canonical()
	if err != nil || encoded != c.expected {
		return nil, fmt.Errorf("fixture immutable artifacts differ from captured dispatch")
	}
	if c.pinned != nil {
		if req.ManifestJson != c.pinned.ManifestJson {
			return nil, fmt.Errorf("fixture immutable inputs are already pinned")
		}
		return proto.Clone(c.pinned).(*agentv1.GetOrRecordJobInputsResponse), nil
	}
	digest, err := manifest.Digest(identity)
	if err != nil {
		return nil, err
	}
	c.pinned = &agentv1.GetOrRecordJobInputsResponse{State: "pinned", ManifestJson: req.ManifestJson, ManifestDigest: digest}
	return proto.Clone(c.pinned).(*agentv1.GetOrRecordJobInputsResponse), nil
}

func assertImmutableJobInputRecord(t *testing.T, job *model.JobRun, payload *taskpayload.JobRunTaskPayload) *taskpayload.JobInputManifest {
	t.Helper()
	if !job.SourceExpectationCaptured || job.InputManifestState != "pinned" {
		t.Fatalf("job lacks controller-captured and pinned immutable inputs: %+v", job)
	}
	manifest, err := taskpayload.ParseJobInputManifest(job.InputManifest)
	if err != nil {
		t.Fatalf("job immutable manifest is not canonical: %v", err)
	}
	identity := taskpayload.JobInputIdentity{PipelineID: job.PipelineID, PipelineRunID: job.PipelineRunID, JobRunID: job.JobRunID, AgentID: job.AgentID, ClaimID: job.ClaimID, RuntimeBindingDigest: job.RuntimeBindingDigest, SourceBindingDigest: job.SourceBindingDigest}
	digest, err := manifest.Digest(identity)
	if err != nil || digest != job.InputManifestDigest {
		t.Fatalf("job immutable input digest lost original execution identity: %v", err)
	}
	if payload.Source == nil {
		if manifest.SourceCommit != "" || job.SourceBindingDigest != "" {
			t.Fatal("source-free job acquired immutable source inputs")
		}
	} else if !sourceprovenance.ValidCommit(manifest.SourceCommit) || manifest.SourceCommit != job.SourceResolvedCommit || payload.Source.BindingDigest != job.SourceBindingDigest {
		t.Fatal("source job did not pin its full original checkout commit")
	}
	expected, err := taskpayload.NewJobInputManifest("", payload.ArtifactURIs, payload.ArtifactReferences)
	if err != nil {
		t.Fatal(err)
	}
	expectation, err := expected.Canonical()
	if err != nil || expectation != job.InputExpectation {
		t.Fatalf("controller input expectation differs from actual Process dispatch: %v", err)
	}
	expected.SourceCommit = manifest.SourceCommit
	encoded, err := expected.Canonical()
	if err != nil || encoded != job.InputManifest {
		t.Fatalf("pinned input content differs from actual Process dispatch: %v", err)
	}
	return manifest
}

func TestImmutableInputFixtureRetainsCapturedInputsAndFirstPin(t *testing.T) {
	_, payload, _ := offlineWorkerFixture(t)
	payload.Source = &taskpayload.SourcePayload{BindingDigest: "v1:" + strings.Repeat("b", 64)}
	payload.ArtifactURIs = map[string]string{"producer/file": "object"}
	payload.ArtifactReferences = artifactTestReferences(payload.ArtifactURIs, map[string][]byte{"object": []byte("original")})
	client := newImmutableInputFixtureClient(payload, &artifactStatusClient{})
	ctx := metadata.AppendToOutgoingContext(context.Background(), executionClaimMetadataKey, "original-claim")
	if _, err := client.ReportJobRunStatus(ctx, &agentv1.ReportJobRunStatusRequest{AgentId: payload.AgentID, JobRunId: payload.JobRunID, Status: 3, RuntimeBindingDigest: payload.Runtime.BindingDigest}); err != nil {
		t.Fatal(err)
	}
	req := &agentv1.GetOrRecordJobInputsRequest{AgentId: payload.AgentID, PipelineId: payload.PipelineID, PipelineRunId: payload.PipelineRunID, JobRunId: payload.JobRunID, OriginalClaimId: "original-claim", RuntimeBindingDigest: payload.Runtime.BindingDigest, SourceBindingDigest: payload.Source.BindingDigest}
	unknown, err := client.GetOrRecordJobInputs(ctx, req)
	if err != nil || unknown.State != "unknown" {
		t.Fatalf("first immutable input read: %+v %v", unknown, err)
	}
	manifest, err := taskpayload.NewJobInputManifest(strings.Repeat("a", 40), payload.ArtifactURIs, payload.ArtifactReferences)
	if err != nil {
		t.Fatal(err)
	}
	req.ManifestJson, err = manifest.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := client.GetOrRecordJobInputs(ctx, req)
	if err != nil || pinned.State != "pinned" {
		t.Fatalf("first immutable input pin: %+v %v", pinned, err)
	}
	original := proto.Clone(pinned).(*agentv1.GetOrRecordJobInputsResponse)
	// Returned responses and the original payload must not alias captured state.
	pinned.ManifestJson = "changed response"
	*payload.ArtifactReferences["producer/file"].Size = 1
	changed, err := taskpayload.NewJobInputManifest(manifest.SourceCommit, payload.ArtifactURIs, payload.ArtifactReferences)
	if err != nil {
		t.Fatal(err)
	}
	req.ManifestJson, _ = changed.Canonical()
	if _, err := client.GetOrRecordJobInputs(ctx, req); err == nil {
		t.Fatal("fixture accepted mutated dispatched artifact metadata")
	}
	manifest.SourceCommit = strings.Repeat("c", 40)
	req.ManifestJson, _ = manifest.Canonical()
	if _, err := client.GetOrRecordJobInputs(ctx, req); err == nil {
		t.Fatal("fixture replaced its first pinned source commit")
	}
	req.ManifestJson = ""
	for _, change := range []func(*agentv1.GetOrRecordJobInputsRequest){
		func(r *agentv1.GetOrRecordJobInputsRequest) { r.AgentId = "other-agent" },
		func(r *agentv1.GetOrRecordJobInputsRequest) { r.PipelineId = "other-pipeline" },
		func(r *agentv1.GetOrRecordJobInputsRequest) { r.PipelineRunId = "other-run" },
		func(r *agentv1.GetOrRecordJobInputsRequest) { r.JobRunId = "other-job" },
		func(r *agentv1.GetOrRecordJobInputsRequest) { r.OriginalClaimId = "other-claim" },
		func(r *agentv1.GetOrRecordJobInputsRequest) { r.RuntimeBindingDigest = "v1:" + strings.Repeat("c", 64) },
		func(r *agentv1.GetOrRecordJobInputsRequest) { r.SourceBindingDigest = "v1:" + strings.Repeat("d", 64) },
	} {
		altered := proto.Clone(req).(*agentv1.GetOrRecordJobInputsRequest)
		change(altered)
		if _, err := client.GetOrRecordJobInputs(ctx, altered); err == nil {
			t.Fatal("fixture accepted a changed input execution identity")
		}
	}
	if _, err := client.GetOrRecordJobInputs(context.Background(), req); err == nil {
		t.Fatal("fixture accepted input request without original claim metadata")
	}
	stored, err := client.GetOrRecordJobInputs(ctx, req)
	if err != nil || !proto.Equal(stored, original) {
		t.Fatalf("fixture changed first pinned response: %+v %v", stored, err)
	}
}
