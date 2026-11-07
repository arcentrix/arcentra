// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/polling"
	"github.com/arcentrix/arcentra/pkg/safe"
	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// Only an explicitly supplied local, empty disposable schema may be used. This
// suite never creates a database, discovers credentials, or starts a server.
// Required-mode CI must set ARCENTRA_REQUIRE_POLLING_MYSQL=1; a missing or invalid
// DSN then fails rather than silently producing a skipped acceptance result.
func pollingMySQLConfig(dsn string) (*drivermysql.Config, error) {
	cfg, err := drivermysql.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("invalid polling MySQL acceptance DSN")
	}
	const prefix = "arcentra_polling_test"
	if cfg.DBName != prefix && !strings.HasPrefix(cfg.DBName, prefix+"_") {
		return nil, errors.New("polling MySQL acceptance requires an arcentra_polling_test disposable schema")
	}
	if len(cfg.DBName) > 64 || strings.Trim(cfg.DBName, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
		return nil, errors.New("invalid disposable polling schema name")
	}
	switch cfg.Net {
	case "unix":
		if !filepath.IsAbs(cfg.Addr) {
			return nil, errors.New("polling MySQL acceptance requires an absolute local socket path")
		}
	case "tcp":
		host, _, err := net.SplitHostPort(cfg.Addr)
		if err != nil || (host != "127.0.0.1" && host != "::1" && host != "localhost") {
			return nil, errors.New("polling MySQL acceptance only permits explicit loopback connections")
		}
	default:
		return nil, errors.New("polling MySQL acceptance only permits TCP loopback or unix sockets")
	}
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 30*time.Second, 30*time.Second
	cfg.ParseTime, cfg.Loc, cfg.MultiStatements = true, time.UTC, false
	// Exercise unchanged-row semantics even if the supplied DSN requested matched rows.
	cfg.ClientFoundRows = false
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["transaction_isolation"] = "'REPEATABLE-READ'"
	return cfg, nil
}

func openPollingMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	required := os.Getenv("ARCENTRA_REQUIRE_POLLING_MYSQL")
	if required != "" && required != "1" {
		t.Fatal("ARCENTRA_REQUIRE_POLLING_MYSQL must be 1 or unset")
	}
	dsn := os.Getenv("ARCENTRA_POLLING_MYSQL_DSN")
	if dsn == "" {
		if required == "1" {
			t.Fatal("required polling MySQL acceptance DSN is missing")
		}
		t.Skip("set an explicitly supplied local disposable ARCENTRA_POLLING_MYSQL_DSN to run MySQL acceptance")
	}
	cfg, err := pollingMySQLConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		// Do not print a driver error that might include connection information.
		t.Fatal("cannot connect to the explicitly supplied local polling MySQL test schema")
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = pool.Close() })
	return db
}

func TestPollingMySQLDSNGuards(t *testing.T) {
	for _, dsn := range []string{
		"test@tcp(127.0.0.1:3306)/production",
		"test@tcp(127.0.0.1:3306)/arcentra_polling_testproduction",
		"test@tcp(database.example:3306)/arcentra_polling_test_safe",
		"test@unix(relative.sock)/arcentra_polling_test_safe",
		"test@tcp(127.0.0.1:3306)/arcentra_polling_test_unsafe-name",
	} {
		if _, err := pollingMySQLConfig(dsn); err == nil {
			t.Fatal("unsafe test connection was accepted")
		}
	}
	for _, dsn := range []string{
		"test@tcp(127.0.0.1:3306)/arcentra_polling_test_safe",
		"test@tcp([::1]:3306)/arcentra_polling_test_safe",
		"test@unix(/tmp/arcentra-polling-test.sock)/arcentra_polling_test",
	} {
		cfg, err := pollingMySQLConfig(dsn)
		if err != nil || cfg.Params["transaction_isolation"] != "'REPEATABLE-READ'" {
			t.Fatalf("safe repeatable-read test configuration rejected: %v", err)
		}
	}
}

