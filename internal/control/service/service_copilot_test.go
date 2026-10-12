package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
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
)

type copilotProjectRepo struct{ repo.IProjectRepository }

func (copilotProjectRepo) Get(ctx context.Context, id string) (*model.Project, error) {
	return &model.Project{ProjectID: id, OrgID: "org"}, nil
}

type copilotPipelineRepo struct{ repo.IPipelineRepository }

func (copilotPipelineRepo) GetRun(ctx context.Context, id string) (*model.PipelineRun, error) {
	return &model.PipelineRun{RunID: id, PipelineID: "pipe-" + id, Env: `{"API_TOKEN":"opaque-env-v9WjKx"}`}, nil
}
func (copilotPipelineRepo) Get(ctx context.Context, id string) (*model.Pipeline, error) {
	project := "p"
	if id == "pipe-foreign" {
		project = "other"
	}
	return &model.Pipeline{PipelineID: id, ProjectID: project}, nil
}

type copilotStepRepo struct{ repo.IStepRunRepository }

func (copilotStepRepo) GetByStepRunID(ctx context.Context, id string) (*model.StepRun, error) {
	run := "run"
	if id == "foreign-step" {
		run = "other"
	}
	return &model.StepRun{StepRunID: id, PipelineID: "pipe-" + run, PipelineRunID: run, JobRunID: "job", Secrets: `{"local":"step-only-opaque"}`}, nil
}

type copilotLogRepo struct {
	repo.ILogRepository
	reads   int
	content string
}

func (r *copilotLogRepo) ListByStepRunID(ctx context.Context, id string, from int32, limit int) ([]*model.StepRunLog, error) {
	r.reads++
	if limit != 201 {
		return nil, errors.New("unbounded log query")
	}
	return []*model.StepRunLog{{StepRunID: id, LineNumber: 1, Content: r.content}}, nil
}

type copilotSecretRepo struct {
	repo.ISecretRepository
	secret string
	fail   bool
}

func (r *copilotSecretRepo) List(context.Context, int, int, string, string, string, string) ([]*model.Secret, int64, error) {
	if r.fail {
		return nil, 0, errors.New("unavailable")
	}
	return []*model.Secret{{SecretID: "secret"}}, 1, nil
}
func (r *copilotSecretRepo) GetValue(context.Context, string) (string, error) { return r.secret, nil }

type copilotAuthz struct {
	authz.IAuthorizer
	deny bool
}

func (a copilotAuthz) Check(ctx context.Context, subject authz.Subject, action string, resource authz.ResourceRef) (bool, error) {
	return !a.deny && subject.UserID == "owner", nil
}

type copilotFakeProvider struct {
	calls int
	input copilot.Input
}

func (p *copilotFakeProvider) Analyze(ctx context.Context, input copilot.Input) (copilot.Output, error) {
	p.calls++
	p.input = input
	return copilot.Output{Text: "hypothesis: registered-secret opaque-env-v9WjKx provider-secret"}, nil
}

type copilotFakeRegistry struct {
	provider *copilotFakeProvider
	enabled  bool
}

func (r copilotFakeRegistry) Options(string) []copilot.ModelOption {
	return []copilot.ModelOption{{ProviderID: "test", Model: "model"}}
}
func (r copilotFakeRegistry) Resolve(string, string, string) (copilot.Provider, error) {
	if !r.enabled {
		return nil, copilot.ErrUnconfigured
	}
	return r.provider, nil
}
func (r copilotFakeRegistry) Secrets() []string { return []string{"provider-secret"} }

type copilotArtifactRepo struct {
	repo.IArtifactRepository
	artifact *model.ProjectArtifact
}

