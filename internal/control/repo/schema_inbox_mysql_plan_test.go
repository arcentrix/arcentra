// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Captured MySQL 8.0.40 operators, with stable synthetic timings. The due
// positive changes only the zero-hit metrics to the required actual one hit.
const inboxDensePrimaryPlan = `-> Limit: 100 row(s)  (cost=20.6 rows=0.05) (actual time=0.0273..0.0663 rows=100 loops=1)
    -> Filter: ((pipeline_run.inbox_origin = 1) and (pipeline_run.id > 4900) and (pipeline_run.id <= 5001))  (cost=20.6 rows=0.05) (actual time=0.0267..0.0607 rows=100 loops=1)
        -> Index range scan on pipeline_run using PRIMARY over (4900 < id <= 5001)  (cost=20.6 rows=101) (actual time=0.024..0.0506 rows=100 loops=1)
`
const inboxDueHitPlan = `-> Limit: 1 row(s)  (cost=0.71 rows=1) (actual time=0.0125..0.0125 rows=1 loops=1)
    -> Index range scan on inbox_work using idx_inbox_work_due over (source_kind = 'run' AND state = 0 AND next_check_at <= '2026-10-18 22:49:34.000'), with index condition: ((inbox_work.state = 0) and (inbox_work.source_kind = 'run') and (inbox_work.next_check_at <= <cache>(now())))  (cost=0.71 rows=1) (actual time=0.0121..0.0121 rows=1 loops=1)
`

func inboxPlanEstimate(table, key string) []inboxMySQLQueryPlan {
	return []inboxMySQLQueryPlan{{Table: table, Key: &key, Rows: 101, Type: "range"}}
}

func TestInboxMySQLBoundedPlanAcceptsActualQuerySpecificRanges(t *testing.T) {
	primary := inboxPlanEstimate("pipeline_run", "PRIMARY")
	require.NoError(t, validateInboxMySQLPlan(primary, inboxDensePrimaryPlan, "repair", 100))
	sparse := strings.ReplaceAll(inboxDensePrimaryPlan, " rows=100 loops=1", " rows=2 loops=1")
	// Even a sparse primary range may examine every slot in the bounded ID window.
	sparse = strings.Replace(sparse, "0.024..0.0506 rows=2", "0.024..0.0506 rows=101", 1)
	require.NoError(t, validateInboxMySQLPlan(primary, sparse, "repair", 2))
	require.Error(t, validateInboxMySQLPlan(primary, strings.ReplaceAll(sparse, "5001", "10001"), "repair-wide", 2), "wide sparse window must not scan PRIMARY")
	for _, key := range []string{"idx_run_inbox_repair", "idx_run_inbox_origin"} {
		secondary := strings.ReplaceAll(sparse, "using PRIMARY over (4900 < id <= 5001)", "using "+key+" over (inbox_origin = 1 AND 4900 < id <= 5001)")
		require.Error(t, validateInboxMySQLPlan(inboxPlanEstimate("pipeline_run", key), secondary, "repair", 2), "origin-aware range already satisfies the redundant filter")
		secondary = strings.Replace(secondary, "0.024..0.0506 rows=101", "0.024..0.0506 rows=2", 1)
		require.NoError(t, validateInboxMySQLPlan(inboxPlanEstimate("pipeline_run", key), secondary, "repair", 2))
		lines := strings.Split(strings.TrimSuffix(secondary, "\n"), "\n")
		withoutFilter := lines[0] + "\n" + strings.TrimPrefix(lines[2], "    ")
		require.Error(t, validateInboxMySQLPlan(inboxPlanEstimate("pipeline_run", key), strings.Replace(withoutFilter, "0.024..0.0506 rows=2", "0.024..0.0506 rows=101", 1), "repair", 2), "unfiltered child output cannot exceed parent output")
		require.NoError(t, validateInboxMySQLPlan(inboxPlanEstimate("pipeline_run", key), withoutFilter, "repair", 2))
		wide := strings.ReplaceAll(withoutFilter, "5001", "10001")
		require.NoError(t, validateInboxMySQLPlan(inboxPlanEstimate("pipeline_run", key), wide, "repair-wide", 2))
		withCondition := strings.Replace(wide, "over (inbox_origin = 1 AND 4900 < id <= 10001)", "over (inbox_origin = 1 AND 4900 < id <= 10001), with index condition: ((pipeline_run.inbox_origin = 1) and (pipeline_run.id > 4900) and (pipeline_run.id <= 10001))", 1)
		require.NoError(t, validateInboxMySQLPlan(inboxPlanEstimate("pipeline_run", key), withCondition, "repair-wide", 2))
		require.Error(t, validateInboxMySQLPlan(inboxPlanEstimate("pipeline_run", key), strings.Replace(wide, "inbox_origin = 1 AND 4900 < id <= 10001", "inbox_origin = 1", 1), "repair-wide", 2))
		require.Error(t, validateInboxMySQLPlan(inboxPlanEstimate("pipeline_run", key), strings.Replace(wide, "0.024..0.0506 rows=2", "0.024..0.0506 rows=5000", 1), "repair-wide", 2))
	}
	require.NoError(t, validateInboxMySQLPlan(inboxPlanEstimate("inbox_work", "idx_inbox_work_due"), inboxDueHitPlan, "due", 1))
}

