//go:build !sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/arcentrix/arcentra/pkg/nova"
)

type acceptancePanicValueContext struct{ context.Context }

func (acceptancePanicValueContext) Value(any) any { panic("ordinary build inspected context values") }

func TestAcceptanceWorkerObservationDisabled(t *testing.T) {
	ctx := acceptancePanicValueContext{context.Background()}
	task := &nova.Task{Type: "unchanged", Payload: []byte("original bytes")}
	wantErr := errors.New("original error")
	calls := 0
	handler := nova.HandlerFunc(func(gotCtx context.Context, gotTask *nova.Task) error {
		calls++
		if gotCtx != ctx || gotTask != task {
			t.Fatal("identity helper changed handler arguments")
		}
		return wantErr
	})
	got := observeAcceptanceWorkerHandler(ctx, handler)
	if reflect.ValueOf(got).Pointer() != reflect.ValueOf(handler).Pointer() {
		t.Fatal("ordinary build replaced the handler")
	}
	if err := got(ctx, task); err != wantErr || calls != 1 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
	if observeAcceptanceWorkerReplay(ctx) != ctx {
		t.Fatal("ordinary build replaced the replay context")
	}
	if observeAcceptanceWorkerHandler(ctx, nil) != nil {
		t.Fatal("ordinary build replaced a nil handler")
	}
}
