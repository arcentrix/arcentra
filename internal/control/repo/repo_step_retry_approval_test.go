package repo

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func gatedStepAttemptFixture(t *testing.T) *stepAttemptFixture {
	t.Helper()
	f := newStepAttemptFixtureConfigured(t, "", func(base *generationFixture) { configureGatedPinFixture(t, base) })
	installRetryApprovalTestAuthority(t, f.generationFixture, nil)
	return f
}
func approveStepStartup(t *testing.T, f *stepAttemptFixture) {
	t.Helper()
	approveRetryApproval(t, f.generationFixture)
	result, err := f.jobs.ReconcileJobRetryApproval(f.ctx, retryApprovalTestRequest(t, f.generationFixture, f.generation, "admit"))
	if err != nil || !result.ExecutionAllowed {
		t.Fatalf("startup: %+v %v", result, err)
	}
}
func TestStepRetryApprovalPendingFirstAndExactReview(t *testing.T) {
	f := gatedStepAttemptFixture(t)
	r, a := retryApprovalTestRow(t, f.generationFixture)
	review, err := r.ReadRetryApproval(f.ctx, a.ApprovalID)
	if err != nil || review.Evidence.Kind != retryapproval.FailedStepKind || review.Evidence.RetryPlan.Version != 2 {
		t.Fatalf("review: %+v %v", review, err)
	}
	if _, err := f.jobs.ReconcileStepAttempt(f.ctx, f.request(t, 0)); err == nil {
		t.Fatal("pending approval admitted first step")
	}
	approveRetryApproval(t, f.generationFixture)
	if _, err := f.jobs.ReconcileStepAttempt(f.ctx, f.request(t, 0)); err == nil {
		t.Fatal("missing immutable startup receipt admitted first step")
	}
	result, err := f.jobs.ReconcileJobRetryApproval(f.ctx, retryApprovalTestRequest(t, f.generationFixture, f.generation, "admit"))
	if err != nil || !result.ExecutionAllowed {
		t.Fatal(err)
	}
	admitted := f.admitStep(t, 0)
	if admitted.ExpiresUnixMilli > a.ExpiresAt.UnixMilli() {
		t.Fatal("step admission escaped approval expiry")
	}
}
func TestStepRetryApprovalRevocationPreservesLostACKAndDeniesRunning(t *testing.T) {
	for _, successor := range []bool{false, true} {
		t.Run(map[bool]string{false: "first", true: "successor"}[successor], func(t *testing.T) {
			f := gatedStepAttemptFixture(t)
			approveStepStartup(t, f)
			request := f.request(t, 0)
			first := f.admitStep(t, 0)
			if successor {
				f.finishStep(t, first, model.JobRunStatusFailed)
				request = f.nextRequest(t, first, 1)
			}
			before, err := f.jobs.ReconcileStepAttempt(f.ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := before.Admission.Canonical()
			if err := f.db.Model(&model.User{}).Where("user_id = ?", "voter").Update("is_enabled", 0).Error; err != nil {
				t.Fatal(err)
			}
			after, err := f.jobs.ReconcileStepAttempt(f.ctx, request)
			if err != nil || after.ExecutionAllowed {
				t.Fatalf("historical replay: %+v %v", after, err)
			}
			replay, _ := after.Admission.Canonical()
			if replay != body {
				t.Fatal("lost ACK bytes changed")
			}
			if err := f.reportStep(t, after.Admission.Scope.StepRunID, after.Admission, model.JobRunStatusRunning, false); err == nil {
				t.Fatal("revoked RUNNING allowed")
			}
			if err := f.reportStep(t, after.Admission.Scope.StepRunID, after.Admission, model.JobRunStatusFailed, false); err != nil {
				t.Fatalf("factual unstarted closure blocked: %v", err)
			}
		})
	}
}
func TestStepRetryApprovalRevocationBeforeSuccessor(t *testing.T) {
	f := gatedStepAttemptFixture(t)
	approveStepStartup(t, f)
	a := f.admitStep(t, 0)
	f.finishStep(t, a, model.JobRunStatusFailed)
	request := f.nextRequest(t, a, 1)
	if err := f.db.Model(&model.User{}).Where("user_id = ?", "actor").Update("is_enabled", 0).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.jobs.ReconcileStepAttempt(f.ctx, request); err == nil {
		t.Fatal("revoked requester admitted successor")
	}
	row, err := f.steps.GetByStepRunID(f.ctx, request.Scope.StepRunID)
	if err != nil || !emptyStepAdmission(row) {
		t.Fatal("denied successor left a receipt")
	}
}
func TestStepRetryApprovalPostWriteExpiryRollsBack(t *testing.T) {
	f := gatedStepAttemptFixture(t)
	approveRetryApproval(t, f.generationFixture)
	setRetryApprovalTestExpiry(t, f.generationFixture, time.Now().Add(200*time.Millisecond))
	if _, err := f.jobs.ReconcileJobRetryApproval(f.ctx, retryApprovalTestRequest(t, f.generationFixture, f.generation, "admit")); err != nil {
		t.Fatal(err)
	}
	name := "test:late-step-approval"
	if err := f.db.Callback().Update().After("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.StepRun{}).TableName() {
			time.Sleep(250 * time.Millisecond)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Update().Remove(name) })
	request := f.request(t, 0)
	if _, err := f.jobs.ReconcileStepAttempt(f.ctx, request); err == nil {
		t.Fatal("post-write expired step committed")
	}
	row, err := f.steps.GetByStepRunID(f.ctx, request.Scope.StepRunID)
	if err != nil || !emptyStepAdmission(row) {
		t.Fatal("expired admission failed to roll back")
	}
}

