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

package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/internal/engine/consts"
	usermodel "github.com/arcentrix/arcentra/internal/engine/model"
	userrepo "github.com/arcentrix/arcentra/internal/engine/repo"
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/arcentrix/arcentra/pkg/util"
	"golang.org/x/crypto/bcrypt"
)

type LoginService interface {
	Login(login *usermodel.Login, auth http.Auth) (*usermodel.LoginResp, error)
}

type UserService struct {
	cache               cache.ICache
	userRepo            userrepo.IUserRepository
	userExtRepo         userrepo.IUserExtRepository
	userRoleBindingRepo userrepo.IUserRoleBindingRepository
	roleMenuBindingRepo userrepo.IRoleMenuBindingRepository
	menuRepo            userrepo.IMenuRepository
	roleRepo            userrepo.IRoleRepository
	menuService         *MenuService
}

func NewUserService(
	cache cache.ICache,
	userRepo userrepo.IUserRepository,
	userExtRepo userrepo.IUserExtRepository,
	userRoleBindingRepo userrepo.IUserRoleBindingRepository,
	roleMenuBindingRepo userrepo.IRoleMenuBindingRepository,
	menuRepo userrepo.IMenuRepository,
	roleRepo userrepo.IRoleRepository,
	menuService *MenuService,
) *UserService {
	return &UserService{
		cache:               cache,
		userRepo:            userRepo,
		userExtRepo:         userExtRepo,
		userRoleBindingRepo: userRoleBindingRepo,
		roleMenuBindingRepo: roleMenuBindingRepo,
		menuRepo:            menuRepo,
		roleRepo:            roleRepo,
		menuService:         menuService,
	}
}

func (ul *UserService) Login(login *usermodel.Login, auth http.Auth) (*usermodel.LoginResp, error) {
	userInfo, err := ul.userRepo.Login(login)
	if err != nil {
		log.Errorw("login failed", "username", login.Username, "email", login.Email, "error", err)
		return nil, err
	}
	if userInfo == nil || userInfo.Username == "" {
		log.Error("userInfo not found")
		return nil, errors.New(http.UserNotExist.Msg)
	}
	// 验证用户名或邮箱匹配（允许使用邮箱登录，或 OAuth 登录时密码为空）
	if login.Username != "" && userInfo.Username != login.Username {
		log.Errorw("username mismatch", "expected", login.Username, "got", userInfo.Username)
		return nil, errors.New(http.UserNotExist.Msg)
	}

	// 如果提供了密码，则进行密码验证（用于普通登录）
	// 如果密码为空，则跳过密码验证（用于 OAuth 登录）
	if login.Password != "" {
		pwd, decodeErr := base64.StdEncoding.DecodeString(login.Password)
		if decodeErr != nil {
			log.Errorw("failed to decode password", "error", decodeErr)
			return nil, errors.New(http.UserIncorrectPassword.Msg)
		}

		// compare stored password hash with provided password
		if !comparePassword(userInfo.Password, string(pwd)) {
			log.Error("incorrect password provided")
			return nil, errors.New(http.UserIncorrectPassword.Msg)
		}
	}

	aToken, rToken, err := jwt.GenToken(userInfo.UserId, []byte(auth.SecretKey), auth.AccessExpire, auth.RefreshExpire)
	if err != nil {
		log.Errorw("failed to generate tokens", "userId", userInfo.UserId, "error", err)
		return nil, err
	}

	now := time.Now()
	createAt := now.Unix()
	expireAt := now.Add(auth.AccessExpire).Unix()

	// 获取用户的角色信息和路由信息
	roles, routes, err := ul.GetUserRolesAndRoutes(userInfo.UserId, "")
	if err != nil {
		log.Warnw("failed to get user roles and routes", "userId", userInfo.UserId, "error", err)
		// 如果获取失败，返回空数组，不影响登录流程
		roles = []usermodel.RoleDTO{}
		routes = []string{}
	}

	resp := &usermodel.LoginResp{
		UserInfo: usermodel.UserInfo{
			UserId:   userInfo.UserId,
			Username: userInfo.Username,
			FullName: userInfo.FullName,
			Avatar:   userInfo.Avatar,
			Email:    userInfo.Email,
			Phone:    userInfo.Phone,
		},
		Token: map[string]string{
			"accessToken":  aToken,
			"refreshToken": rToken,
			"expireAt":     fmt.Sprintf("%d", expireAt),
			"createAt":     fmt.Sprintf("%d", createAt),
		},
		Role:     roles,
		Routes:   routes,
		ExpireAt: expireAt,
		CreateAt: createAt,
	}

	// Persist token/user cache synchronously.
	// Authorization middleware depends on Redis token info; making this async can cause
	// "token expired" responses immediately after login due to race conditions.
	if err = ul.userRepo.SetLoginRespInfo(auth, resp); err != nil {
		log.Errorw("failed to set login response info", "userId", userInfo.UserId, "error", err)
		return nil, err
	}

	// update last login time in userInfo ext (non-critical)
	safe.Go(func() {
		if ul.userExtRepo != nil {
			if err := ul.userExtRepo.UpdateLastLogin(userInfo.UserId); err != nil {
				log.Warnw("failed to update last login time", "userId", userInfo.UserId, "error", err)
			}
		}
	})

	return resp, nil
}