func TestInboxMySQLBoundedPlanRejectsUnknownUnboundedOrExcessiveExecution(t *testing.T) {
	for name, execution := range map[string]string{
		"no-actual":           strings.ReplaceAll(inboxDensePrimaryPlan, "actual time=", "estimated time="),
		"table-scan":          strings.ReplaceAll(inboxDensePrimaryPlan, "Index range scan", "Table scan"),
		"index-full-scan":     strings.ReplaceAll(inboxDensePrimaryPlan, "Index range scan", "Index scan"),
		"wrong-range":         strings.ReplaceAll(inboxDensePrimaryPlan, "4900 < id <= 5001", "1 < id <= 5001"),
		"no-upper-bound":      strings.ReplaceAll(inboxDensePrimaryPlan, "4900 < id <= 5001", "4900 < id"),
		"wrong-origin":        strings.ReplaceAll(inboxDensePrimaryPlan, "inbox_origin = 1", "inbox_origin = 0"),
		"unknown-index":       strings.ReplaceAll(inboxDensePrimaryPlan, "using PRIMARY", "using unreviewed"),
		"over-limit-output":   strings.ReplaceAll(inboxDensePrimaryPlan, "rows=100 loops=1", "rows=101 loops=1"),
		"unbounded-leaf":      strings.Replace(inboxDensePrimaryPlan, "0.024..0.0506 rows=100", "0.024..0.0506 rows=5000", 1),
		"one-over-window":     strings.Replace(inboxDensePrimaryPlan, "0.024..0.0506 rows=100", "0.024..0.0506 rows=102", 1),
		"loop-multiplication": strings.Replace(inboxDensePrimaryPlan, "rows=100 loops=1)", "rows=100 loops=2)", 1),
		"zero-loops":          strings.ReplaceAll(inboxDensePrimaryPlan, "loops=1", "loops=0"),
		"fractional-rows":     strings.ReplaceAll(inboxDensePrimaryPlan, "rows=100 loops=1", "rows=100.5 loops=1"),
		"nan-rows":            strings.ReplaceAll(inboxDensePrimaryPlan, "rows=100 loops=1", "rows=NaN loops=1"),
		"overflow-rows":       strings.ReplaceAll(inboxDensePrimaryPlan, "rows=100 loops=1", "rows=1e999 loops=1"),
		"unknown-sort":        strings.Replace(inboxDensePrimaryPlan, "Filter:", "Sort:", 1),
		"wrong-depth":         strings.Replace(inboxDensePrimaryPlan, "        ->", "    ->", 1),
		"extra-root":          inboxDensePrimaryPlan + inboxDensePrimaryPlan,
		"never-executed":      strings.ReplaceAll(inboxDensePrimaryPlan, "(actual time=0.024..0.0506 rows=100 loops=1)", "(never executed)"),
		"oversize":            strings.Repeat("x", 8193),
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, validateInboxMySQLPlan(inboxPlanEstimate("pipeline_run", "PRIMARY"), execution, "repair", 100))
		})
	}
	for name, execution := range map[string]string{
		"zero-hit":            strings.ReplaceAll(inboxDueHitPlan, "rows=1 loops=1", "rows=0 loops=1"),
		"excessive-scan":      strings.Replace(inboxDueHitPlan, "0.0121..0.0121 rows=1", "0.0121..0.0121 rows=5000", 1),
		"wrong-state":         strings.ReplaceAll(inboxDueHitPlan, "state = 0", "state = 1"),
		"wrong-kind":          strings.ReplaceAll(inboxDueHitPlan, "'run'", "'approval'"),
		"no-time-bound":       strings.Replace(inboxDueHitPlan, " AND next_check_at <= '2026-10-18 22:49:34.000'", "", 1),
		"wrong-limit":         strings.Replace(inboxDueHitPlan, "Limit: 1 row(s)", "Limit: 100 row(s)", 1),
		"loop-multiplication": strings.ReplaceAll(inboxDueHitPlan, "loops=1", "loops=2"),
	} {
		t.Run("due-"+name, func(t *testing.T) {
			require.Error(t, validateInboxMySQLPlan(inboxPlanEstimate("inbox_work", "idx_inbox_work_due"), execution, "due", 1))
		})
	}
}

