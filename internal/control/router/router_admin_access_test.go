package router

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/pkg/cache"
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type adminSessions struct {
	cache.ICache
	values map[string]string
}

func (s *adminSessions) Get(_ context.Context, key string) *redis.StringCmd {
	if value, ok := s.values[key]; ok {
		return redis.NewStringResult(value, nil)
	}
	return redis.NewStringResult("", redis.Nil)
}

type adminFixture struct {
	app    *fiber.App
	rt     *Router
	db     *gorm.DB
	tokens map[string]string
}

func newAdminFixture(t *testing.T) adminFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "admin.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sql, err := db.DB()
	require.NoError(t, err)
	sql.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sql.Close() })
	require.NoError(t, repo.MigrateSchema(context.Background(), db))
	repositories := repo.NewRepositories(collectionTestDB{db}, nil)
	sessions := &adminSessions{values: map[string]string{}}
	f := adminFixture{db: db, tokens: map[string]string{}}
	for _, user := range []string{"admin", "ordinary", "project-owner", "org-owner", "other"} {
		require.NoError(t, db.Create(&model.User{UserID: user, Username: user, FullName: user, IsEnabled: 1}).Error)
		require.NoError(t, db.Create(&model.UserExt{UserID: user, Timezone: "UTC", InvitationStatus: "accepted"}).Error)
		token, _, err := jwt.GenToken(user, []byte("admin-test-secret"), time.Hour, time.Hour)
		require.NoError(t, err)
		f.tokens[user] = token
		raw, err := json.Marshal(apphttp.TokenInfo{AccessToken: token})
		require.NoError(t, err)
		sessions.values[consts.UserTokenKey+user] = string(raw)
	}
	// Exercise the real explicit first-admin grant, rather than a special header.
	require.NoError(t, repo.BootstrapAdmin(context.Background(), db, "admin"))
	for _, scope := range []struct{ user, kind string }{{"project-owner", "project"}, {"org-owner", "org"}} {
		require.NoError(t, db.Create(&model.RoleGrant{GrantID: scope.user, SubjectType: "user", SubjectID: scope.user, RoleID: consts.RolePlatformSuperAdmin, ScopeType: scope.kind, ScopeID: "foreign", IsEnabled: 1}).Error)
	}
	f.rt = &Router{HTTP: &apphttp.HTTP{Auth: apphttp.Auth{SecretKey: "admin-test-secret"}}, Services: service.NewServices(collectionTestDB{db}, nil, repositories)}
	f.app = fiber.New()
	f.app.Use(middleware.UnifiedResponseMiddleware())
	auth := middleware.AuthorizationMiddleware("admin-test-secret", sessions)
	f.rt.settingRouter(f.app, auth)
	f.rt.storageRouter(f.app, auth)
	f.rt.registrationTokenRouter(f.app, auth)
	f.rt.userRouter(f.app, auth)
	f.rt.userExtRouter(f.app, auth)
	f.rt.identityRouter(f.app, auth)
	return f
}
func (f adminFixture) request(t *testing.T, user, method, path, body string) (int, map[string]any, string) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if user != "" {
		r.Header.Set("Authorization", "Bearer "+f.tokens[user])
	}
	// Race-instrumented bcrypt can exceed Fiber's default one-second test budget
	// on a shared CI runner; keep a bounded wait without changing password cost.
	res, err := f.app.Test(r, routerTestTimeoutMillis)
	require.NoError(t, err)
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal(raw, &result), string(raw))
	return res.StatusCode, result, string(raw)
}

