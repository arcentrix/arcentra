package pipeline

import (
	"context"
	"encoding/base64"
	"errors"
	"github.com/arcentrix/arcentra/pkg/nova"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
)

func sourceFrameworkFixture() *TaskFramework {
	return &TaskFramework{execCtx: &ExecutionContext{PipelineIDRef: "pipeline", PipelineRunID: "run", JobRunID: "job", Env: map[string]string{"repo": "https://GitHub.COM/Owner/Repo.git", "ref": "Main", "secrets.access": "credential-canary"}}}
}
func TestResolveSourceExpectationUsesExactDispatchedSource(t *testing.T) {
	tf := sourceFrameworkFixture()
	job := &spec.Job{Source: &spec.Source{Type: "GIT", Repo: "${repo}", Branch: "${ref}"}}
	payload, binding, err := tf.resolveSourceExpectation(job)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Repo != "https://GitHub.COM/Owner/Repo.git" || payload.Branch != "Main" || payload.Type != "GIT" {
		t.Fatalf("dispatch source changed: %+v", payload)
	}
	if binding.Repository != "https://github.com/Owner/Repo.git" || binding.Ref != payload.Branch || binding.Type != "git" || payload.BindingDigest != binding.Digest {
		t.Fatalf("snapshot mismatch: %+v %+v", payload, binding)
	}
	tf.execCtx.Env["repo"] = "https://example/changed.git"
	if payload.Repo != "https://GitHub.COM/Owner/Repo.git" {
		t.Fatal("resolved source did not freeze")
	}
}
func TestResolveSourceExpectationNeverPersistsKnownCredentials(t *testing.T) {
	for _, value := range []string{"https://example/${secrets.access}.git", "https://user:credential-canary@example/repo", "https://example/repo?token=credential-canary", "https://example/credential%2Dcanary.git"} {
		tf := sourceFrameworkFixture()
		_, _, err := tf.resolveSourceExpectation(&spec.Job{Source: &spec.Source{Type: "git", Repo: value}})
		if err == nil || strings.Contains(err.Error(), "credential-canary") {
			t.Fatalf("credential not safely rejected: %v", err)
		}
	}
	tf := sourceFrameworkFixture()
	if _, _, err := tf.resolveSourceExpectation(&spec.Job{Source: &spec.Source{Type: "git", Repo: "/tmp/repo", Branch: "${secrets.access}"}}); err == nil {
		t.Fatal("secret ref accepted")
	}
}
func TestResolveSourceExpectationNoSourceAndInvalidSource(t *testing.T) {
	tf := sourceFrameworkFixture()
	for _, source := range []*spec.Source{nil, {Repo: "/unused/repo"}} {
		_, binding, err := tf.resolveSourceExpectation(&spec.Job{Source: source})
		if err != nil || binding != nil {
			t.Fatalf("disabled source became evidence %v", err)
		}
	}
	for _, source := range []*spec.Source{{Type: "svn", Repo: "/tmp/repo"}, {Type: "git"}} {
		if _, _, err := tf.resolveSourceExpectation(&spec.Job{Source: source}); err == nil {
			t.Fatal("invalid configured source became no source")
		}
	}
}

// sourceRejectingStore 阻止排队前不可变快照失败被忽略
type sourceRejectingStore struct {
	jobRunStoreIface
	called bool
}

func (s *sourceRejectingStore) CaptureSourceExpectation(context.Context, string, string, string, string, *sourceprovenance.Binding) error {
	s.called = true
	return context.Canceled
}
func (s *sourceRejectingStore) GetJobRun(context.Context, string) (*model.JobRun, error) {
	return nil, nil
}

type sourceQueue struct {
	nova.TaskQueue
	enqueued bool
}

func (q *sourceQueue) Enqueue(*nova.Task, ...nova.Option) (*nova.Result, error) {
	q.enqueued = true
	return &nova.Result{}, nil
}
func TestQueueRejectsFailedCaptureBeforePublishingOrStepMutation(t *testing.T) {
	tf := sourceFrameworkFixture()
	store := &sourceRejectingStore{}
	queue := &sourceQueue{}
	tf.execCtx.JobRunStore = store
	tf.execCtx.TaskQueue = queue
	tf.execCtx.SelectAgent = func(context.Context, *spec.Job) (string, error) { return "agent", nil }
	task := &Task{Name: "build", Job: &spec.Job{Name: "build", Source: &spec.Source{Type: "git", Repo: "/tmp/repo"}, Steps: []*spec.Step{{Name: "shell", RunOnAgent: true}}}}
	if err := tf.queue(context.Background(), task); !errors.Is(err, context.Canceled) {
		t.Fatalf("capture failure ignored %v", err)
	}
	if !store.called || queue.enqueued {
		t.Fatal("published before durable capture")
	}
}
func TestSourceKnownSecretEncodedFormsAreRejected(t *testing.T) {
	for _, encode := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		tf := sourceFrameworkFixture()
		value := encode.EncodeToString([]byte(tf.execCtx.Env["secrets.access"]))
		if _, _, err := tf.resolveSourceExpectation(&spec.Job{Source: &spec.Source{Type: "git", Repo: "https://example/" + value + ".git"}}); err == nil {
			t.Fatal("encoded known secret persisted")
		}
	}
}
