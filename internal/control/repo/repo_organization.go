// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrOrganizationForbidden 表示当前作用域权限不足
var ErrOrganizationForbidden = errors.New("organization or membership access denied")

// ErrOrganizationConflict 表示版本、成员状态或所有者保护冲突
var ErrOrganizationConflict = errors.New("organization or membership changed; reload before retrying")

// ErrOrganizationNotFound 表示作用域或成员不存在
var ErrOrganizationNotFound = errors.New("organization or member not found")

// ErrOrganizationInvalid 表示输入不满足受限成员管理契约
var ErrOrganizationInvalid = errors.New("invalid organization or member request")

// ScopedCapabilities 是当前事务权限检查结果
type ScopedCapabilities struct {
	Read, Update, ManageMembers, AssignOwner, Create, CreateProject bool
	AssignableRoleIDs                                               []string
}

// ScopedAccess 根据已从数据库加载的真实资源判断权限
type ScopedAccess func(scopeType, scopeID, orgID string) (ScopedCapabilities, error)

// ScopedAuthorize 只使用事务主库视图重建当前主体和权限
type ScopedAuthorize func(context.Context, *Repositories, string) (ScopedAccess, error)

// IOrganizationRepository 定义事务组织及受限成员管理操作
type IOrganizationRepository interface {
	List(context.Context, string, string, int, int, ScopedAuthorize) (*model.OrganizationList, error)
	Get(context.Context, string, string, ScopedAuthorize) (*model.OrganizationDetail, error)
	Create(context.Context, string, model.OrganizationInput, ScopedAuthorize) (*model.OrganizationDetail, error)
	Update(context.Context, string, string, model.OrganizationInput, bool, ScopedAuthorize) (*model.OrganizationDetail, error)
	Members(context.Context, string, string, string, ScopedAuthorize) (*model.ScopedMemberList, error)
	ChangeMember(context.Context, string, string, string, string, model.ScopedMemberInput, ScopedAuthorize) (*model.ScopedMemberDetail, error)
}

// OrganizationRepo 将成员关联、专属授权与审计放在同一事务中
type OrganizationRepo struct{ database.IDatabase }

// NewOrganizationRepo 创建组织仓储
func NewOrganizationRepo(db database.IDatabase) IOrganizationRepository { return &OrganizationRepo{db} }

var organizationColumns = []string{"org_id", "name", "display_name", "description", "website", "email", "owner_user_id", "status", "is_enabled", "version", "created_at", "updated_at"}

func scopedQuery(ctx context.Context, tx *gorm.DB) *gorm.DB {
	return tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"})
}
func (r *OrganizationRepo) transaction(ctx context.Context, fn func(*gorm.DB) error) error {
	if r == nil || r.IDatabase == nil || r.Database() == nil {
		return fmt.Errorf("organization database unavailable")
	}
	return r.Database().WithContext(ctx).Clauses(database.Write()).Transaction(fn)
}
func scopedAuth(ctx context.Context, tx *gorm.DB, actor string, authorize ScopedAuthorize) (ScopedAccess, func(context.Context) error, error) {
	if authorize == nil || actor == "" {
		return nil, nil, ErrOrganizationForbidden
	}
	repositories, current := NewScopedAuthorizationView(tx)
	access, err := authorize(ctx, repositories, actor)
	return access, current, err
}
func organizationRow(ctx context.Context, tx *gorm.DB, id string) (*model.Organization, error) {
	var rows []model.Organization
	err := scopedQuery(ctx, tx).Select(organizationColumns).Where("org_id = ?", id).Limit(2).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrOrganizationNotFound
	}
	if len(rows) != 1 || rows[0].OrgID != id {
		return nil, ErrOrganizationConflict
	}
	return &rows[0], nil
}
func organizationActive(o *model.Organization) bool {
	return o.IsEnabled == 1 && o.Status == model.OrgStatusActive
}
func organizationDTO(o *model.Organization, c ScopedCapabilities) model.OrganizationDetail {
	return model.OrganizationDetail{OrgID: o.OrgID, Name: o.Name, DisplayName: o.DisplayName, Description: o.Description, Website: o.Website, Email: o.Email, OwnerUserID: o.OwnerUserID, Status: o.Status, IsEnabled: o.IsEnabled, Version: o.Version, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt, CanUpdate: c.Update && organizationActive(o), CanManageMembers: c.ManageMembers && organizationActive(o), CanArchive: c.Update && c.AssignOwner && organizationActive(o)}
}
func orgIsMember(ctx context.Context, tx *gorm.DB, org, actor string) (bool, error) {
	var rows []model.OrganizationMember
	err := scopedQuery(ctx, tx).Select("org_id", "user_id", "status").Where("org_id = ? AND user_id = ? AND status = ?", org, actor, model.OrgMemberStatusActive).Find(&rows).Error
	for _, m := range rows {
		if m.OrgID == org && m.UserID == actor {
			return true, err
		}
	}
	return false, err
}