func (r *copilotArtifactRepo) Get(context.Context, string, string) (*model.ProjectArtifact, error) {
	return r.artifact, nil
}
func copilotFixture(t *testing.T) (*CopilotService, *copilotFakeProvider, *copilotLogRepo, CopilotRequest) {
	t.Helper()
	secrets := &copilotSecretRepo{}
	t.Setenv("ARCENTRA_SECRET_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901")))
	secretService := NewSecretService(secrets)
	encrypted, err := secretService.encrypt("registered-secret")
	if err != nil {
		t.Fatal(err)
	}
	secrets.secret = encrypted
	logs := &copilotLogRepo{content: "failure registered-secret opaque-env-v9WjKx provider-secret\npassword=inline-secret"}
	provider := &copilotFakeProvider{}
	s := NewCopilotService(&repo.Repositories{Project: copilotProjectRepo{}, Pipeline: copilotPipelineRepo{}, StepRun: copilotStepRepo{}, Log: logs, Secret: secrets}, copilotAuthz{}, nil, nil, secretService)
	s.registry = copilotFakeRegistry{provider: provider, enabled: true}
	in := CopilotRequest{RequestID: "request-1234567890", RunID: "run", ProviderID: "test", Model: "model", Question: "why did this fail?", Context: CopilotSelection{StepRunIDs: []string{"step"}}, ShareSelectedContext: true}
	return s, provider, logs, in
}
func TestCopilotPreviewAndReadOnlyAnalysis(t *testing.T) {
	s, p, _, in := copilotFixture(t)
	subject := authz.Subject{UserID: "owner"}
	preview, err := s.Preview(context.Background(), subject, "p", in)
	if err != nil {
		t.Fatal(err)
	}
	if p.calls != 0 {
		t.Fatal("preview contacted provider")
	}
	for _, secret := range []string{"registered-secret", "opaque-env-v9WjKx", "provider-secret", "inline-secret"} {
		if strings.Contains(preview.Text, secret) {
			t.Fatalf("secret leaked: %s", preview.Text)
		}
	}
	in.ContextDigest = preview.Digest
	out, err := s.Analyze(context.Background(), subject, "p", in)
	if err != nil {
		t.Fatal(err)
	}
	if p.calls != 1 || p.input.User != preview.Text || len(out.Evidence) != 1 || strings.Contains(out.Text, "registered-secret") {
		t.Fatalf("unexpected result %+v", out)
	}
	if _, err := s.Analyze(context.Background(), subject, "p", in); !errors.Is(err, copilot.ErrBusy) {
		t.Fatal("duplicate request was resubmitted", err)
	}
}
func TestCopilotDeniedAndForeignContextNeverReadOrSend(t *testing.T) {
	for _, tc := range []struct {
		name, user, run, step string
		unconfigured          bool
	}{{"anonymous", "", "run", "step", false}, {"outsider", "outsider", "run", "step", false}, {"foreign-run", "owner", "foreign", "step", false}, {"foreign-step", "owner", "run", "foreign-step", false}, {"unconfigured", "owner", "run", "step", true}} {
		t.Run(tc.name, func(t *testing.T) {
			s, p, logs, in := copilotFixture(t)
			in.RunID = tc.run
			in.Context.StepRunIDs = []string{tc.step}
			if tc.unconfigured {
				s.registry = copilotFakeRegistry{provider: p}
			}
			_, err := s.Analyze(context.Background(), authz.Subject{UserID: tc.user}, "p", in)
			if err == nil || p.calls != 0 || logs.reads != 0 {
				t.Fatalf("failed closed: err=%v calls=%d reads=%d", err, p.calls, logs.reads)
			}
		})
	}
}
func TestCopilotPreviewDigestAndRedactionFailure(t *testing.T) {
	s, p, logs, in := copilotFixture(t)
	subject := authz.Subject{UserID: "owner"}
	preview, err := s.Preview(context.Background(), subject, "p", in)
	if err != nil {
		t.Fatal(err)
	}
	in.ContextDigest = preview.Digest
	logs.content = "changed log"
	_, err = s.Analyze(context.Background(), subject, "p", in)
	if !errors.Is(err, ErrCopilotPreview) || p.calls != 0 {
		t.Fatal(err)
	}
	s.repos.Secret.(*copilotSecretRepo).fail = true
	_, err = s.Preview(context.Background(), subject, "p", in)
	if !errors.Is(err, ErrCopilotSecrets) || p.calls != 0 {
		t.Fatal(err)
	}
}
func TestCopilotCodeArtifactIntegrityAndConfinement(t *testing.T) {
	for _, tc := range []struct {
		name, filename, content  string
		tamper, foreign, expired bool
		wantError                bool
	}{
		{"secret-name", "opaque-env-v9WjKx.go", "package main", false, false, false, false},
		{"source", "src/main.go", "package main\n// registered-secret step-only-opaque", false, false, false, false},
		{"tampered", "src/main.go", "package main", true, false, false, true},
		{"foreign", "src/main.go", "package main", false, true, false, true},
		{"expired", "src/main.go", "package main", false, false, true, true},
		{"binary", "src/main.go", "\x00binary", false, false, false, true},
		{"oversized", "src/main.go", strings.Repeat("x", 8193), false, false, false, true},
		{"dotenv", ".env", "TOKEN=secret", false, false, false, true},
		{"private-key", "private-key.txt", "private", false, false, false, true},
		{"git", ".git/config", "private", false, false, false, true},
		{"traversal", "../main.go", "private", false, false, false, true},
		{"credentials", "credentials.json", "private", false, false, false, true},
		{"kubeconfig", "kubeconfig.yaml", "private", false, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p, _, in := copilotFixture(t)
			root := t.TempDir()
			sum := sha256.Sum256([]byte(tc.content))
			artifact := &model.ProjectArtifact{ArtifactID: "artifact", ProjectID: "p", PipelineID: "pipe-run", PipelineRunID: "run", JobRunID: "job", StepRunID: "step", Name: tc.filename, StorageType: "local", Size: int64(len(tc.content)), SHA256: hex.EncodeToString(sum[:])}
			if tc.foreign {
				artifact.PipelineRunID = "other"
			}
			if tc.expired {
				expired := time.Now().Add(-time.Second)
				artifact.ExpiresAt = &expired
			}
			relative, err := artifactutil.LocalPath("p", "run", "job", "step", tc.filename)
			if err == nil {
				artifact.StoragePath = relative
				full := filepath.Join(root, relative)
				if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
					t.Fatal(err)
				}
				content := tc.content
				if tc.tamper {
					content = strings.Repeat("x", len(content))
				}
				if err := os.WriteFile(full, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			s.artifacts = NewArtifactService(&copilotArtifactRepo{artifact: artifact}, nil, root)
			in.Context = CopilotSelection{ArtifactIDs: []string{"artifact"}}
			subject := authz.Subject{UserID: "owner"}
			preview, err := s.Preview(context.Background(), subject, "p", in)
			if tc.wantError {
				if err == nil || p.calls != 0 {
					t.Fatal("unsafe artifact accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			in.ContextDigest = preview.Digest
			out, err := s.Analyze(context.Background(), subject, "p", in)
			if err != nil || len(out.Evidence) != 1 || out.Evidence[0].Kind != "code-artifact" || p.calls != 1 {
				t.Fatalf("%+v %v", out, err)
			}
			if strings.Contains(p.input.User, "registered-secret") || strings.Contains(p.input.User, "step-only-opaque") || strings.Contains(p.input.User, "opaque-env-v9WjKx") || strings.Contains(preview.Text, "opaque-env-v9WjKx") {
				t.Fatal("artifact secret leaked")
			}
		})
	}
}
func TestCopilotContextBoundAndExplicitConsent(t *testing.T) {
	s, p, logs, in := copilotFixture(t)
	in.ShareSelectedContext = false
	if _, err := s.Analyze(context.Background(), authz.Subject{UserID: "owner"}, "p", in); !errors.Is(err, ErrCopilotInput) || logs.reads != 0 {
		t.Fatal(err)
	}
	in.ShareSelectedContext = true
	logs.content = strings.Repeat("diagnostic line\n", 5000)
	preview, err := s.Preview(context.Background(), authz.Subject{UserID: "owner"}, "p", in)
	if err != nil || len(preview.Text) > 32*1024 || len(preview.Warnings) == 0 || p.calls != 0 {
		t.Fatalf("bounded preview %d %v", len(preview.Text), err)
	}
}

func TestCopilotExistingInvalidSecretConfigurationFailsClosed(t *testing.T) {
	s, p, logs, in := copilotFixture(t)
	t.Setenv("ARCENTRA_SECRET_ENCRYPTION_KEY", "")
	s.secrets = NewSecretService(s.repos.Secret)
	_, err := s.Preview(context.Background(), authz.Subject{UserID: "owner"}, "p", in)
	if !errors.Is(err, ErrCopilotSecrets) || p.calls != 0 || logs.reads != 0 {
		t.Fatalf("%v reads=%d calls=%d", err, logs.reads, p.calls)
	}
}

func TestCopilotWrongSecretKeyFailsBeforeContentRead(t *testing.T) {
	s, p, logs, in := copilotFixture(t)
	t.Setenv("ARCENTRA_SECRET_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString([]byte("abcdefghijklmnopqrstuvwxyz012345")))
	s.secrets = NewSecretService(s.repos.Secret)
	_, err := s.Preview(context.Background(), authz.Subject{UserID: "owner"}, "p", in)
	if !errors.Is(err, ErrCopilotSecrets) || p.calls != 0 || logs.reads != 0 {
		t.Fatalf("%v reads=%d calls=%d", err, logs.reads, p.calls)
	}
}

// 项目 SCM 凭据既可能整体出现，也可能仅出现拆分后的认证值
func TestCopilotRedactsProjectSCMCredentials(t *testing.T) {
	for _, tc := range []struct {
		name       string
		kind       int
		credential string
		values     []string
	}{
		{"token", model.AuthTypeToken, "opaque-scm-token-Xk92Qa", []string{"opaque-scm-token-Xk92Qa"}},
		{"password", model.AuthTypePassword, "scm-user-Xk92:opaque-password-Qa32", []string{"scm-user-Xk92:opaque-password-Qa32", "opaque-password-Qa32"}},
		{"ssh", model.AuthTypeSSHKey, "opaque-ssh-key-9vJwRa", []string{"opaque-ssh-key-9vJwRa"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, _ := copilotFixture(t)
			r, err := s.redactor(context.Background(), &model.Project{ProjectID: "p", AuthType: tc.kind, Credential: tc.credential}, &model.PipelineRun{PipelineID: "pipe"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range tc.values {
				for _, encoded := range []string{value, base64.StdEncoding.EncodeToString([]byte(value))} {
					if strings.Contains(r.Redact("source snippet "+encoded), encoded) {
						t.Fatal("SCM credential not redacted")
					}
				}
			}
		})
	}
}
