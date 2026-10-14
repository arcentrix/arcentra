// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandboxexec

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/arcentrix/arcentra/pkg/sandbox"
)

type cyclicExitWrapper struct{}

func (e *cyclicExitWrapper) Error() string { return "cycle" }
func (e *cyclicExitWrapper) Unwrap() error { return e }

func TestCommandExitRequiresPureErrorTree(t *testing.T) {
	exit := &CommandExitError{Code: 7}
	var nilExit *CommandExitError
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{"plain", exit, true}, {"wrapped", fmt.Errorf("managed: %w", exit), true},
		{"joined exits", errors.Join(exit, &CommandExitError{Code: 9}), true},
		{"exit plus cleanup", errors.Join(exit, errors.New("cleanup failed")), false},
		{"wrapped join", fmt.Errorf("wrapped: %w", errors.Join(exit, context.Canceled)), false},
		{"deadline plus exit", errors.Join(context.DeadlineExceeded, exit), false},
		{"runtime", errors.New("sandbox command exited with code 7"), false},
		{"success code", &CommandExitError{Code: 0}, false}, {"negative code", &CommandExitError{Code: -1}, false},
		{"out of range", &CommandExitError{Code: 256}, false}, {"typed nil", nilExit, false},
		{"nil", nil, false}, {"cyclic wrapper", &cyclicExitWrapper{}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := IsCommandExit(test.err); got != test.want {
				t.Fatalf("classification=%v want=%v", got, test.want)
			}
		})
	}
}

func TestSessionProducesCommandExitOnlyForVerifiedResult(t *testing.T) {
	backendErr := errors.New("lost runtime result")
	for _, test := range []struct {
		name    string
		code    int32
		err     error
		command bool
	}{
		{"nonzero", 9, nil, true}, {"success", 0, nil, false}, {"invalid negative", -1, nil, false},
		{"invalid upper", 256, nil, false}, {"nonzero with runtime error", 9, backendErr, false},
		{"nonzero with timeout", 9, context.DeadlineExceeded, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := newTestSession(t, &fakeBackend{execute: func(context.Context, string, []string, *sandbox.ExecuteOptions) (*sandbox.ExecuteResult, error) {
				return &sandbox.ExecuteResult{ExitCode: test.code}, test.err
			}})
			_, err := session.Execute(context.Background(), request(session, "test command"))
			if IsCommandExit(err) != test.command {
				t.Fatalf("wrong command classification: %v", err)
			}
			if test.err != nil && !errors.Is(err, test.err) {
				t.Fatalf("runtime cause lost: %v", err)
			}
			if test.err != nil || test.code < 0 || test.code > 255 {
				if _, nextErr := session.Execute(context.Background(), request(session, "must not run")); nextErr == nil || IsCommandExit(nextErr) {
					t.Fatalf("runtime failure did not poison session: %v", nextErr)
				}
			}
		})
	}
}

func TestRetryCommandExitCodeRequiresConsistentAllowedLeaves(t *testing.T) {
	exit := &CommandExitError{Code: 9}
	for _, tc := range []struct {
		name    string
		err     error
		allowed []int32
		want    bool
	}{
		{"plain", exit, []int32{9}, true},
		{"wrapped", fmt.Errorf("command: %w", exit), []int32{9}, true},
		{"same code", errors.Join(exit, &CommandExitError{Code: 9}), []int32{9}, true},
		{"different codes", errors.Join(exit, &CommandExitError{Code: 10}), []int32{9, 10}, false},
		{"cleanup", errors.Join(exit, errors.New("cleanup failed")), []int32{9}, false},
		{"log", errors.Join(exit, errors.New("log append failed")), []int32{9}, false},
		{"cancel", errors.Join(exit, context.Canceled), []int32{9}, false},
		{"deadline", errors.Join(exit, context.DeadlineExceeded), []int32{9}, false},
		{"text", errors.New("sandbox command exited with code 9"), []int32{9}, false},
		{"not allowed", exit, []int32{10}, false},
		{"no allowlist", exit, nil, false},
		{"zero", &CommandExitError{Code: 0}, []int32{0}, false},
		{"negative", &CommandExitError{Code: -9}, []int32{-9}, false},
		{"signal", &CommandExitError{Code: 137}, []int32{137}, false},
		{"reserved 126", &CommandExitError{Code: 126}, []int32{126}, false},
		{"reserved 127", &CommandExitError{Code: 127}, []int32{127}, false},
		{"nil", nil, []int32{9}, false},
		{"typed nil", (*CommandExitError)(nil), []int32{9}, false},
		{"cycle", &cyclicExitWrapper{}, []int32{9}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, ok := RetryCommandExitCode(tc.err, tc.allowed)
			if ok != tc.want || ok && code != 9 || !ok && code != 0 {
				t.Fatalf("code=%d eligible=%v want=%v", code, ok, tc.want)
			}
		})
	}
}