func TestInboxMySQLBoundedPlanRequiresConsistentStructuredRange(t *testing.T) {
	for _, plan := range [][]inboxMySQLQueryPlan{
		nil,
		{{Table: "pipeline_run", Rows: 101, Type: "range"}},
		inboxPlanEstimate("other_table", "PRIMARY"),
		inboxPlanEstimate("pipeline_run", "other_index"),
		append(inboxPlanEstimate("pipeline_run", "PRIMARY"), inboxPlanEstimate("pipeline_run", "PRIMARY")...),
	} {
		require.Error(t, validateInboxMySQLPlan(plan, inboxDensePrimaryPlan, "repair", 100))
	}
	plan := inboxPlanEstimate("pipeline_run", "PRIMARY")
	plan[0].Type = "ALL"
	require.Error(t, validateInboxMySQLPlan(plan, inboxDensePrimaryPlan, "repair", 100))
	plan[0].Type, plan[0].Rows = "range", 5000
	require.Error(t, validateInboxMySQLPlan(plan, inboxDensePrimaryPlan, "repair", 100))
}

// Full observed MySQL 8.0.40 wide-window plan, utf8mb4_0900_ai_ci.
const inboxWideIntersectPlan = `-> Limit: 100 row(s)  (cost=1.09 rows=2) (actual time=0.0506..0.0509 rows=2 loops=1)
    -> Sort: pipeline_run.id, limit input to 100 row(s) per chunk  (cost=1.09 rows=2) (actual time=0.05..0.0502 rows=2 loops=1)
        -> Filter: ((pipeline_run.inbox_origin = 1) and (pipeline_run.id > 4900) and (pipeline_run.id <= 10001))  (cost=1.09 rows=2) (actual time=0.0354..0.0425 rows=2 loops=1)
            -> Intersect rows sorted by row ID  (cost=1.09 rows=2) (actual time=0.0339..0.0406 rows=2 loops=1)
                -> Index range scan on pipeline_run using idx_run_inbox_origin over (inbox_origin = 1 AND 4900 < id <= 10001)  (cost=0.251 rows=4) (actual time=0.0266..0.0293 rows=2 loops=1)
`

// Same exact five-node/one-leaf shape captured on 8.0.40 with estimated one
// output row but actual two at every iterator; estimates are not row visits.
const inboxWideIntersectEstimateOnePlan = `-> Limit: 100 row(s)  (cost=0.7 rows=1) (actual time=15.9..15.9 rows=2 loops=1)
    -> Sort: pipeline_run.id, limit input to 100 row(s) per chunk  (cost=0.7 rows=1) (actual time=15.9..15.9 rows=2 loops=1)
        -> Filter: ((pipeline_run.inbox_origin = 1) and (pipeline_run.id > 4900) and (pipeline_run.id <= 10001))  (cost=0.7 rows=1) (actual time=15.8..15.8 rows=2 loops=1)
            -> Intersect rows sorted by row ID  (cost=0.7 rows=1) (actual time=15.8..15.8 rows=2 loops=1)
                -> Index range scan on pipeline_run using idx_run_inbox_origin over (inbox_origin = 1 AND 4900 < id <= 10001)  (cost=0.25 rows=2) (actual time=15.8..15.8 rows=2 loops=1)
`

