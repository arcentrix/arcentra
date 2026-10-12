// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package process

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/storage"
	"github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/bytedance/sonic"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type cleanupArtifactRepository struct {
	repo.IArtifactRepository
	entries                []model.ProjectArtifact
	listCalls, deleteCalls int
	deleteError            error
	deleteChanged          bool
	projectID, runID       string
	before                 time.Time
	limit                  int
	events                 *[]string
}

func (r *cleanupArtifactRepository) ListExpired(ctx context.Context, projectID, runID string, before time.Time, limit int) ([]model.ProjectArtifact, error) {
	r.listCalls++
	r.projectID, r.runID, r.before, r.limit = projectID, runID, before, limit
	return r.entries, ctx.Err()
}

func (r *cleanupArtifactRepository) DeleteVersion(ctx context.Context, record *model.ProjectArtifact) (bool, error) {
	r.deleteCalls++
	if r.events != nil {
		*r.events = append(*r.events, "metadata:"+record.ArtifactID)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if r.deleteError != nil {
		return false, r.deleteError
	}
	return !r.deleteChanged, nil
}

type cleanupPipelineRepository struct {
	repo.IPipelineRepository
	run      *model.PipelineRun
	pipeline *model.Pipeline
}

func (r *cleanupPipelineRepository) GetRun(ctx context.Context, _ string) (*model.PipelineRun, error) {
	return r.run, ctx.Err()
}
func (r *cleanupPipelineRepository) Get(ctx context.Context, _ string) (*model.Pipeline, error) {
	return r.pipeline, ctx.Err()
}

type cleanupStorage struct {
	storage.IStorage
	keys   []string
	err    error
	events *[]string
}

func (s *cleanupStorage) Delete(ctx context.Context, key string) error {
	s.keys = append(s.keys, key)
	if s.events != nil {
		*s.events = append(*s.events, "object:"+key)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.err
}

func cleanupRecord(projectID, runID, name string) model.ProjectArtifact {
	sum := sha256.Sum256([]byte("artifact payload"))
	expires := time.Now().UTC().Add(-2 * time.Hour)
	record := model.ProjectArtifact{
		ProjectID: projectID, PipelineID: "pipeline-" + projectID, PipelineRunID: runID,
		JobRunID: "job-" + runID, StepRunID: "step-" + runID, Name: name,
		StorageType: "s3", StorageID: "original-storage", StorageFingerprint: strings.Repeat("f", 64),
		Size: int64(len("artifact payload")), SHA256: hex.EncodeToString(sum[:]), ExpiresAt: &expires,
	}
	record.ArtifactID = artifact.ID(record.ProjectID, record.PipelineRunID, record.JobRunID, record.StepRunID, record.Name)
	record.StoragePath, _ = artifact.RemotePath(record.PipelineRunID, record.JobRunID, record.StepRunID, record.SHA256, record.Name)
	return record
}

func cleanupFixture(record model.ProjectArtifact) (*ArtifactCleaner, *cleanupArtifactRepository, *cleanupPipelineRepository, *cleanupStorage) {
	artifacts := &cleanupArtifactRepository{entries: []model.ProjectArtifact{record}}
	pipelines := &cleanupPipelineRepository{
		run:      &model.PipelineRun{RunID: record.PipelineRunID, PipelineID: record.PipelineID, Status: model.PipelineStatusSuccess},
		pipeline: &model.Pipeline{PipelineID: record.PipelineID, ProjectID: record.ProjectID},
	}
	objects := &cleanupStorage{}
	cleaner := &ArtifactCleaner{Artifacts: artifacts, Pipelines: pipelines, ResolveStorage: func(context.Context, *model.ProjectArtifact) (storage.IStorage, error) { return objects, nil }}
	return cleaner, artifacts, pipelines, objects
}

func cleanupOptions(record model.ProjectArtifact) ArtifactCleanupOptions {
	return ArtifactCleanupOptions{ProjectID: record.ProjectID, RunID: record.PipelineRunID, Before: time.Now().UTC().Add(-time.Hour), Limit: 25, Apply: true, ExecutorsStopped: true}
}

func TestArtifactCleanupDryRunDoesNotResolveOrMutate(t *testing.T) {
	record := cleanupRecord("project", "run", "dist/result")
	cleaner, records, _, objects := cleanupFixture(record)
	resolved := 0
	cleaner.ResolveStorage = func(context.Context, *model.ProjectArtifact) (storage.IStorage, error) {
		resolved++
		return objects, nil
	}
	options := cleanupOptions(record)
	options.Apply, options.ExecutorsStopped = false, false
	result, err := cleaner.Prune(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || len(result.Items) != 1 || result.Items[0].Outcome != "would-delete" || result.Deleted != 0 {
		t.Fatalf("unexpected preview: %+v", result)
	}
	if records.deleteCalls != 0 || resolved != 0 || len(objects.keys) != 0 {
		t.Fatal("dry run touched object storage or metadata")
	}
	if records.projectID != options.ProjectID || records.runID != options.RunID || !records.before.Equal(options.Before) || records.limit != options.Limit {
		t.Fatalf("scope not forwarded: %+v", records)
	}
	encoded, err := sonic.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{record.StoragePath, record.StorageID, record.StorageFingerprint, record.SHA256} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("report disclosed internal binding: %s", encoded)
		}
	}
}

func TestArtifactCleanupDeletesObjectBeforeExactMetadataVersion(t *testing.T) {
	record := cleanupRecord("project", "run", "dist/result")
	cleaner, records, _, objects := cleanupFixture(record)
	var events []string
	records.events, objects.events = &events, &events
	cleaner.ResolveStorage = func(_ context.Context, got *model.ProjectArtifact) (storage.IStorage, error) {
		if !reflect.DeepEqual(*got, record) {
			t.Fatalf("storage resolver received different persisted binding: %+v", got)
		}
		return objects, nil
	}
	result, err := cleaner.Prune(context.Background(), cleanupOptions(record))
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 1 || result.Failed != 0 || result.DryRun {
		t.Fatalf("unexpected result: %+v", result)
	}
	if want := []string{"object:" + record.StoragePath, "metadata:" + record.ArtifactID}; !reflect.DeepEqual(events, want) {
		t.Fatalf("deletion order %v, want %v", events, want)
	}
}

func TestArtifactCleanupFailuresRetainMetadata(t *testing.T) {
	for _, failure := range []string{"object-delete", "storage-resolution", "nil-storage", "missing-resolver", "identity-mismatch", "metadata-delete", "metadata-changed"} {
		t.Run(failure, func(t *testing.T) {
			record := cleanupRecord("project", "run", "dist/result")
			cleaner, records, _, objects := cleanupFixture(record)
			switch failure {
			case "object-delete":
				objects.err = errors.New("provider-secret-must-not-leak")
			case "storage-resolution":
				cleaner.ResolveStorage = func(context.Context, *model.ProjectArtifact) (storage.IStorage, error) {
					return nil, errors.New("credential-secret-must-not-leak")
				}
			case "nil-storage":
				cleaner.ResolveStorage = func(context.Context, *model.ProjectArtifact) (storage.IStorage, error) { return nil, nil }
			case "missing-resolver":
				cleaner.ResolveStorage = nil
			case "identity-mismatch":
				records.entries[0].ArtifactID = "wrong-id"
			case "metadata-delete":
				records.deleteError = errors.New("database-secret-must-not-leak")
			case "metadata-changed":
				records.deleteChanged = true
			}
			result, err := cleaner.Prune(context.Background(), cleanupOptions(record))
			if err != nil {
				t.Fatal(err)
			}
			if result.Deleted != 0 || len(result.Items) != 1 {
				t.Fatalf("unexpected result: %+v", result)
			}
			if failure == "metadata-changed" {
				if result.Skipped != 1 {
					t.Fatalf("changed version removed: %+v", result)
				}
			} else if result.Failed != 1 {
				t.Fatalf("failure not reported: %+v", result)
			}
			if failure != "metadata-delete" && failure != "metadata-changed" && records.deleteCalls != 0 {
				t.Fatal("metadata deletion attempted after object failure")
			}
			encoded, _ := sonic.Marshal(result)
			if strings.Contains(string(encoded), "must-not-leak") {
				t.Fatalf("error disclosed provider details: %s", encoded)
			}
		})
	}
}

func TestArtifactCleanupPreservesInvalidScopeAndOwnership(t *testing.T) {
	for _, invalid := range []string{"other-project", "other-run", "indefinite", "not-expired", "unknown-run", "wrong-run", "wrong-pipeline", "missing-pipeline", "other-pipeline-project", "deleted-pipeline", "pending", "running", "paused", "unknown-status"} {
		t.Run(invalid, func(t *testing.T) {
			record := cleanupRecord("project", "run", "dist/result")
			cleaner, records, pipelines, objects := cleanupFixture(record)
			options := cleanupOptions(record)
			switch invalid {
			case "other-project":
				records.entries[0].ProjectID = "other"
			case "other-run":
				records.entries[0].PipelineRunID = "other"
			case "indefinite":
				records.entries[0].ExpiresAt = nil
			case "not-expired":
				expires := options.Before.Add(time.Hour)
				records.entries[0].ExpiresAt = &expires
			case "unknown-run":
				pipelines.run = nil
			case "wrong-run":
				pipelines.run.RunID = "other"
			case "wrong-pipeline":
				pipelines.run.PipelineID = "other"
			case "missing-pipeline":
				pipelines.pipeline = nil
			case "other-pipeline-project":
				pipelines.pipeline.ProjectID = "other"
			case "deleted-pipeline":
				pipelines.pipeline.IsDeleted = 1
			case "pending":
				pipelines.run.Status = model.PipelineStatusPending
			case "running":
				pipelines.run.Status = model.PipelineStatusRunning
			case "paused":
				pipelines.run.Status = model.PipelineStatusPaused
			case "unknown-status":
				pipelines.run.Status = model.PipelineStatusUnknown
			}
			result, err := cleaner.Prune(context.Background(), options)
			if err == nil && (result.Skipped != 1 || result.Deleted != 0) {
				t.Fatalf("unsafe row not preserved: %+v", result)
			}
			if records.deleteCalls != 0 || len(objects.keys) != 0 {
				t.Fatal("out-of-scope or active artifact was deleted")
			}
		})
	}
	for _, status := range []int{model.PipelineStatusSuccess, model.PipelineStatusFailed, model.PipelineStatusCancelled} {
		record := cleanupRecord("project", "run", "dist/result")
		cleaner, _, pipelines, _ := cleanupFixture(record)
		pipelines.run.Status = status
		result, err := cleaner.Prune(context.Background(), cleanupOptions(record))
		if err != nil || result.Deleted != 1 {
			t.Fatalf("terminal status %d not eligible: %+v, %v", status, result, err)
		}
	}
}

func TestArtifactCleanupRequiresExplicitSafeOptions(t *testing.T) {
	for _, invalid := range []string{"missing-project", "project-traversal", "run-traversal", "future-cutoff", "negative-limit", "excessive-limit", "executors-active"} {
		t.Run(invalid, func(t *testing.T) {
			record := cleanupRecord("project", "run", "dist/result")
			cleaner, records, _, _ := cleanupFixture(record)
			options := cleanupOptions(record)
			switch invalid {
			case "missing-project":
				options.ProjectID = ""
			case "project-traversal":
				options.ProjectID = "../other"
			case "run-traversal":
				options.RunID = "../other"
			case "future-cutoff":
				options.Before = time.Now().Add(time.Hour)
			case "negative-limit":
				options.Limit = -1
			case "excessive-limit":
				options.Limit = 501
			case "executors-active":
				options.ExecutorsStopped = false
			}
			if _, err := cleaner.Prune(context.Background(), options); err == nil {
				t.Fatal("unsafe options accepted")
			}
			if records.listCalls != 0 || records.deleteCalls != 0 {
				t.Fatal("invalid request reached repository")
			}
		})
	}
}

func TestArtifactCleanupLocalContentValidation(t *testing.T) {
	for _, test := range []string{"valid", "missing-file", "missing-root", "bad-path", "traversal-path", "wrong-size", "wrong-digest", "empty-digest", "invalid-digest", "uppercase-digest", "symlink-file", "symlink-parent"} {
		t.Run(test, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("ARCENTRA_ARTIFACT_ROOT", root)
			record := cleanupRecord("project", "run", "dist/result")
			record.StorageType, record.StorageID, record.StorageFingerprint = "local", "", ""
			record.StoragePath, _ = artifact.LocalPath(record.ProjectID, record.PipelineRunID, record.JobRunID, record.StepRunID, record.Name)
			filename := filepath.Join(root, filepath.FromSlash(record.StoragePath))
			if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filename, []byte("artifact payload"), 0o644); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("artifact payload"), 0o644); err != nil {
				t.Fatal(err)
			}
			switch test {
			case "missing-file":
				if err := os.Remove(filename); err != nil {
					t.Fatal(err)
				}
			case "missing-root":
				t.Setenv("ARCENTRA_ARTIFACT_ROOT", filepath.Join(root, "not-mounted"))
			case "bad-path":
				record.StoragePath = "different/place"
			case "traversal-path":
				record.StoragePath = "../outside"
			case "wrong-size":
				record.Size++
			case "wrong-digest":
				record.SHA256 = strings.Repeat("a", 64)
			case "empty-digest":
				record.SHA256 = ""
			case "invalid-digest":
				record.SHA256 = strings.Repeat("z", 64)
			case "uppercase-digest":
				record.SHA256 = strings.ToUpper(record.SHA256)
			case "symlink-file":
				if err := os.Remove(filename); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filename); err != nil {
					t.Fatal(err)
				}
			case "symlink-parent":
				if err := os.Remove(filename); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Dir(filename)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Dir(outside), filepath.Dir(filename)); err != nil {
					t.Fatal(err)
				}
			}
			cleaner, records, _, objects := cleanupFixture(record)
			result, err := cleaner.Prune(context.Background(), cleanupOptions(record))
			if err != nil {
				t.Fatal(err)
			}
			if test == "valid" || test == "missing-file" {
				if result.Deleted != 1 || records.deleteCalls != 1 {
					t.Fatalf("valid local cleanup failed: %+v", result)
				}
				if _, err := os.Stat(filename); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("file remains: %v", err)
				}
			} else {
				if result.Failed != 1 || records.deleteCalls != 0 {
					t.Fatalf("unsafe local deletion: %+v", result)
				}
				if test != "symlink-parent" {
					if _, err := os.Lstat(filename); err != nil {
						t.Fatalf("invalid local file was removed: %v", err)
					}
				}
			}
			if len(objects.keys) != 0 {
				t.Fatal("local cleanup called remote storage")
			}
			if content, err := os.ReadFile(outside); err != nil || string(content) != "artifact payload" {
				t.Fatal("cleanup touched outside file")
			}
		})
	}
}