func (ul *UserService) Refresh(userId, rToken string, auth *http.Auth) (map[string]string, error) {
	token, err := jwt.RefreshToken(auth, userId, rToken)
	if err != nil {
		log.Errorw("failed to refresh token", "userId", userId, "error", err)
		return nil, err
	}

	// calculate token expiration time
	expireAt := time.Now().Add(auth.AccessExpire).Unix()
	token["expireAt"] = fmt.Sprintf("%d", expireAt)

	_, err = ul.userRepo.SetToken(userId, token["accessToken"], *auth)
	if err != nil {
		log.Errorw("failed to set token in Redis", "userId", userId, "error", err)
		return token, err
	}

	if _, err := ul.userRepo.SetRefreshToken(userId, token["refreshToken"], *auth); err != nil {
		log.Errorw("failed to set refresh token in Redis", "userId", userId, "error", err)
		return token, err
	}

	return token, nil
}

func (ul *UserService) Register(register *usermodel.Register) error {
	var err error
	register.UserId = id.GetUUIDWithoutDashes()
	// set default values if not provided
	if register.FullName == "" {
		register.FullName = register.Username
	}
	register.CreatedAt = time.Now()
	password, err := getPassword(register.Password)
	if err != nil {
		return err
	}
	register.Password = string(password)
	if err = ul.userRepo.Register(register); err != nil {
		return err
	}

	// 创建 UserExt 记录（替代数据库触发器）
	err = ul.createUserExtIfNotExists(register.UserId)
	if err != nil {
		log.Errorw("failed to create user ext after registration", "username", register.Username, "userId", register.UserId, "error", err)
		// 不返回错误，因为用户已经创建成功，UserExt 可以在后续操作中创建
	}

	return nil
}

func (ul *UserService) Logout(userId string) error {
	// delete token
	tokenKey := consts.UserTokenKey + userId
	if err := ul.userRepo.DelToken(tokenKey); err != nil {
		log.Errorw("failed to delete token", "userId", userId, "error", err)
		return errors.New(http.TokenBeEmpty.Msg)
	}

	refreshTokenKey := consts.UserRefreshTokenKey + userId
	if err := ul.userRepo.DelToken(refreshTokenKey); err != nil {
		log.Errorw("failed to delete refresh token", "userId", userId, "error", err)
	}

	// delete user info cache
	userInfoKey := consts.UserInfoKey + userId
	if err := ul.userRepo.DelToken(userInfoKey); err != nil {
		log.Errorw("failed to delete user info", "userId", userId, "error", err)
		// user info deletion failure does not affect logout
	}

	return nil
}

