package repo

import (
	"context"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"testing"
)

func repoRuntimeRequest(t *testing.T) *taskqueue.RuntimeRequest {
	t.Helper()
	r := &taskqueue.RuntimeRequest{Type: taskqueue.RuntimeContainerdOfflineV1, Image: "registry.example/build@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Resources: taskqueue.RuntimeResources{CPU: "500m", Memory: "64Mi"}}
	digest, err := taskqueue.RuntimeBindingDigest("pipeline", "run", "job", "agent", r)
	if err != nil {
		t.Fatal(err)
	}
	r.BindingDigest = digest
	return r
}
func TestRuntimeCaptureIsAtomicImmutableAndCompatible(t *testing.T) {
	ctx, db, repository, source := sourceTestRepository(t)
	runtime := repoRuntimeRequest(t)
	if err := db.WithContext(ctx).Create(&model.StepRun{StepRunID: "step", JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run", Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.CaptureRuntimeExpectation(ctx, "pipeline", "run", "job", "agent", source, runtime); err != nil {
		t.Fatal(err)
	}
	job, err := repository.GetByJobRunID(ctx, "job")
	if err != nil {
		t.Fatal(err)
	}
	if job.RuntimeBindingDigest != runtime.BindingDigest || job.RuntimeCPU != "500m" || job.SourceBindingDigest != source.Digest || job.AgentID != "agent" || job.Status != 2 {
		t.Fatalf("incomplete capture: %+v", job)
	}
	var step model.StepRun
	if err := db.WithContext(ctx).Where("step_run_id = ?", "step").First(&step).Error; err != nil || step.AgentID != "agent" || step.Status != 2 {
		t.Fatalf("step not owned atomically: %+v %v", step, err)
	}
	for _, key := range append(jobRuntimeColumns, "RuntimeBindingDigest", "RuntimeImage") {
		if err := repository.UpdateByJobRunID(ctx, "job", map[string]any{key: "changed"}); err == nil {
			t.Fatalf("mutable runtime field %s", key)
		}
		if err := repository.PatchReportedStatus(ctx, job, map[string]any{key: "changed", "status": 3, "claim_id": "claim"}); err == nil {
			t.Fatalf("report changed runtime %s", key)
		}
	}
	if err := repository.CaptureSourceExpectation(ctx, "pipeline", "run", "job", "agent", source); err == nil {
		t.Fatal("runtime downgraded to baremetal")
	}
	if err := repository.CaptureRuntimeExpectation(ctx, "pipeline", "run", "job", "agent", source, runtime); err == nil {
		t.Fatal("runtime dispatched twice")
	}
	if err := repository.Create(ctx, &model.JobRun{JobRunID: "forged", RuntimeBindingDigest: runtime.BindingDigest}); err == nil {
		t.Fatal("generic create supplied runtime evidence")
	}
}
func TestRuntimeCaptureRejectsForeignBindingWithoutPartialOwnership(t *testing.T) {
	ctx, _, repository, source := sourceTestRepository(t)
	runtime := repoRuntimeRequest(t)
	runtime.Resources.CPU = "1"
	if err := repository.CaptureRuntimeExpectation(ctx, "pipeline", "run", "job", "agent", source, runtime); err == nil {
		t.Fatal("mutated runtime accepted")
	}
	job, _ := repository.GetByJobRunID(ctx, "job")
	if job.AgentID != "" || job.SourceExpectationCaptured || !emptyRuntime(job) {
		t.Fatal("invalid runtime partially persisted")
	}
	if err := repository.CaptureSourceExpectation(ctx, "pipeline", "run", "job", "agent", nil); err != nil {
		t.Fatalf("baremetal capture changed: %v", err)
	}
}
func TestRuntimeClaimRechecksAllImmutableFields(t *testing.T) {
	for _, field := range jobRuntimeColumns {
		t.Run(field, func(t *testing.T) {
			ctx, db, r, b := sourceTestRepository(t)
			if err := r.CaptureRuntimeExpectation(ctx, "pipeline", "run", "job", "agent", b, repoRuntimeRequest(t)); err != nil {
				t.Fatal(err)
			}
			job, _ := r.GetByJobRunID(ctx, "job")
			if err := db.WithContext(ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update(field, "changed").Error; err != nil {
				t.Fatal(err)
			}
			if err := r.PatchReportedStatus(ctx, job, map[string]any{"status": 3, "claim_id": "claim"}); err == nil {
				t.Fatal("stale runtime claim committed")
			}
		})
	}
}
func TestInspectRuntimeExecutionRequiresOriginalIdentityAndIsReadOnly(t *testing.T) {
	ctx, db, r, b := sourceTestRepository(t)
	runtime := repoRuntimeRequest(t)
	if err := r.CaptureRuntimeExpectation(ctx, "pipeline", "run", "job", "agent", b, runtime); err != nil {
		t.Fatal(err)
	}
	job, _ := r.GetByJobRunID(ctx, "job")
	if err := r.PatchReportedStatus(ctx, job, map[string]any{"status": 3, "claim_id": "claim"}); err != nil {
		t.Fatal(err)
	}
	before, _ := r.GetByJobRunID(ctx, "job")
	if state, err := r.InspectJobExecution(ctx, "agent", "job", "claim", runtime.BindingDigest); err != nil || state != 3 {
		t.Fatalf("inspect original claim: %d %v", state, err)
	}
	for _, identity := range [][4]string{{"other", "job", "claim", runtime.BindingDigest}, {"agent", "JOB", "claim", runtime.BindingDigest}, {"agent", "job", "other", runtime.BindingDigest}, {"agent", "job", "claim", "other"}, {"agent", "job", "", runtime.BindingDigest}} {
		if _, err := r.InspectJobExecution(ctx, identity[0], identity[1], identity[2], identity[3]); err == nil {
			t.Fatal("foreign execution inspected")
		}
	}
	after, _ := r.GetByJobRunID(ctx, "job")
	if !jobSnapshotEqual(before, after) {
		t.Fatal("inspection mutated job")
	}
	if err := db.WithContext(ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("status", 6).Error; err != nil {
		t.Fatal(err)
	}
	if state, err := r.InspectJobExecution(ctx, "agent", "job", "claim", runtime.BindingDigest); err != nil || state != 6 {
		t.Fatalf("terminal inspect: %d %v", state, err)
	}
}

type legacyRuntimeJob struct {
	model.BaseModel
	JobRunID      string `gorm:"column:job_run_id"`
	PipelineID    string `gorm:"column:pipeline_id"`
	PipelineRunID string `gorm:"column:pipeline_run_id"`
}

func (legacyRuntimeJob) TableName() string { return "job_run" }

type legacyRuntimeAgent struct {
	model.BaseModel
	AgentID string `gorm:"column:agent_id"`
	Labels  string `gorm:"column:labels"`
}

func (legacyRuntimeAgent) TableName() string { return "agent" }
func TestRuntimeMigrationDoesNotInventHistoricalEvidence(t *testing.T) {
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err := db.WithContext(ctx).AutoMigrate(&legacyRuntimeJob{}, &legacyRuntimeAgent{}); err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Create(&legacyRuntimeJob{JobRunID: "legacy", PipelineID: "pipeline", PipelineRunID: "run"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Create(&legacyRuntimeAgent{AgentID: "old-agent", Labels: `{"containerdOfflineV1":"true"}`}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).AutoMigrate(&model.JobRun{}, &model.Agent{}); err != nil {
		t.Fatal(err)
	}
	var job model.JobRun
	if err := db.WithContext(ctx).Select(append([]string{"job_run_id"}, jobRuntimeColumns...)).Where("job_run_id = ?", "legacy").First(&job).Error; err != nil {
		t.Fatal(err)
	}
	if !emptyRuntime(&job) {
		t.Fatal("historical job gained fabricated runtime evidence")
	}
	var agent model.Agent
	if err := db.WithContext(ctx).Select("agent_id", "runtime_capabilities", "labels").Where("agent_id = ?", "old-agent").First(&agent).Error; err != nil {
		t.Fatal(err)
	}
	if len(agent.RuntimeCapabilities) != 0 {
		t.Fatal("historical labels became a readiness report")
	}
}

func TestRuntimeStepClaimIsFencedInTheStatusTransaction(t *testing.T) {
	ctx, db, jobs, source := sourceTestRepository(t)
	runtime := repoRuntimeRequest(t)
	if err := db.WithContext(ctx).Create(&model.StepRun{StepRunID: "step", JobRunID: "job", PipelineID: "pipeline", PipelineRunID: "run", Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := jobs.CaptureRuntimeExpectation(ctx, "pipeline", "run", "job", "agent", source, runtime); err != nil {
		t.Fatal(err)
	}
	steps := &StepRunRepo{IDatabase: statusTestDB{db}}
	step, err := steps.GetByStepRunID(ctx, "step")
	if err != nil {
		t.Fatal(err)
	}
	claim := agentidentity.StepClaimID("original", "step")
	if err := steps.PatchReportedStatus(ctx, step, map[string]any{"status": 3, "claim_id": claim}); err == nil {
		t.Fatal("step activated without parent execution claim")
	}
	job, _ := jobs.GetByJobRunID(ctx, "job")
	if err := jobs.PatchReportedStatus(ctx, job, map[string]any{"status": 3, "claim_id": "original"}); err != nil {
		t.Fatal(err)
	}
	if err := steps.PatchReportedStatus(ctx, step, map[string]any{"status": 3, "claim_id": "standalone"}); err == nil {
		t.Fatal("independent runtime step claim accepted")
	}
	if err := steps.PatchReportedStatus(ctx, step, map[string]any{"status": 3, "claim_id": claim}); err != nil {
		t.Fatal(err)
	}
	step, _ = steps.GetByStepRunID(ctx, "step")
	if err := db.WithContext(ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("claim_id", "replacement").Error; err != nil {
		t.Fatal(err)
	}
	if err := steps.PatchReportedStatus(ctx, step, map[string]any{"status": 5, "claim_id": claim}); err == nil {
		t.Fatal("step report crossed replaced parent claim")
	}
}
