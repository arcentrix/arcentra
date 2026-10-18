// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
)

// OrganizationService 提供组织与内置作用域成员管理，不提供任意角色授权入口
type OrganizationService struct{ repository repo.IOrganizationRepository }

// NewOrganizationService 创建组织成员服务
func NewOrganizationService(repository repo.IOrganizationRepository) *OrganizationService {
	return &OrganizationService{repository: repository}
}

// ScopedOrganizationAuthorization 在事务中从主库重建主体与角色，忽略客户端团队声明
func ScopedOrganizationAuthorization(ctx context.Context, repositories *repo.Repositories, actor string) (repo.ScopedAccess, error) {
	reader, ok := repositories.User.(repo.IExecutionActorReader)
	if !ok {
		return nil, repo.ErrOrganizationForbidden
	}
	user, err := reader.GetExecutionActor(ctx, actor)
	if err != nil {
		return nil, err
	}
	if user == nil || user.UserID != actor || user.IsEnabled != 1 {
		return nil, repo.ErrOrganizationForbidden
	}
	subject := authz.Subject{UserID: actor}
	memberships, err := repositories.TeamMember.ListUserTeams(ctx, actor)
	if err != nil {
		return nil, err
	}
	if len(memberships) > 1000 {
		return nil, repo.ErrOrganizationForbidden
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, m := range memberships {
		if m.UserID != actor || m.TeamID == "" {
			return nil, repo.ErrOrganizationForbidden
		}
		if !seen[m.TeamID] {
			ids = append(ids, m.TeamID)
			seen[m.TeamID] = true
		}
	}
	if len(ids) > 0 {
		teams, err := repositories.Team.BatchGet(ctx, ids)
		if err != nil {
			return nil, err
		}
		for _, team := range teams {
			if team == nil || !seen[team.TeamID] {
				return nil, repo.ErrOrganizationForbidden
			}
			if team.IsEnabled == 1 {
				subject.TeamIDs = append(subject.TeamIDs, team.TeamID)
			}
		}
	}
	checker, err := authz.NewLiveAuthorizer(ctx, repositories)
	if err != nil {
		return nil, err
	}
	grants, err := repositories.RoleGrant.ListAllEnabled(ctx)
	if err != nil {
		return nil, err
	}
	roles, err := repositories.Role.BatchGet(ctx, []string{consts.RoleOrgOwner, consts.RoleOrgAdmin, consts.RoleOrgMember, consts.RoleProjectOwner, consts.RoleProjectMaintainer, consts.RoleProjectDeveloper, consts.RoleProjectReporter, consts.RoleProjectGuest})
	if err != nil {
		return nil, err
	}
	enabled := map[string]bool{}
	for _, r := range roles {
		enabled[r.RoleID] = r.IsEnabled == 1
	}
	platformUpdate, err := checker.Check(ctx, subject, consts.PermOrgUpdate, authz.ResourceRef{})
	if err != nil {
		return nil, err
	}
	platformManage, err := checker.Check(ctx, subject, consts.PermOrgManageMember, authz.ResourceRef{})
	if err != nil {
		return nil, err
	}
	platform := platformUpdate && platformManage
	return func(kind, id, org string) (repo.ScopedCapabilities, error) {
		c := repo.ScopedCapabilities{Create: platform, AssignOwner: platform}
		resource := authz.ResourceRef{OrgID: org}
		read, update, manage := consts.PermOrgRead, consts.PermOrgUpdate, consts.PermOrgManageMember
		ownerRole := consts.RoleOrgOwner
		if kind == "project" {
			resource.ProjectID = id
			read, update, manage = consts.PermProjectRead, consts.PermProjectUpdate, consts.PermProjectManageMember
			ownerRole = consts.RoleProjectOwner
		}
		var err error
		c.CreateProject, err = checker.Check(ctx, subject, consts.PermOrgCreateProject, resource)
		if err != nil {
			return c, err
		}
		c.Read, err = checker.Check(ctx, subject, read, resource)
		if err != nil {
			return c, err
		}
		c.Update, err = checker.Check(ctx, subject, update, resource)
		if err != nil {
			return c, err
		}
		c.ManageMembers, err = checker.Check(ctx, subject, manage, resource)
		if err != nil {
			return c, err
		}
		for _, g := range grants {
			if g.SubjectType == "user" && g.SubjectID == actor && g.ScopeType == kind && g.ScopeID == id && g.RoleID == ownerRole && g.IsEnabled == 1 && enabled[g.RoleID] && (g.ExpiresAt == nil || g.ExpiresAt.After(time.Now())) {
				c.AssignOwner = true
			}
		}

		candidates := []string{consts.RoleOrgOwner, consts.RoleOrgAdmin, consts.RoleOrgMember}
		if kind == "project" {
			candidates = []string{consts.RoleProjectOwner, consts.RoleProjectMaintainer, consts.RoleProjectDeveloper, consts.RoleProjectReporter, consts.RoleProjectGuest}
		}
		c.AssignableRoleIDs = []string{}
		if c.ManageMembers {
			for _, role := range candidates {
				if !enabled[role] {
					continue
				}
				if role == ownerRole && !c.AssignOwner {
					continue
				}
				permitted := true
				for _, permission := range consts.SeedBuiltinRolePermissions()[role] {
					allowed, err := checker.Check(ctx, subject, permission, resource)
					if err != nil {
						return c, err
					}
					if !allowed {
						permitted = false
						break
					}
				}
				if permitted {
					c.AssignableRoleIDs = append(c.AssignableRoleIDs, role)
				}
			}
		}
		return c, nil
	}, nil
}

var organizationName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,63}$`)

func validateOrganizationInput(in *model.OrganizationInput, create bool) error {
	if create {
		in.Name = strings.TrimSpace(in.Name)
		if !organizationName.MatchString(in.Name) {
			return repo.ErrOrganizationInvalid
		}
		if in.Version != 0 {
			return repo.ErrOrganizationInvalid
		}
	} else if in.Version == 0 || in.Name != "" {
		return repo.ErrOrganizationInvalid
	}
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if create && in.DisplayName == "" {
		in.DisplayName = in.Name
	}
	if in.DisplayName == "" || !utf8.ValidString(in.Description) || !utf8.ValidString(in.DisplayName) || utf8.RuneCountInString(in.DisplayName) > 128 || utf8.RuneCountInString(in.Description) > 2000 || len(in.Website) > 2048 || len(in.Email) > 254 {
		return repo.ErrOrganizationInvalid
	}
	for _, r := range in.DisplayName {
		if unicode.IsControl(r) {
			return repo.ErrOrganizationInvalid
		}
	}
	for _, r := range in.Description {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return repo.ErrOrganizationInvalid
		}
	}
	if in.Website != "" {
		u, err := url.Parse(in.Website)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
			return repo.ErrOrganizationInvalid
		}
	}
	if in.Email != "" {
		m, err := mail.ParseAddress(in.Email)
		if err != nil || m.Address != in.Email {
			return repo.ErrOrganizationInvalid
		}
	}
	return nil
}
func validScopedID(id string) bool {
	return id != "" && id == strings.TrimSpace(id) && len(id) <= 64 && utf8.ValidString(id) && !strings.ContainsAny(id, "\x00\r\n")
}

// List 返回可见组织分页
func (s *OrganizationService) List(ctx context.Context, actor, name string, page, size int) (*model.OrganizationList, error) {
	if page < 1 || size < 1 || size > 100 || page > 1000000 || len(name) > 128 {
		return nil, repo.ErrOrganizationInvalid
	}
	return s.repository.List(ctx, actor, name, page, size, ScopedOrganizationAuthorization)
}

// Get 返回实时可见组织详情
func (s *OrganizationService) Get(ctx context.Context, actor, id string) (*model.OrganizationDetail, error) {
	if !validScopedID(id) {
		return nil, repo.ErrOrganizationInvalid
	}
	return s.repository.Get(ctx, actor, id, ScopedOrganizationAuthorization)
}

// Create 创建组织并显式赋予创建者组织所有者角色
func (s *OrganizationService) Create(ctx context.Context, actor string, in model.OrganizationInput) (*model.OrganizationDetail, error) {
	if err := validateOrganizationInput(&in, true); err != nil {
		return nil, err
	}
	return s.repository.Create(ctx, actor, in, ScopedOrganizationAuthorization)
}

// Update 更新组织允许的元数据
func (s *OrganizationService) Update(ctx context.Context, actor, id string, in model.OrganizationInput) (*model.OrganizationDetail, error) {
	if !validScopedID(id) {
		return nil, repo.ErrOrganizationInvalid
	}
	if err := validateOrganizationInput(&in, false); err != nil {
		return nil, err
	}
	return s.repository.Update(ctx, actor, id, in, false, ScopedOrganizationAuthorization)
}

// Archive 软归档组织，不删除历史项目或授权记录
func (s *OrganizationService) Archive(ctx context.Context, actor, id string, version uint64) (*model.OrganizationDetail, error) {
	if !validScopedID(id) || version == 0 {
		return nil, repo.ErrOrganizationInvalid
	}
	return s.repository.Update(ctx, actor, id, model.OrganizationInput{Version: version}, true, ScopedOrganizationAuthorization)
}

// Members 返回显式成员关系及管理能力
func (s *OrganizationService) Members(ctx context.Context, actor, kind, id string) (*model.ScopedMemberList, error) {
	if !validScopedID(id) {
		return nil, repo.ErrOrganizationInvalid
	}
	return s.repository.Members(ctx, actor, kind, id, ScopedOrganizationAuthorization)
}

// ChangeMember 仅允许内置作用域角色与已存在用户的成员操作
func (s *OrganizationService) ChangeMember(ctx context.Context, actor, kind, id, operation string, in model.ScopedMemberInput) (*model.ScopedMemberDetail, error) {
	if !validScopedID(id) || !validScopedID(in.UserID) || (kind != "org" && kind != "project") {
		return nil, repo.ErrOrganizationInvalid
	}
	if operation != "add" && operation != "update" && operation != "remove" {
		return nil, repo.ErrOrganizationInvalid
	}
	if operation == "add" && in.Version != 0 {
		return nil, repo.ErrOrganizationInvalid
	}
	if operation != "add" && in.Version == 0 {
		return nil, repo.ErrOrganizationInvalid
	}
	if operation != "remove" && in.RoleID == "" {
		return nil, repo.ErrOrganizationInvalid
	}
	return s.repository.ChangeMember(ctx, actor, kind, id, operation, in, ScopedOrganizationAuthorization)
}
