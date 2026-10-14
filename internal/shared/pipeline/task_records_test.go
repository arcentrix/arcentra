package pipeline

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"go.uber.org/zap"
)

type terminalJobStore struct {
	jobRunStoreIface
	job    *model.JobRun
	writes int
}

func (s *terminalJobStore) GetJobRun(context.Context, string) (*model.JobRun, error) {
	return s.job, nil
}
func (s *terminalJobStore) UpdateJobRun(context.Context, string, map[string]any) error {
	s.writes++
	return nil
}
func (s *terminalJobStore) UpdateJobRunIfStatus(context.Context, string, int, map[string]any) error {
	s.writes++
	return nil
}

func TestRemoteTerminalReportsAreNeverReclassified(t *testing.T) {
	for _, status := range []int{model.JobRunStatusSuccess, model.JobRunStatusFailed, model.JobRunStatusTimeout, model.JobRunStatusCancelled} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			originalEnd := time.Now().Add(-time.Minute)
			store := &terminalJobStore{job: &model.JobRun{JobRunID: "remote", Status: status, EndTime: &originalEnd, Duration: 1234}}
			logger := log.Logger{SugaredLogger: zap.NewNop().Sugar()}
			execution := NewExecutionContext(&spec.Pipeline{Namespace: "test"}, nil, t.TempDir(), logger)
			execution.JobRunStore = store
			execution.JobRunID = "remote"
			framework := NewTaskFramework(execution, logger)
			task := &Task{Name: "remote", State: TaskStateFailed, Job: &spec.Job{Name: "remote", Steps: []*spec.Step{{Name: "shell", RunOnAgent: true}}}}
			framework.finishRecords(context.Background(), task, fmt.Errorf("agent job finished with status %d", status))
			if store.writes != 0 || store.job.Status != status || store.job.Duration != 1234 {
				t.Fatalf("terminal report overwritten: %+v", store.job)
			}
		})
	}
}

func TestRuntimeEnvironmentIsExplicitlyApplied(t *testing.T) {
	ctx := NewExecutionContext(&spec.Pipeline{Runtime: &spec.Runtime{Env: map[string]string{"RUNTIME_ONLY": "available", "PRIORITY": "runtime"}}, Variables: map[string]string{"PRIORITY": "pipeline"}}, nil, t.TempDir(), log.Logger{SugaredLogger: zap.NewNop().Sugar()})
	if ctx.Env["RUNTIME_ONLY"] != "available" || ctx.Env["PRIORITY"] != "pipeline" {
		t.Fatalf("runtime env not applied: %+v", ctx.Env)
	}
}

func TestRemoteArtifactBackflowIsScopedAndSharedWithDownstreamJobs(t *testing.T) {
	logger := log.Logger{SugaredLogger: zap.NewNop().Sugar()}
	root := NewExecutionContext(&spec.Pipeline{Namespace: "test"}, nil, t.TempDir(), logger)
	root.JobRunStore = &terminalJobStore{job: &model.JobRun{JobRunID: "upstream-run", Status: model.JobRunStatusSuccess, ArtifactURIs: `{"dist/output.bin":"opaque/run-a/job-a/dist/output.bin"}`, ArtifactReferences: `{"dist/output.bin":{"storageId":"original-store","fingerprint":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expiresAt":1900000000,"sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","size":123}}`}}
	upstream := root.ForJob()
	task := &Task{Name: "build", Job: &spec.Job{Name: "build"}}
	task.Set("jobRunID", "upstream-run")
	NewTaskFramework(upstream, logger).backflowArtifactURIs(context.Background(), task)
	downstream := root.ForJob()
	uris := downstream.ArtifactSnapshot()
	if len(uris) != 1 || uris["build/dist/output.bin"] != "opaque/run-a/job-a/dist/output.bin" {
		t.Fatalf("artifact backflow lost scope/key: %+v", uris)
	}
	uris["build/dist/output.bin"] = "mutated"
	if root.ArtifactSnapshot()["build/dist/output.bin"] != "opaque/run-a/job-a/dist/output.bin" {
		t.Fatal("downstream snapshot mutated shared artifact state")
	}
	refs := downstream.ArtifactReferenceSnapshot()
	reference := refs["build/dist/output.bin"]
	if reference.StorageID != "original-store" || reference.ExpiresAt != 1900000000 || reference.SHA256 != strings.Repeat("b", 64) || reference.Size == nil || *reference.Size != 123 {
		t.Fatalf("artifact storage binding was not backfilled: %+v", refs)
	}
	*reference.Size = 999
	if *root.ArtifactReferenceSnapshot()["build/dist/output.bin"].Size != 123 {
		t.Fatal("downstream snapshot mutated shared artifact size")
	}
	delete(refs, "build/dist/output.bin")
	if len(root.ArtifactReferenceSnapshot()) != 1 {
		t.Fatal("downstream mutated shared storage binding")
	}
}

func TestArtifactInputSnapshotPairsConcurrentBackflow(t *testing.T) {
	logger := log.Logger{SugaredLogger: zap.NewNop().Sugar()}
	root := NewExecutionContext(&spec.Pipeline{Namespace: "test"}, nil, t.TempDir(), logger)
	done := make(chan struct{})
	safe.Go(func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			size := int64(i)
			root.RecordArtifact(fmt.Sprintf("build/file-%d", i), fmt.Sprintf("key-%d", i), taskqueue.ArtifactReference{Size: &size})
			size = -1
		}
	})
	for {
		uris, refs := root.ArtifactInputSnapshot()
		if len(uris) != len(refs) {
			t.Fatalf("unpaired artifact snapshot: %d/%d", len(uris), len(refs))
		}
		for name := range uris {
			ref, ok := refs[name]
			if !ok || ref.Size == nil || *ref.Size < 0 {
				t.Fatalf("lost or aliased artifact metadata: %s", name)
			}
			*ref.Size = -2
		}
		select {
		case <-done:
			return
		default:
		}
	}
}