func TestPollingMySQLAcceptance(t *testing.T) {
	db, other := openPollingMySQL(t), openPollingMySQL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	tables, err := db.WithContext(ctx).Migrator().GetTables()
	if err != nil {
		t.Fatal("cannot verify the disposable schema is fresh:", err)
	}
	if len(tables) != 0 {
		t.Fatal("polling MySQL acceptance refuses a schema containing existing tables; supply a fresh disposable schema")
	}
	// The schema was empty. Cleanup removes only the application tables this
	// suite creates, never the schema itself or any pre-existing table/data.
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		models := schemaModels()
		for i := len(models) - 1; i >= 0; i-- {
			if err := db.WithContext(cleanup).Migrator().DropTable(models[i]); err != nil {
				t.Error("polling disposable table cleanup:", err)
			}
		}
	})
	if !t.Run("explicit_migration_is_inactive_and_preserves_legacy_data", func(t *testing.T) {
		pollingMySQLMigration(t, db)
	}) {
		return
	}
	for _, provider := range []string{"gitlab", "github", "gitea", "gitee", "bitbucket"} {
		run := func(t *testing.T) {
			if provider == "gitee" || provider == "bitbucket" {
				t.Run("v3_merged_evidence_survives_replay_admission", func(t *testing.T) {
					pollingMySQLV3MergeEvidence(t, newPollingMySQLFixture(t, db, other, provider))
				})
			}
			// No subtest is parallel: catalog current reads deliberately lock the same
			// permission rows. Each fixture still uses unique, clearly prefixed IDs.
			t.Run("empty_eligible_targets_completes_without_build", func(t *testing.T) {
				f := newPollingMySQLFixture(t, db, other, provider)
				s, _ := f.prepare(t, true)
				completed, err := f.streams.AcknowledgeBatch(f.ctx, AcknowledgeBatchInput{ProjectID: f.projectID, ExpectedRevision: s.Revision, ActivationGeneration: s.ActivationGeneration, BatchID: f.batchID})
				if err != nil || completed.PendingBatchJSON != "" || completed.LastTargetCount != 0 || completed.LastObservationCount != 1 || completed.CursorJSON == s.CursorJSON {
					t.Fatalf("empty observation did not complete: %v", err)
				}
				var count int64
				if err := db.Model(&model.PipelineRun{}).Where("pipeline_id = ?", f.pipelineID).Count(&count).Error; err != nil || count != 0 {
					t.Fatal("empty eligible selection created a run")
				}
			})
			t.Run("committed_scope_revocation_defeats_repeatable_read_snapshot", func(t *testing.T) {
				pollingMySQLScopeRevocation(t, newPollingMySQLFixture(t, db, other, provider))
			})
			for _, kind := range []string{"disable", "project_configuration", "pipeline_configuration", "provider_configuration", "archive"} {
				t.Run(kind+"_wins_before_admission", func(t *testing.T) {
					pollingMySQLMutationWins(t, newPollingMySQLFixture(t, db, other, provider), kind)
				})
			}
			t.Run("admission_wins_and_later_disable_preserves_receipt", func(t *testing.T) {
				pollingMySQLAdmissionWins(t, newPollingMySQLFixture(t, db, other, provider))
			})
			t.Run("concurrent_duplicate_creation_has_one_created_now", func(t *testing.T) {
				pollingMySQLConcurrentCreation(t, newPollingMySQLFixture(t, db, other, provider))
			})
			t.Run("cursor_cas_failure_replays_without_reclaim", func(t *testing.T) {
				pollingMySQLCursorReplay(t, newPollingMySQLFixture(t, db, other, provider))
			})
		}
		if provider == "gitlab" {
			run(t)
		} else {
			t.Run(provider, run)
		}
	}
}

func pollingMySQLMigration(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := db.WithContext(ctx).AutoMigrate(&model.Project{}, &model.PipelineRun{}, &model.RoleGrant{}); err != nil {
		t.Fatal(err)
	}
	// This legacy schema predates the later rerun authority markers too.
	for _, column := range []string{"polling_context", "polling_context_digest", "copilot_rerun_intent_id", "copilot_rerun_gate_id"} {
		if err := db.WithContext(ctx).Migrator().DropColumn(&model.PipelineRun{}, column); err != nil {
			t.Fatal(err)
		}
	}
	id := "polling-test-legacy-" + uuid.NewString()[:12]
	project := &model.Project{ProjectID: id, Settings: datatypes.JSON(` {"legacyCursor":"2026-10-01T00:00:00.123456789Z","polling":true} `)}
	if err := db.WithContext(ctx).Create(project).Error; err != nil {
		t.Fatal(err)
	}
	var before model.Project
	if err := db.WithContext(ctx).Select("project_id", "settings").Where("project_id = ?", id).Take(&before).Error; err != nil {
		t.Fatal(err)
	}
	legacy := &model.PipelineRun{RunID: id, PipelineID: id, Status: model.PipelineStatusPending, TriggeredBy: "webhook", Env: "{}"}
	if err := db.WithContext(ctx).Omit("PollingContext", "PollingContextDigest", "CopilotRerunIntentID", "CopilotRerunGateID").Create(legacy).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := MigrateSchema(ctx, db); err != nil {
			t.Fatal(err)
		}
		var grants, streams, runs int64
		for _, check := range []struct {
			entity any
			count  *int64
		}{{&model.RoleGrant{}, &grants}, {&model.SCMPollingStream{}, &streams}, {&model.PipelineRun{}, &runs}} {
			if err := db.WithContext(ctx).Model(check.entity).Count(check.count).Error; err != nil {
				t.Fatal(err)
			}
		}
		if grants != 0 || streams != 0 || runs != 1 {
			t.Fatalf("migration created authority or work: grants=%d streams=%d runs=%d", grants, streams, runs)
		}
		var after model.Project
		if err := db.WithContext(ctx).Select("project_id", "settings").Where("project_id = ?", id).Take(&after).Error; err != nil || string(after.Settings) != string(before.Settings) {
			t.Fatalf("migration rewrote persisted legacy settings: %v", err)
		}
		var stored model.PipelineRun
		if err := db.WithContext(ctx).Select("run_id", "status", "triggered_by", "start_time", "end_time", "polling_context", "polling_context_digest").Where("run_id = ?", id).Take(&stored).Error; err != nil {
			t.Fatal(err)
		}
		if stored.Status != legacy.Status || stored.TriggeredBy != legacy.TriggeredBy || stored.StartTime != nil || stored.EndTime != nil || stored.PollingContext != "" || stored.PollingContextDigest != "" {
			t.Fatal("migration changed legacy execution state or invented polling provenance")
		}
	}
	row := &model.SCMPollingStream{StreamID: id, ProjectID: id, ScopeDigest: id}
	if err := db.WithContext(ctx).Create(row).Error; err != nil {
		t.Fatal(err)
	}
	var stored model.SCMPollingStream
	if err := db.WithContext(ctx).Select(pollingStreamColumns).Where("stream_id = ?", id).Take(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.State != polling.StateDisabled || stored.EnabledBy != "" || stored.EnabledAt != nil || stored.BaselineID != "" || stored.SelectionJSON != "" || stored.PendingBatchJSON != "" {
		t.Fatal("new stream defaults carry activation or dispatch authority")
	}
	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockPollingStream(ctx, tx, id)
		if err != nil {
			return err
		}
		if locked == nil {
			return errors.New("metadata test lost its locked stream")
		}
		// Freeze GORM's automatic timestamp at the persisted millisecond value so
		// MySQL really reports zero changed rows, as same-ms operational writes can.
		frozen := tx.Session(&gorm.Session{NowFunc: func() time.Time { return locked.UpdatedAt }})
		updates := map[string]any{"last_poll_error": locked.LastPollError}
		unchanged := frozen.WithContext(ctx).Model(&model.SCMPollingStream{}).Where("stream_id = ?", id).Updates(updates)
		if unchanged.Error != nil {
			return unchanged.Error
		}
		if unchanged.RowsAffected != 0 {
			return errors.New("same-value MySQL metadata fixture did not exercise zero changed rows")
		}
		if err := updatePollingMetadata(ctx, frozen, locked, updates); err != nil {
			return err
		}
		return refreshPollingStream(ctx, frozen, locked)
	}); err != nil {
		t.Fatal("same-value metadata write failed despite holding the exact current stream lock:", err)
	}
}

