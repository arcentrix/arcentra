package service

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
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/bytedance/sonic"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const syntheticRegistrationToken = "art_synthetic_enrollment_fixture_not_a_credential"

type registrationTestDB struct{ db *gorm.DB }

func (d registrationTestDB) Database() *gorm.DB { return d.db }

type registrationPoisonedCache struct {
	cache.ICache
	values map[string]string
	reads  []string
}

func (c *registrationPoisonedCache) Get(_ context.Context, key string) *redis.StringCmd {
	c.reads = append(c.reads, key)
	value, ok := c.values[key]
	if !ok {
		return redis.NewStringResult("", redis.Nil)
	}
	return redis.NewStringResult(value, nil)
}
func (c *registrationPoisonedCache) Set(_ context.Context, _ string, _ any, _ time.Duration) *redis.StatusCmd {
	return redis.NewStatusResult("OK", nil)
}
func (c *registrationPoisonedCache) Del(_ context.Context, _ ...string) *redis.IntCmd {
	return redis.NewIntResult(0, nil)
}

type registrationStorage struct {
	repo.IStorageRepository
	calls      int
	beforeRead func(context.Context)
}

func (s *registrationStorage) GetDefault(ctx context.Context) (*model.StorageConfig, error) {
	s.calls++
	if s.beforeRead != nil {
		s.beforeRead(ctx)
	}
	return s.IStorageRepository.GetDefault(ctx)
}

type registrationCaseAgent struct {
	model.Agent
	AgentID string `gorm:"column:agent_id;type:text COLLATE NOCASE"`
}

func (registrationCaseAgent) TableName() string { return "agent" }

