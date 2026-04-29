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

package authz

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/casbin/casbin/v2"
	casbinModel "github.com/casbin/casbin/v2/model"
	"github.com/google/uuid"
)

const casbinModelText = `
[request_definition]
r = sub, obj, act, dom

[policy_definition]
p = sub, obj, act, dom

[role_definition]
g = _, _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = g(r.sub, p.sub, r.dom) && (p.obj == "*" || p.obj == r.obj) && (p.act == "*" || p.act == r.act) && (p.dom == "*" || p.dom == r.dom)
`

const wildcardActionID = "*:*"

// Authorizer 表示统一鉴权器实现
type Authorizer struct {
	repos    *repo.Repositories
	cache    cache.ICache
	enforcer *casbin.Enforcer
	mu       sync.RWMutex
}

// NewAuthorizer 创建统一鉴权器
func NewAuthorizer(repos *repo.Repositories, ch cache.ICache) (IAuthorizer, error) {
	if repos == nil {
		return nil, errors.New("repositories is nil")
	}
	m, err := casbinModel.NewModelFromString(casbinModelText)
	if err != nil {
		return nil, err
	}
	enforcer, err := casbin.NewEnforcer(m)
	if err != nil {
		return nil, err
	}
	a := &Authorizer{
		repos:    repos,
		cache:    ch,
		enforcer: enforcer,
	}
	if err = a.Reload(context.Background()); err != nil {
		return nil, err
	}
	return a, nil
}

