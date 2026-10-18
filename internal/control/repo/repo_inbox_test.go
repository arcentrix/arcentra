// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func inboxTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "inbox.db")+"?_busy_timeout=5000&_journal_mode=WAL"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sql, err := db.DB()
	require.NoError(t, err)
	sql.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sql.Close() })
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Project{}, &model.Pipeline{}, &model.PipelineRun{}, &model.ApprovalRequest{}, &model.JobRun{}, &model.StepRun{}, &model.DeploymentRelease{}, &model.InboxDelivery{}, &model.InboxWork{}, &model.InboxCursor{}, &model.InboxSweep{}))
	require.NoError(t, inboxSchemaIndexes(context.Background(), db))
	return db
}
func inboxAllow(ctx context.Context, r *Repositories, owner string, p *model.Project, pipeline string, approval string) error {
	user, err := r.User.(IExecutionActorReader).GetExecutionActor(ctx, owner)
	if err != nil {
		return ErrInboxForbidden
	}
	if user.IsEnabled != 1 {
		return ErrInboxForbidden
	}
	return nil
}
func seedInbox(t *testing.T, db *gorm.DB, id, owner string, status int) *model.PipelineRun {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&model.User{}).Where("user_id = ?", owner).Count(&n).Error)
	if n == 0 {
		require.NoError(t, db.Create(&model.User{UserID: owner, IsEnabled: 1}).Error)
	}
	require.NoError(t, db.Model(&model.Project{}).Where("project_id = ?", "project").Count(&n).Error)
	if n == 0 {
		require.NoError(t, db.Create(&model.Project{ProjectID: "project", OrgID: "org", IsEnabled: 1}).Error)
		require.NoError(t, db.Create(&model.Pipeline{PipelineID: "pipeline", ProjectID: "project", IsEnabled: 1}).Error)
	}
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	end := start.Add(30 * time.Second)
	run := &model.PipelineRun{RunID: id, PipelineID: "pipeline", ProjectID: "project", InboxActorID: owner, InboxOrigin: 1, TriggerType: 1, TriggeredBy: "untrusted-display", Status: status, StartTime: &start, EndTime: &end, Env: `{"TOKEN":"never-copy-secret"}`, PipelineName: "never-copy-name"}
	require.NoError(t, db.Create(run).Error)
	require.NoError(t, enrollInboxWork(db, "run", id))
	return run
}
func dueInbox(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Model(&model.InboxWork{}).Where("state = 0").UpdateColumn("next_check_at", time.Now().Add(-time.Minute)).Error)
}
func inboxTick(t *testing.T, r *Repositories) {
	t.Helper()
	_, err := r.InboxReconcile(context.Background(), inboxAllow)
	require.NoError(t, err)
}
func TestInboxDurableRunHistoryOwnershipRevocationAndRead(t *testing.T) {
	ctx := context.Background()
	db := inboxTestDB(t)
	r := &Repositories{database: statusTestDB{db}}
	for i, status := range []int{model.PipelineStatusSuccess, model.PipelineStatusFailed, model.PipelineStatusCancelled} {
		seedInbox(t, db, fmt.Sprint("run", i), "actor", status)
	}
	seedInbox(t, db, "foreign", "other", model.PipelineStatusSuccess)
	inboxTick(t, r)
	page, err := r.InboxList(ctx, "actor", "all", "", 20, inboxAllow)
	require.NoError(t, err)
	require.Len(t, page.List, 3)
	body, _ := json.Marshal(page)
	require.NotContains(t, string(body), "never-copy")
	require.NotContains(t, string(body), "TriggeredBy")
	count, err := r.InboxUnreadCount(ctx, "actor", inboxAllow)
	require.NoError(t, err)
	require.Equal(t, 3, count.Count)
	first := page.List[0]
	_, err = r.InboxMarkRead(ctx, "other", first.ID, inboxAllow)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	read, err := r.InboxMarkRead(ctx, "actor", first.ID, inboxAllow)
	require.NoError(t, err)
	again, err := r.InboxMarkRead(ctx, "actor", first.ID, inboxAllow)
	require.NoError(t, err)
	require.Equal(t, read.ReadAt, again.ReadAt)
	// Restart/replay never resets read_at or delivery time.
	require.NoError(t, db.Model(&model.InboxWork{}).Where("1=1").Updates(map[string]any{"state": 0, "terminal_done": false, "next_check_at": time.Now()}).Error)
	reopened := &Repositories{database: statusTestDB{db.Session(&gorm.Session{NewDB: true})}}
	inboxTick(t, reopened)
	page, err = reopened.InboxList(ctx, "actor", "all", "", 20, inboxAllow)
	require.NoError(t, err)
	require.Equal(t, first.DeliveredAt, page.List[0].DeliveredAt)
	require.Equal(t, read.ReadAt, *page.List[0].ReadAt)
	deny := func(ctx context.Context, r *Repositories, o string, p *model.Project, pipeline string, a string) error {
		if p != nil {
			return ErrInboxForbidden
		}
		return inboxAllow(ctx, r, o, p, pipeline, a)
	}
	page, err = r.InboxList(ctx, "actor", "all", "", 20, deny)
	require.NoError(t, err)
	require.Empty(t, page.List)
	count, err = r.InboxUnreadCount(ctx, "actor", deny)
	require.NoError(t, err)
	require.Zero(t, count.Count)
	_, err = r.InboxMarkRead(ctx, "actor", first.ID, deny)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	page, err = r.InboxList(ctx, "actor", "all", "", 20, inboxAllow)
	require.NoError(t, err)
	require.Len(t, page.List, 3)
	require.NoError(t, db.Model(&model.User{}).Where("user_id = ?", "actor").Update("is_enabled", 0).Error)
	_, err = r.InboxList(ctx, "actor", "all", "", 20, inboxAllow)
	require.ErrorIs(t, err, ErrInboxForbidden)
}
func TestInboxLateTerminalRepairAndNoLegacyFallback(t *testing.T) {
	db := inboxTestDB(t)
	r := &Repositories{database: statusTestDB{db}}
	old := seedInbox(t, db, "low", "actor", model.PipelineStatusRunning)
	old.EndTime = nil
	require.NoError(t, db.Model(old).UpdateColumn("end_time", nil).Error)
	seedInbox(t, db, "later", "actor", model.PipelineStatusSuccess)
	legacy := seedInbox(t, db, "legacy", "actor", model.PipelineStatusSuccess)
	require.NoError(t, db.Model(legacy).Updates(map[string]any{"inbox_actor_id": "", "inbox_origin": 0, "project_id": ""}).Error)
	unadmitted := seedInbox(t, db, "unadmitted", "actor", model.PipelineStatusFailed)
	require.NoError(t, db.Model(unadmitted).Updates(map[string]any{"project_id": "", "start_time": nil}).Error)
	inboxTick(t, r)
	var count int64
	require.NoError(t, db.Model(&model.InboxDelivery{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, db.Where("source_id = ?", "low").Delete(&model.InboxWork{}).Error)
	now := time.Now().UTC()
	require.NoError(t, db.Model(old).Updates(map[string]any{"status": model.PipelineStatusCancelled, "end_time": now}).Error)
	dueInbox(t, db)
	inboxTick(t, r)
	require.NoError(t, db.Model(&model.InboxDelivery{}).Count(&count).Error)
	require.EqualValues(t, 2, count)
	require.NoError(t, db.Model(&model.Pipeline{}).Where("pipeline_id = ?", "pipeline").Update("project_id", "moved").Error)
	page, err := r.InboxList(context.Background(), "actor", "all", "", 20, inboxAllow)
	require.NoError(t, err)
	require.Len(t, page.List, 2)
	for _, item := range page.List {
		require.Nil(t, item.Target)
	}
}
func TestInboxBoundedCursorsFilteredPagesAndCount(t *testing.T) {
	ctx := context.Background()
	db := inboxTestDB(t)
	r := &Repositories{database: statusTestDB{db}}
	run := seedInbox(t, db, "run", "actor", model.PipelineStatusSuccess)
	for i := 0; i < 205; i++ {
		d := model.InboxDelivery{RecipientID: "actor", SourceKind: "run", SourceID: fmt.Sprint("unknown", i), EventKind: "terminal", RunID: run.RunID, ProjectID: run.ProjectID, PipelineID: run.PipelineID, Status: "success", OccurredAt: time.Now(), DeliveredAt: time.Now()}
		require.NoError(t, db.Create(&d).Error)
	}
	// The newest 200 corrupt/hidden rows still advance the opaque continuation.
	page, err := r.InboxList(ctx, "actor", "all", "", 20, inboxAllow)
	require.NoError(t, err)
	require.Empty(t, page.List)
	require.True(t, page.ScanIncomplete)
	require.NotNil(t, page.NextCursor)
	require.Len(t, *page.NextCursor, 64)
	require.NotContains(t, *page.NextCursor, "unknown")
	count, err := r.InboxUnreadCount(ctx, "actor", inboxAllow)
	require.NoError(t, err)
	require.True(t, count.Incomplete)
	require.Zero(t, count.Count)
	_, err = r.InboxList(ctx, "actor", "unread", *page.NextCursor, 20, inboxAllow)
	require.ErrorIs(t, err, ErrInboxCursor)
	require.NoError(t, db.Create(&model.User{UserID: "other", IsEnabled: 1}).Error)
	_, err = r.InboxList(ctx, "other", "all", *page.NextCursor, 20, inboxAllow)
	require.ErrorIs(t, err, ErrInboxCursor)
	next, err := r.InboxList(ctx, "actor", "all", *page.NextCursor, 20, inboxAllow)
	require.NoError(t, err)
	require.Nil(t, next.NextCursor)
	token := *page.NextCursor
	for i := 0; i < 33; i++ {
		_, err = r.inboxNewCursor(ctx, "actor", "all", 10, inboxAllow)
		require.NoError(t, err)
	}
	var n int64
	require.NoError(t, db.Model(&model.InboxCursor{}).Where("owner_id = ?", "actor").Count(&n).Error)
	require.EqualValues(t, 32, n)
	_, err = r.InboxList(ctx, "actor", "all", token, 20, inboxAllow)
	require.ErrorIs(t, err, ErrInboxCursor)
	require.NoError(t, db.Model(&model.InboxCursor{}).Where("1=1").Update("expires_at", time.Now().Add(-time.Second)).Error)
	require.NoError(t, r.cleanupInboxCursors(ctx))
	require.NoError(t, db.Model(&model.InboxCursor{}).Count(&n).Error)
	require.Zero(t, n)
}
func TestInboxLeaseFencingAndAtomicDeliveryRollback(t *testing.T) {
	ctx := context.Background()
	db := inboxTestDB(t)
	r := &Repositories{database: statusTestDB{db}}
	seedInbox(t, db, "run", "actor", model.PipelineStatusSuccess)
	first, err := r.claimInbox(ctx, "run")
	require.NoError(t, err)
	require.NotNil(t, first)
	second, err := r.claimInbox(ctx, "run")
	require.NoError(t, err)
	require.Nil(t, second)
	dueInbox(t, db)
	second, err = r.claimInbox(ctx, "run")
	require.NoError(t, err)
	require.NotNil(t, second)
	err = r.deliverInbox(ctx, first, "actor", "terminal", inboxAllow)
	require.ErrorIs(t, err, errInboxFence)
	var n int64
	require.NoError(t, db.Model(&model.InboxDelivery{}).Count(&n).Error)
	require.Zero(t, n)
	require.NoError(t, r.deliverInbox(ctx, second, "actor", "terminal", inboxAllow))
	require.NoError(t, r.finishInbox(ctx, second, false, true, ""))
	require.NoError(t, db.Model(&model.InboxDelivery{}).Count(&n).Error)
	require.EqualValues(t, 1, n)
	// Failed source terminal commit must not create an event.
	pending := seedInbox(t, db, "rollback", "actor", model.PipelineStatusRunning)
	require.Error(t, db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tx.Model(pending).Update("status", model.PipelineStatusFailed).Error)
		return errors.New("rollback")
	}))
	inboxTick(t, r)
	require.NoError(t, db.Model(&model.InboxDelivery{}).Count(&n).Error)
	require.EqualValues(t, 1, n)
}
func TestInboxConcurrentWorkersAndReads(t *testing.T) {
	db := inboxTestDB(t)
	r := &Repositories{database: statusTestDB{db}}
	seedInbox(t, db, "run", "actor", model.PipelineStatusSuccess)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = r.InboxReconcile(context.Background(), inboxAllow) }()
	}
	wg.Wait()
	dueInbox(t, db)
	inboxTick(t, r)
	page, err := r.InboxList(context.Background(), "actor", "all", "", 20, inboxAllow)
	require.NoError(t, err)
	require.Len(t, page.List, 1)
	results := make(chan *InboxRead, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := r.InboxMarkRead(context.Background(), "actor", page.List[0].ID, inboxAllow)
			results <- v
			errs <- e
		}()
	}
	wg.Wait()
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	require.Equal(t, (<-results).ReadAt, (<-results).ReadAt)
}
func TestInboxRetryApprovalRealPinTerminalBeforePollAndDeferredCandidate(t *testing.T) {
	f := gatedPinFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.InboxWork{}, &model.InboxDelivery{}, &model.InboxCursor{}, &model.InboxSweep{}, &model.User{}, &model.Project{}, &model.Pipeline{}))
	require.NoError(t, inboxSchemaIndexes(f.ctx, f.db))
	require.NoError(t, f.db.Model(&model.Project{}).Where("project_id = ?", "project").Updates(map[string]any{"org_id": "org", "is_enabled": 1}).Error)
	require.NoError(t, f.db.Model(&model.Pipeline{}).Where("pipeline_id = ?", "pipeline").Update("is_enabled", 1).Error)
	for _, id := range []string{"actor", "voter"} {
		require.NoError(t, f.db.Create(&model.User{UserID: id, IsEnabled: 1}).Error)
	}
	now := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, f.db.Model(&model.PipelineRun{}).Where("run_id = ?", "run").Updates(map[string]any{"inbox_origin": 1, "inbox_actor_id": "actor", "project_id": "project", "start_time": now}).Error)
	job, err := f.jobs.GetOrRecordJobInputs(f.ctx, f.identity, f.manifest)
	require.NoError(t, err)
	var a model.ApprovalRequest
	require.NoError(t, f.db.Where("approval_id = ?", job.RetryApprovalID).First(&a).Error)
	require.Equal(t, 1, a.InboxOrigin)
	// Exercise the authoritative cancellation owner, not a notification callback.
	approvals := &ApprovalRepo{IDatabase: statusTestDB{f.db}}
	_, err = approvals.Cancel(f.ctx, a.ApprovalID, "never-copy-reason")
	require.NoError(t, err)
	r := &Repositories{database: statusTestDB{f.db}}
	require.NoError(t, f.db.Model(&model.User{}).Where("user_id = ?", "voter").Update("is_enabled", 0).Error)
	inboxTick(t, r)
	var deliveries []model.InboxDelivery
	require.NoError(t, f.db.Find(&deliveries).Error)
	require.Len(t, deliveries, 1)
	require.Equal(t, "actor", deliveries[0].RecipientID)
	require.NoError(t, f.db.Model(&model.User{}).Where("user_id = ?", "voter").Update("is_enabled", 1).Error)
	dueInbox(t, f.db)
	inboxTick(t, r)
	page, err := r.InboxList(f.ctx, "voter", "all", "", 20, inboxAllow)
	require.NoError(t, err)
	require.Len(t, page.List, 1)
	require.Equal(t, "approval_requested", page.List[0].Kind)
	require.Equal(t, "cancelled", page.List[0].Status)
	body, _ := json.Marshal(page)
	require.NotContains(t, string(body), "never-copy-reason")
	require.NotContains(t, string(body), a.EvidenceJSON)
}
func TestInboxRetryCandidateLimitMalformedAndPendingDeadline(t *testing.T) {
	// Parser-level upper bound is enforced independently of mutable vote/status.
	for _, n := range []int{100, 101} {
		c := make([]string, n)
		for i := range c {
			c[i] = fmt.Sprint("v", i)
		}
		raw, _ := json.Marshal(c)
		a := model.ApprovalRequest{RequestedBy: "actor", CandidateApprovers: string(raw), Mode: "any", RequiredApproverCount: 1}
		_, err := model.ApprovalCandidates(&a)
		if n == 100 {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
	}
	a := model.ApprovalRequest{RequestedBy: "actor", CandidateApprovers: `["v","v"]`, Mode: "any", RequiredApproverCount: 1}
	_, err := model.ApprovalCandidates(&a)
	require.Error(t, err)
	require.Equal(t, "pending", inboxApprovalStatus(model.ApprovalStatusPending))
	require.Empty(t, inboxRunStatus(model.PipelineStatusRunning))
	require.NotEmpty(t, retryapproval.Kind)
	require.NotEmpty(t, strings.TrimSpace("proof"))
}

func TestInboxGenericApprovalAuditAndPluginCannotMintRecipients(t *testing.T) {
	db := inboxTestDB(t)
	r := &Repositories{database: statusTestDB{db}}
	run := seedInbox(t, db, "manual", "actor", model.PipelineStatusRunning)
	require.NoError(t, db.Create(&model.User{UserID: "candidate", IsEnabled: 1}).Error)
	a := &model.ApprovalRequest{ApprovalID: "plugin-only", PipelineRunID: run.RunID, RequestedBy: "actor", CandidateApprovers: `["candidate"]`, Mode: "any", RequiredApproverCount: 1, Status: model.ApprovalStatusPending, Plugin: "plugin:arbitrary"}
	require.NoError(t, (&ApprovalRepo{IDatabase: statusTestDB{db}}).Create(context.Background(), a))
	inboxTick(t, r)
	var n int64
	require.NoError(t, db.Model(&model.InboxDelivery{}).Count(&n).Error)
	require.Zero(t, n)
	// Even damaged work is not recipient authority: recognized proof is required.
	require.NoError(t, enrollInboxWork(db, "approval", a.ApprovalID))
	dueInbox(t, db)
	inboxTick(t, r)
	require.NoError(t, db.Model(&model.InboxDelivery{}).Count(&n).Error)
	require.Zero(t, n)
	var w model.InboxWork
	require.NoError(t, db.Where("source_kind = ? AND source_id = ?", "approval", a.ApprovalID).First(&w).Error)
	require.Equal(t, "invalid_source", w.ErrorCode)
	require.Zero(t, w.State)
	a.ApprovalID = "forged-origin"
	a.InboxOrigin = 1
	require.Error(t, (&ApprovalRepo{IDatabase: statusTestDB{db}}).Create(context.Background(), a))
}
func TestInboxHundredCandidateFanoutFailureNeverCompletesPartialProjection(t *testing.T) {
	candidates := make([]string, 100)
	for i := range candidates {
		candidates[i] = fmt.Sprintf("v%03d", i)
	}
	f := newGenerationFixture(t)
	configureGatedPinFixturePolicy(t, f, retryapproval.Policy{ID: "policy", Candidates: candidates, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 300})
	require.NoError(t, f.db.AutoMigrate(&model.User{}, &model.InboxWork{}, &model.InboxDelivery{}, &model.InboxCursor{}, &model.InboxSweep{}))
	require.NoError(t, inboxSchemaIndexes(f.ctx, f.db))
	for _, id := range append([]string{"actor"}, candidates...) {
		require.NoError(t, f.db.Create(&model.User{UserID: id, IsEnabled: 1}).Error)
	}
	require.NoError(t, f.db.Model(&model.PipelineRun{}).Where("run_id = ?", "run").Updates(map[string]any{"inbox_origin": 1, "inbox_actor_id": "actor", "project_id": "project", "start_time": time.Now().Add(-time.Minute)}).Error)
	job, err := f.jobs.GetOrRecordJobInputs(f.ctx, f.identity, f.manifest)
	require.NoError(t, err)
	require.NoError(t, f.db.Callback().Create().Before("gorm:create").Register("inbox:partial", func(tx *gorm.DB) {
		if tx.Statement.Table == "inbox_delivery" {
			if d, ok := tx.Statement.Dest.(*model.InboxDelivery); ok && d.RecipientID == "v050" {
				tx.AddError(errors.New("injected write failure"))
			}
		}
	}))
	r := &Repositories{database: statusTestDB{f.db}}
	out, err := r.InboxReconcile(f.ctx, inboxAllow)
	require.NoError(t, err)
	require.Equal(t, 100, out.Recipients)
	var work model.InboxWork
	require.NoError(t, f.db.Where("source_kind = ? AND source_id = ?", "approval", job.RetryApprovalID).First(&work).Error)
	require.False(t, work.RequestedDone)
	require.False(t, work.TerminalDone)
	require.Zero(t, work.State)
	var count int64
	require.NoError(t, f.db.Model(&model.InboxDelivery{}).Count(&count).Error)
	require.EqualValues(t, 99, count)
	page, err := r.InboxList(f.ctx, "v000", "all", "", 20, inboxAllow)
	require.NoError(t, err)
	read, err := r.InboxMarkRead(f.ctx, "v000", page.List[0].ID, inboxAllow)
	require.NoError(t, err)
	require.NoError(t, f.db.Callback().Create().Remove("inbox:partial"))
	dueInbox(t, f.db)
	inboxTick(t, r)
	require.NoError(t, f.db.Where("id = ?", work.ID).First(&work).Error)
	require.True(t, work.RequestedDone)
	require.False(t, work.TerminalDone)
	require.Zero(t, work.State, "requested projection retains terminal observation obligation")
	require.NoError(t, f.db.Model(&model.InboxDelivery{}).Count(&count).Error)
	require.EqualValues(t, 100, count)
	page, err = r.InboxList(f.ctx, "v000", "all", "", 20, inboxAllow)
	require.NoError(t, err)
	require.Equal(t, read.ReadAt, *page.List[0].ReadAt)
}

func inboxTwoCandidateFixture(t *testing.T) (*generationFixture, *Repositories, string) {
	t.Helper()
	f := newGenerationFixture(t)
	configureGatedPinFixturePolicy(t, f, retryapproval.Policy{ID: "policy", Candidates: []string{"first", "second"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 300})
	require.NoError(t, f.db.AutoMigrate(&model.User{}, &model.InboxWork{}, &model.InboxDelivery{}, &model.InboxCursor{}, &model.InboxSweep{}))
	require.NoError(t, inboxSchemaIndexes(f.ctx, f.db))
	for _, id := range []string{"actor", "first", "second"} {
		require.NoError(t, f.db.Create(&model.User{UserID: id, IsEnabled: 1}).Error)
	}
	require.NoError(t, f.db.Model(&model.PipelineRun{}).Where("run_id = ?", "run").Updates(map[string]any{"inbox_origin": 1, "inbox_actor_id": "actor", "project_id": "project", "start_time": time.Now().Add(-time.Minute)}).Error)
	job, err := f.jobs.GetOrRecordJobInputs(f.ctx, f.identity, f.manifest)
	require.NoError(t, err)
	// Use a real file-backed SQLite database for cancellation/restart tests:
	// database/sql may discard a cancelled connection (and its :memory: DB).
	path := filepath.Join(t.TempDir(), "fanout.db")
	require.NoError(t, f.db.Exec("VACUUM INTO ?", path).Error)
	persistent, err := gorm.Open(sqlite.Open(path+"?_busy_timeout=5000&_journal_mode=WAL"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := persistent.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	f.db = persistent
	f.jobs = &JobRunRepo{IDatabase: statusTestDB{persistent}}
	f.steps = &StepRunRepo{IDatabase: statusTestDB{persistent}}
	return f, &Repositories{database: statusTestDB{f.db}}, job.RetryApprovalID
}
func TestInboxFanoutAlternatingRevocationUsesVerifiedDurableProgress(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			f, r, id := inboxTwoCandidateFixture(t)
			require.NoError(t, f.db.Model(&model.User{}).Where("user_id = ?", "second").Update("is_enabled", 0).Error)
			inboxTick(t, r)
			require.NoError(t, f.db.Model(&model.User{}).Where("user_id = ?", "first").Update("is_enabled", 0).Error)
			require.NoError(t, f.db.Model(&model.User{}).Where("user_id = ?", "second").Update("is_enabled", 1).Error)
			if corrupt {
				require.NoError(t, f.db.Model(&model.InboxDelivery{}).Where("recipient_id = ?", "first").Update("project_id", "mismatched-scope").Error)
			}
			dueInbox(t, f.db)
			inboxTick(t, r)
			var w model.InboxWork
			require.NoError(t, f.db.Where("source_kind = ? AND source_id = ?", "approval", id).First(&w).Error)
			var count int64
			require.NoError(t, f.db.Model(&model.InboxDelivery{}).Count(&count).Error)
			if corrupt {
				require.False(t, w.RequestedDone)
				require.Equal(t, "invalid_source", w.ErrorCode)
				require.EqualValues(t, 1, count)
			} else {
				require.True(t, w.RequestedDone)
				require.False(t, w.TerminalDone)
				require.EqualValues(t, 2, count)
			}
		})
	}
}
func TestInboxSlowFirstCandidateCannotStarveLaterRecipientAcrossTicks(t *testing.T) {
	f, r, id := inboxTwoCandidateFixture(t)
	slow := func(ctx context.Context, view *Repositories, actor string, p *model.Project, pipeline, approval string) error {
		if actor == "first" {
			<-ctx.Done()
			return ctx.Err()
		}
		return inboxAllow(ctx, view, actor, p, pipeline, approval)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	_, err := r.InboxReconcile(ctx, slow)
	cancel()
	require.Error(t, err)
	var w model.InboxWork
	require.NoError(t, f.db.Where("source_kind = ? AND source_id = ?", "approval", id).First(&w).Error)
	require.Equal(t, 1, w.RecipientOffset)
	dueInbox(t, f.db)
	ctx, cancel = context.WithTimeout(context.Background(), 80*time.Millisecond)
	_, err = r.InboxReconcile(ctx, slow)
	cancel()
	require.Error(t, err)
	var count int64
	require.NoError(t, f.db.Model(&model.InboxDelivery{}).Where("recipient_id = ?", "second").Count(&count).Error)
	require.EqualValues(t, 1, count, "later candidate must progress even while first never succeeds")
	require.NoError(t, f.db.Where("id = ?", w.ID).First(&w).Error)
	require.False(t, w.RequestedDone)
	dueInbox(t, f.db)
	inboxTick(t, r)
	require.NoError(t, f.db.Where("id = ?", w.ID).First(&w).Error)
	require.True(t, w.RequestedDone)
}

func TestInboxTerminalReplayRejectsMismatchedIdentityAndPreservesRead(t *testing.T) {
	for _, kind := range []string{"run", "approval"} {
		for _, field := range []string{"project_id", "pipeline_id", "run_id", "status", "occurred_at"} {
			t.Run(kind+"/"+field, func(t *testing.T) {
				var db *gorm.DB
				var r *Repositories
				id := "run"
				owner := "actor"
				if kind == "run" {
					db = inboxTestDB(t)
					r = &Repositories{database: statusTestDB{db}}
					seedInbox(t, db, id, owner, model.PipelineStatusSuccess)
				} else {
					f, repo, approval := inboxTwoCandidateFixture(t)
					db, r, id = f.db, repo, approval
					_, err := (&ApprovalRepo{IDatabase: statusTestDB{db}}).Cancel(f.ctx, id, "synthetic")
					require.NoError(t, err)
				}
				inboxTick(t, r)
				var d model.InboxDelivery
				require.NoError(t, db.Where("source_kind = ? AND source_id = ? AND event_kind = ?", kind, id, "terminal").First(&d).Error)
				read, err := r.InboxMarkRead(context.Background(), owner, fmt.Sprint(d.ID), inboxAllow)
				require.NoError(t, err)
				require.NoError(t, db.Model(&model.InboxWork{}).Where("source_kind = ? AND source_id = ?", kind, id).Updates(map[string]any{"state": 0, "terminal_done": false, "next_check_at": time.Now()}).Error)
				replacement := any("mismatch")
				if field == "occurred_at" {
					replacement = d.OccurredAt.Add(time.Second)
				}
				require.NoError(t, db.Model(&model.InboxDelivery{}).Where("id = ?", d.ID).UpdateColumn(field, replacement).Error)
				w, err := r.claimInbox(context.Background(), kind)
				require.NoError(t, err)
				require.NotNil(t, w)
				err = r.deliverInbox(context.Background(), w, owner, "terminal", inboxAllow)
				require.ErrorIs(t, err, ErrInboxSource)
				var current model.InboxWork
				require.NoError(t, db.Where("id = ?", w.ID).First(&current).Error)
				require.False(t, current.TerminalDone)
				var row model.InboxDelivery
				require.NoError(t, db.Where("id = ?", d.ID).First(&row).Error)
				require.Equal(t, read.ReadAt, *row.ReadAt)
				require.Equal(t, d.DeliveredAt, row.DeliveredAt)
			})
		}
	}
}
func TestInboxExactTerminalReplayCompletesDespiteLaterRevocation(t *testing.T) {
	db := inboxTestDB(t)
	r := &Repositories{database: statusTestDB{db}}
	seedInbox(t, db, "run", "actor", model.PipelineStatusSuccess)
	inboxTick(t, r)
	require.NoError(t, db.Model(&model.User{}).Where("user_id = ?", "actor").Update("is_enabled", 0).Error)
	require.NoError(t, db.Model(&model.InboxWork{}).Where("source_id = ?", "run").Updates(map[string]any{"state": 0, "terminal_done": false, "next_check_at": time.Now()}).Error)
	inboxTick(t, r)
	var w model.InboxWork
	require.NoError(t, db.Where("source_id = ?", "run").First(&w).Error)
	require.True(t, w.TerminalDone)
	require.Equal(t, 1, w.State)
}
