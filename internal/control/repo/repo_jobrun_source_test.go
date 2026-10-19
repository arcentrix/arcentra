package repo

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func sourceTestRepository(t *testing.T) (context.Context, *gorm.DB, *JobRunRepo, *sourceprovenance.Binding) {
	t.Helper()
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.WithContext(ctx).AutoMigrate(&model.JobRun{}, &model.StepRun{}, &model.ProjectArtifact{}, &model.PipelineRun{}, &model.Pipeline{}, &model.Project{}); err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Create(&model.JobRun{JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run", Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	// Modern dispatch has an admitted, explicit project scope.
	for _, row := range []any{&model.Project{ProjectID: "project"}, &model.Pipeline{PipelineID: "pipeline", ProjectID: "project"}, &model.PipelineRun{RunID: "run", PipelineID: "pipeline", ProjectID: "project", Status: model.PipelineStatusRunning}} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	b, err := sourceprovenance.NewBinding("pipeline", "run", "job", "git", "https://example/Owner/repo.git", "main")
	if err != nil {
		t.Fatal(err)
	}
	return ctx, db, &JobRunRepo{IDatabase: statusTestDB{db}}, b
}
func captureTestSource(t *testing.T, ctx context.Context, r *JobRunRepo, b *sourceprovenance.Binding) *model.JobRun {
	t.Helper()
	if err := r.CaptureSourceExpectation(ctx, "pipeline", "run", "job", "agent", b); err != nil {
		t.Fatal(err)
	}
	j, err := r.GetByJobRunID(ctx, "job")
	if err != nil {
		t.Fatal(err)
	}
	return j
}
func claimTestSource(t *testing.T, ctx context.Context, r *JobRunRepo, b *sourceprovenance.Binding) *model.JobRun {
	t.Helper()
	j := captureTestSource(t, ctx, r, b)
	if err := r.PatchReportedStatus(ctx, j, map[string]any{"status": 3, "claim_id": "claim"}); err != nil {
		t.Fatal(err)
	}
	j, _ = r.GetByJobRunID(ctx, "job")
	return j
}
func sourceTerminalUpdates() map[string]any {
	return map[string]any{"status": 4, "claim_id": "claim", "terminal_report_digest": strings.Repeat("a", 64), "source_resolved_commit": strings.Repeat("b", 40), "source_provenance_state": "reported"}
}

func TestCaptureSourceExpectationOnceAndGenericUpdateProtection(t *testing.T) {
	ctx, _, r, b := sourceTestRepository(t)
	j := captureTestSource(t, ctx, r, b)
	if !j.SourceExpectationCaptured || j.SourceBindingDigest != b.Digest || j.SourceProvenanceState != "unknown" || j.AgentID != "agent" || j.Status != 2 {
		t.Fatalf("unexpected snapshot %+v", j)
	}
	for _, candidate := range []*sourceprovenance.Binding{b, nil} {
		if err := r.CaptureSourceExpectation(ctx, "pipeline", "run", "job", "other", candidate); err == nil {
			t.Fatal("captured twice")
		}
	}
	for _, key := range append(jobSourceColumns, "SourceBindingDigest", "SourceResolvedCommit") {
		for _, write := range []func(map[string]any) error{func(u map[string]any) error { return r.UpdateByJobRunID(ctx, "job", u) }, func(u map[string]any) error { return r.PatchStatusIfCurrent(ctx, "job", 2, u) }, func(u map[string]any) error { u["status"] = 6; return r.CancelAgentJobIfCurrent(ctx, "job", 2, u) }} {
			if err := write(map[string]any{key: "changed"}); err == nil {
				t.Fatalf("generic update accepted %s", key)
			}
		}
	}
	for _, key := range []string{"agent_id", "AgentID", "claim_id", "pipeline_id", "pipeline_run_id", "job_run_id"} {
		if err := r.UpdateByJobRunID(ctx, "job", map[string]any{key: "changed"}); err == nil {
			t.Fatalf("captured identity changed: %s", key)
		}
	}
	if err := r.UpdateByJobRunID(ctx, "job", map[string]any{"error_message": "normal update"}); err != nil {
		t.Fatal(err)
	}
}
func TestCaptureSourceExpectationRejectsForeignIdentityAndClaimedStates(t *testing.T) {
	for _, field := range []string{"pipeline_id", "pipeline_run_id", "claim_id", "agent_id", "status"} {
		t.Run(field, func(t *testing.T) {
			ctx, db, r, b := sourceTestRepository(t)
			var value any = "other"
			if field == "status" {
				value = 3
			}
			if err := db.WithContext(ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update(field, value).Error; err != nil {
				t.Fatal(err)
			}
			if err := r.CaptureSourceExpectation(ctx, "pipeline", "run", "job", "agent", b); err == nil {
				t.Fatal("foreign or claimed job captured")
			}
		})
	}
	ctx, _, r, _ := sourceTestRepository(t)
	j := captureTestSource(t, ctx, r, nil)
	if !j.SourceExpectationCaptured || j.SourceBindingDigest != "" || j.SourceResolvedCommit != "" || j.SourceProvenanceState != "unknown" {
		t.Fatal("no-source became evidence")
	}
}
func TestSourceReportFencesEveryExpectedFieldAndCaseOnlyChanges(t *testing.T) {
	for _, field := range append(jobSourceColumns, "job_run_id", "pipeline_id", "pipeline_run_id", "agent_id", "claim_id", "cancellation_claim_id", "terminal_report_digest") {
		t.Run(field, func(t *testing.T) {
			ctx, db, r, b := sourceTestRepository(t)
			j := claimTestSource(t, ctx, r, b)
			var changed any = "CHANGED"
			if field == "source_expectation_captured" {
				changed = false
			}
			if err := db.WithContext(ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update(field, changed).Error; err != nil {
				t.Fatal(err)
			}
			if err := r.PatchReportedStatus(ctx, j, sourceTerminalUpdates()); err == nil {
				t.Fatalf("stale %s accepted", field)
			}
		})
	}
	// 比较函数必须逐字节防护忽略大小写的数据库排序规则
	ctx, _, r, b := sourceTestRepository(t)
	j := claimTestSource(t, ctx, r, b)
	for _, change := range []func(*model.JobRun){func(v *model.JobRun) { v.JobRunID = "JOB" }, func(v *model.JobRun) { v.PipelineID = "PIPELINE" }, func(v *model.JobRun) { v.PipelineRunID = "RUN" }, func(v *model.JobRun) { v.AgentID = "AGENT" }, func(v *model.JobRun) { v.ClaimID = "CLAIM" }, func(v *model.JobRun) { v.SourceRef = "MAIN" }, func(v *model.JobRun) { v.SourceRepository = strings.ToUpper(v.SourceRepository) }, func(v *model.JobRun) { v.SourceBindingDigest = strings.ToUpper(v.SourceBindingDigest) }} {
		other := *j
		change(&other)
		if jobSnapshotEqual(j, &other) {
			t.Fatal("case-insensitive snapshot comparison")
		}
	}
}
func TestSourceReceiptAtomicFailureAndCancellation(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprint(cancel), func(t *testing.T) {
			ctx, db, r, b := sourceTestRepository(t)
			j := claimTestSource(t, ctx, r, b)
			if cancel {
				if err := r.CancelAgentJobIfCurrent(ctx, "job", 3, map[string]any{"status": 6, "error_message": "controller reason"}); err != nil {
					t.Fatal(err)
				}
				j, _ = r.GetByJobRunID(ctx, "job")
			}
			write := func() error {
				u := sourceTerminalUpdates()
				if cancel {
					return r.CommitCancellationReceiptWithSource(ctx, j, "claim", u["terminal_report_digest"].(string), map[string]any{"source_provenance_state": u["source_provenance_state"], "source_resolved_commit": u["source_resolved_commit"]})
				}
				return r.PatchReportedStatus(ctx, j, u)
			}
			name := "source-update-failure"
			if err := db.Callback().Update().After("gorm:update").Register(name, func(tx *gorm.DB) {
				if tx.Statement.Table == "job_run" {
					tx.AddError(fmt.Errorf("injected failure"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err := write(); err == nil {
				t.Fatal("failed transaction acknowledged")
			}
			after, _ := r.GetByJobRunID(ctx, "job")
			if after.TerminalReportDigest != "" || after.SourceResolvedCommit != "" || after.SourceProvenanceState != "unknown" || after.Status != j.Status {
				t.Fatalf("partial result %+v", after)
			}
			if err := db.Callback().Update().Remove(name); err != nil {
				t.Fatal(err)
			}
			if err := write(); err != nil {
				t.Fatal(err)
			}
			after, _ = r.GetByJobRunID(ctx, "job")
			if after.SourceResolvedCommit != strings.Repeat("b", 40) || after.SourceProvenanceState != "reported" || after.TerminalReportDigest == "" {
				t.Fatal("missing receipt evidence")
			}
			if cancel && (after.ErrorMessage != "controller reason" || after.Status != 6) {
				t.Fatal("cancelled state overwritten")
			}
			if err := write(); err == nil {
				t.Fatal("stale receipt rewrote provenance")
			}
		})
	}
}
func TestSourceReportRejectsUnexpectedAndNonterminalEvidence(t *testing.T) {
	for _, kind := range []string{"no-source", "unclaimed", "nonterminal", "digest", "sha", "rewrite-binding"} {
		t.Run(kind, func(t *testing.T) {
			ctx, _, r, b := sourceTestRepository(t)
			if kind == "no-source" {
				b = nil
			}
			var j *model.JobRun
			if kind == "unclaimed" {
				j = captureTestSource(t, ctx, r, b)
			} else {
				j = claimTestSource(t, ctx, r, b)
			}
			u := sourceTerminalUpdates()
			switch kind {
			case "nonterminal":
				u["status"] = 3
			case "digest":
				j.SourceBindingDigest = "v1:" + strings.Repeat("f", 64)
			case "sha":
				u["source_resolved_commit"] = "short"
			case "rewrite-binding":
				u["source_binding_digest"] = b.Digest
			}
			if err := r.PatchReportedStatus(ctx, j, u); err == nil {
				t.Fatal("invalid source accepted")
			}
		})
	}
}

type caseInsensitiveSourceJob struct {
	model.JobRun
	JobRunID              string `gorm:"column:job_run_id;type:text COLLATE NOCASE;uniqueIndex:uk_job_run_business_id"`
	PipelineID            string `gorm:"column:pipeline_id;type:text COLLATE NOCASE"`
	PipelineRunID         string `gorm:"column:pipeline_run_id;type:text COLLATE NOCASE"`
	AgentID               string `gorm:"column:agent_id;type:text COLLATE NOCASE"`
	ClaimID               string `gorm:"column:claim_id;type:text COLLATE NOCASE;default:''"`
	SourceType            string `gorm:"column:source_type;type:text COLLATE NOCASE;default:''"`
	SourceRepository      string `gorm:"column:source_repository;type:text COLLATE NOCASE;default:''"`
	SourceRef             string `gorm:"column:source_ref;type:text COLLATE NOCASE;default:''"`
	SourceBindingDigest   string `gorm:"column:source_binding_digest;type:text COLLATE NOCASE;default:''"`
	SourceProvenanceState string `gorm:"column:source_provenance_state;type:text COLLATE NOCASE;default:'unknown'"`
}

func TestSourceReportByteExactFenceWithCaseInsensitiveDatabase(t *testing.T) {
	for _, field := range []string{"job_run_id", "pipeline_id", "pipeline_run_id", "agent_id", "claim_id", "source_type", "source_repository", "source_ref", "source_binding_digest", "source_provenance_state"} {
		t.Run(field, func(t *testing.T) {
			ctx := context.Background()
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, _ := db.DB()
			sqlDB.SetMaxOpenConns(1)
			defer sqlDB.Close()
			if err := db.WithContext(ctx).AutoMigrate(&caseInsensitiveSourceJob{}, &model.StepRun{}, &model.PipelineRun{}, &model.Pipeline{}, &model.Project{}); err != nil {
				t.Fatal(err)
			}
			if err := db.WithContext(ctx).Create(&model.JobRun{JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run", Status: 1}).Error; err != nil {
				t.Fatal(err)
			}
			for _, row := range []any{&model.Project{ProjectID: "project"}, &model.Pipeline{PipelineID: "pipeline", ProjectID: "project"}, &model.PipelineRun{RunID: "run", PipelineID: "pipeline", ProjectID: "project", Status: model.PipelineStatusRunning}} {
				if err := db.Create(row).Error; err != nil {
					t.Fatal(err)
				}
			}
			r := &JobRunRepo{IDatabase: statusTestDB{db}}
			b, _ := sourceprovenance.NewBinding("pipeline", "run", "job", "git", "https://example/Owner/repo.git", "main")
			j := claimTestSource(t, ctx, r, b)
			values := map[string]string{"job_run_id": j.JobRunID, "pipeline_id": j.PipelineID, "pipeline_run_id": j.PipelineRunID, "agent_id": j.AgentID, "claim_id": j.ClaimID, "source_type": j.SourceType, "source_repository": j.SourceRepository, "source_ref": j.SourceRef, "source_binding_digest": j.SourceBindingDigest, "source_provenance_state": j.SourceProvenanceState}
			if err := db.WithContext(ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update(field, strings.ToUpper(values[field])).Error; err != nil {
				t.Fatal(err)
			}
			var count int64
			if err := reportedJobQuery(db.WithContext(ctx).Model(&model.JobRun{}), j).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("fixture did not provide case-insensitive comparison: %d", count)
			}
			if err := r.PatchReportedStatus(ctx, j, sourceTerminalUpdates()); err == nil {
				t.Fatalf("case-only %s changed snapshot accepted", field)
			}
		})
	}
}

func TestSourceCaptureCannotBeBypassedThroughCreateOrReportUpdates(t *testing.T) {
	ctx, _, r, b := sourceTestRepository(t)
	if err := r.Create(ctx, &model.JobRun{JobRunID: "forged", Status: 4, SourceExpectationCaptured: true, SourceBindingDigest: b.Digest, SourceResolvedCommit: strings.Repeat("a", 40), SourceProvenanceState: "reported"}); err == nil {
		t.Fatal("generic create forged evidence")
	}
	j := claimTestSource(t, ctx, r, b)
	for _, field := range []string{"agent_id", "pipeline_id", "pipeline_run_id", "job_run_id", "claim_id", "cancellation_claim_id", "AgentID", "ClaimID"} {
		u := sourceTerminalUpdates()
		u[field] = "other"
		if err := r.PatchReportedStatus(ctx, j, u); err == nil {
			t.Fatalf("report changed identity %s", field)
		}
	}
}

func TestSourceCaptureAssignsStepsAtomicallyWithJobAndCancellation(t *testing.T) {
	ctx, db, r, b := sourceTestRepository(t)
	for _, id := range []string{"first", "retry"} {
		if err := db.WithContext(ctx).Create(&model.StepRun{StepRunID: id, JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run", Status: 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	name := "source-capture-step-failure"
	if err := db.Callback().Update().After("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "step_run" {
			tx.AddError(fmt.Errorf("injected step assignment failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.CaptureSourceExpectation(ctx, "pipeline", "run", "job", "agent", b); err == nil {
		t.Fatal("partial dispatch capture succeeded")
	}
	job, _ := r.GetByJobRunID(ctx, "job")
	if job.SourceExpectationCaptured || job.AgentID != "" || job.Status != 1 {
		t.Fatal("job captured without atomic step assignment")
	}
	var steps []model.StepRun
	if err := db.WithContext(ctx).Select("agent_id", "status").Find(&steps).Error; err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		if step.AgentID != "" || step.Status != 1 {
			t.Fatal("partial step assignment survived rollback")
		}
	}
	if err := db.Callback().Update().Remove(name); err != nil {
		t.Fatal(err)
	}
	captureTestSource(t, ctx, r, b)
	if err := r.CancelAgentJobIfCurrent(ctx, "job", 2, map[string]any{"status": 6}); err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Select("agent_id", "status").Find(&steps).Error; err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		if step.AgentID != "agent" || step.Status != 6 {
			t.Fatal("cancellation missed atomically assigned steps")
		}
	}
	if err := r.CaptureSourceExpectation(ctx, "pipeline", "run", "job", "other", b); err == nil {
		t.Fatal("cancelled job recaptured")
	}
}

func TestSourceCaptureRejectsStaleChildReceiptMetadata(t *testing.T) {
	for _, field := range []string{"cancellation_claim_id", "terminal_report_digest"} {
		t.Run(field, func(t *testing.T) {
			ctx, db, r, b := sourceTestRepository(t)
			if err := db.WithContext(ctx).Create(&model.StepRun{StepRunID: "step", JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run", Status: 1}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.WithContext(ctx).Model(&model.StepRun{}).Where("step_run_id = ?", "step").Update(field, "stale-receipt").Error; err != nil {
				t.Fatal(err)
			}
			if err := r.CaptureSourceExpectation(ctx, "pipeline", "run", "job", "agent", b); err == nil {
				t.Fatal("stale child receipt captured for new dispatch")
			}
			job, _ := r.GetByJobRunID(ctx, "job")
			if job.SourceExpectationCaptured || job.AgentID != "" {
				t.Fatal("failed child preflight mutated job")
			}
		})
	}
}
