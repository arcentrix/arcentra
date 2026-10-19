// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/plugin/dbresolver"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func historyTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "history.db")+"?_busy_timeout=10000&_journal_mode=WAL"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Project{}, &model.Pipeline{}, &model.PipelineRun{}, &model.RoleGrant{}, &model.CopilotConversation{}, &model.CopilotTurn{}, &model.CopilotActionPlan{}))
	return db
}
func historyTestAllow(ctx context.Context, r *Repositories, owner string, p *model.Project, pipeline string) error {
	u, err := r.User.(IExecutionActorReader).GetExecutionActor(ctx, owner)
	if err != nil || u.UserID != owner || u.IsEnabled != 1 {
		return ErrCopilotHistoryForbidden
	}
	return nil
}
func historySeed(t *testing.T, db *gorm.DB, owner, project string) CopilotHistoryScope {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&model.User{}).Where("user_id = ?", owner).Count(&n).Error)
	if n == 0 {
		require.NoError(t, db.Create(&model.User{UserID: owner, IsEnabled: 1}).Error)
	}
	require.NoError(t, db.Model(&model.Project{}).Where("project_id = ?", project).Count(&n).Error)
	if n == 0 {
		require.NoError(t, db.Create(&model.Project{ProjectID: project, OrgID: "org", IsEnabled: 1, Status: model.ProjectStatusActive}).Error)
		require.NoError(t, db.Create(&model.Pipeline{PipelineID: "pipe-" + project, ProjectID: project, IsEnabled: 1, Metadata: `{}`}).Error)
		require.NoError(t, db.Create(&model.PipelineRun{RunID: "run-" + project, PipelineID: "pipe-" + project, ProjectID: project, Env: `{}`}).Error)
	}
	return CopilotHistoryScope{owner, project, "pipe-" + project, "run-" + project}
}

