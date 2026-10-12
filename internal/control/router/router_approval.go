// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"strings"
	"unicode/utf8"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/ast"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) approvalRouter(r fiber.Router, auth fiber.Handler, subject fiber.Handler) {
	g := r.Group("/approvals", approvalHTTPStatus, auth, subject, rt.collectionAccess(), rt.approvalLiveAccess())
	g.Get("/", rt.approvalPermission("approval:read", rt.approvalRunScope()), rt.listApprovals)
	g.Get("/:id", rt.approvalPermission("approval:read", middleware.ResolveFromApprovalID("id", rt)), rt.getApproval)
	g.Post("/:id/approve", rt.approvalPermission("approval:approve", middleware.ResolveFromApprovalID("id", rt)), rt.approveRequest)
	g.Post("/:id/reject", rt.approvalPermission("approval:reject", middleware.ResolveFromApprovalID("id", rt)), rt.rejectRequest)
}

func (rt *Router) approvalLiveAccess() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if rt.Services == nil || rt.Services.Approval == nil {
			return approvalError(c, errors.New("approval service unavailable"))
		}
		actor, authenticated := collectionSubject(c)
		if !authenticated || actor.IsAgent {
			return approvalError(c, repo.ErrApprovalForbidden)
		}
		subject, err := currentApprovalSubject(c.Context(), rt.Services.Repos(), actor.UserID)
		if err != nil {
			return approvalError(c, err)
		}
		c.Locals("approvalSubject", subject)
		checker, err := authz.NewLiveAuthorizer(c.Context(), rt.Services.Repos())
		if err != nil {
			return approvalError(c, err)
		}
		c.Locals("approvalAuthorizer", checker)
		return c.Next()
	}
}

func currentApprovalSubject(ctx context.Context, repositories *repo.Repositories, actor string) (authz.Subject, error) {
	denied := authz.Subject{}
	if repositories == nil || repositories.User == nil || repositories.TeamMember == nil || repositories.Team == nil {
		return denied, repo.ErrApprovalForbidden
	}
	users, ok := repositories.User.(repo.IExecutionActorReader)
	if !ok {
		return denied, repo.ErrApprovalForbidden
	}
	user, err := users.GetExecutionActor(ctx, actor)
	if err != nil || user == nil || user.UserID != actor || user.IsEnabled != 1 {
		return denied, repo.ErrApprovalForbidden
	}
	memberships, err := repositories.TeamMember.ListUserTeams(ctx, actor)
	if err != nil {
		return denied, err
	}
	if len(memberships) > 1000 {
		return denied, repo.ErrApprovalForbidden
	}
	subject := authz.Subject{UserID: actor}
	ids := make([]string, 0, len(memberships))
	seen := make(map[string]bool, len(memberships))
	for _, membership := range memberships {
		if membership.UserID != actor || membership.TeamID == "" {
			return denied, repo.ErrApprovalForbidden
		}
		if !seen[membership.TeamID] {
			seen[membership.TeamID] = true
			ids = append(ids, membership.TeamID)
		}
	}
	if len(ids) == 0 {
		return subject, nil
	}
	teams, err := repositories.Team.BatchGet(ctx, ids)
	if err != nil {
		return denied, err
	}
	for _, team := range teams {
		if team == nil || !seen[team.TeamID] {
			return denied, repo.ErrApprovalForbidden
		}
		if team.IsEnabled == 1 {
			subject.TeamIDs = append(subject.TeamIDs, team.TeamID)
		}
	}
	return subject, nil
}

func (rt *Router) approvalPermission(action string, resolver middleware.ScopeResolver) fiber.Handler {
	return func(c *fiber.Ctx) error {
		resource, err := resolver(c)
		if err != nil {
			return approvalError(c, err)
		}
		allowed, err := approvalAllowed(c, action, resource)
		if err != nil {
			return approvalError(c, err)
		}
		if !allowed {
			return approvalError(c, repo.ErrApprovalForbidden)
		}
		return c.Next()
	}
}

func approvalAllowed(c *fiber.Ctx, action string, resource authz.ResourceRef) (bool, error) {
	checker, ok := c.Locals("approvalAuthorizer").(authz.IAuthorizer)
	subject, authenticated := c.Locals("approvalSubject").(authz.Subject)
	if !ok || !authenticated || subject.UserID == "" || subject.IsAgent {
		return false, repo.ErrApprovalForbidden
	}
	return checker.Check(c.Context(), subject, action, resource)
}

