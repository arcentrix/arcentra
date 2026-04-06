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

package identity

import (
	"context"
	"fmt"
	"mime/multipart"
	"sync/atomic"
	"time"

	"github.com/arcentrix/arcentra/internal/domain/identity"
	"github.com/arcentrix/arcentra/pkg/transport/http"
	"github.com/arcentrix/arcentra/pkg/transport/http/jwt"
	"golang.org/x/crypto/bcrypt"
)

// LoginResult matches the frontend LoginResponse interface.
type LoginResult struct {
	UserInfo *LoginUserInfo `json:"userinfo"`
	Token    *AuthToken     `json:"token"`
	Role     *LoginRole     `json:"role"`
}

// LoginUserInfo is the safe subset of user fields exposed after login.
type LoginUserInfo struct {
	UserID       string `json:"userId"`
	Username     string `json:"username"`
	FullName     string `json:"fullName"`
	Email        string `json:"email"`
	Avatar       string `json:"avatar"`
	Phone        string `json:"phone"`
	IsSuperAdmin bool   `json:"isSuperAdmin"`
}

// AuthToken holds JWT token information.
type AuthToken struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpireAt     int64  `json:"expireAt"`
}

// LoginRole holds the user's primary role information.
type LoginRole struct {
	RoleID      string `json:"roleId"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
}

func (uc *ManageUserUseCase) Login(ctx context.Context, username, email, password string, authConf http.Auth) (*LoginResult, error) {
	var user *identity.User
	var err error

	if username != "" {
		user, err = uc.userRepo.GetByUsername(ctx, username)
	} else if email != "" {
		user, err = uc.userRepo.GetByEmail(ctx, email)
	} else {
		return nil, fmt.Errorf("username or email is required")
	}
	if err != nil {
		return nil, fmt.Errorf("user not found: %w", err)
	}
	if !user.IsEnabled {
		return nil, fmt.Errorf("user is disabled")
	}

	if password != "" {
		storedPassword, pErr := uc.userRepo.GetPassword(ctx, user.UserID)
		if pErr != nil {
			return nil, fmt.Errorf("failed to get password: %w", pErr)
		}
		if cErr := bcrypt.CompareHashAndPassword([]byte(storedPassword), []byte(password)); cErr != nil {
			return nil, fmt.Errorf("invalid password")
		}
	}

	aToken, rToken, tErr := jwt.GenToken(
		user.UserID,
		[]byte(authConf.SecretKey),
		authConf.AccessExpire,
		authConf.RefreshExpire,
	)
	if tErr != nil {
		return nil, fmt.Errorf("failed to generate token: %w", tErr)
	}

	expireAt := time.Now().Add(authConf.AccessExpire).Unix()

	var role *LoginRole
	if uc.roleBindingRepo != nil && uc.roleRepo != nil {
		bindings, _ := uc.roleBindingRepo.List(ctx, user.UserID)
		if len(bindings) > 0 {
			r, rErr := uc.roleRepo.Get(ctx, bindings[0].RoleID)
			if rErr == nil && r != nil {
				role = &LoginRole{
					RoleID:      r.RoleID,
					Name:        r.Name,
					DisplayName: r.DisplayName,
				}
			}
		}
	}

	return &LoginResult{
		Token: &AuthToken{
			AccessToken:  aToken,
			RefreshToken: rToken,
			ExpireAt:     expireAt,
		},
		UserInfo: &LoginUserInfo{
			UserID:       user.UserID,
			Username:     user.Username,
			FullName:     user.FullName,
			Email:        user.Email,
			Avatar:       user.Avatar,
			Phone:        user.Phone,
			IsSuperAdmin: user.IsSuperAdmin,
		},
		Role: role,
	}, nil
}

func (uc *ManageUserUseCase) Register(ctx context.Context, username, fullName, email, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}

	user := &identity.User{
		UserID:    generateUserID(),
		Username:  username,
		FullName:  fullName,
		Email:     email,
		Password:  string(hash),
		IsEnabled: true,
	}
	return uc.userRepo.Create(ctx, user)
}

func (uc *ManageUserUseCase) Refresh(_ context.Context, userID, refreshToken string, authConf http.Auth) (any, error) {
	return jwt.RefreshToken(&authConf, userID, refreshToken)
}

func (uc *ManageUserUseCase) Logout(_ context.Context, _ string) error {
	return nil
}

func (uc *ManageUserUseCase) AddUser(ctx context.Context, username, fullName, email, password, roleID string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}

	user := &identity.User{
		UserID:    generateUserID(),
		Username:  username,
		FullName:  fullName,
		Email:     email,
		Password:  string(hash),
		IsEnabled: true,
	}
	return uc.userRepo.Create(ctx, user)
}

func (uc *ManageUserUseCase) FetchUserInfo(ctx context.Context, userID string) (*identity.User, error) {
	return uc.userRepo.Get(ctx, userID)
}

func (uc *ManageUserUseCase) GetUsersByRole(ctx context.Context, roleID, roleName string, page, size int) ([]identity.User, int64, error) {
	return uc.userRepo.List(ctx, page, size)
}

func (uc *ManageUserUseCase) UpdateUserMap(ctx context.Context, userID string, updates map[string]any) error {
	return uc.userRepo.Update(ctx, userID, updates)
}

func (uc *ManageUserUseCase) ResetPasswordFromReq(ctx context.Context, userID, newPassword string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}
	return uc.userRepo.ResetPassword(ctx, userID, string(hash))
}

func (uc *ManageUserUseCase) UploadAvatar(ctx context.Context, userID string, file *multipart.FileHeader, uploader any) (map[string]any, error) {
	return map[string]any{
		"message": "avatar upload delegated to infrastructure",
	}, nil
}

var userIDCounter uint64

func generateUserID() string {
	n := atomic.AddUint64(&userIDCounter, 1)
	return fmt.Sprintf("u_%d_%d", time.Now().UnixNano(), n)
}
