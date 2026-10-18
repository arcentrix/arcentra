// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/copilot"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/scm/sourcefile"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/bytedance/sonic"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/plugin/dbresolver"
)

type copilotFixtureReader struct {
	calls   int
	content string
	token   string
	before  func(context.Context)
	alter   func(*sourcefile.File)
}

func (r *copilotFixtureReader) ReadFileAtCommit(ctx context.Context, repository, commit, path, token string) (sourcefile.File, error) {
	r.calls++
	r.token = token
	if r.before != nil {
		r.before(ctx)
	}
	hash := sha1.New()
	_, _ = fmt.Fprintf(hash, "blob %d\x00", len(r.content))
	_, _ = hash.Write([]byte(r.content))
	file := sourcefile.File{Repository: repository, Commit: commit, Path: path, BlobSHA: hex.EncodeToString(hash.Sum(nil)), Mode: "100644", Content: []byte(r.content)}
	if r.alter != nil {
		r.alter(&file)
	}
	return file, ctx.Err()
}

func copilotSourceFixture(t *testing.T) (*CopilotService, *gorm.DB, *copilotFixtureReader, *copilotFakeProvider, CopilotRequest) {
	t.Helper()
	s, provider, _, input := copilotFixture(t)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "source.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.User{}, &model.Team{}, &model.TeamMember{}, &model.Role{}, &model.RoleGrant{}, &model.Permission{}, &model.RolePermissionBinding{}, &model.Project{}, &model.Pipeline{}, &model.PipelineRun{}, &model.JobRun{}, &model.StepRun{}, &model.Secret{}, &model.ProjectArtifact{}, &model.StepRunLog{}, &model.AuditLog{}); err != nil {
		t.Fatal(err)
	}
	binding, err := sourceprovenance.NewBinding("pipe-run", "run", "job", "git", "https://github.com/acme/repo.git", "main")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{
		&model.User{UserID: "owner", IsEnabled: 1},
		&model.Project{ProjectID: "p", OrgID: "org", IsEnabled: 1, Status: model.ProjectStatusActive, RepoType: "github", RepoURL: binding.Repository, AuthType: model.AuthTypeToken, Credential: "opaque-scm-current", EnvVars: []byte(`{"PASSWORD":"project-old-value"}`)},
		&model.Pipeline{PipelineID: "pipe-run", ProjectID: "p", IsEnabled: 1},
		&model.PipelineRun{RunID: "run", PipelineID: "pipe-run", Env: `{"API_TOKEN":"run-old-value"}`},
		&model.JobRun{JobRunID: "job", PipelineID: "pipe-run", PipelineRunID: "run", JobName: "Compile", AgentID: "agent", ClaimID: "claim", TerminalReportDigest: strings.Repeat("e", 64), Status: model.JobRunStatusSuccess, TotalSteps: 1, Env: `{"AUTH_TOKEN":"job-old-value"}`, SourceExpectationCaptured: true, SourceType: binding.Type, SourceRepository: binding.Repository, SourceRef: binding.Ref, SourceBindingDigest: binding.Digest, SourceResolvedCommit: strings.Repeat("a", 40), SourceProvenanceState: "reported"},
		&model.StepRun{StepRunID: "step", JobRunID: "job", PipelineID: "pipe-run", PipelineRunID: "run", Env: `{"ACCESS_TOKEN":"step-old-value"}`, Secrets: `{"retired":"old-retired-key"}`},
		&model.Role{RoleID: "reader-role", Name: "reader", IsEnabled: 1},
		&model.RoleGrant{GrantID: "grant", SubjectType: "user", SubjectID: "owner", RoleID: "reader-role", ScopeType: "project", ScopeID: "p", IsEnabled: 1},
	} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, action := range []string{"project:read", "pipeline:read"} {
		parts := strings.Split(action, ":")
		if err := db.Create(&model.Permission{PermissionID: action, ResourceType: parts[0], Action: parts[1]}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.RolePermissionBinding{RoleID: "reader-role", PermissionID: action}).Error; err != nil {
			t.Fatal(err)
		}
	}
	s.repos = repo.NewRepositories(artifactServiceDatabase{db}, nil)
	encrypted, err := s.secrets.encrypt("registered-old-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Secret{SecretID: "old-secret", Name: "redaction", SecretValue: encrypted, Scope: "project", ScopeID: "p"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureSourceContext(copilot.SourceContextConfig{Enabled: true, AllowedProjectIDs: []string{"p"}}); err != nil {
		t.Fatal(err)
	}
	reader := &copilotFixtureReader{content: "package main\n// opaque-scm-current project-old-value run-old-value job-old-value step-old-value old-retired-key registered-old-key provider-secret"}
	s.sourceReader = reader
	input.Context = CopilotSelection{SourceFiles: []CopilotSourceFile{{JobRunID: "job", Path: "src/main.go"}}}
	return s, db, reader, provider, input
}