// createUserExtIfNotExists creates UserExt record if it doesn't exist
// This replaces the database trigger that was inserting into t_user_extension
func (ul *UserService) createUserExtIfNotExists(userId string) error {
	exists, err := ul.userExtRepo.Exists(userId)
	if err != nil {
		return fmt.Errorf("failed to check user ext exists: %w", err)
	}
	if exists {
		return nil // already exists, no need to create
	}

	// Create UserExt record with default values (matching the database trigger)
	now := time.Now()
	userExt := &usermodel.UserExt{
		UserId:           userId,
		Timezone:         "UTC",
		InvitationStatus: usermodel.UserInvitationStatusAccepted,
	}
	userExt.CreatedAt = now
	userExt.UpdatedAt = now

	if err := ul.userExtRepo.Create(userExt); err != nil {
		return fmt.Errorf("failed to create user ext: %w", err)
	}

	return nil
}

func (ul *UserService) AddUser(addUserReq usermodel.AddUserReq) error {
	var err error
	addUserReq.UserId = id.GetUUIDWithoutDashes()
	addUserReq.IsEnabled = 1
	addUserReq.CreatedAt = time.Now()
	if err = ul.userRepo.AddUser(&addUserReq); err != nil {
		return err
	}

	// 创建 UserExt 记录（替代数据库触发器）
	err = ul.createUserExtIfNotExists(addUserReq.UserId)
	if err != nil {
		log.Errorw("failed to create user ext after adding user", "username", addUserReq.Username, "userId", addUserReq.UserId, "error", err)
		// 不返回错误，因为用户已经创建成功，UserExt 可以在后续操作中创建
	}

	return nil
}

func (ul *UserService) UpdateUser(userId string, updateReq *usermodel.UpdateUserReq) error {
	// Check if user exists
	_, err := ul.userRepo.GetUserByUserId(userId)
	if err != nil {
		log.Errorw("get user by userId failed", "userId", userId, "error", err)
		return err
	}

	// Build and update User fields
	updates := buildUserUpdateMap(updateReq)
	if len(updates) > 0 {
		updates["updated_at"] = time.Now()
		if err := ul.userRepo.UpdateUser(userId, updates); err != nil {
			log.Errorw("update user failed", "userId", userId, "error", err)
			return err
		}
	}

	// clear user info cache after update
	userInfoKey := consts.UserInfoKey + userId
	if err := ul.userRepo.DelToken(userInfoKey); err != nil {
		log.Warnw("failed to clear user info cache", "userId", userId, "error", err)
	}

	return nil
}

// buildUserUpdateMap builds update map for User fields
func buildUserUpdateMap(req *usermodel.UpdateUserReq) map[string]any {
	updates := make(map[string]any)
	util.SetIfNotNil(updates, "full_name", req.FullName)
	util.SetIfNotNil(updates, "avatar", req.Avatar)
	util.SetIfNotNil(updates, "email", req.Email)
	util.SetIfNotNil(updates, "phone", req.Phone)
	util.SetIfNotNil(updates, "is_enabled", req.IsEnabled)
	return updates
}

func (ul *UserService) FetchUserInfo(userId string) (*usermodel.UserInfo, error) {
	userInfo, err := ul.userRepo.FetchUserInfo(userId)
	if err != nil {
		return nil, err
	}

	return userInfo, err
}

func (ul *UserService) GetUserList(pageNum, pageSize int) ([]userrepo.UserWithExt, int64, error) {
	// set default values
	if pageNum <= 0 {
		pageNum = 1
	}
	if pageSize <= 0 {
		pageSize = 10
	}

	offset := (pageNum - 1) * pageSize
	users, count, err := ul.userRepo.GetUserList(offset, pageSize)
	if err != nil {
		return nil, 0, err
	}

	return users, count, err
}

