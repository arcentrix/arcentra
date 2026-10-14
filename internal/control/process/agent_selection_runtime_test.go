package process

import (
	"context"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"testing"
	"time"
)

type runtimeAgentRepository struct {
	repo.IAgentRepository
	agents []model.Agent
}

func (r *runtimeAgentRepository) List(context.Context, int, int) ([]model.Agent, int64, error) {
	return r.agents, int64(len(r.agents)), nil
}
func TestRuntimeSelectionIgnoresSpoofedLabelsAndUnknownAgents(t *testing.T) {
	image := "registry.example/build@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	now := time.Now()
	stale := now.Add(-3 * time.Minute)
	base := model.Agent{AgentID: "ready", OS: "linux", Arch: "amd64", Status: 1, IsEnabled: 1, LastHeartbeat: &now, RuntimeCapabilities: []byte(`{"executionMode":"containerd-offline-v1","containerdOfflineV1":true}`)}
	for name, change := range map[string]func(*model.Agent){"unknown": func(a *model.Agent) { a.RuntimeCapabilities = nil }, "labels spoof": func(a *model.Agent) {
		a.RuntimeCapabilities = nil
		a.Labels = []byte(`{"containerdOfflineV1":"true","runtime":"containerd-offline-v1"}`)
	}, "false report": func(a *model.Agent) {
		a.RuntimeCapabilities = []byte(`{"executionMode":"containerd-offline-v1","containerdOfflineV1":false}`)
	}, "malformed": func(a *model.Agent) { a.RuntimeCapabilities = []byte(`{`) }, "windows spoof": func(a *model.Agent) { a.OS = "windows"; a.Labels = []byte(`{"os":"linux"}`) }, "stale": func(a *model.Agent) { a.LastHeartbeat = &stale }, "disabled": func(a *model.Agent) { a.IsEnabled = 0 }, "offline": func(a *model.Agent) { a.Status = 2 }} {
		t.Run(name, func(t *testing.T) {
			agent := base
			change(&agent)
			r := &runtimeAgentRepository{agents: []model.Agent{agent}}
			engine := &Process{repos: &repo.Repositories{Agent: r}, offlineRuntimePolicy: taskqueue.OfflineRuntimePolicy{Enabled: true, AllowedImages: []string{image}, MaxCPU: "2", MaxMemory: "1Gi"}}
			runtime := &taskqueue.RuntimeRequest{Type: taskqueue.RuntimeContainerdOfflineV1, Image: image, Resources: taskqueue.RuntimeResources{CPU: "1", Memory: "64Mi"}}
			job := &spec.Job{Steps: []*spec.Step{{RunOnAgent: true}}}
			if _, err := engine.selectAgentForRuntime(context.Background(), job, runtime); err == nil {
				t.Fatal("incompatible Agent selected")
			}
			r.agents = append(r.agents, base)
			if got, err := engine.selectAgentForRuntime(context.Background(), job, runtime); err != nil || got != "ready" {
				t.Fatalf("ready Agent rejected: %s %v", got, err)
			}
			job.Steps[0].AgentSelector = &spec.AgentSelector{MatchLabels: map[string]string{"arch": "arm64"}}
			if _, err := engine.selectAgentForRuntime(context.Background(), job, runtime); err == nil {
				t.Fatal("runtime capability bypassed selectors")
			}
		})
	}
	old := base
	old.RuntimeCapabilities = nil
	engine := &Process{repos: &repo.Repositories{Agent: &runtimeAgentRepository{agents: []model.Agent{old}}}}
	if got, err := engine.selectAgent(context.Background(), &spec.Job{}); err != nil || got != "ready" {
		t.Fatalf("legacy selection changed: %s %v", got, err)
	}
}

