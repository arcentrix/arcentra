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
	"testing"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func makeTrigger(typ string, opts map[string]any) *pipelinev1.Trigger {
	s, _ := structpb.NewStruct(opts)
	return &pipelinev1.Trigger{Type: typ, Options: s}
}

func TestExtractTriggersByType(t *testing.T) {
	spec := &pipelinev1.Spec{
		Triggers: []*pipelinev1.Trigger{
			makeTrigger("cron", map[string]any{"expression": "0 6 * * *"}),
			makeTrigger("event", map[string]any{"event_type": "push"}),
			makeTrigger("manual", nil),
			makeTrigger("cron", map[string]any{"expression": "*/30 * * * *"}),
		},
	}

	crons := ExtractTriggersByType(spec, TriggerTypeCron)
	if len(crons) != 2 {
		t.Fatalf("expected 2 cron triggers, got %d", len(crons))
	}

	events := ExtractTriggersByType(spec, TriggerTypeEvent)
	if len(events) != 1 {
		t.Fatalf("expected 1 event trigger, got %d", len(events))
	}

	manuals := ExtractTriggersByType(spec, TriggerTypeManual)
	if len(manuals) != 1 {
		t.Fatalf("expected 1 manual trigger, got %d", len(manuals))
	}
}

func TestExtractCronExpressions(t *testing.T) {
	spec := &pipelinev1.Spec{
		Triggers: []*pipelinev1.Trigger{
			makeTrigger("cron", map[string]any{"expression": "0 6 * * *"}),
			makeTrigger("cron", map[string]any{"expression": ""}),
			makeTrigger("cron", map[string]any{}),
			makeTrigger("event", map[string]any{"event_type": "push"}),
			makeTrigger("cron", map[string]any{"expression": "*/15 * * * *"}),
		},
	}

	exprs := ExtractCronExpressions(spec)
	if len(exprs) != 2 {
		t.Fatalf("expected 2 expressions, got %d: %v", len(exprs), exprs)
	}
	if exprs[0] != "0 6 * * *" {
		t.Errorf("expected first expression '0 6 * * *', got '%s'", exprs[0])
	}
	if exprs[1] != "*/15 * * * *" {
		t.Errorf("expected second expression '*/15 * * * *', got '%s'", exprs[1])
	}
}

func TestExtractCronExpressions_NilSpec(t *testing.T) {
	exprs := ExtractCronExpressions(nil)
	if len(exprs) != 0 {
		t.Fatalf("expected 0, got %d", len(exprs))
	}
}

func TestMatchEvent(t *testing.T) {
	tests := []struct {
		name      string
		trigger   *pipelinev1.Trigger
		eventType string
		ref       string
		want      bool
	}{
		{
			name:      "exact match push+main",
			trigger:   makeTrigger("event", map[string]any{"event_type": "push", "branch": "main"}),
			eventType: "push",
			ref:       "refs/heads/main",
			want:      true,
		},
		{
			name:      "event_type mismatch",
			trigger:   makeTrigger("event", map[string]any{"event_type": "push", "branch": "main"}),
			eventType: "pull_request",
			ref:       "refs/heads/main",
			want:      false,
		},
		{
			name:      "branch mismatch",
			trigger:   makeTrigger("event", map[string]any{"event_type": "push", "branch": "main"}),
			eventType: "push",
			ref:       "refs/heads/develop",
			want:      false,
		},
		{
			name:      "glob branch pattern",
			trigger:   makeTrigger("event", map[string]any{"event_type": "push", "branch": "release/*"}),
			eventType: "push",
			ref:       "refs/heads/release/1.0",
			want:      true,
		},
		{
			name:      "glob branch no match",
			trigger:   makeTrigger("event", map[string]any{"event_type": "push", "branch": "release/*"}),
			eventType: "push",
			ref:       "refs/heads/feature/foo",
			want:      false,
		},
		{
			name:      "event_type only (no branch filter)",
			trigger:   makeTrigger("event", map[string]any{"event_type": "push"}),
			eventType: "push",
			ref:       "refs/heads/anything",
			want:      true,
		},
		{
			name:      "no options matches everything",
			trigger:   makeTrigger("event", map[string]any{}),
			eventType: "push",
			ref:       "refs/heads/main",
			want:      true,
		},
		{
			name:      "nil trigger",
			trigger:   nil,
			eventType: "push",
			ref:       "refs/heads/main",
			want:      false,
		},
		{
			name:      "wrong trigger type",
			trigger:   makeTrigger("cron", map[string]any{"expression": "0 6 * * *"}),
			eventType: "push",
			ref:       "refs/heads/main",
			want:      false,
		},
		{
			name:      "bare branch name in ref",
			trigger:   makeTrigger("event", map[string]any{"event_type": "push", "branch": "main"}),
			eventType: "push",
			ref:       "main",
			want:      true,
		},
		{
			name:      "case insensitive event_type",
			trigger:   makeTrigger("event", map[string]any{"event_type": "Push"}),
			eventType: "push",
			ref:       "refs/heads/main",
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchEvent(tt.trigger, tt.eventType, tt.ref)
			if got != tt.want {
				t.Errorf("MatchEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchAnyEventTrigger(t *testing.T) {
	s := &pipelinev1.Spec{
		Triggers: []*pipelinev1.Trigger{
			makeTrigger("cron", map[string]any{"expression": "0 6 * * *"}),
			makeTrigger("event", map[string]any{"event_type": "push", "branch": "main"}),
			makeTrigger("event", map[string]any{"event_type": "pull_request"}),
		},
	}

	if !MatchAnyEventTrigger(s, "push", "refs/heads/main") {
		t.Error("expected match for push+main")
	}
	if !MatchAnyEventTrigger(s, "pull_request", "refs/heads/feature/x") {
		t.Error("expected match for pull_request")
	}
	if MatchAnyEventTrigger(s, "push", "refs/heads/develop") {
		t.Error("expected no match for push+develop")
	}
	if MatchAnyEventTrigger(s, "tag", "v1.0") {
		t.Error("expected no match for tag event")
	}
}