func (rt *Router) approvalRunScope() middleware.ScopeResolver {
	return func(c *fiber.Ctx) (authz.ResourceRef, error) {
		id := c.Query("pipeline_run_id")
		if !validApprovalRouteID(id) || rt.Services == nil || rt.Services.PipelineRepo == nil {
			return authz.ResourceRef{}, repo.ErrApprovalInvalid
		}
		run, err := rt.Services.PipelineRepo.GetRun(c.Context(), id)
		if err != nil {
			return authz.ResourceRef{}, err
		}
		if run == nil || run.RunID != id || run.PipelineID == "" {
			return authz.ResourceRef{}, repo.ErrApprovalNotFound
		}
		org, project, err := rt.resolvePipelineScope(c.Context(), run.PipelineID)
		if err != nil {
			return authz.ResourceRef{}, err
		}
		return authz.ResourceRef{Type: "project", ID: project, OrgID: org, ProjectID: project}, nil
	}
}

func validApprovalRouteID(id string) bool {
	return id != "" && len(id) <= 64 && strings.TrimSpace(id) == id
}

type approvalDetail struct {
	*model.ApprovalRequest
	CanApprove          bool   `json:"canApprove"`
	CanReject           bool   `json:"canReject"`
	DecisionBlockReason string `json:"decisionBlockReason,omitempty"`
	ViewerDecision      string `json:"viewerDecision,omitempty"`
}

func (rt *Router) approvalDetailFor(c *fiber.Ctx, value *model.ApprovalRequest) approvalDetail {
	result := approvalDetail{ApprovalRequest: value}
	subject, ok := collectionSubject(c)
	if !ok || subject.IsAgent || value == nil {
		result.DecisionBlockReason = "authenticated user is required"
		return result
	}
	old, approveErr := rt.Services.Approval.DecisionEligibility(c.Context(), value, subject.UserID, model.ApprovalDecisionApprove)
	_, rejectErr := rt.Services.Approval.DecisionEligibility(c.Context(), value, subject.UserID, model.ApprovalDecisionReject)
	if old != nil {
		if old.Decision == model.ApprovalDecisionApprove {
			result.ViewerDecision = "approved"
		} else if old.Decision == model.ApprovalDecisionReject {
			result.ViewerDecision = "rejected"
		}
	}
	if approveErr != nil && rejectErr != nil {
		// 内部错误与部署证据细节不暴露给仅拥有只读权限的用户
		result.DecisionBlockReason = "approval is unavailable or you are not currently eligible"
		if old != nil {
			result.DecisionBlockReason = "you have already submitted a decision"
		}
		if value.Status != model.ApprovalStatusPending {
			result.DecisionBlockReason = "approval is no longer pending"
		}
		return result
	}
	org, project, err := rt.ResolveProjectScopeByApprovalID(c.Context(), value.ApprovalID)
	if err != nil {
		result.DecisionBlockReason = "approval scope is unavailable"
		return result
	}
	resource := authz.ResourceRef{Type: "approval", ID: value.ApprovalID, OrgID: org, ProjectID: project}
	if approveErr == nil {
		result.CanApprove, err = approvalAllowed(c, "approval:approve", resource)
		if err != nil {
			result.CanApprove = false
		}
	}
	if rejectErr == nil {
		result.CanReject, err = approvalAllowed(c, "approval:reject", resource)
		if err != nil {
			result.CanReject = false
		}
	}
	if !result.CanApprove && !result.CanReject {
		result.DecisionBlockReason = "approval permission is required"
	}
	return result
}

func (rt *Router) getApproval(c *fiber.Ctx) error {
	id := c.Params("id")
	if !validApprovalRouteID(id) {
		return approvalError(c, repo.ErrApprovalInvalid)
	}
	value, err := rt.Services.Approval.GetApproval(c.Context(), id)
	if err != nil {
		return approvalError(c, err)
	}
	return http.Detail(c, rt.approvalDetailFor(c, value))
}

type approvalActionRequest struct {
	Reason string `json:"reason"`
}

