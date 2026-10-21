// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/copilot"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// copilotCurrentAuthorityFixture 使用真实仓储与过时通用授权快照复现默认日志分析路径
func copilotCurrentAuthorityFixture(t *testing.T) (*CopilotService, *gorm.DB, *copilotFakeProvider, CopilotRequest) {
	t.Helper()
	s, db, _, provider, input := copilotSourceFixture(t)
	require.NoError(t, s.ConfigureSourceContext(copilot.SourceContextConfig{}))
	require.NoError(t, db.Create(&model.StepRunLog{LogID: "current-authority-log", StepRunID: "step", LineNumber: 1, Content: "private-compile-context"}).Error)
	var err error
	s.authorizer, err = authz.NewAuthorizer(s.repos, nil)
	require.NoError(t, err)
	input.Context = CopilotSelection{StepRunIDs: []string{"step"}}
	return s, db, provider, input
}

type currentAuthorityLogReader struct {
	repo.ILogRepository
	after func()
}

// ListByStepRunID 仅在实际日志读取完成后注入权限变化
func (r *currentAuthorityLogReader) ListByStepRunID(ctx context.Context, step string, from int32, limit int) ([]*model.StepRunLog, error) {
	rows, err := r.ILogRepository.ListByStepRunID(ctx, step, from, limit)
	if err == nil && r.after != nil {
		r.after()
	}
	return rows, err
}

// TestCopilotRevocationDuringContentReadStopsDisclosureAndDispatch 验证内容读取期间撤销后不返回预览也不发送模型请求
func TestCopilotRevocationDuringContentReadStopsDisclosureAndDispatch(t *testing.T) {
	for _, operation := range []string{"preview", "analysis"} {
		t.Run(operation, func(t *testing.T) {
			s, db, provider, input := copilotCurrentAuthorityFixture(t)
			ctx := context.Background()
			subject := authz.Subject{UserID: "owner"}
			preview, err := s.Preview(ctx, subject, "p", input)
			require.NoError(t, err)
			input.ContextDigest = preview.Digest
			primary := s.contextPrimary
			revoked := false
			s.contextPrimary = func(ctx context.Context) (*repo.Repositories, error) {
				view, err := primary(ctx)
				if err != nil {
					return nil, err
				}
				view.Log = &currentAuthorityLogReader{ILogRepository: view.Log, after: func() {
					require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Update("is_enabled", 0).Error)
					revoked = true
				}}
				return view, nil
			}
			if operation == "preview" {
				result, err := s.Preview(ctx, subject, "p", input)
				require.ErrorIs(t, err, ErrCopilotDenied)
				require.Empty(t, result.Text)
			} else {
				result, err := s.Analyze(ctx, subject, "p", input)
				require.ErrorIs(t, err, ErrCopilotDenied)
				require.Empty(t, result.Text)
			}
			require.True(t, revoked, "negative must occur after the real content read")
			require.Zero(t, provider.calls)
		})
	}
}

// TestCopilotRevocationDuringModelCallSuppressesLateAnswer 验证模型请求中的撤销不会返回迟到结果
func TestCopilotRevocationDuringModelCallSuppressesLateAnswer(t *testing.T) {
	s, db, _, input := copilotCurrentAuthorityFixture(t)
	provider := &historyProvider{answer: "private-late-model-answer", before: func(context.Context) {
		require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Update("is_enabled", 0).Error)
	}}
	s.registry = &historyRegistry{provider: provider}
	ctx := context.Background()
	subject := authz.Subject{UserID: "owner"}
	preview, err := s.Preview(ctx, subject, "p", input)
	require.NoError(t, err)
	input.ContextDigest = preview.Digest
	result, err := s.Analyze(ctx, subject, "p", input)
	require.ErrorIs(t, err, ErrCopilotDenied)
	require.Empty(t, result.Text)
	require.Equal(t, 1, provider.count())
}