func TestInboxMySQLBoundedPlanSeparatesEstimateFromActualWork(t *testing.T) {
	for _, estimate := range []int64{1, 2, 3, 999} {
		plan := inboxObservedMergeEstimate()
		plan[0].Rows = estimate
		require.NoError(t, validateInboxMySQLPlan(plan, inboxWideIntersectEstimateOnePlan, "repair-wide", 2))
		// A harmless estimated count cannot excuse expanded actual work.
		require.Error(t, validateInboxMySQLPlan(plan, strings.ReplaceAll(inboxWideIntersectEstimateOnePlan, "rows=2 loops=1", "rows=3 loops=1"), "repair-wide", 2))
		require.Error(t, validateInboxMySQLPlan(plan, strings.ReplaceAll(inboxWideIntersectEstimateOnePlan, "rows=2 loops=1", "rows=2 loops=2"), "repair-wide", 2))
	}
	for _, estimate := range []int64{-1, 0, 1000, 5000} {
		plan := inboxObservedMergeEstimate()
		plan[0].Rows = estimate
		require.Error(t, validateInboxMySQLPlan(plan, inboxWideIntersectEstimateOnePlan, "repair-wide", 2))
	}
}

func inboxObservedMergeEstimate() []inboxMySQLQueryPlan {
	key, possible := "idx_run_inbox_origin,PRIMARY", "PRIMARY,idx_run_inbox_origin,idx_run_inbox_repair"
	extra := "Using intersect(idx_run_inbox_origin,PRIMARY); Using where; Using filesort"
	return []inboxMySQLQueryPlan{{Table: "pipeline_run", Key: &key, PossibleKeys: &possible, Rows: 2, Type: "index_merge", Filtered: 100, Extra: &extra}}
}

