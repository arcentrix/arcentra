// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/ast"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) organizationRouter(r fiber.Router, authMW fiber.Handler) {
	g := r.Group("/organization", sensitiveHTTPStatus, authMW, rt.firstBuildLiveUser(false))
	g.Get("/", rt.listOrganizations)
	g.Post("/", rt.createOrganization)
	g.Get("/:orgID", rt.getOrganization)
	g.Put("/:orgID", rt.updateOrganization)
	g.Delete("/:orgID", rt.archiveOrganization)
	g.Get("/:orgID/members", rt.scopedOrganizationListMembers)
	g.Post("/:orgID/members", rt.scopedOrganizationAddMember)
	g.Put("/:orgID/members/:userID", rt.scopedOrganizationUpdateMember)
	g.Delete("/:orgID/members/:userID", rt.scopedOrganizationRemoveMember)
}
func organizationError(c *fiber.Ctx, err error) error {
	status, message := fiber.StatusServiceUnavailable, "organization management is unavailable"
	switch {
	case errors.Is(err, repo.ErrOrganizationForbidden), errors.Is(err, repo.ErrApprovalForbidden):
		status, message = fiber.StatusForbidden, "organization or membership access denied"
	case errors.Is(err, repo.ErrOrganizationConflict):
		status, message = fiber.StatusConflict, err.Error()
	case errors.Is(err, repo.ErrOrganizationNotFound):
		status, message = fiber.StatusNotFound, "organization or member not found"
	case errors.Is(err, repo.ErrOrganizationInvalid):
		status, message = fiber.StatusBadRequest, "invalid organization or member request"
	}
	return sensitiveError(c, status, message)
}
func scopedBody(c *fiber.Ctx, out any) error {
	if len(c.Body()) == 0 || len(c.Body()) > 16384 || !utf8.Valid(c.Body()) {
		return repo.ErrOrganizationInvalid
	}
	decoder := sonic.ConfigDefault.NewDecoder(strings.NewReader(string(c.Body())))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return repo.ErrOrganizationInvalid
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return repo.ErrOrganizationInvalid
	}
	// 明确匹配 DTO 的 JSON 字段，避免重复键、大小写别名及 null 被宽松解码。
	typ := reflect.TypeOf(out)
	if typ == nil || typ.Kind() != reflect.Pointer || typ.Elem().Kind() != reflect.Struct {
		return repo.ErrOrganizationInvalid
	}
	typ = typ.Elem()
	fields := make(map[string]reflect.Kind, typ.NumField())
	for i := range typ.NumField() {
		field := typ.Field(i)
		fields[strings.Split(field.Tag.Get("json"), ",")[0]] = field.Type.Kind()
	}
	node, err := sonic.Get(c.Body())
	if err != nil {
		return repo.ErrOrganizationInvalid
	}
	properties, err := node.Properties()
	if err != nil {
		return repo.ErrOrganizationInvalid
	}
	seen := make(map[string]bool, len(fields))
	var pair ast.Pair
	for properties.Next(&pair) {
		kind, ok := fields[pair.Key]
		if !ok || seen[pair.Key] || (kind == reflect.String && pair.Value.Type() != ast.V_STRING) || (kind == reflect.Uint64 && pair.Value.Type() != ast.V_NUMBER) || (kind != reflect.String && kind != reflect.Uint64) {
			return repo.ErrOrganizationInvalid
		}
		seen[pair.Key] = true
	}
	return nil
}
func (rt *Router) organizationService(c *fiber.Ctx) (*service.OrganizationService, string, error) {
	subject, ok := c.Locals("sensitiveSubject").(authz.Subject)
	if !ok || subject.UserID == "" || subject.IsAgent {
		return nil, "", repo.ErrOrganizationForbidden
	}
	if rt.Services == nil || rt.Services.Repos() == nil || rt.Services.Repos().Organization == nil {
		return nil, "", errors.New("organization repository unavailable")
	}
	return service.NewOrganizationService(rt.Services.Repos().Organization), subject.UserID, nil
}
func (rt *Router) listOrganizations(c *fiber.Ctx) error {
	s, actor, err := rt.organizationService(c)
	if err != nil {
		return organizationError(c, err)
	}
	page, e1 := strconv.Atoi(c.Query("page", "1"))
	size, e2 := strconv.Atoi(c.Query("pageSize", "20"))
	if e1 != nil || e2 != nil {
		return organizationError(c, repo.ErrOrganizationInvalid)
	}
	out, err := s.List(c.UserContext(), actor, c.Query("name"), page, size)
	if err != nil {
		return organizationError(c, err)
	}
	return apphttp.Detail(c, out)
}
func (rt *Router) getOrganization(c *fiber.Ctx) error {
	s, actor, err := rt.organizationService(c)
	if err != nil {
		return organizationError(c, err)
	}
	out, err := s.Get(c.UserContext(), actor, c.Params("orgID"))
	if err != nil {
		return organizationError(c, err)
	}
	return apphttp.Detail(c, out)
}
func (rt *Router) createOrganization(c *fiber.Ctx) error {
	s, actor, err := rt.organizationService(c)
	if err != nil {
		return organizationError(c, err)
	}
	var in model.OrganizationInput
	if err := scopedBody(c, &in); err != nil {
		return organizationError(c, err)
	}
	out, err := s.Create(c.UserContext(), actor, in)
	if err != nil {
		return organizationError(c, err)
	}
	return apphttp.Detail(c, out)
}
func (rt *Router) updateOrganization(c *fiber.Ctx) error {
	s, actor, err := rt.organizationService(c)
	if err != nil {
		return organizationError(c, err)
	}
	var in model.OrganizationInput
	if err := scopedBody(c, &in); err != nil {
		return organizationError(c, err)
	}
	out, err := s.Update(c.UserContext(), actor, c.Params("orgID"), in)
	if err != nil {
		return organizationError(c, err)
	}
	return apphttp.Detail(c, out)
}
func (rt *Router) archiveOrganization(c *fiber.Ctx) error {
	s, actor, err := rt.organizationService(c)
	if err != nil {
		return organizationError(c, err)
	}
	var in struct {
		Version uint64 `json:"version"`
	}
	if err := scopedBody(c, &in); err != nil {
		return organizationError(c, err)
	}
	out, err := s.Archive(c.UserContext(), actor, c.Params("orgID"), in.Version)
	if err != nil {
		return organizationError(c, err)
	}
	return apphttp.Detail(c, out)
}
func (rt *Router) scopedMembers(c *fiber.Ctx, kind string) error {
	s, actor, err := rt.organizationService(c)
	if err != nil {
		return organizationError(c, err)
	}
	id := c.Params("orgID")
	if kind == "project" {
		id = c.Params("projectID")
	}
	out, err := s.Members(c.UserContext(), actor, kind, id)
	if err != nil {
		return organizationError(c, err)
	}
	return apphttp.Detail(c, out)
}
func (rt *Router) scopedChangeMember(c *fiber.Ctx, kind, operation string) error {
	s, actor, err := rt.organizationService(c)
	if err != nil {
		return organizationError(c, err)
	}
	id := c.Params("orgID")
	if kind == "project" {
		id = c.Params("projectID")
	}
	var in model.ScopedMemberInput
	if operation == "add" {
		if err := scopedBody(c, &in); err != nil {
			return organizationError(c, err)
		}
	} else if operation == "update" {
		var body struct {
			RoleID  string `json:"roleId"`
			Version uint64 `json:"version"`
		}
		if err := scopedBody(c, &body); err != nil {
			return organizationError(c, err)
		}
		in.RoleID, in.Version = body.RoleID, body.Version
		in.UserID = c.Params("userID")
	} else {
		var body struct {
			Version uint64 `json:"version"`
		}
		if err := scopedBody(c, &body); err != nil {
			return organizationError(c, err)
		}
		in.Version = body.Version
		in.UserID = c.Params("userID")
	}
	out, err := s.ChangeMember(c.UserContext(), actor, kind, id, operation, in)
	if err != nil {
		return organizationError(c, err)
	}
	return apphttp.Detail(c, out)
}
func (rt *Router) scopedOrganizationListMembers(c *fiber.Ctx) error {
	return rt.scopedMembers(c, "org")
}
func (rt *Router) scopedOrganizationAddMember(c *fiber.Ctx) error {
	return rt.scopedChangeMember(c, "org", "add")
}
func (rt *Router) scopedOrganizationUpdateMember(c *fiber.Ctx) error {
	return rt.scopedChangeMember(c, "org", "update")
}
func (rt *Router) scopedOrganizationRemoveMember(c *fiber.Ctx) error {
	return rt.scopedChangeMember(c, "org", "remove")
}
func (rt *Router) scopedProjectListMembers(c *fiber.Ctx) error { return rt.scopedMembers(c, "project") }
func (rt *Router) scopedProjectAddMember(c *fiber.Ctx) error {
	return rt.scopedChangeMember(c, "project", "add")
}
func (rt *Router) scopedProjectUpdateMember(c *fiber.Ctx) error {
	return rt.scopedChangeMember(c, "project", "update")
}
func (rt *Router) scopedProjectRemoveMember(c *fiber.Ctx) error {
	return rt.scopedChangeMember(c, "project", "remove")
}
