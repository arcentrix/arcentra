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
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type stepAttemptFixture struct {
	*generationFixture
	generation *taskqueue.JobGeneration
	session    taskqueue.StepAttemptSession
}

func newStepAttemptFixture(t *testing.T, delay string) *stepAttemptFixture {
	return newStepAttemptFixtureConfigured(t, delay, nil)
}
func newStepAttemptFixtureConfigured(t *testing.T, delay string, configure func(*generationFixture)) *stepAttemptFixture {
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
	return populateStepAttemptFixtureForJobConfigured(t, ctx, db, jobs, delay, "job", configure)
}
func populateStepAttemptFixture(t *testing.T, ctx context.Context, db *gorm.DB, jobs *JobRunRepo, delay string) *stepAttemptFixture {
	t.Helper()
	return populateStepAttemptFixtureForJob(t, ctx, db, jobs, delay, "job")
}

func populateStepAttemptFixtureForJob(t *testing.T, ctx context.Context, db *gorm.DB, jobs *JobRunRepo, delay, jobID string) *stepAttemptFixture {
	return populateStepAttemptFixtureForJobConfigured(t, ctx, db, jobs, delay, jobID, nil)
}
func populateStepAttemptFixtureForJobConfigured(t *testing.T, ctx context.Context, db *gorm.DB, jobs *JobRunRepo, delay, jobID string, configure func(*generationFixture)) *stepAttemptFixture {
	t.Helper()
	if err := jobs.UpdateByJobRunID(ctx, jobID, map[string]any{"artifact_storage_id": "storage", "artifact_storage_fingerprint": strings.Repeat("a", 64), "artifact_storage_type": "s3"}); err != nil {
		t.Fatal(err)
	}
	runtime := repoRuntimeRequest(t)
	runtimeDigest, err := taskqueue.RuntimeBindingDigest("pipeline", "run", jobID, "agent", runtime)
	if err != nil {
		t.Fatal(err)
	}
	runtime.BindingDigest = runtimeDigest
	policy := &pipelinev1.Retry{Mode: "failed_step", MaxAttempts: 3, Idempotent: true, AllowedExitCodes: []int32{9}, Delay: delay}
	policyJSON, _ := sonic.MarshalString(policy)
	payload := &taskqueue.JobRunTaskPayload{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: jobID, AgentID: "agent", Runtime: runtime, Retry: policy}
	for index, name := range []string{"first", "second", "upload"} {
		uses, action := "shell", ""
		if name == "upload" {
			uses, action = "builtin:artifacts", "upload"
		}
		prefix := ""
		if jobID != "job" {
			prefix = jobID + "-"
		}
		ids := []string{prefix + name + "-1", prefix + name + "-2", prefix + name + "-3"}
		payload.Steps = append(payload.Steps, taskqueue.StepPayload{Name: name, StepRunID: ids[0], AttemptRunIDs: ids, StepIndex: int32(index), Uses: uses, Action: action})
		for attempt, id := range ids {
			previous := ""
			if attempt > 0 {
				previous = ids[attempt-1]
			}
			row := &model.StepRun{StepRunID: id, Name: name, StepIndex: index, JobRunID: jobID, PipelineID: "pipeline", PipelineRunID: "run", Attempt: attempt + 1, RetryPolicy: policyJSON, RetryMode: "failed_step", PreviousStepRunID: previous, Uses: uses, Action: action, Status: 1}
			if err := (&StepRunRepo{IDatabase: statusTestDB{db}}).Create(ctx, row); err != nil {
				t.Fatal(err)
			}
		}
	}
	plan, err := taskqueue.NewJobRetryPlan(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.CaptureRuntimeRetryExpectation(ctx, "pipeline", "run", jobID, "agent", nil, runtime, nil, nil, plan); err != nil {
		t.Fatal(err)
	}
	job, _ := jobs.GetByJobRunID(ctx, jobID)
	if err := jobs.PatchReportedStatus(ctx, job, map[string]any{"status": 3, "claim_id": "claim", "start_time": time.Now()}); err != nil {
		t.Fatal(err)
	}
	job, _ = jobs.GetByJobRunID(ctx, jobID)
	identity := generationIdentity(job)
	manifest, _ := taskqueue.NewJobInputManifest("", nil, nil)
	initial := taskqueue.JobGenerationTransition{Version: 1, Identity: identity, RetryPlanDigest: job.RetryPlanDigest, Operation: "admit"}
	initial.RequestDigest, _ = initial.Digest()
	base := &generationFixture{ctx: ctx, db: db, jobs: jobs, steps: &StepRunRepo{IDatabase: statusTestDB{db}}, identity: identity, manifest: manifest, admit: initial, plan: plan}
	if configure != nil {
		configure(base)
	}
	generation := base.admission(t)
	base.pin(t)
	runtimeID := generation.RuntimeClaimID
	return &stepAttemptFixture{generationFixture: base, generation: generation, session: taskqueue.StepAttemptSession{SessionID: runtimeID, SessionIncarnationNonce: uuid.NewString(), Namespace: "fixture", ContainerID: "arcentra-" + runtimeID, SnapshotID: "arcentra-" + runtimeID + "-snapshot", Snapshotter: "overlayfs", ResourceOwnerDigest: strings.Repeat("a", 64), WorkspaceID: "job-" + runtimeID, WorkspaceDevice: 1, WorkspaceInode: 2}}
}
func (f *stepAttemptFixture) request(t *testing.T, index int) taskqueue.StepAttemptTransition {
	t.Helper()
	job, _ := f.jobs.GetByJobRunID(f.ctx, f.identity.JobRunID)
	step := f.plan.Steps[index]
	g := f.generation
	scope := taskqueue.StepAttemptIdentity{Identity: f.identity, GenerationID: g.GenerationID, GenerationBindingDigest: g.BindingDigest, RuntimeClaimID: g.RuntimeClaimID, InputManifestDigest: job.InputManifestDigest, RetryPlanDigest: job.RetryPlanDigest, Session: f.session, LogicalStepRunID: step.AttemptRunIDs[0], StepRunID: step.AttemptRunIDs[0], StepName: step.Name, StepIndex: step.StepIndex, DefinitionDigest: step.DefinitionDigest, Attempt: 1, StepClaimID: agentidentity.StepClaimID(f.identity.ClaimID, step.AttemptRunIDs[0])}
	request := taskqueue.StepAttemptTransition{Version: 1, Operation: "admit", Scope: scope}
	request.RequestDigest, _ = request.Digest()
	return request
}
func (f *stepAttemptFixture) admitStep(t *testing.T, index int) *taskqueue.StepAttemptAdmission {
	t.Helper()
	result, err := f.jobs.ReconcileStepAttempt(f.ctx, f.request(t, index))
	if err != nil || !result.ExecutionAllowed {
		t.Fatalf("step admission: %+v %v", result, err)
	}
	return result.Admission
}
func (f *stepAttemptFixture) reportStep(t *testing.T, id string, a *taskqueue.StepAttemptAdmission, next int, eligible bool) error {
	t.Helper()
	step, err := f.steps.GetByStepRunID(f.ctx, id)
	if err != nil {
		return err
	}
	binding := ""
	if a != nil {
		binding = a.BindingDigest
	}
	ctx := agentidentity.WithStepAttemptReport(agentidentity.WithGenerationReport(f.ctx, f.generation.GenerationID, f.generation.BindingDigest), binding)
	patch := map[string]any{"status": next, "claim_id": agentidentity.StepClaimID(f.identity.ClaimID, id), "generation_id": f.generation.GenerationID, "generation_binding_digest": f.generation.BindingDigest, "retryable_shell_exit": eligible, "exit_code": int32(0)}
	if next == model.JobRunStatusFailed {
		patch["exit_code"] = int32(9)
	}
	if next >= 4 {
		patch["terminal_report_digest"] = strings.Repeat(fmt.Sprintf("%x", next), 64)
	}
	return f.steps.PatchReportedStatus(ctx, step, patch)
}
func (f *stepAttemptFixture) finishStep(t *testing.T, a *taskqueue.StepAttemptAdmission, status int) {
	t.Helper()
	if err := f.reportStep(t, a.Scope.StepRunID, a, 3, false); err != nil {
		t.Fatal(err)
	}
	if err := f.reportStep(t, a.Scope.StepRunID, a, status, status == 5); err != nil {
		t.Fatal(err)
	}
}
func (f *stepAttemptFixture) nextRequest(t *testing.T, a *taskqueue.StepAttemptAdmission, sequence uint64) taskqueue.StepAttemptTransition {
	t.Helper()
	now := time.Now()
	step, _ := f.steps.GetByStepRunID(f.ctx, a.Scope.StepRunID)
	boundary := &taskqueue.PausedStepBoundary{Version: 1, Scope: a.Scope, BoundaryNonce: uuid.NewString(), BoundarySequence: sequence, FailedAdmissionID: a.AdmissionID, FailedAdmissionBindingDigest: a.BindingDigest, FailedStepReceiptDigest: step.TerminalReportDigest, CapturedUnixMilli: now.UnixMilli(), ExpiresUnixMilli: now.Add(taskqueue.PausedStepBoundaryLifetime).UnixMilli(), FailedExecExitObserved: true, ExecReaped: true, TaskPaused: true, ExactLiveInitOnly: true, Childless: true}
	scope := a.Scope
	scope.Attempt++
	scope.StepRunID = f.plan.Steps[scope.StepIndex].AttemptRunIDs[scope.Attempt-1]
	scope.StepClaimID = agentidentity.StepClaimID(f.identity.ClaimID, scope.StepRunID)
	request := taskqueue.StepAttemptTransition{Version: 1, Operation: "advance", Scope: scope, PreviousStepRunID: a.Scope.StepRunID, PreviousStepClaimID: a.Scope.StepClaimID, PreviousAdmissionID: a.AdmissionID, PreviousAdmissionBindingDigest: a.BindingDigest, FailedStepReceiptDigest: step.TerminalReportDigest, Boundary: boundary}
	request.RequestDigest, _ = request.Digest()
	return request
}
func TestStepAttemptFixedGenerationAndCurrentCursor(t *testing.T) {
	f := newStepAttemptFixture(t, "")
	first := f.admitStep(t, 0)
	if err := f.reportStep(t, "first-1", nil, 3, false); err == nil {
		t.Fatal("initial command lacked admission")
	}
	f.finishStep(t, first, 5)
	nextRequest := f.nextRequest(t, first, 1)
	result, err := f.jobs.ReconcileStepAttempt(f.ctx, nextRequest)
	if err != nil || !result.ExecutionAllowed {
		t.Fatalf("advance: %+v %v", result, err)
	}
	second := result.Admission
	if second.Scope.GenerationID != first.Scope.GenerationID || second.Scope.Session != first.Scope.Session || second.Scope.RuntimeClaimID != first.Scope.RuntimeClaimID || second.Scope.Attempt != 2 {
		t.Fatal("retry replaced original session")
	}
	if err := f.reportStep(t, "first-1", first, 3, false); err == nil {
		t.Fatal("old attempt regained RUNNING")
	}
	if err := f.reportStep(t, "first-2", first, 3, false); err == nil {
		t.Fatal("old admission started successor")
	}
	replay, err := f.jobs.ReconcileStepAttempt(f.ctx, nextRequest)
	if err != nil || !reflect.DeepEqual(replay.Admission, second) {
		t.Fatal("lost ACK minted a different admission")
	}
	historical, err := f.jobs.ReconcileStepAttempt(f.ctx, f.request(t, 0))
	if err != nil || historical.ExecutionAllowed || !reflect.DeepEqual(historical.Admission, first) {
		t.Fatal("historical admission lost identity or regained permission")
	}
	inspect := f.request(t, 0)
	inspect.Operation = "inspect"
	inspect.Scope = second.Scope
	inspect.RequestDigest, _ = inspect.Digest()
	observed, err := f.jobs.ReconcileStepAttempt(f.ctx, inspect)
	if err != nil || observed.ExecutionAllowed || !reflect.DeepEqual(observed.Admission, second) {
		t.Fatal("inspect was executable or mutated")
	}
	f.finishStep(t, second, 4)
	if err := f.reportStep(t, "first-3", nil, 8, false); err != nil {
		t.Fatal(err)
	}
	other := f.admitStep(t, 1)
	f.finishStep(t, other, 4)
	if job, _ := f.jobs.GetByJobRunID(f.ctx, "job"); job.CurrentGeneration != 1 || job.CurrentGenerationID != first.Scope.GenerationID {
		t.Fatal("failed_step advanced job generation")
	}
}
func TestStepAttemptRequiresExactAdmissionOrderingAndRunningSuccess(t *testing.T) {
	f := newStepAttemptFixture(t, "")
	if _, err := f.jobs.ReconcileStepAttempt(f.ctx, f.request(t, 1)); err == nil {
		t.Fatal("later logical step bypassed initial admission")
	}
	for _, next := range []int{3, 4, 5} {
		if err := f.reportStep(t, "first-1", nil, next, false); err == nil {
			t.Fatalf("unadmitted status %d accepted", next)
		}
	}
	first := f.admitStep(t, 0)
	if err := f.reportStep(t, "first-1", first, 4, false); err == nil {
		t.Fatal("SUCCESS without RUNNING accepted")
	}
	if _, err := f.jobs.ReconcileStepAttempt(f.ctx, f.request(t, 1)); err == nil {
		t.Fatal("overlapping logical admission accepted")
	}
	if err := f.reportStep(t, "first-1", first, 8, false); err != nil {
		t.Fatal(err)
	}
	second := f.admitStep(t, 1)
	f.finishStep(t, second, 4)
	if err := f.reportStep(t, "second-3", nil, 8, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.jobs.ReconcileStepAttempt(f.ctx, f.nextRequest(t, second, 1)); err == nil {
		t.Fatal("successful predecessor retried")
	}
}
func TestStepAttemptRejectsChangedCASAndBrokenLedger(t *testing.T) {
	for name, mutate := range map[string]func(*stepAttemptFixture, *taskqueue.StepAttemptTransition){
		"receipt": func(f *stepAttemptFixture, r *taskqueue.StepAttemptTransition) {
			r.FailedStepReceiptDigest = strings.Repeat("a", 64)
			r.Boundary.FailedStepReceiptDigest = r.FailedStepReceiptDigest
		},
		"future": func(f *stepAttemptFixture, r *taskqueue.StepAttemptTransition) {
			r.Scope.Attempt = 3
			r.Scope.StepRunID = "first-3"
			r.Scope.StepClaimID = agentidentity.StepClaimID("claim", "first-3")
			r.Boundary.Scope.Attempt = 2
		},
		"session": func(f *stepAttemptFixture, r *taskqueue.StepAttemptTransition) {
			r.Scope.Session.WorkspaceInode++
			r.Boundary.Scope.Session = r.Scope.Session
		},
		"futureBoundary": func(f *stepAttemptFixture, r *taskqueue.StepAttemptTransition) {
			r.Boundary.CapturedUnixMilli = time.Now().Add(time.Minute).UnixMilli()
			r.Boundary.ExpiresUnixMilli = r.Boundary.CapturedUnixMilli + time.Minute.Milliseconds()
		},
		"staleBoundary": func(f *stepAttemptFixture, r *taskqueue.StepAttemptTransition) {
			r.Boundary.CapturedUnixMilli = time.Now().Add(-2 * time.Minute).UnixMilli()
			r.Boundary.ExpiresUnixMilli = time.Now().Add(-time.Minute).UnixMilli()
		},
		"claimedSuccessor": func(f *stepAttemptFixture, r *taskqueue.StepAttemptTransition) {
			f.db.Model(&model.StepRun{}).Where("step_run_id = ?", "first-2").Update("claim_id", "foreign")
		},
		"missingLedger": func(f *stepAttemptFixture, r *taskqueue.StepAttemptTransition) {
			f.db.Model(&model.StepRun{}).Where("step_run_id = ?", "first-1").Update("attempt_admission_request", "")
		},
		"notPure": func(f *stepAttemptFixture, r *taskqueue.StepAttemptTransition) {
			f.db.Model(&model.StepRun{}).Where("step_run_id = ?", "first-1").Update("retryable_shell_exit", false)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newStepAttemptFixture(t, "")
			first := f.admitStep(t, 0)
			f.finishStep(t, first, 5)
			request := f.nextRequest(t, first, 1)
			mutate(f, &request)
			request.RequestDigest, _ = request.Digest()
			if _, err := f.jobs.ReconcileStepAttempt(f.ctx, request); err == nil {
				t.Fatal("invalid advancement accepted")
			}
		})
	}
	f := newStepAttemptFixture(t, "")
	first := f.admitStep(t, 0)
	f.finishStep(t, first, 5)
	request := f.nextRequest(t, first, 1)
	if _, err := f.jobs.ReconcileStepAttempt(f.ctx, request); err != nil {
		t.Fatal(err)
	}
	request.Boundary.BoundaryNonce = uuid.NewString()
	request.RequestDigest, _ = request.Digest()
	if _, err := f.jobs.ReconcileStepAttempt(f.ctx, request); err == nil {
		t.Fatal("changed payload reused committed successor")
	}
	whole := f.advance(t, f.generation)
	if _, err := f.jobs.ReconcileJobGeneration(f.ctx, whole); err == nil {
		t.Fatal("failed_step advanced whole-job generation")
	}
}
func TestStepAttemptDelayExpiryAndNonexecutionClosure(t *testing.T) {
	f := newStepAttemptFixture(t, "40ms")
	first := f.admitStep(t, 0)
	f.finishStep(t, first, 5)
	request := f.nextRequest(t, first, 1)
	next, err := f.jobs.ReconcileStepAttempt(f.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.reportStep(t, "first-2", next.Admission, 3, false); err == nil {
		t.Fatal("delay bypassed")
	}
	time.Sleep(50 * time.Millisecond)
	if err := f.reportStep(t, "first-2", next.Admission, 3, false); err != nil {
		t.Fatal(err)
	}
	step, _ := f.steps.GetByStepRunID(f.ctx, "first-2")
	ctx := agentidentity.WithStepAttemptReport(agentidentity.WithGenerationReport(f.ctx, f.generation.GenerationID, f.generation.BindingDigest), next.Admission.BindingDigest)
	if err := f.steps.ValidateRetryActivation(ctx, step); err != nil {
		t.Fatal(err)
	}
	f.db.Model(&model.Agent{}).Where("agent_id = ?", "agent").Update("is_enabled", 0)
	if err := f.steps.ValidateRetryActivation(ctx, step); err == nil {
		t.Fatal("same-claim RUNNING reACK ignored disabled Agent")
	}
	f2 := newStepAttemptFixture(t, "")
	a := f2.admitStep(t, 0)
	// Fixture corruption only shortens a signed lifetime, preserving request and scope, to test expired observation.
	expired := *a
	expired.NotBeforeUnixMilli = time.Now().Add(-2 * time.Minute).UnixMilli()
	expired.ExpiresUnixMilli = time.Now().Add(-time.Minute).UnixMilli()
	expired.BindingDigest, _ = expired.Digest()
	body, _ := expired.Canonical()
	f2.db.Model(&model.StepRun{}).Where("step_run_id = ?", "first-1").Update("attempt_admission", body)
	observed, err := f2.jobs.ReconcileStepAttempt(f2.ctx, f2.request(t, 0))
	if err != nil || observed.ExecutionAllowed {
		t.Fatal("expired replay renewed execution")
	}
	if err := f2.reportStep(t, "first-1", &expired, 3, false); err == nil {
		t.Fatal("expired RUNNING accepted")
	}
	if err := f2.reportStep(t, "first-1", &expired, 5, false); err != nil {
		t.Fatal("admitted unstarted expiration could not close nonretryably", err)
	}
}
func TestStepAttemptCancellationAndProtectedColumns(t *testing.T) {
	f := newStepAttemptFixture(t, "")
	a := f.admitStep(t, 0)
	for _, field := range []string{"attempt_admission_request", "AttemptAdmissionRequestDigest", "attemptAdmission"} {
		if err := f.steps.PatchByStepRunID(f.ctx, "first-1", map[string]any{field: "replace"}); err == nil {
			t.Fatal("generic ledger mutation accepted")
		}
	}
	if err := f.steps.Create(f.ctx, &model.StepRun{StepRunID: "injected", AttemptAdmission: "injected"}); err == nil {
		t.Fatal("generic create injected admission")
	}
	if err := f.steps.DeleteByStepRunID(f.ctx, "first-1"); err == nil {
		t.Fatal("admission deleted")
	}
	if err := f.jobs.CancelAgentJobIfCurrent(f.ctx, "job", 3, map[string]any{"status": 6}); err != nil {
		t.Fatal(err)
	}
	observed, err := f.jobs.ReconcileStepAttempt(f.ctx, f.request(t, 0))
	if err != nil || observed.ExecutionAllowed || !reflect.DeepEqual(observed.Admission, a) {
		t.Fatal("cancellation destroyed historical admission or renewed permission")
	}
	if err := f.reportStep(t, "first-1", a, 3, false); err == nil {
		t.Fatal("cancelled admission regained RUNNING")
	}
	step, _ := f.steps.GetByStepRunID(f.ctx, "first-1")
	ctx := agentidentity.WithStepAttemptReport(agentidentity.WithGenerationReport(f.ctx, f.generation.GenerationID, f.generation.BindingDigest), a.BindingDigest)
	if err := f.steps.CommitCancellationReceipt(ctx, step, a.Scope.StepClaimID, strings.Repeat("6", 64)); err != nil {
		t.Fatal(err)
	}
	unused, _ := f.steps.GetByStepRunID(f.ctx, "first-3")
	ctx = agentidentity.WithGenerationReport(f.ctx, f.generation.GenerationID, f.generation.BindingDigest)
	if err := f.steps.CommitCancellationReceipt(ctx, unused, agentidentity.StepClaimID("claim", "first-3"), strings.Repeat("6", 64)); err != nil {
		t.Fatal(err)
	}
}
func TestStepAttemptUploadWinnerIgnoresFutureSkipped(t *testing.T) {
	f := newStepAttemptFixture(t, "")
	a := f.admitStep(t, 0)
	f.finishStep(t, a, 5)
	request := f.nextRequest(t, a, 1)
	next, err := f.jobs.ReconcileStepAttempt(f.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	f.finishStep(t, next.Admission, 4)
	second := f.admitStep(t, 1)
	f.finishStep(t, second, 4)
	upload := f.admitStep(t, 2)
	f.finishStep(t, upload, 4)
	for _, id := range []string{"first-3", "second-2", "second-3", "upload-2", "upload-3"} {
		if err := f.reportStep(t, id, nil, 8, false); err != nil {
			t.Fatal(err)
		}
	}
	job, _ := f.jobs.GetByJobRunID(f.ctx, "job")
	ctx := agentidentity.WithGenerationReport(f.ctx, f.generation.GenerationID, f.generation.BindingDigest)
	if err := validateGenerationArtifact(ctx, f.db, job, "upload-1"); err != nil {
		t.Fatal("valid admitted upload shadowed by unused higher slot", err)
	}
	for _, id := range []string{"first-1", "first-2", "upload-2", "upload-3"} {
		if err := validateGenerationArtifact(ctx, f.db, job, id); err == nil {
			t.Fatalf("non-upload/stale/future winner %s accepted", id)
		}
	}
	if err := f.jobs.PatchReportedStatusAndArtifacts(ctx, job, map[string]any{"status": 4, "claim_id": "claim", "terminal_report_digest": strings.Repeat("4", 64)}, nil); err != nil {
		t.Fatal(err)
	}
	replay, err := f.jobs.ReconcileStepAttempt(f.ctx, request)
	if err != nil || replay.ExecutionAllowed {
		t.Fatal("completed job revived admission")
	}
}
func TestStepAttemptConcurrentEqualAndConflictingRequests(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			f := newStepAttemptFixture(t, "")
			first := f.admitStep(t, 0)
			f.finishStep(t, first, 5)
			request := f.nextRequest(t, first, 1)
			other := request
			if conflict {
				b := *request.Boundary
				b.BoundaryNonce = uuid.NewString()
				other.Boundary = &b
				other.RequestDigest, _ = other.Digest()
			}
			var wg sync.WaitGroup
			results := make([]*taskqueue.StepAttemptResult, 2)
			errs := make([]error, 2)
			start := make(chan struct{})
			for i, r := range []taskqueue.StepAttemptTransition{request, other} {
				wg.Add(1)
				safe.Go(func() { defer wg.Done(); <-start; results[i], errs[i] = f.jobs.ReconcileStepAttempt(f.ctx, r) })
			}
			close(start)
			wg.Wait()
			successes := 0
			for _, err := range errs {
				if err == nil {
					successes++
				}
			}
			if conflict && successes != 1 {
				t.Fatalf("conflicting CAS winners=%d %v", successes, errs)
			}
			if !conflict && (successes != 2 || !reflect.DeepEqual(results[0].Admission, results[1].Admission)) {
				t.Fatalf("equal CAS did not preserve one admission: %v", errs)
			}
		})
	}
}

