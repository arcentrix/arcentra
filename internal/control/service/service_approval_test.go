// Copyright 2026 Arcentra Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"context"
	"errors"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm/logger"
	"path/filepath"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type testApprovalPolicyRepo struct {
	policies []model.ApprovalPolicy
	err      error
}

func (r *testApprovalPolicyRepo) Create(_ context.Context, _ *model.ApprovalPolicy) error {
	return nil
}

func (r *testApprovalPolicyRepo) Update(_ context.Context, _ string, _ map[string]any) error {
	return nil
}

func (r *testApprovalPolicyRepo) Get(_ context.Context, _ string) (*model.ApprovalPolicy, error) {
	return nil, nil
}

func (r *testApprovalPolicyRepo) ListByScope(_ context.Context, _ string, _ string) ([]model.ApprovalPolicy, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.policies, nil
}

func (r *testApprovalPolicyRepo) Delete(_ context.Context, _ string) error {
	return nil
}

func TestApprovalServiceMatchPolicyByScopeWithoutRepo(t *testing.T) {
	t.Parallel()

	svc := NewApprovalService(nil, nil, nil)
	_, err := svc.MatchPolicyByScope(context.Background(), "project", "proj-1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not configured")
}

func TestApprovalServiceMatchPolicyByScopeNotFound(t *testing.T) {
	t.Parallel()

	svc := NewApprovalService(nil, nil, &testApprovalPolicyRepo{})
	_, err := svc.MatchPolicyByScope(context.Background(), "project", "proj-1")
	require.Error(t, err)
	require.True(t, errors.Is(err, gorm.ErrRecordNotFound))
}

func TestApprovalServiceMatchPolicyByScopeSelectFirstPolicy(t *testing.T) {
	t.Parallel()

	svc := NewApprovalService(nil, nil, &testApprovalPolicyRepo{
		policies: []model.ApprovalPolicy{
			{PolicyID: "policy-1"},
			{PolicyID: "policy-2"},
		},
	})

	policy, err := svc.MatchPolicyByScope(context.Background(), "project", "proj-1")
	require.NoError(t, err)
	require.NotNil(t, policy)
	require.Equal(t, "policy-1", policy.PolicyID)
}

func TestApprovalServiceUsesDurableDecisionsAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "approvals.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.WithContext(ctx).AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalDecision{}))
	approvals := repo.NewApprovalRepo(approvalServiceTestDB{db})
	decisions := repo.NewApprovalDecisionRepo(approvalServiceTestDB{db})
	svc := NewApprovalService(approvals, decisions, nil)
	expires := time.Now().Add(time.Hour)
	value := &model.ApprovalRequest{ApprovalID: "gate", PipelineRunID: "run", RequestedBy: "requester", CandidateApprovers: `["alice","bob"]`, RequiredApproverCount: 2, ExpiresAt: &expires}
	require.NoError(t, svc.CreateApprovalRequest(ctx, value))
	require.ErrorIs(t, svc.Approve(ctx, "gate", "outsider", "spoof"), repo.ErrApprovalForbidden)
	require.Error(t, svc.Reject(ctx, "gate", "", ""))
	require.NoError(t, svc.Approve(ctx, "gate", "alice", "reviewed"))
	persisted, err := svc.GetApproval(ctx, "gate")
	require.NoError(t, err)
	require.Equal(t, model.ApprovalStatusPending, persisted.Status)
	vote, err := svc.DecisionEligibility(ctx, persisted, "alice", model.ApprovalDecisionApprove)
	require.ErrorIs(t, err, repo.ErrApprovalConflict)
	require.Equal(t, model.ApprovalDecisionApprove, vote.Decision)
	require.NoError(t, svc.Approve(ctx, "gate", "alice", "idempotent"))
	require.ErrorIs(t, svc.Reject(ctx, "gate", "alice", "opposite"), repo.ErrApprovalConflict)
	require.NoError(t, svc.Approve(ctx, "gate", "bob", "reviewed"))
	persisted, err = svc.GetApproval(ctx, "gate")
	require.NoError(t, err)
	require.Equal(t, model.ApprovalStatusApproved, persisted.Status)
	release := "release"
	linked := &model.ApprovalRequest{ApprovalID: "linked", PipelineRunID: "run", RequestedBy: "requester", CandidateApprovers: `["alice"]`, RequiredApproverCount: 1, DeploymentReleaseID: &release, ExpiresAt: &expires}
	require.NoError(t, svc.CreateApprovalRequest(ctx, linked))
	require.ErrorContains(t, svc.Approve(ctx, "linked", "alice", ""), "validator not configured")
	denied := errors.New("current deployment permission revoked")
	called := false
	svc.SetDecisionValidator(func(_ context.Context, req *model.ApprovalRequest, actor, decision string) error {
		require.Equal(t, model.ApprovalDecisionApprove, decision)
		called = true
		require.Equal(t, "linked", req.ApprovalID)
		require.Equal(t, "alice", actor)
		return denied
	})
	require.ErrorIs(t, svc.Approve(ctx, "linked", "alice", ""), denied)
	require.True(t, called)
	records, err := decisions.ListByApproval(ctx, "linked")
	require.NoError(t, err)
	require.Empty(t, records)
}

func TestApprovalServiceReadsPersistExpiry(t *testing.T) {
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "expiry.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.WithContext(ctx).AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalDecision{}))
	approvals := repo.NewApprovalRepo(approvalServiceTestDB{db})
	svc := NewApprovalService(approvals, repo.NewApprovalDecisionRepo(approvalServiceTestDB{db}), nil)
	past := time.Now().Add(-time.Minute)
	for _, id := range []string{"get", "list"} {
		require.NoError(t, svc.CreateApprovalRequest(ctx, &model.ApprovalRequest{ApprovalID: id, PipelineRunID: "run", CandidateApprovers: `["alice"]`, RequiredApproverCount: 1, ExpiresAt: &past}))
	}
	value, err := svc.GetApproval(ctx, "get")
	require.NoError(t, err)
	require.Equal(t, model.ApprovalStatusExpired, value.Status)
	values, err := svc.ListByPipelineRun(ctx, "run")
	require.NoError(t, err)
	require.Len(t, values, 2)
	for _, value := range values {
		require.Equal(t, model.ApprovalStatusExpired, value.Status)
		require.EqualValues(t, 2, value.Version)
	}
}

type approvalServiceTestDB struct{ db *gorm.DB }

func (d approvalServiceTestDB) Database() *gorm.DB { return d.db }