// List 返回具有实时读取权限的真实组织，不将授权作用域当成组织
func (r *OrganizationRepo) List(ctx context.Context, actor, name string, page, size int, authorize ScopedAuthorize) (*model.OrganizationList, error) {
	out := &model.OrganizationList{List: []model.OrganizationDetail{}, Page: page, PageSize: size}
	err := r.transaction(ctx, func(tx *gorm.DB) error {
		var rows []model.Organization
		out.List = []model.OrganizationDetail{}
		out.Total = 0
		q := tx.WithContext(ctx).Select(organizationColumns).Where("is_enabled = ? AND status = ?", 1, model.OrgStatusActive).Order("org_id ASC")
		if name != "" {
			q = q.Where("name LIKE ? OR display_name LIKE ?", "%"+name+"%", "%"+name+"%")
		}
		if err := q.Find(&rows).Error; err != nil {
			return err
		}
		// Listing is an advisory primary snapshot: do not hold organization/member
		// range locks while acquiring the catalog, which would invert creation.
		var memberships []model.OrganizationMember
		if err := tx.WithContext(ctx).Select("org_id", "user_id", "status").Where("user_id = ? AND status = ?", actor, model.OrgMemberStatusActive).Find(&memberships).Error; err != nil {
			return err
		}
		memberScopes := map[string]bool{}
		for _, m := range memberships {
			if m.UserID == actor {
				memberScopes[m.OrgID] = true
			}
		}
		access, current, err := scopedAuth(ctx, tx, actor, authorize)
		if err != nil {
			return err
		}
		platform, err := access("", "", "")
		if err != nil {
			return err
		}
		out.CanCreate = platform.Create
		for i := range rows {
			c, err := access("org", rows[i].OrgID, rows[i].OrgID)
			if err != nil {
				return err
			}
			if !c.Read {
				continue
			}
			out.Total++
			if out.Total <= (page-1)*size || len(out.List) >= size {
				continue
			}
			dto := organizationDTO(&rows[i], c)
			dto.IsMember = memberScopes[rows[i].OrgID]
			out.List = append(out.List, dto)
		}
		return current(ctx)
	})
	return out, err
}

