package repo

import (
	"encoding/json"
	"fmt"
	"github.com/stretchr/testify/require"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// JSON proves scoped key access; ANALYZE proves bounded actual iterator work.
// Estimates and optimizer index names are deliberately not asserted.
var executionPlanOwnerBinding = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])owner_user_id\s*=\s*'voter'`)
var executionPlanProjectBinding = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])project_id\s*=\s*'project'`)
var executionPlanSort = regexp.MustCompile(`^Sort: copilot_execution_intent\.id(?: ASC)?(?:, limit input to 20 row\(s\) per chunk)?$`)
var executionPlanForbiddenBranch = regexp.MustCompile(`(?i)\b(?:or|union|intersect|join)\b`)
var executionPlanIndexLeaf = regexp.MustCompile(`^(?:Covering )?Index (?:range scan|lookup) on copilot_execution_intent using ([A-Za-z0-9_]+)\b`)

func validateExecutionMySQLListPlan(planJSON, execution string, after uint64) error {
	if len(planJSON) == 0 || len(planJSON) > 16384 || len(execution) == 0 || len(execution) > 8192 {
		return fmt.Errorf("execution plan diagnostic bounds")
	}
	var root map[string]any
	if json.Unmarshal([]byte(planJSON), &root) != nil || root["query_block"] == nil {
		return fmt.Errorf("execution structured plan invalid")
	}
	var tables []map[string]any
	var walk func(any) error
	walk = func(v any) error {
		switch x := v.(type) {
		case map[string]any:
			for k, y := range x {
				switch k {
				case "nested_loop", "union_result", "materialized_from_subquery", "attached_subqueries", "grouping_operation":
					return fmt.Errorf("execution unsupported plan branch")
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
	if len(tables) != 1 || tables[0]["table_name"] != "copilot_execution_intent" {
		return fmt.Errorf("execution requires one table")
	}
	table := tables[0]
	if table["access_type"] != "range" && table["access_type"] != "ref" {
		return fmt.Errorf("execution unbounded access method")
	}
	key, ok := table["key"].(string)
	if !ok || key == "" {
		return fmt.Errorf("execution missing scoped key")
	}
	parts, ok := table["used_key_parts"].([]any)
	if !ok || len(parts) < 2 || parts[0] != "owner_user_id" || parts[1] != "project_id" {
		return fmt.Errorf("execution missing owner/project index prefix")
	}
	lines := strings.Split(strings.TrimSuffix(execution, "\n"), "\n")
	if len(lines) < 2 || len(lines) > 5 {
		return fmt.Errorf("execution unsupported iterator count")
	}
	total := float64(0)
	cutoff := strconv.FormatUint(after, 10)
	idBound := regexp.MustCompile(`(?:\bid\s*>\s*` + cutoff + `\b|\b` + cutoff + `\s*<\s*(?:copilot_execution_intent\.)?id\b)`)
	boundedID := false
	previousRows := float64(1)
	for i, line := range lines {
		m := inboxPlanLine.FindStringSubmatch(line)
		if m == nil || len(m[1]) != i*4 {
			return fmt.Errorf("execution malformed or branched iterator")
		}
		desc := strings.ReplaceAll(m[2], "`", "")
		boundedID = boundedID || idBound.MatchString(desc)
		if executionPlanForbiddenBranch.MatchString(desc) {
			return fmt.Errorf("execution unsupported branching predicate")
		}
		metric := make([]float64, 4)
		for j := range metric {
			n, err := strconv.ParseFloat(m[j+3], 64)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
				return fmt.Errorf("execution invalid actual metric")
			}
			if j >= 2 {
				// Bound the parsed value and token before exact rational parsing;
				// hostile huge exponents must not allocate huge denominators.
				if len(m[j+3]) > 64 || j == 2 && (n < 1 || n > 1000) || j == 3 && n != 1 {
					return fmt.Errorf("execution count outside bounded domain")
				}
				exact, ok := new(big.Rat).SetString(m[j+3])
				if !ok || !exact.IsInt() {
					return fmt.Errorf("execution noninteger count")
				}
			}
			metric[j] = n
		}
		if metric[1] < metric[0] || metric[3] != 1 || metric[2] != math.Trunc(metric[2]) || metric[0] < 0 || metric[2] < previousRows || metric[2] > 1000 {
			return fmt.Errorf("execution actual owner work exceeded")
		}
		previousRows = metric[2]
		total += metric[2] * metric[3]
		switch {
		case i == 0:
			if desc != "Limit: 20 row(s)" || metric[2] != 1 {
				return fmt.Errorf("execution missing exact candidate limit")
			}
		case i == len(lines)-1:
			leaf := executionPlanIndexLeaf.FindStringSubmatch(desc)
			if leaf == nil || leaf[1] != key || !executionPlanOwnerBinding.MatchString(desc) || !executionPlanProjectBinding.MatchString(desc) {
				return fmt.Errorf("execution missing matching indexed leaf")
			}
		default:
			if !strings.HasPrefix(desc, "Filter: ") && !executionPlanSort.MatchString(desc) {
				return fmt.Errorf("execution unknown intermediate iterator")
			}
		}
	}
	if !boundedID {
		return fmt.Errorf("execution missing keyset bound")
	}
	if total > 5000 {
		return fmt.Errorf("execution total iterator work exceeded")
	}
	return nil
}

func executionListPlanJSON(key, access string, parts ...string) string {
	b, _ := json.Marshal(map[string]any{"query_block": map[string]any{"table": map[string]any{"table_name": "copilot_execution_intent", "access_type": access, "key": key, "used_key_parts": parts, "rows_examined_per_scan": 999999}}})
	return string(b)
}

const executionRangePlan = `-> Limit: 20 row(s)  (cost=3 rows=20) (actual time=0.1..0.2 rows=1 loops=1)
    -> Index range scan on copilot_execution_intent using any_owner_key over (owner_user_id = 'voter' AND project_id = 'project' AND 7 < id)  (cost=3 rows=999999) (actual time=0.1..0.2 rows=1 loops=1)
`
const executionLookupPlan = `-> Limit: 20 row(s)  (cost=3 rows=20) (actual time=0.1..0.2 rows=1 loops=1)
    -> Sort: copilot_execution_intent.id, limit input to 20 row(s) per chunk  (cost=3 rows=999) (actual time=0.1..0.2 rows=1 loops=1)
        -> Filter: (copilot_execution_intent.id > 7)  (cost=3 rows=999) (actual time=0.1..0.2 rows=1 loops=1)
            -> Index lookup on copilot_execution_intent using any_request_key (owner_user_id='voter', project_id='project')  (cost=3 rows=999) (actual time=0.1..0.2 rows=1000 loops=1)
`

func TestCopilotExecutionMySQLActualPlanShapes(t *testing.T) {
	require.NoError(t, validateExecutionMySQLListPlan(executionListPlanJSON("any_owner_key", "range", "owner_user_id", "project_id", "id"), executionRangePlan, 7))
	require.NoError(t, validateExecutionMySQLListPlan(executionListPlanJSON("any_request_key", "ref", "owner_user_id", "project_id"), executionLookupPlan, 7))
}
func TestCopilotExecutionMySQLActualPlanRejectsUnboundedWork(t *testing.T) {
	j := executionListPlanJSON("any_owner_key", "range", "owner_user_id", "project_id", "id")
	for _, tree := range []string{
		strings.ReplaceAll(executionRangePlan, "rows=1 loops=1", "rows=2001 loops=1"),
		strings.ReplaceAll(executionRangePlan, "loops=1", "loops=100000"),
		strings.ReplaceAll(executionRangePlan, "loops=1", "loops=1.00000000000000000001"),
		strings.ReplaceAll(executionRangePlan, "rows=1 loops=1", "rows=1.00000000000000000001 loops=1"),
		strings.ReplaceAll(executionRangePlan, "owner_user_id = 'voter' AND ", ""),
		strings.ReplaceAll(executionRangePlan, "project_id = 'project' AND ", ""),
		strings.ReplaceAll(executionRangePlan, "AND 7 < id", ""),
		strings.ReplaceAll(executionRangePlan, "AND 7 < id", "OR 7 < id"),
		strings.ReplaceAll(executionRangePlan, "Index range scan", "Table scan"),
		strings.ReplaceAll(executionRangePlan, "any_owner_key", "wrong_key"),
		strings.ReplaceAll(executionRangePlan, "0.1..0.2", "0.1..NaN"),
		strings.ReplaceAll(executionRangePlan, "loops=1", "loops=1e9999999"),
	} {
		require.Error(t, validateExecutionMySQLListPlan(j, tree, 7), tree)
	}
	for _, bad := range []string{`{}`, executionListPlanJSON("x", "ALL", "owner_user_id", "project_id"), executionListPlanJSON("any_owner_key", "range", "project_id", "id"), executionListPlanJSON("any_owner_key", "range", "owner_user_id"), strings.ReplaceAll(j, "copilot_execution_intent", "other_table")} {
		require.Error(t, validateExecutionMySQLListPlan(bad, executionRangePlan, 7))
	}
}
