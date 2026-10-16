//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/executor"
)

// Take one read-only snapshot only after the exact-count waiter has failed.
// Keep its original limit, timeout and assertions. Diagnostics never print log
// content, IDs, commands, caller-provided names, or arbitrary error strings.
func containerAcceptanceStepLogDiagnostic(t *testing.T, f *controllerWorkerFixture, steps []model.StepRun, publisher executor.LogPublisher, wanted map[string]int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
	defer cancel()
	ids := make([]string, 0, len(steps))
	for _, step := range steps {
		ids = append(ids, step.StepRunID)
	}
	var total int64
	countErr := f.db.WithContext(ctx).Model(&model.StepRunLog{}).Where("step_run_id IN ?", ids).Count(&total).Error
	rows, queryErr := f.repos.Log.ListByStepRunIDs(ctx, ids, 0, 1000)
	t.Logf("failed-step log snapshot: rows=%d total=%d limit=1000 limitReached=%t countError=%s queryError=%s", len(rows), total, len(rows) == 1000, containerAcceptanceStepLogErrorClass(countErr), containerAcceptanceStepLogErrorClass(queryErr))
	if status, ok := publisher.(interface{ LastError() error }); ok {
		t.Logf("failed-step log publisher: lastError=%s", containerAcceptanceStepLogErrorClass(status.LastError()))
	}
	if queryErr != nil {
		return
	}
	for _, line := range containerAcceptanceStepLogSnapshotLines(rows, steps, f.conf.Agent.ID, wanted) {
		t.Log(line)
	}
}

func containerAcceptanceStepLogErrorClass(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, errContainerAcceptanceStepLogGap):
		return "line-gap"
	default:
		return "present"
	}
}

// Receiver-assigned contiguous positions cannot establish source completeness.
// Counts and trailing byte lengths distinguish missing records from complete
// persisted streams that still lack a required synthetic marker.
func containerAcceptanceStepLogSnapshotLines(rows []*model.StepRunLog, steps []model.StepRun, agentID string, wanted map[string]int) []string {
	counts, err := containerAcceptanceStepMarkerCounts(rows, agentID)
	lines := []string{fmt.Sprintf("failed-step log reconstruction: error=%s", containerAcceptanceStepLogErrorClass(err))}
	for _, marker := range containerAcceptanceStepDiagnosticMarkers {
		lines = append(lines, fmt.Sprintf("failed-step synthetic marker=%s count=%d want=%d countsValid=%t", marker, counts[marker], wanted[marker], err == nil))
	}
	for ordinal, step := range steps[:min(len(steps), 24)] {
		var selected []*model.StepRunLog
		for _, row := range rows {
			if row != nil && row.StepRunID == step.StepRunID {
				selected = append(selected, row)
			}
		}
		sort.Slice(selected, func(i, j int) bool { return selected[i].LineNumber < selected[j].LineNumber })
		var last, first int32
		var missing int64
		gaps, duplicates, stdoutRows, stderrRows, otherRows, byteCount := 0, 0, 0, 0, 0, 0
		var stdout strings.Builder
		for _, row := range selected {
			if first == 0 {
				first = row.LineNumber
			}
			if row.LineNumber == last {
				duplicates++
			} else if int64(row.LineNumber) > int64(last)+1 {
				gaps++
				missing += int64(row.LineNumber) - int64(last) - 1
			}
			last = row.LineNumber
			byteCount += len(row.Content)
			switch row.Stream {
			case "stdout":
				stdoutRows++
				if byteCount <= 1024*1024 {
					stdout.WriteString(row.Content)
				}
			case "stderr":
				stderrRows++
			default:
				otherRows++
			}
		}
		stream := stdout.String()
		trailing := len(stream) - strings.LastIndex(stream, "\n") - 1
		stepCounts, stepErr := containerAcceptanceStepMarkerCounts(selected, agentID)
		markerCounts := make([]string, 0, len(containerAcceptanceStepDiagnosticMarkers))
		for _, marker := range containerAcceptanceStepDiagnosticMarkers {
			markerCounts = append(markerCounts, fmt.Sprintf("%s:%d", marker, stepCounts[marker]))
		}
		lines = append(lines, fmt.Sprintf("failed-step log scope=%d index=%d attempt=%d admitted=%t rows=%d first=%d last=%d gaps=%d missing=%d duplicatePositions=%d stdout=%d stderr=%d other=%d bytes=%d trailingStdoutBytes=%d bounded=%t reconstruction=%s markers=[%s]", ordinal, step.StepIndex, step.Attempt, step.AttemptAdmission != "", len(selected), first, last, gaps, missing, duplicates, stdoutRows, stderrRows, otherRows, byteCount, trailing, byteCount <= 1024*1024, containerAcceptanceStepLogErrorClass(stepErr), strings.Join(markerCounts, ",")))
	}
	return lines
}

func TestContainerAcceptanceStepLogDiagnostics(t *testing.T) {
	const private = "private-command-token-should-never-print"
	steps := []model.StepRun{{StepRunID: private, StepIndex: 2, Attempt: 1, AttemptAdmission: private}, {StepRunID: "empty", StepIndex: 2, Attempt: 2}}
	row := func(line int32, content string) *model.StepRunLog {
		return &model.StepRunLog{LogID: fmt.Sprint(line), AgentID: "agent", StepRunID: private, LineNumber: line, Stream: "stdout", Content: content}
	}
	wanted := map[string]int{"step-failed-first": 1}
	for _, test := range []struct {
		name string
		rows []*model.StepRunLog
		want []string
	}{
		{"complete-with-missing-marker", []*model.StepRunLog{row(1, private+"\n"), row(2, "step-failed-")}, []string{"count=0 want=1 countsValid=true", "rows=2 first=1 last=2 gaps=0 missing=0", "trailingStdoutBytes=12", "rows=0 first=0 last=0"}},
		{"gap", []*model.StepRunLog{row(1, "step-failed-"), row(3, "first\n")}, []string{"error=line-gap", "countsValid=false", "gaps=1 missing=1"}},
		{"duplicate", []*model.StepRunLog{row(1, private), row(1, private)}, []string{"error=present", "duplicatePositions=1"}},
		{"split-marker", []*model.StepRunLog{row(2, "first\n"), row(1, "step-failed-")}, []string{"count=1 want=1 countsValid=true", "trailingStdoutBytes=0"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := strings.Join(containerAcceptanceStepLogSnapshotLines(test.rows, steps, "agent", wanted), "\n")
			if strings.Contains(result, private) {
				t.Fatal("diagnostics disclosed arbitrary content or identity")
			}
			for _, want := range test.want {
				if !strings.Contains(result, want) {
					t.Fatalf("missing fixed diagnostic %q", want)
				}
			}
		})
	}
	for _, err := range []error{errors.New(private), fmt.Errorf("%s: %w", private, context.DeadlineExceeded)} {
		if strings.Contains(containerAcceptanceStepLogErrorClass(err), private) {
			t.Fatal("diagnostics disclosed arbitrary error text")
		}
	}
}