// Shared SQLite/MySQL fixtures must use valid JSON objects: SQLite does not
// enforce the JSON column constraint that the real MySQL gate enforces.
func TestCopilotHistorySeedHasPortableJSONObjects(t *testing.T) {
	db := historyTestDB(t)
	scope := historySeed(t, db, "json-owner", "json-project")
	var pipeline model.Pipeline
	require.NoError(t, db.Where("pipeline_id = ?", scope.PipelineID).First(&pipeline).Error)
	require.JSONEq(t, `{}`, pipeline.Metadata)
	var run model.PipelineRun
	require.NoError(t, db.Where("run_id = ?", scope.RunID).First(&run).Error)
	require.JSONEq(t, `{}`, run.Env)
}
func historyConversation(t *testing.T, r *Repositories, s CopilotHistoryScope, id string) *model.CopilotConversation {
	t.Helper()
	c, err := r.CopilotHistoryCreate(context.Background(), s, id, "create-"+id, "fingerprint-"+id, historyTestAllow)
	require.NoError(t, err)
	return c
}
func historyReservation(c *model.CopilotConversation, id string) CopilotHistoryReserveInput {
	return CopilotHistoryReserveInput{OwnerUserID: c.OwnerUserID, ProjectID: c.ProjectID, ConversationID: c.ConversationID, TurnID: "turn-" + id, RequestID: "request-" + id, ConversationVersion: c.Version, ProviderID: "synthetic", Model: "fixture", ProviderBinding: "provider-binding", InputFingerprint: "input-" + id, ContextDigest: "context-binding", RedactionPolicyVersion: "v1", PayloadCiphertext: []byte("opaque-synthetic-ciphertext"), CipherVersion: 1}
}
func historyCompleted(t *testing.T, r *Repositories, c *model.CopilotConversation, id string) *model.CopilotTurn {
	t.Helper()
	ctx := context.Background()
	v, fresh, err := r.CopilotHistoryReserve(ctx, historyReservation(c, id), historyTestAllow)
	require.NoError(t, err)
	require.True(t, fresh)
	v, err = r.CopilotHistoryDispatch(ctx, CopilotHistoryDispatchInput{OwnerUserID: c.OwnerUserID, ProjectID: c.ProjectID, ConversationID: c.ConversationID, TurnID: v.TurnID, Version: v.Version, PayloadCiphertext: []byte("opaque-question"), CipherVersion: 1}, historyTestAllow)
	require.NoError(t, err)
	v, err = r.CopilotHistoryFinalize(ctx, CopilotHistoryFinalizeInput{OwnerUserID: c.OwnerUserID, ProjectID: c.ProjectID, ConversationID: c.ConversationID, TurnID: v.TurnID, Version: v.Version, State: "completed", PayloadCiphertext: []byte("opaque-question-answer"), CipherVersion: 1}, historyTestAllow)
	require.NoError(t, err)
	return v
}
func TestCopilotHistoryDurableBindingsPrivateScopeAndRestart(t *testing.T) {
	ctx := context.Background()
	db := historyTestDB(t)
	r := &Repositories{database: statusTestDB{db}}
	s := historySeed(t, db, "actor", "project")
	historySeed(t, db, "other", "project")
	historySeed(t, db, "actor", "other-project")
	c := historyConversation(t, r, s, "conversation")
	same, err := r.CopilotHistoryCreate(ctx, s, "ignored-new-id", c.CreateRequestID, c.CreateFingerprint, historyTestAllow)
	require.NoError(t, err)
	require.Equal(t, c.ConversationID, same.ConversationID)
	_, err = r.CopilotHistoryCreate(ctx, s, "different", c.CreateRequestID, "changed", historyTestAllow)
	require.ErrorIs(t, err, ErrCopilotHistoryConflict)
	v := historyCompleted(t, r, c, "first")
	require.EqualValues(t, 1, v.Sequence)
	sameTurn, fresh, err := r.CopilotHistoryReserve(ctx, historyReservation(c, "first"), historyTestAllow)
	require.NoError(t, err)
	require.False(t, fresh)
	require.Equal(t, v.TurnID, sameTurn.TurnID)
	require.Equal(t, "completed", sameTurn.State)
	changed := historyReservation(c, "first")
	changed.InputFingerprint = "changed"
	_, _, err = r.CopilotHistoryReserve(ctx, changed, historyTestAllow)
	require.ErrorIs(t, err, ErrCopilotHistoryConflict)
	restart := &Repositories{database: statusTestDB{db.Session(&gorm.Session{NewDB: true})}}
	stored, err := restart.CopilotHistoryGetTurn(ctx, "actor", "project", c.ConversationID, v.TurnID, historyTestAllow)
	require.NoError(t, err)
	require.Equal(t, v.PayloadCiphertext, stored.PayloadCiphertext)
	for _, scope := range [][3]string{{"other", "project", c.ConversationID}, {"actor", "other-project", c.ConversationID}, {"Actor", "project", c.ConversationID}, {"actor", "Project", c.ConversationID}, {"actor", "project", "Conversation"}} {
		_, err = restart.CopilotHistoryGet(ctx, scope[0], scope[1], scope[2], historyTestAllow)
		require.Error(t, err)
	}
	page, err := r.CopilotHistoryList(ctx, "other", "project", "", 20, historyTestAllow)
	require.NoError(t, err)
	require.Empty(t, page.List)
	require.NoError(t, db.Model(&model.Pipeline{}).Where("pipeline_id = ?", s.PipelineID).UpdateColumn("project_id", "other-project").Error)
	_, err = r.CopilotHistoryGet(ctx, "actor", "project", c.ConversationID, historyTestAllow)
	require.ErrorIs(t, err, ErrCopilotHistoryNotFound)
}
func TestCopilotHistoryPreCancelAndDeleteFenceLateAnswer(t *testing.T) {
	ctx := context.Background()
	db := historyTestDB(t)
	r := &Repositories{database: statusTestDB{db}}
	c := historyConversation(t, r, historySeed(t, db, "actor", "project"), "conversation")
	tombstone, err := r.CopilotHistoryCancel(ctx, c.OwnerUserID, c.ProjectID, c.ConversationID, "request-early", historyTestAllow)
	require.NoError(t, err)
	v, fresh, err := r.CopilotHistoryReserve(ctx, historyReservation(c, "early"), historyTestAllow)
	require.NoError(t, err)
	require.False(t, fresh)
	require.Equal(t, tombstone.TurnID, v.TurnID)
	require.Equal(t, "cancelled", v.State)
	_, err = r.CopilotHistoryDispatch(ctx, CopilotHistoryDispatchInput{OwnerUserID: c.OwnerUserID, ProjectID: c.ProjectID, ConversationID: c.ConversationID, TurnID: v.TurnID, Version: v.Version, PayloadCiphertext: []byte("opaque"), CipherVersion: 1}, historyTestAllow)
	require.ErrorIs(t, err, ErrCopilotHistoryConflict)
	active, _, err := r.CopilotHistoryReserve(ctx, historyReservation(c, "late"), historyTestAllow)
	require.NoError(t, err)
	active, err = r.CopilotHistoryDispatch(ctx, CopilotHistoryDispatchInput{OwnerUserID: c.OwnerUserID, ProjectID: c.ProjectID, ConversationID: c.ConversationID, TurnID: active.TurnID, Version: active.Version, PayloadCiphertext: []byte("opaque"), CipherVersion: 1}, historyTestAllow)
	require.NoError(t, err)
	require.NoError(t, r.CopilotHistoryDelete(ctx, c.OwnerUserID, c.ProjectID, c.ConversationID, c.Version, historyTestAllow))
	_, err = r.CopilotHistoryFinalize(ctx, CopilotHistoryFinalizeInput{OwnerUserID: c.OwnerUserID, ProjectID: c.ProjectID, ConversationID: c.ConversationID, TurnID: active.TurnID, Version: active.Version, State: "completed", PayloadCiphertext: []byte("late-answer"), CipherVersion: 1}, historyTestAllow)
	require.ErrorIs(t, err, ErrCopilotHistoryNotFound)
	var stored model.CopilotTurn
	require.NoError(t, db.Where("turn_id = ?", active.TurnID).First(&stored).Error)
	require.Empty(t, stored.PayloadCiphertext)
	require.False(t, stored.PayloadPresent)
	require.Equal(t, "cancelled", stored.State)
	require.Greater(t, stored.Version, active.Version)
	_, err = r.CopilotHistoryCreate(ctx, historyScope(c), "resurrect", c.CreateRequestID, c.CreateFingerprint, historyTestAllow)
	require.ErrorIs(t, err, ErrCopilotHistoryNotFound)
}
func TestCopilotHistoryConcurrentOwnerLimitAndDuplicate(t *testing.T) {
	historyConcurrencyTest(t, historyTestDB(t))
}
func historyConcurrencyTest(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	r := &Repositories{database: statusTestDB{db}}
	c := historyConversation(t, r, historySeed(t, db, "concurrent-actor", "concurrent-project"), "concurrent-conversation")
	other := historyConversation(t, r, historySeed(t, db, "concurrent-actor", "second-project"), "second-conversation")
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 8)
	newCount := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			local := &Repositories{database: statusTestDB{db.Session(&gorm.Session{NewDB: true})}}
			_, fresh, err := local.CopilotHistoryReserve(ctx, historyReservation(c, "same"), historyTestAllow)
			results <- err
			newCount <- fresh
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(newCount)
	for err := range results {
		require.NoError(t, err)
	}
	n := 0
	for fresh := range newCount {
		if fresh {
			n++
		}
	}
	require.Equal(t, 1, n)
	_, fresh, err := r.CopilotHistoryReserve(ctx, historyReservation(other, "second"), historyTestAllow)
	require.NoError(t, err)
	require.True(t, fresh)
	_, _, err = r.CopilotHistoryReserve(ctx, historyReservation(c, "third"), historyTestAllow)
	require.ErrorIs(t, err, ErrCopilotHistoryCapacity)
	_, err = r.CopilotHistoryCancel(ctx, c.OwnerUserID, c.ProjectID, c.ConversationID, "request-same", historyTestAllow)
	require.NoError(t, err)
	_, fresh, err = r.CopilotHistoryReserve(ctx, historyReservation(c, "third"), historyTestAllow)
	require.NoError(t, err)
	require.True(t, fresh)
}
func TestCopilotHistoryRecoveryPurgeAndNonSlidingExpiry(t *testing.T) {
	ctx := context.Background()
	db := historyTestDB(t)
	r := &Repositories{database: statusTestDB{db}}
	c := historyConversation(t, r, historySeed(t, db, "actor", "project"), "conversation")
	completed := historyCompleted(t, r, c, "complete")
	reserved, _, err := r.CopilotHistoryReserve(ctx, historyReservation(c, "reserved"), historyTestAllow)
	require.NoError(t, err)
	dispatch, _, err := r.CopilotHistoryReserve(ctx, historyReservation(c, "dispatch"), historyTestAllow)
	require.NoError(t, err)
	dispatch, err = r.CopilotHistoryDispatch(ctx, CopilotHistoryDispatchInput{OwnerUserID: c.OwnerUserID, ProjectID: c.ProjectID, ConversationID: c.ConversationID, TurnID: dispatch.TurnID, Version: dispatch.Version, PayloadCiphertext: []byte("opaque"), CipherVersion: 1}, historyTestAllow)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.CopilotTurn{}).Where("id IN ?", []uint64{reserved.ID, dispatch.ID}).UpdateColumn("lease_until", time.Now().Add(-time.Second)).Error)
	processed, err := r.CopilotHistoryReconcile(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1, processed)
	processed, err = r.CopilotHistoryReconcile(ctx, 20)
	require.NoError(t, err)
	require.Equal(t, 1, processed)
	interrupted, err := r.CopilotHistoryGetRequest(ctx, c.OwnerUserID, c.ProjectID, c.ConversationID, reserved.RequestID, historyTestAllow)
	require.NoError(t, err)
	require.Equal(t, "interrupted", interrupted.State)
	unknown, err := r.CopilotHistoryGetRequest(ctx, c.OwnerUserID, c.ProjectID, c.ConversationID, dispatch.RequestID, historyTestAllow)
	require.NoError(t, err)
	require.Equal(t, "outcome_unknown", unknown.State)
	_, fresh, err := r.CopilotHistoryReserve(ctx, historyReservation(c, "dispatch"), historyTestAllow)
	require.NoError(t, err)
	require.False(t, fresh)
	expired := time.Now().Add(-time.Second)
	require.NoError(t, db.Model(&model.CopilotTurn{}).Where("id = ?", completed.ID).UpdateColumn("expires_at", expired).Error)
	_, err = r.CopilotHistoryGetTurn(ctx, c.OwnerUserID, c.ProjectID, c.ConversationID, completed.TurnID, historyTestAllow)
	require.ErrorIs(t, err, ErrCopilotHistoryNotFound)
	processed, err = r.CopilotHistoryPurge(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1, processed)
	var tombstone model.CopilotTurn
	require.NoError(t, db.Where("id = ?", completed.ID).First(&tombstone).Error)
	require.Empty(t, tombstone.PayloadCiphertext)
	require.Equal(t, completed.RequestID, tombstone.RequestID)
	_, fresh, err = r.CopilotHistoryReserve(ctx, historyReservation(c, "complete"), historyTestAllow)
	require.Error(t, err)
	require.False(t, fresh)
	current, err := r.CopilotHistoryGet(ctx, c.OwnerUserID, c.ProjectID, c.ConversationID, historyTestAllow)
	require.NoError(t, err)
	require.Equal(t, c.Version, current.Version)
	require.Greater(t, current.NextSequence, c.NextSequence)
}
func TestCopilotHistoryReviewOnlyPlanCASAndExpiry(t *testing.T) {
	ctx := context.Background()
	db := historyTestDB(t)
	r := &Repositories{database: statusTestDB{db}}
	c := historyConversation(t, r, historySeed(t, db, "actor", "project"), "conversation")
	v := historyCompleted(t, r, c, "done")
	input := CopilotHistoryPlanInput{OwnerUserID: c.OwnerUserID, ProjectID: c.ProjectID, ConversationID: c.ConversationID, TurnID: v.TurnID, PlanID: "plan", RequestID: "plan-request", Kind: "code_patch", PlanDigest: "plan-digest", ConversationVersion: c.Version, TurnVersion: v.Version, SchemaVersion: 1, CipherVersion: 1, PayloadCiphertext: []byte("encrypted-patch")}
	p, fresh, err := r.CopilotHistoryCreatePlan(ctx, input, historyTestAllow)
	require.NoError(t, err)
	require.True(t, fresh)
	require.Equal(t, "proposed", p.State)
	require.WithinDuration(t, p.CreatedAt.Add(CopilotHistoryPlanTTL), p.ExpiresAt, time.Millisecond)
	input.PlanID = "unused"
	same, fresh, err := r.CopilotHistoryCreatePlan(ctx, input, historyTestAllow)
	require.NoError(t, err)
	require.False(t, fresh)
	require.Equal(t, p.PlanID, same.PlanID)
	transition := CopilotHistoryPlanTransitionInput{OwnerUserID: c.OwnerUserID, ProjectID: c.ProjectID, ConversationID: c.ConversationID, PlanID: p.PlanID, PlanDigest: p.PlanDigest, Version: p.Version, State: "reviewed"}
	reviewed, err := r.CopilotHistoryTransitionPlan(ctx, transition, historyTestAllow)
	require.NoError(t, err)
	require.Equal(t, "reviewed", reviewed.State)
	require.NotNil(t, reviewed.ReviewedAt)
	transition.State = "cancelled"
	_, err = r.CopilotHistoryTransitionPlan(ctx, transition, historyTestAllow)
	require.ErrorIs(t, err, ErrCopilotHistoryConflict)
	transition.Version = reviewed.Version
	cancelled, err := r.CopilotHistoryTransitionPlan(ctx, transition, historyTestAllow)
	require.NoError(t, err)
	require.Equal(t, "cancelled", cancelled.State)
	require.NoError(t, db.Model(&model.CopilotActionPlan{}).Where("id = ?", p.ID).UpdateColumn("expires_at", time.Now().Add(-time.Second)).Error)
	_, err = r.CopilotHistoryGetPlan(ctx, c.OwnerUserID, c.ProjectID, c.ConversationID, p.PlanID, historyTestAllow)
	require.ErrorIs(t, err, ErrCopilotHistoryNotFound)
	n, err := r.CopilotHistoryPurge(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	var row model.CopilotActionPlan
	require.NoError(t, db.Where("id = ?", p.ID).First(&row).Error)
	require.Empty(t, row.PayloadCiphertext)
}
func TestCopilotHistoryCursorPageBoundsAndGrantExpiryRollback(t *testing.T) {
	ctx := context.Background()
	db := historyTestDB(t)
	r := &Repositories{database: statusTestDB{db}}
	s := historySeed(t, db, "actor", "project")
	historySeed(t, db, "other", "project")
	for i := 0; i < 5; i++ {
		historyConversation(t, r, s, fmt.Sprintf("conversation-%d", i))
	}
	page, err := r.CopilotHistoryList(ctx, "actor", "project", "", 2, historyTestAllow)
	require.NoError(t, err)
	require.Len(t, page.List, 2)
	require.NotEmpty(t, page.NextCursor)
	next, err := r.CopilotHistoryList(ctx, "actor", "project", page.NextCursor, 2, historyTestAllow)
	require.NoError(t, err)
	require.Len(t, next.List, 2)
	require.Less(t, next.List[0].ID, page.List[1].ID)
	_, err = r.CopilotHistoryList(ctx, "other", "project", page.NextCursor, 2, historyTestAllow)
	require.ErrorIs(t, err, ErrCopilotHistoryInvalid)
	expiry := time.Now().Add(100 * time.Millisecond)
	require.NoError(t, db.Create(&model.RoleGrant{GrantID: "short", SubjectType: "user", SubjectID: "actor", RoleID: "role", ScopeType: "project", ScopeID: "project", IsEnabled: 1, ExpiresAt: &expiry}).Error)
	expiredAuth := func(ctx context.Context, view *Repositories, owner string, p *model.Project, pipeline string) error {
		_, err := view.RoleGrant.ListAllEnabled(ctx)
		if err != nil {
			return err
		}
		time.Sleep(150 * time.Millisecond)
		return historyTestAllow(ctx, view, owner, p, pipeline)
	}
	_, err = r.CopilotHistoryCreate(ctx, s, "rolled-back", "rollback-request", "rollback-binding", expiredAuth)
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Model(&model.CopilotConversation{}).Where("conversation_id = ?", "rolled-back").Count(&count).Error)
	require.Zero(t, count)
	deny := func(context.Context, *Repositories, string, *model.Project, string) error {
		return errors.New("CANARY raw sensitive database error")
	}
	_, err = r.CopilotHistoryGet(ctx, "actor", "project", "conversation-0", deny)
	require.ErrorIs(t, err, ErrCopilotHistoryForbidden)
	require.NotContains(t, err.Error(), "CANARY")
}
func TestCopilotHistorySchemaOldShapeAndPartialPreflight(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migration.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, _ := db.DB()
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&inboxLegacyRun{}, &inboxLegacyApproval{}))
	require.NoError(t, db.Create(&inboxLegacyRun{RunID: "legacy", PipelineID: "pipe", ProjectID: "project", Env: `{"TOKEN":"unchanged"}`}).Error)
	require.NoError(t, MigrateSchema(context.Background(), db))
	for _, m := range []any{&model.CopilotConversation{}, &model.CopilotTurn{}, &model.CopilotActionPlan{}} {
		var n int64
		require.NoError(t, db.Model(m).Count(&n).Error)
		require.Zero(t, n)
	}
	require.NoError(t, MigrateSchema(context.Background(), db))
	var run model.PipelineRun
	require.NoError(t, db.Where("run_id = ?", "legacy").First(&run).Error)
	require.Contains(t, run.Env, "unchanged")
	require.True(t, db.Migrator().HasIndex(&model.CopilotTurn{}, "uk_copilot_turn_request"))
	require.True(t, db.Migrator().HasIndex(&model.CopilotTurn{}, "idx_copilot_turn_purge"))
	require.NoError(t, db.Migrator().DropTable(&model.CopilotTurn{}))
	require.ErrorContains(t, MigrateSchema(context.Background(), db), "partial schema")
	require.False(t, db.Migrator().HasTable(&model.CopilotTurn{}))
}
func TestCopilotHistorySQLFailureDoesNotExposePayload(t *testing.T) {
	db := historyTestDB(t)
	r := &Repositories{database: statusTestDB{db}}
	s := historySeed(t, db, "actor", "project")
	c := historyConversation(t, r, s, "conversation")
	require.NoError(t, db.Migrator().DropTable(&model.CopilotTurn{}))
	in := historyReservation(c, "private")
	in.PayloadCiphertext = []byte("SENSITIVE-CANARY")
	_, _, err := r.CopilotHistoryReserve(context.Background(), in, historyTestAllow)
	require.ErrorIs(t, err, ErrCopilotHistoryUnavailable)
	require.False(t, strings.Contains(err.Error(), "CANARY"))
}

