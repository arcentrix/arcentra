package interceptor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/bytedance/sonic"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type verifierTestDB struct{ db *gorm.DB }

func (d verifierTestDB) Database() *gorm.DB { return d.db }

type verifierPoisonedCache struct {
	cache.ICache
	values        map[string]string
	reads, writes int
}

func (c *verifierPoisonedCache) Get(_ context.Context, key string) *redis.StringCmd {
	c.reads++
	value, ok := c.values[key]
	if !ok {
		return redis.NewStringResult("", redis.Nil)
	}
	return redis.NewStringResult(value, nil)
}
func (c *verifierPoisonedCache) Set(_ context.Context, _ string, _ any, _ time.Duration) *redis.StatusCmd {
	c.writes++
	return redis.NewStatusResult("OK", nil)
}
func (c *verifierPoisonedCache) Del(_ context.Context, _ ...string) *redis.IntCmd {
	return redis.NewIntResult(0, nil)
}

type verifierCaseAgent struct {
	model.Agent
	AgentID string `gorm:"column:agent_id;type:text COLLATE NOCASE"`
}

func (verifierCaseAgent) TableName() string { return "agent" }

func newLiveVerifier(t *testing.T) (*gorm.DB, *service.AgentService, TokenVerifier, *verifierPoisonedCache) {
	t.Helper()
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "verifier.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.WithContext(ctx).AutoMigrate(&verifierCaseAgent{}, &model.Setting{}); err != nil {
		t.Fatal(err)
	}
	agent := model.Agent{AgentID: "Agent-A", IsEnabled: 1}
	secret := model.Setting{Name: consts.SettingNameAgentSecretKey, Value: []byte(`{"salt":"synthetic-salt","secret_key":"synthetic-current-key"}`)}
	if err := db.WithContext(ctx).Create(&agent).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Create(&secret).Error; err != nil {
		t.Fatal(err)
	}
	cachedAgent, err := sonic.MarshalString(model.AgentDetail{Agent: agent})
	if err != nil {
		t.Fatal(err)
	}
	cachedSecret, err := sonic.MarshalString(secret)
	if err != nil {
		t.Fatal(err)
	}
	cached := &verifierPoisonedCache{values: map[string]string{
		consts.AgentDetailKey + agent.AgentID: cachedAgent,
		consts.SettingKeyPrefix + secret.Name: cachedSecret,
		"agent:agent_secret_key":              string(secret.Value),
	}}
	ar := repo.NewAgentRepo(verifierTestDB{db}, cached)
	ss := service.NewSettingService(repo.NewSettingRepo(verifierTestDB{db}, cached))
	as := service.NewAgentService(ar, nil, ss, nil)
	return db, as, NewAgentTokenVerifier(as, ar, ss, cached), cached
}

func TestVerifierRejectsRevokedIdentityAndRotatedSecretImmediately(t *testing.T) {
	ctx := context.Background()
	for _, format := range []string{"current", "legacy-hint", "legacy-discovery"} {
		for _, change := range []string{"disabled", "deleted", "rotated", "missing-secret", "invalid-secret", "database-unavailable"} {
			t.Run(format+"/"+change, func(t *testing.T) {
				db, as, verifier, cached := newLiveVerifier(t)
				token, err := as.GenerateAgentToken(ctx, "Agent-A")
				if err != nil {
					t.Fatal(err)
				}
				hint := "Agent-A"
				if format != "current" {
					token = strings.SplitN(token, ":", 2)[1]
				}
				if format == "legacy-discovery" {
					hint = ""
				}
				if got, err := verifier.VerifyAgentToken(ctx, token, hint); err != nil || got != "Agent-A" {
					t.Fatalf("enabled agent denied: %q %v", got, err)
				}
				switch change {
				case "disabled":
					err = db.WithContext(ctx).Model(&model.Agent{}).Where("agent_id = ?", "Agent-A").Update("is_enabled", 0).Error
				case "deleted":
					err = db.WithContext(ctx).Where("agent_id = ?", "Agent-A").Delete(&model.Agent{}).Error
				case "rotated":
					err = db.WithContext(ctx).Model(&model.Setting{}).Where("name = ?", consts.SettingNameAgentSecretKey).Update("value", []byte(`{"salt":"synthetic-new-salt","secret_key":"synthetic-new-key"}`)).Error
				case "missing-secret":
					err = db.WithContext(ctx).Where("name = ?", consts.SettingNameAgentSecretKey).Delete(&model.Setting{}).Error
				case "invalid-secret":
					err = db.WithContext(ctx).Model(&model.Setting{}).Where("name = ?", consts.SettingNameAgentSecretKey).Update("value", []byte(`{"salt":"","secret_key":"synthetic-new-key"}`)).Error
				case "database-unavailable":
					sqlDB, _ := db.DB()
					err = sqlDB.Close()
				}
				if err != nil {
					t.Fatal(err)
				}
				if got, err := verifier.VerifyAgentToken(ctx, token, hint); err == nil || got != "" {
					t.Fatalf("revoked authorization accepted: %q %v", got, err)
				}
				if cached.reads != 0 || cached.writes != 0 {
					t.Fatalf("authentication used poisoned cache: reads=%d writes=%d", cached.reads, cached.writes)
				}
				if change == "rotated" {
					newToken, err := as.GenerateAgentToken(ctx, "Agent-A")
					if err != nil {
						t.Fatal(err)
					}
					if got, err := verifier.VerifyAgentToken(ctx, newToken, "Agent-A"); err != nil || got != "Agent-A" {
						t.Fatalf("current credential denied after rotation: %q %v", got, err)
					}
				}
			})
		}
	}
}

