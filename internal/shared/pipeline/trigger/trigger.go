// Copyright 2026 Arcentra Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package trigger provides DSL trigger evaluation for pipeline-level triggers
// defined in pipeline YAML (triggers[].type = cron | event | manual).
package trigger

import (
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
)

const (
	// TriggerTypeManual represents a manual trigger.
	TriggerTypeManual = "manual"
	// TriggerTypeCron represents a cron schedule trigger.
	TriggerTypeCron = "cron"
	// TriggerTypeEvent represents an SCM webhook event trigger.
	TriggerTypeEvent = "event"

	// OptionExpression is the key in Trigger.Options for cron expressions.
	OptionExpression = "expression"
	// OptionEventType is the key in Trigger.Options for event type filtering.
	OptionEventType = "event_type"
	// OptionBranch is the key in Trigger.Options for branch filtering.
	OptionBranch = "branch"
)

// ExtractTriggersByType returns all pipeline-level triggers of the given type.
func ExtractTriggersByType(s *pipelinev1.Spec, triggerType string) []*pipelinev1.Trigger {
	if s == nil {
		return nil
	}
	var result []*pipelinev1.Trigger
	for _, t := range s.GetTriggers() {
		if t != nil && t.GetType() == triggerType {
			result = append(result, t)
		}
	}
	return result
}

// ExtractCronExpressions extracts all cron expressions from pipeline-level
// cron triggers. Returns a slice of non-empty expression strings.
func ExtractCronExpressions(s *pipelinev1.Spec) []string {
	triggers := ExtractTriggersByType(s, TriggerTypeCron)
	var exprs []string
	for _, t := range triggers {
		opts := spec.StructAsMap(t.GetOptions())
		if expr, ok := opts[OptionExpression].(string); ok && expr != "" {
			exprs = append(exprs, expr)
		}
	}
	return exprs
}