func TestCopilotHistoryCancelFinalizeAndDeleteFinalizeRaces(t *testing.T) {
	for _, deleting := range []bool{false, true} {
		t.Run(fmt.Sprint(deleting), func(t *testing.T) {
			ctx := context.Background()
			db := historyTestDB(t)
			r := &Repositories{database: statusTestDB{db}}
			c := historyConversation(t, r, historySeed(t, db, "actor", "project"), "conversation")
			v, _, err := r.CopilotHistoryReserve(ctx, historyReservation(c, "race"), historyTestAllow)
			require.NoError(t, err)
			v, err = r.CopilotHistoryDispatch(ctx, CopilotHistoryDispatchInput{OwnerUserID: c.OwnerUserID, ProjectID: c.ProjectID, ConversationID: c.ConversationID, TurnID: v.TurnID, Version: v.Version, PayloadCiphertext: []byte("opaque-question"), CipherVersion: 1}, historyTestAllow)
			require.NoError(t, err)
			begin := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-begin
				if deleting {
					_ = r.CopilotHistoryDelete(ctx, c.OwnerUserID, c.ProjectID, c.ConversationID, c.Version, historyTestAllow)
				} else {
					_, _ = r.CopilotHistoryCancel(ctx, c.OwnerUserID, c.ProjectID, c.ConversationID, v.RequestID, historyTestAllow)
				}
			}()
			go func() {
				defer wg.Done()
				<-begin
				_, _ = r.CopilotHistoryFinalize(ctx, CopilotHistoryFinalizeInput{OwnerUserID: c.OwnerUserID, ProjectID: c.ProjectID, ConversationID: c.ConversationID, TurnID: v.TurnID, Version: v.Version, State: "completed", PayloadCiphertext: []byte("opaque-answer"), CipherVersion: 1}, historyTestAllow)
			}()
			close(begin)
			wg.Wait()
			var row model.CopilotTurn
			require.NoError(t, db.Where("id = ?", v.ID).First(&row).Error)
			if deleting {
				require.Empty(t, row.PayloadCiphertext)
				_, err = r.CopilotHistoryGet(ctx, c.OwnerUserID, c.ProjectID, c.ConversationID, historyTestAllow)
				require.ErrorIs(t, err, ErrCopilotHistoryNotFound)
			} else {
				require.Contains(t, []string{"completed", "cancelled"}, row.State)
				if row.State == "cancelled" {
					require.NotEqual(t, []byte("opaque-answer"), row.PayloadCiphertext)
				}
			}
		})
	}
}
func TestCopilotHistoryPageCiphertextAndCapacityBounds(t *testing.T) {
	ctx := context.Background()
	db := historyTestDB(t)
	r := &Repositories{database: statusTestDB{db}}
	c := historyConversation(t, r, historySeed(t, db, "actor", "project"), "conversation")
	for i := 0; i < 3; i++ {
		v := historyCompleted(t, r, c, fmt.Sprint(i))
		require.NoError(t, db.Model(&model.CopilotTurn{}).Where("id = ?", v.ID).UpdateColumn("payload_ciphertext", []byte(strings.Repeat("x", CopilotHistoryMaxCiphertext))).Error)
	}
	page, err := r.CopilotHistoryTurns(ctx, c.OwnerUserID, c.ProjectID, c.ConversationID, 0, 20, historyTestAllow)
	require.NoError(t, err)
	require.Len(t, page.List, 2)
	require.EqualValues(t, 2, page.NextAfter)
	next, err := r.CopilotHistoryTurns(ctx, c.OwnerUserID, c.ProjectID, c.ConversationID, page.NextAfter, 20, historyTestAllow)
	require.NoError(t, err)
	require.Len(t, next.List, 1)
	require.Zero(t, next.NextAfter)
	in := historyReservation(c, "too-large")
	in.PayloadCiphertext = make([]byte, CopilotHistoryMaxCiphertext+1)
	_, _, err = r.CopilotHistoryReserve(ctx, in, historyTestAllow)
	require.ErrorIs(t, err, ErrCopilotHistoryInvalid)
	require.NoError(t, db.Model(&model.CopilotConversation{}).Where("id = ?", c.ID).UpdateColumn("next_sequence", CopilotHistoryMaxTurns+1).Error)
	_, _, err = r.CopilotHistoryReserve(ctx, historyReservation(c, "full"), historyTestAllow)
	require.ErrorIs(t, err, ErrCopilotHistoryCapacity)
}
func TestCopilotHistoryChildExpiryRecheckedBeforeCommit(t *testing.T) {
	ctx := context.Background()
	db := historyTestDB(t)
	r := &Repositories{database: statusTestDB{db}}
	c := historyConversation(t, r, historySeed(t, db, "actor", "project"), "conversation")
	v := historyCompleted(t, r, c, "expiry")
	require.NoError(t, db.Model(&model.CopilotTurn{}).Where("id = ?", v.ID).UpdateColumn("expires_at", time.Now().Add(80*time.Millisecond)).Error)
	// A deliberately slow post-query hook models work after child load. Final
	// expiry checking must reject the body although its parent remains unexpired.
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("slow-history-child", func(tx *gorm.DB) {
		if tx.Statement.Table == "copilot_turn" {
			time.Sleep(120 * time.Millisecond)
		}
	}))
	defer db.Callback().Query().Remove("slow-history-child")
	_, err := r.CopilotHistoryGetTurn(ctx, c.OwnerUserID, c.ProjectID, c.ConversationID, v.TurnID, historyTestAllow)
	require.ErrorIs(t, err, ErrCopilotHistoryNotFound)
}
func TestCopilotHistoryPurgeTombstonesWaitForParentLifetime(t *testing.T) {
	ctx := context.Background()
	db := historyTestDB(t)
	r := &Repositories{database: statusTestDB{db}}
	c := historyConversation(t, r, historySeed(t, db, "actor", "project"), "conversation")
	v := historyCompleted(t, r, c, "old")
	past := time.Now().Add(-time.Hour)
	require.NoError(t, db.Model(&model.CopilotTurn{}).Where("id = ?", v.ID).Updates(map[string]any{"expires_at": past, "tombstone_until": past}).Error)
	require.NoError(t, db.Model(&model.CopilotConversation{}).Where("id = ?", c.ID).UpdateColumn("tombstone_until", past).Error)
	_, err := r.CopilotHistoryPurge(ctx, 20)
	require.NoError(t, err)
	var n int64
	require.NoError(t, db.Model(&model.CopilotTurn{}).Where("id = ?", v.ID).Count(&n).Error)
	require.EqualValues(t, 1, n)
	require.NoError(t, db.Model(&model.CopilotConversation{}).Where("id = ?", c.ID).UpdateColumn("expires_at", past).Error)
	_, err = r.CopilotHistoryPurge(ctx, 20)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.CopilotTurn{}).Where("id = ?", v.ID).Count(&n).Error)
	require.Zero(t, n)
}