func TestStepAttemptCannotSkipRunningOrAdmittedRetry(t *testing.T) {
	f := newStepAttemptFixture(t, "")
	first := f.admitStep(t, 0)
	if err := f.reportStep(t, "first-1", first, 3, false); err != nil {
		t.Fatal(err)
	}
	if err := f.reportStep(t, "first-1", first, 8, false); err == nil {
		t.Fatal("RUNNING condition skip accepted")
	}
	if err := f.reportStep(t, "first-1", first, 5, true); err != nil {
		t.Fatal(err)
	}
	request := f.nextRequest(t, first, 1)
	next, err := f.jobs.ReconcileStepAttempt(f.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.reportStep(t, "first-2", next.Admission, 8, false); err == nil {
		t.Fatal("admitted retry skip accepted")
	}
	if _, err := f.jobs.ReconcileStepAttempt(f.ctx, f.request(t, 1)); err == nil {
		t.Fatal("admitted retry skip advanced logical step")
	}
	job, _ := f.jobs.GetByJobRunID(f.ctx, "job")
	ctx := agentidentity.WithGenerationReport(f.ctx, f.generation.GenerationID, f.generation.BindingDigest)
	if err := f.jobs.PatchReportedStatusAndArtifacts(ctx, job, map[string]any{"status": 4, "claim_id": "claim", "terminal_report_digest": strings.Repeat("4", 64)}, nil); err == nil {
		t.Fatal("admitted retry skip promoted final success")
	}
}

func TestStepAttemptBoundaryNonceIsOneUseAcrossRetries(t *testing.T) {
	f := newStepAttemptFixture(t, "")
	first := f.admitStep(t, 0)
	f.finishStep(t, first, 5)
	request := f.nextRequest(t, first, 1)
	second, err := f.jobs.ReconcileStepAttempt(f.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	f.finishStep(t, second.Admission, 5)
	third := f.nextRequest(t, second.Admission, 2)
	third.Boundary.BoundaryNonce = request.Boundary.BoundaryNonce
	third.RequestDigest, _ = third.Digest()
	if _, err := f.jobs.ReconcileStepAttempt(f.ctx, third); err == nil {
		t.Fatal("previous one-use boundary nonce accepted")
	}
	row, _ := f.steps.GetByStepRunID(f.ctx, "first-3")
	if !emptyStepAdmission(row) {
		t.Fatal("reused boundary poisoned durable admission chain")
	}
	third.Boundary.BoundaryNonce = uuid.NewString()
	third.RequestDigest, _ = third.Digest()
	if _, err := f.jobs.ReconcileStepAttempt(f.ctx, third); err != nil {
		t.Fatal(err)
	}
}
func TestStepAttemptAllConditionsSkippedPreservesPreparingGeneration(t *testing.T) {
	f := newStepAttemptFixture(t, "")
	for index := range f.plan.Steps {
		a := f.admitStep(t, index)
		if err := f.reportStep(t, a.Scope.StepRunID, a, 8, false); err != nil {
			t.Fatal(err)
		}
		for _, id := range f.plan.Steps[index].AttemptRunIDs[1:] {
			if err := f.reportStep(t, id, nil, 8, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	job, _ := f.jobs.GetByJobRunID(f.ctx, "job")
	ctx := agentidentity.WithGenerationReport(f.ctx, f.generation.GenerationID, f.generation.BindingDigest)
	if err := f.jobs.PatchReportedStatus(ctx, job, map[string]any{"status": 4, "claim_id": "claim", "terminal_report_digest": strings.Repeat("4", 64)}); err != nil {
		t.Fatal(err)
	}
	var record model.JobRunGeneration
	if err := f.db.Select("state").Where("generation_id = ?", f.generation.GenerationID).First(&record).Error; err != nil || record.State != "preparing" {
		t.Fatal("condition skips invented command execution")
	}
}

func TestStepAttemptExpiredStartWindowDoesNotInvalidateStartedResult(t *testing.T) {
	f := newStepAttemptFixture(t, "")
	first := f.admitStep(t, 0)
	short := *first
	short.ExpiresUnixMilli = time.Now().Add(250 * time.Millisecond).UnixMilli()
	short.BindingDigest, _ = short.Digest()
	body, _ := short.Canonical()
	if err := f.db.Model(&model.StepRun{}).Where("step_run_id = ?", "first-1").Update("attempt_admission", body).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.reportStep(t, "first-1", &short, 3, false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	row, _ := f.steps.GetByStepRunID(f.ctx, "first-1")
	ctx := agentidentity.WithStepAttemptReport(agentidentity.WithGenerationReport(f.ctx, f.generation.GenerationID, f.generation.BindingDigest), short.BindingDigest)
	if err := f.steps.ValidateRetryActivation(ctx, row); err == nil {
		t.Fatal("expired start window permitted fresh RUNNING reACK")
	}
	if err := f.reportStep(t, "first-1", &short, 4, false); err != nil {
		t.Fatal("expiry truncated an already-started current-admission result", err)
	}
}

func TestStepAttemptAdvanceAndCancellationSerialize(t *testing.T) {
	for _, finalize := range []bool{false, true} {
		t.Run(fmt.Sprint(finalize), func(t *testing.T) {
			f := newStepAttemptFixture(t, "")
			first := f.admitStep(t, 0)
			f.finishStep(t, first, 5)
			request := f.nextRequest(t, first, 1)
			job, _ := f.jobs.GetByJobRunID(f.ctx, "job")
			var admission *taskqueue.StepAttemptResult
			var casErr, closeErr error
			start := make(chan struct{})
			var wait sync.WaitGroup
			wait.Add(2)
			safe.Go(func() { defer wait.Done(); <-start; admission, casErr = f.jobs.ReconcileStepAttempt(f.ctx, request) })
			safe.Go(func() {
				defer wait.Done()
				<-start
				if finalize {
					closeErr = f.jobs.FinalizeRetryJobIfCurrent(f.ctx, job, map[string]any{"status": 5, "end_time": time.Now()})
				} else {
					closeErr = f.jobs.CancelAgentJobIfCurrent(f.ctx, "job", 3, map[string]any{"status": 6})
				}
			})
			close(start)
			wait.Wait()
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			old, _ := f.steps.GetByStepRunID(f.ctx, "first-1")
			if old.Status != 5 || old.TerminalReportDigest != request.FailedStepReceiptDigest {
				t.Fatal("cancellation overwrote predecessor failure")
			}
			if casErr == nil {
				observed, err := f.jobs.ReconcileStepAttempt(f.ctx, request)
				if err != nil || observed.ExecutionAllowed || !reflect.DeepEqual(observed.Admission, admission.Admission) {
					t.Fatal("post-cancel CAS regained permission")
				}
				if err := f.reportStep(t, "first-2", admission.Admission, 3, false); err == nil {
					t.Fatal("cancelled CAS started successor")
				}
			} else {
				row, _ := f.steps.GetByStepRunID(f.ctx, "first-2")
				if !emptyStepAdmission(row) {
					t.Fatal("rejected CAS partially wrote ledger")
				}
			}
		})
	}
}
func TestStepAttemptArtifactPublicationAndCancelWinningOrders(t *testing.T) {
	for _, cancelFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelFirst), func(t *testing.T) {
			f := newStepAttemptFixture(t, "")
			for index := range f.plan.Steps {
				a := f.admitStep(t, index)
				f.finishStep(t, a, 4)
				for _, id := range f.plan.Steps[index].AttemptRunIDs[1:] {
					if err := f.reportStep(t, id, nil, 8, false); err != nil {
						t.Fatal(err)
					}
				}
			}
			hash := strings.Repeat("b", 64)
			key, err := artifactutil.RemotePath("run", "job", "upload-1", hash, "out.txt")
			if err != nil {
				t.Fatal(err)
			}
			artifact := &model.ProjectArtifact{ProjectID: "project", PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", StepRunID: "upload-1", Name: "out.txt", StorageType: "s3", StorageID: "storage", StorageFingerprint: strings.Repeat("a", 64), StoragePath: key, SHA256: hash, Size: 1}
			job, _ := f.jobs.GetByJobRunID(f.ctx, "job")
			ctx := agentidentity.WithGenerationReport(f.ctx, f.generation.GenerationID, f.generation.BindingDigest)
			if err := NewArtifactRepo(statusTestDB{f.db}).Upsert(ctx, artifact); err == nil {
				t.Fatal("generic artifact shortcut bypassed final publication")
			}
			if cancelFirst {
				if err := f.jobs.CancelAgentJobIfCurrent(f.ctx, "job", 3, map[string]any{"status": 6}); err != nil {
					t.Fatal(err)
				}
			}
			err = f.jobs.PatchReportedStatusAndArtifacts(ctx, job, map[string]any{"status": 4, "claim_id": "claim", "terminal_report_digest": strings.Repeat("4", 64)}, []*model.ProjectArtifact{artifact})
			if cancelFirst && err == nil {
				t.Fatal("cancellation-first allowed publication")
			}
			if !cancelFirst && err != nil {
				t.Fatal(err)
			}
			if !cancelFirst {
				if err := f.jobs.CancelAgentJobIfCurrent(f.ctx, "job", 3, map[string]any{"status": 6}); err == nil {
					t.Fatal("late cancellation overwrote successful publication")
				}
			}
			var count int64
			f.db.Model(&model.ProjectArtifact{}).Count(&count)
			want := int64(1)
			if cancelFirst {
				want = 0
			}
			if count != want {
				t.Fatal("final artifact registration escaped transaction")
			}
		})
	}
}

func TestStepAttemptScopedFixturesKeepIndependentLedgers(t *testing.T) {
	first := newStepAttemptFixture(t, "")
	if err := first.jobs.Create(first.ctx, &model.JobRun{JobRunID: "other-job", PipelineID: "pipeline", PipelineRunID: "run", Status: 1}); err != nil {
		t.Fatal(err)
	}
	second := populateStepAttemptFixtureForJob(t, first.ctx, first.db, first.jobs, "", "other-job")
	if first.generation.GenerationID == second.generation.GenerationID || first.session.SessionID == second.session.SessionID {
		t.Fatal("independent fixtures shared a generation or runtime session")
	}
	for _, f := range []*stepAttemptFixture{first, second} {
		assertStepAttemptCASReportsAndPublication(t, f)
		for _, stepID := range f.plan.Steps[0].AttemptRunIDs {
			row, err := f.steps.GetByStepRunID(f.ctx, stepID)
			if err != nil || row == nil || row.JobRunID != f.identity.JobRunID {
				t.Fatalf("scoped fixture crossed stored step ownership: %v", err)
			}
		}
	}
	var count int64
	if err := first.db.WithContext(first.ctx).Model(&model.StepRun{}).Count(&count).Error; err != nil || count != 18 {
		t.Fatalf("independent fixtures did not retain both allocations: count=%d err=%v", count, err)
	}
}
