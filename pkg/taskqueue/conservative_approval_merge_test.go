package taskqueue

import (
	"strings"
	"testing"

	"github.com/bytedance/sonic"
)

func conservativeMergePayload(t *testing.T, mode string, gated bool) (*JobRunTaskPayload, OfflineRuntimePolicy) {
	t.Helper()
	p, _ := generationContractFixture(t)
	p.Retry.Mode = mode
	p.RetryApprovalRequired = gated
	if gated {
		p.RetryApprovalTimeoutSeconds = 300
	}
	var err error
	p.RetryPlan, err = NewJobRetryPlan(p)
	if err != nil {
		t.Fatal(err)
	}
	p.RetryPlanDigest, err = p.RetryPlan.Digest(p.PipelineID, p.PipelineRunID, p.JobRunID, p.AgentID, p.Runtime.BindingDigest)
	if err != nil {
		t.Fatal(err)
	}
	policy := OfflineRuntimePolicy{Enabled: true, AllowWholeJobRetry: true, AllowWholeJobRetryApproval: true, AllowFailedStepRetry: true, AllowedImages: []string{p.Runtime.Image}, MaxCPU: "2", MaxMemory: "1Gi"}
	if err := ValidateJobRuntime(p, policy); err != nil {
		t.Fatal("valid baseline rejected", err)
	}
	return p, policy
}

func TestConservativeMergeRejectsApprovalBearingFailedStepContracts(t *testing.T) {
	for _, budget := range []int64{0, 1, 300, 3600, 3601} {
		p, policy := conservativeMergePayload(t, "failed_step", false)
		p.RetryApprovalRequired = true
		p.RetryApprovalTimeoutSeconds = budget
		if _, err := NewJobRetryPlan(p); err == nil {
			t.Fatalf("constructor accepted approval-bearing failed_step with budget %d", budget)
		}
		if err := ValidateJobRuntime(p, policy); err == nil {
			t.Fatalf("runtime accepted approval-bearing failed_step with budget %d", budget)
		}
		if err := ValidateJobRetryPlan(p); err == nil {
			t.Fatalf("full plan validation accepted approval-bearing failed_step with budget %d", budget)
		}
		p.RetryPlan.RetryApprovalRequired = true
		p.RetryPlan.RetryApprovalTimeoutSeconds = budget
		if _, err := p.RetryPlan.Canonical(); err == nil {
			t.Fatalf("canonical plan accepted approval-bearing failed_step with budget %d", budget)
		}
		encoded, err := sonic.MarshalString(p.RetryPlan)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseJobRetryPlan(encoded); err == nil {
			t.Fatalf("parser accepted approval-bearing failed_step with budget %d", budget)
		}
	}
	p, policy := conservativeMergePayload(t, "failed_step", false)
	p.RetryApprovalTimeoutSeconds = 300
	p.RetryPlan.RetryApprovalTimeoutSeconds = 300
	if _, err := NewJobRetryPlan(p); err == nil {
		t.Fatal("ungated failed_step accepted orphan approval budget")
	}
	if _, err := p.RetryPlan.Canonical(); err == nil {
		t.Fatal("ungated failed_step plan accepted orphan approval budget")
	}
	if err := ValidateJobRuntime(p, policy); err == nil {
		t.Fatal("ungated failed_step runtime accepted orphan approval budget")
	}
}

func TestConservativeMergeApprovalRemainsBoundToWholeJobV1(t *testing.T) {
	for name, mutate := range map[string]func(*JobRunTaskPayload){
		"payload required removed": func(p *JobRunTaskPayload) { p.RetryApprovalRequired = false; p.RetryApprovalTimeoutSeconds = 0 },
		"plan required removed": func(p *JobRunTaskPayload) {
			p.RetryPlan.RetryApprovalRequired = false
			p.RetryPlan.RetryApprovalTimeoutSeconds = 0
		},
		"payload timeout changed": func(p *JobRunTaskPayload) { p.RetryApprovalTimeoutSeconds++ },
		"plan timeout changed":    func(p *JobRunTaskPayload) { p.RetryPlan.RetryApprovalTimeoutSeconds++ },
		"payload mode changed":    func(p *JobRunTaskPayload) { p.Retry.Mode = "failed_step" },
		"plan mode changed":       func(p *JobRunTaskPayload) { p.RetryPlan.Policy.Mode = "failed_step"; p.RetryPlan.Version = 2 },
		"plan version changed":    func(p *JobRunTaskPayload) { p.RetryPlan.Version = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			p, policy := conservativeMergePayload(t, "whole_job", true)
			mutate(p)
			if err := ValidateJobRetryPlan(p); err == nil {
				t.Fatal("changed approval contract retained full-plan authority")
			}
			if err := ValidateJobRuntime(p, policy); err == nil {
				t.Fatal("changed approval contract retained runtime authority")
			}
		})
	}
	for _, mode := range []string{"whole_job", "failed_step"} {
		p, _ := conservativeMergePayload(t, mode, false)
		body, err := p.RetryPlan.Canonical()
		if err != nil || strings.Contains(body, "retryApproval") {
			t.Fatalf("ungated %s canonical bytes acquired approval fields: %s %v", mode, body, err)
		}
	}
}