func TestStepRetryApprovalQuorumAndApprovedSuccessor(t *testing.T) {
	f := newStepAttemptFixtureConfigured(t, "", func(base *generationFixture) {
		configureGatedPinFixturePolicy(t, base, retryapproval.Policy{ID: "policy", Candidates: []string{"voter", "voter2"}, Mode: "all", RequiredApproverCount: 2, TimeoutSeconds: 300})
	})
	installRetryApprovalTestAuthority(t, f.generationFixture, nil)
	if err := f.db.Create(&model.User{UserID: "voter2", IsEnabled: 1}).Error; err != nil {
		t.Fatal(err)
	}
	r, a := retryApprovalTestRow(t, f.generationFixture)
	if _, err := r.Decide(f.ctx, a.ApprovalID, "actor", model.ApprovalDecisionApprove, ""); err == nil {
		t.Fatal("requester voted")
	}
	approveRetryApproval(t, f.generationFixture)
	if _, err := f.jobs.ReconcileStepAttempt(f.ctx, f.request(t, 0)); err == nil {
		t.Fatal("incomplete quorum admitted step")
	}
	if _, err := r.Decide(f.ctx, a.ApprovalID, "voter2", model.ApprovalDecisionApprove, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.jobs.ReconcileJobRetryApproval(f.ctx, retryApprovalTestRequest(t, f.generationFixture, f.generation, "admit")); err != nil {
		t.Fatal(err)
	}
	first := f.admitStep(t, 0)
	f.finishStep(t, first, model.JobRunStatusFailed)
	request := f.nextRequest(t, first, 1)
	successor, err := f.jobs.ReconcileStepAttempt(f.ctx, request)
	if err != nil || !successor.ExecutionAllowed {
		t.Fatalf("approved successor: %+v %v", successor, err)
	}
	inspect := request
	inspect.Operation = "inspect"
	inspect.PreviousAdmissionID = ""
	inspect.PreviousAdmissionBindingDigest = ""
	inspect.PreviousStepRunID = ""
	inspect.PreviousStepClaimID = ""
	inspect.FailedStepReceiptDigest = ""
	inspect.Boundary = nil
	inspect.RequestDigest, _ = inspect.Digest()
	observed, err := f.jobs.ReconcileStepAttempt(f.ctx, inspect)
	if err != nil || observed.ExecutionAllowed {
		t.Fatalf("inspect granted execution: %+v %v", observed, err)
	}
	f.finishStep(t, successor.Admission, model.JobRunStatusSuccess)
	f.admitStep(t, 1)
}

func TestStepRetryApprovalMySQLSourceFixtures(t *testing.T) {
	for _, quorum := range []int{1, 2} {
		f := stepRetryApprovalSourceFixture(t, quorum)
		_, a := retryApprovalTestRow(t, f.generationFixture)
		if a.BindingKind != retryapproval.FailedStepKind || a.RequiredApproverCount != quorum {
			t.Fatal("composed MySQL source fixture lost exact gate")
		}
	}
}

func TestStepRetryApprovalRunningPostWriteExpiryRollsBack(t *testing.T) {
	f := gatedStepAttemptFixture(t)
	approveRetryApproval(t, f.generationFixture)
	setRetryApprovalTestExpiry(t, f.generationFixture, time.Now().Add(250*time.Millisecond))
	if _, err := f.jobs.ReconcileJobRetryApproval(f.ctx, retryApprovalTestRequest(t, f.generationFixture, f.generation, "admit")); err != nil {
		t.Fatal(err)
	}
	admission := f.admitStep(t, 0)
	name := "test:late-running-approval"
	if err := f.db.Callback().Update().After("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.StepRun{}).TableName() {
			time.Sleep(300 * time.Millisecond)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Update().Remove(name) })
	if err := f.reportStep(t, admission.Scope.StepRunID, admission, model.JobRunStatusRunning, false); err == nil {
		t.Fatal("expired RUNNING write committed")
	}
	row, err := f.steps.GetByStepRunID(f.ctx, admission.Scope.StepRunID)
	if err != nil || row.Status == model.JobRunStatusRunning || row.ClaimID != "" {
		t.Fatal("expired RUNNING failed to roll back")
	}
	replay, err := f.jobs.ReconcileStepAttempt(f.ctx, f.request(t, 0))
	if err != nil || replay.ExecutionAllowed {
		t.Fatalf("expired admission replay: %+v %v", replay, err)
	}
	before, _ := admission.Canonical()
	after, _ := replay.Admission.Canonical()
	if before != after {
		t.Fatal("expiry changed historical descriptor")
	}
}