// Get 返回单个真实组织及显式成员状态
func (r *OrganizationRepo) Get(ctx context.Context, actor, id string, authorize ScopedAuthorize) (*model.OrganizationDetail, error) {
	var out model.OrganizationDetail
	err := r.transaction(ctx, func(tx *gorm.DB) error {
		o, err := organizationRow(ctx, tx, id)
		if err != nil {
			return err
		}
		access, current, err := scopedAuth(ctx, tx, actor, authorize)
		if err != nil {
			return err
		}
		c, err := access("org", id, id)
		if err != nil {
			return err
		}
		if !c.Read {
			return ErrOrganizationForbidden
		}
		if !organizationActive(o) {
			return ErrOrganizationNotFound
		}
		out = organizationDTO(o, c)
		out.IsMember, err = orgIsMember(ctx, tx, id, actor)
		if err != nil {
			return err
		}
		return current(ctx)
	})
	return &out, err
}
func scopedAudit(ctx context.Context, tx *gorm.DB, actor, action, kind, id, target, role string) error {
	body, err := sonic.Marshal(map[string]string{"targetUserId": target, "roleId": role})
	if err != nil {
		return err
	}
	return tx.WithContext(ctx).Create(&model.AuditLog{LogID: uuid.NewString(), ActorUserID: actor, Action: action, ResourceType: kind, ResourceID: id, ScopeType: kind, ScopeID: id, Result: model.AuditResultSuccess, Payload: datatypes.JSON(body)}).Error
}
func scopedRole(ctx context.Context, tx *gorm.DB, scope, role string) error {
	allowed := map[string]string{consts.RoleOrgOwner: "org", consts.RoleOrgAdmin: "org", consts.RoleOrgMember: "org", consts.RoleProjectOwner: "project", consts.RoleProjectMaintainer: "project", consts.RoleProjectDeveloper: "project", consts.RoleProjectReporter: "project", consts.RoleProjectGuest: "project"}
	if allowed[role] != scope {
		return ErrOrganizationInvalid
	}
	var rows []model.Role
	if err := scopedQuery(ctx, tx).Select("role_id", "scope_type", "is_system", "is_enabled").Where("role_id = ?", role).Limit(2).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) != 1 || rows[0].RoleID != role || rows[0].ScopeType != scope || rows[0].IsSystem != 1 || rows[0].IsEnabled != 1 {
		return ErrOrganizationConflict
	}
	return validateScopedBuiltinPermissions(ctx, tx, role)
}
func scopedGrant(ctx context.Context, tx *gorm.DB, actor, user, scope, id, role string) (string, error) {
	if err := scopedRole(ctx, tx, scope, role); err != nil {
		return "", err
	}
	grant := model.RoleGrant{GrantID: uuid.NewString(), SubjectType: "user", SubjectID: user, RoleID: role, ScopeType: scope, ScopeID: id, GrantedBy: actor, IsEnabled: 1}
	return grant.GrantID, tx.WithContext(ctx).Create(&grant).Error
}

// Create 仅将明确创建组织的操作者赋予新组织的所有者角色
func (r *OrganizationRepo) Create(ctx context.Context, actor string, in model.OrganizationInput, authorize ScopedAuthorize) (*model.OrganizationDetail, error) {
	var out model.OrganizationDetail
	err := r.transaction(ctx, func(tx *gorm.DB) error {
		access, current, err := scopedAuth(ctx, tx, actor, authorize)
		if err != nil {
			return err
		}
		c, err := access("", "", "")
		if err != nil {
			return err
		}
		if !c.Create {
			return ErrOrganizationForbidden
		}
		o := model.Organization{OrgID: uuid.NewString(), Name: in.Name, DisplayName: in.DisplayName, Description: in.Description, Website: in.Website, Email: in.Email, OwnerUserID: actor, Status: model.OrgStatusActive, IsEnabled: 1, Version: 1, Plan: model.PlanFree, Settings: datatypes.JSON("{}")}
		if err := tx.WithContext(ctx).Create(&o).Error; err != nil {
			return fmt.Errorf("%w: organization name is unavailable", ErrOrganizationConflict)
		}
		grant, err := scopedGrant(ctx, tx, actor, actor, "org", o.OrgID, consts.RoleOrgOwner)
		if err != nil {
			return err
		}
		member := model.OrganizationMember{OrgID: o.OrgID, UserID: actor, InvitedBy: actor, Status: model.OrgMemberStatusActive, Version: 1, ManagedGrantID: grant, ManagedRoleID: consts.RoleOrgOwner}
		if err := tx.WithContext(ctx).Create(&member).Error; err != nil {
			return err
		}
		if err := scopedAudit(ctx, tx, actor, "org:create", "org", o.OrgID, actor, consts.RoleOrgOwner); err != nil {
			return err
		}
		out = organizationDTO(&o, ScopedCapabilities{Read: true, Update: true, ManageMembers: true, AssignOwner: true})
		out.IsMember = true
		return current(ctx)
	})
	return &out, err
}

