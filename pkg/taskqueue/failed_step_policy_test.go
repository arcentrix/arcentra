package taskqueue

import (
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"testing"
)

func TestFailedStepPolicyRequiresIndependentOperatorPermission(t *testing.T) {
	retry := &pipelinev1.Retry{Mode: "failed_step", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{23}}
	policy := runtimeTestPolicy()
	policy.AllowWholeJobRetry = true
	if err := ValidateRuntimeRetry(retry, policy); err == nil {
		t.Fatal("whole-job permission enabled failed-step")
	}
	policy.AllowFailedStepRetry = true
	if err := ValidateRuntimeRetry(retry, policy); err != nil {
		t.Fatal(err)
	}
	if !FailedStepRuntimeAccepted {
		t.Fatal("accepted live-session implementation gate is closed")
	}
	if err := policy.ValidateProduction(); err != nil {
		t.Fatal("explicit accepted failed-step policy rejected", err)
	}
	policy.Enabled = false
	if err := policy.ValidateProduction(); err != nil {
		t.Fatal("disabled runtime configuration rejected", err)
	}
	if (OfflineRuntimePolicy{}).AllowFailedStepRetry {
		t.Fatal("failed-step permission must default off")
	}
	policy.AllowFailedStepRetry = false
	if err := policy.ValidateProduction(); err != nil {
		t.Fatal(err)
	}
	policy.Enabled = true
	if err := policy.ValidateProduction(); err != nil {
		t.Fatal("whole-job compatibility changed", err)
	}
}

func TestFailedStepPolicyBoundsRemainExplicit(t *testing.T) {
	policy := runtimeTestPolicy()
	policy.AllowFailedStepRetry = true
	for name, mutate := range map[string]func(*pipelinev1.Retry){
		"unknown":        func(p *pipelinev1.Retry) { p.Mode = "automatic" },
		"not idempotent": func(p *pipelinev1.Retry) { p.Idempotent = false },
		"unbounded":      func(p *pipelinev1.Retry) { p.MaxAttempts = 6 },
		"no codes":       func(p *pipelinev1.Retry) { p.AllowedExitCodes = nil },
		"zero exit":      func(p *pipelinev1.Retry) { p.AllowedExitCodes = []int32{0} },
		"excess delay":   func(p *pipelinev1.Retry) { p.Delay = "2h" },
	} {
		t.Run(name, func(t *testing.T) {
			p := &pipelinev1.Retry{Mode: "failed_step", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{23}}
			mutate(p)
			if ValidateRuntimeRetry(p, policy) == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
	whole := &pipelinev1.Retry{Mode: "whole_job", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{23}}
	if ValidateRuntimeRetry(whole, policy) == nil {
		t.Fatal("failed-step policy enabled whole-job")
	}
}
