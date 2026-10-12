package repo

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/bytedance/sonic"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/plugin/dbresolver"
)

type liveAuthCache struct {
	cache.ICache
	values        map[string]string
	reads, writes int
}

func (c *liveAuthCache) Get(_ context.Context, key string) *redis.StringCmd {
	c.reads++
	value, ok := c.values[key]
	if !ok {
		return redis.NewStringResult("", redis.Nil)
	}
	return redis.NewStringResult(value, nil)
}
func (c *liveAuthCache) Set(_ context.Context, _ string, _ any, _ time.Duration) *redis.StatusCmd {
	c.writes++
	return redis.NewStatusResult("OK", nil)
}
func (c *liveAuthCache) Del(_ context.Context, _ ...string) *redis.IntCmd {
	return redis.NewIntResult(0, nil) // Simulate a stale cache that cannot be invalidated.
}

func liveAuthJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := sonic.MarshalString(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

type caseInsensitiveAuthAgent struct {
	model.Agent
	AgentID string `gorm:"column:agent_id;type:text COLLATE NOCASE"`
}

func (caseInsensitiveAuthAgent) TableName() string { return "agent" }

func liveAuthDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.WithContext(context.Background()).AutoMigrate(&caseInsensitiveAuthAgent{}, &model.Setting{}, &model.RegistrationToken{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestAgentGetUsesLiveExactIdentityWhileDetailsRemainCached(t *testing.T) {
	ctx := context.Background()
	db := liveAuthDB(t, filepath.Join(t.TempDir(), "agents.db"))
	original := model.Agent{AgentID: "Agent-A", IsEnabled: 1}
	if err := db.WithContext(ctx).Create(&original).Error; err != nil {
		t.Fatal(err)
	}
	cached := &liveAuthCache{values: map[string]string{
		consts.AgentDetailKey + original.AgentID: liveAuthJSON(t, model.AgentDetail{Agent: original}),
	}}
	r := NewAgentRepo(statusTestDB{db}, cached)
	if err := db.WithContext(ctx).Model(&model.Agent{}).Where("agent_id = ?", original.AgentID).Update("is_enabled", 0).Error; err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ctx, original.AgentID)
	if err != nil || got.IsEnabled != 0 {
		t.Fatalf("stale authorization: %+v, %v", got, err)
	}
	if cached.reads != 0 || cached.writes != 0 {
		t.Fatal("authentication accessed cache")
	}
	detail, err := r.GetDetail(ctx, original.AgentID)
	if err != nil || detail.IsEnabled != 1 || cached.reads != 1 {
		t.Fatalf("UI cache changed: %+v, %v", detail, err)
	}
	var loose model.Agent
	if err := db.WithContext(ctx).Select("agent_id").Where("agent_id = ?", "agent-a").First(&loose).Error; err != nil {
		t.Fatal("fixture must use a case-insensitive identity column", err)
	}
	if _, err := r.Get(ctx, "agent-a"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("case-insensitive identity accepted: %v", err)
	}
	if err := db.WithContext(ctx).Where("agent_id = ?", original.AgentID).Delete(&model.Agent{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, original.AgentID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("deleted identity accepted: %v", err)
	}
	sqlDB, _ := db.DB()
	_ = sqlDB.Close()
	if got, err := r.Get(ctx, original.AgentID); err == nil || got != nil {
		t.Fatal("database failure fell back to cached identity")
	}
}

func TestAgentSecretSettingBypassesPoisonedCache(t *testing.T) {
	ctx := context.Background()
	db := liveAuthDB(t, filepath.Join(t.TempDir(), "settings.db"))
	setting := model.Setting{Name: consts.SettingNameAgentSecretKey, Value: []byte(`{"salt":"synthetic-salt","secret_key":"synthetic-old-key"}`)}
	if err := db.WithContext(ctx).Create(&setting).Error; err != nil {
		t.Fatal(err)
	}
	cached := &liveAuthCache{values: map[string]string{
		consts.SettingKeyPrefix + setting.Name:              liveAuthJSON(t, setting),
		consts.SettingKeyPrefix + consts.SettingNameGeneral: liveAuthJSON(t, model.Setting{Name: consts.SettingNameGeneral, Value: []byte(`{"cached":true}`)}),
	}}
	r := NewSettingRepo(statusTestDB{db}, cached)
	rotated := []byte(`{"salt":"synthetic-new-salt","secret_key":"synthetic-new-key"}`)
	if err := db.WithContext(ctx).Model(&model.Setting{}).Where("name = ?", setting.Name).Update("value", rotated).Error; err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ctx, setting.Name)
	if err != nil || string(got.Value) != string(rotated) {
		t.Fatalf("stale secret: %+v %v", got, err)
	}
	if cached.reads != 0 || cached.writes != 0 {
		t.Fatal("secret lookup accessed cache")
	}
	if _, err := r.Get(ctx, consts.SettingNameGeneral); err != nil || cached.reads != 1 {
		t.Fatal("ordinary setting no longer uses cache", err)
	}
	if err := db.WithContext(ctx).Where("name = ?", setting.Name).Delete(&model.Setting{}).Error; err != nil {
		t.Fatal(err)
	}
	if got, err := r.Get(ctx, setting.Name); err == nil || got != nil {
		t.Fatal("deleted secret fell back to cache")
	}
	sqlDB, _ := db.DB()
	_ = sqlDB.Close()
	if got, err := r.Get(ctx, setting.Name); err == nil || got != nil {
		t.Fatal("unavailable secret fell back to cache")
	}
}

func TestAuthenticationReadsPrimaryInsteadOfStaleReplica(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	primary := liveAuthDB(t, filepath.Join(dir, "primary.db"))
	replicaPath := filepath.Join(dir, "replica.db")
	replica := liveAuthDB(t, replicaPath)
	for _, source := range []struct {
		db      *gorm.DB
		enabled int
		key     string
	}{{primary, 0, "synthetic-current"}, {replica, 1, "synthetic-stale"}} {
		if err := source.db.WithContext(ctx).Create(&model.Agent{AgentID: "agent", IsEnabled: source.enabled}).Error; err != nil {
			t.Fatal(err)
		}
		if err := source.db.WithContext(ctx).Create(&model.Setting{Name: consts.SettingNameAgentSecretKey, Value: []byte(liveAuthJSON(t, map[string]string{"salt": "synthetic-salt", "secret_key": source.key}))}).Error; err != nil {
			t.Fatal(err)
		}
		if err := source.db.WithContext(ctx).Create(&model.Setting{Name: consts.SettingNameAgentAutoApprove, Value: []byte(liveAuthJSON(t, map[string]bool{"auto_approve": source.enabled == 1}))}).Error; err != nil {
			t.Fatal(err)
		}
		if err := source.db.WithContext(ctx).Create(&model.RegistrationToken{TokenHash: "synthetic-hash", IsActive: source.enabled}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := primary.Use(dbresolver.Register(dbresolver.Config{Replicas: []gorm.Dialector{sqlite.Open(replicaPath)}})); err != nil {
		t.Fatal(err)
	}
	var stale model.Agent
	if err := primary.WithContext(ctx).Clauses(database.Read()).Select("agent_id", "is_enabled").First(&stale).Error; err != nil || stale.IsEnabled != 1 {
		t.Fatal("replica fixture was not stale", err)
	}
	agent, err := NewAgentRepo(statusTestDB{primary}, nil).Get(ctx, "agent")
	if err != nil || agent.IsEnabled != 0 {
		t.Fatalf("agent authorization used replica: %+v %v", agent, err)
	}
	settingRepo := NewSettingRepo(statusTestDB{primary}, nil)
	secret, err := settingRepo.Get(ctx, consts.SettingNameAgentSecretKey)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]string
	if secret == nil || sonic.Unmarshal(secret.Value, &fields) != nil || fields["secret_key"] != "synthetic-current" {
		t.Fatalf("secret read used replica: %+v", secret)
	}
	policy, err := settingRepo.Get(ctx, consts.SettingNameAgentAutoApprove)
	if err != nil || policy == nil || string(policy.Value) != `{"auto_approve":false}` {
		t.Fatalf("approval policy used replica: %+v %v", policy, err)
	}
	tokens, err := NewRegistrationTokenRepo(statusTestDB{primary}).GetAllActive(ctx)
	if err != nil || len(tokens) != 0 {
		t.Fatalf("revoked enrollment token read from stale replica: count=%d error=%v", len(tokens), err)
	}
}