func TestVerifierBindsExactIdentityAtAuthenticatedRPC(t *testing.T) {
	ctx := context.Background()
	db, as, verifier, _ := newLiveVerifier(t)
	previous := tokenVerifier
	SetTokenVerifier(verifier)
	t.Cleanup(func() { SetTokenVerifier(previous) })
	for _, tc := range []struct {
		tokenID, requestID string
		allowed            bool
	}{{"Agent-A", "Agent-A", true}, {"Agent-A", "agent-a", false}, {"agent-a", "agent-a", false}, {"Agent-A", " Agent-A", false}} {
		token, err := as.GenerateAgentToken(ctx, tc.tokenID)
		if err != nil {
			t.Fatal(err)
		}
		called := false
		authCtx := metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer "+token))
		_, err = AuthUnaryInterceptor()(authCtx, &agentv1.HeartbeatRequest{AgentId: tc.requestID}, &grpc.UnaryServerInfo{FullMethod: agentv1.AgentService_Heartbeat_FullMethodName}, func(ctx context.Context, _ any) (any, error) {
			called = true
			identity, ok := GetTokenInfofromContext(ctx)
			if !ok || identity.ID != "Agent-A" {
				t.Fatal("authenticated identity changed")
			}
			return nil, nil
		})
		if called != tc.allowed || (err == nil) != tc.allowed {
			t.Fatalf("identity %+v: handler=%v error=%v", tc, called, err)
		}
	}
	if err := db.WithContext(ctx).Model(&model.Agent{}).Where("agent_id = ?", "Agent-A").Update("is_enabled", 0).Error; err != nil {
		t.Fatal(err)
	}
	token, err := as.GenerateAgentToken(ctx, "Agent-A")
	if err != nil {
		t.Fatal(err)
	}
	authCtx := metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer "+token))
	_, err = AuthUnaryInterceptor()(authCtx, &agentv1.HeartbeatRequest{AgentId: "Agent-A"}, &grpc.UnaryServerInfo{FullMethod: agentv1.AgentService_Heartbeat_FullMethodName}, func(context.Context, any) (any, error) { t.Fatal("disabled RPC reached handler"); return nil, nil })
	if err == nil {
		t.Fatal("disabled RPC authenticated")
	}
}

func TestVerifierFailuresNeverExposeSecretMaterial(t *testing.T) {
	ctx := context.Background()
	db, as, verifier, _ := newLiveVerifier(t)
	const canary = "synthetic-secret-parse-canary"
	dir := t.TempDir()
	conf := log.SetDefaults()
	conf.Output, conf.Path, conf.Filename = "file", dir, "verifier.log"
	if err := log.Init(conf); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Init(log.SetDefaults()) })
	previous := tokenVerifier
	SetTokenVerifier(verifier)
	t.Cleanup(func() { SetTokenVerifier(previous) })
	for _, fault := range []string{"malformed-json", "database-error"} {
		if fault == "malformed-json" {
			if err := db.WithContext(ctx).Model(&model.Setting{}).Where("name = ?", consts.SettingNameAgentSecretKey).Update("value", []byte(`{"secret_key":"`+canary+`","salt":`)).Error; err != nil {
				t.Fatal(err)
			}
		} else {
			if err := db.Callback().Query().Before("gorm:query").Register("synthetic-secret-error", func(tx *gorm.DB) { tx.AddError(errors.New(canary)) }); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < 2; i++ {
			if _, err := as.GenerateAgentToken(ctx, "Agent-A"); err == nil || strings.Contains(err.Error(), canary) {
				t.Fatalf("credential generation exposed secret: %v", err)
			}
			for _, token := range []string{canary + ":invalid-signature", canary} {
				if _, err := parseToken(ctx, token, &agentv1.HeartbeatRequest{AgentId: "Agent-A"}); err == nil || strings.Contains(err.Error(), canary) {
					t.Fatalf("authentication failure exposed secret: %v", err)
				}
			}
		}
	}
	_ = log.Sync()
	data, err := os.ReadFile(filepath.Join(dir, "verifier.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), canary) {
		t.Fatal("secret appeared in authentication logs")
	}
}

type verifierStaleListRepo struct {
	repo.IAgentRepository
	afterList func(context.Context)
}

func (r verifierStaleListRepo) List(ctx context.Context, page, size int) ([]model.Agent, int64, error) {
	agents, count, err := r.IAgentRepository.List(ctx, page, size)
	if err == nil {
		r.afterList(ctx)
	}
	return agents, count, err
}

func TestLegacyVerifierRechecksCandidateAfterDiscovery(t *testing.T) {
	ctx := context.Background()
	db, as, verifier, _ := newLiveVerifier(t)
	token, err := as.GenerateAgentToken(ctx, "Agent-A")
	if err != nil {
		t.Fatal(err)
	}
	v := verifier.(*agentTokenVerifier)
	v.agentRepo = verifierStaleListRepo{IAgentRepository: v.agentRepo, afterList: func(ctx context.Context) {
		if err := db.WithContext(ctx).Model(&model.Agent{}).Where("agent_id = ?", "Agent-A").Update("is_enabled", 0).Error; err != nil {
			t.Fatal(err)
		}
	}}
	if got, err := v.VerifyAgentToken(ctx, strings.SplitN(token, ":", 2)[1], ""); err == nil || got != "" {
		t.Fatalf("stale legacy candidate authenticated: %q %v", got, err)
	}
}