func parseApprovalAction(c *fiber.Ctx) (*approvalActionRequest, error) {
	media, _, err := mime.ParseMediaType(c.Get(fiber.HeaderContentType))
	body := bytes.TrimSpace(c.Body())
	if err != nil || media != fiber.MIMEApplicationJSON || len(body) == 0 || len(c.Body()) > 4096 || !utf8.Valid(body) || body[0] != '{' || c.Query("user") != "" {
		return nil, repo.ErrApprovalInvalid
	}
	var input approvalActionRequest
	decoder := sonic.ConfigDefault.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return nil, repo.ErrApprovalInvalid
	}
	if decoder.Decode(new(any)) != io.EOF || len(input.Reason) > 2048 {
		return nil, repo.ErrApprovalInvalid
	}
	// 避免大小写别名、重复键或 null 被解码器宽松解释
	node, err := sonic.Get(body)
	if err != nil {
		return nil, repo.ErrApprovalInvalid
	}
	properties, err := node.Properties()
	if err != nil {
		return nil, repo.ErrApprovalInvalid
	}
	seen := false
	var pair ast.Pair
	for properties.Next(&pair) {
		if seen || pair.Key != "reason" || pair.Value.Type() != ast.V_STRING {
			return nil, repo.ErrApprovalInvalid
		}
		seen = true
	}
	return &input, nil
}

func (rt *Router) approveRequest(c *fiber.Ctx) error {
	return rt.decideApproval(c, model.ApprovalDecisionApprove)
}
func (rt *Router) rejectRequest(c *fiber.Ctx) error {
	return rt.decideApproval(c, model.ApprovalDecisionReject)
}

func (rt *Router) decideApproval(c *fiber.Ctx, decision string) error {
	subject, ok := collectionSubject(c)
	if !ok || subject.IsAgent {
		return http.Err(c, http.Unauthorized.Code, http.Unauthorized.Msg)
	}
	id := c.Params("id")
	if !validApprovalRouteID(id) {
		return approvalError(c, repo.ErrApprovalInvalid)
	}
	input, err := parseApprovalAction(c)
	if err != nil {
		return approvalError(c, err)
	}
	value, err := rt.Services.Approval.Decide(c.Context(), id, subject.UserID, decision, input.Reason)
	if err != nil {
		return approvalError(c, err)
	}
	return http.Detail(c, rt.approvalDetailFor(c, value))
}

func (rt *Router) listApprovals(c *fiber.Ctx) error {
	id := c.Query("pipeline_run_id")
	if !validApprovalRouteID(id) {
		return approvalError(c, repo.ErrApprovalInvalid)
	}
	values, err := rt.Services.Approval.ListByPipelineRun(c.Context(), id)
	if err != nil {
		return approvalError(c, err)
	}
	result := make([]approvalDetail, 0, len(values))
	for _, value := range values {
		result = append(result, rt.approvalDetailFor(c, value))
	}
	return http.Detail(c, result)
}

func approvalError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, repo.ErrApprovalInvalid):
		return http.Err(c, http.BadRequest.Code, "invalid approval request")
	case errors.Is(err, repo.ErrApprovalForbidden), errors.Is(err, service.ErrDeploymentAuthorization):
		return http.Err(c, http.Forbidden.Code, "not eligible to decide this approval")
	case errors.Is(err, repo.ErrApprovalNotFound):
		return http.Err(c, http.NotFound.Code, "approval not found")
	case errors.Is(err, repo.ErrApprovalExpired):
		c.Status(fiber.StatusConflict)
		return http.Err(c, http.BadRequest.Code, "approval has expired")
	case errors.Is(err, repo.ErrApprovalConflict), errors.Is(err, service.ErrDeploymentApproval), errors.Is(err, service.ErrDeploymentTargetChanged):
		c.Status(fiber.StatusConflict)
		return http.Err(c, http.BadRequest.Code, "approval decision conflicts with current state")
	default:
		return http.Err(c, http.Failed.Code, "approval is currently unavailable")
	}
}

func approvalHTTPStatus(c *fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "private, no-store")
	err := c.Next()
	if err != nil {
		return err
	}
	if c.Locals(http.DETAIL) != nil || c.Response().StatusCode() != fiber.StatusOK {
		return nil
	}
	var failure struct {
		Code int `json:"code"`
	}
	if sonic.Unmarshal(c.Response().Body(), &failure) != nil {
		c.Status(fiber.StatusServiceUnavailable)
		return nil
	}
	switch {
	case failure.Code == http.Forbidden.Code || failure.Code == http.PermissionDenied.Code:
		c.Status(fiber.StatusForbidden)
	case failure.Code >= 4400 && failure.Code < 4500:
		c.Status(fiber.StatusUnauthorized)
	case failure.Code == http.NotFound.Code:
		c.Status(fiber.StatusNotFound)
	case failure.Code == http.BadRequest.Code:
		c.Status(fiber.StatusBadRequest)
	default:
		c.Status(fiber.StatusServiceUnavailable)
	}
	return nil
}
