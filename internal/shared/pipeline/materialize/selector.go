// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package materialize

import (
	"slices"
	"strconv"

	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
)

func MatchesAgentSelector(labels map[string]string, selector *spec.AgentSelector) bool {
	if selector == nil {
		return true
	}
	for key, value := range selector.MatchLabels {
		if v, ok := labels[key]; !ok || v != value {
			return false
		}
	}
	for _, expression := range selector.MatchExpressions {
		if expression == nil {
			return false
		}
		value, exists := labels[expression.Key]
		switch expression.Operator {
		case "In":
			if !exists || !slices.Contains(expression.Values, value) {
				return false
			}
		case "NotIn":
			if exists && slices.Contains(expression.Values, value) {
				return false
			}
		case "Exists":
			if !exists {
				return false
			}
		case "NotExists":
			if exists {
				return false
			}
		case "Gt", "Lt":
			if !exists || len(expression.Values) != 1 {
				return false
			}
			actual, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return false
			}
			expected, err := strconv.ParseFloat(expression.Values[0], 64)
			if err != nil {
				return false
			}
			if expression.Operator == "Gt" && actual <= expected || expression.Operator == "Lt" && actual >= expected {
				return false
			}
		default:
			return false
		}
	}
	return true
}
