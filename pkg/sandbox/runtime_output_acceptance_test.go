//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package sandbox

import (
	"context"
	"errors"
	"testing"
)

func TestAcceptanceWrappersPreserveRequiredOutputCompletion(t *testing.T) {
	stream := newCompletionIO(false)
	stream.finish()
	process := &containerdProcess{Process: &completionSDKProcess{stream: stream, fake: newFakeProcess(true)}}
	wrapped := &acceptanceRuntimeProcess{runtimeProcess: process}
	if err := wrapped.waitOutput(context.Background()); err != nil || stream.waits.Load() != 1 {
		t.Fatalf("acceptance process bypassed output completion: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := wrapped.waitOutput(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("acceptance wrapper revived canceled drain: %v", err)
	}
	task := &acceptanceRuntimeTask{runtimeTask: &containerdTask{Task: &completionSDKTask{stream: stream}}}
	if err := task.waitOutput(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("acceptance task bypassed output cancellation: %v", err)
	}
}
