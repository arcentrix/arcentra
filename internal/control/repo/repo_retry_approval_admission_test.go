package repo

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"gorm.io/gorm"
)

func installRetryApprovalTestAuthority(t *testing.T, f *generationFixture, extra func(string, string) error) {
	t.Helper()
	if err := f.db.WithContext(f.ctx).AutoMigrate(&model.User{}, &model.TeamMember{}, &model.Team{}, &model.Permission{}, &model.RolePermissionBinding{}, &model.Role{}, &model.RoleGrant{}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"actor", "voter"} {
		if err := f.db.WithContext(f.ctx).Create(&model.User{UserID: id, IsEnabled: 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	repos := &Repositories{database: statusTestDB{f.db}}
	err := SetRetryApprovalAuthorizer(repos, func(ctx context.Context, view RetryApprovalAuthorizationView, e *retryapproval.Evidence, id, action string) error {
		reader, ok := view.LiveRepositories().User.(IExecutionActorReader)
		if !ok {
			return fmt.Errorf("typed actor reader unavailable")
		}
		u, err := reader.GetExecutionActor(ctx, id)
		if err != nil || u == nil || u.UserID != id || u.IsEnabled != 1 {
			return ErrApprovalForbidden
		}
		if extra != nil {
			return extra(id, action)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetRetryApprovalAuthorizer(repos, nil) })
}
func retryApprovalTestRequest(t *testing.T, f *generationFixture, g *taskqueue.JobGeneration, operation string) retryapproval.Request {
	t.Helper()
	job, err := f.jobs.GetByJobRunID(f.ctx, "job")
	if err != nil {
		t.Fatal(err)
	}
	return retryapproval.Request{Version: 1, Operation: operation, Identity: f.identity, GenerationID: g.GenerationID, GenerationBindingDigest: g.BindingDigest, RetryPlanDigest: job.RetryPlanDigest, InputManifestDigest: job.InputManifestDigest}
}
func retryApprovalTestRow(t *testing.T, f *generationFixture) (*ApprovalRepo, *model.ApprovalRequest) {
	t.Helper()
	job, err := f.jobs.GetByJobRunID(f.ctx, "job")
	if err != nil {
		t.Fatal(err)
	}
	r := &ApprovalRepo{IDatabase: statusTestDB{f.db}}
	a, err := r.GetByApprovalID(f.ctx, job.RetryApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	return r, a
}
func approveRetryApproval(t *testing.T, f *generationFixture) {
	t.Helper()
	r, a := retryApprovalTestRow(t, f)
	if _, err := r.Decide(f.ctx, a.ApprovalID, "voter", model.ApprovalDecisionApprove, ""); err != nil {
		t.Fatal(err)
	}
}

func TestRetryApprovalAdmissionIsDistinctAndFresh(t *testing.T) {
	f := gatedPinFixture(t)
	g := f.admission(t)
	f.pin(t)
	r, a := retryApprovalTestRow(t, f)
	if _, err := r.Decide(f.ctx, a.ApprovalID, "voter", model.ApprovalDecisionApprove, ""); err == nil {
		t.Fatal("missing validator admitted vote")
	}
	installRetryApprovalTestAuthority(t, f, nil)
	request := retryApprovalTestRequest(t, f, g, "observe")
	pending, err := f.jobs.ReconcileJobRetryApproval(f.ctx, request)
	if err != nil || pending.State != "pending" || !pending.Required || pending.ExecutionAllowed || pending.Admission != nil {
		t.Fatalf("observe granted authority: %+v %v", pending, err)
	}
	request.Operation = "admit"
	if _, err := f.jobs.ReconcileJobRetryApproval(f.ctx, request); err == nil {
		t.Fatal("pending startup admitted")
	}
	if err := f.report(t, g, "first-1", model.JobRunStatusRunning, false); err == nil {
		t.Fatal("pending command admitted")
	}
	approveRetryApproval(t, f)
	if err := f.report(t, g, "first-1", model.JobRunStatusRunning, false); err == nil {
		t.Fatal("verdict alone admitted command")
	}
	var before model.JobRunGeneration
	if err := f.db.WithContext(f.ctx).Where("generation_id = ?", g.GenerationID).First(&before).Error; err != nil {
		t.Fatal(err)
	}
	approved, err := f.jobs.ReconcileJobRetryApproval(f.ctx, request)
	if err != nil || !approved.ExecutionAllowed || approved.Admission == nil {
		t.Fatalf("approved startup denied: %+v %v", approved, err)
	}
	replay, err := f.jobs.ReconcileJobRetryApproval(f.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := approved.Admission.Canonical()
	again, _ := replay.Admission.Canonical()
	if first != again {
		t.Fatal("startup replay renewed immutable receipt")
	}
	var after model.JobRunGeneration
	if err := f.db.WithContext(f.ctx).Where("generation_id = ?", g.GenerationID).First(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.GenerationJSON != before.GenerationJSON || after.AdmissionDigest != before.AdmissionDigest || after.AdmissionRequest != before.AdmissionRequest || after.RetryApprovalAdmission == "" {
		t.Fatal("startup rewrote preparation receipt")
	}
	request.Operation = "observe"
	observed, err := f.jobs.ReconcileJobRetryApproval(f.ctx, request)
	if err != nil || observed.ExecutionAllowed || observed.Admission != nil {
		t.Fatal("approved observation granted execution", err)
	}
	if err := f.report(t, g, "first-1", model.JobRunStatusRunning, false); err != nil {
		t.Fatal(err)
	}
	if err := f.db.WithContext(f.ctx).Model(&model.User{}).Where("user_id = ?", "voter").Update("is_enabled", 0).Error; err != nil {
		t.Fatal(err)
	}
	request.Operation = "admit"
	if _, err := f.jobs.ReconcileJobRetryApproval(f.ctx, request); err == nil {
		t.Fatal("old receipt bypassed revoked voter")
	}
	if err := f.report(t, g, "second-1", model.JobRunStatusRunning, false); err == nil {
		t.Fatal("new command bypassed revoked voter")
	}
	if _, err := r.Decide(f.ctx, a.ApprovalID, "voter", model.ApprovalDecisionApprove, "lost ACK"); err != nil {
		t.Fatal("exact historical vote ACK failed", err)
	}
	if _, err := r.Decide(f.ctx, a.ApprovalID, "voter", model.ApprovalDecisionReject, ""); err == nil {
		t.Fatal("opposing replay accepted")
	}
	var count int64
	if err := f.db.WithContext(f.ctx).Model(&model.ApprovalDecision{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("vote replay duplicated", count, err)
	}
}

func TestRetryApprovalAdmissionRejectsBrokenBindings(t *testing.T) {
	tests := []struct {
		name   string
		entity any
		key    string
		value  any
	}{
		{"required marker", &model.JobRun{}, "retry_approval_required", false},
		{"missing backlink", &model.JobRun{}, "retry_approval_id", ""},
		{"changed hash", &model.JobRun{}, "retry_approval_evidence_hash", "v1:" + strings.Repeat("f", 64)},
		{"wrong kind", &model.ApprovalRequest{}, "binding_kind", "deployment"},
		{"missing forward link", &model.ApprovalRequest{}, "retry_job_run_id", nil},
		{"mixed binding", &model.ApprovalRequest{}, "deployment_release_id", "release"},
		{"missing evidence", &model.ApprovalRequest{}, "evidence_json", ""},
		{"forged quorum", &model.ApprovalRequest{}, "approved_count", 2},
		{"changed plan", &model.JobRun{}, "retry_plan", "{}"},
		{"changed runtime", &model.JobRun{}, "runtime_image", "changed"},
		{"changed inputs", &model.JobRun{}, "input_manifest", "{}"},
		{"changed source", &model.JobRun{}, "source_resolved_commit", strings.Repeat("f", 40)},
		{"missing context", &model.PipelineRun{}, "retry_approval_context", ""},
		{"foreign actor", &model.PipelineRun{}, "triggered_by", "intruder"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := gatedPinFixture(t)
			g := f.admission(t)
			f.pin(t)
			installRetryApprovalTestAuthority(t, f, nil)
			approveRetryApproval(t, f)
			request := retryApprovalTestRequest(t, f, g, "admit")
			if err := f.db.WithContext(f.ctx).Model(test.entity).Where("1 = 1").Update(test.key, test.value).Error; err != nil {
				if test.name == "mixed binding" && strings.Contains(err.Error(), "ck_approval_exclusive_binding") {
					return
				}
				t.Fatal(err)
			}
			if _, err := f.jobs.ReconcileJobRetryApproval(f.ctx, request); err == nil {
				t.Fatal("altered approval admitted")
			}
			if err := f.report(t, g, "first-1", model.JobRunStatusRunning, false); err == nil {
				t.Fatal("altered approval admitted RUNNING")
			}
		})
	}
}

func TestRetryApprovalPendingAllSkippedCannotPublishSuccess(t *testing.T) {
	f := gatedPinFixture(t)
	g := f.admission(t)
	f.pin(t)
	for _, id := range []string{"first-1", "second-1", "first-2", "second-2", "first-3", "second-3"} {
		if err := f.report(t, g, id, model.JobRunStatusSkipped, false); err != nil {
			t.Fatal(err)
		}
	}
	report := func() error {
		job, err := f.jobs.GetByJobRunID(f.ctx, "job")
		if err != nil {
			return err
		}
		return f.jobs.PatchReportedStatus(agentidentity.WithGenerationReport(f.ctx, g.GenerationID, g.BindingDigest), job, map[string]any{"status": model.JobRunStatusSuccess, "terminal_report_digest": strings.Repeat("c", 64)})
	}
	if err := report(); err == nil {
		t.Fatal("all skipped bypassed pending approval")
	}
	installRetryApprovalTestAuthority(t, f, nil)
	approveRetryApproval(t, f)
	if err := report(); err == nil {
		t.Fatal("all skipped bypassed explicit startup admission")
	}
	if _, err := f.jobs.ReconcileJobRetryApproval(f.ctx, retryApprovalTestRequest(t, f, g, "admit")); err != nil {
		t.Fatal(err)
	}
	if err := report(); err != nil {
		t.Fatal("approved skipped job could not finish", err)
	}
}

func TestRetryApprovalCancellationAndFinalizationCancelOnlyPending(t *testing.T) {
	for _, approved := range []bool{false, true} {
		for _, finalize := range []bool{false, true} {
			t.Run(fmt.Sprintf("approved=%t/finalize=%t", approved, finalize), func(t *testing.T) {
				f := gatedPinFixture(t)
				g := f.admission(t)
				f.pin(t)
				r, a := retryApprovalTestRow(t, f)
				if approved {
					installRetryApprovalTestAuthority(t, f, nil)
					approveRetryApproval(t, f)
				}
				job, err := f.jobs.GetByJobRunID(f.ctx, "job")
				if err != nil {
					t.Fatal(err)
				}
				if finalize {
					err = f.jobs.FinalizeRetryJobIfCurrent(f.ctx, job, map[string]any{"status": model.JobRunStatusTimeout, "end_time": time.Now()})
				} else {
					err = f.jobs.CancelAgentJobIfCurrent(f.ctx, "job", job.Status, map[string]any{"status": model.JobRunStatusCancelled})
				}
				if err != nil {
					t.Fatal(err)
				}
				got, err := r.GetByApprovalID(f.ctx, a.ApprovalID)
				if err != nil {
					t.Fatal(err)
				}
				want := model.ApprovalStatusCanceled
				if approved {
					want = model.ApprovalStatusApproved
				}
				if got.Status != want {
					t.Fatalf("verdict changed: %d want %d", got.Status, want)
				}
				if _, err := f.jobs.ReconcileJobRetryApproval(f.ctx, retryApprovalTestRequest(t, f, g, "admit")); err == nil {
					t.Fatal("terminal job admitted")
				}
			})
		}
	}
}

func setRetryApprovalTestExpiry(t *testing.T, f *generationFixture, expiry time.Time) {
	t.Helper()
	r, a := retryApprovalTestRow(t, f)
	_ = r
	e, err := retryapproval.ParseEvidence(a.EvidenceJSON)
	if err != nil {
		t.Fatal(err)
	}
	expiry = expiry.UTC().Truncate(time.Millisecond)
	e.ExpiresAtUnixMilli = expiry.UnixMilli()
	body, err := e.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := e.Digest()
	if err := f.db.WithContext(f.ctx).Model(&model.ApprovalRequest{}).Where("approval_id = ?", a.ApprovalID).Updates(map[string]any{"expires_at": expiry, "evidence_json": body, "evidence_hash": hash}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.WithContext(f.ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("retry_approval_evidence_hash", hash).Error; err != nil {
		t.Fatal(err)
	}
}
func TestRetryApprovalDeadlineCrossingRollsBackVoteAndStartup(t *testing.T) {
	t.Run("vote", func(t *testing.T) {
		f := gatedPinFixture(t)
		f.admission(t)
		f.pin(t)
		installRetryApprovalTestAuthority(t, f, func(id, action string) error {
			if action == "approval:approve" {
				time.Sleep(70 * time.Millisecond)
			}
			return nil
		})
		setRetryApprovalTestExpiry(t, f, time.Now().Add(40*time.Millisecond))
		r, a := retryApprovalTestRow(t, f)
		if _, err := r.Decide(f.ctx, a.ApprovalID, "voter", model.ApprovalDecisionApprove, ""); err == nil {
			t.Fatal("late vote committed")
		}
		var count int64
		f.db.WithContext(f.ctx).Model(&model.ApprovalDecision{}).Count(&count)
		if count != 0 {
			t.Fatal("late vote retained")
		}
		got, _ := r.GetByApprovalID(f.ctx, a.ApprovalID)
		if got.Status != model.ApprovalStatusExpired {
			t.Fatal("late pending verdict not expired")
		}
	})
	t.Run("startup", func(t *testing.T) {
		f := gatedPinFixture(t)
		g := f.admission(t)
		f.pin(t)
		installRetryApprovalTestAuthority(t, f, nil)
		approveRetryApproval(t, f)
		setRetryApprovalTestExpiry(t, f, time.Now().Add(40*time.Millisecond))
		name := "test:late-admission"
		if err := f.db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
			if tx.Statement.Table == "job_run_generation" {
				time.Sleep(70 * time.Millisecond)
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.db.Callback().Update().Remove(name) })
		if _, err := f.jobs.ReconcileJobRetryApproval(f.ctx, retryApprovalTestRequest(t, f, g, "admit")); err == nil {
			t.Fatal("late startup committed")
		}
		var row model.JobRunGeneration
		if err := f.db.WithContext(f.ctx).Where("generation_id = ?", g.GenerationID).First(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.RetryApprovalAdmission != "" {
			t.Fatal("late startup receipt retained")
		}
	})
}

func TestRetryApprovalAdvanceRechecksAuthorityAndKeepsOneBinding(t *testing.T) {
	f := gatedPinFixture(t)
	g := f.admission(t)
	f.pin(t)
	installRetryApprovalTestAuthority(t, f, nil)
	approveRetryApproval(t, f)
	if _, err := f.jobs.ReconcileJobRetryApproval(f.ctx, retryApprovalTestRequest(t, f, g, "admit")); err != nil {
		t.Fatal(err)
	}
	f.failedRound(t, g)
	advance := f.advance(t, g)
	if err := f.db.WithContext(f.ctx).Model(&model.User{}).Where("user_id = ?", "actor").Update("is_enabled", 0).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.jobs.ReconcileJobGeneration(f.ctx, advance); err == nil {
		t.Fatal("revoked requester admitted successor")
	}
	if err := f.db.WithContext(f.ctx).Model(&model.User{}).Where("user_id = ?", "actor").Update("is_enabled", 1).Error; err != nil {
		t.Fatal(err)
	}
	next, err := f.jobs.ReconcileJobGeneration(f.ctx, advance)
	if err != nil {
		t.Fatal(err)
	}
	if next.Number != 2 {
		t.Fatal("wrong successor")
	}
	if err := f.report(t, next, "first-2", model.JobRunStatusRunning, false); err == nil {
		t.Fatal("successor reused predecessor startup receipt")
	}
	if _, err := f.jobs.ReconcileJobRetryApproval(f.ctx, retryApprovalTestRequest(t, f, next, "admit")); err != nil {
		t.Fatal(err)
	}
	if err := f.report(t, next, "first-2", model.JobRunStatusRunning, false); err != nil {
		t.Fatal(err)
	}
	var count int64
	f.db.WithContext(f.ctx).Model(&model.ApprovalRequest{}).Count(&count)
	if count != 1 {
		t.Fatal("successor created new approval")
	}
}

func TestRetryApprovalRequiredPlanCannotLoseAllGateMarkers(t *testing.T) {
	f := gatedPinFixture(t)
	g := f.admission(t)
	f.pin(t)
	installRetryApprovalTestAuthority(t, f, nil)
	approveRetryApproval(t, f)
	request := retryApprovalTestRequest(t, f, g, "admit")
	if err := f.db.WithContext(f.ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(map[string]any{"retry_approval_required": false, "retry_approval_id": "", "retry_approval_evidence_hash": ""}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.WithContext(f.ctx).Model(&model.PipelineRun{}).Where("run_id = ?", "run").Updates(map[string]any{"retry_approval_context": "", "retry_approval_context_digest": ""}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.jobs.ReconcileJobRetryApproval(f.ctx, request); err == nil {
		t.Fatal("required frozen plan became ungated")
	}
	if err := f.report(t, g, "first-1", model.JobRunStatusRunning, false); err == nil {
		t.Fatal("required frozen plan executed after marker removal")
	}
}

func TestRetryApprovalCommittedAdvanceACKSurvivesRevocationWithoutAuthority(t *testing.T) {
	f := gatedPinFixture(t)
	g := f.admission(t)
	f.pin(t)
	installRetryApprovalTestAuthority(t, f, nil)
	approveRetryApproval(t, f)
	if _, err := f.jobs.ReconcileJobRetryApproval(f.ctx, retryApprovalTestRequest(t, f, g, "admit")); err != nil {
		t.Fatal(err)
	}
	f.failedRound(t, g)
	request := f.advance(t, g)
	next, err := f.jobs.ReconcileJobGeneration(f.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.WithContext(f.ctx).Model(&model.User{}).Where("user_id = ?", "voter").Update("is_enabled", 0).Error; err != nil {
		t.Fatal(err)
	}
	replay, err := f.jobs.ReconcileJobGeneration(f.ctx, request)
	if err != nil || replay.GenerationID != next.GenerationID || replay.ExecutionAllowed {
		t.Fatalf("historical cleanup ACK regained authority or failed: %+v %v", replay, err)
	}
}