func TestSensitiveRoutesRejectAnonymousUnprivilegedAndForeignScopes(t *testing.T) {
	f := newAdminFixture(t)
	for _, route := range []struct{ method, path, body string }{
		{"GET", "/identity/providers", ""}, {"GET", "/identity/providers/types", ""}, {"GET", "/identity/providers/test", ""}, {"POST", "/identity/providers", `{}`}, {"PUT", "/identity/providers/test", `{}`}, {"PUT", "/identity/providers/test/toggle", `{}`}, {"DELETE", "/identity/providers/test", ""},
		{"GET", "/settings/", ""}, {"GET", "/settings/AGENT_SECRET_KEY", ""}, {"PUT", "/settings/AGENT_SECRET_KEY", `{"value":{"secret_key":"bad"}}`}, {"DELETE", "/settings/AGENT_SECRET_KEY", ""},
		{"GET", "/storage/configs", ""}, {"GET", "/storage/configs/default", ""}, {"GET", "/storage/configs/id", ""}, {"POST", "/storage/configs", `{}`}, {"PUT", "/storage/configs/id", `{}`}, {"DELETE", "/storage/configs/id", ""}, {"POST", "/storage/configs/id/default", `{}`},
		{"GET", "/registration-tokens", ""}, {"POST", "/registration-tokens", `{"description":"forbidden"}`}, {"DELETE", "/registration-tokens/1", ""},
		{"GET", "/users/", ""}, {"GET", "/users/by-role?roleID=platform_super_admin", ""}, {"POST", "/users/invite", `{"email":"other@example.invalid"}`}, {"PUT", "/users/other", `{"isEnabled":0}`}, {"PUT", "/users/other/password", `{"newPassword":"bmV3LXBhc3N3b3Jk"}`},
		{"GET", "/users/other/ext/", ""}, {"PUT", "/users/other/ext/timezone", `{"timezone":"UTC"}`}, {"PUT", "/users/other/ext/", `{"userId":"admin"}`}, {"PUT", "/users/ordinary/ext/invitation", `{"status":"accepted"}`},
	} {
		for _, user := range []string{"", "ordinary", "project-owner", "org-owner"} {
			status, _, _ := f.request(t, user, route.method, route.path, route.body)
			want := 403
			if user == "" {
				want = 401
			}
			require.Equal(t, want, status, user+" "+route.method+" "+route.path)
		}
	}
	var count int64
	require.NoError(t, f.db.Model(&model.RegistrationToken{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestSensitiveAdminCredentialsAreWriteOnlyAndBlankUpdatesPreserve(t *testing.T) {
	f := newAdminFixture(t)
	status, result, raw := f.request(t, "admin", "PUT", "/settings/AGENT_SECRET_KEY", `{"value":{"salt":"secret-salt-canary","secret_key":"secret-key-canary"}}`)
	require.Equal(t, 200, status)
	require.NotContains(t, raw, "secret-key-canary")
	require.NotContains(t, raw, "secret-salt-canary")
	require.ElementsMatch(t, []any{"salt", "secret_key"}, result["detail"].(map[string]any)["configuredFields"])
	for _, path := range []string{"/settings/", "/settings/AGENT_SECRET_KEY"} {
		status, _, raw = f.request(t, "admin", "GET", path, "")
		require.Equal(t, 200, status)
		require.NotContains(t, raw, "canary")
	}
	status, _, _ = f.request(t, "admin", "PUT", "/settings/AGENT_SECRET_KEY", `{"value":{"secret_key":"","salt":null}}`)
	require.Equal(t, 200, status)
	setting, err := f.rt.Services.Repos().Setting.Get(context.Background(), "AGENT_SECRET_KEY")
	require.NoError(t, err)
	require.Contains(t, string(setting.Value), "secret-key-canary")
	require.Contains(t, string(setting.Value), "secret-salt-canary")
	for _, value := range []string{"[redacted]", "********", "configured"} {
		status, _, _ = f.request(t, "admin", "PUT", "/settings/AGENT_SECRET_KEY", `{"value":{"secret_key":"`+value+`"}}`)
		require.Equal(t, 400, status)
	}
	status, _, _ = f.request(t, "admin", "PUT", "/settings/GENERAL", `{"name":"AGENT_SECRET_KEY","value":{}}`)
	require.Equal(t, 400, status)
	config := base64.StdEncoding.EncodeToString([]byte(`{"endpoint":"127.0.0.1:9000","bucket":"test","accessKey":"access-canary","secretKey":"storage-canary","useTLS":false}`))
	status, _, raw = f.request(t, "admin", "POST", "/storage/configs", `{"storageId":"test","name":"Test","storageType":"minio","isDefault":1,"config":"`+config+`"}`)
	require.Equal(t, 200, status)
	require.NotContains(t, raw, "canary")
	for _, path := range []string{"/storage/configs", "/storage/configs/default", "/storage/configs/test"} {
		status, _, raw = f.request(t, "admin", "GET", path, "")
		require.Equal(t, 200, status)
		require.NotContains(t, raw, "canary")
	}
	config = base64.StdEncoding.EncodeToString([]byte(`{"secretKey":"","accessKey":null,"bucket":"changed"}`))
	status, _, raw = f.request(t, "admin", "PUT", "/storage/configs/test", `{"config":"`+config+`","isEnabled":1}`)
	require.Equal(t, 200, status)
	require.NotContains(t, raw, "canary")
	stored, err := f.rt.Services.Storage.GetStorageConfig(context.Background(), "test")
	require.NoError(t, err)
	require.Contains(t, string(stored.Config), "storage-canary")
	require.Contains(t, string(stored.Config), "access-canary")
	require.Contains(t, string(stored.Config), "changed")
}

func TestSensitiveLiveRevocationDoesNotReuseCachedAuthority(t *testing.T) {
	f := newAdminFixture(t)
	status, _, _ := f.request(t, "admin", "GET", "/settings/", "")
	require.Equal(t, 200, status)
	require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("subject_id = ?", "admin").Update("is_enabled", 0).Error)
	status, _, _ = f.request(t, "admin", "GET", "/settings/", "")
	require.Equal(t, 403, status)
	require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("subject_id = ?", "admin").Update("is_enabled", 1).Error)
	past := time.Now().Add(-time.Minute)
	require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("subject_id = ?", "admin").Update("expires_at", past).Error)
	status, _, _ = f.request(t, "admin", "POST", "/registration-tokens", `{"description":"revoked"}`)
	require.Equal(t, 403, status)
	require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("subject_id = ?", "admin").Update("expires_at", nil).Error)
	require.NoError(t, f.db.Model(&model.Role{}).Where("role_id = ?", consts.RolePlatformSuperAdmin).Update("is_enabled", 0).Error)
	status, _, _ = f.request(t, "admin", "GET", "/settings/", "")
	require.Equal(t, 403, status)
	require.NoError(t, f.db.Model(&model.Role{}).Where("role_id = ?", consts.RolePlatformSuperAdmin).Update("is_enabled", 1).Error)
	require.NoError(t, f.db.Model(&model.User{}).Where("user_id = ?", "admin").Update("is_enabled", 0).Error)
	status, _, _ = f.request(t, "admin", "GET", "/settings/", "")
	require.Equal(t, 403, status)
}

