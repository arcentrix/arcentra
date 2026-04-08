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

package trigger

import (
	"path"
	"strings"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/pkg/pipeline/spec"
)

// MatchEvent checks whether a single event trigger definition matches the
// incoming SCM event (eventType such as "push", ref such as "refs/heads/main").
func MatchEvent(trigger *pipelinev1.Trigger, eventType string, ref string) bool {
	if trigger == nil || trigger.GetType() != TriggerTypeEvent {
		return false
	}

	opts := spec.StructAsMap(trigger.GetOptions())

	if wantType, ok := opts[OptionEventType].(string); ok && wantType != "" {
		if !strings.EqualFold(wantType, eventType) {
			return false
		}
	}

	if wantBranch, ok := opts[OptionBranch].(string); ok && wantBranch != "" {
		if !matchBranch(wantBranch, ref) {
			return false
		}
	}

	return true
}

// MatchAnyEventTrigger returns true when any event trigger in the spec matches
// the given SCM event.
func MatchAnyEventTrigger(s *pipelinev1.Spec, eventType string, ref string) bool {
	for _, t := range ExtractTriggersByType(s, TriggerTypeEvent) {
		if MatchEvent(t, eventType, ref) {
			return true
		}
	}
	return false
}

// matchBranch checks if ref matches the branch pattern. The pattern can be:
//   - a literal branch name: "main" matches "refs/heads/main" or "main"
//   - a glob pattern: "release/*" matches "refs/heads/release/1.0"
func matchBranch(pattern string, ref string) bool {
	branch := NormalizeBranch(ref)

	if strings.EqualFold(pattern, branch) {
		return true
	}

	matched, _ := path.Match(pattern, branch)
	return matched
}

// NormalizeBranch strips the "refs/heads/" prefix from a Git ref.
func NormalizeBranch(ref string) string {
	return strings.TrimPrefix(ref, "refs/heads/")
}
