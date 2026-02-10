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

package router

import (
	"time"

	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/pkg/auth"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) userRouter(r fiber.Router, auth fiber.Handler) {
	userGroup := r.Group("/users")
	{
		// Authentication routes (no authentication required)
		userGroup.Post("/login", rt.login)       // POST /users/login - user login
		userGroup.Post("/register", rt.register) // POST /users/register - user registration

		// Session routes (authentication required)
		userGroup.Post("/logout", auth, rt.logout)   // POST /users/logout - user logout
		userGroup.Post("/refresh", auth, rt.refresh) // POST /users/refresh - refresh token

		// User resource routes (authentication required)
		userGroup.Get("/", auth, rt.getUserList)                   // GET /users - list users with pagination
		userGroup.Get("/by-role", auth, rt.getUsersByRole)         // GET /users/by-role - get users by roleId or roleName
		userGroup.Get("/fetch", auth, rt.fetchUserInfo)            // GET /users/fetch - get current user info
		userGroup.Post("/invite", auth, rt.addUser)                // POST /users/invite - invite user
		userGroup.Put("/:userId", auth, rt.updateUser)             // PUT /users/:id - update user info
		userGroup.Put("/:userId/password", auth, rt.resetPassword) // PUT /users/:id/password - reset user password
		userGroup.Post("/fetch/avatar", auth, rt.uploadAvatar)     // POST /users/me/avatar - upload user avatar
		// userGroup.Get("/:id", auth, rt.getUser)          // GET /users/:id - get specific user (to be implemented)
		// userGroup.Delete("/:id", auth, rt.deleteUser)    // DELETE /users/:id - delete user (to be implemented)
	}
}

func (rt *Router) login(c *fiber.Ctx) error {
	var login *model.Login
	userService := rt.Services.User

	if err := c.BodyParser(&login); err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	user, err := userService.Login(login, rt.Http.Auth)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	result := make(map[string]interface{})
	result["token"] = user.Token
	result["role"] = nil

	c.Locals(middleware.DETAIL, user)
	return nil
}

func (rt *Router) register(c *fiber.Ctx) error {
	var register *model.Register
	userLogic := rt.Services.User
	if err := c.BodyParser(&register); err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	if err := userLogic.Register(register); err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.OPERATION, "")
	return nil
}

func (rt *Router) refresh(c *fiber.Ctx) error {
	userLogic := rt.Services.User
	userId := c.Query("userId")
	refreshToken := c.Query("refreshToken")

	token, err := userLogic.Refresh(userId, refreshToken, &rt.Http.Auth)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, token)
	return nil
}

func (rt *Router) logout(c *fiber.Ctx) error {
	userLogic := rt.Services.User

	claims, err := auth.ParseAuthorizationToken(c, rt.Http.Auth.SecretKey)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	if err := userLogic.Logout(claims.UserId); err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.OPERATION, "")
	return nil
}

func (rt *Router) addUser(c *fiber.Ctx) error {
	var addUserReq *model.AddUserReq
	userLogic := rt.Services.User
	if err := c.BodyParser(&addUserReq); err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, http.Failed.Msg, c.Path())
	}

	if err := userLogic.AddUser(*addUserReq); err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, http.Failed.Msg, c.Path())
	}

	c.Locals(middleware.OPERATION, "")
	return nil
}

func (rt *Router) updateUser(c *fiber.Ctx) error {
	var updateReq *model.UpdateUserReq
	userLogic := rt.Services.User
	if err := c.BodyParser(&updateReq); err != nil {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "invalid request body", c.Path())
	}

	userId := c.Params("userId")
	if userId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "user id is required", c.Path())
	}

	if err := userLogic.UpdateUser(userId, updateReq); err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, http.Failed.Msg, c.Path())
	}

	c.Locals(middleware.OPERATION, "update user information")
	return nil
}

func (rt *Router) fetchUserInfo(c *fiber.Ctx) error {
	var user *model.UserInfo
	userLogic := rt.Services.User

	claims, err := auth.ParseAuthorizationToken(c, rt.Http.Auth.SecretKey)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	user, err = userLogic.FetchUserInfo(claims.UserId)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.DETAIL, user)
	return nil
}