// Update 以版本比较交换更新元数据或软归档，保留所有历史对象
func (r *OrganizationRepo) Update(ctx context.Context, actor, id string, in model.OrganizationInput, archive bool, authorize ScopedAuthorize) (*model.OrganizationDetail, error) {
	var out model.OrganizationDetail
	err := r.transaction(ctx, func(tx *gorm.DB) error {
		o, err := organizationRow(ctx, tx, id)
		if err != nil {
			return err
		}
		var active []model.Project
		if archive {
			if err := scopedQuery(ctx, tx).Select("project_id").Where("org_id = ? AND is_enabled = ? AND status = ?", id, 1, model.ProjectStatusActive).Order("project_id ASC").Find(&active).Error; err != nil {
				return err
			}
		}

		access, current, err := scopedAuth(ctx, tx, actor, authorize)
		if err != nil {
			return err
		}
		c, err := access("org", id, id)
		if err != nil {
			return err
		}
		if !c.Update || (archive && !c.AssignOwner) {
			return ErrOrganizationForbidden
		}
		if !organizationActive(o) || in.Version != o.Version || o.Version == math.MaxUint64 {
			return ErrOrganizationConflict
		}

		if archive && len(active) > 0 {
			return fmt.Errorf("%w: archive active projects first", ErrOrganizationConflict)
		}

		values := map[string]any{"display_name": in.DisplayName, "description": in.Description, "website": in.Website, "email": in.Email, "version": o.Version + 1}
		action := "org:update"
		if archive {
			values = map[string]any{"status": model.OrgStatusDeleted, "is_enabled": 0, "version": o.Version + 1}
			action = "org:archive"
		}
		result := tx.WithContext(ctx).Model(&model.Organization{}).Where("org_id = ? AND version = ?", id, in.Version).Updates(values)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrOrganizationConflict
		}
		if err := scopedAudit(ctx, tx, actor, action, "org", id, "", ""); err != nil {
			return err
		}
		o, err = organizationRow(ctx, tx, id)
		if err != nil {
			return err
		}
		out = organizationDTO(o, c)
		out.IsMember, err = orgIsMember(ctx, tx, id, actor)
		if err != nil {
			return err
		}
		return current(ctx)
	})
	return &out, err
}

type membershipScope struct{ kind, id, orgID, protectedOwner string }

func scopedResource(ctx context.Context, tx *gorm.DB, kind, id string) (membershipScope, error) {
	s := membershipScope{kind: kind, id: id}
	if kind == "org" {
		o, err := organizationRow(ctx, tx, id)
		if err != nil {
			return s, err
		}
		if !organizationActive(o) {
			return s, ErrOrganizationConflict
		}
		s.orgID = id
		s.protectedOwner = o.OwnerUserID
		return s, nil
	}
	if kind != "project" {
		return s, ErrOrganizationInvalid
	}
	// Lock order for organization-managed project operations: org -> project -> users -> catalog.
	var candidate model.Project
	if err := tx.WithContext(ctx).Select("project_id", "org_id").Where("project_id = ?", id).Take(&candidate).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return s, ErrOrganizationNotFound
		}
		return s, err
	}
	if candidate.ProjectID != id {
		return s, ErrOrganizationNotFound
	}
	o, orgErr := organizationRow(ctx, tx, candidate.OrgID)
	if orgErr != nil && !errors.Is(orgErr, ErrOrganizationNotFound) {
		return s, orgErr
	}
	if orgErr == nil && !organizationActive(o) {
		return s, ErrOrganizationConflict
	}
	var rows []model.Project
	if err := scopedQuery(ctx, tx).Select("project_id", "org_id", "created_by", "is_enabled", "status").Where("project_id = ?", id).Limit(2).Find(&rows).Error; err != nil {
		return s, err
	}
	if len(rows) != 1 || rows[0].ProjectID != id {
		return s, ErrOrganizationNotFound
	}
	p := rows[0]
	if p.OrgID != candidate.OrgID || p.IsEnabled != 1 || p.Status != model.ProjectStatusActive {
		return s, ErrOrganizationConflict
	}
	s.orgID = p.OrgID
	s.protectedOwner = p.CreatedBy
	return s, nil
}

