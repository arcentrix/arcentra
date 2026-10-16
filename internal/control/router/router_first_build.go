// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/consts"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/gofiber/fiber/v2"
)

type firstBuildScopeOption struct {
	ScopeID string `json:"scopeId"`
	Source  string `json:"source"`
}

type firstBuildConfiguration struct {
	Access                   string `json:"access"`
	DefaultStorageConfigured *bool  `json:"defaultStorageConfigured,omitempty"`
	AgentSigningConfigured   *bool  `json:"agentSigningConfigured,omitempty"`
}

type firstBuildAgent struct {
	Access              string `json:"access"`
	EnabledRecentlySeen *bool  `json:"enabledRecentlySeen,omitempty"`
}

type firstBuildDetail struct {
	ScopeOptions          []firstBuildScopeOption `json:"scopeOptions"`
	ScopeOptionsTruncated bool                    `json:"scopeOptionsTruncated"`
	CanEnterScope         bool                    `json:"canEnterScope"`
	Configuration         firstBuildConfiguration `json:"configuration"`
	Agent                 firstBuildAgent         `json:"agent"`
}

// This is a permission-filtered saved-state hint, not an execution readiness
// probe. Storage consumers may hold a startup snapshot and require restart
// after configuration changes. A recent agent heartbeat does not attest to
// runtime capabilities, selectors, capacity, or connectivity on the next build.
// Organization scope IDs are role-grant evidence only, never memberships or
// evidence that an organization row exists. No request-supplied scope is used.
func (rt *Router) getMyFirstBuild(c *fiber.Ctx) error {
	unavailable := func() error {
		return sensitiveError(c, fiber.StatusServiceUnavailable, "first-build guidance is unavailable")
	}
	subject, ok := c.Locals("sensitiveSubject").(authz.Subject)
	if !ok || subject.UserID == "" || subject.IsAgent {
		return sensitiveError(c, fiber.StatusForbidden, "current user access denied")
	}
	repositories := firstBuildRepositories(c)
	if repositories == nil || repositories.FirstBuild == nil {
		return unavailable()
	}
	ctx := c.UserContext()
	checker, err := authz.NewLiveAuthorizer(ctx, repositories)
	if err != nil {
		return unavailable()
	}
	result := firstBuildDetail{
		ScopeOptions:  []firstBuildScopeOption{},
		Configuration: firstBuildConfiguration{Access: "restricted"},
		Agent:         firstBuildAgent{Access: "restricted"},
	}
	result.CanEnterScope, err = checker.Check(ctx, subject, consts.PermOrgCreateProject, authz.ResourceRef{})
	if err != nil {
		return unavailable()
	}
	scopes, truncated, err := repositories.FirstBuild.ScopeCandidates(ctx, subject.UserID, subject.TeamIDs)
	if err != nil {
		return unavailable()
	}
	result.ScopeOptionsTruncated = truncated
	for _, scope := range scopes {
		// Project creation trims its target; never silently rename a grant scope.
		if scope == "" || strings.TrimSpace(scope) != scope {
			continue
		}
		allowed, err := checker.Check(ctx, subject, consts.PermOrgCreateProject, authz.ResourceRef{OrgID: scope})
		if err != nil {
			return unavailable()
		}
		if allowed {
			result.ScopeOptions = append(result.ScopeOptions, firstBuildScopeOption{ScopeID: scope, Source: "role_grant"})
		}
	}
	configurationAllowed, err := checker.Check(ctx, subject, consts.PermOrgUpdate, authz.ResourceRef{})
	if err != nil {
		return unavailable()
	}
	if configurationAllowed {
		storage, err := repositories.FirstBuild.DefaultStorageConfigured(ctx)
		if err != nil {
			return unavailable()
		}
		signing, err := repositories.FirstBuild.SigningConfigured(ctx)
		if err != nil {
			return unavailable()
		}
		result.Configuration = firstBuildConfiguration{
			Access: "visible", DefaultStorageConfigured: &storage, AgentSigningConfigured: &signing,
		}
	}
	agentAllowed, err := checker.Check(ctx, subject, consts.PermAgentRead, authz.ResourceRef{})
	if err != nil {
		return unavailable()
	}
	if agentAllowed {
		now := time.Now()
		seen, err := repositories.FirstBuild.AgentEnabledRecentlySeen(ctx, now.Add(-2*time.Minute), now)
		if err != nil {
			return unavailable()
		}
		result.Agent = firstBuildAgent{Access: "visible", EnabledRecentlySeen: &seen}
	}
	return apphttp.Detail(c, result)
}
