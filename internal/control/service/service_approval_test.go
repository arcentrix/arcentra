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
	"testing"

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