func TestArtifactCleanupCancellationDoesNotDelete(t *testing.T) {
	record := cleanupRecord("project", "run", "dist/result")
	cleaner, records, _, objects := cleanupFixture(record)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cleaner.Prune(ctx, cleanupOptions(record)); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancellation", err)
	}
	if records.deleteCalls != 0 || len(objects.keys) != 0 {
		t.Fatal("canceled cleanup deleted data")
	}
}

func TestArtifactCleanupSQLiteKeepsOtherScopesAndIndefiniteRetention(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "cleanup.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	// 仅测试数据库显式建表；清理入口自身绝不迁移
	if err := db.AutoMigrate(&model.Project{}, &model.Pipeline{}, &model.PipelineRun{}, &model.JobRun{}, &model.StepRun{}, &model.ProjectArtifact{}); err != nil {
		t.Fatal(err)
	}
	adapter := processTestDB{db: db}
	artifacts, pipelines := repo.NewArtifactRepo(adapter), repo.NewPipelineRepo(adapter)
	for _, projectID := range []string{"a", "b"} {
		for _, row := range []any{&model.Project{ProjectID: projectID}, &model.Pipeline{ProjectID: projectID, PipelineID: "pipeline-" + projectID}} {
			if err := db.WithContext(context.Background()).Create(row).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	var values []model.ProjectArtifact
	for _, test := range []struct {
		project, run       string
		status             int
		indefinite, future bool
	}{
		{"a", "eligible", model.PipelineStatusSuccess, false, false},
		{"a", "active", model.PipelineStatusRunning, false, false},
		{"a", "indefinite", model.PipelineStatusSuccess, true, false},
		{"a", "future", model.PipelineStatusSuccess, false, true},
		{"b", "other-project", model.PipelineStatusSuccess, false, false},
	} {
		record := cleanupRecord(test.project, test.run, "dist/result")
		if test.indefinite {
			record.ExpiresAt = nil
		}
		if test.future {
			expiry := time.Now().Add(time.Hour)
			record.ExpiresAt = &expiry
		}
		for _, row := range []any{
			&model.PipelineRun{RunID: record.PipelineRunID, PipelineID: record.PipelineID, Status: test.status},
			&model.JobRun{JobRunID: record.JobRunID, PipelineID: record.PipelineID, PipelineRunID: record.PipelineRunID},
			&model.StepRun{StepRunID: record.StepRunID, JobRunID: record.JobRunID, PipelineID: record.PipelineID, PipelineRunID: record.PipelineRunID},
		} {
			if err := db.WithContext(context.Background()).Create(row).Error; err != nil {
				t.Fatal(err)
			}
		}
		if err := artifacts.Upsert(context.Background(), &record); err != nil {
			t.Fatal(err)
		}
		values = append(values, record)
	}
	objects := &cleanupStorage{}
	cleaner := &ArtifactCleaner{Artifacts: artifacts, Pipelines: pipelines, ResolveStorage: func(context.Context, *model.ProjectArtifact) (storage.IStorage, error) { return objects, nil }}
	options := cleanupOptions(values[0])
	options.RunID = ""
	preview := options
	preview.Apply = false
	if result, err := cleaner.Prune(context.Background(), preview); err != nil || result.Deleted != 0 || len(objects.keys) != 0 {
		rows, readErr := artifacts.ListExpired(context.Background(), options.ProjectID, options.RunID, options.Before, options.Limit)
		for _, row := range rows {
			t.Logf("candidate project=%q run=%q expiry=%v cutoff=%v", row.ProjectID, row.PipelineRunID, row.ExpiresAt, options.Before)
		}
		t.Fatalf("preview mutated: %+v %v readErr=%v", result, err, readErr)
	}
	for _, record := range values {
		if got, err := artifacts.Get(context.Background(), record.ProjectID, record.ArtifactID); err != nil || got == nil {
			t.Fatal("preview removed metadata")
		}
	}
	result, err := cleaner.Prune(context.Background(), options)
	if err != nil || result.Deleted != 1 || result.Skipped != 1 || len(objects.keys) != 1 {
		t.Fatalf("cleanup: %+v %v", result, err)
	}
	for i, record := range values {
		got, err := artifacts.Get(context.Background(), record.ProjectID, record.ArtifactID)
		if err != nil || (i == 0 && got != nil) || (i != 0 && got == nil) {
			t.Fatalf("wrong metadata retention for %s: %+v %v", record.PipelineRunID, got, err)
		}
	}
}
