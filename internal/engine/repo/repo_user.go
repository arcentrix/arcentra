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

package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/internal/engine/consts"
	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/bytedance/sonic"
	"gorm.io/gorm"
)

type IUserRepository interface {
	AddUser(addUserReq *model.AddUserReq) error
	UpdateUser(userId string, updates map[string]any) error
	GetUserByUserId(userId string) (*model.User, error)
	FetchUserInfo(userId string) (*model.UserInfo, error)
	GetUserByUsername(username string) (string, error)
	Login(login *model.Login) (*model.User, error)
	Register(register *model.Register) error
	Logout(userKey string) error
	GetUserList(offset int, pageSize int) ([]UserWithExt, int64, error)
	GetUsersByRole(roleId string, roleName string, offset int, pageSize int) ([]UserWithExt, int64, error)
	SetToken(userId, aToken string, auth http.Auth) (string, error)
	SetRefreshToken(userId, rToken string, auth http.Auth) (string, error)
	SetLoginRespInfo(auth http.Auth, loginResp *model.LoginResp) error
	GetToken(key string) (string, error)
	DelToken(key string) error
	GetUserPassword(userId string) (string, error)
	ResetPassword(userId, newPasswordHash string) error
	UpdateAvatar(userId, avatarUrl string) error
	GetUserAvatar(userId string) (string, error)
}

type UserRepo struct {
	database.IDatabase
	cache.ICache
}

func NewUserRepo(db database.IDatabase, cache cache.ICache) IUserRepository {
	return &UserRepo{
		IDatabase: db,
		ICache:    cache,
	}
}

func (ur *UserRepo) AddUser(addUserReq *model.AddUserReq) error {
	return ur.Database().Create(addUserReq).Error
}