func registrationJSON(t *testing.T, v any) string {
	t.Helper()
	value, err := sonic.MarshalString(v)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func newRegistrationFixture(t *testing.T, autoApprove bool) (*gorm.DB, *AgentServiceImpl, *registrationStorage, *registrationPoisonedCache) {
	t.Helper()
	ctx := context.Background()
	db := terminalTestDB(t, filepath.Join(t.TempDir(), "registration.db"), false)
	if err := db.WithContext(ctx).AutoMigrate(&registrationCaseAgent{}, &model.Setting{}, &model.StorageConfig{}, &model.RegistrationToken{}); err != nil {
		t.Fatal(err)
	}
	secret := model.Setting{Name: consts.SettingNameAgentSecretKey, Value: []byte(`{"salt":"synthetic-salt","secret_key":"synthetic-agent-secret"}`)}
	if err := db.WithContext(ctx).Create(&secret).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Create(&model.Setting{Name: consts.SettingNameAgentAutoApprove, Value: []byte(registrationJSON(t, map[string]bool{"auto_approve": autoApprove}))}).Error; err != nil {
		t.Fatal(err)
	}
	agent := model.Agent{AgentID: "Agent-A", IsEnabled: 1, Labels: []byte(`{"origin":"database"}`)}
	if err := db.WithContext(ctx).Create(&agent).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Create(&model.StorageConfig{StorageID: "synthetic-store", StorageType: "s3", IsDefault: 1, IsEnabled: 1, Config: []byte(`{"endpoint":"https://storage.example.invalid","bucket":"fixture","accessKey":"synthetic-access","secretKey":"synthetic-storage-secret"}`)}).Error; err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(syntheticRegistrationToken), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Create(&model.RegistrationToken{TokenHash: string(hash), IsActive: 1}).Error; err != nil {
		t.Fatal(err)
	}
	cached := &registrationPoisonedCache{values: map[string]string{
		consts.AgentDetailKey + agent.AgentID: registrationJSON(t, model.AgentDetail{Agent: agent}),
		consts.SettingKeyPrefix + secret.Name: registrationJSON(t, secret),
		"agent:agent_secret_key":              string(secret.Value),
	}}
	store := registrationTestDB{db}
	as := NewAgentService(repo.NewAgentRepo(store, cached), nil, NewSettingService(repo.NewSettingRepo(store, cached)), nil)
	as.SetRegistrationTokenService(NewRegistrationTokenService(repo.NewRegistrationTokenRepo(store)))
	storage := &registrationStorage{IStorageRepository: repo.NewStorageRepo(store, nil)}
	return db, NewAgentServiceImpl(as, storage), storage, cached
}

func TestLegacyRegistrationUsesLiveAuthorizationAndSecret(t *testing.T) {
	ctx := context.Background()
	for _, change := range []string{"enabled", "pending-or-disabled", "invalid-enable-state", "deleted", "rotated", "missing-secret", "database-unavailable", "case-alias", "request-mismatch"} {
		t.Run(change, func(t *testing.T) {
			db, server, storage, cached := newRegistrationFixture(t, true)
			tokenID, requestID := "Agent-A", "Agent-A"
			if change == "case-alias" {
				tokenID, requestID = "agent-a", "agent-a"
			}
			if change == "request-mismatch" {
				requestID = "agent-a"
			}
			token, err := server.agentService.GenerateAgentToken(ctx, tokenID)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "pending-or-disabled":
				err = db.WithContext(ctx).Model(&model.Agent{}).Where("agent_id = ?", "Agent-A").Update("is_enabled", 0).Error
			case "invalid-enable-state":
				err = db.WithContext(ctx).Model(&model.Agent{}).Where("agent_id = ?", "Agent-A").Update("is_enabled", 2).Error
			case "deleted":
				err = db.WithContext(ctx).Where("agent_id = ?", "Agent-A").Delete(&model.Agent{}).Error
			case "rotated":
				err = db.WithContext(ctx).Model(&model.Setting{}).Where("name = ?", consts.SettingNameAgentSecretKey).Update("value", []byte(`{"salt":"synthetic-new-salt","secret_key":"synthetic-new-key"}`)).Error
			case "missing-secret":
				err = db.WithContext(ctx).Where("name = ?", consts.SettingNameAgentSecretKey).Delete(&model.Setting{}).Error
			case "database-unavailable":
				sqlDB, _ := db.DB()
				err = sqlDB.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			response, err := server.Register(ctx, &agentv1.RegisterRequest{AgentId: requestID, Token: token, Labels: map[string]string{"origin": "current-request"}})
			if change == "enabled" {
				if err != nil || response == nil || !response.Success || response.PendingApproval || response.AgentId != "Agent-A" || response.Storage == nil || response.Storage.SecretKey != "synthetic-storage-secret" || response.Labels["origin"] != "current-request" {
					t.Fatalf("enabled registration: %+v %v", response, err)
				}
			} else if err == nil || response != nil || storage.calls != 0 {
				t.Fatalf("revoked registration returned credentials/storage: response=%+v error=%v storageCalls=%d", response, err, storage.calls)
			}
			if len(cached.reads) != 0 {
				t.Fatalf("legacy authorization read stale cache: %v", cached.reads)
			}
		})
	}
}

func TestDynamicRegistrationPendingGetsIdentityWithoutStorage(t *testing.T) {
	ctx := context.Background()
	for _, enabled := range []bool{false, true} {
		t.Run(registrationJSON(t, enabled), func(t *testing.T) {
			db, server, storage, _ := newRegistrationFixture(t, enabled)
			response, err := server.Register(ctx, &agentv1.RegisterRequest{RegistrationToken: syntheticRegistrationToken, AgentName: "synthetic-agent", Labels: map[string]string{"pool": "fixture"}})
			if err != nil || response == nil || !response.Success || response.AgentId == "" || response.PendingApproval == enabled || response.AuthToken == "" {
				t.Fatalf("dynamic enrollment: %+v %v", response, err)
			}
			expected, err := server.agentService.GenerateAgentToken(ctx, response.AgentId)
			if err != nil || response.AuthToken != expected {
				t.Fatal("per-agent token does not bind issued identity", err)
			}
			if !enabled && (response.Storage != nil || storage.calls != 0) {
				t.Fatal("pending enrollment accessed or returned storage")
			}
			if enabled && (response.Storage == nil || storage.calls != 1) {
				t.Fatal("approved enrollment missing storage")
			}
			var persisted model.Agent
			if err := db.WithContext(ctx).Select("agent_id", "is_enabled", "registered_by").Where("agent_id = ?", response.AgentId).First(&persisted).Error; err != nil {
				t.Fatal(err)
			}
			if (persisted.IsEnabled == 1) != enabled || persisted.RegisteredBy != "dynamic" {
				t.Fatalf("wrong persisted approval: %+v", persisted)
			}
			if !enabled {
				if retry, err := server.Register(ctx, &agentv1.RegisterRequest{AgentId: response.AgentId, Token: response.AuthToken}); err == nil || retry != nil {
					t.Fatal("pending identity authenticated before approval")
				}
				if err := server.agentService.agentRepo.Approve(ctx, response.AgentId); err != nil {
					t.Fatal(err)
				}
				if retry, err := server.Register(ctx, &agentv1.RegisterRequest{AgentId: response.AgentId, Token: response.AuthToken}); err != nil || retry.Storage == nil {
					t.Fatalf("approved persisted identity could not resume: %+v %v", retry, err)
				}
			}
		})
	}
}

func TestRegistrationRechecksAuthorizationBeforeCredentialResponse(t *testing.T) {
	ctx := context.Background()
	for _, flow := range []string{"legacy", "dynamic"} {
		for _, revoke := range []string{"disabled", "deleted", "rotated", "read-failure"} {
			t.Run(flow+"/"+revoke, func(t *testing.T) {
				db, server, storage, _ := newRegistrationFixture(t, true)
				token, err := server.agentService.GenerateAgentToken(ctx, "Agent-A")
				if err != nil {
					t.Fatal(err)
				}
				storage.beforeRead = func(ctx context.Context) {
					var err error
					switch revoke {
					case "disabled":
						err = db.WithContext(ctx).Model(&model.Agent{}).Where("is_enabled = ?", 1).Update("is_enabled", 0).Error
					case "deleted":
						err = db.WithContext(ctx).Where("is_enabled = ?", 1).Delete(&model.Agent{}).Error
					case "rotated":
						err = db.WithContext(ctx).Model(&model.Setting{}).Where("name = ?", consts.SettingNameAgentSecretKey).Update("value", []byte(`{"salt":"synthetic-new-salt","secret_key":"synthetic-new-key"}`)).Error
					case "read-failure":
						sqlDB, _ := db.DB()
						err = sqlDB.Close()
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				request := &agentv1.RegisterRequest{AgentId: "Agent-A", Token: token}
				if flow == "dynamic" {
					request = &agentv1.RegisterRequest{RegistrationToken: syntheticRegistrationToken}
				}
				response, err := server.Register(ctx, request)
				if err == nil || response != nil {
					t.Fatalf("authorization changed before reply but credentials escaped: %+v %v", response, err)
				}
			})
		}
	}
}

func TestRegistrationSecretParsingErrorsAreRedacted(t *testing.T) {
	ctx := context.Background()
	db, server, _, _ := newRegistrationFixture(t, true)
	const canary = "synthetic-registration-secret-parse-canary"
	if err := db.WithContext(ctx).Model(&model.Setting{}).Where("name = ?", consts.SettingNameAgentSecretKey).Update("value", []byte(`{"secret_key":"`+canary+`","salt":`)).Error; err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	conf := log.SetDefaults()
	conf.Output, conf.Path, conf.Filename = "file", dir, "registration.log"
	if err := log.Init(conf); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Init(log.SetDefaults()) })
	for i := 0; i < 2; i++ {
		for _, request := range []*agentv1.RegisterRequest{{AgentId: "Agent-A", Token: "Agent-A:" + canary}, {RegistrationToken: syntheticRegistrationToken}} {
			response, err := server.Register(ctx, request)
			if err == nil || response != nil || strings.Contains(err.Error(), canary) {
				t.Fatalf("registration error leaked secret: %+v %v", response, err)
			}
		}
	}
	_ = log.Sync()
	data, err := os.ReadFile(filepath.Join(dir, "registration.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), canary) {
		t.Fatal("registration logs leaked secret JSON")
	}
}

func TestDynamicRegistrationPolicyFailureRequiresApproval(t *testing.T) {
	ctx := context.Background()
	for _, fault := range []string{"missing", "malformed", "read-failure", "stale-cache"} {
		t.Run(fault, func(t *testing.T) {
			db, server, storage, cached := newRegistrationFixture(t, true)
			cached.values[consts.SettingKeyPrefix+consts.SettingNameAgentAutoApprove] = registrationJSON(t, model.Setting{Name: consts.SettingNameAgentAutoApprove, Value: []byte(`{"auto_approve":true}`)})
			switch fault {
			case "missing":
				if err := db.WithContext(ctx).Where("name = ?", consts.SettingNameAgentAutoApprove).Delete(&model.Setting{}).Error; err != nil {
					t.Fatal(err)
				}
			case "malformed":
				if err := db.WithContext(ctx).Model(&model.Setting{}).Where("name = ?", consts.SettingNameAgentAutoApprove).Update("value", []byte(`{"auto_approve":`)).Error; err != nil {
					t.Fatal(err)
				}
			case "stale-cache":
				if err := db.WithContext(ctx).Model(&model.Setting{}).Where("name = ?", consts.SettingNameAgentAutoApprove).Update("value", []byte(`{"auto_approve":false}`)).Error; err != nil {
					t.Fatal(err)
				}
			case "read-failure":
				if err := db.Callback().Query().Before("gorm:query").Register("unavailable-approval-policy", func(tx *gorm.DB) {
					where, ok := tx.Statement.Clauses["WHERE"].Expression.(clause.Where)
					if !ok {
						return
					}
					for _, condition := range where.Exprs {
						if expr, ok := condition.(clause.Expr); ok {
							for _, value := range expr.Vars {
								if name, ok := value.(string); ok && name == consts.SettingNameAgentAutoApprove {
									tx.AddError(errors.New("synthetic policy read failure"))
								}
							}
						}
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			response, err := server.Register(ctx, &agentv1.RegisterRequest{RegistrationToken: syntheticRegistrationToken})
			if err != nil || response == nil || !response.Success || !response.PendingApproval || response.AuthToken == "" || response.Storage != nil || storage.calls != 0 {
				t.Fatalf("policy failure enabled enrollment: %+v %v", response, err)
			}
			current, err := server.agentService.agentRepo.Get(ctx, response.AgentId)
			if err != nil || current.IsEnabled != 0 {
				t.Fatalf("policy failure created enabled identity: %+v %v", current, err)
			}
			if len(cached.reads) != 0 {
				t.Fatalf("authorization policy used stale cache: %v", cached.reads)
			}
		})
	}
}

func TestDynamicRegistrationRechecksSharedTokenRevocation(t *testing.T) {
	ctx := context.Background()
	for _, timing := range []string{"before-registration", "before-response"} {
		t.Run(timing, func(t *testing.T) {
			db, server, storage, _ := newRegistrationFixture(t, true)
			revoke := func(ctx context.Context) {
				if err := db.WithContext(ctx).Model(&model.RegistrationToken{}).Where("is_active = ?", 1).Update("is_active", 0).Error; err != nil {
					t.Fatal(err)
				}
			}
			if timing == "before-registration" {
				revoke(ctx)
			} else {
				storage.beforeRead = revoke
			}
			response, err := server.Register(ctx, &agentv1.RegisterRequest{RegistrationToken: syntheticRegistrationToken})
			if err == nil || response != nil {
				t.Fatalf("revoked enrollment token issued credentials: %+v %v", response, err)
			}
			var records []model.Agent
			if err := db.WithContext(ctx).Select("agent_id", "is_enabled").Where("registered_by = ?", "dynamic").Find(&records).Error; err != nil {
				t.Fatal(err)
			}
			if timing == "before-registration" {
				if len(records) != 0 || storage.calls != 0 {
					t.Fatal("initially revoked token created identity or read storage")
				}
			} else if len(records) != 1 || records[0].IsEnabled != 0 {
				t.Fatalf("interrupted enrollment must preserve a pending operator-reconciliation record: %+v", records)
			}
		})
	}
}
