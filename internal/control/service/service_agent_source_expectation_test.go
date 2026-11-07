// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// This fixture deliberately hides the optional claimed-gate expectation adapter.
// Neither read nor proposed observation may fall back to legacy pinning.
func TestSourceExpectationGRPCUnsupportedRepositoryHasNoObservationOrApproval(t *testing.T) {
	db, req := inputServiceFixture(t, filepath.Join(t.TempDir(), "expectation.db"))
	if err := db.AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalDecision{}); err != nil {
		t.Fatal(err)
	}
	before := sourceReadJob(t, db)
	ordinary := repo.NewJobRunRepo(artifactServiceDatabase{db})
	legacy, ok := ordinary.(sourceExpectationLegacyInputs)
	if !ok {
		t.Fatal("legacy input repository missing")
	}
	unsupported := &sourceExpectationUnsupportedStore{IJobRunRepository: ordinary, inputs: legacy}
	if _, ok := any(unsupported).(ExpectedSourceInputRepository); ok {
		t.Fatal("unsupported fixture unexpectedly exposes expectation protocol")
	}
	client, _ := terminalTestClient(t, db, nil, unsupported)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-arcentra-claim-id", "original-claim")
	for _, manifest := range []string{"", req.ManifestJson} {
		r := proto.Clone(req).(*agentv1.GetOrRecordJobInputsRequest)
		r.ManifestJson = manifest
		r.SourceExpectationDigest = "v1:" + strings.Repeat("a", 64)
		if _, err := client.GetOrRecordJobInputs(ctx, r); status.Code(err) != codes.Unimplemented {
			t.Fatalf("unsupported expectation: %v", err)
		}
	}
	if after := sourceReadJob(t, db); !reflect.DeepEqual(before, after) {
		t.Fatal("unsupported protocol modified observed inputs")
	}
	var approvals, decisions int64
	if err := db.Model(&model.ApprovalRequest{}).Count(&approvals).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.ApprovalDecision{}).Count(&decisions).Error; err != nil {
		t.Fatal(err)
	}
	if approvals != 0 || decisions != 0 {
		t.Fatal("unsupported expectation created approval")
	}
	read := proto.Clone(req).(*agentv1.GetOrRecordJobInputsRequest)
	read.ManifestJson = ""
	response, err := client.GetOrRecordJobInputs(ctx, read)
	if err != nil || response.State != "unknown" || response.ManifestJson != "" || response.ManifestDigest != "" || response.SourceExpectationJson != "" || response.SourceExpectationDigest != "" {
		t.Fatal(response, err)
	}
	if err := db.Model(&model.ApprovalRequest{}).Count(&approvals).Error; err != nil || approvals != 0 {
		t.Fatal("nil observation read created approval", err)
	}
	if after := sourceReadJob(t, db); !reflect.DeepEqual(before, after) {
		t.Fatal("nil observation read changed proof")
	}
}

type sourceExpectationStoreStub struct {
	repo.IJobRunRepository
	row         *model.JobRun
	expectation *taskqueue.SourceCheckoutExpectation
}

func (s *sourceExpectationStoreStub) GetOrRecordJobInputsWithSourceExpectation(context.Context, taskqueue.JobInputIdentity, string, *taskqueue.JobInputManifest) (*model.JobRun, *taskqueue.SourceCheckoutExpectation, error) {
	return s.row, s.expectation, nil
}

// This tests only response validation; it is not a substitute for the pending
// real claimed-gate transaction and fresh approval persistence tests.
func TestSourceExpectationServiceRejectsIncoherentRepositoryReceipt(t *testing.T) {
	identity := taskqueue.JobInputIdentity{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", AgentID: "agent", ClaimID: "claim", RuntimeBindingDigest: "v1:" + strings.Repeat("a", 64), SourceBindingDigest: "v1:" + strings.Repeat("b", 64)}
	e := &taskqueue.SourceCheckoutExpectation{Version: 1, ScheduleID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", SealID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", ExpectedSourceCommit: strings.Repeat("c", 40)}
	digest, _ := e.Digest(identity)
	request := &agentv1.GetOrRecordJobInputsRequest{SourceExpectationDigest: digest}
	row := model.JobRun{PipelineID: identity.PipelineID, PipelineRunID: identity.PipelineRunID, JobRunID: identity.JobRunID, AgentID: identity.AgentID, ClaimID: identity.ClaimID, RuntimeBindingDigest: identity.RuntimeBindingDigest, SourceBindingDigest: identity.SourceBindingDigest, InputManifestState: "unknown"}
	stub := &sourceExpectationStoreStub{row: &row, expectation: e}
	service := &AgentServiceImpl{agentService: &AgentService{jobRunRepo: stub}}
	if r, err := service.getOrRecordExpectedSourceInputs(context.Background(), request, identity, nil); err != nil || r.State != "unknown" || r.ManifestJson != "" {
		t.Fatal(r, err)
	}
	for name, mutate := range map[string]func(*model.JobRun){"run": func(j *model.JobRun) { j.PipelineRunID = "other" }, "job": func(j *model.JobRun) { j.JobRunID = "other" }, "agent": func(j *model.JobRun) { j.AgentID = "other" }, "claim": func(j *model.JobRun) { j.ClaimID = "other" }, "runtime": func(j *model.JobRun) { j.RuntimeBindingDigest = "v1:" + strings.Repeat("d", 64) }, "source": func(j *model.JobRun) { j.SourceBindingDigest = "v1:" + strings.Repeat("d", 64) }, "approval": func(j *model.JobRun) { j.RetryApprovalID = "unexpected" }, "manifest": func(j *model.JobRun) { j.InputManifest = "{}" }} {
		t.Run(name, func(t *testing.T) {
			copy := row
			mutate(&copy)
			stub.row = &copy
			if _, err := service.getOrRecordExpectedSourceInputs(context.Background(), request, identity, nil); err == nil {
				t.Fatal("incoherent receipt accepted")
			}
		})
	}
	stub.row = &row
	manifest, _ := taskqueue.NewJobInputManifest(e.ExpectedSourceCommit, nil, nil)
	if _, err := service.getOrRecordExpectedSourceInputs(context.Background(), request, identity, manifest); err == nil {
		t.Fatal("observation returned unknown")
	}
	observed := row
	observed.InputManifestState = "pinned"
	observed.InputManifest, _ = manifest.Canonical()
	observed.InputManifestDigest, _ = manifest.Digest(identity)
	stub.row = &observed
	if _, err := service.getOrRecordExpectedSourceInputs(context.Background(), request, identity, manifest); err != nil {
		t.Fatal(err)
	}
	changed := *e
	changed.SealID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	stub.expectation = &changed
	if _, err := service.getOrRecordExpectedSourceInputs(context.Background(), request, identity, manifest); err == nil {
		t.Fatal("changed seal accepted")
	}
}

// Test-only wrapper forwards the old input contract but intentionally does not
// promote the new optional claimed-gate interface from concrete JobRunRepo.
type sourceExpectationLegacyInputs interface {
	GetOrRecordJobInputs(context.Context, taskqueue.JobInputIdentity, *taskqueue.JobInputManifest) (*model.JobRun, error)
}
type sourceExpectationUnsupportedStore struct {
	repo.IJobRunRepository
	inputs sourceExpectationLegacyInputs
}

func (s *sourceExpectationUnsupportedStore) GetOrRecordJobInputs(ctx context.Context, identity taskqueue.JobInputIdentity, proposed *taskqueue.JobInputManifest) (*model.JobRun, error) {
	return s.inputs.GetOrRecordJobInputs(ctx, identity, proposed)
}
