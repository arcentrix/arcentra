// Copyright 2025 Arcentra Authors.
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

package identity

import (
	"context"
	"encoding/json"
	"time"

	domain "github.com/arcentrix/arcentra/internal/domain/identity"
	"github.com/arcentrix/arcentra/pkg/store/cache"
	"github.com/arcentrix/arcentra/pkg/store/database"
	"github.com/arcentrix/arcentra/pkg/telemetry/log"
	"gorm.io/datatypes"
)

var userSelectFields = []string{
	"id", "user_id", "username", "full_name",
	"avatar", "email", "phone",
	"is_enabled", "is_super_admin",
	"created_at", "updated_at",
}

// ---------------------------------------------------------------------------
// UserRepo
// ---------------------------------------------------------------------------

var _ domain.IUserRepository = (*UserRepo)(nil)

const (
	userCacheKeyPrefix = "user:detail:"
	userCacheTTL       = 5 * time.Minute
)

type UserRepo struct {
	db    database.IDatabase
	cache cache.ICache
}

func NewUserRepo(db database.IDatabase, ch cache.ICache) *UserRepo {
	if ch == nil {
		log.Warnw("UserRepo initialized without cache, caching will be disabled")
	}
	return &UserRepo{db: db, cache: ch}
}

func (r *UserRepo) Create(ctx context.Context, user *domain.User) error {
	po := UserPOFromDomain(user)
	if err := r.db.Database().WithContext(ctx).Table(po.TableName()).Create(po).Error; err != nil {
		return err
	}
	user.ID = po.ID
	user.CreatedAt = po.CreatedAt
	user.UpdatedAt = po.UpdatedAt
	return nil
}

func (r *UserRepo) Get(ctx context.Context, userID string) (*domain.User, error) {
	po, err := r.getUserByIDCached(ctx, userID)
	if err != nil {
		return nil, err
	}
	return po.ToDomain(), nil
}

func (r *UserRepo) GetByUsername(ctx context.Context, username string) (*domain.User, error) {
	po, err := r.queryUserByField(ctx, "username", username)
	if err != nil {
		return nil, err
	}
	r.cacheUserPO(ctx, po)
	return po.ToDomain(), nil
}

func (r *UserRepo) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	po, err := r.queryUserByField(ctx, "email", email)
	if err != nil {
		return nil, err
	}
	r.cacheUserPO(ctx, po)
	return po.ToDomain(), nil
}

func (r *UserRepo) Update(ctx context.Context, userID string, updates map[string]any) error {
	if err := r.db.Database().WithContext(ctx).
		Table(UserPO{}.TableName()).
		Where("user_id = ?", userID).
		Updates(updates).Error; err != nil {
		return err
	}
	r.invalidateUserCache(ctx, userID)
	return nil
}

func (r *UserRepo) Delete(ctx context.Context, userID string) error {
	if err := r.db.Database().WithContext(ctx).
		Table(UserPO{}.TableName()).
		Where("user_id = ?", userID).
		Delete(&UserPO{}).Error; err != nil {
		return err
	}
	r.invalidateUserCache(ctx, userID)
	return nil
}

func (r *UserRepo) List(ctx context.Context, page, size int) ([]domain.User, int64, error) {
	var pos []UserPO
	var count int64
	tbl := UserPO{}.TableName()
	offset := (page - 1) * size

	if err := r.db.Database().WithContext(ctx).Table(tbl).Count(&count).Error; err != nil {
		return nil, 0, err
	}
	if err := r.db.Database().WithContext(ctx).
		Select(userSelectFields).
		Table(tbl).
		Offset(offset).Limit(size).
		Order("id DESC").
		Find(&pos).Error; err != nil {
		return nil, 0, err
	}

	users := make([]domain.User, len(pos))
	for i := range pos {
		users[i] = *pos[i].ToDomain()
	}
	return users, count, nil
}

func (r *UserRepo) GetPassword(ctx context.Context, userID string) (string, error) {
	var po UserPO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Select("password").
		Where("user_id = ?", userID).
		First(&po).Error; err != nil {
		return "", err
	}
	return po.Password, nil
}