func TestCopilotSourceOnlyPreviewAnalysisAndRedaction(t *testing.T) {
	s, _, reader, provider, in := copilotSourceFixture(t)
	subject := authz.Subject{UserID: "owner", TeamIDs: []string{"forged-team"}}
	status, err := s.Status(context.Background(), subject, "p")
	if err != nil || !status.SourceContextEnabled || status.MaxSourceFiles != 3 || status.MaxSourceFileBytes != 8192 {
		t.Fatalf("status %+v %v", status, err)
	}
	sources, err := s.Sources(context.Background(), subject, "p", "run")
	if err != nil || len(sources.SourceJobs) != 1 || !sources.SourceJobs[0].Available || sources.SourceJobs[0].ResolvedCommit != strings.Repeat("a", 40) {
		t.Fatalf("sources %+v %v", sources, err)
	}
	if reader.calls != 0 || provider.calls != 0 {
		t.Fatal("metadata read contacted upstream")
	}
	preview, err := s.Preview(context.Background(), subject, "p", in)
	if err != nil {
		t.Fatal(err)
	}
	if reader.calls != 1 || provider.calls != 0 || len(preview.Evidence) != 1 || preview.Evidence[0].Kind != "source-file" || preview.Evidence[0].BlobSHA == "" || preview.Evidence[0].ProvenanceState != "reported" {
		t.Fatalf("bad preview %+v", preview)
	}
	in.ContextDigest = preview.Digest
	result, err := s.Analyze(context.Background(), subject, "p", in)
	if err != nil || provider.calls != 1 || reader.calls != 2 {
		t.Fatalf("result %+v %v", result, err)
	}
	if reader.token != "opaque-scm-current" {
		t.Fatal("reader did not use current project credential")
	}
	payload, _ := sonic.MarshalString(struct {
		Preview CopilotPreview
		Result  CopilotResult
		Input   copilot.Input
	}{preview, result, provider.input})
	for _, secret := range []string{"opaque-scm-current", "project-old-value", "run-old-value", "job-old-value", "step-old-value", "old-retired-key", "registered-old-key", "provider-secret"} {
		if strings.Contains(payload, secret) {
			t.Fatal("secret leaked:", secret)
		}
	}
}

func TestCopilotSourceAllSelectionsValidateBeforeFetch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*CopilotService, *gorm.DB, *CopilotRequest)
	}{
		{"disabled", func(s *CopilotService, _ *gorm.DB, _ *CopilotRequest) { s.sourceAllowed = nil }},
		{"wrong-run", func(_ *CopilotService, _ *gorm.DB, in *CopilotRequest) { in.RunID = "missing" }},
		{"wrong-job", func(_ *CopilotService, _ *gorm.DB, in *CopilotRequest) {
			in.Context.SourceFiles = append(in.Context.SourceFiles, CopilotSourceFile{JobRunID: "missing", Path: "other.go"})
		}},
		{"wrong-project", func(_ *CopilotService, db *gorm.DB, _ *CopilotRequest) {
			db.Model(&model.Pipeline{}).Where("pipeline_id = ?", "pipe-run").Update("project_id", "other")
		}},
		{"foreign-step", func(_ *CopilotService, _ *gorm.DB, in *CopilotRequest) { in.Context.StepRunIDs = []string{"missing"} }},
		{"unknown-provenance", func(_ *CopilotService, db *gorm.DB, _ *CopilotRequest) {
			db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("source_provenance_state", "unknown")
		}},
		{"missing-terminal-receipt", func(_ *CopilotService, db *gorm.DB, _ *CopilotRequest) {
			db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("terminal_report_digest", "")
		}},
		{"bad-binding", func(_ *CopilotService, db *gorm.DB, _ *CopilotRequest) {
			db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("source_binding_digest", "v1:"+strings.Repeat("f", 64))
		}},
		{"unsupported-sha256", func(_ *CopilotService, db *gorm.DB, _ *CopilotRequest) {
			db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("source_resolved_commit", strings.Repeat("a", 64))
		}},
		{"repository-drift", func(_ *CopilotService, db *gorm.DB, _ *CopilotRequest) {
			db.Model(&model.Project{}).Where("project_id = ?", "p").Update("repo_url", "https://github.com/acme/other")
		}},
		{"unsupported-auth", func(_ *CopilotService, db *gorm.DB, _ *CopilotRequest) {
			db.Model(&model.Project{}).Where("project_id = ?", "p").Update("auth_type", model.AuthTypeSSHKey)
		}},
		{"missing-step-secret", func(_ *CopilotService, db *gorm.DB, _ *CopilotRequest) {
			db.Where("step_run_id = ?", "step").Delete(&model.StepRun{})
		}},
		{"malformed-old-secret", func(_ *CopilotService, db *gorm.DB, _ *CopilotRequest) {
			db.Model(&model.StepRun{}).Where("step_run_id = ?", "step").Update("secrets", "bad-json")
		}},
		{"no-primary-factory", func(s *CopilotService, _ *gorm.DB, _ *CopilotRequest) {
			s.sourcePrimary = (&repo.Repositories{}).CopilotPrimaryRepositories
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, reader, provider, in := copilotSourceFixture(t)
			tc.change(s, db, &in)
			_, err := s.Preview(context.Background(), authz.Subject{UserID: "owner"}, "p", in)
			if err == nil || reader.calls != 0 || provider.calls != 0 {
				t.Fatalf("unsafe selection contacted upstream: %v %d", err, reader.calls)
			}
		})
	}
	for _, path := range []string{"../main.go", ".env", "private-key.go", "config/secrets.json", "https://github.com/acme/repo/main.go", strings.Repeat("x", 513) + ".go"} {
		t.Run(path, func(t *testing.T) {
			s, _, reader, _, in := copilotSourceFixture(t)
			in.Context.SourceFiles[0].Path = path
			_, err := s.Preview(context.Background(), authz.Subject{UserID: "owner"}, "p", in)
			if !errors.Is(err, ErrCopilotInput) || reader.calls != 0 {
				t.Fatal(err)
			}
		})
	}
}

