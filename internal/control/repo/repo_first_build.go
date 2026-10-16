// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package repo

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/bytedance/sonic"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// FirstBuildScopeLimit 限制首次构建提示读取的授权作用域候选数量
const FirstBuildScopeLimit = 100

// IFirstBuildRepository 提供权限校验后的最小只读主库数据，不返回配置值或基础设施标识
// 配置只反映持久化状态，不保证连通性、启动配置快照已更新或构建可执行
// ScopeCandidates 返回有效授权而非组织成员关系，截断后仍须实时权限检查
type IFirstBuildRepository interface {
	ScopeCandidates(ctx context.Context, userID string, teamIDs []string) ([]string, bool, error)
	DefaultStorageConfigured(ctx context.Context) (bool, error)
	SigningConfigured(ctx context.Context) (bool, error)
	AgentEnabledRecentlySeen(ctx context.Context, since, now time.Time) (bool, error)
}

// FirstBuildRepo 实现首次构建提示的窄范围主库读取，不使用缓存或外部服务探测
type FirstBuildRepo struct {
	database.IDatabase
}

// NewFirstBuildRepo 创建首次构建提示仓储
func NewFirstBuildRepo(db database.IDatabase) IFirstBuildRepository {
	return &FirstBuildRepo{IDatabase: db}
}

// ScopeCandidates 返回当前用户及有效团队的去重组织授权作用域，先过滤失效授权及角色再限量
func (r *FirstBuildRepo) ScopeCandidates(ctx context.Context, userID string, teamIDs []string) ([]string, bool, error) {
	if userID == "" {
		return nil, false, errors.New("first-build subject is unavailable")
	}
	// Keep indexed predicates and additionally compare bytes: MySQL's default
	// collation must not turn a case-colliding user/team/role into this subject.
	// HEX is supported by both configured database drivers (MySQL and SQLite).
	subjects := r.Database().WithContext(ctx).
		Where("subject_type = ? AND subject_id = ?", string(consts.SubjectTypeUser), userID).
		Where("HEX(subject_type) = ? AND HEX(subject_id) = ?", firstBuildHex(string(consts.SubjectTypeUser)), firstBuildHex(userID))
	if len(teamIDs) > 0 {
		teamHexIDs := make([]string, 0, len(teamIDs))
		for _, id := range teamIDs {
			teamHexIDs = append(teamHexIDs, firstBuildHex(id))
		}
		teams := r.Database().WithContext(ctx).
			Where("subject_type = ? AND subject_id IN ?", string(consts.SubjectTypeTeam), teamIDs).
			Where("HEX(subject_type) = ? AND HEX(subject_id) IN ?", firstBuildHex(string(consts.SubjectTypeTeam)), teamHexIDs)
		subjects = subjects.Or(teams)
	}
	enabledRoles := r.Database().WithContext(ctx).Clauses(database.Write()).Model(&model.Role{}).
		Select("HEX(role_id)").Where("is_enabled = ?", 1)
	var encodedScopes []string
	err := r.Database().WithContext(ctx).Clauses(database.Write()).Model(&model.RoleGrant{}).
		Select("HEX(scope_id)").Distinct().Where(subjects).
		Where("scope_type = ? AND scope_id <> ? AND is_enabled = ?", string(consts.ScopeTypeOrganization), "", 1).
		Where("HEX(scope_type) = ?", firstBuildHex(string(consts.ScopeTypeOrganization))).
		Where("expires_at IS NULL OR expires_at > ?", time.Now()).
		Where("HEX(role_id) IN (?)", enabledRoles).
		Order("HEX(scope_id) ASC").Limit(FirstBuildScopeLimit+1).Pluck("scope_id", &encodedScopes).Error
	if err != nil {
		return nil, false, errors.New("first-build scopes are unavailable")
	}
	// Distinct/order operate on bytes so case-distinct scope IDs never collapse.
	scopes := make([]string, 0, len(encodedScopes))
	for _, encoded := range encodedScopes {
		decoded, err := hex.DecodeString(encoded)
		if err != nil || !utf8.Valid(decoded) {
			return nil, false, errors.New("first-build scopes are unavailable")
		}
		scopes = append(scopes, string(decoded))
	}
	truncated := len(scopes) > FirstBuildScopeLimit
	if truncated {
		scopes = scopes[:FirstBuildScopeLimit]
	}
	return scopes, truncated, nil
}

func firstBuildHex(value string) string {
	return strings.ToUpper(hex.EncodeToString([]byte(value)))
}

// DefaultStorageConfigured 仅查询启用默认存储的存在性，不读取地址或凭证
func (r *FirstBuildRepo) DefaultStorageConfigured(ctx context.Context) (bool, error) {
	var rows []struct{ IsDefault int }
	err := r.Database().WithContext(ctx).Clauses(database.Write()).Model(&model.StorageConfig{}).
		Select("is_default").Where("is_default = ? AND is_enabled = ?", 1, 1).Limit(1).Find(&rows).Error
	if err != nil {
		return false, errors.New("first-build storage configuration is unavailable")
	}
	return len(rows) == 1, nil
}

// SigningConfigured 仅返回签名密钥和盐是否齐备，不向调用方返回配置值
func (r *FirstBuildRepo) SigningConfigured(ctx context.Context) (bool, error) {
	var row struct{ Value datatypes.JSON }
	err := r.Database().WithContext(ctx).Clauses(database.Write()).Model(&model.Setting{}).
		Select("value").Where("name = ?", consts.SettingNameAgentSecretKey).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("first-build signing configuration is unavailable")
	}
	var setting struct {
		SecretKey string `json:"secret_key"`
		Salt      string `json:"salt"`
	}
	if err := sonic.Unmarshal(row.Value, &setting); err != nil {
		return false, errors.New("first-build signing configuration is unavailable")
	}
	return strings.TrimSpace(setting.SecretKey) != "" && strings.TrimSpace(setting.Salt) != "", nil
}

// AgentEnabledRecentlySeen 只返回近期报告在线或空闲的启用 Agent 是否存在
func (r *FirstBuildRepo) AgentEnabledRecentlySeen(ctx context.Context, since, now time.Time) (bool, error) {
	var rows []struct{ IsEnabled int }
	err := r.Database().WithContext(ctx).Clauses(database.Write()).Model(&model.Agent{}).
		Select("is_enabled").Where("is_enabled = ? AND status IN ?", 1, []int{1, 4}).
		Where("last_heartbeat >= ? AND last_heartbeat <= ?", since, now).Limit(1).Find(&rows).Error
	if err != nil {
		return false, errors.New("first-build agent state is unavailable")
	}
	return len(rows) == 1, nil
}
