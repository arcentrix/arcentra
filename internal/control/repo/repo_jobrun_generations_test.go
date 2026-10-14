package repo

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"gorm.io/gorm"
)

type generationFixture struct {
	ctx      context.Context
	db       *gorm.DB
	jobs     *JobRunRepo
	steps    *StepRunRepo
	identity taskqueue.JobInputIdentity
	manifest *taskqueue.JobInputManifest
	admit    taskqueue.JobGenerationTransition
	plan     *taskqueue.JobRetryPlan
}

func newGenerationFixture(t *testing.T, configure ...func(*pipelinev1.Retry)) *generationFixture {
	t.Helper()
	ctx, db, jobs, _ := sourceTestRepository(t)
	if err := db.WithContext(ctx).AutoMigrate(&model.Agent{}, &model.PipelineRun{}, &model.JobRunGeneration{}, &model.Pipeline{}, &model.Project{}); err != nil {
		t.Fatal(err)
	}
	for _, v := range []any{&model.Agent{AgentID: "agent", IsEnabled: 1}, &model.PipelineRun{RunID: "run", PipelineID: "pipeline", Status: model.PipelineStatusRunning}, &model.Pipeline{PipelineID: "pipeline", ProjectID: "project"}, &model.Project{ProjectID: "project"}} {
		if err := db.WithContext(ctx).Create(v).Error; err != nil {
			t.Fatal(err)
		}
	}
	runtime := repoRuntimeRequest(t)
	policy := &pipelinev1.Retry{Mode: "whole_job", MaxAttempts: 3, Idempotent: true, AllowedExitCodes: []int32{9}}
	for _, modify := range configure {
		modify(policy)
	}
	encoded, _ := sonic.MarshalString(policy)
	payload := &taskqueue.JobRunTaskPayload{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", AgentID: "agent", Runtime: runtime, Retry: policy}
	for index, name := range []string{"first", "second"} {
		ids := []string{name + "-1", name + "-2", name + "-3"}
		payload.Steps = append(payload.Steps, taskqueue.StepPayload{Name: name, StepIndex: int32(index), StepRunID: ids[0], AttemptRunIDs: ids, Uses: "shell", Args: map[string]any{"run": "echo fixture"}})
		for i, id := range ids {
			previous := ""
			if i > 0 {
				previous = ids[i-1]
			}
			v := &model.StepRun{StepRunID: id, Name: name, StepIndex: index, JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run", Attempt: i + 1, RetryPolicy: encoded, RetryMode: "whole_job", PreviousStepRunID: previous, Uses: "shell", Status: 1}
			if err := db.WithContext(ctx).Create(v).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	plan, err := taskqueue.NewJobRetryPlan(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.CaptureRuntimeRetryExpectation(ctx, "pipeline", "run", "job", "agent", nil, runtime, nil, nil, plan); err != nil {
		t.Fatal(err)
	}
	job, _ := jobs.GetByJobRunID(ctx, "job")
	if err := jobs.PatchReportedStatus(ctx, job, map[string]any{"status": 3, "claim_id": "claim", "start_time": time.Now()}); err != nil {
		t.Fatal(err)
	}
	job, _ = jobs.GetByJobRunID(ctx, "job")
	identity := generationIdentity(job)
	manifest, _ := taskqueue.NewJobInputManifest("", nil, nil)
	admit := taskqueue.JobGenerationTransition{Version: 1, Identity: identity, RetryPlanDigest: job.RetryPlanDigest, Operation: "admit"}
	admit.RequestDigest, _ = admit.Digest()
	return &generationFixture{ctx, db, jobs, &StepRunRepo{IDatabase: statusTestDB{db}}, identity, manifest, admit, plan}
}
func (f *generationFixture) pin(t *testing.T) string {
	t.Helper()
	job, err := f.jobs.GetOrRecordJobInputs(f.ctx, f.identity, f.manifest)
	if err != nil {
		t.Fatal(err)
	}
	return job.InputManifestDigest
}
func (f *generationFixture) admission(t *testing.T) *taskqueue.JobGeneration {
	t.Helper()
	g, err := f.jobs.ReconcileJobGeneration(f.ctx, f.admit)
	if err != nil {
		t.Fatal(err)
	}
	return g
}
func (f *generationFixture) report(t *testing.T, g *taskqueue.JobGeneration, id string, next int, eligible bool) error {
	t.Helper()
	s, err := f.steps.GetByStepRunID(f.ctx, id)
	if err != nil {
		return err
	}
	claim := agentidentity.StepClaimID(f.identity.ClaimID, id)
	patch := map[string]any{"status": next, "claim_id": claim, "generation_id": g.GenerationID, "generation_binding_digest": g.BindingDigest, "retryable_shell_exit": eligible, "exit_code": int32(0)}
	if next == 5 {
		patch["exit_code"] = int32(9)
	}
	if next >= 4 {
		patch["terminal_report_digest"] = strings.Repeat(fmt.Sprintf("%x", next), 64)
	}
	return f.steps.PatchReportedStatus(agentidentity.WithGenerationReport(f.ctx, g.GenerationID, g.BindingDigest), s, patch)
}
func (f *generationFixture) failedRound(t *testing.T, g *taskqueue.JobGeneration) {
	t.Helper()
	id := fmt.Sprintf("first-%d", g.Number)
	if err := f.report(t, g, id, 3, false); err != nil {
		t.Fatal(err)
	}
	if err := f.report(t, g, id, 5, true); err != nil {
		t.Fatal(err)
	}
	if err := f.report(t, g, fmt.Sprintf("second-%d", g.Number), 8, false); err != nil {
		t.Fatal(err)
	}
}
func (f *generationFixture) advance(t *testing.T, g *taskqueue.JobGeneration) taskqueue.JobGenerationTransition {
	t.Helper()
	job, _ := f.jobs.GetByJobRunID(f.ctx, "job")
	r := &taskqueue.JobGenerationCleanupReceipt{Version: 1, PipelineRunID: g.PipelineRunID, JobRunID: g.JobRunID, AgentID: g.AgentID, OriginalClaimID: g.OriginalClaimID, RuntimeBindingDigest: g.RuntimeBindingDigest, GenerationID: g.GenerationID, GenerationBindingDigest: g.BindingDigest, RuntimeClaimID: g.RuntimeClaimID, SessionID: g.RuntimeClaimID, Namespace: "fixture", ContainerID: "arcentra-" + g.RuntimeClaimID, SnapshotID: "arcentra-" + g.RuntimeClaimID + "-snapshot", Snapshotter: "fixture", ResourceOwnerDigest: strings.Repeat("a", 64), WorkspaceID: "job-" + g.RuntimeClaimID, WorkspaceDevice: 1, WorkspaceInode: 2, ProcessesAbsent: true, ContainerAbsent: true, SnapshotAbsent: true, WorkspaceAbsent: true}
	req := taskqueue.JobGenerationTransition{Version: 1, Identity: f.identity, InputManifestDigest: job.InputManifestDigest, RetryPlanDigest: job.RetryPlanDigest, Operation: "advance", PreviousGenerationID: g.GenerationID, FailedStepRunID: fmt.Sprintf("first-%d", g.Number), FailedStepReceiptDigest: strings.Repeat("5", 64), CleanupReceipt: r}
	req.RequestDigest, _ = req.Digest()
	return req
}

func TestGenerationBootstrapRequiresPinAndKeepsOriginalClaim(t *testing.T) {
	f := newGenerationFixture(t)
	g := f.admission(t)
	if g.Number != 1 || !g.PreparationOnly || g.InputManifestDigest != "" || !g.ExecutionAllowed {
		t.Fatalf("bad preparation: %+v", g)
	}
	if err := f.report(t, g, "first-1", 3, false); err == nil {
		t.Fatal("command claim before input pin accepted")
	}
	digest := f.pin(t)
	f.failedRound(t, g)
	request := f.advance(t, g)
	next, err := f.jobs.ReconcileJobGeneration(f.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if next.Number != 2 || next.InputManifestDigest != digest || next.GenerationID == g.GenerationID || next.RuntimeClaimID == g.RuntimeClaimID || next.PreparationOnly {
		t.Fatalf("not a fresh bound generation: %+v", next)
	}
	job, _ := f.jobs.GetByJobRunID(f.ctx, "job")
	if job.ClaimID != "claim" || job.InputManifestDigest != digest || job.CurrentGenerationID != next.GenerationID {
		t.Fatal("generation changed original claim or input")
	}
	if err := f.report(t, g, "first-2", 3, false); err == nil {
		t.Fatal("old generation admitted new command")
	}
	if err := f.report(t, next, "first-2", 3, false); err != nil {
		t.Fatal(err)
	}
}

func TestGenerationAdvanceIdempotentConcurrentAndReadonlyInspect(t *testing.T) {
	f := newGenerationFixture(t)
	g := f.admission(t)
	f.pin(t)
	f.failedRound(t, g)
	req := f.advance(t, g)
	var wg sync.WaitGroup
	results := make(chan *taskqueue.JobGeneration, 12)
	failures := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		safe.Go(func() {
			defer wg.Done()
			next, err := f.jobs.ReconcileJobGeneration(f.ctx, req)
			if err != nil {
				failures <- err
			} else {
				results <- next
			}
		})
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	id := ""
	for next := range results {
		if id != "" && id != next.GenerationID {
			t.Fatal("duplicate admission")
		}
		id = next.GenerationID
	}
	var count int64
	f.db.Model(&model.JobRunGeneration{}).Count(&count)
	if count != 2 {
		t.Fatalf("allocated %d generations", count)
	}
	inspect := f.admit
	inspect.Operation = "inspect"
	inspect.RequestDigest, _ = inspect.Digest()
	observed, err := f.jobs.ReconcileJobGeneration(f.ctx, inspect)
	if err != nil || observed.ExecutionAllowed || observed.GenerationID != id {
		t.Fatalf("inspect: %+v %v", observed, err)
	}
	old, err := f.jobs.ReconcileJobGeneration(f.ctx, f.admit)
	if err != nil || old.GenerationID != g.GenerationID || old.ExecutionAllowed {
		t.Fatalf("old admit authorized execution: %+v %v", old, err)
	}
	req.CleanupReceipt.WorkspaceInode++
	req.RequestDigest, _ = req.Digest()
	if _, err := f.jobs.ReconcileJobGeneration(f.ctx, req); err == nil {
		t.Fatal("changed transition replay accepted")
	}
}

func TestGenerationAdvanceRejectsLostAuthorityAndEvidence(t *testing.T) {
	mutations := map[string]func(*generationFixture, *taskqueue.JobGeneration, *taskqueue.JobGenerationTransition){
		"cancel-job": func(f *generationFixture, _ *taskqueue.JobGeneration, _ *taskqueue.JobGenerationTransition) {
			f.db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("status", 6)
		},
		"cancel-run": func(f *generationFixture, _ *taskqueue.JobGeneration, _ *taskqueue.JobGenerationTransition) {
			f.db.Model(&model.PipelineRun{}).Where("run_id = ?", "run").Update("status", 6)
		},
		"disable-agent": func(f *generationFixture, _ *taskqueue.JobGeneration, _ *taskqueue.JobGenerationTransition) {
			f.db.Model(&model.Agent{}).Where("agent_id = ?", "agent").Update("is_enabled", 0)
		},
		"different-agent": func(_ *generationFixture, _ *taskqueue.JobGeneration, r *taskqueue.JobGenerationTransition) {
			r.Identity.AgentID = "Agent"
		},
		"different-claim": func(_ *generationFixture, _ *taskqueue.JobGeneration, r *taskqueue.JobGenerationTransition) {
			r.Identity.ClaimID = "Claim"
		},
		"different-input": func(_ *generationFixture, _ *taskqueue.JobGeneration, r *taskqueue.JobGenerationTransition) {
			r.InputManifestDigest = "v1:" + strings.Repeat("b", 64)
		},
		"different-plan": func(_ *generationFixture, _ *taskqueue.JobGeneration, r *taskqueue.JobGenerationTransition) {
			r.RetryPlanDigest = "v1:" + strings.Repeat("b", 64)
		},
		"bad-receipt": func(_ *generationFixture, _ *taskqueue.JobGeneration, r *taskqueue.JobGenerationTransition) {
			r.FailedStepReceiptDigest = strings.Repeat("e", 64)
		},
		"incomplete-round": func(f *generationFixture, _ *taskqueue.JobGeneration, _ *taskqueue.JobGenerationTransition) {
			f.db.Model(&model.StepRun{}).Where("step_run_id = ?", "second-1").Updates(map[string]any{"status": 2, "terminal_report_digest": ""})
		},
		"mixed-failure": func(f *generationFixture, _ *taskqueue.JobGeneration, _ *taskqueue.JobGenerationTransition) {
			f.db.Model(&model.StepRun{}).Where("step_run_id = ?", "first-1").Update("retryable_shell_exit", false)
		},
		"unsafe-code": func(f *generationFixture, _ *taskqueue.JobGeneration, _ *taskqueue.JobGenerationTransition) {
			f.db.Model(&model.StepRun{}).Where("step_run_id = ?", "first-1").Update("exit_code", 127)
		},
		"not-skipped-remainder": func(f *generationFixture, _ *taskqueue.JobGeneration, _ *taskqueue.JobGenerationTransition) {
			f.db.Model(&model.StepRun{}).Where("step_run_id = ?", "second-1").Update("status", 4)
		},
		"cleanup-incomplete": func(_ *generationFixture, _ *taskqueue.JobGeneration, r *taskqueue.JobGenerationTransition) {
			r.CleanupReceipt.ProcessesAbsent = false
		},
		"cleanup-foreign": func(_ *generationFixture, _ *taskqueue.JobGeneration, r *taskqueue.JobGenerationTransition) {
			r.CleanupReceipt.GenerationBindingDigest = "v1:" + strings.Repeat("b", 64)
		},
		"deadline": func(f *generationFixture, _ *taskqueue.JobGeneration, _ *taskqueue.JobGenerationTransition) {
			f.db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(map[string]any{"timeout": "1s", "start_time": time.Now().Add(-time.Hour)})
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			f := newGenerationFixture(t)
			g := f.admission(t)
			f.pin(t)
			f.failedRound(t, g)
			req := f.advance(t, g)
			mutate(f, g, &req)
			req.RequestDigest, _ = req.Digest()
			if _, err := f.jobs.ReconcileJobGeneration(f.ctx, req); err == nil {
				t.Fatal("invalid advance accepted")
			}
			var count int64
			f.db.Model(&model.JobRunGeneration{}).Count(&count)
			if count != 1 {
				t.Fatal("rejected transition allocated resources")
			}
		})
	}
}

func TestGenerationBudgetAndStaleReports(t *testing.T) {
	f := newGenerationFixture(t)
	g := f.admission(t)
	f.pin(t)
	for round := 1; round <= 3; round++ {
		f.failedRound(t, g)
		req := f.advance(t, g)
		next, err := f.jobs.ReconcileJobGeneration(f.ctx, req)
		if round == 3 {
			if err == nil {
				t.Fatal("budget exceeded")
			}
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		g = next
	}
}

func TestGenerationGenericRepositoriesCannotExpandOrPublishOldAttempts(t *testing.T) {
	f := newGenerationFixture(t)
	g := f.admission(t)
	f.pin(t)
	f.failedRound(t, g)
	next, err := f.jobs.ReconcileJobGeneration(f.ctx, f.advance(t, g))
	if err != nil {
		t.Fatal(err)
	}
	for _, patch := range []map[string]any{{"status": 3}, {"current_generation": 9}, {"retry_plan_digest": "changed"}, {"timeout": "24h"}} {
		if err := f.jobs.UpdateByJobRunID(f.ctx, "job", patch); err == nil {
			t.Fatal("generic job update changed frozen retry")
		}
	}
	for _, patch := range []map[string]any{{"status": 3}, {"retry_policy": "{}"}, {"generation_id": g.GenerationID}} {
		if err := f.steps.PatchByStepRunID(f.ctx, "first-2", patch); err == nil {
			t.Fatal("generic step update changed frozen retry")
		}
	}
	job, _ := f.jobs.GetByJobRunID(f.ctx, "job")
	before := *job
	if err := f.jobs.PatchReportedStatus(agentidentity.WithGenerationReport(f.ctx, g.GenerationID, g.BindingDigest), job, map[string]any{"status": 5, "claim_id": "claim", "terminal_report_digest": strings.Repeat("a", 64)}); err == nil {
		t.Fatal("old generation finalized job")
	}
	after, _ := f.jobs.GetByJobRunID(f.ctx, "job")
	if !reflect.DeepEqual(before, *after) {
		t.Fatal("rejected report changed job")
	}
	if err := validateGenerationArtifact(agentidentity.WithGenerationReport(f.ctx, next.GenerationID, next.BindingDigest), f.db, job, "first-1"); err == nil {
		t.Fatal("old-generation output accepted")
	}
	for _, id := range []string{"first-3", "second-3"} {
		if err := f.report(t, next, id, 8, false); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGenerationCancellationRaceNeverAdmitsCommandsAfterCancel(t *testing.T) {
	f := newGenerationFixture(t)
	g := f.admission(t)
	f.pin(t)
	f.failedRound(t, g)
	request := f.advance(t, g)
	var wg sync.WaitGroup
	wg.Add(2)
	start := make(chan struct{})
	advance := make(chan error, 1)
	cancelled := make(chan error, 1)
	safe.Go(func() {
		defer wg.Done()
		<-start
		_, err := f.jobs.ReconcileJobGeneration(f.ctx, request)
		advance <- err
	})
	safe.Go(func() {
		defer wg.Done()
		<-start
		cancelled <- f.jobs.CancelAgentJobIfCurrent(f.ctx, "job", 3, map[string]any{"status": 6})
	})
	close(start)
	wg.Wait()
	if err := <-cancelled; err != nil {
		t.Fatal(err)
	}
	_ = <-advance
	job, _ := f.jobs.GetByJobRunID(f.ctx, "job")
	if job.Status != 6 || job.ClaimID != "claim" || job.CancellationClaimID != "claim" {
		t.Fatal("cancellation lost original authority")
	}
	inspect := f.admit
	inspect.Operation = "inspect"
	inspect.RequestDigest, _ = inspect.Digest()
	last, err := f.jobs.ReconcileJobGeneration(f.ctx, inspect)
	if err != nil || last.ExecutionAllowed {
		t.Fatalf("cancelled inspection: %+v %v", last, err)
	}
	if err := f.report(t, last, "first-2", 3, false); err == nil {
		t.Fatal("command admitted after cancellation")
	}
	if _, err := f.jobs.GetOrRecordJobInputs(f.ctx, f.identity, f.manifest); err == nil {
		t.Fatal("cancelled job reacquired input authority")
	}
}

func TestGenerationDelayIsControllerEnforcedAndDoesNotPreventUnusedClosure(t *testing.T) {
	f := newGenerationFixture(t, func(p *pipelinev1.Retry) { p.Delay = "1h" })
	g := f.admission(t)
	f.pin(t)
	f.failedRound(t, g)
	next, err := f.jobs.ReconcileJobGeneration(f.ctx, f.advance(t, g))
	if err != nil {
		t.Fatal(err)
	}
	if next.NotBeforeUnixMilli <= time.Now().UnixMilli() {
		t.Fatal("delay not bound")
	}
	if err := f.report(t, next, "first-2", 3, false); err == nil {
		t.Fatal("early command admitted")
	}
	for _, id := range []string{"first-2", "second-2", "first-3", "second-3"} {
		if err := f.report(t, next, id, 7, false); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGenerationMigrationDoesNotInventLegacyAuthority(t *testing.T) {
	ctx, db, jobs, _ := sourceTestRepository(t)
	for _, field := range jobGenerationColumns {
		if err := db.WithContext(ctx).Migrator().DropColumn(&model.JobRun{}, field); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.WithContext(ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(map[string]any{"status": 4, "claim_id": "legacy"}).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := MigrateSchema(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	job, err := jobs.GetByJobRunID(ctx, "job")
	if err != nil || !emptyJobGeneration(job) || job.ClaimID != "legacy" || job.Status != 4 {
		t.Fatalf("legacy promoted: %+v %v", job, err)
	}
	var count int64
	db.Model(&model.JobRunGeneration{}).Count(&count)
	if count != 0 {
		t.Fatal("migration invented generation records")
	}
}

func TestGenerationControllerFinalizesCaptureAndActiveFailuresWithoutReceipts(t *testing.T) {
	for _, name := range []string{"enqueue-failed", "active-timeout", "active-cancel"} {
		t.Run(name, func(t *testing.T) {
			f := newGenerationFixture(t)
			status := model.JobRunStatusFailed
			if name == "enqueue-failed" {
				if err := f.db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(map[string]any{"status": 2, "claim_id": "", "start_time": nil}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if name != "enqueue-failed" {
				g := f.admission(t)
				f.pin(t)
				if err := f.report(t, g, "first-1", 3, false); err != nil {
					t.Fatal(err)
				}
				status = model.JobRunStatusTimeout
				if name == "active-cancel" {
					status = model.JobRunStatusCancelled
				}
			}
			job, _ := f.jobs.GetByJobRunID(f.ctx, "job")
			if err := f.jobs.FinalizeRetryJobIfCurrent(f.ctx, job, map[string]any{"status": status, "end_time": time.Now(), "error_message": "fixture stopped"}); err != nil {
				t.Fatal(err)
			}
			after, _ := f.jobs.GetByJobRunID(f.ctx, "job")
			if after.Status != status || after.ClaimID != job.ClaimID || after.TerminalReportDigest != "" || after.RetryPlanDigest != job.RetryPlanDigest {
				t.Fatal("controller finalization invented or changed authority")
			}
			var unfinished int64
			f.db.Model(&model.StepRun{}).Where("job_run_id = ? AND status < ?", "job", 4).Count(&unfinished)
			if unfinished != 0 {
				t.Fatal("controller left unused attempts active")
			}
			var receipts int64
			f.db.Model(&model.StepRun{}).Where("job_run_id = ? AND terminal_report_digest <> ?", "job", "").Count(&receipts)
			if receipts != 0 {
				t.Fatal("controller invented terminal receipt")
			}
			if after.CurrentGenerationID != "" {
				var generation model.JobRunGeneration
				f.db.Where("generation_id = ?", after.CurrentGenerationID).First(&generation)
				if generation.State != "aborted" || generation.ClosureDigest != "" {
					t.Fatal("finalization invented cleanup transition")
				}
			}
		})
	}
}

func TestGenerationControllerFinalizationRejectsSuccessStaleOrAuthorityFields(t *testing.T) {
	f := newGenerationFixture(t)
	job, _ := f.jobs.GetByJobRunID(f.ctx, "job")
	f.admission(t)
	for _, updates := range []map[string]any{{"status": 4, "end_time": time.Now()}, {"status": 5, "end_time": time.Now(), "claim_id": "changed"}, {"status": 5, "end_time": time.Now(), "terminal_report_digest": strings.Repeat("a", 64)}, {"status": 7, "end_time": time.Now()}} {
		if err := f.jobs.FinalizeRetryJobIfCurrent(f.ctx, job, updates); err == nil {
			t.Fatal("unsafe or stale controller finalization accepted")
		}
	}
}

func TestGenerationExplicitOfflineRecoveryNeverResumesOrInventsEvidence(t *testing.T) {
	f := newGenerationFixture(t)
	g := f.admission(t)
	f.pin(t)
	f.failedRound(t, g)
	next, err := f.jobs.ReconcileJobGeneration(f.ctx, f.advance(t, g))
	if err != nil {
		t.Fatal(err)
	}
	summary, err := RecoverInterruptedRuns(f.ctx, f.db, []string{"run"}, true)
	if err != nil || summary.Jobs != 1 {
		t.Fatalf("offline recovery: %+v %v", summary, err)
	}
	inspect := f.admit
	inspect.Operation = "inspect"
	inspect.RequestDigest, _ = inspect.Digest()
	observed, err := f.jobs.ReconcileJobGeneration(f.ctx, inspect)
	if err != nil || observed.ExecutionAllowed || observed.GenerationID != next.GenerationID {
		t.Fatalf("recovery resumed: %+v %v", observed, err)
	}
	var current model.JobRunGeneration
	f.db.Where("generation_id = ?", next.GenerationID).First(&current)
	if current.State != "aborted" || current.ClosureDigest != "" {
		t.Fatal("recovery fabricated cleanup")
	}
	old, err := f.steps.GetByStepRunID(f.ctx, "first-1")
	if err != nil || old.TerminalReportDigest != strings.Repeat("5", 64) {
		t.Fatal("recovery erased committed receipt")
	}
}

func TestGenerationRejectsSuccessfulHistoricalArtifactAndGenericPublication(t *testing.T) {
	f := newGenerationFixture(t)
	first := f.admission(t)
	f.pin(t)
	for _, report := range []struct {
		id       string
		status   int
		eligible bool
	}{{"first-1", 3, false}, {"first-1", 4, false}, {"second-1", 3, false}, {"second-1", 5, true}} {
		if err := f.report(t, first, report.id, report.status, report.eligible); err != nil {
			t.Fatal(err)
		}
	}
	request := f.advance(t, first)
	request.FailedStepRunID = "second-1"
	request.RequestDigest, _ = request.Digest()
	second, err := f.jobs.ReconcileJobGeneration(f.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	old, err := f.steps.GetByStepRunID(f.ctx, "first-1")
	if err != nil || old.Status != 4 {
		t.Fatal("fixture needs successful old attempt")
	}
	hash := strings.Repeat("b", 64)
	key, err := artifactutil.RemotePath("run", "job", "first-1", hash, "out.txt")
	if err != nil {
		t.Fatal(err)
	}
	artifact := &model.ProjectArtifact{ProjectID: "project", PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", StepRunID: "first-1", Name: "out.txt", StorageType: "s3", StorageID: "storage", StorageFingerprint: strings.Repeat("a", 64), StoragePath: key, SHA256: hash, Size: 1}
	for _, ctx := range []context.Context{f.ctx, agentidentity.WithGenerationReport(f.ctx, first.GenerationID, first.BindingDigest), agentidentity.WithGenerationReport(f.ctx, second.GenerationID, second.BindingDigest)} {
		if err := NewArtifactRepo(statusTestDB{f.db}).Upsert(ctx, artifact); err == nil {
			t.Fatal("generic or successful historical publication accepted")
		}
	}
	var count int64
	f.db.Model(&model.ProjectArtifact{}).Count(&count)
	if count != 0 {
		t.Fatal("rejected old output was persisted")
	}
}

func TestGenerationAttemptDeletionPreservesQueuedAndTerminalEvidence(t *testing.T) {
	f := newGenerationFixture(t)
	if err := f.steps.DeleteByStepRunID(f.ctx, "first-2"); err == nil {
		t.Fatal("queued captured attempt was deleted")
	}
	g := f.admission(t)
	f.pin(t)
	f.failedRound(t, g)
	before, err := f.steps.GetByStepRunID(f.ctx, "first-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.steps.DeleteByStepRunID(f.ctx, "first-1"); err == nil {
		t.Fatal("committed prior receipt was deleted")
	}
	after, err := f.steps.GetByStepRunID(f.ctx, "first-1")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("delete changed terminal evidence")
	}
	if _, err := f.jobs.ReconcileJobGeneration(f.ctx, f.advance(t, g)); err != nil {
		t.Fatalf("rejected delete broke advancement: %v", err)
	}
	// 任意一半固定计划证据仍须保留，不能因损坏计划文本而开放删除
	if err := f.db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("retry_plan", "").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.steps.DeleteByStepRunID(f.ctx, "second-3"); err == nil {
		t.Fatal("digest-only captured attempt was deleted")
	}
	var count int64
	f.db.Model(&model.StepRun{}).Where("job_run_id = ?", "job").Count(&count)
	if count != 6 {
		t.Fatalf("retained %d attempts", count)
	}
}

func TestGenerationDeletionGuardPreservesLegacyDeleteBehavior(t *testing.T) {
	ctx, db, _, _ := sourceTestRepository(t)
	steps := &StepRunRepo{IDatabase: statusTestDB{db}}
	for _, id := range []string{"legacy-queued", "legacy-terminal"} {
		if err := db.WithContext(ctx).Create(&model.StepRun{StepRunID: id, JobRunID: "job", Status: 4}).Error; err != nil {
			t.Fatal(err)
		}
		if err := steps.DeleteByStepRunID(ctx, id); err != nil {
			t.Fatal(err)
		}
		remaining, err := steps.GetByStepRunID(ctx, id)
		if err != nil || remaining != nil {
			t.Fatal("uncaptured legacy attempt was not deleted")
		}
	}
	if err := steps.DeleteByStepRunID(ctx, "already-absent"); err != nil {
		t.Fatal("idempotent missing delete changed", err)
	}
}

func TestGenerationFreshCancellationReceiptsRequireCurrentProof(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(fmt.Sprintf("started-%t", started), func(t *testing.T) {
			f := newGenerationFixture(t)
			first := f.admission(t)
			f.pin(t)
			f.failedRound(t, first)
			current, err := f.jobs.ReconcileJobGeneration(f.ctx, f.advance(t, first))
			if err != nil {
				t.Fatal(err)
			}
			if started {
				if err := f.report(t, current, "first-2", 3, false); err != nil {
					t.Fatal(err)
				}
			}
			job, _ := f.jobs.GetByJobRunID(f.ctx, "job")
			if err := f.jobs.FinalizeRetryJobIfCurrent(f.ctx, job, map[string]any{"status": 6, "end_time": time.Now()}); err != nil {
				t.Fatal(err)
			}
			job, _ = f.jobs.GetByJobRunID(f.ctx, "job")
			for _, proof := range []agentidentity.GenerationReport{{}, {ID: first.GenerationID, BindingDigest: first.BindingDigest}, {ID: "foreign", BindingDigest: "v1:" + strings.Repeat("e", 64)}} {
				ctx := agentidentity.WithGenerationReport(f.ctx, proof.ID, proof.BindingDigest)
				if err := f.jobs.CommitCancellationReceipt(ctx, job, "claim", strings.Repeat("e", 64)); err == nil {
					t.Fatal("wrong fresh job cancellation proof accepted")
				}
				for _, id := range []string{"first-2", "first-3"} {
					step, _ := f.steps.GetByStepRunID(f.ctx, id)
					if err := f.steps.CommitCancellationReceipt(ctx, step, agentidentity.StepClaimID("claim", id), strings.Repeat("e", 64)); err == nil {
						t.Fatal("wrong fresh step cancellation proof accepted")
					}
				}
			}
			ctx := agentidentity.WithGenerationReport(f.ctx, current.GenerationID, current.BindingDigest)
			for _, id := range []string{"first-2", "first-3"} {
				step, _ := f.steps.GetByStepRunID(f.ctx, id)
				if step.TerminalReportDigest != "" {
					t.Fatal("rejected proof poisoned receipt")
				}
				if err := f.steps.CommitCancellationReceipt(ctx, step, agentidentity.StepClaimID("claim", id), strings.Repeat("c", 64)); err != nil {
					t.Fatal(err)
				}
			}
			future, _ := f.steps.GetByStepRunID(f.ctx, "first-3")
			if future.ClaimID != "" || future.GenerationID != "" {
				t.Fatal("unused cancellation invented execution")
			}
			if err := f.jobs.CommitCancellationReceipt(ctx, job, "claim", strings.Repeat("c", 64)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGenerationPreAdmissionCancellationRequiresEmptyProof(t *testing.T) {
	f := newGenerationFixture(t)
	job, _ := f.jobs.GetByJobRunID(f.ctx, "job")
	if err := f.jobs.FinalizeRetryJobIfCurrent(f.ctx, job, map[string]any{"status": 6, "end_time": time.Now()}); err != nil {
		t.Fatal(err)
	}
	job, _ = f.jobs.GetByJobRunID(f.ctx, "job")
	step, _ := f.steps.GetByStepRunID(f.ctx, "first-1")
	wrong := agentidentity.WithGenerationReport(f.ctx, "foreign", "v1:"+strings.Repeat("e", 64))
	if err := f.jobs.CommitCancellationReceipt(wrong, job, "claim", strings.Repeat("e", 64)); err == nil {
		t.Fatal("pre-admission job acquired generation")
	}
	if err := f.steps.CommitCancellationReceipt(wrong, step, agentidentity.StepClaimID("claim", "first-1"), strings.Repeat("e", 64)); err == nil {
		t.Fatal("pre-admission step acquired generation")
	}
	if err := f.steps.CommitCancellationReceipt(f.ctx, step, agentidentity.StepClaimID("claim", "first-1"), strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	if err := f.jobs.CommitCancellationReceipt(f.ctx, job, "claim", strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
}

func TestGenerationCancellationBeforeHandoffRejectsAdvanceAndKeepsCurrentReceiptProof(t *testing.T) {
	f := newGenerationFixture(t)
	first := f.admission(t)
	f.pin(t)
	f.failedRound(t, first)
	advance := f.advance(t, first)
	job, err := f.jobs.GetByJobRunID(f.ctx, "job")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.jobs.FinalizeRetryJobIfCurrent(f.ctx, job, map[string]any{"status": 6, "end_time": time.Now()}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := f.jobs.ReconcileJobGeneration(f.ctx, advance); err == nil {
			t.Fatal("cancelled predecessor admitted successor")
		}
	}
	job, _ = f.jobs.GetByJobRunID(f.ctx, "job")
	if job.CurrentGeneration != 1 || job.CurrentGenerationID != first.GenerationID || job.ClaimID != "claim" {
		t.Fatal("cancelled generation identity changed")
	}
	if err := f.jobs.CommitCancellationReceipt(f.ctx, job, "claim", strings.Repeat("e", 64)); err == nil {
		t.Fatal("empty first cancellation proof accepted")
	}
	ctx := agentidentity.WithGenerationReport(f.ctx, first.GenerationID, first.BindingDigest)
	if err := f.jobs.CommitCancellationReceipt(ctx, job, "claim", strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := f.db.WithContext(f.ctx).Model(&model.JobRunGeneration{}).Where("job_run_id = ?", "job").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("cancelled generation count: %d %v", count, err)
	}
}