type scopedMembership struct {
	UserID, GrantID, RoleID string
	Version                 uint64
	Active                  bool
}

func scopedMemberships(ctx context.Context, tx *gorm.DB, s membershipScope) ([]scopedMembership, error) {
	out := []scopedMembership{}
	if s.kind == "org" {
		var rows []model.OrganizationMember
		err := scopedQuery(ctx, tx).Select("org_id", "user_id", "managed_grant_id", "managed_role_id", "version", "status").Where("org_id = ?", s.id).Order("user_id ASC").Find(&rows).Error
		for _, m := range rows {
			if m.OrgID != s.id {
				return nil, ErrOrganizationConflict
			}
			out = append(out, scopedMembership{m.UserID, m.ManagedGrantID, m.ManagedRoleID, m.Version, m.Status == model.OrgMemberStatusActive})
		}
		return out, err
	}
	var rows []model.ProjectMember
	err := scopedQuery(ctx, tx).Select("project_id", "user_id", "managed_grant_id", "managed_role_id", "version", "is_enabled").Where("project_id = ?", s.id).Order("user_id ASC").Find(&rows).Error
	for _, m := range rows {
		if m.ProjectID != s.id {
			return nil, ErrOrganizationConflict
		}
		out = append(out, scopedMembership{m.UserID, m.ManagedGrantID, m.ManagedRoleID, m.Version, m.IsEnabled == 1})
	}
	return out, err
}
func scopedMemberUser(ctx context.Context, tx *gorm.DB, user string) (model.User, error) {
	var rows []model.User
	err := scopedQuery(ctx, tx).Select("user_id", "username", "full_name", "is_enabled").Where("user_id = ?", user).Limit(2).Find(&rows).Error
	if err != nil {
		return model.User{}, err
	}
	if len(rows) != 1 || rows[0].UserID != user {
		return model.User{}, ErrOrganizationNotFound
	}
	return rows[0], nil
}
func managedGrant(ctx context.Context, tx *gorm.DB, s membershipScope, m scopedMembership) (*model.RoleGrant, error) {
	if m.GrantID == "" {
		return nil, nil
	}
	var rows []model.RoleGrant
	err := scopedQuery(ctx, tx).Select("grant_id", "subject_type", "subject_id", "scope_type", "scope_id", "role_id", "is_enabled", "expires_at").Where("grant_id = ?", m.GrantID).Limit(2).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 || rows[0].GrantID != m.GrantID || rows[0].SubjectType != "user" || rows[0].SubjectID != m.UserID || rows[0].ScopeType != s.kind || rows[0].ScopeID != s.id || rows[0].RoleID != m.RoleID {
		return nil, ErrOrganizationConflict
	}
	return &rows[0], nil
}
func memberDTO(ctx context.Context, tx *gorm.DB, s membershipScope, m scopedMembership) (model.ScopedMemberDetail, error) {
	dto := model.ScopedMemberDetail{UserID: m.UserID, Version: m.Version, HasOtherAccess: true, IsProtectedOwner: m.UserID == s.protectedOwner}
	if s.kind == "org" {
		dto.OrgID = s.id
	} else {
		dto.ProjectID = s.id
	}
	user, err := scopedMemberUser(ctx, tx, m.UserID)
	if err != nil {
		return dto, err
	}
	dto.Username = user.Username
	dto.DisplayName = user.FullName
	grant, err := managedGrant(ctx, tx, s, m)
	if err != nil {
		return dto, err
	}
	if grant != nil && grant.IsEnabled == 1 && (grant.ExpiresAt == nil || grant.ExpiresAt.After(time.Now())) {
		dto.RoleID = grant.RoleID
	}
	return dto, nil
}

