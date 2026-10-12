// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package terminalops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/agent/taskqueue"
	"github.com/bytedance/sonic"
)

type fakeStore struct {
	records   []taskqueue.TerminalQuarantineSummary
	err       error
	closeErr  error
	closed    bool
	listCalls int
	kind      string
	runID     string
	review    string
}

func (f *fakeStore) List(context.Context) ([]taskqueue.TerminalQuarantineSummary, error) {
	f.listCalls++
	return f.records, f.err
}

func (f *fakeStore) Inspect(_ context.Context, kind, runID string) (taskqueue.TerminalQuarantineSummary, error) {
	f.kind, f.runID = kind, runID
	if f.err != nil {
		return taskqueue.TerminalQuarantineSummary{}, f.err
	}
	return f.records[0], nil
}

func (f *fakeStore) MarkReviewed(ctx context.Context, kind, runID, disposition string) (taskqueue.TerminalQuarantineSummary, error) {
	f.review = disposition
	return f.Inspect(ctx, kind, runID)
}

func (f *fakeStore) Close() error {
	f.closed = true
	return f.closeErr
}

func commandArgs(action string, extras ...string) []string {
	args := []string{commandName, action, "--directory", "/private/existing-outbox",
		"--agent-id", "agent-1", "--controller", "controller:9090"}
	return append(args, extras...)
}

func runFake(t *testing.T, ctx context.Context, args []string, store *fakeStore) (int, string, string, bool) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	opened := false
	handled, code := run(ctx, args, &stdout, &stderr, func(_ context.Context, directory, agentID, controller string) (quarantineStore, error) {
		opened = true
		if directory != "/private/existing-outbox" || agentID != "agent-1" || controller != "controller:9090" {
			t.Fatal("command did not preserve explicit identity selectors")
		}
		return store, nil
	})
	if !handled {
		t.Fatal("quarantine command was not handled")
	}
	return code, stdout.String(), stderr.String(), opened
}

func TestNonOperatorCommandNotHandled(t *testing.T) {
	for _, args := range [][]string{nil, {"--conf", "agent.toml"}, {"different-command"}} {
		var output bytes.Buffer
		open := func(context.Context, string, string, string) (quarantineStore, error) {
			t.Fatal("ordinary Agent command opened a quarantine")
			return nil, nil
		}
		handled, code := run(context.Background(), args, &output, &output, open)
		if handled || code != 0 || output.Len() != 0 {
			t.Fatalf("ordinary command handled=%v code=%d output=%q", handled, code, output.String())
		}
	}
}

func TestHelpDoesNotOpenOutbox(t *testing.T) {
	for _, args := range [][]string{{commandName, "--help"}, {commandName, "list", "--help"}, {commandName, "mark-reviewed", "-h"}} {
		code, out, errOut, opened := runFake(t, context.Background(), args, &fakeStore{})
		if code != 0 || !strings.Contains(out, "Usage:") || errOut != "" || opened {
			t.Fatalf("help result: code=%d opened=%v stdout=%q stderr=%q", code, opened, out, errOut)
		}
	}
}

func TestArgumentsFailBeforeOpening(t *testing.T) {
	secret := "sensitive-value-do-not-print"
	cases := [][]string{
		{commandName},
		{commandName, "delete"},
		{commandName, "list"},
		{commandName, "list", "--directory", "/private/existing-outbox", "--agent-id", "agent-1"},
		commandArgs("list", "--token", secret),
		commandArgs("list", "--limit", secret),
		commandArgs("list", "--limit", "0"),
		commandArgs("list", "--limit", "1001"),
		commandArgs("list", "--offset", "-1"),
		commandArgs("list", "--offset", "10001"),
		commandArgs("list", secret),
		commandArgs("inspect", "--kind", "job"),
		commandArgs("inspect", "--kind", "invalid", "--run-id", "run-1"),
		commandArgs("inspect", "--kind", "job", "--run-id", "\n"+secret),
		commandArgs("mark-reviewed", "--kind", "job", "--run-id", "run-1", "--disposition", "investigated"),
		commandArgs("mark-reviewed", "--kind", "job", "--run-id", "run-1", "--disposition", "retry", "--confirm-reviewed"),
		commandArgs("mark-reviewed", "--kind", "job", "--run-id", "run-1", "--disposition", "investigated", "--confirm-reviewed=false"),
	}
	for i, args := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			code, out, errOut, opened := runFake(t, context.Background(), args, &fakeStore{})
			if code != 2 || out != "" || errOut == "" || opened || strings.Contains(errOut, secret) {
				t.Fatalf("invalid arguments: code=%d opened=%v stdout=%q stderr=%q", code, opened, out, errOut)
			}
		})
	}
}

