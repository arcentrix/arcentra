package agentidentity

import (
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"testing"
)

func TestStepAttemptClaimKeepsOriginalAlgorithm(t *testing.T) {
	for _, job := range []string{"original", "job-claim", "5ed5de8d-c056-48d7-b3ec-a9b48b054135"} {
		for _, step := range []string{"first-1", "first-2", "upload-3"} {
			if taskqueue.StepAttemptClaimID(job, step) != StepClaimID(job, step) {
				t.Fatal("step admission changed the existing original-job claim derivation")
			}
		}
	}
}