func (r *UserRepo) ResetPassword(ctx context.Context, userID, passwordHash string) error {
	if err := r.db.Database().WithContext(ctx).
		Table(UserPO{}.TableName()).
		Where("user_id = ?", userID).
		Update("password", passwordHash).Error; err != nil {
		return err
	}
	r.invalidateUserCache(ctx, userID)
	return nil
}

// --- cache helpers ---

func userCacheKey(userID string) string {
	return userCacheKeyPrefix + userID
}

func (r *UserRepo) getUserByIDCached(ctx context.Context, userID string) (*UserPO, error) {
	keyFunc := func(params ...any) string {
		return userCacheKey(params[0].(string))
	}
	queryFunc := func(ctx context.Context) (*UserPO, error) {
		return r.queryUserByField(ctx, "user_id", userID)
	}
	cq := cache.NewCachedQuery(
		r.cache, keyFunc, queryFunc,
		cache.WithTTL[*UserPO](userCacheTTL),
		cache.WithLogPrefix[*UserPO]("[UserRepo]"),
	)
	return cq.Get(ctx, userID)
}

func (r *UserRepo) queryUserByField(ctx context.Context, field, value string) (*UserPO, error) {
	var po UserPO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Select(userSelectFields).
		Where(field+" = ?", value).
		First(&po).Error; err != nil {
		return nil, err
	}
	return &po, nil
}

func (r *UserRepo) cacheUserPO(ctx context.Context, po *UserPO) {
	if r.cache == nil || po == nil || po.UserID == "" {
		return
	}
	keyFunc := func(params ...any) string {
		return userCacheKey(params[0].(string))
	}
	cq := cache.NewCachedQuery[*UserPO](r.cache, keyFunc, nil,
		cache.WithTTL[*UserPO](userCacheTTL),
	)
	_, _ = cq.GetOrSet(ctx, func(_ context.Context) (*UserPO, error) {
		return po, nil
	}, po.UserID)
}

func (r *UserRepo) invalidateUserCache(ctx context.Context, userID string) {
	keyFunc := func(params ...any) string {
		return userCacheKey(params[0].(string))
	}
	cq := cache.NewCachedQuery[*UserPO](r.cache, keyFunc, nil)
	_ = cq.Invalidate(ctx, userID)
}

// ---------------------------------------------------------------------------
// UserExtRepo
// ---------------------------------------------------------------------------

var _ domain.IUserExtRepository = (*UserExtRepo)(nil)

type UserExtRepo struct {
	db database.IDatabase
}

func NewUserExtRepo(db database.IDatabase) *UserExtRepo {
	return &UserExtRepo{db: db}
}

func (r *UserExtRepo) Get(ctx context.Context, userID string) (*domain.UserExt, error) {
	var po UserExtPO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Where("user_id = ?", userID).
		First(&po).Error; err != nil {
		return nil, err
	}
	return po.ToDomain(), nil
}

func (r *UserExtRepo) Create(ctx context.Context, ext *domain.UserExt) error {
	po := UserExtPOFromDomain(ext)
	if err := r.db.Database().WithContext(ctx).Table(po.TableName()).Create(po).Error; err != nil {
		return err
	}
	ext.ID = po.ID
	ext.CreatedAt = po.CreatedAt
	ext.UpdatedAt = po.UpdatedAt
	return nil
}

func (r *UserExtRepo) Update(ctx context.Context, userID string, ext *domain.UserExt) error {
	po := UserExtPOFromDomain(ext)
	return r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Where("user_id = ?", userID).
		Updates(po).Error
}

func (r *UserExtRepo) UpdateLastLogin(ctx context.Context, userID string) error {
	now := time.Now()
	return r.db.Database().WithContext(ctx).
		Table(UserExtPO{}.TableName()).
		Where("user_id = ?", userID).
		Update("last_login_at", now).Error
}

func (r *UserExtRepo) UpdateTimezone(ctx context.Context, userID, timezone string) error {
	return r.db.Database().WithContext(ctx).
		Table(UserExtPO{}.TableName()).
		Where("user_id = ?", userID).
		Update("timezone", timezone).Error
}

