package service

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type registrationTestDatabase struct{ db *gorm.DB }

func (d registrationTestDatabase) Database() *gorm.DB { return d.db }

func registrationServiceFixture(t *testing.T, ch cache.ICache) (*UserService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "registration.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.User{}, &model.UserExt{}, &model.RoleGrant{}); err != nil {
		t.Fatal(err)
	}
	adapter := registrationTestDatabase{db: db}
	return NewUserService(ch, repo.NewUserRepo(adapter, ch), repo.NewUserExtRepo(adapter),
		repo.NewRoleGrantRepo(adapter), nil, nil, nil, nil), db
}

func TestRegisterHashesDecodedPasswordForLogin(t *testing.T) {
	svc, _ := registrationServiceFixture(t, nil)
	password := "Synthetic-Registration-Password!"
	encoded := base64.StdEncoding.EncodeToString([]byte(password))
	register := &model.Register{Username: "registration-test", Password: encoded}
	if err := svc.Register(register); err != nil {
		t.Fatal(err)
	}
	// Use the production login repository, not a fake record or a mocked hash.
	user, err := svc.userRepo.Login(&model.Login{Username: register.Username})
	if err != nil {
		t.Fatal(err)
	}
	if !comparePassword(user.Password, password) || comparePassword(user.Password, encoded) {
		t.Fatal("registration hash does not match the single-decoded login password")
	}
	if _, err := svc.Login(&model.Login{Username: register.Username, Password: encoded}, http.Auth{
		SecretKey: "synthetic-signing-key", AccessExpire: time.Minute, RefreshExpire: time.Minute,
	}); err == nil || err.Error() != "cache not available" {
		t.Fatalf("real login must pass password/role checks and reach the intentionally absent session store: %v", err)
	}
	wrong := base64.StdEncoding.EncodeToString([]byte("wrong-password"))
	if _, err := svc.Login(&model.Login{Username: register.Username, Password: wrong}, http.Auth{}); err == nil || err.Error() != "Invalid credentials" {
		t.Fatalf("wrong password passed credential validation: %v", err)
	}
}

func TestRegisterRejectsInvalidEncodedPasswordBeforePersistence(t *testing.T) {
	svc := &UserService{}
	for _, req := range []*model.Register{nil, {}, {Username: "test"},
		{Username: "test", Password: "not base64!"}, {Username: "test", Password: "Zh=="}} {
		if err := svc.Register(req); err == nil {
			t.Fatal("invalid registration unexpectedly reached persistence")
		}
	}
}

func TestRegistrationFixDoesNotRewriteLegacyPasswordHashes(t *testing.T) {
	svc, db := registrationServiceFixture(t, nil)
	password := "Synthetic-Legacy-Password!"
	encoded := base64.StdEncoding.EncodeToString([]byte(password))
	legacyHash, err := getPassword(encoded)
	if err != nil {
		t.Fatal(err)
	}
	legacy := &model.User{UserID: "legacy-test-user", Username: "legacy-test", Password: string(legacyHash), IsEnabled: 1}
	if err := db.WithContext(context.Background()).Create(legacy).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Login(&model.Login{Username: legacy.Username, Password: encoded}, http.Auth{}); err == nil || err.Error() != "Invalid credentials" {
		t.Fatalf("legacy invalid hash unexpectedly acquired fallback authentication: %v", err)
	}
	saved, err := svc.userRepo.GetUserByUserID(legacy.UserID)
	if err != nil || saved.Password != string(legacyHash) {
		t.Fatal("legacy password hash was automatically rewritten", err)
	}
}

// TestRegisterLoginWithRealRedis 仅在显式启用真实依赖时验证完整会话写入
func TestRegisterLoginWithRealRedis(t *testing.T) {
	if os.Getenv("ARCENTRA_REAL_STACK") != "1" {
		t.Skip("set ARCENTRA_REAL_STACK=1 for the disposable Redis integration")
	}
	address := os.Getenv("ARCENTRA_TEST_REDIS_ADDRESS")
	if address != "127.0.0.1:16379" {
		t.Fatal("real registration test requires the synthetic loopback Redis fixture")
	}
	client := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	svc, _ := registrationServiceFixture(t, cache.NewRedisCache(client))
	password := "Synthetic-Real-Registration!"
	encoded := base64.StdEncoding.EncodeToString([]byte(password))
	register := &model.Register{Username: "real-registration-test", Password: encoded}
	if err := svc.Register(register); err != nil {
		t.Fatal(err)
	}
	resp, err := svc.Login(&model.Login{Username: register.Username, Password: encoded}, http.Auth{
		SecretKey: "synthetic-registration-signing-key", AccessExpire: time.Minute, RefreshExpire: time.Minute,
	})
	if err != nil || resp == nil || resp.UserInfo.UserID != register.UserID || resp.Token["accessToken"] == "" {
		t.Fatal("actual register → login session failed", err)
	}
	if err := svc.Logout(register.UserID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		ext, err := svc.userExtRepo.Get(ctx, register.UserID)
		if err == nil && ext.LastLoginAt != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("login timestamp update did not complete")
}