func TestStepRetryApprovalRejectsKindSubstitution(t *testing.T) {
	f := gatedStepAttemptFixture(t)
	r, a := retryApprovalTestRow(t, f.generationFixture)
	if err := f.db.Model(&model.ApprovalRequest{}).Where("approval_id = ?", a.ApprovalID).Update("binding_kind", retryapproval.Kind).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadRetryApproval(f.ctx, a.ApprovalID); err == nil {
		t.Fatal("whole-job kind accepted v2 plan")
	}
	if _, err := r.Decide(f.ctx, a.ApprovalID, "voter", model.ApprovalDecisionApprove, ""); err == nil {
		t.Fatal("mismatched kind vote accepted")
	}
	if _, err := r.Cancel(f.ctx, a.ApprovalID, ""); err == nil {
		t.Fatal("mismatched kind approval mutation accepted")
	}
}

func TestStepRetryApprovalHistoricalReviewAndVoteStayImmutable(t *testing.T) {
	f := gatedStepAttemptFixture(t)
	approveStepStartup(t, f)
	r, a := retryApprovalTestRow(t, f.generationFixture)
	before, err := r.ReadRetryApproval(f.ctx, a.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	body, err := before.Evidence.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.User{}).Where("user_id IN ?", []string{"actor", "voter"}).Update("is_enabled", 0).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("status", model.JobRunStatusSuccess).Error; err != nil {
		t.Fatal(err)
	}
	after, err := r.ReadRetryApproval(f.ctx, a.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := after.Evidence.Canonical()
	if err != nil || body != replay {
		t.Fatal("historical review changed")
	}
	vote, err := r.Decide(f.ctx, a.ApprovalID, "voter", model.ApprovalDecisionApprove, "new ignored comment")
	if err != nil || vote.Status != a.Status || vote.Reason != a.Reason {
		t.Fatalf("historical vote changed: %+v %v", vote, err)
	}
	if err := r.ValidateRetryApprovalDecision(f.ctx, a.ApprovalID, "voter", model.ApprovalDecisionApprove); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Decide(f.ctx, a.ApprovalID, "voter", model.ApprovalDecisionReject, ""); err == nil {
		t.Fatal("historical vote changed decision")
	}
	if _, err := r.Decide(f.ctx, a.ApprovalID, "actor", model.ApprovalDecisionApprove, ""); err == nil {
		t.Fatal("nonvoter acquired historical vote")
	}
	if err := f.db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("retry_plan_digest", "v1:tampered").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadRetryApproval(f.ctx, a.ApprovalID); err == nil {
		t.Fatal("tampered plan review accepted")
	}
	if _, err := r.Decide(f.ctx, a.ApprovalID, "voter", model.ApprovalDecisionApprove, ""); err == nil {
		t.Fatal("tampered plan vote replay accepted")
	}
}

func TestStepRetryApprovalMySQLConsumerSourceFixture(t *testing.T) {
	producer := gatedStepAttemptFixture(t)
	approveStepStartup(t, producer)
	var upload string
	for index, step := range producer.plan.Steps {
		a := producer.admitStep(t, index)
		producer.finishStep(t, a, model.JobRunStatusSuccess)
		upload = a.Scope.StepRunID
		for _, id := range step.AttemptRunIDs[1:] {
			if err := producer.reportStep(t, id, nil, model.JobRunStatusSkipped, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	artifact := stepAttemptMySQLArtifact(t, producer, upload)
	if err := stepAttemptMySQLPublish(producer, producer.ctx, stepAttemptMySQLReadJob(t, producer), artifact); err != nil {
		t.Fatal(err)
	}
	consumer := stepRetryApprovalConsumerFixture(t, producer, artifact)
	if _, err := consumer.jobs.ReconcileStepAttempt(consumer.ctx, consumer.request(t, 0)); err != nil {
		t.Fatal(err)
	}
}

func TestStepRetryApprovalTerminalRPCRejectsBeforeSharedLocks(t *testing.T) {
	f := gatedStepAttemptFixture(t)
	approveStepStartup(t, f)
	if err := f.db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("status", model.JobRunStatusSuccess).Error; err != nil {
		t.Fatal(err)
	}
	name := "test:terminal-shared-run-refusal"
	if err := f.db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.PipelineRun{}).TableName() || tx.Statement.Table == (&model.Agent{}).TableName() {
			_ = tx.AddError(fmt.Errorf("unexpected shared authority read"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(name) })
	for _, operation := range []string{"observe", "admit"} {
		_, err := f.jobs.ReconcileJobRetryApproval(f.ctx, retryApprovalTestRequest(t, f.generationFixture, f.generation, operation))
		if err == nil || !strings.Contains(err.Error(), "terminal job") {
			t.Fatalf("terminal approval RPC reached authority: %v", err)
		}
	}
	if _, err := f.jobs.ReconcileJobGeneration(f.ctx, f.admit); err == nil || !strings.Contains(err.Error(), "terminal job") {
		t.Fatalf("terminal generation RPC reached authority: %v", err)
	}
}

func TestStepRetryApprovalLedgerUsesOnlyUniquePointLocks(t *testing.T) {
	f := gatedStepAttemptFixture(t)
	approveStepStartup(t, f)
	name := "test:ledger-lock-scope"
	seen := 0
	if err := f.db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table != (model.StepRun{}).TableName() {
			return
		}
		lock, ok := tx.Statement.Clauses["FOR"].Expression.(clause.Locking)
		if !ok || lock.Strength != "UPDATE" {
			return
		}
		seen++
		if !strings.Contains(tx.Statement.SQL.String(), "WHERE step_run_id = ?") || len(tx.Statement.Vars) != 1 || tx.RowsAffected != 1 {
			_ = tx.AddError(fmt.Errorf("ledger must lock one unique planned step, got %s", tx.Statement.SQL.String()))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(name) })
	f.admitStep(t, 0)
	if seen != len(f.plan.Steps)*int(f.plan.Policy.MaxAttempts) {
		t.Fatalf("point locks = %d", seen)
	}
}

func TestStepRetryApprovalTerminalHistoryLocksOnlyParent(t *testing.T) {
	f := gatedStepAttemptFixture(t)
	approveStepStartup(t, f)
	request := f.request(t, 0)
	admission := f.admitStep(t, 0)
	if err := f.db.Model(&model.JobRun{}).Where("job_run_id = ?", f.identity.JobRunID).Update("status", model.JobRunStatusSuccess).Error; err != nil {
		t.Fatal(err)
	}
	name := "test:history-parent-only"
	if err := f.db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		lock, ok := tx.Statement.Clauses["FOR"].Expression.(clause.Locking)
		if ok && lock.Strength == "UPDATE" && tx.Statement.Table != (model.JobRun{}).TableName() {
			_ = tx.AddError(fmt.Errorf("terminal history locked %s", tx.Statement.Table))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(name) })
	for _, operation := range []string{"admit", "inspect"} {
		request.Operation = operation
		request.RequestDigest, _ = request.Digest()
		result, err := f.jobs.ReconcileStepAttempt(f.ctx, request)
		if err != nil || result.ExecutionAllowed {
			t.Fatalf("history: %+v %v", result, err)
		}
		before, _ := admission.Canonical()
		after, _ := result.Admission.Canonical()
		if before != after {
			t.Fatal("historical descriptor changed")
		}
	}
}

func TestStepRetryApprovalFrozenAllocationRejectsCreate(t *testing.T) {
	f := gatedStepAttemptFixture(t)
	row := &model.StepRun{StepRunID: "late-extra", JobRunID: f.identity.JobRunID, PipelineID: f.identity.PipelineID, PipelineRunID: f.identity.PipelineRunID, Status: 1}
	if err := f.steps.Create(f.ctx, row); err == nil {
		t.Fatal("late allocation inserted into frozen retry plan")
	}
	var count int64
	if err := f.db.Model(&model.StepRun{}).Where("step_run_id = ?", row.StepRunID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("late allocation persisted")
	}
}

func TestStepRetryApprovalFrozenAllocationRejectsReparent(t *testing.T) {
	f := gatedStepAttemptFixture(t)
	legacy := &model.StepRun{StepRunID: "legacy-unrelated", Status: 1}
	if err := f.steps.Create(f.ctx, legacy); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"job_run_id", "JobRunID", "jobRunId", "step_run_id", "StepRunID"} {
		if err := f.steps.PatchByStepRunID(f.ctx, legacy.StepRunID, map[string]any{key: f.identity.JobRunID}); err == nil {
			t.Fatalf("identity mutation accepted: %s", key)
		}
		if err := f.steps.PatchStatusIfCurrent(f.ctx, legacy.StepRunID, 1, map[string]any{key: f.identity.JobRunID}); err == nil {
			t.Fatalf("status identity mutation accepted: %s", key)
		}
	}
	row, err := f.steps.GetByStepRunID(f.ctx, legacy.StepRunID)
	if err != nil || row.JobRunID != "" {
		t.Fatal("legacy step moved into frozen allocation")
	}
}

func TestStepRetryApprovalLedgerEnumerationRejectsAllocationTampering(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		for _, change := range []string{"extra", "missing", "substituted"} {
			t.Run(fmt.Sprintf("terminal=%t/%s", terminal, change), func(t *testing.T) {
				f := gatedStepAttemptFixture(t)
				approveStepStartup(t, f)
				f.admitStep(t, 0)
				if terminal {
					if err := f.db.Model(&model.JobRun{}).Where("job_run_id = ?", f.identity.JobRunID).Update("status", model.JobRunStatusSuccess).Error; err != nil {
						t.Fatal(err)
					}
				}
				id := f.plan.Steps[len(f.plan.Steps)-1].AttemptRunIDs[2]
				var err error
				switch change {
				case "extra":
					err = f.db.Create(&model.StepRun{StepRunID: "unplanned-extra", JobRunID: f.identity.JobRunID, Status: 1}).Error
				case "missing":
					err = f.db.Where("step_run_id = ?", id).Delete(&model.StepRun{}).Error
				case "substituted":
					err = f.db.Model(&model.StepRun{}).Where("step_run_id = ?", id).Update("step_run_id", "unplanned-substitute").Error
				}
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.jobs.ReconcileStepAttempt(f.ctx, f.request(t, 0)); err == nil {
					t.Fatal("invalid allocation accepted by historical/live ledger")
				}
			})
		}
	}
}