func (r *UserExtRepo) UpdateInvitationStatus(ctx context.Context, userID string, status domain.InvitationStatus) error {
	return r.db.Database().WithContext(ctx).
		Table(UserExtPO{}.TableName()).
		Where("user_id = ?", userID).
		Update("invitation_status", string(status)).Error
}

func (r *UserExtRepo) Delete(ctx context.Context, userID string) error {
	return r.db.Database().WithContext(ctx).
		Table(UserExtPO{}.TableName()).
		Where("user_id = ?", userID).
		Delete(&UserExtPO{}).Error
}

func (r *UserExtRepo) Exists(ctx context.Context, userID string) (bool, error) {
	var count int64
	if err := r.db.Database().WithContext(ctx).
		Table(UserExtPO{}.TableName()).
		Where("user_id = ?", userID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// ---------------------------------------------------------------------------
// RoleRepo
// ---------------------------------------------------------------------------

var _ domain.IRoleRepository = (*RoleRepo)(nil)

type RoleRepo struct {
	db database.IDatabase
}

func NewRoleRepo(db database.IDatabase) *RoleRepo {
	return &RoleRepo{db: db}
}

func (r *RoleRepo) Create(ctx context.Context, role *domain.Role) error {
	po := RolePOFromDomain(role)
	if err := r.db.Database().WithContext(ctx).Table(po.TableName()).Create(po).Error; err != nil {
		return err
	}
	role.ID = po.ID
	role.CreatedAt = po.CreatedAt
	role.UpdatedAt = po.UpdatedAt
	return nil
}

func (r *RoleRepo) Get(ctx context.Context, roleID string) (*domain.Role, error) {
	var po RolePO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Where("role_id = ?", roleID).
		First(&po).Error; err != nil {
		return nil, err
	}
	return po.ToDomain(), nil
}

func (r *RoleRepo) BatchGet(ctx context.Context, roleIDs []string) ([]domain.Role, error) {
	var pos []RolePO
	if err := r.db.Database().WithContext(ctx).
		Table(RolePO{}.TableName()).
		Where("role_id IN ?", roleIDs).
		Find(&pos).Error; err != nil {
		return nil, err
	}
	roles := make([]domain.Role, len(pos))
	for i := range pos {
		roles[i] = *pos[i].ToDomain()
	}
	return roles, nil
}

func (r *RoleRepo) List(ctx context.Context, page, size int) ([]domain.Role, int64, error) {
	var pos []RolePO
	var count int64
	tbl := RolePO{}.TableName()
	offset := (page - 1) * size

	if err := r.db.Database().WithContext(ctx).Table(tbl).Count(&count).Error; err != nil {
		return nil, 0, err
	}
	if err := r.db.Database().WithContext(ctx).
		Table(tbl).
		Offset(offset).Limit(size).
		Order("id DESC").
		Find(&pos).Error; err != nil {
		return nil, 0, err
	}

	roles := make([]domain.Role, len(pos))
	for i := range pos {
		roles[i] = *pos[i].ToDomain()
	}
	return roles, count, nil
}

func (r *RoleRepo) Update(ctx context.Context, roleID string, updates map[string]any) error {
	return r.db.Database().WithContext(ctx).
		Table(RolePO{}.TableName()).
		Where("role_id = ?", roleID).
		Updates(updates).Error
}

func (r *RoleRepo) Delete(ctx context.Context, roleID string) error {
	return r.db.Database().WithContext(ctx).
		Table(RolePO{}.TableName()).
		Where("role_id = ?", roleID).
		Delete(&RolePO{}).Error
}

// ---------------------------------------------------------------------------
// MenuRepo
// ---------------------------------------------------------------------------

var _ domain.IMenuRepository = (*MenuRepo)(nil)

type MenuRepo struct {
	db database.IDatabase
}

func NewMenuRepo(db database.IDatabase) *MenuRepo {
	return &MenuRepo{db: db}
}

func (r *MenuRepo) Get(ctx context.Context, menuID string) (*domain.Menu, error) {
	var po MenuPO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Where("menu_id = ?", menuID).
		First(&po).Error; err != nil {
		return nil, err
	}
	return po.ToDomain(), nil
}

func (r *MenuRepo) BatchGet(ctx context.Context, menuIDs []string) ([]domain.Menu, error) {
	var pos []MenuPO
	if err := r.db.Database().WithContext(ctx).
		Table(MenuPO{}.TableName()).
		Where("menu_id IN ?", menuIDs).
		Find(&pos).Error; err != nil {
		return nil, err
	}
	menus := make([]domain.Menu, len(pos))
	for i := range pos {
		menus[i] = *pos[i].ToDomain()
	}
	return menus, nil
}

func (r *MenuRepo) List(ctx context.Context) ([]domain.Menu, error) {
	var pos []MenuPO
	if err := r.db.Database().WithContext(ctx).
		Table(MenuPO{}.TableName()).
		Order("`order` ASC").
		Find(&pos).Error; err != nil {
		return nil, err
	}
	menus := make([]domain.Menu, len(pos))
	for i := range pos {
		menus[i] = *pos[i].ToDomain()
	}
	return menus, nil
}

func (r *MenuRepo) ListByParent(ctx context.Context, parentID string) ([]domain.Menu, error) {
	var pos []MenuPO
	if err := r.db.Database().WithContext(ctx).
		Table(MenuPO{}.TableName()).
		Where("parent_id = ?", parentID).
		Order("`order` ASC").
		Find(&pos).Error; err != nil {
		return nil, err
	}
	menus := make([]domain.Menu, len(pos))
	for i := range pos {
		menus[i] = *pos[i].ToDomain()
	}
	return menus, nil
}

// ---------------------------------------------------------------------------
// TeamRepo
// ---------------------------------------------------------------------------

var _ domain.ITeamRepository = (*TeamRepo)(nil)

type TeamRepo struct {
	db database.IDatabase
}

func NewTeamRepo(db database.IDatabase) *TeamRepo {
	return &TeamRepo{db: db}
}

func (r *TeamRepo) Create(ctx context.Context, team *domain.Team) error {
	po := TeamPOFromDomain(team)
	if err := r.db.Database().WithContext(ctx).Table(po.TableName()).Create(po).Error; err != nil {
		return err
	}
	team.ID = po.ID
	team.CreatedAt = po.CreatedAt
	team.UpdatedAt = po.UpdatedAt
	return nil
}

func (r *TeamRepo) Get(ctx context.Context, teamID string) (*domain.Team, error) {
	var po TeamPO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Where("team_id = ?", teamID).
		First(&po).Error; err != nil {
		return nil, err
	}
	return po.ToDomain(), nil
}

func (r *TeamRepo) GetByName(ctx context.Context, orgID, name string) (*domain.Team, error) {
	var po TeamPO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Where("org_id = ? AND name = ?", orgID, name).
		First(&po).Error; err != nil {
		return nil, err
	}
	return po.ToDomain(), nil
}

func (r *TeamRepo) Update(ctx context.Context, teamID string, updates map[string]any) error {
	return r.db.Database().WithContext(ctx).
		Table(TeamPO{}.TableName()).
		Where("team_id = ?", teamID).
		Updates(updates).Error
}

func (r *TeamRepo) Delete(ctx context.Context, teamID string) error {
	return r.db.Database().WithContext(ctx).
		Table(TeamPO{}.TableName()).
		Where("team_id = ?", teamID).
		Delete(&TeamPO{}).Error
}

func (r *TeamRepo) List(ctx context.Context, orgID string, page, size int) ([]*domain.Team, int64, error) {
	var pos []TeamPO
	var count int64
	tbl := TeamPO{}.TableName()
	offset := (page - 1) * size

	q := r.db.Database().WithContext(ctx).Table(tbl).Where("org_id = ?", orgID)

	if err := q.Count(&count).Error; err != nil {
		return nil, 0, err
	}
	if err := q.Offset(offset).Limit(size).Order("id DESC").Find(&pos).Error; err != nil {
		return nil, 0, err
	}

	teams := make([]*domain.Team, len(pos))
	for i := range pos {
		teams[i] = pos[i].ToDomain()
	}
	return teams, count, nil
}

func (r *TeamRepo) ListByOrg(ctx context.Context, orgID string) ([]*domain.Team, error) {
	var pos []TeamPO
	if err := r.db.Database().WithContext(ctx).
		Table(TeamPO{}.TableName()).
		Where("org_id = ?", orgID).
		Find(&pos).Error; err != nil {
		return nil, err
	}
	teams := make([]*domain.Team, len(pos))
	for i := range pos {
		teams[i] = pos[i].ToDomain()
	}
	return teams, nil
}

func (r *TeamRepo) ListSubTeams(ctx context.Context, parentTeamID string) ([]*domain.Team, error) {
	var pos []TeamPO
	if err := r.db.Database().WithContext(ctx).
		Table(TeamPO{}.TableName()).
		Where("parent_team_id = ?", parentTeamID).
		Find(&pos).Error; err != nil {
		return nil, err
	}
	teams := make([]*domain.Team, len(pos))
	for i := range pos {
		teams[i] = pos[i].ToDomain()
	}
	return teams, nil
}

func (r *TeamRepo) ListByUser(ctx context.Context, userID string) ([]*domain.Team, error) {
	var pos []TeamPO
	subQuery := r.db.Database().WithContext(ctx).
		Table(TeamMemberPO{}.TableName()).
		Select("team_id").
		Where("user_id = ?", userID)

	if err := r.db.Database().WithContext(ctx).
		Table(TeamPO{}.TableName()).
		Where("team_id IN (?)", subQuery).
		Find(&pos).Error; err != nil {
		return nil, err
	}
	teams := make([]*domain.Team, len(pos))
	for i := range pos {
		teams[i] = pos[i].ToDomain()
	}
	return teams, nil
}

func (r *TeamRepo) BatchGet(ctx context.Context, teamIDs []string) ([]*domain.Team, error) {
	var pos []TeamPO
	if err := r.db.Database().WithContext(ctx).
		Table(TeamPO{}.TableName()).
		Where("team_id IN ?", teamIDs).
		Find(&pos).Error; err != nil {
		return nil, err
	}
	teams := make([]*domain.Team, len(pos))
	for i := range pos {
		teams[i] = pos[i].ToDomain()
	}
	return teams, nil
}

func (r *TeamRepo) Exists(ctx context.Context, teamID string) (bool, error) {
	var count int64
	if err := r.db.Database().WithContext(ctx).
		Table(TeamPO{}.TableName()).
		Where("team_id = ?", teamID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *TeamRepo) NameExists(ctx context.Context, orgID, name string, excludeTeamID ...string) (bool, error) {
	var count int64
	q := r.db.Database().WithContext(ctx).
		Table(TeamPO{}.TableName()).
		Where("org_id = ? AND name = ?", orgID, name)
	if len(excludeTeamID) > 0 && excludeTeamID[0] != "" {
		q = q.Where("team_id != ?", excludeTeamID[0])
	}
	if err := q.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *TeamRepo) UpdatePath(ctx context.Context, teamID, path string, level int) error {
	return r.db.Database().WithContext(ctx).
		Table(TeamPO{}.TableName()).
		Where("team_id = ?", teamID).
		Updates(map[string]any{"path": path, "level": level}).Error
}

func (r *TeamRepo) IncrementMembers(ctx context.Context, teamID string, delta int) error {
	return r.db.Database().WithContext(ctx).
		Table(TeamPO{}.TableName()).
		Where("team_id = ?", teamID).
		UpdateColumn("total_members", r.db.Database().Raw("total_members + ?", delta)).Error
}

func (r *TeamRepo) IncrementProjects(ctx context.Context, teamID string, delta int) error {
	return r.db.Database().WithContext(ctx).
		Table(TeamPO{}.TableName()).
		Where("team_id = ?", teamID).
		UpdateColumn("total_projects", r.db.Database().Raw("total_projects + ?", delta)).Error
}

// ---------------------------------------------------------------------------
// TeamMemberRepo
// ---------------------------------------------------------------------------

var _ domain.ITeamMemberRepository = (*TeamMemberRepo)(nil)

type TeamMemberRepo struct {
	db database.IDatabase
}

func NewTeamMemberRepo(db database.IDatabase) *TeamMemberRepo {
	return &TeamMemberRepo{db: db}
}

func (r *TeamMemberRepo) Get(ctx context.Context, teamID, userID string) (*domain.TeamMember, error) {
	var po TeamMemberPO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Where("team_id = ? AND user_id = ?", teamID, userID).
		First(&po).Error; err != nil {
		return nil, err
	}
	return po.ToDomain(), nil
}

func (r *TeamMemberRepo) ListByTeam(ctx context.Context, teamID string) ([]domain.TeamMember, error) {
	var pos []TeamMemberPO
	if err := r.db.Database().WithContext(ctx).
		Table(TeamMemberPO{}.TableName()).
		Where("team_id = ?", teamID).
		Find(&pos).Error; err != nil {
		return nil, err
	}
	members := make([]domain.TeamMember, len(pos))
	for i := range pos {
		members[i] = *pos[i].ToDomain()
	}
	return members, nil
}

func (r *TeamMemberRepo) ListByUser(ctx context.Context, userID string) ([]domain.TeamMember, error) {
	var pos []TeamMemberPO
	if err := r.db.Database().WithContext(ctx).
		Table(TeamMemberPO{}.TableName()).
		Where("user_id = ?", userID).
		Find(&pos).Error; err != nil {
		return nil, err
	}
	members := make([]domain.TeamMember, len(pos))
	for i := range pos {
		members[i] = *pos[i].ToDomain()
	}
	return members, nil
}

func (r *TeamMemberRepo) Add(ctx context.Context, member *domain.TeamMember) error {
	po := TeamMemberPOFromDomain(member)
	if err := r.db.Database().WithContext(ctx).Table(po.TableName()).Create(po).Error; err != nil {
		return err
	}
	member.ID = po.ID
	member.CreatedAt = po.CreatedAt
	member.UpdatedAt = po.UpdatedAt
	return nil
}

func (r *TeamMemberRepo) UpdateRole(ctx context.Context, teamID, userID, roleID string) error {
	return r.db.Database().WithContext(ctx).
		Table(TeamMemberPO{}.TableName()).
		Where("team_id = ? AND user_id = ?", teamID, userID).
		Update("role_id", roleID).Error
}

func (r *TeamMemberRepo) Remove(ctx context.Context, teamID, userID string) error {
	return r.db.Database().WithContext(ctx).
		Table(TeamMemberPO{}.TableName()).
		Where("team_id = ? AND user_id = ?", teamID, userID).
		Delete(&TeamMemberPO{}).Error
}

// ---------------------------------------------------------------------------
// UserRoleBindingRepo
// ---------------------------------------------------------------------------

var _ domain.IUserRoleBindingRepository = (*UserRoleBindingRepo)(nil)

type UserRoleBindingRepo struct {
	db database.IDatabase
}

func NewUserRoleBindingRepo(db database.IDatabase) *UserRoleBindingRepo {
	return &UserRoleBindingRepo{db: db}
}

func (r *UserRoleBindingRepo) List(ctx context.Context, userID string) ([]domain.UserRoleBinding, error) {
	var pos []UserRoleBindingPO
	if err := r.db.Database().WithContext(ctx).
		Table(UserRoleBindingPO{}.TableName()).
		Where("user_id = ?", userID).
		Find(&pos).Error; err != nil {
		return nil, err
	}
	bindings := make([]domain.UserRoleBinding, len(pos))
	for i := range pos {
		bindings[i] = *pos[i].ToDomain()
	}
	return bindings, nil
}

func (r *UserRoleBindingRepo) GetByRole(ctx context.Context, userID, roleID string) (*domain.UserRoleBinding, error) {
	var po UserRoleBindingPO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Where("user_id = ? AND role_id = ?", userID, roleID).
		First(&po).Error; err != nil {
		return nil, err
	}
	return po.ToDomain(), nil
}

func (r *UserRoleBindingRepo) Create(ctx context.Context, binding *domain.UserRoleBinding) error {
	po := UserRoleBindingPOFromDomain(binding)
	if err := r.db.Database().WithContext(ctx).Table(po.TableName()).Create(po).Error; err != nil {
		return err
	}
	binding.ID = po.ID
	binding.CreatedAt = po.CreatedAt
	binding.UpdatedAt = po.UpdatedAt
	return nil
}

func (r *UserRoleBindingRepo) Delete(ctx context.Context, bindingID string) error {
	return r.db.Database().WithContext(ctx).
		Table(UserRoleBindingPO{}.TableName()).
		Where("binding_id = ?", bindingID).
		Delete(&UserRoleBindingPO{}).Error
}

func (r *UserRoleBindingRepo) DeleteByUser(ctx context.Context, userID string) error {
	return r.db.Database().WithContext(ctx).
		Table(UserRoleBindingPO{}.TableName()).
		Where("user_id = ?", userID).
		Delete(&UserRoleBindingPO{}).Error
}

// ---------------------------------------------------------------------------
// RoleMenuBindingRepo
// ---------------------------------------------------------------------------

var _ domain.IRoleMenuBindingRepository = (*RoleMenuBindingRepo)(nil)

type RoleMenuBindingRepo struct {
	db database.IDatabase
}

func NewRoleMenuBindingRepo(db database.IDatabase) *RoleMenuBindingRepo {
	return &RoleMenuBindingRepo{db: db}
}

func (r *RoleMenuBindingRepo) List(ctx context.Context, roleID string) ([]domain.RoleMenuBinding, error) {
	var pos []RoleMenuBindingPO
	if err := r.db.Database().WithContext(ctx).
		Table(RoleMenuBindingPO{}.TableName()).
		Where("role_id = ?", roleID).
		Find(&pos).Error; err != nil {
		return nil, err
	}
	bindings := make([]domain.RoleMenuBinding, len(pos))
	for i := range pos {
		bindings[i] = *pos[i].ToDomain()
	}
	return bindings, nil
}

func (r *RoleMenuBindingRepo) ListByResource(ctx context.Context, roleID, resourceID string) ([]domain.RoleMenuBinding, error) {
	var pos []RoleMenuBindingPO
	if err := r.db.Database().WithContext(ctx).
		Table(RoleMenuBindingPO{}.TableName()).
		Where("role_id = ? AND resource_id = ?", roleID, resourceID).
		Find(&pos).Error; err != nil {
		return nil, err
	}
	bindings := make([]domain.RoleMenuBinding, len(pos))
	for i := range pos {
		bindings[i] = *pos[i].ToDomain()
	}
	return bindings, nil
}

func (r *RoleMenuBindingRepo) ListByRoles(ctx context.Context, roleIDs []string, resourceID string) ([]domain.RoleMenuBinding, error) {
	var pos []RoleMenuBindingPO
	q := r.db.Database().WithContext(ctx).
		Table(RoleMenuBindingPO{}.TableName()).
		Where("role_id IN ?", roleIDs)
	if resourceID != "" {
		q = q.Where("resource_id = ?", resourceID)
	}
	if err := q.Find(&pos).Error; err != nil {
		return nil, err
	}
	bindings := make([]domain.RoleMenuBinding, len(pos))
	for i := range pos {
		bindings[i] = *pos[i].ToDomain()
	}
	return bindings, nil
}

func (r *RoleMenuBindingRepo) Create(ctx context.Context, binding *domain.RoleMenuBinding) error {
	po := RoleMenuBindingPOFromDomain(binding)
	if err := r.db.Database().WithContext(ctx).Table(po.TableName()).Create(po).Error; err != nil {
		return err
	}
	binding.ID = po.ID
	binding.CreatedAt = po.CreatedAt
	binding.UpdatedAt = po.UpdatedAt
	return nil
}

func (r *RoleMenuBindingRepo) Delete(ctx context.Context, roleMenuID string) error {
	return r.db.Database().WithContext(ctx).
		Table(RoleMenuBindingPO{}.TableName()).
		Where("role_menu_id = ?", roleMenuID).
		Delete(&RoleMenuBindingPO{}).Error
}

// ---------------------------------------------------------------------------
// IdentityProviderRepo
// ---------------------------------------------------------------------------

var _ domain.IIdentityProviderRepository = (*IdentityProviderRepo)(nil)

type IdentityProviderRepo struct {
	db database.IDatabase
}

func NewIdentityProviderRepo(db database.IDatabase) *IdentityProviderRepo {
	return &IdentityProviderRepo{db: db}
}

func (r *IdentityProviderRepo) Get(ctx context.Context, name string) (*domain.IdentityProvider, error) {
	var po IdentityProviderPO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Where("name = ?", name).
		First(&po).Error; err != nil {
		return nil, err
	}
	return po.ToDomain(), nil
}

func (r *IdentityProviderRepo) GetByType(ctx context.Context, providerType domain.ProviderType) ([]domain.IdentityProvider, error) {
	var pos []IdentityProviderPO
	if err := r.db.Database().WithContext(ctx).
		Table(IdentityProviderPO{}.TableName()).
		Where("provider_type = ?", string(providerType)).
		Find(&pos).Error; err != nil {
		return nil, err
	}
	providers := make([]domain.IdentityProvider, len(pos))
	for i := range pos {
		providers[i] = *pos[i].ToDomain()
	}
	return providers, nil
}

func (r *IdentityProviderRepo) List(ctx context.Context) ([]domain.IdentityProvider, error) {
	var pos []IdentityProviderPO
	if err := r.db.Database().WithContext(ctx).
		Table(IdentityProviderPO{}.TableName()).
		Order("priority ASC").
		Find(&pos).Error; err != nil {
		return nil, err
	}
	providers := make([]domain.IdentityProvider, len(pos))
	for i := range pos {
		providers[i] = *pos[i].ToDomain()
	}
	return providers, nil
}

func (r *IdentityProviderRepo) ListTypes(ctx context.Context) ([]string, error) {
	var types []string
	if err := r.db.Database().WithContext(ctx).
		Table(IdentityProviderPO{}.TableName()).
		Distinct("provider_type").
		Pluck("provider_type", &types).Error; err != nil {
		return nil, err
	}
	return types, nil
}

func (r *IdentityProviderRepo) Create(ctx context.Context, provider *domain.IdentityProvider) error {
	po := IdentityProviderPOFromDomain(provider)
	if err := r.db.Database().WithContext(ctx).Table(po.TableName()).Create(po).Error; err != nil {
		return err
	}
	provider.ID = po.ID
	provider.CreatedAt = po.CreatedAt
	provider.UpdatedAt = po.UpdatedAt
	return nil
}

func (r *IdentityProviderRepo) Update(ctx context.Context, name string, provider *domain.IdentityProvider) error {
	po := IdentityProviderPOFromDomain(provider)
	return r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Where("name = ?", name).
		Updates(po).Error
}

func (r *IdentityProviderRepo) Delete(ctx context.Context, name string) error {
	return r.db.Database().WithContext(ctx).
		Table(IdentityProviderPO{}.TableName()).
		Where("name = ?", name).
		Delete(&IdentityProviderPO{}).Error
}

func (r *IdentityProviderRepo) Exists(ctx context.Context, name string) (bool, error) {
	var count int64
	if err := r.db.Database().WithContext(ctx).
		Table(IdentityProviderPO{}.TableName()).
		Where("name = ?", name).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *IdentityProviderRepo) Toggle(ctx context.Context, name string) error {
	return r.db.Database().WithContext(ctx).
		Table(IdentityProviderPO{}.TableName()).
		Where("name = ?", name).
		UpdateColumn("is_enabled", r.db.Database().Raw("1 - is_enabled")).Error
}

// suppress unused import warnings
var (
	_ = (*datatypes.JSON)(nil)
	_ = json.RawMessage(nil)
	_ = time.Now
)
