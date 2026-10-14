package validation

import (
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

func TestContainerRetryAdmissionChecksOperatorAndApproval(t *testing.T) {
	image := "registry.example/build@sha256:" + strings.Repeat("a", 64)
	policy := taskqueue.OfflineRuntimePolicy{Enabled: true, AllowWholeJobRetry: true, AllowedImages: []string{image}, MaxCPU: "1", MaxMemory: "128Mi"}
	job := &spec.Job{Name: "build", Retry: &spec.Retry{Mode: "whole_job", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}}, Steps: []*spec.Step{{Name: "shell", Uses: "shell", RunOnAgent: true, Args: spec.MapAsStruct(map[string]any{"run": "exit 9"})}}}
	pipeline := &spec.Pipeline{Namespace: "test", Runtime: &spec.Runtime{Type: taskqueue.RuntimeContainerdOfflineV1, Image: image, Resources: &spec.Resources{CpuLimit: "500m", MemoryLimit: "64Mi"}}, Jobs: []*spec.Job{job}}
	if err := NewSchemaValidatorWithRuntimePolicy(policy).Validate(pipeline); err != nil {
		t.Fatal(err)
	}
	policy.AllowWholeJobRetry = false
	if err := NewSchemaValidatorWithRuntimePolicy(policy).Validate(pipeline); err == nil {
		t.Fatal("operator permission missing")
	}
	policy.AllowWholeJobRetry = true
	job.Retry.Mode = "failed_step"
	if err := NewSchemaValidatorWithRuntimePolicy(policy).Validate(pipeline); err == nil {
		t.Fatal("unproven failed_step admitted")
	}
	job.Retry.Mode = "whole_job"
	job.Approval = &spec.Approval{}
	if err := NewSchemaValidatorWithRuntimePolicy(policy).Validate(pipeline); err == nil {
		t.Fatal("approval without complete retry authorization admitted")
	}
	job.Approval = nil
	job.Env = map[string]string{taskqueue.RuntimeGenerationEnv: "2"}
	if err := NewSchemaValidatorWithRuntimePolicy(policy).Validate(pipeline); err == nil {
		t.Fatal("generation override admitted")
	}
	job.Env = nil
	job.Steps[0].Env = map[string]string{taskqueue.RuntimeGenerationEnv: "2"}
	if err := NewSchemaValidatorWithRuntimePolicy(policy).Validate(pipeline); err == nil {
		t.Fatal("step generation override admitted")
	}
}
