package taskqueue

import "testing"

func TestApprovalPlanCanonicalDigestGoldenCompatibility(t *testing.T) {
	for _, tc := range []struct {
		mode   string
		gated  bool
		digest string
	}{
		{"whole_job", false, "v1:701feda8db01fbcb5d9ff7464dd7abbaa4850dfbb32210bc3283c82f01c4668a"},
		{"whole_job", true, "v1:c8c2353d4069b820878da3e3dc6f1e3bf29c930548bba353de0e1091ee1369fb"},
		{"failed_step", false, "v1:c3b2463cfd82d25aaba227a9482a44ea203d3b438c9c2dfb253d684d9369626c"},
		{"failed_step", true, "v1:917a3067434c9e48e42b3d2f5f1f2995600f85c7e93520b49c5eaa8600f108b6"},
	} {
		t.Run(tc.mode+tc.digest[:8], func(t *testing.T) {
			p, _ := generationContractFixture(t)
			p.Retry.Mode = tc.mode
			p.RetryApprovalRequired = tc.gated
			if tc.gated {
				p.RetryApprovalTimeoutSeconds = 300
			}
			plan, err := NewJobRetryPlan(p)
			if err != nil {
				t.Fatal(err)
			}
			digest, err := plan.Digest(p.PipelineID, p.PipelineRunID, p.JobRunID, p.AgentID, p.Runtime.BindingDigest)
			if err != nil || digest != tc.digest {
				t.Fatalf("canonical digest changed: %s %v", digest, err)
			}
			body, err := plan.Canonical()
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := ParseJobRetryPlan(body)
			if err != nil {
				t.Fatal(err)
			}
			again, err := parsed.Canonical()
			if err != nil || again != body {
				t.Fatal("canonical round trip changed")
			}
		})
	}
}