func TestCopilotSourceRechecksLiveStateDuringFetch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*gorm.DB) error
	}{
		{"user", func(db *gorm.DB) error {
			return db.Model(&model.User{}).Where("user_id = ?", "owner").Update("is_enabled", 0).Error
		}},
		{"project", func(db *gorm.DB) error {
			return db.Model(&model.Project{}).Where("project_id = ?", "p").Update("status", model.ProjectStatusArchived).Error
		}},
		{"pipeline", func(db *gorm.DB) error {
			return db.Model(&model.Pipeline{}).Where("pipeline_id = ?", "pipe-run").Update("is_enabled", 0).Error
		}},
		{"grant", func(db *gorm.DB) error {
			return db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Update("is_enabled", 0).Error
		}},
		{"expired-grant", func(db *gorm.DB) error {
			return db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Update("expires_at", time.Now().Add(-time.Second)).Error
		}},
		{"role", func(db *gorm.DB) error {
			return db.Model(&model.Role{}).Where("role_id = ?", "reader-role").Update("is_enabled", 0).Error
		}},
		{"permission", func(db *gorm.DB) error {
			return db.Where("permission_id = ?", "pipeline:read").Delete(&model.RolePermissionBinding{}).Error
		}},
		{"credential", func(db *gorm.DB) error {
			return db.Model(&model.Project{}).Where("project_id = ?", "p").Update("credential", "different-current-key").Error
		}},
		{"repository", func(db *gorm.DB) error {
			return db.Model(&model.Project{}).Where("project_id = ?", "p").Update("repo_url", "https://github.com/acme/other").Error
		}},
		{"provenance", func(db *gorm.DB) error {
			return db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("source_resolved_commit", strings.Repeat("b", 40)).Error
		}},
		{"run-env", func(db *gorm.DB) error {
			return db.Model(&model.PipelineRun{}).Where("run_id = ?", "run").Update("env", `{"TOKEN":"new-value"}`).Error
		}},
		{"job-env", func(db *gorm.DB) error {
			return db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("env", `{"TOKEN":"new-value"}`).Error
		}},
		{"step-secret", func(db *gorm.DB) error {
			return db.Model(&model.StepRun{}).Where("step_run_id = ?", "step").Update("secrets", `{"retired":"new-value"}`).Error
		}},
		{"registered-secret", func(db *gorm.DB) error {
			return db.Model(&model.Secret{}).Where("secret_id = ?", "old-secret").Update("secret_value", "changed-ciphertext").Error
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, reader, provider, in := copilotSourceFixture(t)
			subject := authz.Subject{UserID: "owner"}
			preview, err := s.Preview(context.Background(), subject, "p", in)
			if err != nil {
				t.Fatal(err)
			}
			in.ContextDigest = preview.Digest
			reader.before = func(context.Context) {
				if err := tc.mutate(db); err != nil {
					t.Fatal(err)
				}
			}
			_, err = s.Analyze(context.Background(), subject, "p", in)
			if err == nil || provider.calls != 0 {
				t.Fatalf("transmitted after %s drift: %v", tc.name, err)
			}
		})
	}
}

func TestCopilotSourceDigestBindsIdentityDestinationAndCredential(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*CopilotService, *gorm.DB, *CopilotRequest)
	}{
		{"credential", func(_ *CopilotService, db *gorm.DB, _ *CopilotRequest) {
			db.Model(&model.Project{}).Where("project_id = ?", "p").Update("credential", "new-opaque-credential")
		}},
		{"commit-same-content", func(_ *CopilotService, db *gorm.DB, _ *CopilotRequest) {
			db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("source_resolved_commit", strings.Repeat("b", 40))
		}},
		{"path-same-content", func(_ *CopilotService, _ *gorm.DB, in *CopilotRequest) {
			in.Context.SourceFiles[0].Path = "src/other.go"
		}},
		{"provider", func(_ *CopilotService, _ *gorm.DB, in *CopilotRequest) { in.ProviderID = "other" }},
		{"model", func(_ *CopilotService, _ *gorm.DB, in *CopilotRequest) { in.Model = "other" }},
		{"restart", func(s *CopilotService, _ *gorm.DB, _ *CopilotRequest) { s.sourceKey = []byte(strings.Repeat("z", 32)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, reader, provider, in := copilotSourceFixture(t)
			reader.content = "package main"
			subject := authz.Subject{UserID: "owner"}
			preview, err := s.Preview(context.Background(), subject, "p", in)
			if err != nil {
				t.Fatal(err)
			}
			in.ContextDigest = preview.Digest
			tc.mutate(s, db, &in)
			_, err = s.Analyze(context.Background(), subject, "p", in)
			if !errors.Is(err, ErrCopilotPreview) || provider.calls != 0 {
				t.Fatalf("stale preview accepted: %v", err)
			}
		})
	}
}

func TestCopilotSourceBoundsAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*copilotFixtureReader)
	}{
		{"oversize", func(r *copilotFixtureReader) { r.content = strings.Repeat("x", 8193) }},
		{"binary", func(r *copilotFixtureReader) { r.content = "x\x00y" }},
		{"invalid-utf8", func(r *copilotFixtureReader) { r.content = "\xff" }},
		{"lfs", func(r *copilotFixtureReader) {
			r.content = "version https://git-lfs.github.com/spec/v1\noid sha256:abc"
		}},
		{"wrong-repository", func(r *copilotFixtureReader) {
			r.alter = func(f *sourcefile.File) { f.Repository = "https://github.com/other/repo" }
		}},
		{"wrong-commit", func(r *copilotFixtureReader) {
			r.alter = func(f *sourcefile.File) { f.Commit = strings.Repeat("b", 40) }
		}},
		{"wrong-path", func(r *copilotFixtureReader) { r.alter = func(f *sourcefile.File) { f.Path = "other.go" } }},
		{"wrong-blob", func(r *copilotFixtureReader) {
			r.alter = func(f *sourcefile.File) { f.BlobSHA = strings.Repeat("b", 40) }
		}},
		{"symlink", func(r *copilotFixtureReader) { r.alter = func(f *sourcefile.File) { f.Mode = "120000" } }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, reader, provider, in := copilotSourceFixture(t)
			tc.setup(reader)
			_, err := s.Preview(context.Background(), authz.Subject{UserID: "owner"}, "p", in)
			if !errors.Is(err, ErrCopilotContext) || provider.calls != 0 {
				t.Fatal(err)
			}
		})
	}
	t.Run("cancel-during-source", func(t *testing.T) {
		s, _, reader, provider, in := copilotSourceFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		reader.before = func(context.Context) { cancel() }
		_, err := s.Preview(ctx, authz.Subject{UserID: "owner"}, "p", in)
		if !errors.Is(err, context.Canceled) || provider.calls != 0 {
			t.Fatal(err)
		}
	})
	t.Run("source-count", func(t *testing.T) {
		s, _, reader, _, in := copilotSourceFixture(t)
		for i := 0; i < 3; i++ {
			in.Context.SourceFiles = append(in.Context.SourceFiles, CopilotSourceFile{JobRunID: "job", Path: fmt.Sprintf("file%d.go", i)})
		}
		_, err := s.Preview(context.Background(), authz.Subject{UserID: "owner"}, "p", in)
		if !errors.Is(err, ErrCopilotInput) || reader.calls != 0 {
			t.Fatal(err)
		}
	})
	t.Run("step-count", func(t *testing.T) {
		s, db, reader, _, in := copilotSourceFixture(t)
		steps := []model.StepRun{}
		for i := 0; i < 200; i++ {
			steps = append(steps, model.StepRun{StepRunID: fmt.Sprintf("extra-%d", i), JobRunID: "job", PipelineID: "pipe-run", PipelineRunID: "run"})
		}
		if err := db.Create(&steps).Error; err != nil {
			t.Fatal(err)
		}
		_, err := s.Preview(context.Background(), authz.Subject{UserID: "owner"}, "p", in)
		if !errors.Is(err, ErrCopilotSecrets) || reader.calls != 0 {
			t.Fatal(err)
		}
	})
}

func TestCopilotSourcePrimaryRejectsStaleReplicaAndTeamClaims(t *testing.T) {
	s, primary, reader, provider, in := copilotSourceFixture(t)
	_, replica, _, _, _ := copilotSourceFixture(t)

	sqliteDriver, ok := replica.Dialector.(*sqlite.Dialector)
	if !ok {
		t.Fatal("unexpected fixture driver")
	}
	replicaPath := sqliteDriver.DSN

	if err := primary.Model(&model.User{}).Where("user_id = ?", "owner").Update("is_enabled", 0).Error; err != nil {
		t.Fatal(err)
	}
	if err := primary.Use(dbresolver.Register(dbresolver.Config{Replicas: []gorm.Dialector{sqlite.Open(replicaPath)}})); err != nil {
		t.Fatal(err)
	}
	var stale model.User
	if err := primary.Clauses(database.Read()).Where("user_id = ?", "owner").First(&stale).Error; err != nil || stale.IsEnabled != 1 {
		t.Fatal("replica fixture not stale", err)
	}
	_, err := s.Preview(context.Background(), authz.Subject{UserID: "owner", TeamIDs: []string{"admin"}}, "p", in)
	if !errors.Is(err, ErrCopilotDenied) || reader.calls != 0 || provider.calls != 0 {
		t.Fatal("used stale replica", err)
	}
}