// getUserList gets user list with pagination
func (rt *Router) getUserList(c *fiber.Ctx) error {
	userLogic := rt.Services.User

	// Support both "page" and "pageNum" parameters
	pageNum := rt.Http.QueryInt(c, "pageNum")
	if pageNum == 0 {
		pageNum = rt.Http.QueryInt(c, "page")
	}
	if pageNum == 0 {
		pageNum = 1
	}

	pageSize := rt.Http.QueryInt(c, "pageSize")
	if pageSize == 0 {
		pageSize = 10
	}

	users, count, err := userLogic.GetUserList(pageNum, pageSize)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	// build response without created_at and updated_at
	type UserResponse struct {
		UserId           string     `json:"userId"`
		Username         string     `json:"username"`
		FullName         string     `json:"fullName"`
		Avatar           string     `json:"avatar"`
		Email            string     `json:"email"`
		Phone            string     `json:"phone"`
		IsEnabled        int        `json:"isEnabled"`
		IsSuperAdmin     int        `json:"isSuperAdmin"`
		LastLoginAt      *time.Time `json:"lastLoginAt"`
		InvitationStatus string     `json:"invitationStatus"`
		RoleName         *string    `json:"roleName"` // 角色名称
	}

	var response []UserResponse
	for _, user := range users {
		response = append(response, UserResponse{
			UserId:           user.UserId,
			Username:         user.Username,
			FullName:         user.FullName,
			Avatar:           user.Avatar,
			Email:            user.Email,
			Phone:            user.Phone,
			IsEnabled:        user.IsEnabled,
			IsSuperAdmin:     user.IsSuperAdmin,
			LastLoginAt:      user.LastLoginAt,
			InvitationStatus: user.InvitationStatus,
			RoleName:         user.RoleName,
		})
	}

	result := make(map[string]interface{})
	result["users"] = response
	result["count"] = count
	result["pageNum"] = pageNum
	result["pageSize"] = pageSize

	c.Locals(middleware.DETAIL, result)
	return nil
}

// getUsersByRole GET /users/by-role - get users by roleId or roleName
func (rt *Router) getUsersByRole(c *fiber.Ctx) error {
	userLogic := rt.Services.User

	roleId := c.Query("roleId")
	roleName := c.Query("roleName")

	if roleId == "" && roleName == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "roleId or roleName is required", c.Path())
	}

	// Support both "page" and "pageNum" parameters
	pageNum := rt.Http.QueryInt(c, "pageNum")
	if pageNum == 0 {
		pageNum = rt.Http.QueryInt(c, "page")
	}
	if pageNum == 0 {
		pageNum = 1
	}

	pageSize := rt.Http.QueryInt(c, "pageSize")
	if pageSize == 0 {
		pageSize = 10
	}

	users, count, err := userLogic.GetUsersByRole(roleId, roleName, pageNum, pageSize)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	// build response without created_at and updated_at
	type UserResponse struct {
		UserId           string     `json:"userId"`
		Username         string     `json:"username"`
		FullName         string     `json:"fullName"`
		Avatar           string     `json:"avatar"`
		Email            string     `json:"email"`
		Phone            string     `json:"phone"`
		IsEnabled        int        `json:"isEnabled"`
		IsSuperAdmin     int        `json:"isSuperAdmin"`
		LastLoginAt      *time.Time `json:"lastLoginAt"`
		InvitationStatus string     `json:"invitationStatus"`
		RoleName         *string    `json:"roleName"` // 角色名称
	}

	var response []UserResponse
	for _, user := range users {
		response = append(response, UserResponse{
			UserId:           user.UserId,
			Username:         user.Username,
			FullName:         user.FullName,
			Avatar:           user.Avatar,
			Email:            user.Email,
			Phone:            user.Phone,
			IsEnabled:        user.IsEnabled,
			IsSuperAdmin:     user.IsSuperAdmin,
			LastLoginAt:      user.LastLoginAt,
			InvitationStatus: user.InvitationStatus,
			RoleName:         user.RoleName,
		})
	}

	result := make(map[string]interface{})
	result["users"] = response
	result["count"] = count
	result["pageNum"] = pageNum
	result["pageSize"] = pageSize

	c.Locals(middleware.DETAIL, result)
	return nil
}

// resetPassword resets user password
func (rt *Router) resetPassword(c *fiber.Ctx) error {
	userLogic := rt.Services.User

	// get user ID from path parameter
	userId := c.Params("userId")
	if userId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "user id is required", c.Path())
	}

	var req model.ResetPasswordReq
	if err := c.BodyParser(&req); err != nil {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "invalid request parameters", c.Path())
	}

	// validate required fields
	if req.NewPassword == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "newPassword is required", c.Path())
	}

	if err := userLogic.ResetPassword(userId, &req); err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	c.Locals(middleware.OPERATION, "reset user password")
	return nil
}

// uploadAvatar uploads avatar for current user
func (rt *Router) uploadAvatar(c *fiber.Ctx) error {
	userService := rt.Services.User
	uploadService := rt.Services.Upload

	// get current user ID from token
	claims, err := auth.ParseAuthorizationToken(c, rt.Http.Auth.SecretKey)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	// get file from form
	file, err := c.FormFile("file")
	if err != nil {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "file is required", c.Path())
	}

	// upload avatar to object storage
	response, err := uploadService.UploadAvatar(file, claims.UserId)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	// update user avatar in database with complete URL
	if err := userService.UpdateAvatar(claims.UserId, response.FileURL); err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, err.Error(), c.Path())
	}

	// prepare response with avatar URL
	result := map[string]interface{}{
		"fileUrl":      response.FileURL,
		"originalName": response.OriginalName,
		"size":         response.Size,
		"contentType":  response.ContentType,
		"storageId":    response.StorageId,
		"storageType":  response.StorageType,
		"uploadTime":   response.UploadTime,
	}

	c.Locals(middleware.DETAIL, result)
	c.Locals(middleware.OPERATION, "upload user avatar")
	return nil
}
