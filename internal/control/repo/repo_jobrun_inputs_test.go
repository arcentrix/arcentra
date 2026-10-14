package repo

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func inputRepoFixture(t *testing.T, withSource bool) (context.Context, *gorm.DB, *JobRunRepo, taskqueue.JobInputIdentity, *taskqueue.JobInputManifest) {
	t.Helper()
	ctx, db, r, source := sourceTestRepository(t)
	if err := db.WithContext(ctx).AutoMigrate(&model.Agent{}, &model.PipelineRun{}, &model.Pipeline{}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{&model.Agent{AgentID: "agent", IsEnabled: 1}, &model.PipelineRun{RunID: "run", PipelineID: "pipeline", Status: model.PipelineStatusRunning}, &model.Pipeline{PipelineID: "pipeline", ProjectID: "project"}} {
		if err := db.WithContext(ctx).Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	if !withSource {
		source = nil
	}
	runtime := repoRuntimeRequest(t)
	if err := r.CaptureRuntimeInputExpectation(ctx, "pipeline", "run", "job", "agent", source, runtime, nil, nil); err != nil {
		t.Fatal(err)
	}
	current, _ := r.GetByJobRunID(ctx, "job")
	if err := r.PatchReportedStatus(ctx, current, map[string]any{"status": 3, "claim_id": "claim"}); err != nil {
		t.Fatal(err)
	}
	identity := taskqueue.JobInputIdentity{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", AgentID: "agent", ClaimID: "claim", RuntimeBindingDigest: runtime.BindingDigest}
	commit := ""
	if source != nil {
		identity.SourceBindingDigest = source.Digest
		commit = strings.Repeat("a", 40)
	}
	manifest, err := taskqueue.NewJobInputManifest(commit, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, db, r, identity, manifest
}

func TestJobInputsFirstPinReplayAndSeparateProvenance(t *testing.T) {
	for _, source := range []bool{false, true} {
		t.Run(map[bool]string{false: "source-free", true: "git"}[source], func(t *testing.T) {
			ctx, _, r, i, m := inputRepoFixture(t, source)
			before, err := r.GetOrRecordJobInputs(ctx, i, nil)
			if err != nil || before.InputManifestState != "unknown" {
				t.Fatalf("initial state: %+v %v", before, err)
			}
			first, err := r.GetOrRecordJobInputs(ctx, i, m)
			if err != nil {
				t.Fatal(err)
			}
			persisted, _ := r.GetByJobRunID(ctx, "job")
			if persisted.InputManifestState != "pinned" || persisted.InputManifestDigest == "" || persisted.SourceResolvedCommit != "" || persisted.SourceProvenanceState != "unknown" || persisted.SourceBindingDigest != i.SourceBindingDigest {
				t.Fatalf("pin changed old provenance: %+v", persisted)
			}
			for _, proposed := range []*taskqueue.JobInputManifest{nil, m, m} {
				got, err := r.GetOrRecordJobInputs(ctx, i, proposed)
				if err != nil || got.InputManifest != first.InputManifest {
					t.Fatalf("replay: %+v %v", got, err)
				}
			}
			after, _ := r.GetByJobRunID(ctx, "job")
			if !reflect.DeepEqual(persisted, after) {
				t.Fatal("identical replay mutated job")
			}
			changed := *m
			changed.SourceCommit = strings.Repeat("b", 40)
			if _, err := r.GetOrRecordJobInputs(ctx, i, &changed); err == nil {
				t.Fatal("pin was replaced")
			}
		})
	}
}

func TestJobInputsRequireExactLiveIdentity(t *testing.T) {
	mutations := map[string]func(*gorm.DB, *taskqueue.JobInputIdentity){
		"agent": func(_ *gorm.DB, i *taskqueue.JobInputIdentity) { i.AgentID = "Agent" }, "claim": func(_ *gorm.DB, i *taskqueue.JobInputIdentity) { i.ClaimID = "Claim" }, "pipeline": func(_ *gorm.DB, i *taskqueue.JobInputIdentity) { i.PipelineID = "Pipeline" }, "run": func(_ *gorm.DB, i *taskqueue.JobInputIdentity) { i.PipelineRunID = "Run" }, "job": func(_ *gorm.DB, i *taskqueue.JobInputIdentity) { i.JobRunID = "Job" }, "source": func(_ *gorm.DB, i *taskqueue.JobInputIdentity) {
			i.SourceBindingDigest = "v1:" + strings.Repeat("b", 64)
		}, "runtime": func(_ *gorm.DB, i *taskqueue.JobInputIdentity) {
			i.RuntimeBindingDigest = "v1:" + strings.Repeat("b", 64)
		},
		"job-ended": func(db *gorm.DB, _ *taskqueue.JobInputIdentity) {
			db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("status", 6)
		}, "run-ended": func(db *gorm.DB, _ *taskqueue.JobInputIdentity) {
			db.Model(&model.PipelineRun{}).Where("run_id = ?", "run").Update("status", model.PipelineStatusCancelled)
		}, "agent-disabled": func(db *gorm.DB, _ *taskqueue.JobInputIdentity) {
			db.Model(&model.Agent{}).Where("agent_id = ?", "agent").Update("is_enabled", 0)
		}, "legacy-expectation": func(db *gorm.DB, _ *taskqueue.JobInputIdentity) {
			db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("input_expectation", "")
		},
	}
	for name, change := range mutations {
		t.Run(name, func(t *testing.T) {
			ctx, db, r, i, m := inputRepoFixture(t, true)
			change(db, &i)
			for _, proposal := range []*taskqueue.JobInputManifest{nil, m} {
				if _, err := r.GetOrRecordJobInputs(ctx, i, proposal); err == nil {
					t.Fatal("invalid live identity accepted")
				}
			}
			j, _ := r.GetByJobRunID(ctx, "job")
			if j.InputManifestState != "unknown" {
				t.Fatal("failed request mutated pin")
			}
		})
	}
}

func TestJobInputsGenericWritesAndStaleSnapshotsCannotMutatePin(t *testing.T) {
	ctx, _, r, i, m := inputRepoFixture(t, true)
	stale, _ := r.GetByJobRunID(ctx, "job")
	for _, field := range append(jobInputColumns, "InputManifest", "InputExpectation") {
		for _, write := range []func(map[string]any) error{func(u map[string]any) error { return r.UpdateByJobRunID(ctx, "job", u) }, func(u map[string]any) error { return r.PatchStatusIfCurrent(ctx, "job", 3, u) }, func(u map[string]any) error { return r.PatchReportedStatus(ctx, stale, u) }} {
			if err := write(map[string]any{field: "forged"}); err == nil {
				t.Fatalf("generic write accepted %s", field)
			}
		}
	}
	if _, err := r.GetOrRecordJobInputs(ctx, i, m); err != nil {
		t.Fatal(err)
	}
	if err := r.PatchReportedStatus(ctx, stale, map[string]any{"status": 4, "claim_id": "claim", "terminal_report_digest": strings.Repeat("a", 64)}); err == nil {
		t.Fatal("stale terminal snapshot ignored manifest change")
	}
}

func seedInputArtifact(t *testing.T, ctx context.Context, db *gorm.DB) (map[string]string, map[string]taskqueue.ArtifactReference) {
	t.Helper()
	hash := strings.Repeat("a", 64)
	key := "artifacts/run/producer/producer-step/" + hash + "/result.txt"
	size := int64(0)
	for _, value := range []any{&model.Project{ProjectID: "project"}, &model.Pipeline{PipelineID: "pipeline", ProjectID: "project"}, &model.JobRun{JobRunID: "producer", JobName: "producer", PipelineID: "pipeline", PipelineRunID: "run", Status: 4}, &model.StepRun{StepRunID: "producer-step", JobRunID: "producer", PipelineID: "pipeline", PipelineRunID: "run", Status: 4}, &model.ProjectArtifact{ArtifactID: "artifact", ProjectID: "project", PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "producer", StepRunID: "producer-step", Name: "result.txt", StorageID: "store", StorageFingerprint: strings.Repeat("b", 64), StoragePath: key, Size: size, SHA256: hash}} {
		if err := db.WithContext(ctx).Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	return map[string]string{"producer/result.txt": key}, map[string]taskqueue.ArtifactReference{"producer/result.txt": {StorageID: "store", Fingerprint: strings.Repeat("b", 64), SHA256: hash, Size: &size}}
}

func TestJobInputsDispatchRejectsUnregisteredOrForeignArtifactsAtomically(t *testing.T) {
	mutations := map[string]func(*gorm.DB, map[string]string, map[string]taskqueue.ArtifactReference){"valid": func(*gorm.DB, map[string]string, map[string]taskqueue.ArtifactReference) {}, "foreign-project": func(db *gorm.DB, _ map[string]string, _ map[string]taskqueue.ArtifactReference) {
		db.Model(&model.ProjectArtifact{}).Where("artifact_id = ?", "artifact").Update("project_id", "other")
	}, "foreign-run": func(db *gorm.DB, _ map[string]string, _ map[string]taskqueue.ArtifactReference) {
		db.Model(&model.ProjectArtifact{}).Where("artifact_id = ?", "artifact").Update("pipeline_run_id", "other")
	}, "foreign-step": func(db *gorm.DB, _ map[string]string, _ map[string]taskqueue.ArtifactReference) {
		db.Model(&model.StepRun{}).Where("step_run_id = ?", "producer-step").Update("job_run_id", "other")
	}, "producer-running": func(db *gorm.DB, _ map[string]string, _ map[string]taskqueue.ArtifactReference) {
		db.Model(&model.JobRun{}).Where("job_run_id = ?", "producer").Update("status", 3)
	}, "unknown": func(db *gorm.DB, _ map[string]string, _ map[string]taskqueue.ArtifactReference) {
		db.Where("artifact_id = ?", "artifact").Delete(&model.ProjectArtifact{})
	}, "size": func(_ *gorm.DB, _ map[string]string, refs map[string]taskqueue.ArtifactReference) {
		*refs["producer/result.txt"].Size = 5
	}, "expired": func(db *gorm.DB, _ map[string]string, refs map[string]taskqueue.ArtifactReference) {
		expiry := time.Now().Add(-time.Minute)
		db.Model(&model.ProjectArtifact{}).Where("artifact_id = ?", "artifact").Update("expires_at", expiry)
		ref := refs["producer/result.txt"]
		ref.ExpiresAt = expiry.Unix()
		refs["producer/result.txt"] = ref
	}}
	for name, change := range mutations {
		t.Run(name, func(t *testing.T) {
			ctx, db, r, source := sourceTestRepository(t)
			if err := db.WithContext(ctx).AutoMigrate(&model.Pipeline{}, &model.Project{}); err != nil {
				t.Fatal(err)
			}
			uris, refs := seedInputArtifact(t, ctx, db)
			change(db, uris, refs)
			err := r.CaptureRuntimeInputExpectation(ctx, "pipeline", "run", "job", "agent", source, repoRuntimeRequest(t), uris, refs)
			if name == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid producer accepted")
			}
			job, _ := r.GetByJobRunID(ctx, "job")
			if job.SourceExpectationCaptured || job.InputExpectation != "" || job.AgentID != "" {
				t.Fatal("invalid artifact partially dispatched")
			}
		})
	}
}

func TestJobInputsMigrationNeverPromotesTerminalEvidence(t *testing.T) {
	ctx, db, r, _ := sourceTestRepository(t)
	for _, field := range jobInputColumns {
		if err := db.WithContext(ctx).Migrator().DropColumn(&model.JobRun{}, field); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.WithContext(ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(map[string]any{"source_resolved_commit": strings.Repeat("a", 40), "source_provenance_state": "reported", "status": 4}).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := MigrateSchema(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	job, _ := r.GetByJobRunID(ctx, "job")
	if job.InputManifestState != "unknown" || job.InputExpectation != "" || job.InputManifest != "" || job.InputManifestDigest != "" || job.SourceResolvedCommit != strings.Repeat("a", 40) {
		t.Fatalf("legacy record promoted: %+v", job)
	}
}

func TestJobInputsRejectsChangedAuthorityAfterPin(t *testing.T) {
	for _, field := range []string{"status", "claim_id", "agent_id", "pipeline_id", "runtime_binding_digest", "source_binding_digest", "input_expectation", "input_manifest", "input_manifest_digest"} {
		t.Run(field, func(t *testing.T) {
			ctx, db, r, i, m := inputRepoFixture(t, true)
			if _, err := r.GetOrRecordJobInputs(ctx, i, m); err != nil {
				t.Fatal(err)
			}
			value := any("changed")
			if field == "status" {
				value = 6
			}
			if err := db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update(field, value).Error; err != nil {
				t.Fatal(err)
			}
			for _, proposed := range []*taskqueue.JobInputManifest{nil, m} {
				if _, err := r.GetOrRecordJobInputs(ctx, i, proposed); err == nil {
					t.Fatal("changed pinned authority accepted")
				}
			}
		})
	}
}

func TestJobInputsConcurrentFirstPinsHaveOneWinner(t *testing.T) {
	ctx, _, r, i, first := inputRepoFixture(t, true)
	second := *first
	second.SourceCommit = strings.Repeat("b", 40)
	start := make(chan struct{})
	done := make(chan error, 2)
	for _, proposed := range []*taskqueue.JobInputManifest{first, &second} {
		candidate := proposed
		safe.Go(func() { <-start; _, err := r.GetOrRecordJobInputs(ctx, i, candidate); done <- err })
	}
	close(start)
	successes := 0
	for range 2 {
		if <-done == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("first pin winners=%d", successes)
	}
	stored, err := r.GetOrRecordJobInputs(ctx, i, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := taskqueue.ParseJobInputManifest(stored.InputManifest)
	if err != nil {
		t.Fatal(err)
	}
	if m.SourceCommit != first.SourceCommit && m.SourceCommit != second.SourceCommit {
		t.Fatal("CAS stored neither candidate")
	}
	if _, err := r.GetOrRecordJobInputs(ctx, i, m); err != nil {
		t.Fatal("winning pin was not idempotent")
	}
}

func TestJobInputsManifestSchemaSupportsBoundedLargeManifest(t *testing.T) {
	parsed, err := schema.Parse(&model.JobRun{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	dialect := mysql.Dialector{Config: &mysql.Config{}}
	for _, name := range []string{"InputExpectation", "InputManifest"} {
		field := parsed.LookUpField(name)
		if field == nil || dialect.DataTypeOf(field) != "mediumtext" || field.DefaultValue != "('')" {
			t.Fatalf("MySQL field %s cannot hold bounded manifest with compatible default: %+v", name, field)
		}
	}
	uris := map[string]string{}
	refs := map[string]taskqueue.ArtifactReference{}
	for index := range 128 {
		name := fmt.Sprintf("producer/%03d/%s", index, strings.Repeat("x", 500))
		size := int64(index)
		uris[name] = "artifacts/run/producer/step/" + strings.Repeat("a", 64) + "/" + strings.TrimPrefix(name, "producer/")
		refs[name] = taskqueue.ArtifactReference{StorageID: "store", Fingerprint: strings.Repeat("b", 64), SHA256: strings.Repeat("a", 64), Size: &size}
	}
	manifest, err := taskqueue.NewJobInputManifest("", uris, refs)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := manifest.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) <= 65535 {
		t.Fatal("fixture does not exercise former TEXT limit")
	}
	ctx, db, r, _ := sourceTestRepository(t)
	if err := db.WithContext(ctx).Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(map[string]any{"input_expectation": encoded, "input_manifest": encoded}).Error; err != nil {
		t.Fatal(err)
	}
	job, err := r.GetByJobRunID(ctx, "job")
	if err != nil || job.InputExpectation != encoded || job.InputManifest != encoded {
		t.Fatal("large manifest was truncated")
	}
}

func TestJobInputsPinsOnlyControllerSelectedArtifactSetAndRechecksExpiry(t *testing.T) {
	ctx, db, r, source := sourceTestRepository(t)
	if err := db.WithContext(ctx).AutoMigrate(&model.Pipeline{}, &model.Project{}, &model.Agent{}, &model.PipelineRun{}); err != nil {
		t.Fatal(err)
	}
	uris, refs := seedInputArtifact(t, ctx, db)
	for _, value := range []any{&model.Agent{AgentID: "agent", IsEnabled: 1}, &model.PipelineRun{RunID: "run", PipelineID: "pipeline", Status: model.PipelineStatusRunning}} {
		if err := db.WithContext(ctx).Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	runtime := repoRuntimeRequest(t)
	if err := r.CaptureRuntimeInputExpectation(ctx, "pipeline", "run", "job", "agent", source, runtime, uris, refs); err != nil {
		t.Fatal(err)
	}
	job, _ := r.GetByJobRunID(ctx, "job")
	if err := r.PatchReportedStatus(ctx, job, map[string]any{"status": 3, "claim_id": "claim"}); err != nil {
		t.Fatal(err)
	}
	identity := taskqueue.JobInputIdentity{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", AgentID: "agent", ClaimID: "claim", RuntimeBindingDigest: runtime.BindingDigest, SourceBindingDigest: source.Digest}
	for _, altered := range []bool{false, true} {
		candidate, err := taskqueue.NewJobInputManifest(strings.Repeat("a", 40), uris, refs)
		if err != nil {
			t.Fatal(err)
		}
		if altered {
			candidate.Artifacts[0].Name = "other/result.txt"
		} else {
			candidate.Artifacts = []taskqueue.JobInputArtifact{}
		}
		if _, err := r.GetOrRecordJobInputs(ctx, identity, candidate); err == nil {
			t.Fatal("agent changed controller-selected set")
		}
	}
	selected, err := taskqueue.NewJobInputManifest(strings.Repeat("a", 40), uris, refs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.GetOrRecordJobInputs(ctx, identity, selected); err != nil {
		t.Fatal(err)
	}
	before, _ := r.GetByJobRunID(ctx, "job")
	if err := db.WithContext(ctx).Model(&model.ProjectArtifact{}).Where("artifact_id = ?", "artifact").Update("expires_at", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []*taskqueue.JobInputManifest{nil, selected} {
		if _, err := r.GetOrRecordJobInputs(ctx, identity, candidate); err == nil {
			t.Fatal("expired or changed registered input reused")
		}
	}
	after, _ := r.GetByJobRunID(ctx, "job")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("expired input check rewrote manifest")
	}
}
