// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package process

import (
	"context"
	"github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/validation"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/protobuf/types/known/structpb"
	"strings"
	"testing"
	"time"
)

func TestRetryApprovalProcessRefusesUnverifiedDirectSubmission(t *testing.T) {
	image := "registry.example/build@sha256:" + strings.Repeat("a", 64)
	args, _ := structpb.NewStruct(map[string]any{"run": "exit 9"})
	parsed := &spec.Pipeline{Namespace: "test", Runtime: &spec.Runtime{Type: taskqueue.RuntimeContainerdOfflineV1, Image: image, Resources: &spec.Resources{CpuLimit: "1", MemoryLimit: "64Mi"}}, Jobs: []*spec.Job{{Name: "build", Retry: &spec.Retry{Mode: "whole_job", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}}, Approval: &spec.Approval{Required: true, Type: "manual", Plugin: retryapproval.Plugin}, Steps: []*spec.Step{{Name: "shell", Uses: "shell", Args: args, RunOnAgent: true}}}}}
	policy := retryapproval.Policy{ID: "retry", Candidates: []string{"reviewer"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 60}
	policy.Fingerprint, _ = policy.Digest()
	definition, _ := validation.ResolvedDefinitionDigest(parsed)
	capture := &retryapproval.RunContext{Version: 1, ProjectID: "project", PipelineID: "pipeline", PipelineRunID: "run", ActorID: "actor", DefinitionCommitSHA: strings.Repeat("c", 40), DefinitionPath: "pipeline.yaml", ResolvedDefinitionDigest: definition, GatedJobs: []string{"build"}, Policy: policy}
	canonical, _ := capture.Canonical()
	digest, _ := capture.Digest()
	run := &model.PipelineRun{RunID: "run", PipelineID: "pipeline", TriggeredBy: "actor", TriggerType: 1, DefinitionCommitSha: capture.DefinitionCommitSHA, DefinitionPath: capture.DefinitionPath, RetryApprovalContext: canonical, RetryApprovalContextDigest: digest}
	engine := &Process{appConf: &config.AppConfig{Pipeline: config.PipelineConfig{RetryApproval: &policy}}, offlineRuntimePolicy: taskqueue.OfflineRuntimePolicy{Enabled: true, AllowWholeJobRetry: true, AllowWholeJobRetryApproval: true, AllowedImages: []string{image}, MaxCPU: "1", MaxMemory: "64Mi"}}
	if err := engine.Submit(run, parsed); err == nil || !strings.Contains(err.Error(), "verified manual submission") {
		t.Fatalf("unverified direct Process admitted: %v", err)
	}
	if err := engine.SubmitVerified(context.Background(), run, parsed); err == nil {
		t.Fatal("empty verified context admitted")
	}
	parsed.Jobs[0].Steps[0].Name = "changed"
	if _, err := engine.validateRetryApprovalRun(run, parsed); err == nil {
		t.Fatal("changed resolved definition accepted")
	}
}

func TestRetryApprovalAgentSelectionRequiresNewCapability(t *testing.T) {
	now := time.Now()
	image := "registry.example/build@sha256:" + strings.Repeat("a", 64)
	agent := model.Agent{AgentID: "agent", OS: "linux", IsEnabled: 1, Status: 1, LastHeartbeat: &now, RuntimeCapabilities: []byte(`{"executionMode":"containerd-offline-v1","containerdOfflineV1":true,"containerdWholeJobRetryV1":true}`), Labels: []byte(`{"containerdRetryApprovalV1":"true"}`)}
	repository := &runtimeAgentRepository{agents: []model.Agent{agent}}
	engine := &Process{repos: &repo.Repositories{Agent: repository}, offlineRuntimePolicy: taskqueue.OfflineRuntimePolicy{Enabled: true, AllowWholeJobRetry: true, AllowWholeJobRetryApproval: true, AllowedImages: []string{image}, MaxCPU: "1", MaxMemory: "64Mi"}}
	job := &spec.Job{Retry: &spec.Retry{Mode: "whole_job", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}}, Approval: &spec.Approval{Required: true, Type: "manual", Plugin: retryapproval.Plugin}}
	runtime := &taskqueue.RuntimeRequest{Type: taskqueue.RuntimeContainerdOfflineV1, Image: image, Resources: taskqueue.RuntimeResources{CPU: "1", Memory: "64Mi"}}
	if _, err := engine.selectAgentForRuntime(context.Background(), job, runtime); err == nil {
		t.Fatal("label spoof or old Agent selected")
	}
	repository.agents[0].RuntimeCapabilities = []byte(`{"executionMode":"containerd-offline-v1","containerdOfflineV1":true,"containerdWholeJobRetryV1":true,"containerdRetryApprovalV1":true}`)
	if _, err := engine.selectAgentForRuntime(context.Background(), job, runtime); err != nil {
		t.Fatal(err)
	}
	engine.offlineRuntimePolicy.AllowWholeJobRetryApproval = false
	if _, err := engine.selectAgentForRuntime(context.Background(), job, runtime); err == nil {
		t.Fatal("operator opt-in bypassed")
	}
}