func (ul *UserService) GetUsersByRole(roleId, roleName string, pageNum, pageSize int) ([]userrepo.UserWithExt, int64, error) {
	// set default values
	if pageNum <= 0 {
		pageNum = 1
	}
	if pageSize <= 0 {
		pageSize = 10
	}

	offset := (pageNum - 1) * pageSize
	users, count, err := ul.userRepo.GetUsersByRole(roleId, roleName, offset, pageSize)
	if err != nil {
		log.Errorw("get users by role failed", "roleId", roleId, "roleName", roleName, "error", err)
		return nil, 0, err
	}

	return users, count, err
}

func comparePassword(oldPassword, newPassword string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(oldPassword), []byte(newPassword))
	if err != nil {
		return false
	} else {
		return true
	}
}

// ResetPassword resets user password (for forgot password scenario, no old password required)
func (ul *UserService) ResetPassword(userId string, req *usermodel.ResetPasswordReq) error {
	// decode new password from base64
	newPwd, err := base64.StdEncoding.DecodeString(req.NewPassword)
	if err != nil {
		log.Errorw("failed to decode new password", "userId", userId, "error", err)
		return errors.New("invalid new password format")
	}

	// validate new password length
	if len(newPwd) < 6 {
		return errors.New("new password must be at least 6 characters")
	}

	// hash new password
	newPasswordHash, err := getPassword(string(newPwd))
	if err != nil {
		log.Errorw("failed to hash new password", "userId", userId, "error", err)
		return errors.New("failed to process new password")
	}

	// update password
	if err := ul.userRepo.ResetPassword(userId, string(newPasswordHash)); err != nil {
		log.Errorw("failed to reset password", "userId", userId, "error", err)
		return errors.New("failed to reset password")
	}

	// invalidate all tokens for security
	tokenKey := consts.UserTokenKey + userId
	if err := ul.userRepo.DelToken(tokenKey); err != nil {
		log.Warnw("failed to delete token after password reset", "userId", userId, "error", err)
		// this is not critical, continue
	}

	refreshTokenKey := consts.UserRefreshTokenKey + userId
	if err := ul.userRepo.DelToken(refreshTokenKey); err != nil {
		log.Warnw("failed to delete refresh token after password reset", "userId", userId, "error", err)
	}

	log.Infow("user password reset successfully", "userId", userId)
	return nil
}

// UpdateAvatar updates user avatar URL and clears cache
func (ul *UserService) UpdateAvatar(userId, avatarUrl string) error {
	// update avatar in database
	if err := ul.userRepo.UpdateAvatar(userId, avatarUrl); err != nil {
		log.Errorw("failed to update user avatar", "userId", userId, "error", err)
		return errors.New("failed to update user avatar")
	}

	// clear user info cache in Redis
	if ul.cache != nil {
		key := consts.UserInfoKey + userId
		if err := ul.cache.Del(context.Background(), key).Err(); err != nil {
			log.Warnw("failed to clear user info cache", "userId", userId, "error", err)
			// not critical, continue
		}
	}

	log.Infow("user avatar updated successfully", "userId", userId, "avatarUrl", avatarUrl)
	return nil
}

// GetUserRoles 获取用户的角色信息
func (ul *UserService) GetUserRoles(userId string) ([]usermodel.RoleDTO, error) {
	// 获取用户的所有角色绑定
	roleBindings, err := ul.userRoleBindingRepo.GetUserRoleBindings(userId)
	if err != nil {
		log.Errorw("failed to get user role bindings", "userId", userId, "error", err)
		return nil, err
	}

	if len(roleBindings) == 0 {
		return []usermodel.RoleDTO{}, nil
	}

	// 提取角色ID列表
	roleIds := make([]string, 0, len(roleBindings))
	for _, binding := range roleBindings {
		roleIds = append(roleIds, binding.RoleId)
	}

	// 获取角色详情
	roles, err := ul.roleRepo.GetRolesByRoleIds(roleIds)
	if err != nil {
		log.Errorw("failed to get roles", "roleIds", roleIds, "error", err)
		return nil, err
	}

	// 构建角色列表
	roleList := make([]usermodel.RoleDTO, 0, len(roles))
	for _, role := range roles {
		roleList = append(roleList, usermodel.RoleDTO{
			RoleId:      role.RoleId,
			Name:        role.Name,
			DisplayName: role.DisplayName,
			Description: role.Description,
		})
	}

	return roleList, nil
}

