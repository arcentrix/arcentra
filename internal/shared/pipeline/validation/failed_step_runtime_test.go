package validation

import (
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"testing"
)

func TestFailedStepProfileRejectsUnsupportedContinuationAndApproval(t *testing.T) {
	policy := taskqueue.OfflineRuntimePolicy{AllowFailedStepRetry: true}
	makeJob := func() *spec.Job {
		return &spec.Job{Name: "build", Retry: &spec.Retry{Mode: "failed_step", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{23}}, Steps: []*spec.Step{{Name: "shell", Uses: "shell", RunOnAgent: true}}}
	}
	if err := validateOfflineRuntimeJob(makeJob(), policy); err != nil {
		t.Fatal(err)
	}
	continued := makeJob()
	continued.Steps[0].ContinueOnError = true
	if validateOfflineRuntimeJob(continued, policy) == nil {
		t.Fatal("continued failure could silently resume boundary")
	}
	approved := makeJob()
	approved.Approval = &spec.Approval{Required: true, Type: "manual"}
	if validateOfflineRuntimeJob(approved, policy) == nil {
		t.Fatal("approval not bound to full step retry plan accepted")
	}
	if validateOfflineRuntimeJob(makeJob(), taskqueue.OfflineRuntimePolicy{AllowWholeJobRetry: true}) == nil {
		t.Fatal("whole-job opt-in enabled failed-step")
	}
}