// Members 列出显式成员及此流程拥有的角色，继承授权不被归入成员列表
func (r *OrganizationRepo) Members(ctx context.Context, actor, kind, id string, authorize ScopedAuthorize) (*model.ScopedMemberList, error) {
	out := &model.ScopedMemberList{List: []model.ScopedMemberDetail{}}
	err := r.transaction(ctx, func(tx *gorm.DB) error {
		s, err := scopedResource(ctx, tx, kind, id)
		if err != nil {
			return err
		}
		members, err := scopedMemberships(ctx, tx, s)
		if err != nil {
			return err
		}
		// Domain membership and user locks precede the permission catalog.
		users := []string{actor}
		for _, m := range members {
			if m.Active {
				users = append(users, m.UserID)
			}
		}
		if err := lockScopedUsers(ctx, tx, users); err != nil {
			return err
		}
		access, current, err := scopedAuth(ctx, tx, actor, authorize)
		if err != nil {
			return err
		}
		c, err := access(kind, id, s.orgID)
		if err != nil {
			return err
		}
		if !c.ManageMembers {
			return ErrOrganizationForbidden
		}
		out.CanManageMembers = true
		out.CanAssignOwner = c.AssignOwner && slices.Contains(c.AssignableRoleIDs, ownerRole(kind))
		out.AssignableRoleIDs = append([]string{}, c.AssignableRoleIDs...)
		for _, m := range members {
			if !m.Active {
				continue
			}
			dto, err := memberDTO(ctx, tx, s, m)
			if err != nil {
				return err
			}
			out.List = append(out.List, dto)
		}
		out.Total = len(out.List)
		return current(ctx)
	})
	return out, err
}
func lockScopedUsers(ctx context.Context, tx *gorm.DB, ids []string) error {
	var users []model.User
	return scopedQuery(ctx, tx).Select("user_id", "is_enabled").Where("user_id IN ?", ids).Order("user_id ASC").Find(&users).Error
}
func ownerRole(kind string) string {
	if kind == "org" {
		return consts.RoleOrgOwner
	}
	return consts.RoleProjectOwner
}
func countScopedOwners(ctx context.Context, tx *gorm.DB, s membershipScope, members []scopedMembership) (int, error) {
	n := 0
	for _, m := range members {
		if !m.Active {
			continue
		}
		g, err := managedGrant(ctx, tx, s, m)
		if err != nil {
			return 0, err
		}
		if g != nil && g.IsEnabled == 1 && g.RoleID == ownerRole(s.kind) && (g.ExpiresAt == nil || g.ExpiresAt.After(time.Now())) {
			user, err := scopedMemberUser(ctx, tx, m.UserID)
			if err != nil {
				return 0, err
			}
			if user.IsEnabled == 1 {
				n++
			}
		}
	}
	return n, nil
}

