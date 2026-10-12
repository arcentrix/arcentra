package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/storage"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/bytedance/sonic"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type artifactServiceDatabase struct{ db *gorm.DB }

func (d artifactServiceDatabase) Database() *gorm.DB { return d.db }

type artifactStorageRepository struct {
	repo.IStorageRepository
	config    *model.StorageConfig
	requested string
}

func (r *artifactStorageRepository) Get(_ context.Context, id string) (*model.StorageConfig, error) {
	r.requested = id
	if r.config == nil {
		return nil, errors.New("secret internal storage failure")
	}
	return r.config, nil
}

type artifactRemoteReader struct {
	io.Reader
	closed bool
}

func (r *artifactRemoteReader) Close() error { r.closed = true; return nil }

type artifactRemoteStorage struct {
	storage.IStorage
	reader *artifactRemoteReader
	key    string
	err    error
}

func (s *artifactRemoteStorage) UploadStream(context.Context, string, io.ReadSeeker, int64, string) error {
	return nil
}
func (s *artifactRemoteStorage) DownloadStream(_ context.Context, key string) (io.ReadCloser, error) {
	s.key = key
	return s.reader, s.err
}

func artifactServiceFixture(t *testing.T) (*ArtifactService, *gorm.DB, *model.ProjectArtifact) {
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
	for _, value := range []any{&model.Project{ProjectID: "project"}, &model.Pipeline{ProjectID: "project", PipelineID: "pipe"}, &model.PipelineRun{RunID: "run", PipelineID: "pipe"}, &model.JobRun{JobRunID: "job", PipelineID: "pipe", PipelineRunID: "run", ArtifactURIs: `{"older.txt":"credentials-should-not-leak"}`}, &model.StepRun{StepRunID: "step", JobRunID: "job", PipelineID: "pipe", PipelineRunID: "run"}} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	relative, _ := artifactutil.LocalPath("project", "run", "job", "step", "dist/日本語.json")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, relative)), 0700); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"code":123,"actual":"artifact"}`)
	if err := os.WriteFile(filepath.Join(root, relative), payload, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	value := &model.ProjectArtifact{ProjectID: "project", PipelineID: "pipe", PipelineRunID: "run", JobRunID: "job", StepRunID: "step", Name: "dist/日本語.json", StorageType: "local", StoragePath: relative, Size: int64(len(payload)), SHA256: hex.EncodeToString(digest[:]), MediaType: "application/json"}
	r := repo.NewArtifactRepo(artifactServiceDatabase{db})
	if err := r.Upsert(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	return NewArtifactService(r, nil, root), db, value
}
func TestArtifactServiceListsAndOpensRealLocalFiles(t *testing.T) {
	s, db, value := artifactServiceFixture(t)
	ctx := context.Background()
	list, err := s.List(ctx, "project", ArtifactListQuery{Page: 1, PageSize: 1}, func(string) (bool, error) { return true, nil })
	if err != nil || list.Total != 2 || len(list.List) != 1 {
		t.Fatalf("list %+v %v", list, err)
	}
	all, err := s.List(ctx, "project", ArtifactListQuery{}, func(string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := sonic.Marshal(all)
	if strings.Contains(string(encoded), "credentials-should-not-leak") || strings.Contains(string(encoded), s.root) {
		t.Fatalf("private locator leaked: %s", encoded)
	}
	denied, err := s.List(ctx, "project", ArtifactListQuery{}, func(string) (bool, error) { return false, nil })
	if err != nil || denied.Total != 0 {
		t.Fatalf("denied list %+v %v", denied, err)
	}
	filtered, err := s.List(ctx, "project", ArtifactListQuery{Status: "unavailable"}, func(string) (bool, error) { return true, nil })
	if err != nil || filtered.Total != 1 || filtered.List[0].SizeKnown {
		t.Fatalf("legacy list %+v %v", filtered, err)
	}
	download, err := s.Open(ctx, "project", value.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(download.Reader)
	_ = download.Reader.Close()
	if err != nil || !strings.Contains(string(payload), "actual") {
		t.Fatalf("download %q %v", payload, err)
	}
	if _, err := s.Open(ctx, "other", value.ArtifactID); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("cross-project read %v", err)
	}
	expired := time.Now().Add(-time.Minute)
	if err := db.Model(&model.ProjectArtifact{}).Where("artifact_id = ?", value.ArtifactID).Update("expires_at", expired).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(ctx, "project", value.ArtifactID); !errors.Is(err, ErrArtifactExpired) {
		t.Fatalf("expired read %v", err)
	}
}
func TestArtifactServiceRejectsTamperingAndLinks(t *testing.T) {
	s, _, value := artifactServiceFixture(t)
	ctx := context.Background()
	file := filepath.Join(s.root, value.StoragePath)
	if err := os.WriteFile(file, []byte(strings.Repeat("x", int(value.Size))), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(ctx, "project", value.ArtifactID); !errors.Is(err, ErrArtifactUnavailable) {
		t.Fatalf("changed bytes accepted: %v", err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, file); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(ctx, "project", value.ArtifactID); !errors.Is(err, ErrArtifactUnavailable) {
		t.Fatalf("symlink accepted: %v", err)
	}
}
func TestArtifactServiceRemoteUsesRecordedIdentityAndClosesStream(t *testing.T) {
	s, db, value := artifactServiceFixture(t)
	ctx := context.Background()
	details := model.StorageConfigDetail{Endpoint: "https://store.example", Bucket: "original", BasePath: "prefix", AccessKey: "private-access", SecretKey: "private-secret", UseTLS: true}
	raw, _ := sonic.Marshal(details)
	sr := &artifactStorageRepository{config: &model.StorageConfig{StorageID: "recorded-storage", StorageType: "s3", Config: raw, IsEnabled: 1}}
	s.storageRepo = sr
	remote := &artifactRemoteStorage{reader: &artifactRemoteReader{Reader: strings.NewReader("payload")}}
	s.newStorage = func(config *storage.Storage) (storage.IStorage, error) {
		if config.Bucket != "original" {
			t.Fatalf("wrong bucket %s", config.Bucket)
		}
		return remote, nil
	}
	value.StorageType = "s3"
	value.StorageID = "recorded-storage"
	value.StorageFingerprint = artifactutil.StorageFingerprint("s3", details.Endpoint, details.Bucket, details.Region, details.BasePath, details.UseTLS)
	value.StoragePath, _ = artifactutil.RemotePath(value.PipelineRunID, value.JobRunID, value.StepRunID, value.SHA256, value.Name)
	if err := db.Model(&model.ProjectArtifact{}).Where("artifact_id = ?", value.ArtifactID).Updates(map[string]any{"storage_type": value.StorageType, "storage_id": value.StorageID, "storage_fingerprint": value.StorageFingerprint, "storage_path": value.StoragePath}).Error; err != nil {
		t.Fatal(err)
	}
	download, err := s.Open(ctx, "project", value.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	_ = download.Reader.Close()
	if !remote.reader.closed || remote.key != value.StoragePath || sr.requested != "recorded-storage" {
		t.Fatalf("incorrect binding/stream close: %+v %q", remote, sr.requested)
	}
	details.Bucket = "changed"
	raw, _ = sonic.Marshal(details)
	sr.config.Config = raw
	if _, err := s.Open(ctx, "project", value.ArtifactID); !errors.Is(err, ErrArtifactUnavailable) {
		t.Fatalf("changed bucket accepted: %v", err)
	}
	sr.config = nil
	_, err = s.Open(ctx, "project", value.ArtifactID)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("storage error leaked: %v", err)
	}
}

type lateArtifactFailure struct{ reads int }

func (r *lateArtifactFailure) Read(buffer []byte) (int, error) {
	r.reads++
	if r.reads == 1 {
		return copy(buffer, []byte("ok")), nil
	}
	return 0, errors.New("GET https://bucket/secret?X-Amz-Credential=private: denied")
}
func (r *lateArtifactFailure) Close() error { return errors.New("close secret signed URL") }
func TestArtifactStreamSanitizesLateReadAndCloseErrors(t *testing.T) {
	reader := &artifactContentStream{ReadCloser: &lateArtifactFailure{}}
	buffer := make([]byte, 16)
	if n, err := reader.Read(buffer); n != 2 || err != nil {
		t.Fatalf("first read %d %v", n, err)
	}
	if _, err := reader.Read(buffer); !errors.Is(err, ErrArtifactUnavailable) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("read error leaked %v", err)
	}
	if err := reader.Close(); !errors.Is(err, ErrArtifactUnavailable) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("close error leaked %v", err)
	}
	for _, sentinel := range []error{io.EOF, io.ErrUnexpectedEOF, context.Canceled, context.DeadlineExceeded} {
		if safeArtifactStreamError(sentinel) != sentinel {
			t.Fatalf("sentinel lost %v", sentinel)
		}
	}
}

func TestPrunedBoundArtifactsDoNotReappearAsLegacy(t *testing.T) {
	s, db, value := artifactServiceFixture(t)
	uris, err := sonic.Marshal(map[string]string{value.Name: "artifacts/run/job/step/digest/name"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.JobRun{}).Where("job_run_id = ?", value.JobRunID).Updates(map[string]any{"artifact_storage_id": "original-storage", "artifact_uris": string(uris)}).Error; err != nil {
		t.Fatal(err)
	}
	if deleted, err := s.artifacts.DeleteVersion(context.Background(), value); err != nil || !deleted {
		t.Fatalf("delete %v %v", deleted, err)
	}
	list, err := s.List(context.Background(), "project", ArtifactListQuery{}, func(string) (bool, error) { return true, nil })
	if err != nil || list.Total != 0 {
		t.Fatalf("pruned artifact resurrected: %+v %v", list, err)
	}
}

type shiftingArtifactRepository struct {
	repo.IArtifactRepository
	registered [][]model.ProjectArtifact
	legacy     [][]repo.LegacyArtifactJob
}

func (r *shiftingArtifactRepository) List(_ context.Context, query repo.ArtifactQuery) ([]model.ProjectArtifact, int64, error) {
	page := query.Offset / 100
	if page >= len(r.registered) {
		return nil, 0, nil
	}
	return r.registered[page], 101, nil
}
func (r *shiftingArtifactRepository) ListLegacyJobs(_ context.Context, query repo.ArtifactQuery) ([]repo.LegacyArtifactJob, error) {
	page := query.Offset / 100
	if page >= len(r.legacy) {
		return nil, nil
	}
	return r.legacy[page], nil
}
func TestArtifactListDeduplicatesRowsShiftedBetweenPages(t *testing.T) {
	expired := time.Now().Add(-time.Hour)
	registered := make([]model.ProjectArtifact, 100)
	legacy := make([]repo.LegacyArtifactJob, 100)
	for i := range 100 {
		name := fmt.Sprintf("file-%03d", i)
		registered[i] = model.ProjectArtifact{ArtifactID: artifactutil.ID("p", "r", "j", "s", name), ProjectID: "p", PipelineID: "pipe", PipelineRunID: "r", JobRunID: "j", StepRunID: "s", Name: name, StorageType: "local", ExpiresAt: &expired}
		legacy[i] = repo.LegacyArtifactJob{ProjectID: "p", PipelineID: "pipe", PipelineRunID: "legacy-run", JobRunID: fmt.Sprintf("legacy-job-%03d", i), ArtifactURIs: `{"result":"private-key"}`}
	}
	next := registered[0]
	next.Name = "file-100"
	next.ArtifactID = artifactutil.ID("p", "r", "j", "s", next.Name)
	nextLegacy := legacy[0]
	nextLegacy.JobRunID = "legacy-job-100"
	repository := &shiftingArtifactRepository{registered: [][]model.ProjectArtifact{registered, {registered[99], next}}, legacy: [][]repo.LegacyArtifactJob{legacy, {legacy[99], nextLegacy}}}
	s := NewArtifactService(repository, nil, t.TempDir())
	result, err := s.List(context.Background(), "p", ArtifactListQuery{Page: 3, PageSize: 100}, func(string) (bool, error) { return true, nil })
	if err != nil || result.Total != 202 || len(result.List) != 2 {
		t.Fatalf("shifted pages inflated count or output: %+v %v", result, err)
	}
	seen := make(map[string]bool)
	for page := 1; page <= 3; page++ {
		result, err := s.List(context.Background(), "p", ArtifactListQuery{Page: page, PageSize: 100}, func(string) (bool, error) { return true, nil })
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range result.List {
			if seen[item.ArtifactID] {
				t.Fatalf("duplicate artifact across pages: %s", item.ArtifactID)
			}
			seen[item.ArtifactID] = true
		}
	}
	if len(seen) != 202 {
		t.Fatalf("lost unique rows: %d", len(seen))
	}
}