// GetUserByUserId 根据userId获取用户
func (ur *UserRepo) GetUserByUserId(userId string) (*model.User, error) {
	var user model.User
	err := ur.Database().Table(user.TableName()).
		Where("user_id = ?", userId).
		First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// UpdateUser updates user information (user_id, username, password, created_at cannot be updated)
func (ur *UserRepo) UpdateUser(userId string, updates map[string]any) error {
	var user model.User
	err := ur.Database().Table(user.TableName()).
		Where("user_id = ?", userId).
		Updates(updates).Error
	if err != nil {
		return err
	}

	// 清除用户信息缓存
	ur.invalidateUserInfoCache(userId)
	return nil
}

func (ur *UserRepo) FetchUserInfo(userId string) (*model.UserInfo, error) {
	ctx := context.Background()

	keyFunc := func(params ...any) string {
		return consts.UserInfoKey + params[0].(string)
	}

	queryFunc := func(ctx context.Context) (*model.UserInfo, error) {
		var user model.User
		err := ur.Database().Table(user.TableName()).
			Select("user_id, username, full_name, avatar, email, phone").
			Where("user_id = ?", userId).First(&user).Error
		if err != nil {
			return nil, fmt.Errorf("failed to get user info: %w", err)
		}

		// 转换为 UserInfo
		userInfo := &model.UserInfo{
			UserId:   user.UserId,
			Username: user.Username,
			FullName: user.FullName,
			Avatar:   user.Avatar,
			Email:    user.Email,
			Phone:    user.Phone,
		}
		return userInfo, nil
	}

	cq := cache.NewCachedQuery(
		ur.ICache,
		keyFunc,
		queryFunc,
		cache.WithTTL[*model.UserInfo](time.Hour),
		cache.WithLogPrefix[*model.UserInfo]("[UserRepo]"),
	)

	return cq.Get(ctx, userId)
}

func (ur *UserRepo) GetUserByUsername(username string) (string, error) {
	var user model.User
	err := ur.Database().Table(user.TableName()).Select("user_id").Where("username = ?", username).
		First(&user).Error
	return user.UserId, err
}

func (ur *UserRepo) Login(login *model.Login) (*model.User, error) {
	var user model.User
	scope := func(db *gorm.DB) *gorm.DB {
		return db.Table(user.TableName()).Select("user_id, username, full_name, avatar, email, phone, password")
	}

	err := ur.Database().Scopes(scope).Where(
		"(username = ? OR email = ?) AND is_enabled = 1",
		login.Username, login.Email,
	).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (ur *UserRepo) Register(register *model.Register) error {
	var user model.User
	err := ur.Database().Table(user.TableName()).Select("username").
		Where("username = ?", register.Username).
		First(&user).Error
	if err == nil {
		return errors.New(http.UserAlreadyExist.Msg)
	}
	return ur.Database().Exec(
		"INSERT INTO t_user (user_id, username, full_name, email, avatar, password, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		register.UserId,
		register.Username,
		register.FullName,
		register.Email,
		register.Avatar,
		register.Password,
		register.CreatedAt,
	).Error
}

func (ur *UserRepo) Logout(userKey string) error {
	if ur.ICache == nil {
		return nil
	}
	ctx := context.Background()
	return ur.ICache.Del(ctx, userKey).Err()
}

// UserWithExt combines user and ext information
type UserWithExt struct {
	model.User
	LastLoginAt      *time.Time `gorm:"column:last_login_at" json:"lastLoginAt"`
	InvitationStatus string     `gorm:"column:invitation_status" json:"invitationStatus"`
	RoleName         *string    `gorm:"column:role_name" json:"roleName"` // 角色名称
}

func (ur *UserRepo) GetUserList(offset int, pageSize int) ([]UserWithExt, int64, error) {
	var usersExt []UserWithExt
	var user model.User
	var count int64

	// join with user ext table and role table to get last login time, invitation status, and role name
	// use subquery to get the first role name for each user to avoid duplicate rows
	selectFields := "u.user_id, u.username, u.full_name, u.avatar, u.email, u.phone, u.is_enabled, u.is_superadmin, ue.last_login_at, COALESCE(ue.invitation_status, 'accepted') AS invitation_status, role.role_name"
	userExtJoin := "LEFT JOIN t_user_ext ue ON ue.user_id = u.user_id"
	roleSubqueryJoin := "LEFT JOIN (SELECT user_id, name AS role_name FROM (SELECT urb.user_id, r.name, ROW_NUMBER() OVER (PARTITION BY urb.user_id ORDER BY urb.create_time ASC) rn FROM t_user_role_binding urb JOIN t_role r ON r.role_id = urb.role_id WHERE r.is_enabled = 1) t WHERE rn = 1) role ON role.user_id = u.user_id"

	err := ur.Database().
		Table("t_user AS u").
		Select(selectFields).
		Joins(userExtJoin).
		Joins(roleSubqueryJoin).
		Order("u.created_at DESC").
		Offset(offset).
		Limit(pageSize).
		Find(&usersExt).Error
	if err != nil {
		return nil, 0, err
	}

	err = ur.Database().Model(&user).Count(&count).Error
	return usersExt, count, err
}

// GetUsersByRole 根据角色ID或角色名称获取用户列表
func (ur *UserRepo) GetUsersByRole(roleId string, roleName string, offset int, pageSize int) ([]UserWithExt, int64, error) {
	var usersExt []UserWithExt
	var count int64

	selectFields := "u.user_id, u.username, u.full_name, u.avatar, u.email, u.phone, u.is_enabled, u.is_superadmin, ue.last_login_at, COALESCE(ue.invitation_status, 'accepted') AS invitation_status, role.role_name"
	userExtJoin := "LEFT JOIN t_user_ext ue ON ue.user_id = u.user_id"

	// 构建角色子查询，根据 roleId 或 roleName 过滤
	var roleSubqueryJoin string
	if roleId != "" {
		// 按 roleId 查询：使用 INNER JOIN 确保只返回有该角色的用户
		roleSubqueryJoin = "INNER JOIN (SELECT DISTINCT urb.user_id, r.name AS role_name FROM t_user_role_binding urb JOIN t_role r ON r.role_id = urb.role_id WHERE r.is_enabled = 1 AND urb.role_id = ?) role ON role.user_id = u.user_id"
	} else if roleName != "" {
		// 按 roleName 查询：使用 INNER JOIN 确保只返回有该角色名称的用户
		roleSubqueryJoin = "INNER JOIN (SELECT user_id, name AS role_name FROM (SELECT urb.user_id, r.name, ROW_NUMBER() OVER (PARTITION BY urb.user_id ORDER BY urb.create_time ASC) rn FROM t_user_role_binding urb JOIN t_role r ON r.role_id = urb.role_id WHERE r.is_enabled = 1 AND r.name = ?) t WHERE rn = 1) role ON role.user_id = u.user_id"
	} else {
		// 默认：返回所有用户的第一个角色
		roleSubqueryJoin = "LEFT JOIN (SELECT user_id, name AS role_name FROM (SELECT urb.user_id, r.name, ROW_NUMBER() OVER (PARTITION BY urb.user_id ORDER BY urb.create_time ASC) rn FROM t_user_role_binding urb JOIN t_role r ON r.role_id = urb.role_id WHERE r.is_enabled = 1) t WHERE rn = 1) role ON role.user_id = u.user_id"
	}

	db := ur.Database().
		Table("t_user AS u").
		Select(selectFields).
		Joins(userExtJoin)

	// 根据 roleId 或 roleName 应用不同的 JOIN
	if roleId != "" {
		db = db.Joins(roleSubqueryJoin, roleId)
	} else if roleName != "" {
		db = db.Joins(roleSubqueryJoin, roleName)
	} else {
		db = db.Joins(roleSubqueryJoin)
	}

	// 获取总数
	countDb := db
	if err := countDb.Count(&count).Error; err != nil {
		return nil, 0, err
	}

	// 查询列表
	err := db.
		Order("u.created_at DESC").
		Offset(offset).
		Limit(pageSize).
		Find(&usersExt).Error
	if err != nil {
		return nil, 0, err
	}

	return usersExt, count, err
}

func (ur *UserRepo) SetToken(userId, aToken string, auth http.Auth) (string, error) {
	if ur.ICache == nil {
		return "", fmt.Errorf("cache not available")
	}
	ctx := context.Background()

	// 构建 TokenInfo 结构
	now := time.Now()
	tokenInfo := http.TokenInfo{
		AccessToken:  aToken,
		RefreshToken: "", // refresh token stored in its own key
		ExpireAt:     now.Add(auth.AccessExpire).Unix(),
		CreateAt:     now.Unix(),
	}

	// 序列化 token 信息为 JSON
	tokenInfoJson, err := sonic.MarshalString(&tokenInfo)
	if err != nil {
		return "", fmt.Errorf("failed to marshal token info: %w", err)
	}

	key := consts.UserTokenKey + userId
	if err := ur.ICache.Set(ctx, key, tokenInfoJson, auth.AccessExpire).Err(); err != nil {
		return "", fmt.Errorf("failed to set token in Redis: %w", err)
	}
	return key, nil
}

func (ur *UserRepo) SetRefreshToken(userId, rToken string, auth http.Auth) (string, error) {
	if ur.ICache == nil {
		return "", fmt.Errorf("cache not available")
	}
	ctx := context.Background()

	// 构建 TokenInfo 结构（refresh token 专用）
	now := time.Now()
	tokenInfo := http.TokenInfo{
		AccessToken:  "",
		RefreshToken: rToken,
		ExpireAt:     now.Add(auth.RefreshExpire).Unix(),
		CreateAt:     now.Unix(),
	}

	tokenInfoJson, err := sonic.MarshalString(&tokenInfo)
	if err != nil {
		return "", fmt.Errorf("failed to marshal token info: %w", err)
	}

	key := consts.UserRefreshTokenKey + userId
	if err := ur.ICache.Set(ctx, key, tokenInfoJson, auth.RefreshExpire).Err(); err != nil {
		return "", fmt.Errorf("failed to set refresh token in Redis: %w", err)
	}
	return key, nil
}

func (ur *UserRepo) SetLoginRespInfo(auth http.Auth, loginResp *model.LoginResp) error {
	if ur.ICache == nil {
		return fmt.Errorf("cache not available")
	}
	ctx := context.Background()

	pipe := ur.ICache.Pipeline()

	accessTokenInfo := http.TokenInfo{
		AccessToken:  loginResp.Token["accessToken"],
		RefreshToken: "",
		ExpireAt:     loginResp.ExpireAt,
		CreateAt:     loginResp.CreateAt,
	}

	accessTokenInfoJson, err := sonic.Marshal(&accessTokenInfo)
	if err != nil {
		return fmt.Errorf("failed to marshal token info: %w", err)
	}

	tokenKey := consts.UserTokenKey + loginResp.UserInfo.UserId
	if err := pipe.Set(ctx, tokenKey, accessTokenInfoJson, auth.AccessExpire).Err(); err != nil {
		return fmt.Errorf("failed to set token in Redis: %w", err)
	}

	refreshTokenInfo := http.TokenInfo{
		AccessToken:  "",
		RefreshToken: loginResp.Token["refreshToken"],
		ExpireAt:     time.Now().Add(auth.RefreshExpire).Unix(),
		CreateAt:     time.Now().Unix(),
	}
	refreshTokenInfoJson, err := sonic.Marshal(&refreshTokenInfo)
	if err != nil {
		return fmt.Errorf("failed to marshal refresh token info: %w", err)
	}

	refreshTokenKey := consts.UserRefreshTokenKey + loginResp.UserInfo.UserId
	if err := pipe.Set(ctx, refreshTokenKey, refreshTokenInfoJson, auth.RefreshExpire).Err(); err != nil {
		return fmt.Errorf("failed to set refresh token in Redis: %w", err)
	}

	userInfoJson, err := sonic.Marshal(&loginResp.UserInfo)
	if err != nil {
		return fmt.Errorf("failed to marshal user info: %w", err)
	}

	userInfoKey := consts.UserInfoKey + loginResp.UserInfo.UserId
	if err := pipe.Set(ctx, userInfoKey, userInfoJson, auth.AccessExpire).Err(); err != nil {
		return fmt.Errorf("failed to set user info in Redis: %w", err)
	}

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("failed to execute Redis pipeline: %w", err)
	}
	return nil
}

func (ur *UserRepo) GetToken(key string) (string, error) {
	if ur.ICache == nil {
		return "", fmt.Errorf("cache not available")
	}
	ctx := context.Background()
	token, err := ur.ICache.Get(ctx, key).Result()
	if err != nil {
		return "", fmt.Errorf("failed to get token from Redis: %w", err)
	}
	return token, nil
}

func (ur *UserRepo) DelToken(key string) error {
	if ur.ICache == nil {
		return nil
	}
	ctx := context.Background()
	if err := ur.ICache.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("failed to delete token from Redis: %w", err)
	}
	return nil
}