// TestCopilotDefaultContextUsesCurrentAuthority 验证默认路径拒绝过时允许与失效身份而不读取或传输内容
func TestCopilotDefaultContextUsesCurrentAuthority(t *testing.T) {
	for _, mutation := range []string{"disabled-role", "deleted-role", "expired-grant", "revoked-grant", "project-downgrade", "pipeline-downgrade", "member-removal", "cross-project", "disabled-user", "disabled-project", "disabled-pipeline"} {
		t.Run(mutation, func(t *testing.T) {
			s, db, provider, input := copilotCurrentAuthorityFixture(t)
			ctx := context.Background()
			subject := authz.Subject{UserID: "owner", TeamIDs: []string{"team-a"}}
			if mutation == "member-removal" {
				require.NoError(t, db.Create(&model.Team{TeamID: "team-a", Name: "team-a", IsEnabled: 1}).Error)
				require.NoError(t, db.Create(&model.TeamMember{TeamID: "team-a", UserID: "owner"}).Error)
				require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Updates(map[string]any{"subject_type": "team", "subject_id": "team-a"}).Error)
				require.NoError(t, s.authorizer.Reload(ctx))
			}
			preview, err := s.Preview(ctx, subject, "p", input)
			require.NoError(t, err)
			require.Contains(t, preview.Text, "private-compile-context")
			input.ContextDigest = preview.Digest
			switch mutation {
			case "disabled-role":
				require.NoError(t, db.Model(&model.Role{}).Where("role_id = ?", "reader-role").Update("is_enabled", 0).Error)
			case "deleted-role":
				require.NoError(t, db.Where("role_id = ?", "reader-role").Delete(&model.Role{}).Error)
			case "expired-grant":
				require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Update("expires_at", time.Now().Add(-time.Minute)).Error)
			case "revoked-grant":
				require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Update("is_enabled", 0).Error)
			case "project-downgrade":
				require.NoError(t, db.Where("role_id = ? AND permission_id = ?", "reader-role", "project:read").Delete(&model.RolePermissionBinding{}).Error)
			case "pipeline-downgrade":
				require.NoError(t, db.Where("role_id = ? AND permission_id = ?", "reader-role", "pipeline:read").Delete(&model.RolePermissionBinding{}).Error)
			case "member-removal":
				require.NoError(t, db.Where("team_id = ? AND user_id = ?", "team-a", "owner").Delete(&model.TeamMember{}).Error)
			case "cross-project":
				require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Update("scope_id", "foreign-project").Error)
			case "disabled-user":
				require.NoError(t, db.Model(&model.User{}).Where("user_id = ?", "owner").Update("is_enabled", 0).Error)
			case "disabled-project":
				require.NoError(t, db.Model(&model.Project{}).Where("project_id = ?", "p").Update("is_enabled", 0).Error)
			case "disabled-pipeline":
				require.NoError(t, db.Model(&model.Pipeline{}).Where("pipeline_id = ?", "pipe-run").Update("is_enabled", 0).Error)
			}
			// Never reload the stale checker or discard the forged/stale TeamIDs.
			// The service, not the caller or a role-management side effect, owns
			// current authorization for every request.
			status, statusErr := s.Status(ctx, subject, "p")
			if mutation == "pipeline-downgrade" || mutation == "disabled-pipeline" {
				require.NoError(t, statusErr, "project status requires project read, not unrelated pipeline authority")
			} else {
				require.ErrorIs(t, statusErr, ErrCopilotDenied)
				require.Empty(t, status.Models)
			}
			sources, err := s.Sources(ctx, subject, "p", "run")
			require.ErrorIs(t, err, ErrCopilotDenied)
			require.Empty(t, sources.Steps)
			require.Empty(t, sources.Artifacts)
			denied, err := s.Preview(ctx, subject, "p", input)
			require.ErrorIs(t, err, ErrCopilotDenied)
			require.Empty(t, denied.Text)
			result, err := s.Analyze(ctx, subject, "p", input)
			require.ErrorIs(t, err, ErrCopilotDenied)
			require.Empty(t, result.Text)
			require.Zero(t, provider.calls)
		})
	}
}

// TestCopilotCurrentAuthorityRejectsForeignUserAndProject 验证预览指纹不替代当前用户及项目授权
func TestCopilotCurrentAuthorityRejectsForeignUserAndProject(t *testing.T) {
	s, db, provider, input := copilotCurrentAuthorityFixture(t)
	ctx := context.Background()
	preview, err := s.Preview(ctx, authz.Subject{UserID: "owner"}, "p", input)
	require.NoError(t, err)
	input.ContextDigest = preview.Digest
	require.NoError(t, db.Create(&model.User{UserID: "outsider", IsEnabled: 1}).Error)
	for _, user := range []string{"outsider", "owner"} {
		project := "p"
		if user == "owner" {
			project = "foreign"
			require.NoError(t, db.Create(&model.Project{ProjectID: project, OrgID: "org", IsEnabled: 1, Status: model.ProjectStatusActive}).Error)
		}
		_, err = s.Analyze(ctx, authz.Subject{UserID: user, TeamIDs: []string{"team-a"}}, project, input)
		require.True(t, errors.Is(err, ErrCopilotDenied) || errors.Is(err, ErrCopilotNotFound))
		require.Zero(t, provider.calls)
	}
}

