//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	controlconfig "github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	controlservice "github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/pkg/cache"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/id"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"github.com/redis/go-redis/v9"
)

// terminalSessionCommands replaces only Redis transport for this local regression.
// Real RedisCache/client pipeline queuing and production login/auth/logout remain
// unchanged. FastCache supplies bounded TTL storage; absent GET returns redis.Nil.
// This is not Redis wire, MySQL, containerd or hosted browser acceptance evidence.
// The real-container fixture continues to require its real, pinged Redis client.
type terminalSessionCommands struct {
	store        *cache.FastCache
	mu           sync.Mutex
	pipelines    int
	failPipeline bool
}

// DialHook 禁止本地回归意外连接外部 Redis
func (*terminalSessionCommands) DialHook(redis.DialHook) redis.DialHook {
	return func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("local session regression forbids Redis network access")
	}
}

// ProcessHook 仅实现会话路径使用的缓存命令，未知命令明确失败
func (h *terminalSessionCommands) ProcessHook(redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if err := ctx.Err(); err != nil {
			cmd.SetErr(err)
			return err
		}
		args := cmd.Args()
		switch cmd.Name() {
		case "get":
			value, err := h.store.Get(ctx, args[1].(string)).Result()
			if err == nil && value == "" {
				err = redis.Nil
			}
			cmd.(*redis.StringCmd).SetVal(value)
			cmd.SetErr(err)
		case "set":
			// All authentication writes must retain the explicit positive TTL.
			if len(args) != 5 || (args[3] != "ex" && args[3] != "px") {
				cmd.SetErr(errors.New("session regression requires explicit SET expiration"))
				break
			}
			ttl := time.Duration(args[4].(int64)) * time.Second
			if args[3] == "px" {
				ttl = time.Duration(args[4].(int64)) * time.Millisecond
			}
			if ttl <= 0 {
				cmd.SetErr(errors.New("session regression requires positive TTL"))
				break
			}
			value, err := h.store.Set(ctx, args[1].(string), args[2], ttl).Result()
			cmd.(*redis.StatusCmd).SetVal(value)
			cmd.SetErr(err)
		case "del":
			keys := make([]string, len(args)-1)
			for i := range keys {
				keys[i] = args[i+1].(string)
			}
			value, err := h.store.Del(ctx, keys...).Result()
			cmd.(*redis.IntCmd).SetVal(value)
			cmd.SetErr(err)
		default:
			cmd.SetErr(fmt.Errorf("unsupported local session command: %s", cmd.Name()))
		}
		return cmd.Err()
	}
}

// ProcessPipelineHook 保留真实客户端排队及 Exec 边界，不伪造登录响应
func (h *terminalSessionCommands) ProcessPipelineHook(redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, commands []redis.Cmder) error {
		h.mu.Lock()
		h.pipelines++
		fail := h.failPipeline
		h.mu.Unlock()
		if fail {
			return errors.New("synthetic session pipeline unavailable")
		}
		if len(commands) != 3 {
			return errors.New("login must queue exactly three session writes")
		}
		for _, cmd := range commands {
			if cmd.Name() != "set" {
				return errors.New("login pipeline must contain SET commands")
			}
			if err := h.ProcessHook(nil)(ctx, cmd); err != nil {
				return err
			}
		}
		return nil
	}
}

