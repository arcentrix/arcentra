// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const rerunArtifactPlanTarget = "11111111-1111-4111-8111-111111111111"

// Literal MySQL 8.0.40 plans from the hosted 766555be acceptance run, with
// testing.T log indentation removed. These are parser fixtures, not replacement
// evidence for the live absent-identity lookup and independent lock witnesses.
const rerunArtifactProjectAIPlan = `-> Limit: 1 row(s)  (cost=0.35 rows=1) (actual time=0.0119..0.0119 rows=0 loops=1)
    -> Covering index lookup on project_artifact using idx_project_artifact_job_run (job_run_id='11111111-1111-4111-8111-111111111111')  (cost=0.35 rows=1) (actual time=0.011..0.011 rows=0 loops=1)
`
const rerunArtifactProjectBinPlan = `-> Limit: 1 row(s)  (cost=0.35 rows=1) (actual time=0.0115..0.0115 rows=0 loops=1)
    -> Covering index lookup on project_artifact using idx_project_artifact_job_run (job_run_id='11111111-1111-4111-8111-111111111111')  (cost=0.35 rows=1) (actual time=0.0107..0.0107 rows=0 loops=1)
`
const rerunArtifactStepAIPlan = `-> Limit: 1 row(s)  (cost=0.35 rows=1) (actual time=0.0124..0.0124 rows=0 loops=1)
    -> Filter: (step_run_artifact.job_run_id = '11111111-1111-4111-8111-111111111111')  (cost=0.35 rows=1) (actual time=0.0119..0.0119 rows=0 loops=1)
        -> Index lookup on step_run_artifact using idx_step_run_artifact_job_run (job_run_id='11111111-1111-4111-8111-111111111111')  (cost=0.35 rows=1) (actual time=0.0112..0.0112 rows=0 loops=1)
`
const rerunArtifactStepBinPlan = `-> Limit: 1 row(s)  (cost=0.35 rows=1) (actual time=0.0101..0.0101 rows=0 loops=1)
    -> Filter: (step_run_artifact.job_run_id = '11111111-1111-4111-8111-111111111111')  (cost=0.35 rows=1) (actual time=0.00954..0.00954 rows=0 loops=1)
        -> Index lookup on step_run_artifact using idx_step_run_artifact_job_run (job_run_id='11111111-1111-4111-8111-111111111111')  (cost=0.35 rows=1) (actual time=0.00893..0.00893 rows=0 loops=1)
`

func TestCopilotRerunArtifactActualPlanObservedLookups(t *testing.T) {
	for _, tc := range []struct{ name, table, plan string }{
		{"project-ai", "project_artifact", rerunArtifactProjectAIPlan},
		{"project-bin", "project_artifact", rerunArtifactProjectBinPlan},
		{"step-ai", "step_run_artifact", rerunArtifactStepAIPlan},
		{"step-bin", "step_run_artifact", rerunArtifactStepBinPlan},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, validateRerunArtifactActualPlan(tc.plan, tc.table, rerunArtifactPlanTarget))
			// Large cost estimates are not actual work and must not fail the proof.
			require.NoError(t, validateRerunArtifactActualPlan(strings.ReplaceAll(tc.plan, "cost=0.35 rows=1", "cost=999 rows=1000000"), tc.table, rerunArtifactPlanTarget))
		})
	}
	plain := strings.ReplaceAll(rerunArtifactProjectAIPlan, "Covering index lookup", "Index lookup")
	require.NoError(t, validateRerunArtifactActualPlan(plain, "project_artifact", rerunArtifactPlanTarget))
	rangePlan := strings.ReplaceAll(plain, "Index lookup", "Index range scan")
	rangePlan = strings.ReplaceAll(rangePlan, " (job_run_id='", " over (job_run_id = '")
	require.NoError(t, validateRerunArtifactActualPlan(rangePlan, "project_artifact", rerunArtifactPlanTarget))
	bounded := strings.ReplaceAll(plain, "rows=0 loops=1", "rows=1.0 loops=1e0")
	require.NoError(t, validateRerunArtifactActualPlan(bounded, "project_artifact", rerunArtifactPlanTarget))
}

func TestCopilotRerunArtifactActualWorkBounds(t *testing.T) {
	// Preserve the original negative contracts: high rows/loops, table scan,
	// wrong identity column and wrong target, now applied to both real shapes.
	for _, tc := range []struct{ table, plan string }{
		{"project_artifact", rerunArtifactProjectAIPlan},
		{"step_run_artifact", rerunArtifactStepAIPlan},
	} {
		t.Run(tc.table, func(t *testing.T) {
			for _, value := range []string{"2", "2000", "-1", "NaN", "Inf", "0.5", "1.0000000000000001", "1e999", "1e-999", "1e-999999999", strings.Repeat("0", 65)} {
				for _, field := range []string{"rows", "loops"} {
					for line := range strings.Split(strings.TrimSuffix(tc.plan, "\n"), "\n") {
						t.Run(fmt.Sprintf("%s-%s-line-%d", field, value, line), func(t *testing.T) {
							lines := strings.Split(strings.TrimSuffix(tc.plan, "\n"), "\n")
							old, replacement := "rows=0 loops=1", "rows="+value+" loops=1"
							if field == "loops" {
								replacement = "rows=0 loops=" + value
							}
							lines[line] = strings.ReplaceAll(lines[line], old, replacement)
							require.Error(t, validateRerunArtifactActualPlan(strings.Join(lines, "\n"), tc.table, rerunArtifactPlanTarget))
						})
					}
				}
			}
			for _, replacement := range []string{"", " (never executed)", " (actual time=0.1..0.2 rows=0 loops=1) trailing", " (actual time=0.1..0.2 rows=0 loops=1) (actual time=0.1..0.2 rows=2000 loops=1)"} {
				for line := range strings.Split(strings.TrimSuffix(tc.plan, "\n"), "\n") {
					lines := strings.Split(strings.TrimSuffix(tc.plan, "\n"), "\n")
					start := strings.Index(lines[line], " (actual time=")
					lines[line] = lines[line][:start] + replacement
					require.Error(t, validateRerunArtifactActualPlan(strings.Join(lines, "\n"), tc.table, rerunArtifactPlanTarget))
				}
			}
			require.Error(t, validateRerunArtifactActualPlan(strings.ReplaceAll(tc.plan, "loops=1", "loops=0"), tc.table, rerunArtifactPlanTarget))
			require.Error(t, validateRerunArtifactActualPlan(strings.ReplaceAll(tc.plan, "job_run_id", "pipeline_run_id"), tc.table, rerunArtifactPlanTarget))
			require.Error(t, validateRerunArtifactActualPlan(strings.ReplaceAll(tc.plan, rerunArtifactPlanTarget, "foreign"), tc.table, rerunArtifactPlanTarget))
		})
	}
}

