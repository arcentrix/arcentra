// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"errors"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"strings"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type protectedReceiptStore struct {
	repo.IJobRunRepository
	row        model.JobRun
	inspected  int
	inspectErr error
	mutateRead func(*model.JobRun)
	reads      int
}

func (s *protectedReceiptStore) GetByJobRunID(context.Context, string) (*model.JobRun, error) {
	s.reads++
	row := s.row
	if s.reads > 1 && s.mutateRead != nil {
		s.mutateRead(&row)
	}
	return &row, nil
}
func (s *protectedReceiptStore) InspectCopilotRerunJobReceipt(_ context.Context, row *model.JobRun) error {
	s.inspected++
	if row.TerminalReportDigest != s.row.TerminalReportDigest {
		return errors.New("stale receipt")
	}
	return s.inspectErr
}
func (s *protectedReceiptStore) CommitCancellationReceiptWithSource(_ context.Context, _ *model.JobRun, claim, digest string, _ map[string]any) error {
	s.row.TerminalReportDigest = digest
	return nil
}
func (s *protectedReceiptStore) ValidateTerminalConflict(context.Context, *model.JobRun, string) error {
	return errors.New("legacy protected report path must not be called")
}

func TestProtectedJobReportShortcutsInspectExactReceipt(t *testing.T) {
	for _, state := range []int{model.JobRunStatusRunning, model.JobRunStatusFailed, model.JobRunStatusCancelled} {
		for _, denied := range []bool{false, true} {
			t.Run(string(rune('0'+state))+map[bool]string{false: "/ok", true: "/deny"}[denied], func(t *testing.T) {
				store := &protectedReceiptStore{row: model.JobRun{JobRunID: "job", PipelineRunID: "run", PipelineID: "pipeline", AgentID: "agent", ClaimID: "claim", CopilotRerunGateID: "gate", CopilotRerunIntentID: "intent", Status: state}}
				req := &agentv1.ReportJobRunStatusRequest{JobRunId: "job", AgentId: "agent", Status: int32(state)}
				if state == model.JobRunStatusFailed {
					digest, err := terminalReportDigest(req, "claim", "pipeline", "run", "job")
					require.NoError(t, err)
					store.row.TerminalReportDigest = digest
				}
				if state == model.JobRunStatusCancelled {
					store.row.CancellationClaimID = "claim"
				}
				if denied {
					store.inspectErr = errors.New("private storage detail")
				}
				ctx := agentidentity.WithIdentity(context.Background(), agentidentity.Identity{ID: "agent"})
				ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("x-arcentra-claim-id", "claim"))
				service := NewAgentServiceImpl(&AgentService{jobRunRepo: store}, nil)
				result, err := service.ReportJobRunStatus(ctx, req)
				require.Equal(t, 1, store.inspected)
				if denied {
					require.Equal(t, codes.FailedPrecondition, status.Code(err))
					require.NotContains(t, err.Error(), "private")
					require.Nil(t, result)
				} else {
					require.NoError(t, err)
					require.True(t, result.Success)
				}
				if state == model.JobRunStatusCancelled {
					require.Equal(t, 2, store.reads)
					require.NotEmpty(t, store.row.TerminalReportDigest)
				}
			})
		}
	}
}

func TestProtectedCancellationFreshReadRejectsIdentityOrDigestChanges(t *testing.T) {
	for name, change := range map[string]func(*model.JobRun){
		"gate":    func(j *model.JobRun) { j.CopilotRerunGateID = "other" },
		"intent":  func(j *model.JobRun) { j.CopilotRerunIntentID = "other" },
		"claim":   func(j *model.JobRun) { j.ClaimID = "other" },
		"storage": func(j *model.JobRun) { j.ArtifactStorageID = "other" },
		"digest":  func(j *model.JobRun) { j.TerminalReportDigest = "other" },
		"source":  func(j *model.JobRun) { j.SourceResolvedCommit = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			row := model.JobRun{JobRunID: "job", PipelineRunID: "run", PipelineID: "pipeline", AgentID: "agent", ClaimID: "claim", CancellationClaimID: "claim", CopilotRerunGateID: "gate", CopilotRerunIntentID: "intent", Status: model.JobRunStatusCancelled, TerminalReportDigest: "digest"}
			store := &protectedReceiptStore{row: row, reads: 1, mutateRead: change}
			err := inspectProtectedCancellationReceipt(context.Background(), store, &row, "claim", "digest", map[string]any{"source_provenance_state": "unknown", "source_resolved_commit": ""})
			require.Error(t, err)
			require.Zero(t, store.inspected)
		})
	}
}
func TestProtectedReceiptRequiresCapabilityAndPreservesOrdinary(t *testing.T) {
	require.NoError(t, inspectProtectedRerunReceipt(context.Background(), struct{}{}, &model.JobRun{}))
	for _, job := range []*model.JobRun{{CopilotRerunGateID: "gate"}, {CopilotRerunIntentID: "intent"}, {ExpectedSourceCommit: "commit"}} {
		require.Equal(t, codes.Unimplemented, status.Code(inspectProtectedRerunReceipt(context.Background(), struct{}{}, job)))
	}
}

func TestProtectedSourceAndCancellationReplayAvoidLegacyLockPath(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "source-terminal", true: "cancel-repeat"}[cancelled], func(t *testing.T) {
			state := model.JobRunStatusFailed
			if cancelled {
				state = model.JobRunStatusCancelled
			}
			binding, err := sourceprovenance.NewBinding("pipeline", "run", "job", "git", "https://example.test/project.git", "main")
			require.NoError(t, err)
			commit := strings.Repeat("a", 40)
			row := model.JobRun{JobRunID: "job", PipelineRunID: "run", PipelineID: "pipeline", AgentID: "agent", ClaimID: "claim", CopilotRerunGateID: "gate", CopilotRerunIntentID: "intent", Status: state, SourceExpectationCaptured: true, SourceType: "git", SourceRepository: binding.Repository, SourceRef: binding.Ref, SourceBindingDigest: binding.Digest, SourceResolvedCommit: commit, SourceProvenanceState: "reported"}
			req := &agentv1.ReportJobRunStatusRequest{JobRunId: "job", AgentId: "agent", Status: int32(state), SourceCheckout: &agentv1.SourceCheckout{BindingDigest: binding.Digest, ResolvedCommit: commit}}
			if cancelled {
				row.CancellationClaimID = "claim"
			}
			row.TerminalReportDigest, err = terminalReportDigest(req, "claim", "pipeline", "run", "job")
			require.NoError(t, err)
			store := &protectedReceiptStore{row: row}
			ctx := agentidentity.WithIdentity(context.Background(), agentidentity.Identity{ID: "agent"})
			ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("x-arcentra-claim-id", "claim"))
			result, err := NewAgentServiceImpl(&AgentService{jobRunRepo: store}, nil).ReportJobRunStatus(ctx, req)
			require.NoError(t, err)
			require.True(t, result.Success)
			require.Equal(t, 2, store.inspected)
		})
	}
}
