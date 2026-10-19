// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/polling"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type pollingFixture struct {
	ctx     context.Context
	db      *gorm.DB
	streams *SCMPollingRepo
	runs    *PipelineRepo
	repos   *Repositories
	scope   polling.Scope
	denied  bool
	failure error
	calls   [][]string
}

func newPollingFixture(t *testing.T, providers ...string) *pollingFixture {
	t.Helper()
	db, e := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "polling.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal(e)
	}
	pool, e := db.DB()
	if e != nil {
		t.Fatal(e)
	}
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	ctx := context.Background()
	if e := db.WithContext(ctx).AutoMigrate(&model.Project{}, &model.Pipeline{}, &model.PipelineRun{}, &model.SCMPollingStream{}, &model.JobRun{}, &model.User{}, &model.Team{}, &model.TeamMember{}, &model.Permission{}, &model.RolePermissionBinding{}, &model.Role{}, &model.RoleGrant{}); e != nil {
		t.Fatal(e)
	}
	f := &pollingFixture{ctx: ctx, db: db, scope: polling.Scope{Version: 1, ProjectID: "project", Provider: "gitlab", APIOrigin: "https://gitlab.example.com", Repository: "https://gitlab.example.com/group/nested/repo", ProviderProjectID: "42", Stream: "merge_requests"}}
	if len(providers) > 0 && providers[0] != "gitlab" {
		f.scope.Provider = providers[0]
		f.scope.Repository = "https://github.com/owner/repo"
		switch providers[0] {
		case "gitea":
			f.scope.Repository = "https://gitea.example.com/owner/repo"
		case "gitee":
			f.scope.Repository = "https://gitee.com/owner/repo"
		case "bitbucket":
			f.scope.Repository = "https://bitbucket.org/owner/repo"
			f.scope.ProviderProjectID = "12345678-1234-1234-1234-123456789abc"
		}
		f.scope.Version, f.scope.APIOrigin, f.scope.Stream, e = polling.ProviderRepository(f.scope.Provider, f.scope.Repository)
		if e != nil {
			t.Fatal(e)
		}
	}
	f.streams = &SCMPollingRepo{IDatabase: statusTestDB{db}}
	f.runs = &PipelineRepo{IDatabase: statusTestDB{db}}
	f.repos = &Repositories{database: statusTestDB{db}}
	for _, v := range []any{&model.Project{ProjectID: "project", OrgID: "org", RepoType: f.scope.Provider, RepoURL: f.scope.Repository + ".git", DefaultBranch: "main", IsEnabled: 1, Status: model.ProjectStatusActive, TriggerMode: model.TriggerModeMR, Settings: []byte(`{"legacyCursor":"untouched"}`)}, &model.Pipeline{PipelineID: "pipeline", ProjectID: "project", RepoURL: f.scope.Repository, DefaultBranch: "main", PipelineFilePath: "pipeline.yaml", IsEnabled: 1, Metadata: "{}"}, &model.User{UserID: "actor", IsEnabled: 1}} {
		if e := db.WithContext(ctx).Create(v).Error; e != nil {
			t.Fatal(e)
		}
	}
	if e := SetPollingAuthorizer(f.repos, func(ctx context.Context, view PollingAuthorizationView, actor, project string, ids []string) error {
		if actor != "actor" || project != "project" {
			return polling.ErrForbidden
		}
		user, e := view.LiveRepositories().User.(IExecutionActorReader).GetExecutionActor(ctx, actor)
		if e != nil {
			return e
		}
		if user.IsEnabled != 1 || f.denied {
			return polling.ErrForbidden
		}
		f.calls = append(f.calls, append([]string(nil), ids...))
		return f.failure
	}); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = SetPollingAuthorizer(f.repos, nil) })
	return f
}
func (f *pollingFixture) baseline(t *testing.T) *model.SCMPollingStream {
	t.Helper()
	old, e := f.streams.GetByProject(f.ctx, "project")
	if e != nil {
		t.Fatal(e)
	}
	rev := uint64(0)
	if old != nil {
		rev = old.Revision
	}
	now := time.Now().UTC()
	s, e := f.streams.Baseline(f.ctx, BaselineInput{ProjectID: "project", ActorID: "actor", ExpectedRevision: rev, Scope: f.scope, Cursor: polling.Cursor{Version: 1, Boundary: now.Format(time.RFC3339Nano), Complete: true, Observed: map[string]string{"7": "old"}}, StartedAt: now.Add(-time.Second), CompletedAt: now})
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func (f *pollingFixture) enable(t *testing.T) *model.SCMPollingStream {
	t.Helper()
	s := f.baseline(t)
	s, e := f.streams.Enable(f.ctx, EnableInput{ProjectID: "project", ActorID: "actor", ExpectedRevision: s.Revision, BaselineID: s.BaselineID, PipelineIDs: []string{"pipeline"}})
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func (f *pollingFixture) capture(t *testing.T, s *model.SCMPollingStream, empty bool) (*model.SCMPollingStream, *model.PipelineRun) {
	t.Helper()
	return f.captureState(t, s, empty, "opened")
}
func (f *pollingFixture) captureState(t *testing.T, s *model.SCMPollingStream, empty bool, state string) (*model.SCMPollingStream, *model.PipelineRun) {
	t.Helper()
	key, e := polling.EventRequestID(f.scope, "mr:7:revision:new")
	if e != nil {
		t.Fatal(e)
	}
	target := polling.Target{MRID: 7, MRIID: 7, State: "opened", UpdatedAt: "2026-10-16T16:00:00.123456789Z", PipelineID: "pipeline", ObservationID: "mr:7:revision:new", RequestID: key, SourceBranch: "feature", TargetBranch: "main", SourceSHA: strings.Repeat("a", 40), DefinitionCommitSHA: strings.Repeat("b", 40), DefinitionPath: "pipeline.yaml", ResolvedDefinitionDigest: "v1:" + strings.Repeat("c", 64)}
	if f.scope.Version == polling.ScopeVersion2 {
		target.SourceProjectID = 99
		target.SourceRepository = "fork-owner/repo"
		target.TargetSHA = strings.Repeat("d", 40)
	}
	if f.scope.Version == polling.ScopeVersion3 {
		setV3PollingTarget(&target, f.scope, state)
	}

	targets := []polling.Target{target}
	if empty {
		targets = nil
	}
	next := polling.Cursor{Version: 1, Boundary: time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano), Complete: true, Observed: map[string]string{"7": target.ObservationID}}
	s, e = f.streams.CaptureObservations(f.ctx, CaptureObservationsInput{ProjectID: "project", ExpectedRevision: s.Revision, ActivationGeneration: s.ActivationGeneration, Batch: polling.ObservedBatch{Version: 1, ID: "batch", ScopeDigest: s.ScopeDigest, Generation: s.ActivationGeneration, SelectionDigest: s.SelectionDigest, NextCursor: next, Observations: []polling.Observation{target.Observation()}}})
	if e != nil {
		t.Fatal(e)
	}
	s, e = f.streams.CaptureBatch(f.ctx, CaptureBatchInput{ProjectID: "project", ExpectedRevision: s.Revision, ActivationGeneration: s.ActivationGeneration, Batch: polling.Batch{Version: 1, ID: "batch", ScopeDigest: s.ScopeDigest, Generation: s.ActivationGeneration, SelectionDigest: s.SelectionDigest, NextCursor: next, Targets: targets, ObservationCount: 1}})
	if e != nil {
		t.Fatal(e)
	}
	c := polling.RunContext{BaselineID: s.BaselineID, BaselineCutoff: s.BaselineCutoff, Version: 1, StreamID: s.StreamID, Scope: f.scope, ScopeDigest: s.ScopeDigest, ActivationGeneration: s.ActivationGeneration, ActorID: s.EnabledBy, SelectionDigest: s.SelectionDigest, BatchID: "batch", BatchRevision: s.Revision, RunID: "run", Target: target}
	body, e := c.Canonical()
	if e != nil {
		t.Fatal(e)
	}
	digest, _ := c.Digest()
	run := &model.PipelineRun{RunID: "run", PipelineID: "pipeline", RequestID: key, Branch: target.SourceBranch, CommitSha: target.SourceSHA, DefinitionCommitSha: target.DefinitionCommitSHA, DefinitionPath: target.DefinitionPath, TriggerType: int(pipelinev1.TriggerType_TRIGGER_TYPE_EVENT), TriggeredBy: polling.Actor, Status: model.PipelineStatusPending, Env: "{}", PollingContext: body, PollingContextDigest: digest}
	return s, run
}
func (f *pollingFixture) create(t *testing.T, run *model.PipelineRun) {
	t.Helper()
	result, e := f.runs.CreatePollingRun(f.ctx, run)
	if e != nil || !result.CreatedNow || result.Outcome != polling.OutcomeCreated {
		t.Fatalf("create %+v %v", result, e)
	}
}
func (f *pollingFixture) latest(t *testing.T) *model.SCMPollingStream {
	t.Helper()
	s, e := f.streams.GetByProject(f.ctx, "project")
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestPollingBaselineDefaultDisabledAndSeparateEnable(t *testing.T) {
	for _, provider := range []string{"gitlab", "github", "gitea", "gitee", "bitbucket"} {
		t.Run(provider, func(t *testing.T) {
			f := newPollingFixture(t, provider)
			s, e := f.streams.GetByProject(f.ctx, "project")
			if e != nil || s != nil {
				t.Fatalf("implicit stream %+v %v", s, e)
			}
			s = f.baseline(t)
			if s.State != polling.StateBaselined || s.EnabledBy != "" || s.SelectionJSON != "" {
				t.Fatalf("baseline enables %+v", s)
			}
			var count int64
			if e := f.db.WithContext(f.ctx).Model(&model.PipelineRun{}).Count(&count).Error; e != nil || count != 0 {
				t.Fatal("baseline created runs")
			}
			var p model.Project
			if e := f.db.WithContext(f.ctx).Select("settings").Where("project_id = ?", "project").First(&p).Error; e != nil || string(p.Settings) != `{"legacyCursor":"untouched"}` {
				t.Fatal("settings changed")
			}
			for _, ids := range [][]string{nil, {"pipeline", "pipeline"}, {"missing"}} {
				if _, e := f.streams.Enable(f.ctx, EnableInput{ProjectID: "project", ActorID: "actor", ExpectedRevision: s.Revision, BaselineID: s.BaselineID, PipelineIDs: ids}); e == nil {
					t.Fatalf("bad selection accepted %v", ids)
				}
			}
			if got := f.latest(t); got.Revision != s.Revision || got.State != polling.StateBaselined {
				t.Fatal("failed enable mutated baseline")
			}

		})
	}
}

func TestPollingBatchClaimReceiptAndCursorCAS(t *testing.T) {
	for _, provider := range []string{"gitlab", "github", "gitea", "gitee", "bitbucket"} {
		t.Run(provider, func(t *testing.T) {
			f := newPollingFixture(t, provider)
			s, run := f.capture(t, f.enable(t), false)
			before := s.CursorJSON
			f.create(t, run)
			if _, e := f.streams.AcknowledgeBatch(f.ctx, AcknowledgeBatchInput{ProjectID: "project", ExpectedRevision: s.Revision, ActivationGeneration: s.ActivationGeneration, BatchID: "batch"}); !errors.Is(e, polling.ErrUnresolved) {
				t.Fatalf("pending advanced %v", e)
			}
			result, e := f.runs.StartPollingRunIfPending(f.ctx, run)
			if e != nil || !result.ClaimedNow || result.Outcome != polling.OutcomeAdmitted {
				t.Fatalf("claim %+v %v", result, e)
			}
			if _, e := f.streams.AcknowledgeBatch(f.ctx, AcknowledgeBatchInput{ProjectID: "project", ExpectedRevision: s.Revision, ActivationGeneration: s.ActivationGeneration, BatchID: "batch"}); !errors.Is(e, polling.ErrConflict) {
				t.Fatalf("stale cursor CAS %v", e)
			}
			s = f.latest(t)
			if s.CursorJSON != before || s.PendingBatchJSON == "" {
				t.Fatal("failed cursor CAS lost batch")
			}
			dup, e := f.runs.CreatePollingRun(f.ctx, run)
			if e != nil || dup.CreatedNow || dup.Outcome != polling.OutcomeAdmitted {
				t.Fatalf("duplicate %+v %v", dup, e)
			}
			again, e := f.runs.StartPollingRunIfPending(f.ctx, run)
			if e != nil || again.ClaimedNow || again.Outcome != polling.OutcomeAdmitted {
				t.Fatalf("reclaim %+v %v", again, e)
			}
			s = f.latest(t)
			s, e = f.streams.AcknowledgeBatch(f.ctx, AcknowledgeBatchInput{ProjectID: "project", ExpectedRevision: s.Revision, ActivationGeneration: s.ActivationGeneration, BatchID: "batch"})
			if e != nil || s.PendingBatchJSON != "" || s.CursorJSON == before {
				t.Fatalf("ack %+v %v", s, e)
			}

		})
	}
}

func TestPollingPendingDuplicateNeverResubmits(t *testing.T) {
	for _, provider := range []string{"gitlab", "github", "gitea", "gitee", "bitbucket"} {
		t.Run(provider, func(t *testing.T) {
			f := newPollingFixture(t, provider)
			_, run := f.capture(t, f.enable(t), false)
			f.create(t, run)
			dup, e := f.runs.CreatePollingRun(f.ctx, run)
			if e != nil || dup.CreatedNow || dup.Outcome != polling.OutcomeUnresolved {
				t.Fatalf("pending replay %+v %v", dup, e)
			}

		})
	}
}

func TestPollingRevocationPausesAndRetainsPending(t *testing.T) {
	for _, provider := range []string{"gitlab", "github", "gitea", "gitee", "bitbucket"} {
		t.Run(provider, func(t *testing.T) {
			for _, stage := range []string{"create", "admit"} {
				t.Run(stage, func(t *testing.T) {
					f := newPollingFixture(t, provider)
					s, run := f.capture(t, f.enable(t), false)
					if stage == "admit" {
						f.create(t, run)
					}
					f.denied = true
					if stage == "create" {
						o, e := f.runs.CreatePollingRun(f.ctx, run)
						if !errors.Is(e, polling.ErrForbidden) || o.CreatedNow {
							t.Fatalf("revoked create %+v %v", o, e)
						}
					} else {
						o, e := f.runs.StartPollingRunIfPending(f.ctx, run)
						if !errors.Is(e, polling.ErrForbidden) || o.ClaimedNow {
							t.Fatalf("revoked admission %+v %v", o, e)
						}
					}
					paused := f.latest(t)
					if paused.State != polling.StatePaused || paused.PauseReason != "authorization_denied" || paused.PendingBatchJSON == "" || paused.CursorJSON != s.CursorJSON || paused.ActivationGeneration <= s.ActivationGeneration {
						t.Fatalf("lost pause evidence %+v", paused)
					}
					if stage == "admit" {
						stored, e := f.runs.GetRun(f.ctx, run.RunID)
						if e != nil || stored.Status != model.PipelineStatusFailed || stored.StartTime != nil || stored.EndTime == nil {
							t.Fatal("definitive denial left orphan pending")
						}
						batch, e := polling.ParseBatch(paused.PendingBatchJSON)
						if e != nil || len(batch.Results) != 1 || batch.Results[0].Outcome != polling.OutcomeRejected {
							t.Fatal("denial receipt missing")
						}
					}
					f.denied = false
					if o, e := f.runs.StartPollingRunIfPending(f.ctx, run); e == nil || o.ClaimedNow {
						t.Fatal("grant restoration revived paused activation")
					}
				})
			}

		})
	}
}

func TestPollingDisableAndUnknownStoreErrorsCannotClaim(t *testing.T) {
	for _, provider := range []string{"gitlab", "github", "gitea", "gitee", "bitbucket"} {
		t.Run(provider, func(t *testing.T) {
			f := newPollingFixture(t, provider)
			s, run := f.capture(t, f.enable(t), false)
			f.create(t, run)
			f.failure = fmt.Errorf("authorization store unavailable")
			o, e := f.runs.StartPollingRunIfPending(f.ctx, run)
			if e == nil || o.ClaimedNow {
				t.Fatal("store error claimed")
			}
			f.failure = nil
			s, e = f.streams.Disable(f.ctx, DisableInput{ProjectID: "project", ActorID: "actor", ExpectedRevision: s.Revision})
			if e != nil {
				t.Fatal(e)
			}
			o, e = f.runs.StartPollingRunIfPending(f.ctx, run)
			if e == nil || o.ClaimedNow {
				t.Fatal("disabled generation claimed")
			}
			stored, e := f.runs.GetRun(f.ctx, run.RunID)
			if e != nil || stored.Status != model.PipelineStatusPending || stored.StartTime != nil {
				t.Fatal("fail closed mutated pending run")
			}
			if s.PendingBatchJSON == "" {
				t.Fatal("disable discarded batch")
			}

		})
	}
}

func TestPollingRejectIsTypedAndEmptyBatchAdvances(t *testing.T) {
	for _, provider := range []string{"gitlab", "github", "gitea", "gitee", "bitbucket"} {
		t.Run(provider, func(t *testing.T) {
			t.Run("typed rejection", func(t *testing.T) {
				f := newPollingFixture(t, provider)
				_, run := f.capture(t, f.enable(t), false)
				f.create(t, run)
				o, e := f.runs.RejectPendingPollingRun(f.ctx, run)
				if e != nil || o.Outcome != polling.OutcomeRejected || o.ClaimedNow {
					t.Fatalf("reject %+v %v", o, e)
				}
				s := f.latest(t)
				if _, e := f.streams.AcknowledgeBatch(f.ctx, AcknowledgeBatchInput{ProjectID: "project", ExpectedRevision: s.Revision, ActivationGeneration: s.ActivationGeneration, BatchID: "batch"}); e != nil {
					t.Fatal(e)
				}
			})
			t.Run("empty", func(t *testing.T) {
				f := newPollingFixture(t, provider)
				s, _ := f.capture(t, f.enable(t), true)
				if _, e := f.streams.AcknowledgeBatch(f.ctx, AcknowledgeBatchInput{ProjectID: "project", ExpectedRevision: s.Revision, ActivationGeneration: s.ActivationGeneration, BatchID: "batch"}); e != nil {
					t.Fatal(e)
				}
			})
			t.Run("untyped failure", func(t *testing.T) {
				f := newPollingFixture(t, provider)
				s, run := f.capture(t, f.enable(t), false)
				f.create(t, run)
				if e := f.db.WithContext(f.ctx).Model(&model.PipelineRun{}).Where("run_id = ?", run.RunID).Updates(map[string]any{"status": model.PipelineStatusFailed, "end_time": time.Now()}).Error; e != nil {
					t.Fatal(e)
				}
				if _, e := f.streams.AcknowledgeBatch(f.ctx, AcknowledgeBatchInput{ProjectID: "project", ExpectedRevision: s.Revision, ActivationGeneration: s.ActivationGeneration, BatchID: "batch"}); !errors.Is(e, polling.ErrUnresolved) {
					t.Fatalf("untyped failure acknowledged %v", e)
				}
			})

		})
	}
}

func TestPollingGenericCreationAndIdentityChangesRejected(t *testing.T) {
	for _, provider := range []string{"gitlab", "github", "gitea", "gitee", "bitbucket"} {
		t.Run(provider, func(t *testing.T) {
			f := newPollingFixture(t, provider)
			_, run := f.capture(t, f.enable(t), false)
			if e := f.runs.CreateRun(f.ctx, run); e == nil {
				t.Fatal("generic create forged polling context")
			}
			if e := f.runs.CreateRetryApprovalRun(f.ctx, run); e == nil {
				t.Fatal("retry create forged polling context")
			}
			for _, k := range []string{"polling_context", "PollingContext", "polling_context_digest", "PollingContextDigest"} {
				if e := f.runs.UpdateRun(f.ctx, "run", map[string]any{k: "forged"}); e == nil {
					t.Fatalf("mutable context %s", k)
				}
			}
			for _, mutate := range []func(*model.PipelineRun){func(r *model.PipelineRun) { r.TriggeredBy = "webhook" }, func(r *model.PipelineRun) { r.TriggerType = 1 }, func(r *model.PipelineRun) { r.RequestID += "x" }, func(r *model.PipelineRun) { r.CommitSha = strings.Repeat("f", 40) }, func(r *model.PipelineRun) { r.PollingContextDigest = "" }, func(r *model.PipelineRun) { r.AllowDeployments = true }} {
				copy := *run
				mutate(&copy)
				if o, e := f.runs.CreatePollingRun(f.ctx, &copy); e == nil || o.CreatedNow {
					t.Fatal("changed identity created")
				}
				if IsAutomaticPipelineRun(&copy) {
					t.Fatal("polling context accepted as legacy automatic")
				}
			}
			f.create(t, run)
			for _, mutate := range []func(*model.PipelineRun){func(r *model.PipelineRun) { r.Env = `{"INJECT":"x"}` }, func(r *model.PipelineRun) { r.RunID = "RUN" }} {
				copy := *run
				mutate(&copy)
				if o, e := f.runs.StartPollingRunIfPending(f.ctx, &copy); e == nil && o.ClaimedNow {
					t.Fatal("changed admission identity claimed")
				}
			}

		})
	}
}

func TestPollingUnresolvedPendingPreventsRebaselineDiscard(t *testing.T) {
	f := newPollingFixture(t)
	s, run := f.capture(t, f.enable(t), false)
	f.create(t, run)
	s, e := f.streams.Disable(f.ctx, DisableInput{ProjectID: "project", ActorID: "actor", ExpectedRevision: s.Revision})
	if e != nil {
		t.Fatal(e)
	}
	cursor, e := polling.ParseCursor(s.CursorJSON)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	if _, e := f.streams.Baseline(f.ctx, BaselineInput{ProjectID: "project", ActorID: "actor", ExpectedRevision: s.Revision, Scope: f.scope, Cursor: *cursor, StartedAt: now, CompletedAt: now, DiscardPending: true}); !errors.Is(e, polling.ErrUnresolved) {
		t.Fatalf("rebaseline hid pending %v", e)
	}
}

type pollingUnknownCommitPool struct{ *sql.DB }

func (p *pollingUnknownCommitPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	tx, e := p.DB.BeginTx(ctx, options)
	if e != nil {
		return nil, e
	}
	return &pollingUnknownCommitTx{Tx: tx, db: p.DB}, nil
}
func (p *pollingUnknownCommitPool) GetDBConn() (*sql.DB, error) { return p.DB, nil }

type pollingUnknownCommitTx struct {
	*sql.Tx
	db *sql.DB
}

func (t *pollingUnknownCommitTx) Commit() error {
	if e := t.Tx.Commit(); e != nil {
		return e
	}
	return fmt.Errorf("injected lost commit acknowledgement")
}
func (t *pollingUnknownCommitTx) GetDBConn() (*sql.DB, error) { return t.db, nil }
func unknownCommitPipelineRepo(t *testing.T, f *pollingFixture) *PipelineRepo {
	t.Helper()
	pool, e := f.db.DB()
	if e != nil {
		t.Fatal(e)
	}
	db, e := gorm.Open(sqlite.New(sqlite.Config{Conn: &pollingUnknownCommitPool{DB: pool}}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal(e)
	}
	return &PipelineRepo{IDatabase: statusTestDB{db}}
}
func TestPollingUnknownCommitNeverAuthorizesScheduling(t *testing.T) {
	for _, provider := range []string{"gitlab", "github", "gitea", "gitee", "bitbucket"} {
		t.Run(provider, func(t *testing.T) {
			for _, stage := range []string{"create", "claim"} {
				t.Run(stage, func(t *testing.T) {
					f := newPollingFixture(t, provider)
					_, run := f.capture(t, f.enable(t), false)
					uncertain := unknownCommitPipelineRepo(t, f)
					if stage == "create" {
						o, e := uncertain.CreatePollingRun(f.ctx, run)
						if e == nil || o.CreatedNow || o.Outcome != polling.OutcomeUnresolved {
							t.Fatalf("uncertain create %+v %v", o, e)
						}
						stored, e := f.runs.GetRun(f.ctx, run.RunID)
						if e != nil || stored.Status != model.PipelineStatusPending {
							t.Fatal("fault did not commit pending row")
						}
						dup, e := f.runs.CreatePollingRun(f.ctx, run)
						if e != nil || dup.CreatedNow || dup.Outcome != polling.OutcomeUnresolved {
							t.Fatal("lost create replayed pending")
						}
					} else {
						f.create(t, run)
						o, e := uncertain.StartPollingRunIfPending(f.ctx, run)
						if e == nil || o.ClaimedNow || o.Outcome != polling.OutcomeUnresolved {
							t.Fatalf("uncertain claim %+v %v", o, e)
						}
						stored, e := f.runs.GetRun(f.ctx, run.RunID)
						if e != nil || stored.Status != model.PipelineStatusRunning {
							t.Fatal("fault did not commit claim")
						}
						again, e := f.runs.StartPollingRunIfPending(f.ctx, run)
						if e != nil || again.ClaimedNow || again.Outcome != polling.OutcomeAdmitted {
							t.Fatal("unknown claim replayed scheduling")
						}
					}
				})
			}

		})
	}
}

func TestPollingBackoffPreservesProgressAndSummaryOnlyAtAck(t *testing.T) {
	f := newPollingFixture(t)
	s := f.enable(t)
	before := s.CursorJSON
	notBefore := time.Now().Add(5 * time.Minute)
	s, e := f.streams.ScheduleRetry(f.ctx, "project", s.Revision, s.ActivationGeneration, notBefore, "provider_rate_limited")
	if e != nil || s.NextPollAt == nil || s.LastPollError != "provider_rate_limited" || s.CursorJSON != before || s.State != polling.StateEnabled {
		t.Fatalf("backoff %+v %v", s, e)
	}
	due, e := f.streams.ListEnabled(f.ctx)
	if e != nil || len(due) != 0 {
		t.Fatal("future backoff was eligible")
	}
	for _, reason := range []string{"unsafe provider body token=secret", "authorization_denied"} {
		if _, e := f.streams.ScheduleRetry(f.ctx, "project", s.Revision, s.ActivationGeneration, notBefore, reason); e == nil {
			t.Fatal("unsanitized retry error accepted")
		}
	}
	if _, e := f.streams.ScheduleRetry(f.ctx, "project", s.Revision, s.ActivationGeneration, time.Now().Add(48*time.Hour), "provider_unavailable"); e == nil {
		t.Fatal("unbounded retry accepted")
	}
	s, run := f.capture(t, s, false)
	if s.NextPollAt != nil || s.LastPollError != "" || s.LastCompletedAt != nil {
		t.Fatal("capture retained retry or asserted completion")
	}
	f.create(t, run)
	if _, e := f.runs.StartPollingRunIfPending(f.ctx, run); e != nil {
		t.Fatal(e)
	}
	s = f.latest(t)
	s, e = f.streams.AcknowledgeBatch(f.ctx, AcknowledgeBatchInput{ProjectID: "project", ExpectedRevision: s.Revision, ActivationGeneration: s.ActivationGeneration, BatchID: "batch"})
	if e != nil || s.LastCompletedAt == nil || s.LastTargetCount != 1 {
		t.Fatal("ack summary missing")
	}
}
func TestPollingAdmissionExpiryAndExecutionEvidenceFailClosed(t *testing.T) {
	for _, guard := range []string{"expired", "job", "cancelled", "started", "context"} {
		t.Run(guard, func(t *testing.T) {
			f := newPollingFixture(t)
			_, run := f.capture(t, f.enable(t), false)
			f.create(t, run)
			switch guard {
			case "expired":
				if e := SetPollingAuthorizer(f.repos, func(ctx context.Context, view PollingAuthorizationView, actor, project string, ids []string) error {
					view.(*retryApprovalView).expires = time.Now().Add(-time.Second)
					return nil
				}); e != nil {
					t.Fatal(e)
				}
			case "job":
				if e := f.db.WithContext(f.ctx).Create(&model.JobRun{JobRunID: "job", PipelineID: run.PipelineID, PipelineRunID: run.RunID}).Error; e != nil {
					t.Fatal(e)
				}
			case "cancelled":
				if e := f.db.WithContext(f.ctx).Model(&model.PipelineRun{}).Where("run_id = ?", run.RunID).Update("status", model.PipelineStatusCancelled).Error; e != nil {
					t.Fatal(e)
				}
			case "started":
				if e := f.db.WithContext(f.ctx).Model(&model.PipelineRun{}).Where("run_id = ?", run.RunID).Update("start_time", time.Now()).Error; e != nil {
					t.Fatal(e)
				}
			case "context":
				if e := f.db.WithContext(f.ctx).Model(&model.PipelineRun{}).Where("run_id = ?", run.RunID).Update("polling_context_digest", "forged").Error; e != nil {
					t.Fatal(e)
				}
			}
			o, _ := f.runs.StartPollingRunIfPending(f.ctx, run)
			if o.ClaimedNow {
				t.Fatalf("guard %s claimed", guard)
			}
			s := f.latest(t)
			batch, e := polling.ParseBatch(s.PendingBatchJSON)
			if e != nil || len(batch.Results) != 0 {
				t.Fatal("failed admission wrote receipt")
			}
		})
	}
}

func TestPollingDefinitionFailureRetainsPreDispatchObservations(t *testing.T) {
	f := newPollingFixture(t)
	s := f.enable(t)
	before := s.CursorJSON
	o := polling.Observation{MRID: 7, MRIID: 7, State: "opened", UpdatedAt: "2026-10-16T16:00:00.123456789Z", SourceBranch: "feature", TargetBranch: "main", SourceSHA: strings.Repeat("a", 40), ObservationID: "mr:7:new"}
	next := polling.Cursor{Version: 1, Boundary: time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano), Complete: true, Observed: map[string]string{"7": o.ObservationID}}
	s, e := f.streams.CaptureObservations(f.ctx, CaptureObservationsInput{ProjectID: "project", ExpectedRevision: s.Revision, ActivationGeneration: s.ActivationGeneration, Batch: polling.ObservedBatch{Version: 1, ID: "observed", ScopeDigest: s.ScopeDigest, Generation: s.ActivationGeneration, SelectionDigest: s.SelectionDigest, NextCursor: next, Observations: []polling.Observation{o}}})
	if e != nil {
		t.Fatal(e)
	}
	captured := s.PendingObservationJSON
	s, e = f.streams.Pause(f.ctx, PauseInput{ProjectID: "project", ExpectedRevision: s.Revision, ActivationGeneration: s.ActivationGeneration, Reason: "definition_unavailable"})
	if e != nil || captured == "" || s.PendingObservationJSON != captured || s.PendingBatchJSON != "" || s.CursorJSON != before {
		t.Fatal("definition failure lost normalized snapshot")
	}
	cursor, e := polling.ParseCursor(s.CursorJSON)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	in := BaselineInput{ProjectID: "project", ActorID: "actor", ExpectedRevision: s.Revision, Scope: f.scope, Cursor: *cursor, StartedAt: now, CompletedAt: now}
	if _, e := f.streams.Baseline(f.ctx, in); !errors.Is(e, polling.ErrUnresolved) {
		t.Fatalf("silent observation discard %v", e)
	}
	in.DiscardPending = true
	s, e = f.streams.Baseline(f.ctx, in)
	if e != nil || s.PendingObservationJSON != "" {
		t.Fatal("explicit reviewed discard failed", e)
	}
}
func TestPollingUnresolvedMarkerDoesNotRevokeConcurrentCreator(t *testing.T) {
	f := newPollingFixture(t)
	s, run := f.capture(t, f.enable(t), false)
	f.create(t, run)
	marked, e := f.streams.MarkUnresolved(f.ctx, "project", s.Revision, s.ActivationGeneration)
	if e != nil || marked.State != polling.StateEnabled || marked.ActivationGeneration != s.ActivationGeneration || marked.PendingBatchJSON != s.PendingBatchJSON || marked.CursorJSON != s.CursorJSON || marked.LastPollError != "recovery_required" {
		t.Fatalf("marker changed authority %+v %v", marked, e)
	}
	repeated, e := f.streams.MarkUnresolved(f.ctx, "project", marked.Revision, marked.ActivationGeneration)
	if e != nil || repeated.Revision != marked.Revision || repeated.LastPollError != marked.LastPollError || repeated.State != marked.State {
		t.Fatalf("same-value marker was not idempotent %+v %v", repeated, e)
	}
	out, e := f.runs.StartPollingRunIfPending(f.ctx, run)
	if e != nil || !out.ClaimedNow {
		t.Fatalf("marker killed legitimate creator %+v %v", out, e)
	}
	latest := f.latest(t)
	if latest.LastPollError != "" {
		t.Fatal("successful admission retained unresolved marker")
	}
	if _, e := f.streams.MarkUnresolved(f.ctx, "project", marked.Revision, marked.ActivationGeneration); !errors.Is(e, polling.ErrConflict) {
		t.Fatal("stale marker overwrote newer admission")
	}
}
func TestPollingAttemptsRotateWithoutChangingAuthority(t *testing.T) {
	f := newPollingFixture(t)
	s := f.enable(t)
	other := &model.SCMPollingStream{StreamID: "zz-other", ProjectID: "other", ScopeDigest: "other-scope", State: polling.StateEnabled, Revision: 1, ActivationGeneration: 1}
	if e := f.db.WithContext(f.ctx).Create(other).Error; e != nil {
		t.Fatal(e)
	}
	updated, e := f.streams.RecordAttempt(f.ctx, "project", s.Revision, s.ActivationGeneration)
	if e != nil || updated.LastAttemptAt == nil || updated.ActivationGeneration != s.ActivationGeneration || updated.CursorJSON != s.CursorJSON || updated.State != s.State {
		t.Fatal("attempt marker changed authority", e)
	}
	rows, e := f.streams.ListEnabled(f.ctx)
	if e != nil || len(rows) != 2 || rows[0].StreamID != other.StreamID {
		t.Fatal("never-attempted stream starved", e)
	}
}

func TestPollingEnabledCycleIsBoundedAndLaterStreamsRotateIn(t *testing.T) {
	f := newPollingFixture(t)
	const laterCount = 3
	for i := 0; i < pollingCycleStreamLimit+laterCount; i++ {
		id := fmt.Sprintf("bounded-stream-%03d", i)
		for _, row := range []any{
			&model.Project{ProjectID: id},
			&model.SCMPollingStream{StreamID: id, ProjectID: id, ScopeDigest: id, State: polling.StateEnabled, Revision: 1, ActivationGeneration: 1, EnabledBy: "actor"},
		} {
			if e := f.db.WithContext(f.ctx).Create(row).Error; e != nil {
				t.Fatal(e)
			}
		}
	}
	first, e := f.streams.ListEnabled(f.ctx)
	if e != nil || len(first) != pollingCycleStreamLimit {
		t.Fatalf("unbounded or incomplete first cycle: rows=%d error=%v", len(first), e)
	}
	for i, row := range first {
		if row.StreamID != fmt.Sprintf("bounded-stream-%03d", i) {
			t.Fatal("equal-attempt rows lost stable stream ordering")
		}
		attempted, e := f.streams.RecordAttempt(f.ctx, row.ProjectID, row.Revision, row.ActivationGeneration)
		if e != nil || attempted.Revision != row.Revision || attempted.ActivationGeneration != row.ActivationGeneration {
			t.Fatalf("fairness metadata changed authority: %+v %v", attempted, e)
		}
	}
	next, e := f.streams.ListEnabled(f.ctx)
	if e != nil || len(next) != pollingCycleStreamLimit {
		t.Fatalf("unbounded or incomplete next cycle: rows=%d error=%v", len(next), e)
	}
	for i := 0; i < laterCount; i++ {
		if next[i].StreamID != fmt.Sprintf("bounded-stream-%03d", pollingCycleStreamLimit+i) {
			t.Fatalf("stream beyond the initial cap starved: position=%d stream=%s", i, next[i].StreamID)
		}
	}
}

func TestPollingUntypedTerminalRunCannotBeHiddenByRebaseline(t *testing.T) {
	for _, state := range []int{model.PipelineStatusUnknown, model.PipelineStatusFailed, model.PipelineStatusCancelled} {
		t.Run(fmt.Sprint(state), func(t *testing.T) {
			f := newPollingFixture(t)
			s, run := f.capture(t, f.enable(t), false)
			f.create(t, run)
			if e := f.db.WithContext(f.ctx).Model(&model.PipelineRun{}).Where("run_id = ?", run.RunID).Updates(map[string]any{"status": state, "end_time": time.Now()}).Error; e != nil {
				t.Fatal(e)
			}
			s, e := f.streams.Disable(f.ctx, DisableInput{ProjectID: "project", ActorID: "actor", ExpectedRevision: s.Revision})
			if e != nil {
				t.Fatal(e)
			}
			cursor, e := polling.ParseCursor(s.CursorJSON)
			if e != nil {
				t.Fatal(e)
			}
			now := time.Now()
			if _, e := f.streams.Baseline(f.ctx, BaselineInput{ProjectID: "project", ActorID: "actor", ExpectedRevision: s.Revision, Scope: f.scope, Cursor: *cursor, StartedAt: now, CompletedAt: now, DiscardPending: true}); !errors.Is(e, polling.ErrUnresolved) {
				t.Fatalf("untyped state %d silently discarded: %v", state, e)
			}
		})
	}
}

func TestPollingConcurrentAttemptMetadataDoesNotInvalidateCapture(t *testing.T) {
	f := newPollingFixture(t)
	s := f.enable(t)
	for i := 0; i < 3; i++ {
		updated, e := f.streams.RecordAttempt(f.ctx, "project", s.Revision, s.ActivationGeneration)
		if e != nil || updated.Revision != s.Revision {
			t.Fatalf("attempt invalidated semantic revision %+v %v", updated, e)
		}
	}
	captured, run := f.capture(t, s, false)
	f.create(t, run)
	marked, e := f.streams.MarkUnresolved(f.ctx, "project", captured.Revision, captured.ActivationGeneration)
	if e != nil || marked.Revision != captured.Revision {
		t.Fatal("unresolved metadata invalidated batch revision")
	}
	o, e := f.runs.StartPollingRunIfPending(f.ctx, run)
	if e != nil || !o.ClaimedNow {
		t.Fatal("concurrent metadata prevented valid capture/claim", e)
	}
}

func TestPollingNewBaselineClearsOldScopeBackoffAndSummaries(t *testing.T) {
	f := newPollingFixture(t)
	s := f.enable(t)
	s, e := f.streams.ScheduleRetry(f.ctx, "project", s.Revision, s.ActivationGeneration, time.Now().Add(6*time.Hour), "provider_rate_limited")
	if e != nil {
		t.Fatal(e)
	}
	if e := f.db.WithContext(f.ctx).Model(&model.SCMPollingStream{}).Where("stream_id = ?", s.StreamID).Updates(map[string]any{"last_completed_at": time.Now(), "last_observation_count": 5, "last_target_count": 2, "last_attempt_at": time.Now()}).Error; e != nil {
		t.Fatal(e)
	}
	if _, e := f.streams.Disable(f.ctx, DisableInput{ProjectID: "project", ActorID: "actor", ExpectedRevision: s.Revision}); e != nil {
		t.Fatal(e)
	}
	s = f.baseline(t)
	if s.NextPollAt != nil || s.LastPollError != "" || s.LastCompletedAt != nil || s.LastAttemptAt != nil || s.LastObservationCount != 0 || s.LastTargetCount != 0 {
		t.Fatal("new baseline retained prior scope backoff/summary")
	}
	s, e = f.streams.Enable(f.ctx, EnableInput{ProjectID: "project", ActorID: "actor", ExpectedRevision: s.Revision, BaselineID: s.BaselineID, PipelineIDs: []string{"pipeline"}})
	if e != nil {
		t.Fatal(e)
	}
	due, e := f.streams.ListEnabled(f.ctx)
	if e != nil || len(due) != 1 || due[0].StreamID != s.StreamID {
		t.Fatal("new explicit activation inherited stale provider delay", e)
	}
}

// These are sequential durable semantics tests, not SQLite concurrency evidence.
func TestPollingNewProvidersCurrentProjectScopeGuard(t *testing.T) {
	for _, provider := range []string{"github", "gitea"} {
		t.Run(provider, func(t *testing.T) {
			for _, field := range []string{"repo_type", "repo_url", "status"} {
				t.Run(field, func(t *testing.T) {
					f := newPollingFixture(t, provider)
					s, run := f.capture(t, f.enable(t), false)
					var value any = "gitee"
					if field == "repo_url" {
						value = f.scope.Repository + "-different"
					}
					if field == "status" {
						value = model.ProjectStatusArchived
					}
					// Deliberately bypass the configuration mutator's proactive pause to prove
					// the locked current-project check itself prevents stale authorization.
					if err := f.db.Model(&model.Project{}).Where("project_id = ?", "project").Update(field, value).Error; err != nil {
						t.Fatal(err)
					}
					outcome, err := f.runs.CreatePollingRun(f.ctx, run)
					if err == nil || outcome.CreatedNow {
						t.Fatal("changed current project accepted stale scope")
					}
					latest := f.latest(t)
					if latest.CursorJSON != s.CursorJSON || latest.PendingBatchJSON == "" {
						t.Fatal("scope invalidation lost pending evidence")
					}
				})
			}
			t.Run("stable_repository_id", func(t *testing.T) {
				f := newPollingFixture(t, provider)
				_, run := f.capture(t, f.enable(t), false)
				evidence, err := polling.ParseRunContext(run.PollingContext)
				if err != nil {
					t.Fatal(err)
				}
				evidence.Scope.ProviderProjectID = "43"
				evidence.ScopeDigest, err = evidence.Scope.Digest()
				if err != nil {
					t.Fatal(err)
				}
				evidence.Target.RequestID, err = polling.EventRequestID(evidence.Scope, evidence.Target.ObservationID)
				if err != nil {
					t.Fatal(err)
				}
				run.RequestID = evidence.Target.RequestID
				run.PollingContext, err = evidence.Canonical()
				if err != nil {
					t.Fatal(err)
				}
				run.PollingContextDigest, err = evidence.Digest()
				if err != nil {
					t.Fatal(err)
				}
				outcome, err := f.runs.CreatePollingRun(f.ctx, run)
				if err == nil || outcome.CreatedNow {
					t.Fatal("changed stable repository identity admitted")
				}
			})
		})
	}
}

func TestPollingSourceProvenanceImmutableAcrossDurableAdmission(t *testing.T) {
	for _, provider := range []string{"github", "gitea"} {
		t.Run(provider, func(t *testing.T) {
			f := newPollingFixture(t, provider)
			stream, run := f.capture(t, f.enable(t), false)
			batch, err := polling.ParseBatch(stream.PendingBatchJSON)
			if err != nil {
				t.Fatal(err)
			}
			original, err := polling.ParseRunContext(run.PollingContext)
			if err != nil {
				t.Fatal(err)
			}
			if batch.Targets[0] != original.Target || original.Target.SourceProjectID != 99 || original.Target.SourceRepository != "fork-owner/repo" || original.Target.TargetSHA != strings.Repeat("d", 40) {
				t.Fatal("durable batch lost fork/base provenance")
			}
			for _, mutate := range []func(*polling.Target){func(t *polling.Target) { t.SourceProjectID = 100 }, func(t *polling.Target) { t.SourceRepository = "different/repo" }, func(t *polling.Target) { t.TargetSHA = strings.Repeat("e", 40) }, func(t *polling.Target) { t.SourceProjectID = 0; t.SourceRepository = ""; t.TargetSHA = "" }} {
				changed := *original
				mutate(&changed.Target)
				copy := *run
				copy.PollingContext, err = changed.Canonical()
				if err != nil {
					t.Fatal(err)
				}
				copy.PollingContextDigest, err = changed.Digest()
				if err != nil {
					t.Fatal(err)
				}
				result, err := f.runs.CreatePollingRun(f.ctx, &copy)
				if err == nil || result.CreatedNow {
					t.Fatal("mutated provenance created run")
				}
			}
			f.create(t, run)
			stored, err := f.runs.GetRun(f.ctx, run.RunID)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := polling.ParseRunContext(stored.PollingContext)
			if err != nil || restored.Target != original.Target {
				t.Fatal("stored run lost provenance")
			}
			changed := *original
			changed.Target.SourceProjectID++
			copy := *run
			copy.PollingContext, err = changed.Canonical()
			if err != nil {
				t.Fatal(err)
			}
			copy.PollingContextDigest, err = changed.Digest()
			if err != nil {
				t.Fatal(err)
			}
			result, err := f.runs.StartPollingRunIfPending(f.ctx, &copy)
			if err == nil || result.ClaimedNow {
				t.Fatal("mutated provenance admitted")
			}
			result, err = f.runs.StartPollingRunIfPending(f.ctx, run)
			if err != nil || !result.ClaimedNow {
				t.Fatalf("original provenance failed admission: %v", err)
			}
		})
	}
}