// Reload 重新加载权限策略到 Casbin
func (a *Authorizer) Reload(ctx context.Context) error {
	permissions, err := a.repos.Permission.ListPermissions(ctx)
	if err != nil {
		return err
	}
	bindings, err := a.repos.Permission.ListAllRoleBindings(ctx)
	if err != nil {
		return err
	}
	grants, err := a.repos.RoleGrant.ListAllEnabled(ctx)
	if err != nil {
		return err
	}

	permMap := make(map[string]struct {
		ResourceType string
		Action       string
	}, len(permissions))
	for _, permission := range permissions {
		permMap[permission.PermissionID] = struct {
			ResourceType string
			Action       string
		}{
			ResourceType: permission.ResourceType,
			Action:       permission.Action,
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.enforcer.ClearPolicy()

	missing := make(map[string]struct{})
	for _, binding := range bindings {
		permDef, ok := permMap[binding.PermissionID]
		if !ok {
			missing[binding.PermissionID] = struct{}{}
			continue
		}
		_, _ = a.enforcer.AddPolicy(binding.RoleID, permDef.ResourceType, permDef.Action, "*")
	}
	if len(missing) > 0 {
		ids := make([]string, 0, len(missing))
		for id := range missing {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		log.Warnw(
			"role_permission_binding references unknown permission_id; bindings ignored",
			"missingPermissionIDs", ids,
		)
	}
	for _, grant := range grants {
		subject := buildSubjectKey(grant.SubjectType, grant.SubjectID)
		domain := buildScopeKey(grant.ScopeType, grant.ScopeID)
		_, _ = a.enforcer.AddGroupingPolicy(subject, grant.RoleID, domain)
	}
	return nil
}

// Check 检查单个操作权限
func (a *Authorizer) Check(ctx context.Context, subject Subject, action string, resource ResourceRef) (bool, error) {
	_ = ctx
	obj, act := splitAction(action)
	subjectKeys := buildSubjectKeys(subject)
	if len(subjectKeys) == 0 {
		return false, nil
	}
	candidateDomains := buildCandidateScopeKeys(resource)

	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, sub := range subjectKeys {
		for _, dom := range candidateDomains {
			allowed, err := a.enforcer.Enforce(sub, obj, act, dom)
			if err != nil {
				return false, err
			}
			if allowed {
				a.recordAudit(ctx, subject, action, resource, model.AuditResultSuccess, "")
				return true, nil
			}
		}
	}
	a.recordAudit(ctx, subject, action, resource, model.AuditResultDenied, "permission denied")
	return false, nil
}

// CheckAny 检查多个操作是否至少命中一个
func (a *Authorizer) CheckAny(ctx context.Context, subject Subject, actions []string, resource ResourceRef) (bool, error) {
	for _, action := range actions {
		allowed, err := a.Check(ctx, subject, action, resource)
		if err != nil {
			return false, err
		}
		if allowed {
			return true, nil
		}
	}
	return false, nil
}

// EffectiveActions 返回主体在指定作用域下的有效 action 列表
func (a *Authorizer) EffectiveActions(ctx context.Context, subject Subject, scopeType, scopeID string) ([]string, error) {
	key := buildEffectiveActionsCacheKey(subject, scopeType, scopeID)
	resource := resourceFromScope(scopeType, scopeID)
	if actions, ok := getCachedActions(ctx, a.cache, key); ok {
		containsWildcard := false
		for _, action := range actions {
			if action == wildcardActionID {
				containsWildcard = true
				break
			}
		}
		// 兼容历史缓存：若缓存未命中通配 action，实时补一次无审计判定
		if !containsWildcard {
			wildcardAllowed, err := a.checkNoAudit(subject, wildcardActionID, resource)
			if err != nil {
				return nil, err
			}
			if wildcardAllowed {
				actions = append(actions, wildcardActionID)
				sort.Strings(actions)
				setCachedActions(ctx, a.cache, key, actions)
			}
		}
		return actions, nil
	}

	permissions, err := a.repos.Permission.ListPermissions(ctx)
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{})
	for _, permission := range permissions {
		allowed, checkErr := a.Check(ctx, subject, permission.PermissionID, resource)
		if checkErr != nil {
			return nil, checkErr
		}
		if allowed {
			set[permission.PermissionID] = struct{}{}
		}
	}
	// 超级管理员可能通过通配策略获得权限（不依赖 permission_id 字典项）
	// 这里显式注入通配 action，供菜单过滤与前端守卫识别
	wildcardAllowed, wildcardErr := a.Check(ctx, subject, wildcardActionID, resource)
	if wildcardErr != nil {
		return nil, wildcardErr
	}
	if wildcardAllowed {
		set[wildcardActionID] = struct{}{}
	}
	actions := make([]string, 0, len(set))
	for action := range set {
		actions = append(actions, action)
	}
	sort.Strings(actions)
	setCachedActions(ctx, a.cache, key, actions)
	return actions, nil
}

// InvalidateSubjectCache 清理主体指定作用域缓存
func (a *Authorizer) InvalidateSubjectCache(ctx context.Context, subject Subject, scopeType, scopeID string) error {
	key := buildEffectiveActionsCacheKey(subject, scopeType, scopeID)
	if a.cache != nil {
		_ = a.cache.Del(ctx, key).Err()
	}
	return nil
}

func buildSubjectKeys(subject Subject) []string {
	keys := make([]string, 0, 1+len(subject.TeamIDs))
	if subject.UserID != "" {
		keys = append(keys, buildSubjectKey(string(consts.SubjectTypeUser), subject.UserID))
	}
	for _, teamID := range subject.TeamIDs {
		if teamID == "" {
			continue
		}
		keys = append(keys, buildSubjectKey(string(consts.SubjectTypeTeam), teamID))
	}
	return keys
}

func buildSubjectKey(subjectType, subjectID string) string {
	return subjectType + ":" + subjectID
}

func splitAction(action string) (string, string) {
	obj, act, ok := strings.Cut(action, ":")
	if !ok {
		return "*", action
	}
	if obj == "" {
		obj = "*"
	}
	return obj, act
}

func resourceFromScope(scopeType, scopeID string) ResourceRef {
	switch scopeType {
	case string(consts.ScopeTypeOrganization):
		return ResourceRef{OrgID: scopeID}
	case string(consts.ScopeTypeProject):
		return ResourceRef{ProjectID: scopeID}
	case string(consts.ScopeTypeTeam):
		return ResourceRef{TeamID: scopeID}
	case string(consts.ScopeTypePipeline):
		return ResourceRef{Type: string(consts.ScopeTypePipeline), ID: scopeID}
	default:
		return ResourceRef{}
	}
}

func (a *Authorizer) recordAudit(
	ctx context.Context,
	subject Subject,
	action string,
	resource ResourceRef,
	result string,
	reason string,
) {
	if a.repos == nil || a.repos.AuditLog == nil {
		return
	}
	if result == model.AuditResultSuccess && !shouldAuditAllow(action) {
		return
	}
	resourceType, _ := splitAction(action)
	entry := &model.AuditLog{
		LogID:        uuid.NewString(),
		ActorUserID:  subject.UserID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resource.ID,
		ScopeType:    firstNonEmpty(resource.Type, scopeTypeFromResource(resource)),
		ScopeID:      scopeIDFromResource(resource),
		Result:       result,
		Reason:       reason,
	}
	_ = a.repos.AuditLog.Create(ctx, entry)
}

func shouldAuditAllow(action string) bool {
	return strings.Contains(action, "deploy_prod") ||
		strings.HasPrefix(action, "secret:") ||
		strings.HasPrefix(action, "role:")
}

func scopeTypeFromResource(resource ResourceRef) string {
	if resource.ProjectID != "" {
		return string(consts.ScopeTypeProject)
	}
	if resource.TeamID != "" {
		return string(consts.ScopeTypeTeam)
	}
	if resource.OrgID != "" {
		return string(consts.ScopeTypeOrganization)
	}
	return string(consts.ScopeTypePlatform)
}

func scopeIDFromResource(resource ResourceRef) string {
	if resource.ID != "" {
		return resource.ID
	}
	if resource.ProjectID != "" {
		return resource.ProjectID
	}
	if resource.TeamID != "" {
		return resource.TeamID
	}
	if resource.OrgID != "" {
		return resource.OrgID
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (a *Authorizer) checkNoAudit(subject Subject, action string, resource ResourceRef) (bool, error) {
	obj, act := splitAction(action)
	subjectKeys := buildSubjectKeys(subject)
	if len(subjectKeys) == 0 {
		return false, nil
	}
	candidateDomains := buildCandidateScopeKeys(resource)

	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, sub := range subjectKeys {
		for _, dom := range candidateDomains {
			allowed, err := a.enforcer.Enforce(sub, obj, act, dom)
			if err != nil {
				return false, err
			}
			if allowed {
				return true, nil
			}
		}
	}
	return false, nil
}