// GetUserRoutes 获取用户可访问的路由列表
func (ul *UserService) GetUserRoutes(userId string, resourceId string) ([]string, error) {
	// 获取用户的所有角色绑定
	roleBindings, err := ul.userRoleBindingRepo.GetUserRoleBindings(userId)
	if err != nil {
		log.Errorw("failed to get user role bindings", "userId", userId, "error", err)
		return nil, err
	}

	if len(roleBindings) == 0 {
		return []string{}, nil
	}

	// 提取角色ID列表
	roleIds := make([]string, 0, len(roleBindings))
	for _, binding := range roleBindings {
		roleIds = append(roleIds, binding.RoleId)
	}

	// 获取这些角色在指定资源下的菜单绑定
	menuBindings, err := ul.roleMenuBindingRepo.GetMenuBindingsByRoles(roleIds, resourceId)
	if err != nil {
		log.Errorw("failed to get menu bindings", "userId", userId, "roleIds", roleIds, "error", err)
		return nil, err
	}

	if len(menuBindings) == 0 {
		return []string{}, nil
	}

	// 提取菜单ID列表（去重）
	menuIdSet := make(map[string]bool)
	for _, binding := range menuBindings {
		if binding.IsVisible == usermodel.MenuVisible && binding.IsAccessible == usermodel.RoleMenuAccessible {
			menuIdSet[binding.MenuId] = true
		}
	}

	menuIds := make([]string, 0, len(menuIdSet))
	for menuId := range menuIdSet {
		menuIds = append(menuIds, menuId)
	}

	// 获取菜单详情（只需要path字段）
	menus, err := ul.menuRepo.GetMenusByMenuIds(menuIds)
	if err != nil {
		log.Errorw("failed to get menus", "menuIds", menuIds, "error", err)
		return nil, err
	}

	// 提取所有路由路径
	routes := make([]string, 0)
	for _, menu := range menus {
		if menu.Path != "" {
			routes = append(routes, menu.Path)
		}
	}

	return routes, nil
}

// GetUserMenus 获取用户可访问的菜单列表（树形结构）
func (ul *UserService) GetUserMenus(userId string, resourceId string) ([]usermodel.MenuDTO, []string, error) {
	roleBindings, err := ul.userRoleBindingRepo.GetUserRoleBindings(userId)
	if err != nil {
		log.Errorw("failed to get user role bindings", "userId", userId, "error", err)
		return nil, nil, err
	}

	if len(roleBindings) == 0 {
		return []usermodel.MenuDTO{}, []string{}, nil
	}

	roleIds := make([]string, 0, len(roleBindings))
	for _, binding := range roleBindings {
		roleIds = append(roleIds, binding.RoleId)
	}

	menuBindings, err := ul.roleMenuBindingRepo.GetMenuBindingsByRoles(roleIds, resourceId)
	if err != nil {
		log.Errorw("failed to get menu bindings", "userId", userId, "roleIds", roleIds, "error", err)
		return nil, nil, err
	}

	if len(menuBindings) == 0 {
		return []usermodel.MenuDTO{}, []string{}, nil
	}

	menuIdSet := make(map[string]bool)
	for _, binding := range menuBindings {
		if binding.IsVisible == usermodel.MenuVisible && binding.IsAccessible == usermodel.RoleMenuAccessible {
			menuIdSet[binding.MenuId] = true
		}
	}

	menuIds := make([]string, 0, len(menuIdSet))
	for menuId := range menuIdSet {
		menuIds = append(menuIds, menuId)
	}

	menus, err := ul.menuRepo.GetMenusByMenuIds(menuIds)
	if err != nil {
		log.Errorw("failed to get menus", "menuIds", menuIds, "error", err)
		return nil, nil, err
	}

	menuTree := ul.menuService.BuildMenuTree(menus)
	routes := ul.menuService.ExtractRoutes(menuTree)

	return menuTree, routes, nil
}