func TestExecutionModeKeepsUnreadySandboxAgentsOutOfBaremetalQueues(t *testing.T) {
	now := time.Now()
	image := "registry.example/build@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	runtime := &taskqueue.RuntimeRequest{Type: taskqueue.RuntimeContainerdOfflineV1, Image: image, Resources: taskqueue.RuntimeResources{CPU: "1", Memory: "64Mi"}}
	for _, raw := range []string{`{"executionMode":"containerd-offline-v1"}`, `{"executionMode":"containerd-offline-v1","containerdOfflineV1":false}`, `{"executionMode":"containerd-offline-v1","containerdOfflineV1":true}`, `{"executionMode":"future"}`, `{"executionMode":5}`, `null`, `{"executionMode":null}`, `{"containerdOfflineV1":null}`, `{"ExecutionMode":"containerd-offline-v1"}`, `{"containerdOfflineV1":true}`, `{`} {
		t.Run(raw, func(t *testing.T) {
			r := &runtimeAgentRepository{agents: []model.Agent{{AgentID: "sandbox", OS: "linux", Status: 1, IsEnabled: 1, LastHeartbeat: &now, RuntimeCapabilities: []byte(raw), Labels: []byte(`{"executionMode":"baremetal"}`)}}}
			e := &Process{repos: &repo.Repositories{Agent: r}, offlineRuntimePolicy: taskqueue.OfflineRuntimePolicy{Enabled: true, AllowedImages: []string{image}, MaxCPU: "1", MaxMemory: "128Mi"}}
			if _, err := e.selectAgent(context.Background(), &spec.Job{}); err == nil {
				t.Fatal("baremetal job reached runtime-mode or unknown Agent")
			}
			if raw != `{"executionMode":"containerd-offline-v1","containerdOfflineV1":true}` {
				if _, err := e.selectAgentForRuntime(context.Background(), &spec.Job{}, runtime); err == nil {
					t.Fatal("unready or unknown execution mode received runtime job")
				}
			}
		})
	}
}

func TestWholeJobRetrySelectionRequiresIndependentCapability(t *testing.T) {
	image := "registry.example/build@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	now := time.Now()
	base := model.Agent{AgentID: "agent", OS: "linux", IsEnabled: 1, Status: 1, LastHeartbeat: &now}
	policy := taskqueue.OfflineRuntimePolicy{Enabled: true, AllowWholeJobRetry: true, AllowedImages: []string{image}, MaxCPU: "1", MaxMemory: "128Mi"}
	job := &spec.Job{Retry: &spec.Retry{Mode: "whole_job", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}}}
	runtime := &taskqueue.RuntimeRequest{Type: taskqueue.RuntimeContainerdOfflineV1, Image: image, Resources: taskqueue.RuntimeResources{CPU: "1", Memory: "64Mi"}}
	for _, raw := range []string{`{"executionMode":"containerd-offline-v1","containerdOfflineV1":true}`, `{"executionMode":"containerd-offline-v1","containerdOfflineV1":true,"containerdWholeJobRetryV1":false}`, `{"executionMode":"containerd-offline-v1","containerdOfflineV1":true,"containerdWholeJobRetryV1":"true"}`} {
		agent := base
		agent.RuntimeCapabilities = []byte(raw)
		agent.Labels = []byte(`{"containerdWholeJobRetryV1":"true"}`)
		engine := &Process{repos: &repo.Repositories{Agent: &runtimeAgentRepository{agents: []model.Agent{agent}}}, offlineRuntimePolicy: policy}
		if _, err := engine.selectAgentForRuntime(context.Background(), job, runtime); err == nil {
			t.Fatal("legacy/forged capability selected")
		}
	}
	base.RuntimeCapabilities = []byte(`{"executionMode":"containerd-offline-v1","containerdOfflineV1":true,"containerdWholeJobRetryV1":true}`)
	engine := &Process{repos: &repo.Repositories{Agent: &runtimeAgentRepository{agents: []model.Agent{base}}}, offlineRuntimePolicy: policy}
	if got, err := engine.selectAgentForRuntime(context.Background(), job, runtime); err != nil || got != "agent" {
		t.Fatalf("capable Agent rejected: %s %v", got, err)
	}
	engine.offlineRuntimePolicy.AllowWholeJobRetry = false
	if _, err := engine.selectAgentForRuntime(context.Background(), job, runtime); err == nil {
		t.Fatal("operator default-off bypassed")
	}
}