func TestCopilotHistoryAlwaysUsesPrimaryDespiteStaleReplica(t *testing.T) {
	ctx := context.Background()
	primary := historyTestDB(t)
	replica := historyTestDB(t)
	pRepo := &Repositories{database: statusTestDB{primary}}
	replicaRepo := &Repositories{database: statusTestDB{replica}}
	c := historyConversation(t, pRepo, historySeed(t, primary, "actor", "project"), "conversation")
	historyConversation(t, replicaRepo, historySeed(t, replica, "actor", "project"), "conversation")
	require.NoError(t, primary.Model(&model.User{}).Where("user_id = ?", "actor").UpdateColumn("is_enabled", 0).Error)
	require.NoError(t, primary.Use(dbresolver.Register(dbresolver.Config{Replicas: []gorm.Dialector{sqlite.Open(replica.Dialector.(*sqlite.Dialector).DSN)}})))
	var stale model.User
	require.NoError(t, primary.Where("user_id = ?", "actor").First(&stale).Error)
	require.Equal(t, 1, stale.IsEnabled)
	_, err := pRepo.CopilotHistoryGet(ctx, "actor", "project", c.ConversationID, historyTestAllow)
	require.ErrorIs(t, err, ErrCopilotHistoryForbidden)
	_, err = pRepo.CopilotHistoryList(ctx, "actor", "project", "", 20, historyTestAllow)
	require.ErrorIs(t, err, ErrCopilotHistoryForbidden)
}

