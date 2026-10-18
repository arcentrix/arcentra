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