// ChangeMember 原子维护成员关联和专属内置角色；删除不会撤销其他直接或继承授权
func (r *OrganizationRepo) ChangeMember(ctx context.Context, actor, kind, id, operation string, in model.ScopedMemberInput, authorize ScopedAuthorize) (*model.ScopedMemberDetail, error) {
	var out model.ScopedMemberDetail
	err := r.transaction(ctx, func(tx *gorm.DB) error {
		s, err := scopedResource(ctx, tx, kind, id)
		if err != nil {
			return err
		}
		members, err := scopedMemberships(ctx, tx, s)
		if err != nil {
			return err
		}
		userIDs := []string{actor, in.UserID}
		for _, m := range members {
			if m.Active {
				userIDs = append(userIDs, m.UserID)
			}
		}
		if err := lockScopedUsers(ctx, tx, userIDs); err != nil {
			return err
		}
		access, current, err := scopedAuth(ctx, tx, actor, authorize)
		if err != nil {
			return err
		}
		c, err := access(kind, id, s.orgID)
		if err != nil {
			return err
		}
		if !c.ManageMembers {
			return ErrOrganizationForbidden
		}
		user, err := scopedMemberUser(ctx, tx, in.UserID)
		if err != nil {
			return err
		}
		if operation != "remove" && user.IsEnabled != 1 {
			return ErrOrganizationConflict
		}
		var existing *scopedMembership
		for i := range members {
			if members[i].UserID == in.UserID {
				if existing != nil {
					return ErrOrganizationConflict
				}
				existing = &members[i]
			}
		}
		if operation == "add" {
			if existing != nil && existing.Active {
				return ErrOrganizationConflict
			}
		} else if existing == nil || !existing.Active {
			return ErrOrganizationNotFound
		}
		if operation != "add" && (in.Version == 0 || existing.Version != in.Version) {
			return ErrOrganizationConflict
		}
		if operation != "remove" {
			if err := scopedRole(ctx, tx, kind, in.RoleID); err != nil {
				return err
			}
			if !slices.Contains(c.AssignableRoleIDs, in.RoleID) {
				return ErrOrganizationForbidden
			}
		}
		var oldGrant *model.RoleGrant
		if existing != nil {
			oldGrant, err = managedGrant(ctx, tx, s, *existing)
			if err != nil {
				return err
			}
		}
		if operation == "update" && oldGrant != nil && (oldGrant.IsEnabled != 1 || oldGrant.ExpiresAt != nil) {
			return ErrOrganizationConflict
		}
		wasOwner := oldGrant != nil && oldGrant.RoleID == ownerRole(kind) && oldGrant.IsEnabled == 1 && (oldGrant.ExpiresAt == nil || oldGrant.ExpiresAt.After(time.Now()))
		toOwner := operation != "remove" && in.RoleID == ownerRole(kind)
		if (wasOwner || toOwner) && !c.AssignOwner {
			return ErrOrganizationForbidden
		}
		// Immutable creation ownership is never transferred by changing member roles.
		if in.UserID == s.protectedOwner && (operation == "remove" || !toOwner) {
			return ErrOrganizationConflict
		}
		if wasOwner && user.IsEnabled == 1 && !toOwner {
			owners, err := countScopedOwners(ctx, tx, s, members)
			if err != nil {
				return err
			}
			if owners <= 1 {
				return ErrOrganizationConflict
			}
		}
		version := uint64(1)
		if existing != nil {
			if existing.Version == math.MaxUint64 {
				return ErrOrganizationConflict
			}
			version = existing.Version + 1
		}
		if oldGrant != nil && oldGrant.IsEnabled == 1 {
			result := tx.WithContext(ctx).Model(&model.RoleGrant{}).Where("grant_id = ? AND subject_type = ? AND subject_id = ? AND scope_type = ? AND scope_id = ? AND role_id = ? AND is_enabled = ?", oldGrant.GrantID, "user", in.UserID, kind, id, oldGrant.RoleID, 1).Update("is_enabled", 0)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrOrganizationConflict
			}
		}
		grantID := ""
		if operation != "remove" {
			grantID, err = scopedGrant(ctx, tx, actor, in.UserID, kind, id, in.RoleID)
			if err != nil {
				return err
			}
		}
		if kind == "org" {
			if existing == nil {
				err = tx.WithContext(ctx).Create(&model.OrganizationMember{OrgID: id, UserID: in.UserID, Username: user.Username, InvitedBy: actor, Status: model.OrgMemberStatusActive, Version: version, ManagedGrantID: grantID, ManagedRoleID: in.RoleID}).Error
			} else {
				status := model.OrgMemberStatusActive
				if operation == "remove" {
					status = model.OrgMemberStatusDisabled
				}
				result := tx.WithContext(ctx).Model(&model.OrganizationMember{}).Where("org_id = ? AND user_id = ? AND version = ?", id, in.UserID, existing.Version).Updates(map[string]any{"status": status, "version": version, "managed_grant_id": grantID, "managed_role_id": in.RoleID})
				err = result.Error
				if err == nil && result.RowsAffected != 1 {
					err = ErrOrganizationConflict
				}
			}
		} else {
			if existing == nil {
				err = tx.WithContext(ctx).Create(&model.ProjectMember{ProjectID: id, UserID: in.UserID, IsEnabled: 1, Version: version, ManagedGrantID: grantID, ManagedRoleID: in.RoleID}).Error
			} else {
				enabled := 1
				if operation == "remove" {
					enabled = 0
				}
				result := tx.WithContext(ctx).Model(&model.ProjectMember{}).Where("project_id = ? AND user_id = ? AND version = ?", id, in.UserID, existing.Version).Updates(map[string]any{"is_enabled": enabled, "version": version, "managed_grant_id": grantID, "managed_role_id": in.RoleID})
				err = result.Error
				if err == nil && result.RowsAffected != 1 {
					err = ErrOrganizationConflict
				}
			}
		}
		if err != nil {
			return err
		}
		if err := scopedAudit(ctx, tx, actor, kind+":member_"+operation, kind, id, in.UserID, in.RoleID); err != nil {
			return err
		}
		out, err = memberDTO(ctx, tx, s, scopedMembership{UserID: in.UserID, Version: version, GrantID: grantID, RoleID: in.RoleID, Active: operation != "remove"})
		if err != nil {
			return err
		}
		return current(ctx)
	})
	return &out, err
}