func TestCopilotSourceInitialLoadDeadlineCancellationAndPreviewCapacity(t *testing.T) {
	t.Run("analysis-cancel-before-state", func(t *testing.T) {
		s, _, reader, provider, in := copilotSourceFixture(t)
		s.sourcePrimary = func(ctx context.Context) (*repo.Repositories, error) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 45*time.Second {
				t.Fatal("missing analysis deadline")
			}
			if err := s.Cancel(context.Background(), authz.Subject{UserID: "owner"}, "p", in.RequestID); err != nil {
				t.Fatal(err)
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}
		_, err := s.Analyze(context.Background(), authz.Subject{UserID: "owner"}, "p", in)
		if !errors.Is(err, context.Canceled) || reader.calls != 0 || provider.calls != 0 {
			t.Fatal("initial state was not cancellable", err)
		}
	})
	t.Run("preview-deadline-before-state", func(t *testing.T) {
		s, _, reader, _, in := copilotSourceFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s.sourcePrimary = func(ctx context.Context) (*repo.Repositories, error) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 15*time.Second {
				t.Fatal("missing preview deadline")
			}
			cancel()
			<-ctx.Done()
			return nil, ctx.Err()
		}
		_, err := s.Preview(ctx, authz.Subject{UserID: "owner"}, "p", in)
		if !errors.Is(err, context.Canceled) || reader.calls != 0 {
			t.Fatal("initial preview state was not cancellable", err)
		}
	})
	t.Run("preview-capacity-is-separate", func(t *testing.T) {
		s, _, reader, _, in := copilotSourceFixture(t)
		_, finish1, err := s.previews.Begin(context.Background(), "owner")
		if err != nil {
			t.Fatal(err)
		}
		defer finish1()
		_, finish2, err := s.previews.Begin(context.Background(), "owner")
		if err != nil {
			t.Fatal(err)
		}
		defer finish2()
		_, err = s.Preview(context.Background(), authz.Subject{UserID: "owner"}, "p", in)
		if !errors.Is(err, copilot.ErrBusy) || reader.calls != 0 {
			t.Fatal("preview capacity did not bound initial work", err)
		}
		finish1()
		finish2()
		preview, err := s.Preview(context.Background(), authz.Subject{UserID: "owner"}, "p", in)
		if err != nil {
			t.Fatal(err)
		}
		// 同一个标识既可重复预览，也可在独立分析注册表中发送一次
		if _, err = s.Preview(context.Background(), authz.Subject{UserID: "owner"}, "p", in); err != nil {
			t.Fatal(err)
		}
		in.ContextDigest = preview.Digest
		if _, err = s.Analyze(context.Background(), authz.Subject{UserID: "owner"}, "p", in); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCopilotSourceMixedOtherJobSecretsRechecked(t *testing.T) {
	s, db, reader, provider, in := copilotSourceFixture(t)
	other := model.StepRun{StepRunID: "other-step", PipelineID: "pipe-run", PipelineRunID: "run", JobRunID: "other-job", Secrets: `{"old":"other-job-old-key"}`}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.StepRunLog{LogID: "log", StepRunID: "other-step", LineNumber: 1, Content: "other-job-old-key"}).Error; err != nil {
		t.Fatal(err)
	}
	in.Context.StepRunIDs = []string{"other-step"}
	preview, err := s.Preview(context.Background(), authz.Subject{UserID: "owner"}, "p", in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(preview.Text, "other-job-old-key") {
		t.Fatal("other job secret not redacted")
	}
	in.ContextDigest = preview.Digest
	reader.before = func(context.Context) {
		if err := db.Model(&model.StepRun{}).Where("step_run_id = ?", "other-step").Update("secrets", `{"old":"new-other-key"}`).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Analyze(context.Background(), authz.Subject{UserID: "owner"}, "p", in); !errors.Is(err, ErrCopilotPreview) || provider.calls != 0 {
		t.Fatal("mixed stale secret accepted", err)
	}
}

func TestCopilotSourceCurrentTeamMembershipOnly(t *testing.T) {
	s, db, reader, provider, in := copilotSourceFixture(t)
	for _, object := range []any{&model.Team{TeamID: "team", IsEnabled: 1}, &model.TeamMember{TeamID: "team", UserID: "owner"}} {
		if err := db.Create(object).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Updates(map[string]any{"subject_type": "team", "subject_id": "team"}).Error; err != nil {
		t.Fatal(err)
	}
	subject := authz.Subject{UserID: "owner", TeamIDs: []string{"forged"}}
	preview, err := s.Preview(context.Background(), subject, "p", in)
	if err != nil {
		t.Fatal("current membership not loaded", err)
	}
	in.ContextDigest = preview.Digest
	subject.TeamIDs = []string{"team"}
	reader.before = func(context.Context) {
		if err := db.Where("team_id = ? AND user_id = ?", "team", "owner").Delete(&model.TeamMember{}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Analyze(context.Background(), subject, "p", in); !errors.Is(err, ErrCopilotDenied) || provider.calls != 0 {
		t.Fatal("stale input team authorized", err)
	}
}

func TestCopilotSourceDigestDriftReadsPrimary(t *testing.T) {
	for _, column := range []string{"credential", "source_resolved_commit", "secrets"} {
		t.Run(column, func(t *testing.T) {
			s, primary, _, provider, in := copilotSourceFixture(t)
			_, replica, _, _, _ := copilotSourceFixture(t)
			subject := authz.Subject{UserID: "owner"}
			preview, err := s.Preview(context.Background(), subject, "p", in)
			if err != nil {
				t.Fatal(err)
			}
			in.ContextDigest = preview.Digest
			var updateErr error
			switch column {
			case "credential":
				updateErr = primary.Model(&model.Project{}).Where("project_id = ?", "p").Update(column, "new-token-value").Error
			case "source_resolved_commit":
				updateErr = primary.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update(column, strings.Repeat("b", 40)).Error
			case "secrets":
				updateErr = primary.Model(&model.StepRun{}).Where("step_run_id = ?", "step").Update(column, `{"new":"new-step-value"}`).Error
			}
			if updateErr != nil {
				t.Fatal(updateErr)
			}
			driver, ok := replica.Dialector.(*sqlite.Dialector)
			if !ok {
				t.Fatal("wrong fixture driver")
			}
			if err := primary.Use(dbresolver.Register(dbresolver.Config{Replicas: []gorm.Dialector{sqlite.Open(driver.DSN)}})); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Analyze(context.Background(), subject, "p", in); !errors.Is(err, ErrCopilotPreview) || provider.calls != 0 {
				t.Fatal("stale replica retained source consent", err)
			}
		})
	}
}

func TestCopilotSourceMetadataBoundsAndReasons(t *testing.T) {
	s, db, reader, provider, _ := copilotSourceFixture(t)
	for i := 0; i < 101; i++ {
		if err := db.Create(&model.JobRun{JobRunID: fmt.Sprintf("extra-%03d", i), PipelineID: "pipe-run", PipelineRunID: "run", SourceProvenanceState: "unknown"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	sources, err := s.Sources(context.Background(), authz.Subject{UserID: "owner"}, "p", "run")
	if err != nil || len(sources.SourceJobs) != 100 || len(sources.Warnings) == 0 || reader.calls != 0 || provider.calls != 0 {
		t.Fatalf("unbounded listing %+v %v", sources, err)
	}
	unavailable := 0
	for _, job := range sources.SourceJobs {
		if !job.Available {
			unavailable++
			if job.Reason == "" || job.Repository != "" || job.ResolvedCommit != "" {
				t.Fatal("unsafe unavailable metadata")
			}
		}
	}
	if unavailable == 0 {
		t.Fatal("expected unavailable jobs")
	}
}

type copilotABASecretRepo struct {
	repo.ISecretRepository
	calls  *int
	second string
}

func (r copilotABASecretRepo) GetCopilotSecretValue(ctx context.Context, id string) (string, error) {
	*r.calls++
	if *r.calls == 2 {
		return r.second, nil
	}
	return r.ISecretRepository.(repo.ICopilotSecretValueReader).GetCopilotSecretValue(ctx, id)
}

type copilotABAStepRepo struct {
	repo.IStepRunRepository
	calls *int
}

func (r copilotABAStepRepo) GetByStepRunID(ctx context.Context, id string) (*model.StepRun, error) {
	step, err := r.IStepRunRepository.GetByStepRunID(ctx, id)
	if err != nil || step == nil {
		return step, err
	}
	*r.calls++
	if *r.calls == 2 {
		step.Secrets = `{"ephemeral":"transient-B-key"}`
	}
	return step, nil
}

func TestCopilotSourceSealedRedactorRejectsABA(t *testing.T) {
	t.Run("registered-secret", func(t *testing.T) {
		s, _, reader, provider, in := copilotSourceFixture(t)
		second, err := s.secrets.encrypt("transient-B-key")
		if err != nil {
			t.Fatal(err)
		}
		original, calls := s.sourcePrimary, 0
		s.sourcePrimary = func(ctx context.Context) (*repo.Repositories, error) {
			r, err := original(ctx)
			if err == nil {
				r.Secret = copilotABASecretRepo{ISecretRepository: r.Secret, calls: &calls, second: second}
			}
			return r, err
		}
		_, err = s.Preview(context.Background(), authz.Subject{UserID: "owner"}, "p", in)
		if !errors.Is(err, ErrCopilotPreview) || reader.calls != 0 || provider.calls != 0 {
			t.Fatalf("ABA secret escaped captured snapshot: %v reads=%d", err, calls)
		}
	})
	t.Run("mixed-step-secret", func(t *testing.T) {
		s, db, reader, provider, in := copilotSourceFixture(t)
		if err := db.Create(&model.StepRun{StepRunID: "other-step", JobRunID: "other-job", PipelineID: "pipe-run", PipelineRunID: "run", Secrets: `{"old":"other-job-old-key"}`}).Error; err != nil {
			t.Fatal(err)
		}
		in.Context.StepRunIDs = []string{"other-step"}
		original, calls := s.sourcePrimary, 0
		s.sourcePrimary = func(ctx context.Context) (*repo.Repositories, error) {
			r, err := original(ctx)
			if err == nil {
				r.StepRun = copilotABAStepRepo{IStepRunRepository: r.StepRun, calls: &calls}
			}
			return r, err
		}
		_, err := s.Preview(context.Background(), authz.Subject{UserID: "owner"}, "p", in)
		if !errors.Is(err, ErrCopilotPreview) || reader.calls != 0 || provider.calls != 0 {
			t.Fatalf("ABA step escaped captured snapshot: %v reads=%d", err, calls)
		}
	})
}

func TestCopilotSourceNewOrRotatedRegisteredSecretsDuringFetch(t *testing.T) {
	for _, scope := range []string{"project", "pipeline", "global"} {
		for _, rotate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/rotate=%t", scope, rotate), func(t *testing.T) {
				s, db, reader, provider, in := copilotSourceFixture(t)
				subject := authz.Subject{UserID: "owner"}
				reader.content = "package main\n// newly-classified-value registered-old-key"
				scopeID := ""
				if scope == "project" {
					scopeID = "p"
				}
				if scope == "pipeline" {
					scopeID = "pipe-run"
				}
				if rotate {
					initial, err := s.secrets.encrypt("scope-initial-key")
					if err != nil {
						t.Fatal(err)
					}
					if err := db.Create(&model.Secret{SecretID: "scope-secret", Name: "scoped", Scope: scope, ScopeID: scopeID, SecretValue: initial}).Error; err != nil {
						t.Fatal(err)
					}
				}
				preview, err := s.Preview(context.Background(), subject, "p", in)
				if err != nil {
					t.Fatal(err)
				}
				in.ContextDigest = preview.Digest
				ciphertext, err := s.secrets.encrypt("newly-classified-value")
				if err != nil {
					t.Fatal(err)
				}
				reader.before = func(context.Context) {
					if rotate {
						if err := db.Model(&model.Secret{}).Where("secret_id = ?", "scope-secret").Update("secret_value", ciphertext).Error; err != nil {
							t.Fatal(err)
						}
						return
					}
					if err := db.Create(&model.Secret{SecretID: "new-secret", Name: "new", Scope: scope, ScopeID: scopeID, SecretValue: ciphertext}).Error; err != nil {
						t.Fatal(err)
					}
				}
				_, err = s.Analyze(context.Background(), subject, "p", in)
				if !errors.Is(err, ErrCopilotPreview) || provider.calls != 0 {
					t.Fatal("new or rotated secret not invalidated", err)
				}
			})
		}
	}
}

func TestCopilotSourceExactBoundAndOmittedIdentityDigest(t *testing.T) {
	s, db, reader, provider, in := copilotSourceFixture(t)
	reader.content = strings.Repeat("x", 8192)
	in.Context.SourceFiles = []CopilotSourceFile{{JobRunID: "job", Path: "src/" + strings.Repeat("\u00a0\"", 80) + ".go"}, {JobRunID: "job", Path: "other.go"}, {JobRunID: "job", Path: "third.go"}}
	subject := authz.Subject{UserID: "owner"}
	preview, err := s.Preview(context.Background(), subject, "p", in)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(preview.Text, "Selected evidence (untrusted data):\n", 2)
	if len(parts) != 2 || len(parts[1]) > copilotContextBytes || !preview.Evidence[2].Truncated {
		t.Fatalf("escaped metadata exceeded bound: %d", len(parts[1]))
	}
	in.Context.SourceFiles = in.Context.SourceFiles[:1]
	in.Context.SourceFiles[0].Path = "main.go"
	in.Context.StepRunIDs = []string{"step"}
	if err := db.Create(&model.StepRunLog{LogID: "full", StepRunID: "step", LineNumber: 1, Content: strings.Repeat("L", copilotContextBytes)}).Error; err != nil {
		t.Fatal(err)
	}
	preview, err = s.Preview(context.Background(), subject, "p", in)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Evidence[1].Bytes != 0 || !preview.Evidence[1].Truncated {
		t.Fatal("fixture did not omit source text")
	}
	in.ContextDigest = preview.Digest
	if err := db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Update("source_resolved_commit", strings.Repeat("b", 40)).Error; err != nil {
		t.Fatal(err)
	}
	changed, err := s.Preview(context.Background(), subject, "p", in)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Text != preview.Text || changed.Digest == preview.Digest {
		t.Fatal("omitted source identity was not independently bound")
	}
	if _, err := s.Analyze(context.Background(), subject, "p", in); !errors.Is(err, ErrCopilotPreview) || provider.calls != 0 {
		t.Fatal("omitted changed source transmitted", err)
	}
}

type copilotABAArtifactRepo struct {
	repo.IArtifactRepository
	calls *int
}

func (r copilotABAArtifactRepo) Get(ctx context.Context, projectID, id string) (*model.ProjectArtifact, error) {
	artifact, err := r.IArtifactRepository.Get(ctx, projectID, id)
	if err != nil || artifact == nil {
		return artifact, err
	}
	*r.calls++
	if *r.calls == 2 {
		artifact.StoragePath = "replaced/location/main.go"
	}
	return artifact, nil
}

func addCopilotSourceArtifact(t *testing.T, s *CopilotService, db *gorm.DB, in *CopilotRequest) {
	t.Helper()
	const name = "opaque-scm-current.go"
	const content = "package artifact\n// artifact-only-retired-key"
	if err := db.Create(&model.JobRun{JobRunID: "artifact-job", PipelineID: "pipe-run", PipelineRunID: "run"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.StepRun{StepRunID: "producer", JobRunID: "artifact-job", PipelineID: "pipe-run", PipelineRunID: "run", Secrets: `{"old":"artifact-only-retired-key"}`}).Error; err != nil {
		t.Fatal(err)
	}
	relative, err := artifactutil.LocalPath("p", "run", "artifact-job", "producer", name)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	full := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	artifact := &model.ProjectArtifact{ArtifactID: "selected-artifact", ProjectID: "p", PipelineID: "pipe-run", PipelineRunID: "run", JobRunID: "artifact-job", StepRunID: "producer", Name: name, StorageType: "local", StoragePath: relative, Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}
	if err := db.Create(artifact).Error; err != nil {
		t.Fatal(err)
	}
	// 根路径与存储工厂从原服务保留，元数据仓储必须由请求内主库替换
	s.artifacts = NewArtifactService(&copilotArtifactRepo{artifact: &model.ProjectArtifact{ArtifactID: "stale-replica-artifact"}}, nil, root)
	in.Context.ArtifactIDs = []string{artifact.ArtifactID}
}

func TestCopilotSourceMixedArtifactsUseCapturedPrimaryState(t *testing.T) {
	t.Run("read-and-redact", func(t *testing.T) {
		s, db, _, provider, in := copilotSourceFixture(t)
		addCopilotSourceArtifact(t, s, db, &in)
		preview, err := s.Preview(context.Background(), authz.Subject{UserID: "owner"}, "p", in)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := sonic.MarshalString(preview)
		if len(preview.Evidence) != 2 || preview.Evidence[0].Kind != "code-artifact" || strings.Contains(encoded, "artifact-only-retired-key") || strings.Contains(encoded, "opaque-scm-current") {
			t.Fatal("mixed artifact redaction/primary selection failed")
		}
		in.ContextDigest = preview.Digest
		if _, err := s.Analyze(context.Background(), authz.Subject{UserID: "owner"}, "p", in); err != nil || provider.calls != 1 {
			t.Fatal(err)
		}
	})
	t.Run("ABA-metadata", func(t *testing.T) {
		s, db, reader, provider, in := copilotSourceFixture(t)
		addCopilotSourceArtifact(t, s, db, &in)
		original, calls := s.sourcePrimary, 0
		s.sourcePrimary = func(ctx context.Context) (*repo.Repositories, error) {
			r, err := original(ctx)
			if err == nil {
				r.Artifact = copilotABAArtifactRepo{IArtifactRepository: r.Artifact, calls: &calls}
			}
			return r, err
		}
		if _, err := s.Preview(context.Background(), authz.Subject{UserID: "owner"}, "p", in); err == nil || reader.calls != 0 || provider.calls != 0 {
			t.Fatal("changed artifact metadata accepted", err)
		}
	})
	t.Run("producer-secret-ABA", func(t *testing.T) {
		s, db, reader, provider, in := copilotSourceFixture(t)
		addCopilotSourceArtifact(t, s, db, &in)
		original, calls := s.sourcePrimary, 0
		s.sourcePrimary = func(ctx context.Context) (*repo.Repositories, error) {
			r, err := original(ctx)
			if err == nil {
				r.StepRun = copilotABAStepRepo{IStepRunRepository: r.StepRun, calls: &calls}
			}
			return r, err
		}
		if _, err := s.Preview(context.Background(), authz.Subject{UserID: "owner"}, "p", in); !errors.Is(err, ErrCopilotPreview) || reader.calls != 0 || provider.calls != 0 {
			t.Fatal("producer ABA secret accepted", err)
		}
	})
	t.Run("producer-secret-drift", func(t *testing.T) {
		s, db, reader, provider, in := copilotSourceFixture(t)
		addCopilotSourceArtifact(t, s, db, &in)
		preview, err := s.Preview(context.Background(), authz.Subject{UserID: "owner"}, "p", in)
		if err != nil {
			t.Fatal(err)
		}
		in.ContextDigest = preview.Digest
		reader.before = func(context.Context) {
			if err := db.Model(&model.StepRun{}).Where("step_run_id = ?", "producer").Update("secrets", `{"old":"rotated-artifact-key"}`).Error; err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.Analyze(context.Background(), authz.Subject{UserID: "owner"}, "p", in); !errors.Is(err, ErrCopilotPreview) || provider.calls != 0 {
			t.Fatal("artifact producer secret drift accepted", err)
		}
	})
}

type copilotAfterSecretRead struct {
	repo.ISecretRepository
	after func()
}

func (r copilotAfterSecretRead) GetCopilotSecretValue(ctx context.Context, id string) (string, error) {
	value, err := r.ISecretRepository.(repo.ICopilotSecretValueReader).GetCopilotSecretValue(ctx, id)
	r.after()
	return value, err
}
func TestCopilotSourceFinalPermissionGateAfterSecretEnumeration(t *testing.T) {
	s, db, reader, provider, in := copilotSourceFixture(t)
	subject := authz.Subject{UserID: "owner"}
	preview, err := s.Preview(context.Background(), subject, "p", in)
	if err != nil {
		t.Fatal(err)
	}
	in.ContextDigest = preview.Digest
	original, calls := s.sourcePrimary, 0
	s.sourcePrimary = func(ctx context.Context) (*repo.Repositories, error) {
		r, err := original(ctx)
		if err == nil {
			r.Secret = copilotAfterSecretRead{ISecretRepository: r.Secret, after: func() {
				calls++
				if calls == 4 {
					if err := db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Update("is_enabled", 0).Error; err != nil {
						t.Fatal(err)
					}
				}
			}}
		}
		return r, err
	}
	_, err = s.Analyze(context.Background(), subject, "p", in)
	if !errors.Is(err, ErrCopilotDenied) || calls != 4 || reader.calls != 2 || provider.calls != 0 {
		t.Fatalf("late secret enumeration revocation accepted: %v calls=%d source=%d", err, calls, reader.calls)
	}
}

func TestCopilotSourceDuplicateSecretIdentityFailsClosed(t *testing.T) {
	for _, tc := range []struct{ name, id, scope, scopeID string }{
		{"same-scope", "old-secret", "project", "p"},
		{"cross-scope", "old-secret", "global", ""},
		{"unrelated-project", "old-secret", "project", "foreign"},
		{"case-alias", "OLD-SECRET", "pipeline", "pipe-run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, reader, provider, in := copilotSourceFixture(t)
			// Retain runtime fail-closed coverage on historical schemas lacking unique keys.
			for _, index := range []string{"uk_secret_business_id", "uk_secret_scope_name"} {
				if err := db.Migrator().DropIndex(&model.Secret{}, index); err != nil {
					t.Fatal(err)
				}
			}
			ciphertext, err := s.secrets.encrypt("ambiguous-second-value")
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&model.Secret{SecretID: tc.id, Name: "duplicate", Scope: tc.scope, ScopeID: tc.scopeID, SecretValue: ciphertext}).Error; err != nil {
				t.Fatal(err)
			}
			reader.content = "package main\n// ambiguous-second-value"
			if _, err := s.Preview(context.Background(), authz.Subject{UserID: "owner"}, "p", in); !errors.Is(err, ErrCopilotSecrets) || reader.calls != 0 || provider.calls != 0 {
				t.Fatal("ambiguous registered secret was silently dropped", err)
			}
		})
	}
}
