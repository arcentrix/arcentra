package retry

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
)

func policy(mode string) *pipelinev1.Retry {
	return &pipelinev1.Retry{Mode: mode, MaxAttempts: 3, Idempotent: true, AllowedExitCodes: []int32{9}}
}
func exit9(t *testing.T) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit 9").Run()
	if err == nil {
		t.Fatal("expected process failure")
	}
	return err
}

func TestExplicitRetryScopeAndBounds(t *testing.T) {
	for _, mode := range []string{"failed_step", "whole_job"} {
		t.Run(mode, func(t *testing.T) {
			attempts := [3]int{}
			failure := exit9(t)
			err := Run(context.Background(), policy(mode), []Step{{Uses: "shell"}, {Uses: "shell"}, {Uses: "shell"}}, func(index, attempt int) error {
				attempts[index]++
				if index == 1 && attempt == 1 {
					return failure
				}
				return nil
			})
			expected := [3]int{1, 2, 1}
			if mode == "whole_job" {
				expected[0] = 2
			}
			if err != nil || attempts != expected {
				t.Fatalf("got %v, %v; want %v", attempts, err, expected)
			}
		})
	}
}
func TestRetryRequiresExplicitPolicy(t *testing.T) {
	for _, candidate := range []*pipelinev1.Retry{{MaxAttempts: 3}, {Mode: "failed_step", MaxAttempts: 3, AllowedExitCodes: []int32{9}}, {Mode: "whole_job", MaxAttempts: 6, Idempotent: true, AllowedExitCodes: []int32{9}}, {Mode: "failed_step", MaxAttempts: 3, Idempotent: true, AllowedExitCodes: []int32{127}}, {Mode: "failed_step", MaxAttempts: 3, Idempotent: true, AllowedExitCodes: []int32{9}, Delay: "-1s"}} {
		if Validate(candidate) == nil {
			t.Fatalf("accepted unsafe policy %+v", candidate)
		}
	}
	calls := 0
	err := Run(context.Background(), nil, []Step{{Uses: "shell"}}, func(int, int) error { calls++; return exit9(t) })
	if err == nil || calls != 1 {
		t.Fatalf("default retries: %d %v", calls, err)
	}
}
func TestBlockedFailureCannotContinueOrRetry(t *testing.T) {
	for _, mode := range []string{"failed_step", "whole_job"} {
		calls := 0
		failure := Block(exit9(t))
		err := Run(context.Background(), policy(mode), []Step{{Uses: "shell", ContinueOnError: true}, {Uses: "shell"}}, func(int, int) error { calls++; return failure })
		if calls != 1 || !IsBlocked(err) {
			t.Fatalf("blocked %s was continued/retried: %d %v", mode, calls, err)
		}
	}
}
func TestRetryCancellationInterruptsDelay(t *testing.T) {
	p := policy("failed_step")
	p.Delay = "1h"
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	start := time.Now()
	err := Run(ctx, p, []Step{{Uses: "shell"}}, func(int, int) error { calls++; cancel(); return exit9(t) })
	if calls != 1 || time.Since(start) > time.Second || (!errors.Is(err, context.Canceled) && err == nil) {
		t.Fatalf("cancelled retry: %d %v", calls, err)
	}
}