func createProjectInActiveOrganization(ctx context.Context, db *gorm.DB, p *model.Project) error {
	if p == nil {
		return ErrOrganizationInvalid
	}
	return db.WithContext(ctx).Clauses(database.Write()).Transaction(func(tx *gorm.DB) error {
		// Legacy isolated SQLite repository tests predate organization schema. Production always requires explicit migration.
		if tx.Dialector.Name() != "sqlite" || tx.Migrator().HasTable(&model.Organization{}) {
			o, err := organizationRow(ctx, tx, p.OrgID)
			if err != nil && !errors.Is(err, ErrOrganizationNotFound) {
				return err
			}
			if err == nil && !organizationActive(o) {
				return ErrOrganizationConflict
			}
		}
		return tx.WithContext(ctx).Create(p).Error
	})
}

func validateScopedBuiltinPermissions(ctx context.Context, tx *gorm.DB, role string) error {
	expected := map[string]consts.PermissionSeed{}
	allowed := map[string]bool{}
	for _, id := range consts.SeedBuiltinRolePermissions()[role] {
		allowed[id] = true
	}
	for _, p := range consts.SeedBuiltinPermissions() {
		if allowed[p.PermissionID] {
			expected[p.PermissionID] = p
		}
	}
	var bindings []model.RolePermissionBinding
	if err := scopedQuery(ctx, tx).Select("role_id", "permission_id").Where("role_id = ?", role).Order("permission_id ASC").Find(&bindings).Error; err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, binding := range bindings {
		if binding.RoleID != role || !allowed[binding.PermissionID] || seen[binding.PermissionID] {
			return ErrOrganizationConflict
		}
		seen[binding.PermissionID] = true
	}
	if len(seen) != len(expected) {
		return ErrOrganizationConflict
	}
	ids := []string{}
	for id := range expected {
		ids = append(ids, id)
	}
	var permissions []model.Permission
	if err := scopedQuery(ctx, tx).Select("permission_id", "resource_type", "action", "scope_type").Where("permission_id IN ?", ids).Order("permission_id ASC").Find(&permissions).Error; err != nil {
		return err
	}
	if len(permissions) != len(expected) {
		return ErrOrganizationConflict
	}
	for _, p := range permissions {
		seed, ok := expected[p.PermissionID]
		if !ok || p.ResourceType != seed.ResourceType || p.Action != seed.Action || p.ScopeType != string(seed.ScopeType) {
			return ErrOrganizationConflict
		}
	}
	return nil
}
