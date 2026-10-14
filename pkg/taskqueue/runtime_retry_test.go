package taskqueue

import (
	"testing"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"google.golang.org/protobuf/proto"
)

func TestOfflineWholeJobRetryPolicyIsExplicitAndBounded(t *testing.T) {
	good := &pipelinev1.Retry{Mode: "whole_job", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}, Delay: "1ms"}
	if err := ValidateRuntimeRetry(good, OfflineRuntimePolicy{}); err == nil {
		t.Fatal("default policy admitted container retry")
	}
	enabled := OfflineRuntimePolicy{AllowWholeJobRetry: true}
	if err := ValidateRuntimeRetry(good, enabled); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*pipelinev1.Retry){
		"failed_step":    func(p *pipelinev1.Retry) { p.Mode = "failed_step" },
		"implicit_mode":  func(p *pipelinev1.Retry) { p.Mode = "" },
		"nonidempotent":  func(p *pipelinev1.Retry) { p.Idempotent = false },
		"single":         func(p *pipelinev1.Retry) { p.MaxAttempts = 1 },
		"too_many":       func(p *pipelinev1.Retry) { p.MaxAttempts = 6 },
		"no_exit":        func(p *pipelinev1.Retry) { p.AllowedExitCodes = nil },
		"zero_exit":      func(p *pipelinev1.Retry) { p.AllowedExitCodes = []int32{0} },
		"signal":         func(p *pipelinev1.Retry) { p.AllowedExitCodes = []int32{137} },
		"permission":     func(p *pipelinev1.Retry) { p.AllowedExitCodes = []int32{126} },
		"duplicate":      func(p *pipelinev1.Retry) { p.AllowedExitCodes = []int32{9, 9} },
		"negative_delay": func(p *pipelinev1.Retry) { p.Delay = "-1s" },
		"too_long":       func(p *pipelinev1.Retry) { p.Delay = "61m" },
		"invalid_delay":  func(p *pipelinev1.Retry) { p.Delay = "forever" },
	} {
		t.Run(name, func(t *testing.T) {
			p := proto.Clone(good).(*pipelinev1.Retry)
			mutate(p)
			if ValidateRuntimeRetry(p, enabled) == nil {
				t.Fatal("invalid retry accepted")
			}
		})
	}
	if err := ValidateRuntimeRetry(nil, OfflineRuntimePolicy{}); err != nil {
		t.Fatal(err)
	}
}