func TestSensitiveSelfServiceRemainsScopedAndAdminCanResetOthers(t *testing.T) {
	f := newAdminFixture(t)
	for _, request := range []struct{ method, path, body string }{
		{"GET", "/users/fetch", ""}, {"GET", "/users/ordinary/ext/", ""}, {"PUT", "/users/ordinary", `{"fullName":"My profile"}`}, {"PUT", "/users/ordinary/ext/timezone", `{"timezone":"Europe/Paris"}`},
	} {
		status, _, _ := f.request(t, "ordinary", request.method, request.path, request.body)
		require.Equal(t, 200, status, request.path)
	}
	status, _, _ := f.request(t, "ordinary", "PUT", "/users/ordinary", `{"isEnabled":1}`)
	require.Equal(t, 403, status)
	status, _, _ = f.request(t, "ordinary", "PUT", "/users/ordinary/ext/", `{"userId":"admin","invitationStatus":"accepted"}`)
	require.Equal(t, 403, status)
	status, _, _ = f.request(t, "ordinary", "POST", "/users/refresh?userID=admin", "")
	require.Equal(t, 403, status)
	password := base64.StdEncoding.EncodeToString([]byte("new-synthetic-password"))
	status, _, _ = f.request(t, "ordinary", "PUT", "/users/me/password", `{"newPassword":"`+password+`"}`)
	require.Equal(t, 200, status)
	status, _, _ = f.request(t, "admin", "PUT", "/users/other/password", `{"newPassword":"`+password+`"}`)
	require.Equal(t, 200, status)
	user, err := f.rt.Services.Repos().User.GetUserByUserID("other")
	require.NoError(t, err)
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(user.Password), []byte("new-synthetic-password")))
	status, result, _ := f.request(t, "admin", "POST", "/registration-tokens", `{"description":"test","createdBy":"spoofed"}`)
	require.Equal(t, 200, status)
	detail := result["detail"].(map[string]any)
	require.Equal(t, "admin", detail["createdBy"])
	require.NotEmpty(t, detail["token"])
	status, _, raw := f.request(t, "admin", "GET", "/registration-tokens", "")
	require.Equal(t, 200, status)
	require.NotContains(t, raw, detail["token"])
}