// GetUserPassword gets user password hash by user ID
func (ur *UserRepo) GetUserPassword(userId string) (string, error) {
	var user model.User
	err := ur.Database().Table(user.TableName()).
		Select("password").
		Where("user_id = ?", userId).
		First(&user).Error
	if err != nil {
		return "", err
	}
	return user.Password, nil
}

// ResetPassword resets user password
func (ur *UserRepo) ResetPassword(userId, newPasswordHash string) error {
	var user model.User
	return ur.Database().Table(user.TableName()).
		Where("user_id = ?", userId).
		Update("password", newPasswordHash).Error
}

// UpdateAvatar updates user avatar URL
func (ur *UserRepo) UpdateAvatar(userId, avatarUrl string) error {
	var user model.User
	result := ur.Database().Table(user.TableName()).
		Where("user_id = ?", userId).
		Update("avatar", avatarUrl)

	if result.Error != nil {
		return result.Error
	}

	// 清除用户信息缓存
	if result.RowsAffected > 0 {
		ur.invalidateUserInfoCache(userId)
	}

	return nil
}

// GetUserAvatar gets user avatar URL by user ID
func (ur *UserRepo) GetUserAvatar(userId string) (string, error) {
	var user model.User
	err := ur.Database().Table(user.TableName()).
		Select("avatar").
		Where("user_id = ?", userId).
		First(&user).Error
	if err != nil {
		return "", err
	}
	return user.Avatar, nil
}

// invalidateUserInfoCache 清除用户信息缓存
func (ur *UserRepo) invalidateUserInfoCache(userId string) {
	ctx := context.Background()
	keyFunc := func(params ...any) string {
		return consts.UserInfoKey + params[0].(string)
	}
	cq := cache.NewCachedQuery[*model.UserInfo](ur.ICache, keyFunc, nil)
	_ = cq.Invalidate(ctx, userId)
}