// TestCopilotCurrentAuthorityPreservesOrganizationScope 验证当前组织授权只覆盖所属项目
func TestCopilotCurrentAuthorityPreservesOrganizationScope(t *testing.T) {
	s, db, provider, input := copilotCurrentAuthorityFixture(t)
	ctx := context.Background()
	require.NoError(t, db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Updates(map[string]any{"scope_type": string(consts.ScopeTypeOrganization), "scope_id": "org"}).Error)
	subject := authz.Subject{UserID: "owner"}
	_, err := s.Status(ctx, subject, "p")
	require.NoError(t, err)
	_, err = s.Preview(ctx, subject, "p", input)
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.Project{ProjectID: "outside-org", OrgID: "other-org", IsEnabled: 1, Status: model.ProjectStatusActive}).Error)
	_, err = s.Status(ctx, subject, "outside-org")
	require.ErrorIs(t, err, ErrCopilotDenied)
	require.Zero(t, provider.calls)
}

// TestCopilotCurrentAuthorityNeverFallsBackToCachedAllow 验证主库读失败时不回退到旧授权
func TestCopilotCurrentAuthorityNeverFallsBackToCachedAllow(t *testing.T) {
	s, _, provider, input := copilotCurrentAuthorityFixture(t)
	ctx := context.Background()
	subject := authz.Subject{UserID: "owner"}
	preview, err := s.Preview(ctx, subject, "p", input)
	require.NoError(t, err)
	input.ContextDigest = preview.Digest
	s.contextPrimary = func(context.Context) (*repo.Repositories, error) {
		return nil, errors.New("synthetic current primary unavailable")
	}
	status, err := s.Status(ctx, subject, "p")
	require.ErrorIs(t, err, ErrCopilotContext)
	require.Empty(t, status.Models)
	sources, err := s.Sources(ctx, subject, "p", "run")
	require.ErrorIs(t, err, ErrCopilotContext)
	require.Empty(t, sources.Steps)
	denied, err := s.Preview(ctx, subject, "p", input)
	require.ErrorIs(t, err, ErrCopilotContext)
	require.Empty(t, denied.Text)
	result, err := s.Analyze(ctx, subject, "p", input)
	require.ErrorIs(t, err, ErrCopilotContext)
	require.Empty(t, result.Text)
	require.Zero(t, provider.calls)
}

// TestCopilotCurrentAuthorityKeepsPrivateHistoryOwnerBound 验证同项目的当前读者也不能访问别人的私有历史
func TestCopilotCurrentAuthorityKeepsPrivateHistoryOwnerBound(t *testing.T) {
	s, db, provider, conversation, input := historyFixture(t)
	historySend(t, s, conversation, input)
	ctx := context.Background()
	for _, row := range []any{
		&model.User{UserID: "other-reader", IsEnabled: 1},
		&model.RoleGrant{GrantID: "other-reader-grant", SubjectType: "user", SubjectID: "other-reader", RoleID: "reader-role", ScopeType: "project", ScopeID: "p", IsEnabled: 1},
		&model.Project{ProjectID: "other-project", OrgID: "org", IsEnabled: 1, Status: model.ProjectStatusActive},
		&model.RoleGrant{GrantID: "other-project-grant", SubjectType: "user", SubjectID: "owner", RoleID: "reader-role", ScopeType: "project", ScopeID: "other-project", IsEnabled: 1},
	} {
		require.NoError(t, db.Create(row).Error)
	}
	for _, scope := range []struct{ user, project string }{{"other-reader", "p"}, {"owner", "other-project"}} {
		subject := authz.Subject{UserID: scope.user}
		_, err := s.Status(ctx, subject, scope.project)
		require.NoError(t, err, "negative control must retain ordinary current project read")
		list, err := s.ListConversations(ctx, subject, scope.project, "", 20)
		require.NoError(t, err)
		require.Empty(t, list.Conversations)
		detail, err := s.GetConversation(ctx, subject, scope.project, conversation.ConversationID, 0, 20)
		require.ErrorIs(t, err, ErrCopilotNotFound)
		require.Empty(t, detail.Turns)
		preview, err := s.PreviewConversationTurn(ctx, subject, scope.project, conversation.ConversationID, input)
		require.ErrorIs(t, err, ErrCopilotNotFound)
		require.Empty(t, preview.Text)
	}
	require.Equal(t, 1, provider.count(), "unauthorized history reads must never invoke the model")
}