func TestInboxMySQLBoundedPlanObservedSingleLeafIntersection(t *testing.T) {
	for _, execution := range []string{
		inboxWideIntersectPlan,
		// Same full observed shape in utf8mb4_bin, including its actual timings.
		strings.NewReplacer("0.0506..0.0509", "0.0531..0.0534", "0.05..0.0502", "0.0523..0.0524",
			"0.0354..0.0425", "0.0365..0.0439", "0.0339..0.0406", "0.0345..0.0414", "0.0266..0.0293", "0.0268..0.0296").Replace(inboxWideIntersectPlan),
	} {
		require.NoError(t, validateInboxMySQLPlan(inboxObservedMergeEstimate(), execution, "repair-wide", 2))
	}
	for _, kind := range []string{"repair", "due", "unknown"} {
		require.Error(t, validateInboxMySQLPlan(inboxObservedMergeEstimate(), inboxWideIntersectPlan, kind, 2))
	}
	for i := range 5 {
		for _, metrics := range []string{"rows=0 loops=1", "rows=3 loops=1", "rows=5000 loops=1", "rows=2 loops=2", "rows=2 loops=0", "rows=NaN loops=1", "rows=2 loops=1e999"} {
			lines := strings.Split(strings.TrimSuffix(inboxWideIntersectPlan, "\n"), "\n")
			lines[i] = strings.Replace(lines[i], "rows=2 loops=1", metrics, 1)
			require.Error(t, validateInboxMySQLPlan(inboxObservedMergeEstimate(), strings.Join(lines, "\n"), "repair-wide", 2), "iterator %d: %s", i, metrics)
		}
	}
	for name, execution := range map[string]string{
		"unknown-intersect":        strings.Replace(inboxWideIntersectPlan, "Intersect rows sorted by row ID", "Union rows sorted by row ID", 1),
		"unlimited-sort":           strings.Replace(inboxWideIntersectPlan, ", limit input to 100 row(s) per chunk", "", 1),
		"larger-sort":              strings.Replace(inboxWideIntersectPlan, "limit input to 100", "limit input to 5000", 1),
		"wrong-sort":               strings.Replace(inboxWideIntersectPlan, "Sort: pipeline_run.id,", "Sort: pipeline_run.run_id,", 1),
		"descending-sort":          strings.Replace(inboxWideIntersectPlan, "Sort: pipeline_run.id,", "Sort: pipeline_run.id DESC,", 1),
		"wrong-limit":              strings.Replace(inboxWideIntersectPlan, "Limit: 100", "Limit: 1", 1),
		"wrong-filter":             strings.Replace(inboxWideIntersectPlan, "pipeline_run.inbox_origin = 1", "pipeline_run.inbox_origin = 0", 1),
		"unknown-leaf":             strings.Replace(inboxWideIntersectPlan, "Index range scan", "Index scan", 1),
		"unbounded-leaf":           strings.Replace(inboxWideIntersectPlan, "inbox_origin = 1 AND 4900 < id <= 10001", "inbox_origin = 1", 1),
		"wrong-lower-bound":        strings.Replace(inboxWideIntersectPlan, "4900 < id", "1 < id", 1),
		"wrong-filter-upper-bound": strings.Replace(inboxWideIntersectPlan, "id <= 10001)", "id <= 50000)", 1),
		"wrong-leaf-upper-bound":   strings.Replace(inboxWideIntersectPlan, "over (inbox_origin = 1 AND 4900 < id <= 10001)", "over (inbox_origin = 1 AND 4900 < id <= 50000)", 1),
		"inclusive-lower-bound":    strings.Replace(inboxWideIntersectPlan, "4900 < id", "4900 <= id", 1),
		"wrong-origin":             strings.Replace(inboxWideIntersectPlan, "over (inbox_origin = 1", "over (inbox_origin = 0", 1),
		"wrong-index":              strings.Replace(inboxWideIntersectPlan, "using idx_run_inbox_origin", "using PRIMARY", 1),
		"wrong-leaf-table":         strings.Replace(inboxWideIntersectPlan, "scan on pipeline_run", "scan on inbox_work", 1),
		"second-leaf":              inboxWideIntersectPlan + "                -> Index range scan on pipeline_run using PRIMARY over (4900 < id <= 10001)  (cost=1 rows=2) (actual time=0.01..0.02 rows=2 loops=1)\n",
		"sibling-branch":           strings.Replace(inboxWideIntersectPlan, "                ->", "            ->", 1),
		"extra-node":               inboxWideIntersectPlan + "                    -> Table scan on pipeline_run  (cost=1 rows=2) (actual time=0.01..0.02 rows=2 loops=1)\n",
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, validateInboxMySQLPlan(inboxObservedMergeEstimate(), execution, "repair-wide", 2))
		})
	}
	for _, mutate := range []func(*inboxMySQLQueryPlan){
		func(p *inboxMySQLQueryPlan) { p.Type = "range" },
		func(p *inboxMySQLQueryPlan) { p.Type = "ALL" },
		func(p *inboxMySQLQueryPlan) { p.Table = "inbox_work" },
		func(p *inboxMySQLQueryPlan) { p.Rows = 1000 },
		func(p *inboxMySQLQueryPlan) { p.Filtered = 50 },
		func(p *inboxMySQLQueryPlan) { p.Key = nil },
		func(p *inboxMySQLQueryPlan) { *p.Key = "PRIMARY,idx_run_inbox_origin" },
		func(p *inboxMySQLQueryPlan) { p.Extra = nil },
		func(p *inboxMySQLQueryPlan) { *p.Extra = "Using union(idx_run_inbox_origin,PRIMARY)" },
		func(p *inboxMySQLQueryPlan) { p.PossibleKeys = nil },
	} {
		plan := inboxObservedMergeEstimate()
		mutate(&plan[0])
		require.Error(t, validateInboxMySQLPlan(plan, inboxWideIntersectPlan, "repair-wide", 2))
	}
}
