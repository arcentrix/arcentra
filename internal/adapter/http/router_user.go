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

package http

import (
	"encoding/base64"
	"time"

	"github.com/arcentrix/arcentra/pkg/transport/auth"
	"github.com/arcentrix/arcentra/pkg/transport/http"
	"github.com/gofiber/fiber/v2"
)

func decodeBase64Password(encoded string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (rt *Router) userRoutes(r fiber.Router, authMW fiber.Handler) {
	userGroup := r.Group("/users")
	userGroup.Post("/login", rt.login)
	userGroup.Post("/register", rt.register)
	userGroup.Post("/logout", authMW, rt.logout)
	userGroup.Post("/refresh", authMW, rt.refresh)
	userGroup.Get("/", authMW, rt.getUserList)
	userGroup.Get("/by-role", authMW, rt.getUsersByRole)
	userGroup.Get("/fetch", authMW, rt.fetchUserInfo)
	userGroup.Post("/invite", authMW, rt.addUser)
	userGroup.Put("/:userID", authMW, rt.updateUser)
	userGroup.Put("/:userID/password", authMW, rt.resetPassword)
	userGroup.Post("/fetch/avatar", authMW, rt.uploadAvatar)
}

func (rt *Router) login(c *fiber.Ctx) error {
	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	password, err := decodeBase64Password(req.Password)
	if err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid password encoding")
	}

	result, err := rt.ManageUser.Login(c.Context(), req.Username, req.Email, password, rt.HTTP.Auth)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	rt.storeTokenInCache(result.UserInfo.UserID, result.Token, rt.HTTP.Auth.AccessExpire)

	return http.Detail(c, result)
}

func (rt *Router) register(c *fiber.Ctx) error {
	var req struct {
		Username string `json:"username"`
		FullName string `json:"fullName"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	password, err := decodeBase64Password(req.Password)
	if err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid password encoding")
	}

	if err := rt.ManageUser.Register(c.Context(), req.Username, req.FullName, req.Email, password); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.NotDetail(c)
}

func (rt *Router) refresh(c *fiber.Ctx) error {
	userID := c.Query("userID")
	refreshToken := c.Query("refreshToken")

	token, err := rt.ManageUser.Refresh(c.Context(), userID, refreshToken, rt.HTTP.Auth)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, token)
}

func (rt *Router) logout(c *fiber.Ctx) error {
	claims, err := auth.ParseAuthorizationToken(c, rt.HTTP.Auth.SecretKey)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	if err := rt.ManageUser.Logout(c.Context(), claims.UserID); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.NotDetail(c)
}

func (rt *Router) addUser(c *fiber.Ctx) error {
	var req struct {
		Username string `json:"username"`
		FullName string `json:"fullName"`
		Email    string `json:"email"`
		Password string `json:"password"`
		RoleID   string `json:"roleId"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	password, err := decodeBase64Password(req.Password)
	if err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid password encoding")
	}

	if err := rt.ManageUser.AddUser(c.Context(), req.Username, req.FullName, req.Email, password, req.RoleID); err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.NotDetail(c)
}

func (rt *Router) updateUser(c *fiber.Ctx) error {
	userID := c.Params("userID")
	if userID == "" {
		return http.Err(c, http.BadRequest.Code, "user id is required")
	}

	var req map[string]any
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request body")
	}

	if err := rt.ManageUser.UpdateUserMap(c.Context(), userID, req); err != nil {
		return http.Err(c, http.Failed.Code, http.Failed.Msg)
	}

	return http.NotDetail(c)
}

func (rt *Router) fetchUserInfo(c *fiber.Ctx) error {
	claims, err := auth.ParseAuthorizationToken(c, rt.HTTP.Auth.SecretKey)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	user, err := rt.ManageUser.FetchUserInfo(c.Context(), claims.UserID)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, user)
}

func (rt *Router) getUserList(c *fiber.Ctx) error {
	pageNum := rt.HTTP.QueryInt(c, "pageNum")
	if pageNum == 0 {
		pageNum = rt.HTTP.QueryInt(c, "page")
	}
	if pageNum == 0 {
		pageNum = 1
	}
	pageSize := rt.HTTP.QueryInt(c, "pageSize")
	if pageSize == 0 {
		pageSize = 10
	}

	users, count, err := rt.ManageUser.ListUsers(c.Context(), pageNum, pageSize)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	type UserResponse struct {
		UserID           string     `json:"userID"`
		Username         string     `json:"username"`
		FullName         string     `json:"fullName"`
		Avatar           string     `json:"avatar"`
		Email            string     `json:"email"`
		Phone            string     `json:"phone"`
		IsEnabled        bool       `json:"isEnabled"`
		IsSuperAdmin     bool       `json:"isSuperAdmin"`
		LastLoginAt      *time.Time `json:"lastLoginAt"`
		InvitationStatus string     `json:"invitationStatus"`
	}

	var response []UserResponse
	for _, u := range users {
		response = append(response, UserResponse{
			UserID:       u.UserID,
			Username:     u.Username,
			FullName:     u.FullName,
			Avatar:       u.Avatar,
			Email:        u.Email,
			Phone:        u.Phone,
			IsEnabled:    u.IsEnabled,
			IsSuperAdmin: u.IsSuperAdmin,
		})
	}

	return http.Detail(c, map[string]any{
		"users":    response,
		"count":    count,
		"pageNum":  pageNum,
		"pageSize": pageSize,
	})
}

func (rt *Router) getUsersByRole(c *fiber.Ctx) error {
	roleID := c.Query("roleID")
	roleName := c.Query("roleName")
	if roleID == "" && roleName == "" {
		return http.Err(c, http.BadRequest.Code, "roleID or roleName is required")
	}

	pageNum := rt.HTTP.QueryInt(c, "pageNum")
	if pageNum == 0 {
		pageNum = rt.HTTP.QueryInt(c, "page")
	}
	if pageNum == 0 {
		pageNum = 1
	}
	pageSize := rt.HTTP.QueryInt(c, "pageSize")
	if pageSize == 0 {
		pageSize = 10
	}

	users, count, err := rt.ManageUser.GetUsersByRole(c.Context(), roleID, roleName, pageNum, pageSize)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, map[string]any{
		"users":    users,
		"count":    count,
		"pageNum":  pageNum,
		"pageSize": pageSize,
	})
}

func (rt *Router) resetPassword(c *fiber.Ctx) error {
	userID := c.Params("userID")
	if userID == "" {
		return http.Err(c, http.BadRequest.Code, "user id is required")
	}

	var req struct {
		NewPassword string `json:"newPassword"`
	}
	if err := c.BodyParser(&req); err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid request parameters")
	}
	if req.NewPassword == "" {
		return http.Err(c, http.BadRequest.Code, "newPassword is required")
	}

	newPassword, err := decodeBase64Password(req.NewPassword)
	if err != nil {
		return http.Err(c, http.BadRequest.Code, "invalid password encoding")
	}

	if err := rt.ManageUser.ResetPasswordFromReq(c.Context(), userID, newPassword); err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.NotDetail(c)
}

func (rt *Router) uploadAvatar(c *fiber.Ctx) error {
	claims, err := auth.ParseAuthorizationToken(c, rt.HTTP.Auth.SecretKey)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	file, err := c.FormFile("file")
	if err != nil {
		return http.Err(c, http.BadRequest.Code, "file is required")
	}

	response, err := rt.ManageUser.UploadAvatar(c.Context(), claims.UserID, file, rt.UploadUC)
	if err != nil {
		return http.Err(c, http.Failed.Code, err.Error())
	}

	return http.Detail(c, response)
}