func TestListOutputIsPaginatedAndBounded(t *testing.T) {
	store := &fakeStore{}
	for i := 0; i < 205; i++ {
		store.records = append(store.records, taskqueue.TerminalQuarantineSummary{
			Kind: "step", RunID: fmt.Sprintf("run-%03d", i), Reason: "terminal_conflict",
		})
	}
	code, out, errOut, opened := runFake(t, context.Background(), commandArgs("list", "--offset", "100", "--limit", "100"), store)
	if code != 0 || errOut != "" || !opened || !store.closed {
		t.Fatalf("list failed: code=%d stderr=%q", code, errOut)
	}
	var result listResult
	if err := sonic.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.Total != 205 || result.Offset != 100 || len(result.Records) != 100 || result.NextOffset == nil || *result.NextOffset != 200 {
		t.Fatalf("unexpected page: %+v", result)
	}
	if result.Records[0].RunID != "run-100" || result.Records[99].RunID != "run-199" {
		t.Fatal("wrong sorted page")
	}
	code, out, errOut, _ = runFake(t, context.Background(), commandArgs("list", "--offset", "205"), store)
	if code != 0 || errOut != "" || strings.Contains(out, "nextOffset") || !strings.Contains(out, `"records":[]`) {
		t.Fatalf("empty last page failed: code=%d stdout=%q stderr=%q", code, out, errOut)
	}
}

func TestListEmptyArray(t *testing.T) {
	code, out, errOut, _ := runFake(t, context.Background(), commandArgs("list"), &fakeStore{})
	if code != 0 || errOut != "" || out != "{\"records\":[],\"total\":0,\"offset\":0}\n" {
		t.Fatalf("empty list failed: code=%d stdout=%q stderr=%q", code, out, errOut)
	}
}

func TestInspectAndConfirmedReviewUseExactSelector(t *testing.T) {
	for _, action := range []string{"inspect", "mark-reviewed"} {
		for _, disposition := range []string{"investigated", "verified_external_effects"} {
			store := &fakeStore{records: []taskqueue.TerminalQuarantineSummary{{Kind: "step", RunID: "run-1", Reason: "terminal_conflict"}}}
			args := commandArgs(action, "--kind", "step", "--run-id", "run-1")
			if action == "mark-reviewed" {
				args = append(args, "--disposition", disposition, "--confirm-reviewed")
			}
			code, out, errOut, _ := runFake(t, context.Background(), args, store)
			if code != 0 || errOut != "" || !store.closed || store.kind != "step" ||
				store.runID != "run-1" || !strings.Contains(out, `"runId":"run-1"`) {
				t.Fatalf("selector failed: code=%d stderr=%q store=%+v", code, errOut, store)
			}
			if action == "mark-reviewed" && store.review != disposition {
				t.Fatal("review disposition was not passed exactly")
			}
		}
	}
}

func TestBackendAndOutputErrorsAreSanitized(t *testing.T) {
	secretErr := errors.New("secret-payload-token-password-must-not-print")
	for _, action := range []string{"list", "inspect", "mark-reviewed"} {
		store := &fakeStore{err: secretErr}
		args := commandArgs(action)
		if action != "list" {
			args = append(args, "--kind", "job", "--run-id", "run-1")
		}
		if action == "mark-reviewed" {
			args = append(args, "--disposition", "investigated", "--confirm-reviewed")
		}
		code, out, errOut, _ := runFake(t, context.Background(), args, store)
		if code != 1 || out != "" || !store.closed || errOut == "" || strings.Contains(errOut, secretErr.Error()) {
			t.Fatalf("backend error leaked or not closed: code=%d stdout=%q stderr=%q", code, out, errOut)
		}
	}
	var stderr bytes.Buffer
	open := func(context.Context, string, string, string) (quarantineStore, error) { return nil, secretErr }
	_, code := run(context.Background(), commandArgs("list"), io.Discard, &stderr, open)
	if code != 1 || strings.Contains(stderr.String(), secretErr.Error()) {
		t.Fatalf("open error not sanitized: code=%d stderr=%q", code, stderr.String())
	}
	store := &fakeStore{closeErr: secretErr}
	code, out, errOut, _ := runFake(t, context.Background(), commandArgs("list"), store)
	if code != 1 || out != "" || strings.Contains(errOut, secretErr.Error()) {
		t.Fatalf("close error not sanitized: code=%d stdout=%q stderr=%q", code, out, errOut)
	}
}

func TestCanceledContextDoesNotOpen(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code, out, errOut, opened := runFake(t, ctx, commandArgs("list"), &fakeStore{})
	if code != 1 || out != "" || errOut == "" || opened {
		t.Fatalf("cancellation failed: code=%d opened=%v stdout=%q stderr=%q", code, opened, out, errOut)
	}
}

func TestLargeSummaryFailsWithoutPartialOutput(t *testing.T) {
	store := &fakeStore{records: []taskqueue.TerminalQuarantineSummary{{Kind: "job", RunID: strings.Repeat("x", maxOutputBytes)}}}
	code, out, errOut, _ := runFake(t, context.Background(), commandArgs("list"), store)
	if code != 1 || out != "" || errOut == "" || !store.closed {
		t.Fatalf("output bound failed: code=%d stdout length=%d stderr=%q", code, len(out), errOut)
	}
}