type userRolesRoutesCache struct {
	Roles  []usermodel.RoleDTO `json:"roles"`
	Routes []string            `json:"routes"`
}

// GetUserRolesAndRoutes 获取用户的角色信息和路由信息
func (ul *UserService) GetUserRolesAndRoutes(userId string, resourceId string) ([]usermodel.RoleDTO, []string, error) {
	ctx := context.Background()

	keyFunc := func(params ...any) string {
		uid := params[0].(string)
		resId := params[1].(string)
		if resId == "" {
			resId = "default"
		}
		return consts.UserRolesRoutesKey + uid + ":" + resId
	}

	queryFunc := func(ctx context.Context) (userRolesRoutesCache, error) {
		roleBindings, err := ul.userRoleBindingRepo.GetUserRoleBindings(userId)
		if err != nil {
			log.Errorw("failed to get user role bindings", "userId", userId, "error", err)
			return userRolesRoutesCache{}, err
		}

		if len(roleBindings) == 0 {
			return userRolesRoutesCache{Roles: []usermodel.RoleDTO{}, Routes: []string{}}, nil
		}

		// 提取角色ID列表
		roleIds := make([]string, 0, len(roleBindings))
		for _, binding := range roleBindings {
			roleIds = append(roleIds, binding.RoleId)
		}

		var roles []usermodel.Role
		var menuBindings []usermodel.RoleMenuBinding
		var roleErr, menuErr error

		roles, roleErr = ul.roleRepo.GetRolesByRoleIds(roleIds)
		if roleErr != nil {
			log.Errorw("failed to get roles", "roleIds", roleIds, "error", roleErr)
			return userRolesRoutesCache{}, roleErr
		}

		menuBindings, menuErr = ul.roleMenuBindingRepo.GetMenuBindingsByRoles(roleIds, resourceId)
		if menuErr != nil {
			log.Errorw("failed to get menu bindings", "userId", userId, "roleIds", roleIds, "error", menuErr)
			return userRolesRoutesCache{}, menuErr
		}

		roleList := make([]usermodel.RoleDTO, 0, len(roles))
		for _, role := range roles {
			roleList = append(roleList, usermodel.RoleDTO{
				RoleId:      role.RoleId,
				Name:        role.Name,
				DisplayName: role.DisplayName,
				Description: role.Description,
			})
		}

		if len(menuBindings) == 0 {
			return userRolesRoutesCache{Roles: roleList, Routes: []string{}}, nil
		}

		menuIdSet := make(map[string]bool)
		for _, binding := range menuBindings {
			if binding.IsVisible == usermodel.MenuVisible && binding.IsAccessible == usermodel.RoleMenuAccessible {
				menuIdSet[binding.MenuId] = true
			}
		}

		menuIds := make([]string, 0, len(menuIdSet))
		for menuId := range menuIdSet {
			menuIds = append(menuIds, menuId)
		}

		menus, err := ul.menuRepo.GetMenusByMenuIds(menuIds)
		if err != nil {
			log.Errorw("failed to get menus", "menuIds", menuIds, "error", err)
			return userRolesRoutesCache{}, err
		}

		routes := make([]string, 0, len(menus))
		for _, menu := range menus {
			if menu.Path != "" {
				routes = append(routes, menu.Path)
			}
		}

		return userRolesRoutesCache{Roles: roleList, Routes: routes}, nil
	}

	cq := cache.NewCachedQuery(
		ul.cache,
		keyFunc,
		queryFunc,
		cache.WithTTL[userRolesRoutesCache](10*time.Minute),
		cache.WithLogPrefix[userRolesRoutesCache]("[UserService]"),
	)

	data, err := cq.Get(ctx, userId, resourceId)
	if err != nil {
		return nil, nil, err
	}

	return data.Roles, data.Routes, nil
}
