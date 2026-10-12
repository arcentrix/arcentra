package repo

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type artifactTestDatabase struct{ db *gorm.DB }

func (d artifactTestDatabase) Database() *gorm.DB { return d.db }
func artifactRepositoryFixture(t *testing.T) (*gorm.DB, IArtifactRepository) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "artifacts.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.Project{}, &model.Pipeline{}, &model.PipelineRun{}, &model.JobRun{}, &model.StepRun{}, &model.ProjectArtifact{}); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"a", "b"} {
		for _, value := range []any{&model.Project{ProjectID: scope}, &model.Pipeline{ProjectID: scope, PipelineID: "p-" + scope}, &model.PipelineRun{RunID: "r-" + scope, PipelineID: "p-" + scope}, &model.JobRun{JobRunID: "j-" + scope, PipelineID: "p-" + scope, PipelineRunID: "r-" + scope}, &model.StepRun{StepRunID: "s-" + scope, JobRunID: "j-" + scope, PipelineID: "p-" + scope, PipelineRunID: "r-" + scope}} {
			if err := db.Create(value).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	return db, NewArtifactRepo(artifactTestDatabase{db})
}
func artifactRecord(scope, name string) *model.ProjectArtifact {
	relative, _ := artifactutil.LocalPath(scope, "r-"+scope, "j-"+scope, "s-"+scope, name)
	return &model.ProjectArtifact{ProjectID: scope, PipelineID: "p-" + scope, PipelineRunID: "r-" + scope, JobRunID: "j-" + scope, StepRunID: "s-" + scope, Name: name, StorageType: "local", StoragePath: relative, Size: 7, SHA256: strings.Repeat("a", 64), MediaType: "application/octet-stream"}
}
func TestArtifactRepositoryOwnershipImmutabilityAndPagination(t *testing.T) {
	db, r := artifactRepositoryFixture(t)
	ctx := context.Background()
	first := artifactRecord("a", "dist/one")
	if err := r.Upsert(ctx, first); err != nil {
		t.Fatal(err)
	}
	again := artifactRecord("a", "dist/one")
	if err := r.Upsert(ctx, again); err != nil {
		t.Fatal(err)
	}
	again.Size++
	if err := r.Upsert(ctx, again); !errors.Is(err, ErrArtifactConflict) {
		t.Fatalf("mutation accepted %v", err)
	}
	wrong := artifactRecord("a", "dist/wrong")
	wrong.StepRunID = "s-b"
	wrong.StoragePath, _ = artifactutil.LocalPath("a", "r-a", "j-a", "s-b", wrong.Name)
	if err := r.Upsert(ctx, wrong); err == nil {
		t.Fatal("cross-run step accepted")
	}
	wrong = artifactRecord("a", "dist/wrong")
	wrong.ProjectID = "b"
	if err := r.Upsert(ctx, wrong); err == nil {
		t.Fatal("cross-project row accepted")
	}
	for _, record := range []*model.ProjectArtifact{artifactRecord("a", "dist/two"), artifactRecord("b", "dist/three")} {
		if err := r.Upsert(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	list, total, err := r.List(ctx, ArtifactQuery{ProjectID: "a", Limit: 1})
	if err != nil || len(list) != 1 || total != 2 {
		t.Fatalf("page %+v %d %v", list, total, err)
	}
	if row, err := r.Get(ctx, "b", first.ArtifactID); err != nil || row != nil {
		t.Fatalf("cross-project lookup %v %v", row, err)
	}
	if list, total, err := r.List(ctx, ArtifactQuery{ProjectID: "a", RunID: "r-b"}); err != nil || total != 0 || len(list) != 0 {
		t.Fatalf("cross-run list %+v %d %v", list, total, err)
	}
	for _, query := range []ArtifactQuery{{ProjectID: "a", RunID: "r-a", JobRunID: "j-a"}, {ProjectID: "a", RunID: "r-a", JobRunID: "j-b"}} {
		rows, total, err := r.List(ctx, query)
		want := int64(2)
		if query.JobRunID == "j-b" {
			want = 0
		}
		if err != nil || total != want || int64(len(rows)) != want {
			t.Fatalf("scoped job list %+v %d %v", rows, total, err)
		}
	}
	// 损坏的持久化关系在列表和下载查询中均被过滤
	if err := db.Model(&model.ProjectArtifact{}).Where("artifact_id = ?", first.ArtifactID).Update("step_run_id", "s-b").Error; err != nil {
		t.Fatal(err)
	}
	if row, err := r.Get(ctx, "a", first.ArtifactID); err != nil || row != nil {
		t.Fatalf("corrupt ownership lookup %v %v", row, err)
	}
}
func TestArtifactRepositoryRemoteIdentityAndExpiry(t *testing.T) {
	_, r := artifactRepositoryFixture(t)
	ctx := context.Background()
	value := artifactRecord("a", "dist/data")
	value.StorageType = "s3"
	value.StorageID = "storage-a"
	value.StorageFingerprint = strings.Repeat("f", 64)
	value.StoragePath = "some-other-project/secret"
	if err := r.Upsert(ctx, value); err == nil {
		t.Fatal("arbitrary object key accepted")
	}
	value.StoragePath, _ = artifactutil.RemotePath(value.PipelineRunID, value.JobRunID, value.StepRunID, value.SHA256, value.Name)
	expired := time.Now().Add(-time.Hour)
	value.ExpiresAt = &expired
	if err := r.Upsert(ctx, value); err != nil {
		t.Fatal(err)
	}
	rows, err := r.ListExpired(ctx, "a", "r-a", time.Now(), 100)
	if err != nil || len(rows) != 1 {
		t.Fatalf("expiry %+v %v", rows, err)
	}
	stale := rows[0]
	stale.UpdatedAt = stale.UpdatedAt.Add(-time.Second)
	if deleted, err := r.DeleteVersion(ctx, &stale); err != nil || deleted {
		t.Fatalf("stale delete %v %v", deleted, err)
	}
	if deleted, err := r.DeleteVersion(ctx, &rows[0]); err != nil || !deleted {
		t.Fatalf("delete %v %v", deleted, err)
	}
}

func TestArtifactExpiryUsesUTCForSQLiteOrdering(t *testing.T) {
	_, r := artifactRepositoryFixture(t)
	ctx := context.Background()
	cutoff := time.Date(2026, time.January, 2, 3, 0, 0, 0, time.UTC)
	west := time.FixedZone("west", -7*60*60)
	east := time.FixedZone("east", 14*60*60)
	futureTime := cutoff.Add(2 * time.Hour).In(west)
	pastTime := cutoff.Add(-2 * time.Hour).In(east)
	future := artifactRecord("a", "future")
	future.ExpiresAt = &futureTime
	past := artifactRecord("a", "past")
	past.ExpiresAt = &pastTime
	if err := r.Upsert(ctx, future); err != nil {
		t.Fatal(err)
	}
	if err := r.Upsert(ctx, past); err != nil {
		t.Fatal(err)
	}
	if futureTime.Location() != west || pastTime.Location() != east {
		t.Fatal("repository mutated caller's time values")
	}
	if future.ExpiresAt.Location() != time.UTC || past.ExpiresAt.Location() != time.UTC {
		t.Fatal("stored metadata did not normalize to UTC")
	}
	// 重试使用不同的时区表示同一瞬间，仍然是幂等回执
	duplicate := artifactRecord("a", "future")
	duplicateTime := futureTime.In(east)
	duplicate.ExpiresAt = &duplicateTime
	if err := r.Upsert(ctx, duplicate); err != nil {
		t.Fatalf("equivalent expiry rejected: %v", err)
	}
	for _, zone := range []*time.Location{time.UTC, west, east} {
		rows, err := r.ListExpired(ctx, "a", "r-a", cutoff.In(zone), 100)
		if err != nil || len(rows) != 1 || rows[0].Name != "past" {
			t.Fatalf("timezone %s selected incorrect expiries: %+v %v", zone, rows, err)
		}
	}
}
