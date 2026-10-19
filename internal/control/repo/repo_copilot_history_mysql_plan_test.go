// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// MySQL 8.0.40 FORMAT=JSON describes the chosen key prefix; ANALYZE TREE
// supplies actual per-iterator work. Estimates and index names are not proofs.
// https://dev.mysql.com/doc/refman/8.0/en/explain.html
// A serialized production create caps ALL rows for one owner at 1000. Thus a
// scoped request-key lookup + bounded sort is valid as well as a descending
// owner/id range. Neither permits scanning the 3000 unrelated fixture rows.
var historyPlanOwnerBinding = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])owner_user_id\s*=\s*'plan-owner'`)
var historyPlanProjectBinding = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])project_id\s*=\s*'plan-project'`)
var historyPlanSort = regexp.MustCompile(`^Sort: copilot_conversation\.id DESC(?:, limit input to 201 row\(s\) per chunk)?$`)
var historyPlanForbiddenBranch = regexp.MustCompile(`(?i)\b(?:or|union|intersect|join)\b`)
var historyPlanIndexLeaf = regexp.MustCompile(`^(?:Covering )?Index (?:range scan|lookup) on copilot_conversation using ([A-Za-z0-9_]+)\b`)

func validateHistoryMySQLListPlan(planJSON, execution string) error {
	if len(planJSON) == 0 || len(planJSON) > 16384 || len(execution) == 0 || len(execution) > 8192 {
		return fmt.Errorf("history plan diagnostic bounds")
	}
	var root map[string]any
	if json.Unmarshal([]byte(planJSON), &root) != nil || root["query_block"] == nil {
		return fmt.Errorf("history structured plan invalid")
	}
	var tables []map[string]any
	var walk func(any) error
	walk = func(v any) error {
		switch x := v.(type) {
		case map[string]any:
			for k, y := range x {
				switch k {
				case "nested_loop", "union_result", "materialized_from_subquery", "attached_subqueries", "grouping_operation":
					return fmt.Errorf("history unsupported plan branch")
				}
				if k == "table_name" {
					tables = append(tables, x)
				}
				if err := walk(y); err != nil {
					return err
				}
			}
		case []any:
			for _, y := range x {
				if err := walk(y); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(root); err != nil {
		return err
	}
	if len(tables) != 1 || tables[0]["table_name"] != "copilot_conversation" {
		return fmt.Errorf("history requires one table")
	}
	table := tables[0]
	if table["access_type"] != "range" && table["access_type"] != "ref" {
		return fmt.Errorf("history unbounded access method")
	}
	key, ok := table["key"].(string)
	if !ok || key == "" {
		return fmt.Errorf("history missing scoped key")
	}
	parts, ok := table["used_key_parts"].([]any)
	if !ok || len(parts) < 2 || parts[0] != "owner_user_id" || parts[1] != "project_id" {
		return fmt.Errorf("history missing owner/project index prefix")
	}
	lines := strings.Split(strings.TrimSuffix(execution, "\n"), "\n")
	if len(lines) < 2 || len(lines) > 5 {
		return fmt.Errorf("history unsupported iterator count")
	}
	total := float64(0)
	previousRows := float64(201)
	for i, line := range lines {
		m := inboxPlanLine.FindStringSubmatch(line)
		if m == nil || len(m[1]) != i*4 {
			return fmt.Errorf("history malformed or branched iterator")
		}
		desc := strings.ReplaceAll(m[2], "`", "")
		if historyPlanForbiddenBranch.MatchString(desc) {
			return fmt.Errorf("history unsupported branching predicate")
		}
		metric := make([]float64, 4)
		for j := range metric {
			n, err := strconv.ParseFloat(m[j+3], 64)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
				return fmt.Errorf("history invalid actual metric")
			}
			if j >= 2 {
				// Bound the parsed value and token before exact rational parsing;
				// hostile huge exponents must not allocate huge denominators.
				if len(m[j+3]) > 64 || j == 2 && (n < 201 || n > 1000) || j == 3 && n != 1 {
					return fmt.Errorf("history count outside bounded domain")
				}
				exact, ok := new(big.Rat).SetString(m[j+3])
				if !ok || !exact.IsInt() {
					return fmt.Errorf("history noninteger count")
				}
			}
			metric[j] = n
		}
		if metric[1] < metric[0] || metric[3] != 1 || metric[2] != math.Trunc(metric[2]) || metric[0] < 0 || metric[2] < previousRows || metric[2] > 1000 {
			return fmt.Errorf("history actual owner work exceeded")
		}
		previousRows = metric[2]
		total += metric[2] * metric[3]
		switch {
		case i == 0:
			if desc != "Limit: 201 row(s)" || metric[2] != 201 {
				return fmt.Errorf("history missing exact candidate limit")
			}
		case i == len(lines)-1:
			leaf := historyPlanIndexLeaf.FindStringSubmatch(desc)
			if leaf == nil || leaf[1] != key || !historyPlanOwnerBinding.MatchString(desc) || !historyPlanProjectBinding.MatchString(desc) {
				return fmt.Errorf("history missing matching indexed leaf")
			}
		default:
			if !strings.HasPrefix(desc, "Filter: ") && !historyPlanSort.MatchString(desc) {
				return fmt.Errorf("history unknown intermediate iterator")
			}
		}
	}
	if total > 5000 {
		return fmt.Errorf("history total iterator work exceeded")
	}
	return nil
}

func historyPlanJSON(key, access string, parts ...string) string {
	b, _ := json.Marshal(map[string]any{"query_block": map[string]any{"table": map[string]any{"table_name": "copilot_conversation", "access_type": access, "key": key, "used_key_parts": parts, "rows_examined_per_scan": 999999}}})
	return string(b)
}

const historyListRangePlan = `-> Limit: 201 row(s)  (cost=3 rows=201) (actual time=0.1..0.2 rows=201 loops=1)
    -> Filter: (copilot_conversation.state = 'active')  (cost=3 rows=201) (actual time=0.1..0.2 rows=201 loops=1)
        -> Index range scan on copilot_conversation using any_owner_key over (owner_user_id = 'plan-owner' AND project_id = 'plan-project' AND id < 4000) (reverse)  (cost=3 rows=999) (actual time=0.1..0.2 rows=201 loops=1)
`
const historyListLookupSortPlan = `-> Limit: 201 row(s)  (cost=3 rows=201) (actual time=0.1..0.2 rows=201 loops=1)
    -> Sort: copilot_conversation.id DESC, limit input to 201 row(s) per chunk  (cost=3 rows=999) (actual time=0.1..0.2 rows=201 loops=1)
        -> Filter: (copilot_conversation.id < 4000)  (cost=3 rows=999) (actual time=0.1..0.2 rows=1000 loops=1)
            -> Index lookup on copilot_conversation using any_request_key (owner_user_id='plan-owner', project_id='plan-project')  (cost=3 rows=999) (actual time=0.1..0.2 rows=1000 loops=1)
`

func TestCopilotHistoryMySQLPlanSupportsScopedRangeAndLookup(t *testing.T) {
	require.NoError(t, validateHistoryMySQLListPlan(historyPlanJSON("any_owner_key", "range", "owner_user_id", "project_id", "id"), historyListRangePlan))
	require.NoError(t, validateHistoryMySQLListPlan(historyPlanJSON("any_request_key", "ref", "owner_user_id", "project_id"), historyListLookupSortPlan))
}
func TestCopilotHistoryMySQLPlanRejectsScopeAndAccessDrift(t *testing.T) {
	for _, p := range []string{
		`{}`, `{"query_block":{"nested_loop":[]}}`,
		historyPlanJSON("any_request_key", "ALL", "owner_user_id", "project_id"),
		historyPlanJSON("any_request_key", "index", "owner_user_id", "project_id"),
		historyPlanJSON("any_request_key", "ref", "project_id", "owner_user_id"),
		historyPlanJSON("any_request_key", "ref", "owner_user_id"),
		historyPlanJSON("", "ref", "owner_user_id", "project_id"),
		strings.Replace(historyPlanJSON("any_request_key", "ref", "owner_user_id", "project_id"), "copilot_conversation", "other_table", 1),
	} {
		require.Error(t, validateHistoryMySQLListPlan(p, historyListLookupSortPlan))
	}
}
func TestCopilotHistoryMySQLPlanRejectsUnboundedActualWork(t *testing.T) {
	p := historyPlanJSON("any_request_key", "ref", "owner_user_id", "project_id")
	for _, s := range []string{
		strings.ReplaceAll(historyListLookupSortPlan, "rows=1000 loops=1", "rows=1001 loops=1"),
		strings.ReplaceAll(historyListLookupSortPlan, "rows=1000 loops=1", "rows=4000 loops=1"),
		strings.ReplaceAll(historyListLookupSortPlan, "rows=1000 loops=1", "rows=1000 loops=2"),
		strings.ReplaceAll(historyListLookupSortPlan, "rows=1000 loops=1", "rows=NaN loops=1"),
		strings.ReplaceAll(historyListLookupSortPlan, "rows=1000 loops=1", "rows=1e-999999999 loops=1"),
		strings.ReplaceAll(historyListLookupSortPlan, "rows=1000 loops=1", "rows=201.5 loops=1"),
		strings.ReplaceAll(historyListLookupSortPlan, "rows=1000 loops=1", "rows=200.999999999999999 loops=1"),
		strings.Replace(historyListLookupSortPlan, "project_id='plan-project'", "project_id='wrong'", 1),
		strings.Replace(historyListLookupSortPlan, "project_id='plan-project'", "wrong_project_id='plan-project'", 1),
		strings.Replace(historyListLookupSortPlan, "owner_user_id='plan-owner'", "owner_user_id>'plan-owner'", 1),
		strings.Replace(historyListLookupSortPlan, "actual time=0.1..0.2 rows=1000 loops=1)\n", "actual time=0.1..0.2 rows=1001 loops=1)\n", 1),
		strings.Replace(historyListLookupSortPlan, "Sort: copilot_conversation.id DESC", "Sort: copilot_conversation.id ASC", 1),
		strings.Replace(historyListLookupSortPlan, "Sort: copilot_conversation.id DESC", "Sort: copilot_conversation.id DESC, other ASC", 1),
		strings.Replace(historyListLookupSortPlan, "owner_user_id='plan-owner',", "owner_user_id='plan-owner' OR", 1),
		strings.Replace(historyListLookupSortPlan, "Filter: (copilot_conversation.id < 4000)", "Filter: ((copilot_conversation.id < 4000) OR 1)", 1),
		strings.Replace(historyListLookupSortPlan, "Limit: 201", "Limit: 2000", 1),
		strings.Replace(historyListLookupSortPlan, "Index lookup", "Table scan", 1),
		strings.Replace(historyListLookupSortPlan, "using any_request_key", "using wrong_key", 1),
		strings.Replace(historyListLookupSortPlan, "Filter:", "Nested loop inner join:", 1),
		strings.Replace(historyListLookupSortPlan, "            ->", "        ->", 1),
		historyListLookupSortPlan + "            -> Table scan on other_table  (cost=3 rows=201) (actual time=0.1..0.2 rows=201 loops=1)\n",
	} {
		require.Error(t, validateHistoryMySQLListPlan(p, s))
	}
}