func TestSensitiveNestedSecretsAndContainerReplacementFailClosed(t *testing.T) {
	f := newAdminFixture(t)
	require.NoError(t, f.db.Create(&model.Role{RoleID: "settings-editor", Name: "Settings editor", IsEnabled: 1}).Error)
	require.NoError(t, f.db.Create(&model.RolePermissionBinding{RoleID: "settings-editor", PermissionID: consts.PermOrgUpdate}).Error)
	require.NoError(t, f.db.Create(&model.RoleGrant{GrantID: "settings-editor", SubjectType: "user", SubjectID: "ordinary", RoleID: "settings-editor", ScopeType: "platform", IsEnabled: 1}).Error)
	status, _, _ := f.request(t, "ordinary", "PUT", "/settings/GENERAL", `{"value":{"host":"safe"}}`)
	require.Equal(t, 200, status)
	for _, value := range []string{`{"providers":[{"password":"nested-canary"}]}`, `{"provider":{"password":"nested-canary"}}`} {
		status, _, _ = f.request(t, "ordinary", "PUT", "/settings/GENERAL", `{"value":`+value+`}`)
		require.Equal(t, 403, status)
	}
	status, _, raw := f.request(t, "admin", "PUT", "/settings/GENERAL", `{"value":{"provider":{"password":"stored-canary","host":"old"},"providers":[{"password":"nested-canary"}]}}`)
	require.Equal(t, 200, status)
	require.NotContains(t, raw, "canary")
	status, result, raw := f.request(t, "ordinary", "GET", "/settings/GENERAL", "")
	require.Equal(t, 200, status)
	require.NotContains(t, raw, "canary")
	require.ElementsMatch(t, []any{"provider.password", "providers.0.password"}, result["detail"].(map[string]any)["configuredFields"])
	for _, value := range []string{`{"provider":null}`, `{"provider":[]}`, `{"provider":"replace"}`, `{"providers":[]}`, `{"providers":[null]}`, `{"providers":[{"password":"[redacted]"}]}`} {
		status, _, _ = f.request(t, "admin", "PUT", "/settings/GENERAL", `{"value":`+value+`}`)
		require.Equal(t, 400, status, value)
	}
	status, _, _ = f.request(t, "ordinary", "PUT", "/settings/GENERAL", `{"value":{"provider":{"host":"new"},"providers":[{}]}}`)
	require.Equal(t, 200, status)
	stored, err := f.rt.Services.Repos().Setting.Get(context.Background(), "GENERAL")
	require.NoError(t, err)
	require.Contains(t, string(stored.Value), "stored-canary")
	require.Contains(t, string(stored.Value), "nested-canary")
}