type pollingMySQLFixture struct {
	ctx                   context.Context
	db, other             *gorm.DB
	streams, otherStreams *SCMPollingRepo
	runs, otherRuns       *PipelineRepo
	projectID, pipelineID string
	actorID, roleID       string
	grantID, batchID      string
	scope                 polling.Scope
}

func newPollingMySQLFixture(t *testing.T, db, other *gorm.DB, providers ...string) *pollingMySQLFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	prefix := "polling-test-" + uuid.NewString()[:12]
	f := &pollingMySQLFixture{ctx: ctx, db: db, other: other,
		projectID: prefix + "-project", pipelineID: prefix + "-pipeline", actorID: prefix + "-actor",
		roleID: prefix + "-role", grantID: prefix + "-grant", batchID: prefix + "-batch"}
	f.streams, f.otherStreams = &SCMPollingRepo{IDatabase: statusTestDB{db}}, &SCMPollingRepo{IDatabase: statusTestDB{other}}
	f.runs, f.otherRuns = &PipelineRepo{IDatabase: statusTestDB{db}}, &PipelineRepo{IDatabase: statusTestDB{other}}
	f.scope = polling.Scope{Version: polling.Version, ProjectID: f.projectID, Provider: "gitlab", APIOrigin: "https://gitlab.example.com", Repository: "https://gitlab.example.com/group/nested/" + prefix, ProviderProjectID: "42", Stream: "merge_requests"}
	if len(providers) > 0 && providers[0] != "gitlab" {
		f.scope.Provider = providers[0]
		f.scope.Repository = "https://github.com/owner/" + prefix
		switch providers[0] {
		case "gitea":
			f.scope.Repository = "https://gitea.example.com/owner/" + prefix
		case "gitee":
			f.scope.Repository = "https://gitee.com/owner/" + prefix
		case "bitbucket":
			f.scope.Repository = "https://bitbucket.org/owner/" + prefix
			f.scope.ProviderProjectID = "12345678-1234-1234-1234-123456789abc"
		}
		var err error
		f.scope.Version, f.scope.APIOrigin, f.scope.Stream, err = polling.ProviderRepository(f.scope.Provider, f.scope.Repository)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []any{
		&model.Project{ProjectID: f.projectID, OrgID: prefix + "-org", RepoType: f.scope.Provider, RepoURL: f.scope.Repository, DefaultBranch: "main", IsEnabled: 1, Status: model.ProjectStatusActive, TriggerMode: model.TriggerModeMR, Settings: datatypes.JSON(`{"legacyCursor":"untouched"}`)},
		&model.Pipeline{PipelineID: f.pipelineID, ProjectID: f.projectID, RepoURL: f.scope.Repository, DefaultBranch: "main", PipelineFilePath: "pipeline.yaml", IsEnabled: 1, Metadata: "{}"},
		&model.User{UserID: f.actorID, IsEnabled: 1},
		&model.Role{RoleID: f.roleID, Name: f.roleID, ScopeType: "project", IsEnabled: 1},
		&model.RoleGrant{GrantID: f.grantID, SubjectType: "user", SubjectID: f.actorID, RoleID: f.roleID, ScopeType: "project", ScopeID: f.projectID, IsEnabled: 1},
	} {
		if err := db.WithContext(ctx).Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, permission := range []string{"project:read", "project:update", "pipeline:read", "pipeline:trigger"} {
		if err := db.WithContext(ctx).Create(&model.RolePermissionBinding{RoleID: f.roleID, PermissionID: permission}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, connection := range []*gorm.DB{db, other} {
		repositories := &Repositories{database: statusTestDB{connection}}
		if err := SetPollingAuthorizer(repositories, f.authorize); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = SetPollingAuthorizer(repositories, nil) })
	}
	return f
}

// Repository-package tests cannot import authz (it imports repo). This narrow
// test authorizer evaluates the fixture's actual scoped role grant through the
// transaction-bound read facades, including permission/binding/role/grant locks.
// Production authorizer policy is exercised separately in service tests.
func (f *pollingMySQLFixture) authorize(ctx context.Context, view PollingAuthorizationView, actor, project string, ids []string) error {
	if actor != f.actorID || project != f.projectID {
		return polling.ErrForbidden
	}
	r := view.LiveRepositories()
	permissions, err := r.Permission.ListPermissions(ctx)
	if err != nil {
		return err
	}
	bindings, err := r.Permission.ListAllRoleBindings(ctx)
	if err != nil {
		return err
	}
	roles, err := r.Role.BatchGet(ctx, []string{f.roleID})
	if err != nil {
		return err
	}
	grants, err := r.RoleGrant.ListAllEnabled(ctx)
	if err != nil {
		return err
	}
	if len(roles) != 1 || roles[0].RoleID != f.roleID || roles[0].IsEnabled != 1 {
		return polling.ErrForbidden
	}
	scoped := false
	for _, grant := range grants {
		if grant.GrantID == f.grantID && grant.RoleID == f.roleID && grant.SubjectType == "user" && grant.SubjectID == actor && grant.ScopeType == "project" && grant.ScopeID == project && grant.IsEnabled == 1 && (grant.ExpiresAt == nil || time.Now().Before(*grant.ExpiresAt)) {
			scoped = true
		}
	}
	if !scoped {
		return polling.ErrForbidden
	}
	available := map[string]bool{}
	for _, p := range permissions {
		for _, b := range bindings {
			if b.RoleID == f.roleID && b.PermissionID == p.PermissionID {
				available[p.ResourceType+":"+p.Action] = true
			}
		}
	}
	if !available["project:read"] || !available["project:update"] {
		return polling.ErrForbidden
	}
	for _, id := range ids {
		if id != f.pipelineID || !available["pipeline:read"] || !available["pipeline:trigger"] {
			return polling.ErrForbidden
		}
	}
	return nil
}

func (f *pollingMySQLFixture) prepare(t *testing.T, empty ...bool) (*model.SCMPollingStream, *model.PipelineRun) {
	t.Helper()
	return f.prepareState(t, "opened", empty...)
}
func (f *pollingMySQLFixture) prepareState(t *testing.T, state string, empty ...bool) (*model.SCMPollingStream, *model.PipelineRun) {
	t.Helper()
	// MySQL JSON storage normalizes whitespace. Compare the exact persisted
	// bytes across Baseline, not database output with the original input literal.
	var before model.Project
	if err := f.db.WithContext(f.ctx).Select("project_id", "settings").Where("project_id = ?", f.projectID).Take(&before).Error; err != nil {
		t.Fatal("read persisted settings before baseline:", err)
	}
	var initial map[string]string
	if err := json.Unmarshal(before.Settings, &initial); err != nil || !reflect.DeepEqual(initial, map[string]string{"legacyCursor": "untouched"}) {
		t.Fatal("fixture did not persist the expected legacy settings")
	}
	now := time.Now().UTC()
	started := now.Add(-2 * time.Second).Truncate(time.Second).Add(123456789 * time.Nanosecond)
	s, err := f.streams.Baseline(f.ctx, BaselineInput{ProjectID: f.projectID, ActorID: f.actorID, Scope: f.scope,
		Cursor: polling.Cursor{Version: polling.Version, Boundary: now.Format(time.RFC3339Nano), Complete: true, Observed: map[string]string{"7": "old"}}, StartedAt: started, CompletedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := f.streams.GetByProject(f.ctx, f.projectID)
	if err != nil || roundTrip.BaselineCutoff != started.Format(time.RFC3339Nano) {
		t.Fatalf("MySQL truncated exact baseline cutoff: %v", err)
	}
	if s.State != polling.StateBaselined || s.EnabledBy != "" || s.SelectionJSON != "" {
		t.Fatal("baseline implicitly activated polling")
	}
	var count int64
	if err := f.db.WithContext(f.ctx).Model(&model.PipelineRun{}).Where("pipeline_id = ?", f.pipelineID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("baseline created historical runs")
	}
	var project model.Project
	if err := f.db.WithContext(f.ctx).Select("project_id", "settings").Where("project_id = ?", f.projectID).Take(&project).Error; err != nil {
		t.Fatal("read persisted settings after baseline:", err)
	}
	if string(project.Settings) != string(before.Settings) {
		t.Fatal("baseline changed persisted project settings")
	}
	s, err = f.streams.Enable(f.ctx, EnableInput{ProjectID: f.projectID, ActorID: f.actorID, ExpectedRevision: s.Revision, BaselineID: s.BaselineID, PipelineIDs: []string{f.pipelineID}})
	if err != nil {
		t.Fatal(err)
	}
	requestID, err := polling.EventRequestID(f.scope, "mr:7:revision:new")
	if err != nil {
		t.Fatal(err)
	}
	target := polling.Target{MRID: 7, MRIID: 7, State: "opened", UpdatedAt: "2026-10-16T16:00:00.123456789Z", PipelineID: f.pipelineID, ObservationID: "mr:7:revision:new", RequestID: requestID, SourceBranch: "feature", TargetBranch: "main", SourceSHA: strings.Repeat("a", 40), DefinitionCommitSHA: strings.Repeat("b", 40), DefinitionPath: "pipeline.yaml", ResolvedDefinitionDigest: "v1:" + strings.Repeat("c", 64)}
	if f.scope.Version == polling.ScopeVersion2 {
		target.SourceProjectID = 99
		target.SourceRepository = "fork-owner/repo"
		target.TargetSHA = strings.Repeat("d", 40)
	}
	if f.scope.Version == polling.ScopeVersion3 {
		setV3PollingTarget(&target, f.scope, state)
	}

	next := polling.Cursor{Version: polling.Version, Boundary: now.Add(time.Minute).Format(time.RFC3339Nano), Complete: true, Observed: map[string]string{"7": target.ObservationID}}
	s, err = f.streams.CaptureObservations(f.ctx, CaptureObservationsInput{ProjectID: f.projectID, ExpectedRevision: s.Revision, ActivationGeneration: s.ActivationGeneration, Batch: polling.ObservedBatch{Version: polling.Version, ID: f.batchID, ScopeDigest: s.ScopeDigest, Generation: s.ActivationGeneration, SelectionDigest: s.SelectionDigest, NextCursor: next, Observations: []polling.Observation{target.Observation()}}})
	if err != nil {
		t.Fatal(err)
	}
	targets := []polling.Target{target}
	if len(empty) > 0 && empty[0] {
		targets = nil
	}
	s, err = f.streams.CaptureBatch(f.ctx, CaptureBatchInput{ProjectID: f.projectID, ExpectedRevision: s.Revision, ActivationGeneration: s.ActivationGeneration, Batch: polling.Batch{Version: polling.Version, ID: f.batchID, ScopeDigest: s.ScopeDigest, Generation: s.ActivationGeneration, SelectionDigest: s.SelectionDigest, NextCursor: next, Targets: targets, ObservationCount: 1}})
	if err != nil {
		t.Fatal(err)
	}
	context := polling.RunContext{BaselineID: s.BaselineID, BaselineCutoff: s.BaselineCutoff, Version: polling.Version, StreamID: s.StreamID, Scope: f.scope, ScopeDigest: s.ScopeDigest, ActivationGeneration: s.ActivationGeneration, ActorID: f.actorID, SelectionDigest: s.SelectionDigest, BatchID: f.batchID, BatchRevision: s.Revision, RunID: "polling-test-run-" + uuid.NewString(), Target: target}
	body, err := context.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := context.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return s, &model.PipelineRun{RunID: context.RunID, PipelineID: f.pipelineID, RequestID: requestID, Branch: target.SourceBranch, CommitSha: target.SourceSHA, DefinitionCommitSha: target.DefinitionCommitSHA, DefinitionPath: target.DefinitionPath, TriggerType: int(pipelinev1.TriggerType_TRIGGER_TYPE_EVENT), TriggeredBy: polling.Actor, Status: model.PipelineStatusPending, Env: "{}", PollingContext: body, PollingContextDigest: digest}
}

func (f *pollingMySQLFixture) create(t *testing.T, run *model.PipelineRun) {
	t.Helper()
	result, err := f.runs.CreatePollingRun(f.ctx, run)
	if err != nil || !result.CreatedNow || result.Outcome != polling.OutcomeCreated {
		t.Fatalf("create polling run: outcome=%+v err=%v", result, err)
	}
}

func (f *pollingMySQLFixture) latest(t *testing.T) *model.SCMPollingStream {
	t.Helper()
	s, err := f.otherStreams.GetByProject(f.ctx, f.projectID)
	if err != nil || s == nil {
		t.Fatalf("read stream: %v", err)
	}
	return s
}

func (f *pollingMySQLFixture) stored(t *testing.T, run *model.PipelineRun) *model.PipelineRun {
	t.Helper()
	var stored model.PipelineRun
	if err := f.other.WithContext(f.ctx).Select(pollingRunColumns).Where("run_id = ?", run.RunID).Take(&stored).Error; err != nil {
		t.Fatal(err)
	}
	var jobs int64
	if err := f.other.WithContext(f.ctx).Model(&model.JobRun{}).Where("pipeline_run_id = ?", run.RunID).Count(&jobs).Error; err != nil || jobs != 0 {
		t.Fatalf("repository admission created execution jobs: jobs=%d err=%v", jobs, err)
	}
	return &stored
}

type pollingMySQLGate struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newPollingMySQLGate(t *testing.T) *pollingMySQLGate {
	t.Helper()
	gate := &pollingMySQLGate{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(gate.open)
	return gate
}

func (g *pollingMySQLGate) open() { g.once.Do(func() { close(g.release) }) }

func (g *pollingMySQLGate) hold(tx *gorm.DB) {
	close(g.entered)
	select {
	case <-g.release:
	case <-tx.Statement.Context.Done():
		tx.AddError(tx.Statement.Context.Err())
	}
}

func awaitPollingMySQL[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-ctx.Done():
		t.Fatal("polling MySQL acceptance operation timed out:", ctx.Err())
		var zero T
		return zero
	}
}

func pollingMySQLLockedQuery(tx *gorm.DB, table string) bool {
	locking, ok := tx.Statement.Clauses["FOR"].Expression.(clause.Locking)
	return tx.Statement.Table == table && ok && locking.Strength == "UPDATE"
}

func pollingMySQLQueryGate(t *testing.T, db *gorm.DB, table string) *pollingMySQLGate {
	t.Helper()
	gate := newPollingMySQLGate(t)
	name := "polling:mysql:query:" + uuid.NewString()
	var once sync.Once
	if err := db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if pollingMySQLLockedQuery(tx, table) {
			once.Do(func() { gate.hold(tx) })
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(name) })
	return gate
}

func pollingMySQLWriteGate(t *testing.T, db *gorm.DB, table string, create bool) *pollingMySQLGate {
	t.Helper()
	gate := newPollingMySQLGate(t)
	name := "polling:mysql:write:" + uuid.NewString()
	var once sync.Once
	callback := func(tx *gorm.DB) {
		if tx.Statement.Table == table && tx.Error == nil {
			once.Do(func() { gate.hold(tx) })
		}
	}
	var err error
	if create {
		err = db.Callback().Create().After("gorm:create").Register(name, callback)
		t.Cleanup(func() { _ = db.Callback().Create().Remove(name) })
	} else {
		err = db.Callback().Update().After("gorm:update").Register(name, callback)
		t.Cleanup(func() { _ = db.Callback().Update().Remove(name) })
	}
	if err != nil {
		t.Fatal(err)
	}
	return gate
}

type pollingMySQLAdmissionResult struct {
	outcome polling.AdmissionOutcome
	err     error
}

type pollingMySQLCreationResult struct {
	outcome polling.CreationOutcome
	err     error
}

func pollingMySQLScopeRevocation(t *testing.T, f *pollingMySQLFixture) {
	s, run := f.prepare(t)
	f.create(t, run)
	holder := f.other.WithContext(f.ctx).Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if holder.Error != nil {
		t.Fatal(holder.Error)
	}
	defer holder.Rollback()
	if err := holder.Model(&model.RoleGrant{}).Where("grant_id = ?", f.grantID).Update("scope_id", f.projectID+"-revoked").Error; err != nil {
		t.Fatal(err)
	}
	// Install an ordinary snapshot read inside the admission transaction before
	// its first pipeline lock. The uncommitted revocation is invisible here.
	name := "polling:mysql:snapshot:" + uuid.NewString()
	var once sync.Once
	if err := f.db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if pollingMySQLLockedQuery(tx, "pipeline") {
			once.Do(func() {
				var stale model.RoleGrant
				err := tx.Session(&gorm.Session{NewDB: true, Context: tx.Statement.Context}).Select("grant_id", "scope_id").Where("grant_id = ?", f.grantID).Take(&stale).Error
				if err != nil {
					tx.AddError(err)
				} else if stale.ScopeID != f.projectID {
					tx.AddError(errors.New("repeatable-read snapshot did not observe the pre-revocation scope"))
				}
			})
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(name) })
	gate := pollingMySQLQueryGate(t, f.db, "role_grant")
	done := make(chan pollingMySQLAdmissionResult, 1)
	safe.Go(func() {
		outcome, err := f.runs.StartPollingRunIfPending(f.ctx, run)
		done <- pollingMySQLAdmissionResult{outcome, err}
	})
	awaitPollingMySQL(t, f.ctx, gate.entered)
	if err := holder.Commit().Error; err != nil {
		t.Fatal(err)
	}
	gate.open()
	result := awaitPollingMySQL(t, f.ctx, done)
	if !errors.Is(result.err, polling.ErrForbidden) || result.outcome.ClaimedNow || result.outcome.Outcome != polling.OutcomeRejected {
		t.Fatalf("committed scope revocation failed closed incorrectly: %+v err=%v", result.outcome, result.err)
	}
	stored, latest := f.stored(t, run), f.latest(t)
	if stored.Status != model.PipelineStatusFailed || stored.StartTime != nil || stored.EndTime == nil || latest.State != polling.StatePaused || latest.CursorJSON != s.CursorJSON || latest.ActivationGeneration <= s.ActivationGeneration {
		t.Fatal("revoked admission started work or lost the paused pending evidence")
	}
	batch, err := polling.ParseBatch(latest.PendingBatchJSON)
	if err != nil || len(batch.Results) != 1 || batch.Results[0].Outcome != polling.OutcomeRejected {
		t.Fatalf("revocation lacks one durable rejection receipt: %v", err)
	}
	if err := f.other.WithContext(f.ctx).Model(&model.RoleGrant{}).Where("grant_id = ?", f.grantID).Update("scope_id", f.projectID).Error; err != nil {
		t.Fatal(err)
	}
	if result, err := f.runs.StartPollingRunIfPending(f.ctx, run); err == nil || result.ClaimedNow {
		t.Fatal("scope restoration automatically resumed a paused generation")
	}
}

// Configuration contention must perform a genuine identity change. Reusing
// Gitee as a constant became a no-op when Gitee joined the provider matrix, so
// the after-update witness correctly never fired. Keep the lock gates and their
// deadline unchanged; vary only this fixture input.
func pollingMySQLChangedProvider(current string) string {
	if current == "gitee" {
		return "github"
	}
	return "gitee"
}

func TestPollingMySQLProviderMutationChangesEveryFixtureIdentity(t *testing.T) {
	for _, current := range []string{"gitlab", "github", "gitea", "gitee", "bitbucket"} {
		t.Run(current, func(t *testing.T) {
			next := pollingMySQLChangedProvider(current)
			if next == current || (next != "gitee" && next != "github") {
				t.Fatalf("provider mutation is not a supported distinct identity: %q -> %q", current, next)
			}
		})
	}
}

func pollingMySQLMutationWins(t *testing.T, f *pollingMySQLFixture, kind string) {
	s, run := f.prepare(t)
	f.create(t, run)
	mutationGate := pollingMySQLWriteGate(t, f.other, "scm_polling_stream", false)
	mutation := make(chan error, 1)
	safe.Go(func() {
		var err error
		switch kind {
		case "disable":
			_, err = f.otherStreams.Disable(f.ctx, DisableInput{ProjectID: f.projectID, ActorID: f.actorID, ExpectedRevision: s.Revision})
		case "project_configuration":
			err = (&ProjectRepo{IDatabase: statusTestDB{f.other}}).Update(f.ctx, f.projectID, map[string]any{"default_branch": "changed"})
		case "provider_configuration":
			err = (&ProjectRepo{IDatabase: statusTestDB{f.other}}).Update(f.ctx, f.projectID, map[string]any{"repo_type": pollingMySQLChangedProvider(f.scope.Provider)})
		case "archive":
			err = (&ProjectRepo{IDatabase: statusTestDB{f.other}}).Update(f.ctx, f.projectID, map[string]any{"status": model.ProjectStatusArchived})
		case "pipeline_configuration":
			err = f.otherRuns.Update(f.ctx, f.pipelineID, map[string]any{"pipeline_file_path": "changed.yaml"})
		}
		mutation <- err
	})
	awaitPollingMySQL(t, f.ctx, mutationGate.entered)
	admissionGate := pollingMySQLQueryGate(t, f.db, "pipeline")
	admission := make(chan pollingMySQLAdmissionResult, 1)
	safe.Go(func() {
		outcome, err := f.runs.StartPollingRunIfPending(f.ctx, run)
		admission <- pollingMySQLAdmissionResult{outcome, err}
	})
	awaitPollingMySQL(t, f.ctx, admissionGate.entered)
	admissionGate.open()
	mutationGate.open()
	if err := awaitPollingMySQL(t, f.ctx, mutation); err != nil {
		t.Fatal(err)
	}
	result := awaitPollingMySQL(t, f.ctx, admission)
	if result.err == nil || result.outcome.ClaimedNow || result.outcome.Outcome == polling.OutcomeAdmitted {
		t.Fatalf("admission ignored earlier mutation: outcome=%+v err=%v", result.outcome, result.err)
	}
	stored, latest := f.stored(t, run), f.latest(t)
	if stored.Status != model.PipelineStatusPending || stored.StartTime != nil || stored.EndTime != nil || latest.PendingBatchJSON != s.PendingBatchJSON || latest.CursorJSON != s.CursorJSON || latest.ActivationGeneration <= s.ActivationGeneration {
		t.Fatal("mutation winner started/cancelled pending work or discarded unacknowledged evidence")
	}
}

func pollingMySQLAdmissionWins(t *testing.T, f *pollingMySQLFixture) {
	s, run := f.prepare(t)
	f.create(t, run)
	claimGate := pollingMySQLWriteGate(t, f.db, "scm_polling_stream", false)
	claim := make(chan pollingMySQLAdmissionResult, 1)
	safe.Go(func() {
		outcome, err := f.runs.StartPollingRunIfPending(f.ctx, run)
		claim <- pollingMySQLAdmissionResult{outcome, err}
	})
	awaitPollingMySQL(t, f.ctx, claimGate.entered)
	disableGate := pollingMySQLQueryGate(t, f.other, "project")
	disabled := make(chan error, 1)
	safe.Go(func() {
		_, err := f.otherStreams.Disable(f.ctx, DisableInput{ProjectID: f.projectID, ActorID: f.actorID, ExpectedRevision: s.Revision + 1})
		disabled <- err
	})
	awaitPollingMySQL(t, f.ctx, disableGate.entered)
	disableGate.open()
	claimGate.open()
	result := awaitPollingMySQL(t, f.ctx, claim)
	if result.err != nil || !result.outcome.ClaimedNow || result.outcome.Outcome != polling.OutcomeAdmitted {
		t.Fatalf("earlier admission failed: outcome=%+v err=%v", result.outcome, result.err)
	}
	if err := awaitPollingMySQL(t, f.ctx, disabled); err != nil {
		t.Fatal(err)
	}
	stored, latest := f.stored(t, run), f.latest(t)
	if stored.Status != model.PipelineStatusRunning || stored.StartTime == nil || stored.EndTime != nil || latest.State != polling.StateDisabled || latest.CursorJSON != s.CursorJSON {
		t.Fatal("later disable cancelled previously admitted work or advanced the cursor")
	}
	batch, err := polling.ParseBatch(latest.PendingBatchJSON)
	if err != nil || len(batch.Results) != 1 || batch.Results[0].RunID != run.RunID || batch.Results[0].Outcome != polling.OutcomeAdmitted {
		t.Fatalf("earlier claim did not retain exactly one durable admission receipt: %v", err)
	}
	if repeated, err := f.runs.StartPollingRunIfPending(f.ctx, run); err == nil || repeated.ClaimedNow {
		t.Fatal("disabled generation reclaimed an already admitted run")
	}
}

func pollingMySQLConcurrentCreation(t *testing.T, f *pollingMySQLFixture) {
	_, first := f.prepare(t)
	second := *first
	context, err := polling.ParseRunContext(first.PollingContext)
	if err != nil {
		t.Fatal(err)
	}
	second.RunID = "polling-test-run-" + uuid.NewString()
	context.RunID = second.RunID
	second.PollingContext, err = context.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	second.PollingContextDigest, err = context.Digest()
	if err != nil {
		t.Fatal(err)
	}
	createdGate := pollingMySQLWriteGate(t, f.db, "pipeline_run", true)
	firstDone := make(chan pollingMySQLCreationResult, 1)
	safe.Go(func() {
		outcome, err := f.runs.CreatePollingRun(f.ctx, first)
		firstDone <- pollingMySQLCreationResult{outcome, err}
	})
	awaitPollingMySQL(t, f.ctx, createdGate.entered)
	secondGate := pollingMySQLQueryGate(t, f.other, "pipeline")
	secondDone := make(chan pollingMySQLCreationResult, 1)
	safe.Go(func() {
		outcome, err := f.otherRuns.CreatePollingRun(f.ctx, &second)
		secondDone <- pollingMySQLCreationResult{outcome, err}
	})
	awaitPollingMySQL(t, f.ctx, secondGate.entered)
	secondGate.open()
	createdGate.open()
	a, b := awaitPollingMySQL(t, f.ctx, firstDone), awaitPollingMySQL(t, f.ctx, secondDone)
	if a.err != nil || b.err != nil || !a.outcome.CreatedNow || b.outcome.CreatedNow || a.outcome.RunID != b.outcome.RunID || b.outcome.Outcome != polling.OutcomeUnresolved {
		t.Fatalf("concurrent create results: first=%+v/%v second=%+v/%v", a.outcome, a.err, b.outcome, b.err)
	}
	var count int64
	if err := f.db.WithContext(f.ctx).Model(&model.PipelineRun{}).Where("pipeline_id = ? AND request_id = ?", f.pipelineID, first.RequestID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("duplicate creation persisted %d runs: %v", count, err)
	}
	if stored := f.stored(t, first); stored.Status != model.PipelineStatusPending || stored.StartTime != nil {
		t.Fatal("creation or duplicate replay implicitly admitted the run")
	}
}

func pollingMySQLCursorReplay(t *testing.T, f *pollingMySQLFixture) {
	s, run := f.prepare(t)
	f.create(t, run)
	first, err := f.runs.StartPollingRunIfPending(f.ctx, run)
	if err != nil || !first.ClaimedNow || first.Outcome != polling.OutcomeAdmitted {
		t.Fatalf("claim: %+v err=%v", first, err)
	}
	before := f.stored(t, run)
	if _, err := f.streams.AcknowledgeBatch(f.ctx, AcknowledgeBatchInput{ProjectID: f.projectID, ExpectedRevision: s.Revision, ActivationGeneration: s.ActivationGeneration, BatchID: f.batchID}); !errors.Is(err, polling.ErrConflict) {
		t.Fatalf("stale cursor CAS unexpectedly succeeded: %v", err)
	}
	if latest := f.latest(t); latest.CursorJSON != s.CursorJSON || latest.PendingBatchJSON == "" {
		t.Fatal("cursor CAS failure discarded the frozen batch")
	}
	for i := 0; i < 2; i++ {
		created, err := f.otherRuns.CreatePollingRun(f.ctx, run)
		if err != nil || created.CreatedNow || created.Outcome != polling.OutcomeAdmitted || created.RunID != run.RunID {
			t.Fatalf("replay created work: %+v err=%v", created, err)
		}
		claimed, err := f.otherRuns.StartPollingRunIfPending(f.ctx, run)
		if err != nil || claimed.ClaimedNow || claimed.Outcome != polling.OutcomeAdmitted {
			t.Fatalf("replay reclaimed work: %+v err=%v", claimed, err)
		}
	}
	latest := f.latest(t)
	batch, err := polling.ParseBatch(latest.PendingBatchJSON)
	if err != nil || len(batch.Results) != 1 {
		t.Fatalf("replay duplicated admission receipts: %v", err)
	}
	if after := f.stored(t, run); !reflect.DeepEqual(before, after) {
		t.Fatal("replay changed immutable run or execution evidence")
	}
	acknowledged, err := f.streams.AcknowledgeBatch(f.ctx, AcknowledgeBatchInput{ProjectID: f.projectID, ExpectedRevision: latest.Revision, ActivationGeneration: latest.ActivationGeneration, BatchID: f.batchID})
	if err != nil || acknowledged.PendingBatchJSON != "" || acknowledged.CursorJSON == s.CursorJSON {
		t.Fatalf("fresh cursor CAS did not acknowledge the admitted target: %v", err)
	}
	var count int64
	if err := f.db.WithContext(f.ctx).Model(&model.PipelineRun{}).Where("pipeline_id = ? AND request_id = ?", f.pipelineID, run.RequestID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("cursor replay changed run cardinality: count=%d err=%v", count, err)
	}
}