func TestCopilotRerunArtifactActualPlanRejectsOperatorAndScopeDrift(t *testing.T) {
	good := rerunArtifactProjectAIPlan
	for name, bad := range map[string]string{
		"table-scan":             strings.ReplaceAll(good, "Covering index lookup", "Table scan"),
		"index-scan":             strings.ReplaceAll(good, "Covering index lookup", "Index scan"),
		"covering-scan":          strings.ReplaceAll(good, "Covering index lookup", "Covering index scan"),
		"covering-range":         strings.ReplaceAll(good, "Covering index lookup", "Covering index range scan"),
		"operator-prefix":        strings.ReplaceAll(good, "Covering index lookup", "Not Covering index lookup"),
		"operator-suffix":        strings.ReplaceAll(good, "Covering index lookup", "Covering index lookups"),
		"leaf-in-filter":         strings.ReplaceAll(good, "Covering index lookup", "Filter: Covering index lookup"),
		"leaf-in-comment":        strings.ReplaceAll(good, "Covering index lookup", "/* Covering index lookup"),
		"wrong-index":            strings.ReplaceAll(good, "using idx_project_artifact_job_run", "using PRIMARY"),
		"index-prefix":           strings.ReplaceAll(good, "using idx_project_artifact_job_run", "using idx_project_artifact_job_run_extra"),
		"wrong-table":            strings.ReplaceAll(good, "on project_artifact", "on other_artifact"),
		"table-prefix":           strings.ReplaceAll(good, "on project_artifact", "on project_artifact_extra"),
		"wrong-column":           strings.ReplaceAll(good, "(job_run_id=", "(other_job_run_id="),
		"identity-prefix":        strings.ReplaceAll(good, rerunArtifactPlanTarget, rerunArtifactPlanTarget+"-extra"),
		"identity-only-comment":  strings.ReplaceAll(good, "(job_run_id='"+rerunArtifactPlanTarget+"')", "(job_run_id='foreign') /* "+rerunArtifactPlanTarget+" */"),
		"range-predicate":        strings.ReplaceAll(good, "(job_run_id=", "(job_run_id>="),
		"or-predicate":           strings.ReplaceAll(good, "')  (cost", "' OR 1)  (cost"),
		"suffix-predicate":       strings.ReplaceAll(good, "')  (cost", "') OR (job_run_id='foreign')  (cost"),
		"wrong-limit":            strings.ReplaceAll(good, "Limit: 1 row(s)", "Limit: 2000 row(s)"),
		"no-root":                strings.Split(good, "\n")[1],
		"no-arrow":               strings.ReplaceAll(good, "-> ", ""),
		"root-indent":            "    " + good,
		"sibling-leaf":           strings.ReplaceAll(good, "    ->", "->"),
		"skipped-depth":          strings.ReplaceAll(good, "    ->", "        ->"),
		"extra-leaf":             good + strings.Split(good, "\n")[1] + "\n",
		"extra-scan":             strings.Split(good, "\n")[0] + "\n    -> Table scan on project_artifact  (cost=1 rows=1) (actual time=0.1..0.2 rows=0 loops=1)\n    " + strings.Split(good, "\n")[1],
		"extra-blank-line":       good + "\n",
		"oversized":              strings.Repeat("x", 8193),
		"empty":                  "",
		"backwards-time":         strings.ReplaceAll(good, "time=0.011..0.011", "time=0.02..0.01"),
		"nonfinite-time":         strings.ReplaceAll(good, "time=0.011..0.011", "time=0.01..1e999"),
		"fractional-time-syntax": strings.ReplaceAll(good, "time=0.011..0.011", "time=0.01.1..0.02"),
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, validateRerunArtifactActualPlan(bad, "project_artifact", rerunArtifactPlanTarget))
		})
	}
	for _, replacement := range []string{"Table scan: ", "Sort: ", "Nested loop: ", "Filter: OR ", "Filter: foreign "} {
		bad := strings.Replace(rerunArtifactStepAIPlan, "Filter: ", replacement, 1)
		require.Error(t, validateRerunArtifactActualPlan(bad, "step_run_artifact", rerunArtifactPlanTarget))
	}
	require.Error(t, validateRerunArtifactActualPlan(good, "unknown", rerunArtifactPlanTarget))
	require.Error(t, validateRerunArtifactActualPlan(good, "project_artifact", ""))
}