// TestContainerTerminalRerunProductionSession 验证共享夹具的生产登录、会话缓存及撤销链路
func TestContainerTerminalRerunProductionSession(t *testing.T) {
	commands := &terminalSessionCommands{store: cache.NewFastCache(cache.FastCacheConfig{MaxBytes: 1024 * 1024})}
	t.Cleanup(commands.store.Clear)
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	client.AddHook(commands)
	t.Cleanup(func() { _ = client.Close() })
	store := cache.NewRedisCache(client)
	var services *controlservice.Services
	options := controllerWorkerFixtureOptions{repositoryCache: store, timeout: 30 * time.Second}
	options.configureRepositories = func(repositories *repo.Repositories) {
		adapter := repositories.GetDB()
		if err := adapter.Database().WithContext(context.Background()).AutoMigrate(&model.User{}, &model.UserExt{}, &model.Role{}, &model.RoleGrant{}, &model.Permission{}, &model.RolePermissionBinding{}, &model.Menu{}, &model.Team{}, &model.TeamMember{}); err != nil {
			t.Fatal(err)
		}
		var err error
		services, err = controlservice.ProvideServices(adapter, store, repositories, taskpayload.OfflineRuntimePolicy{})
		if err != nil || services.Authorizer == nil {
			t.Fatal("production service dependency construction failed", err)
		}
	}
	// Exercise the exact shared constructor that previously dropped the cache,
	// not an independently assembled UserRepo or injected authenticated identity.
	f := newControllerWorkerFixture(t, false, options)
	actor, role, permission := "terminal-session-"+id.GetUild(), "session-role-"+id.GetUild(), "session-permission-"+id.GetUild()
	for _, row := range []any{
		&model.User{UserID: actor, IsEnabled: 1},
		&model.UserExt{UserID: actor},
		&model.Role{RoleID: role, Name: role, IsEnabled: 1},
		&model.RoleGrant{GrantID: id.GetUild(), SubjectType: "user", SubjectID: actor, RoleID: role, ScopeType: "project", ScopeID: f.projectID, IsEnabled: 1},
		&model.Permission{PermissionID: permission, ResourceType: "pipeline", Action: "read"},
		&model.RolePermissionBinding{RoleID: role, PermissionID: permission},
		&model.Menu{MenuID: id.GetUild(), Name: "session-menu", Path: "/session-test", PermissionID: permission, IsEnabled: 1},
	} {
		if err := f.db.WithContext(f.ctx).Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	var h *terminalRerunHTTP
	latestLoginStarted := time.Now()
	loginPersisted := func(ctx context.Context) bool {
		ext, err := f.repos.UserExt.Get(ctx, actor)
		return err == nil && ext.LastLoginAt != nil && !ext.LastLoginAt.Before(latestLoginStarted)
	}
	// Register before HTTP login so later fatal assertions cannot close the
	// database underneath the production asynchronous timestamp update.
	t.Cleanup(func() {
		if h == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		for !loginPersisted(ctx) {
			if ctx.Err() != nil {
				t.Error("last successful login timestamp did not drain before cleanup")
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	h = newTerminalRerunHTTP(t, f, services, store, &controlconfig.AppConfig{}, []string{actor})
	f.wait("first login timestamp is persisted", func() bool { return loginPersisted(f.ctx) })
	for name, injected := range map[string]cache.ICache{
		"user":    f.repos.User.(*repo.UserRepo).ICache,
		"agent":   f.repos.Agent.(*repo.AgentRepo).ICache,
		"storage": f.repos.Storage.(*repo.StorageRepo).ICache,
		"setting": f.repos.Setting.(*repo.SettingRepo).ICache,
	} {
		if injected != store {
			t.Fatalf("%s repository does not share the session cache", name)
		}
	}
	firstToken := h.tokens[actor]
	keys := []string{consts.UserTokenKey + actor, consts.UserRefreshTokenKey + actor, consts.UserInfoKey + actor}
	var access, refresh apphttp.TokenInfo
	var user model.UserInfo
	for i, out := range []any{&access, &refresh, &user} {
		value, err := store.Get(f.ctx, keys[i]).Result()
		if err != nil || sonic.UnmarshalString(value, out) != nil {
			t.Fatal("production login did not persist all session records")
		}
		ttl := commands.store.TTL(f.ctx, keys[i]).Val()
		if ttl <= 0 || ttl > 5*time.Minute {
			t.Fatal("session expiration was omitted or widened")
		}
	}
	if access.AccessToken != firstToken || access.RefreshToken != "" || refresh.AccessToken != "" || refresh.RefreshToken == "" || user.UserID != actor || user.Username != actor {
		t.Fatal("session records lost exact user/token separation")
	}
	roles, routes, err := services.User.GetUserRolesAndRoutes(actor, "")
	if err != nil || len(roles) != 1 || roles[0].RoleID != role || len(routes) != 1 || routes[0] != "/session-test" {
		t.Fatal("real role/menu dependency path was not exercised", err)
	}
	h.request(t, f.ctx, actor, http.MethodGet, "/users/fetch", nil, &user)
	if user.UserID != actor {
		t.Fatal("authenticated route changed the actual login identity")
	}

	// JWT timestamps have one-second precision: cross that boundary naturally
	// before a second real login, rather than synthesizing or editing a token.
	timer := time.NewTimer(time.Until(time.Now().Truncate(time.Second).Add(time.Second)) + 10*time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	secondLoginStarted := time.Now()
	h.login(t, f.ctx, actor)
	latestLoginStarted = secondLoginStarted
	f.wait("last successful login timestamp is persisted before fixture cleanup", func() bool { return loginPersisted(f.ctx) })
	if h.tokens[actor] == firstToken {
		t.Fatal("second production login did not replace the session")
	}
	terminalSessionResponse(t, h, f.ctx, http.MethodGet, "/users/fetch", firstToken, nil, http.StatusUnauthorized)
	h.request(t, f.ctx, actor, http.MethodGet, "/users/fetch", nil, &user)
	if err := f.db.WithContext(f.ctx).Model(&model.User{}).Where("user_id = ?", actor).Update("is_enabled", 0).Error; err != nil {
		t.Fatal(err)
	}
	terminalSessionResponse(t, h, f.ctx, http.MethodGet, "/users/fetch", h.tokens[actor], nil, http.StatusForbidden)
	if err := f.db.WithContext(f.ctx).Model(&model.User{}).Where("user_id = ?", actor).Update("is_enabled", 1).Error; err != nil {
		t.Fatal(err)
	}
	h.request(t, f.ctx, actor, http.MethodPost, "/users/logout", nil, nil)
	for _, key := range keys {
		if _, err := store.Get(f.ctx, key).Result(); !errors.Is(err, redis.Nil) {
			t.Fatal("logout retained a session cache record")
		}
	}
	terminalSessionResponse(t, h, f.ctx, http.MethodGet, "/users/fetch", h.tokens[actor], nil, http.StatusUnauthorized)

	login := map[string]string{"username": actor, "password": base64.StdEncoding.EncodeToString([]byte("wrong-synthetic-password"))}
	terminalSessionResponse(t, h, f.ctx, http.MethodPost, "/users/login", "", login, http.StatusOK)
	commands.mu.Lock()
	commands.failPipeline = true
	commands.mu.Unlock()
	login["password"] = base64.StdEncoding.EncodeToString([]byte(h.passwords[actor]))
	terminalSessionResponse(t, h, f.ctx, http.MethodPost, "/users/login", "", login, http.StatusOK)
	for _, key := range keys {
		if _, err := store.Get(f.ctx, key).Result(); !errors.Is(err, redis.Nil) {
			t.Fatal("failed session persistence created usable authentication")
		}
	}
	commands.mu.Lock()
	pipelines := commands.pipelines
	commands.mu.Unlock()
	if pipelines != 3 {
		t.Fatal("wrong-password or failed-cache login bypassed the production pipeline boundary")
	}
	t.Log("local production HTTP login/cache/auth/logout regression passed; Redis transport and database are local substitutes, not hosted acceptance")
}

func terminalSessionResponse(t *testing.T, h *terminalRerunHTTP, ctx context.Context, method, path, token string, body any, status int) {
	t.Helper()
	raw, err := sonic.Marshal(body)
	if err != nil {
		t.Fatal("negative session request encoding failed")
	}
	request, err := http.NewRequestWithContext(ctx, method, h.base+"/api/v1"+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal("negative session request construction failed")
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := h.client.Do(request)
	if err != nil {
		t.Fatal("negative session request failed")
	}
	defer response.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(response.Body, 16385))
	var envelope struct {
		Code   int `json:"code"`
		Detail struct {
			Token map[string]string `json:"token"`
		} `json:"detail"`
	}
	if err != nil || len(raw) > 16384 || sonic.Unmarshal(raw, &envelope) != nil || response.StatusCode != status || envelope.Code == 200 || len(envelope.Detail.Token) != 0 {
		t.Fatal("production session route did not preserve its fail-closed envelope")
	}
}
