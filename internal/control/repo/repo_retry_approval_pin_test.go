package repo

import (
	"fmt"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"gorm.io/gorm"
	"strings"
	"testing"
	"time"
)

func gatedPinFixture(t *testing.T) *generationFixture {
	t.Helper()
	f := newGenerationFixture(t)
	configureGatedPinFixture(t, f)
	return f
}

func configureGatedPinFixture(t *testing.T, f *generationFixture) {
	configureGatedPinFixturePolicy(t, f, retryapproval.Policy{ID: "policy", Candidates: []string{"voter"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 300})
}
func configureGatedPinFixturePolicy(t *testing.T, f *generationFixture, policy retryapproval.Policy) {
	t.Helper()
	if err := f.db.WithContext(f.ctx).AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalDecision{}); err != nil {
		t.Fatal(err)
	}
	policy.Fingerprint, _ = policy.Digest()
	c := retryapproval.RunContext{Version: 1, ProjectID: "project", PipelineID: "pipeline", PipelineRunID: "run", ActorID: "actor", DefinitionCommitSHA: strings.Repeat("a", 40), DefinitionPath: "pipeline.yaml", ResolvedDefinitionDigest: "v1:" + strings.Repeat("b", 64), GatedJobs: []string{"build"}, Policy: policy}
	body, err := c.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := c.Digest()
	if err := f.db.WithContext(f.ctx).Model(&model.PipelineRun{}).Where("run_id = ?", "run").Updates(map[string]any{"triggered_by": "actor", "trigger_type": 1, "definition_commit_sha": c.DefinitionCommitSHA, "definition_path": c.DefinitionPath, "retry_approval_context": body, "retry_approval_context_digest": digest}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.WithContext(f.ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(map[string]any{"job_name": "build", "retry_approval_required": true}).Error; err != nil {
		t.Fatal(err)
	}
	f.plan.RetryApprovalRequired = true
	f.plan.RetryApprovalTimeoutSeconds = c.Policy.TimeoutSeconds
	planJSON, _ := f.plan.Canonical()
	planDigest, _ := f.plan.Digest("pipeline", "run", "job", "agent", f.identity.RuntimeBindingDigest)
	if err := f.db.WithContext(f.ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(map[string]any{"retry_plan": planJSON, "retry_plan_digest": planDigest}).Error; err != nil {
		t.Fatal(err)
	}
	f.admit.RetryPlanDigest = planDigest
	f.admit.RequestDigest, _ = f.admit.Digest()
}
func TestRetryApprovalPinIsAtomicAndNeverRenews(t *testing.T) {
	f := gatedPinFixture(t)
	job, err := f.jobs.GetOrRecordJobInputs(f.ctx, f.identity, f.manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !job.RetryApprovalRequired || job.RetryApprovalID == "" {
		t.Fatal("gate missing")
	}
	approvals := &ApprovalRepo{IDatabase: statusTestDB{f.db}}
	a, err := approvals.GetByApprovalID(f.ctx, job.RetryApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	e, err := retryapproval.ParseEvidence(a.EvidenceJSON)
	if err != nil {
		t.Fatal(err)
	}
	if e.Identity != f.identity || e.RetryPlanDigest != job.RetryPlanDigest || e.InputManifestDigest != job.InputManifestDigest {
		t.Fatal("incomplete evidence")
	}
	again, err := f.jobs.GetOrRecordJobInputs(f.ctx, f.identity, f.manifest)
	if err != nil {
		t.Fatal(err)
	}
	b, err := approvals.GetByApprovalID(f.ctx, again.RetryApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if a.ApprovalID != b.ApprovalID || a.EvidenceJSON != b.EvidenceJSON || !a.ExpiresAt.Equal(*b.ExpiresAt) {
		t.Fatal("replay renewed binding")
	}
	var count int64
	if err := f.db.WithContext(f.ctx).Model(&model.ApprovalRequest{}).Where("retry_job_run_id = ?", "job").Count(&count).Error; err != nil || count != 1 {
		t.Fatal("not unique", count, err)
	}
	reopened := &JobRunRepo{IDatabase: statusTestDB{f.db.Session(&gorm.Session{NewDB: true})}}
	restored, err := reopened.GetOrRecordJobInputs(f.ctx, f.identity, f.manifest)
	if err != nil || restored.RetryApprovalID != a.ApprovalID {
		t.Fatal("reopen changed binding", err)
	}
}
func TestRetryApprovalPinRollsBackInputsAndBinding(t *testing.T) {
	f := gatedPinFixture(t)
	err := f.db.Callback().Create().Before("gorm:create").Register("test:deny_retry_approval", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "approval_request" {
			tx.AddError(fmt.Errorf("injected approval write failure"))
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.jobs.GetOrRecordJobInputs(f.ctx, f.identity, f.manifest); err == nil {
		t.Fatal("failed write accepted")
	}
	job, err := f.jobs.GetByJobRunID(f.ctx, "job")
	if err != nil {
		t.Fatal(err)
	}
	if job.InputManifestState != "unknown" || job.InputManifest != "" || job.RetryApprovalID != "" {
		t.Fatal("partial pin committed")
	}
}
func TestRetryApprovalPinRejectsDowngradeAndDeadline(t *testing.T) {
	for _, name := range []string{"downgrade", "deadline", "missing context"} {
		t.Run(name, func(t *testing.T) {
			f := gatedPinFixture(t)
			patch := map[string]any{}
			switch name {
			case "downgrade":
				patch["retry_approval_required"] = false
			case "deadline":
				patch["timeout"] = "1ms"
				patch["start_time"] = time.Now().Add(-time.Second)
			case "missing context":
				if err := f.db.WithContext(f.ctx).Model(&model.PipelineRun{}).Where("run_id = ?", "run").Updates(map[string]any{"retry_approval_context": "", "retry_approval_context_digest": ""}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if len(patch) > 0 {
				if err := f.db.WithContext(f.ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(patch).Error; err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.jobs.GetOrRecordJobInputs(f.ctx, f.identity, f.manifest); err == nil {
				t.Fatal("unsafe pin accepted")
			}
			var n int64
			f.db.WithContext(f.ctx).Model(&model.ApprovalRequest{}).Count(&n)
			if n != 0 {
				t.Fatal("orphan approval")
			}
		})
	}
}
func TestRetryApprovalGenericMutationsCannotManufactureBinding(t *testing.T) {
	f := gatedPinFixture(t)
	for _, field := range []string{"retry_approval_required", "retry_approval_id", "RetryApprovalEvidenceHash"} {
		if err := f.jobs.UpdateByJobRunID(f.ctx, "job", map[string]any{field: "changed"}); err == nil {
			t.Fatal("generic gate patch accepted")
		}
	}
	pipelines := &PipelineRepo{IDatabase: statusTestDB{f.db}}
	for _, field := range []string{"retry_approval_context", "RetryApprovalContextDigest"} {
		if err := pipelines.UpdateRun(f.ctx, "run", map[string]any{field: "changed"}); err == nil {
			t.Fatal("generic context patch accepted")
		}
	}
	if err := pipelines.CreateRun(f.ctx, &model.PipelineRun{RunID: "other", RetryApprovalContext: "{}"}); err == nil {
		t.Fatal("generic gate creation accepted")
	}
}

func TestRetryApprovalCaptureDerivesRequiredFromRun(t *testing.T) {
	f := gatedPinFixture(t)
	// Restore the pre-dispatch state while retaining the same preallocated attempts and trusted run context.
	reset := map[string]any{"retry_approval_required": false, "retry_approval_id": "", "retry_approval_evidence_hash": "", "status": model.JobRunStatusPending, "claim_id": "", "agent_id": "", "source_expectation_captured": false, "runtime_type": "", "runtime_image": "", "runtime_cpu": "", "runtime_memory": "", "runtime_binding_digest": "", "input_expectation": "", "retry_plan": "", "retry_plan_digest": ""}
	if err := f.db.WithContext(f.ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(reset).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.jobs.CaptureSourceExpectation(f.ctx, "pipeline", "run", "job", "agent", nil); err == nil {
		t.Fatal("listed gate downgraded to source-only capture")
	}
	runtime := repoRuntimeRequest(t)
	falsePlan := *f.plan
	falsePlan.RetryApprovalRequired = false
	falsePlan.RetryApprovalTimeoutSeconds = 0
	if err := f.jobs.CaptureRuntimeRetryExpectation(f.ctx, "pipeline", "run", "job", "agent", nil, runtime, nil, nil, &falsePlan); err == nil {
		t.Fatal("gated context accepted ungated executable plan")
	}
	wrongBudget := *f.plan
	wrongBudget.RetryApprovalTimeoutSeconds++
	if err := f.jobs.CaptureRuntimeRetryExpectation(f.ctx, "pipeline", "run", "job", "agent", nil, runtime, nil, nil, &wrongBudget); err == nil {
		t.Fatal("gated context accepted expanded wait budget")
	}
	if err := f.jobs.CaptureRuntimeRetryExpectation(f.ctx, "pipeline", "run", "job", "agent", nil, runtime, nil, nil, f.plan); err != nil {
		t.Fatal(err)
	}
	job, err := f.jobs.GetByJobRunID(f.ctx, "job")
	if err != nil {
		t.Fatal(err)
	}
	if !job.RetryApprovalRequired || job.RetryApprovalID != "" {
		t.Fatal("capture failed to set preparation-only marker")
	}
}

func TestRetryApprovalRunDedicatedCreateIsImmutable(t *testing.T) {
	f := gatedPinFixture(t)
	var original model.PipelineRun
	if err := f.db.WithContext(f.ctx).Where("run_id = ?", "run").First(&original).Error; err != nil {
		t.Fatal(err)
	}
	c, err := retryapproval.ParseRunContext(original.RetryApprovalContext)
	if err != nil {
		t.Fatal(err)
	}
	c.PipelineRunID = "newrun"
	body, _ := c.Canonical()
	hash, _ := c.Digest()
	run := &model.PipelineRun{RunID: "newrun", PipelineID: "pipeline", TriggeredBy: c.ActorID, TriggerType: 1, DefinitionCommitSha: c.DefinitionCommitSHA, DefinitionPath: c.DefinitionPath, Status: model.PipelineStatusPending, RetryApprovalContext: body, RetryApprovalContextDigest: hash}
	r := &PipelineRepo{IDatabase: statusTestDB{f.db}}
	if err := r.CreateRun(f.ctx, run); err == nil {
		t.Fatal("generic create manufactured gate")
	}
	if err := r.CreateRetryApprovalRun(f.ctx, run); err != nil {
		t.Fatal(err)
	}
	run.RunID = "forged"
	if err := r.CreateRetryApprovalRun(f.ctx, run); err == nil {
		t.Fatal("foreign run context accepted")
	}
}

func TestRetryApprovalMigrationDoesNotInventLegacyAuthority(t *testing.T) {
	ctx, db, jobs, _ := sourceTestRepository(t)
	removeLaterRerunColumnsForMigration(t, db)
	for _, field := range []string{"retry_approval_required", "retry_approval_id", "retry_approval_evidence_hash"} {
		if err := db.WithContext(ctx).Migrator().DropColumn(&model.JobRun{}, field); err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range []string{"retry_approval_context", "retry_approval_context_digest"} {
		if err := db.WithContext(ctx).Migrator().DropColumn(&model.PipelineRun{}, field); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := MigrateSchema(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	j, err := jobs.GetByJobRunID(ctx, "job")
	if err != nil {
		t.Fatal(err)
	}
	if !emptyRetryApproval(j) {
		t.Fatal("legacy job gained approval authority")
	}
	if !db.Migrator().HasIndex(&model.ApprovalRequest{}, "uk_approval_retry_job_run") || !db.Migrator().HasConstraint(&model.ApprovalRequest{}, "ck_approval_exclusive_binding") {
		t.Fatal("missing retry approval uniqueness/exclusivity")
	}
}

func TestRetryApprovalMissingPlanCannotDowngradeGenericWrites(t *testing.T) {
	f := gatedPinFixture(t)
	if err := f.db.WithContext(f.ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(map[string]any{"retry_plan": "", "retry_plan_digest": ""}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.jobs.UpdateByJobRunID(f.ctx, "job", map[string]any{"status": model.JobRunStatusSuccess}); err == nil {
		t.Fatal("missing plan bypassed generic status guard")
	}
	if err := f.jobs.PatchStatusIfCurrent(f.ctx, "job", model.JobRunStatusRunning, map[string]any{"status": model.JobRunStatusSuccess}); err == nil {
		t.Fatal("missing plan bypassed status CAS guard")
	}
	if err := f.steps.DeleteByStepRunID(f.ctx, "first-1"); err == nil {
		t.Fatal("missing plan allowed approval attempt deletion")
	}
}

func TestRetryApprovalEvidenceShowsPinnedSourceAndCompleteProducer(t *testing.T) {
	f := gatedPinFixture(t)
	hash := strings.Repeat("d", 64)
	key := "artifacts/run/producer/producer-step/" + hash + "/result.txt"
	size := int64(42)
	expiry := time.Now().Add(time.Minute).Truncate(time.Second)
	for _, v := range []any{&model.JobRun{JobRunID: "producer", JobName: "producer", PipelineID: "pipeline", PipelineRunID: "run", Status: model.JobRunStatusSuccess}, &model.StepRun{StepRunID: "producer-step", JobRunID: "producer", PipelineID: "pipeline", PipelineRunID: "run", Status: model.JobRunStatusSuccess}, &model.ProjectArtifact{ArtifactID: "artifact", ProjectID: "project", PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "producer", StepRunID: "producer-step", Name: "result.txt", StorageID: "store", StorageFingerprint: strings.Repeat("b", 64), StoragePath: key, SHA256: hash, Size: size, ExpiresAt: &expiry}} {
		if err := f.db.WithContext(f.ctx).Create(v).Error; err != nil {
			t.Fatal(err)
		}
	}
	refs := map[string]taskqueue.ArtifactReference{"producer/result.txt": {StorageID: "store", Fingerprint: strings.Repeat("b", 64), SHA256: hash, Size: &size, ExpiresAt: expiry.Unix()}}
	uris := map[string]string{"producer/result.txt": key}
	expected, err := taskqueue.NewJobInputManifest("", uris, refs)
	if err != nil {
		t.Fatal(err)
	}
	expectedJSON, _ := expected.Canonical()
	source, err := sourceprovenance.NewBinding("pipeline", "run", "job", "git", "https://example.test/repo.git", "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.WithContext(f.ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(map[string]any{"input_expectation": expectedJSON, "source_type": source.Type, "source_repository": source.Repository, "source_ref": source.Ref, "source_binding_digest": source.Digest}).Error; err != nil {
		t.Fatal(err)
	}
	f.identity.SourceBindingDigest = source.Digest
	manifest, _ := taskqueue.NewJobInputManifest(strings.Repeat("c", 40), uris, refs)
	job, err := f.jobs.GetOrRecordJobInputs(f.ctx, f.identity, manifest)
	if err != nil {
		t.Fatal(err)
	}
	approvals := &ApprovalRepo{IDatabase: statusTestDB{f.db}}
	a, err := approvals.GetByApprovalID(f.ctx, job.RetryApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	e, err := retryapproval.ParseEvidence(a.EvidenceJSON)
	if err != nil {
		t.Fatal(err)
	}
	if e.Source == nil || e.Source.Ref != "main" || e.Inputs.SourceCommit != strings.Repeat("c", 40) || e.Context.DefinitionCommitSHA != strings.Repeat("a", 40) || len(e.Artifacts) != 1 {
		t.Fatal("incomplete pinned review")
	}
	if !a.ExpiresAt.Equal(expiry) {
		t.Fatal("approval outlived pinned artifact expiry")
	}
	p := e.Artifacts[0]
	if p.ProducerJobRunID != "producer" || p.ProducerStepRunID != "producer-step" || p.StorageKey != key || p.Reference.StorageID != "store" || p.Reference.SHA256 != hash || *p.Reference.Size != 42 || p.Reference.ExpiresAt != expiry.Unix() {
		t.Fatal("incomplete producer identity")
	}
}