func TestSensitiveIdentityManagementAndPublicMetadata(t *testing.T) {
	f := newAdminFixture(t)
	status, _, raw := f.request(t, "admin", "POST", "/identity/providers", `{"name":"test","providerType":"oauth","isEnabled":1,"config":{"clientId":"public-client","clientSecret":"identity-canary","tokenURL":"https://example.invalid/token"}}`)
	require.Equal(t, 200, status)
	require.NotContains(t, raw, "identity-canary")
	for _, item := range []struct{ user, path string }{{"admin", "/identity/providers/test"}, {"admin", "/identity/providers"}, {"", "/identity/login/providers"}} {
		status, _, raw = f.request(t, item.user, "GET", item.path, "")
		require.Equal(t, 200, status, item.path)
		require.NotContains(t, raw, "identity-canary")
	}
	status, _, _ = f.request(t, "admin", "PUT", "/identity/providers/test", `{"config":{"clientSecret":"","scopes":["email"]}}`)
	require.Equal(t, 200, status)
	stored, err := f.rt.Services.Identity.GetProvider(context.Background(), "test")
	require.NoError(t, err)
	require.Contains(t, string(stored.Config), "identity-canary")
	status, _, _ = f.request(t, "admin", "PUT", "/identity/providers/test", `{"config":{"clientSecret":"********"}}`)
	require.Equal(t, 400, status)
	// Existing unsafe endpoints cannot expose embedded credentials; new ones fail input validation.
	view := publicStorage(&model.StorageConfig{Config: []byte(`{"endpoint":"https://user:url-canary@example.invalid","accessKey":"access-canary","secretKey":"secret-canary"}`)})
	rawView, err := json.Marshal(view)
	require.NoError(t, err)
	require.NotContains(t, string(rawView), "canary")
	require.Error(t, validateStorageEndpoint([]byte(`{"endpoint":"https://user:url-canary@example.invalid"}`)))
}

func TestSensitiveTeamMembershipRevocationIsLive(t *testing.T) {
	f := newAdminFixture(t)
	require.NoError(t, f.db.Create(&model.Team{TeamID: "operators", Name: "Operators", IsEnabled: 1}).Error)
	require.NoError(t, f.db.Create(&model.TeamMember{TeamID: "operators", UserID: "ordinary"}).Error)
	require.NoError(t, f.db.Create(&model.RoleGrant{GrantID: "operators", SubjectType: "team", SubjectID: "operators", RoleID: consts.RolePlatformSuperAdmin, ScopeType: "platform", IsEnabled: 1}).Error)
	status, _, _ := f.request(t, "ordinary", "GET", "/settings/", "")
	require.Equal(t, 200, status)
	require.NoError(t, f.db.Where("team_id = ? AND user_id = ?", "operators", "ordinary").Delete(&model.TeamMember{}).Error)
	status, _, _ = f.request(t, "ordinary", "GET", "/settings/", "")
	require.Equal(t, 403, status)
}

func TestSensitivePublicRegistrationNeverGrantsAdministrativeAccess(t *testing.T) {
	f := newAdminFixture(t)
	password := base64.StdEncoding.EncodeToString([]byte("registration-synthetic-password"))
	status, result, _ := f.request(t, "", "POST", "/users/register", `{"username":"fresh","email":"fresh@example.invalid","password":"`+password+`","role":"platform_super_admin","isEnabled":1}`)
	require.Equal(t, 200, status)
	require.EqualValues(t, 200, result["code"])
	var user model.User
	require.NoError(t, f.db.Where("username = ?", "fresh").First(&user).Error)
	var count int64
	require.NoError(t, f.db.Model(&model.RoleGrant{}).Where("subject_id = ?", user.UserID).Count(&count).Error)
	require.Zero(t, count)
	require.Error(t, repo.BootstrapAdmin(context.Background(), f.db, "fresh"), "existing administrator prevents a second bootstrap")
}

func TestSensitiveCredentialURLReplacementAndMasks(t *testing.T) {
	previous := []byte(`{"provider":{"authURL":"https://user:url-canary@example.invalid"}}`)
	public, configured := redactConfig(previous, false)
	require.NotContains(t, string(public), "url-canary")
	require.Equal(t, []string{"provider.authURL"}, configured)
	_, _, err := mergeConfigSecrets(nil, previous, false)
	require.Error(t, err)
	_, _, err = mergeConfigSecrets(previous, []byte(`{"provider":null}`), false)
	require.Error(t, err)
	merged, sensitive, err := mergeConfigSecrets(previous, []byte(`{"provider":{"authURL":"https://safe.example.invalid"}}`), false)
	require.NoError(t, err)
	require.True(t, sensitive)
	require.NotContains(t, string(merged), "url-canary")
}
