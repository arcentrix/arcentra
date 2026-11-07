// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"bytes"
	"errors"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func TestRerunBridgeIncarnationRejectionIsDefiniteButOldClaimSurvives(t *testing.T) {
	f := newRerunBridgeFixture(t)
	service := NewAgentServiceImpl(&AgentService{rerunClaim: f.bridge}, nil)
	r := f.request.Reference
	request := &agentv1.ClaimAndFetchCopilotRerunRequest{Reference: &agentv1.CopilotRerunClaimReference{AgentId: r.AgentID, PipelineId: r.PipelineID, RunId: r.RunID, JobRunId: r.JobRunID, GateId: r.GateID, ScheduleId: r.ScheduleID, SealId: r.SealID, SealDigest: r.SealDigest, PayloadDigest: r.PayloadDigest}, ClaimId: f.request.ClaimID, AdmissionNonce: bytes.Clone(f.request.AdmissionNonce), ArtifactStorageId: f.request.ArtifactStorageID, ArtifactStorageFingerprint: f.request.ArtifactStorageFingerprint}
	first, err := service.ClaimAndFetchCopilotRerun(f.ctx, request)
	require.NoError(t, err)
	var old model.JobRun
	require.NoError(t, f.db.Where("job_run_id = ?", r.JobRunID).First(&old).Error)
	changed := proto.Clone(request).(*agentv1.ClaimAndFetchCopilotRerunRequest)
	changed.ClaimId = uuid.NewString()
	changed.AdmissionNonce[0] ^= 1
	out, err := service.ClaimAndFetchCopilotRerun(f.ctx, changed)
	require.Nil(t, out)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.NotContains(t, err.Error(), string(changed.AdmissionNonce))
	var after model.JobRun
	require.NoError(t, f.db.Where("job_run_id = ?", r.JobRunID).First(&after).Error)
	require.Equal(t, old, after)
	replay, err := service.ClaimAndFetchCopilotRerun(f.ctx, request)
	require.NoError(t, err)
	require.True(t, proto.Equal(first, replay))
	const name = "rerun-incarnation-test-storage-error"
	require.NoError(t, f.db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "CopilotRerunAdmissionGate" {
			tx.AddError(errors.New("synthetic storage unavailable"))
		}
	}))
	out, err = service.ClaimAndFetchCopilotRerun(f.ctx, changed)
	require.Nil(t, out)
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.NoError(t, f.db.Callback().Query().Remove(name))
}
