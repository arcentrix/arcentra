package pipeline

import (
	"context"
	"errors"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/validation"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"go.uber.org/zap"
	"testing"
)

type runtimeDispatchStore struct {
	jobRunStoreIface
	captured *taskqueue.RuntimeRequest
	err      error
}

func (s *runtimeDispatchStore) CaptureRuntimeInputExpectation(_ context.Context, pipelineID, runID, jobID, agentID string, _ *sourceprovenance.Binding, runtime *taskqueue.RuntimeRequest, _ map[string]string, _ map[string]taskqueue.ArtifactReference) error {
	copy := *runtime
	s.captured = &copy
	return s.err
}
func (s *runtimeDispatchStore) GetJobRun(context.Context, string) (*model.JobRun, error) {
	return nil, nil
}

type runtimeDispatchQueue struct {
	nova.TaskQueue
	data  []byte
	store *runtimeDispatchStore
	t     *testing.T
}

func (q *runtimeDispatchQueue) Enqueue(task *nova.Task, _ ...nova.Option) (*nova.Result, error) {
	if q.store.captured == nil {
		q.t.Fatal("enqueued before durable runtime capture")
	}
	q.data = append([]byte(nil), task.Payload...)
	return &nova.Result{}, nil
}
func runtimeDispatchFixture(t *testing.T) (*TaskFramework, *Task, *runtimeDispatchStore, *runtimeDispatchQueue) {
	t.Helper()
	image := "registry.example/build@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	job := &spec.Job{Name: "build", Steps: []*spec.Step{{Name: "shell", Uses: "shell", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"run": "echo ok"})}}}
	definition := &spec.Pipeline{Namespace: "test", Runtime: &spec.Runtime{Type: taskqueue.RuntimeContainerdOfflineV1, Image: image, Resources: &spec.Resources{CpuLimit: "500m", MemoryLimit: "64Mi"}}, Jobs: []*spec.Job{job}}
	policy := taskqueue.OfflineRuntimePolicy{Enabled: true, AllowedImages: []string{image}, MaxCPU: "1", MaxMemory: "128Mi"}
	logger := log.Logger{SugaredLogger: zap.NewNop().Sugar()}
	execCtx := NewExecutionContext(definition, nil, t.TempDir(), logger)
	execCtx.PipelineIDRef = "pipeline"
	execCtx.PipelineRunID = "run"
	execCtx.JobRunID = "job"
	execCtx.RuntimePolicy = policy
	var err error
	execCtx.Runtime, err = validation.ResolveRuntime(definition, policy)
	if err != nil {
		t.Fatal(err)
	}
	execCtx.SelectAgent = func(context.Context, *spec.Job) (string, error) { return "agent", nil }
	store := &runtimeDispatchStore{}
	queue := &runtimeDispatchQueue{store: store, t: t}
	execCtx.JobRunStore = store
	execCtx.TaskQueue = queue
	return NewTaskFramework(execCtx, logger), &Task{Name: "build", Job: job}, store, queue
}
func TestRuntimeDispatchCapturesExactPayloadAndRejectsFailure(t *testing.T) {
	tf, task, store, queue := runtimeDispatchFixture(t)
	if err := tf.queue(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	var payload taskqueue.JobRunTaskPayload
	if err := sonic.Unmarshal(queue.data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Runtime == nil || *payload.Runtime != *store.captured {
		t.Fatal("queue runtime differs from durable snapshot")
	}
	if err := taskqueue.ValidateJobRuntime(&payload, tf.execCtx.RuntimePolicy); err != nil {
		t.Fatal(err)
	}
	tf.execCtx.Runtime.Image = "changed"
	if payload.Runtime.Image == "changed" {
		t.Fatal("payload aliases execution context")
	}
	tf, task, store, queue = runtimeDispatchFixture(t)
	store.err = errors.New("capture failed")
	if err := tf.queue(context.Background(), task); !errors.Is(err, store.err) || len(queue.data) > 0 {
		t.Fatalf("failed capture published: %v", err)
	}
	tf, task, store, queue = runtimeDispatchFixture(t)
	task.Job.Steps[0].Uses = "http"
	if err := tf.queue(context.Background(), task); err == nil || store.captured != nil || len(queue.data) > 0 {
		t.Fatal("invalid runtime reached capture or queue")
	}
}
func TestRuntimeContextCannotFallBackToLocalOrStandaloneRunners(t *testing.T) {
	tf, task, _, _ := runtimeDispatchFixture(t)
	if err := tf.execCtx.validateRuntimeContext(); err != nil {
		t.Fatal(err)
	}
	for _, run := range []func(context.Context) error{NewJobRunner(tf.execCtx, task.Job).Run, NewStepRunner(tf.execCtx, task.Job, task.Job.Steps[0]).Run} {
		if err := run(context.Background()); err == nil {
			t.Fatal("legacy runner accepted runtime")
		}
	}
	child := tf.execCtx.ForJob()
	child.Runtime.Image = "changed"
	child.RuntimePolicy.AllowedImages[0] = "changed"
	if tf.execCtx.Runtime.Image == "changed" || tf.execCtx.RuntimePolicy.AllowedImages[0] == "changed" {
		t.Fatal("job context shares mutable runtime policy")
	}
	tf.execCtx.Runtime = nil
	if err := tf.prepare(context.Background(), task); err == nil {
		t.Fatal("missing runtime snapshot fell back to host")
	}
}