// Commit succeeds in the database but the caller receives a synthetic transport
// failure. This is an actual lost-acknowledgement injection, not a second write.
type historyLostCommitPool struct {
	gorm.ConnPool
	pool *sql.DB
	lose *atomic.Bool
}

func (p historyLostCommitPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.pool.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &historyLostCommitTx{Tx: tx, lose: p.lose}, nil
}

type historyLostCommitTx struct {
	*sql.Tx
	lose *atomic.Bool
}

func (t *historyLostCommitTx) Commit() error {
	if err := t.Tx.Commit(); err != nil {
		return err
	}
	if t.lose.CompareAndSwap(true, false) {
		return errors.New("synthetic lost commit acknowledgement")
	}
	return nil
}
func TestCopilotHistoryLostCommitAcknowledgementPrimaryReconciliation(t *testing.T) {
	historyLostAcknowledgementTest(t, historyTestDB(t))
}

func historyLostAcknowledgementTest(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	r := &Repositories{database: statusTestDB{db}}
	c := historyConversation(t, r, historySeed(t, db, "actor", "project"), "conversation")
	v, _, err := r.CopilotHistoryReserve(ctx, historyReservation(c, "lost-ack"), historyTestAllow)
	require.NoError(t, err)
	v, err = r.CopilotHistoryDispatch(ctx, CopilotHistoryDispatchInput{OwnerUserID: c.OwnerUserID, ProjectID: c.ProjectID, ConversationID: c.ConversationID, TurnID: v.TurnID, Version: v.Version, PayloadCiphertext: []byte("opaque-question"), CipherVersion: 1}, historyTestAllow)
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	lose := &atomic.Bool{}
	lose.Store(true)
	wrapped := db.Session(&gorm.Session{NewDB: true, Initialized: true})
	wrapped.Statement.ConnPool = historyLostCommitPool{ConnPool: pool, pool: pool, lose: lose}
	lossRepo := &Repositories{database: statusTestDB{wrapped}}
	result, err := lossRepo.CopilotHistoryFinalize(ctx, CopilotHistoryFinalizeInput{OwnerUserID: c.OwnerUserID, ProjectID: c.ProjectID, ConversationID: c.ConversationID, TurnID: v.TurnID, Version: v.Version, State: "completed", PayloadCiphertext: []byte("opaque-durable-answer"), CipherVersion: 1}, historyTestAllow)
	require.Nil(t, result)
	require.ErrorIs(t, err, ErrCopilotHistoryUnavailable)
	require.False(t, lose.Load())
	recovered, err := r.CopilotHistoryGetRequest(ctx, c.OwnerUserID, c.ProjectID, c.ConversationID, v.RequestID, historyTestAllow)
	require.NoError(t, err)
	require.Equal(t, "completed", recovered.State)
	require.Equal(t, []byte("opaque-durable-answer"), recovered.PayloadCiphertext)
	duplicate, fresh, err := r.CopilotHistoryReserve(ctx, historyReservation(c, "lost-ack"), historyTestAllow)
	require.NoError(t, err)
	require.False(t, fresh)
	require.Equal(t, recovered.Version, duplicate.Version)
}
