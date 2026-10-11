// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package outbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOutboxRejectsUndeliverablePayloadBeforeSequenceAllocation(t *testing.T) {
	cfg := Config{WALDir: t.TempDir(), AgentID: "agent", SendInterval: time.Hour}
	o, err := NewOutbox(cfg, &mockSender{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Close() })
	ctx := context.Background()
	if seq, err := o.AppendMap(ctx, map[string]any{"data": strings.Repeat("x", MaxEventPayloadBytes)}); !errors.Is(err, ErrEventPayloadTooLarge) || seq != 0 {
		t.Fatalf("oversized payload = %d, %v", seq, err)
	}
	// Escaping can make a small decoded string exceed the server's JSON limit.
	if seq, err := o.AppendMap(ctx, map[string]any{"data": strings.Repeat("\x00", MaxEventPayloadBytes/6)}); !errors.Is(err, ErrEventPayloadTooLarge) || seq != 0 {
		t.Fatalf("escaped oversized payload = %d, %v", seq, err)
	}
	for _, payload := range []string{"not JSON", "null", "[]", `{"value":1e999}`} {
		if seq, err := o.Append(ctx, []byte(payload)); err == nil || seq != 0 {
			t.Fatalf("invalid %q = %d, %v", payload, seq, err)
		}
	}
	if seq, err := o.AppendMap(ctx, map[string]any{"valid": true}); err != nil || seq != 1 {
		t.Fatalf("rejected records created a sequence gap: %d, %v", seq, err)
	}
}

func TestOutboxPayloadAtJSONLimit(t *testing.T) {
	// {"data":"..."} adds exactly 11 bytes to the JSON string.
	if err := ValidateEventPayload(map[string]any{"data": strings.Repeat("x", MaxEventPayloadBytes-11)}); err != nil {
		t.Fatalf("payload at limit rejected: %v", err)
	}
	if err := ValidateEventPayload(map[string]any{"data": strings.Repeat("x", MaxEventPayloadBytes-10)}); !errors.Is(err, ErrEventPayloadTooLarge) {
		t.Fatalf("payload beyond limit = %v", err)
	}
	if err := ValidateEventPayload(map[string]any{"invalid": make(chan int)}); err == nil {
		t.Fatal("unsupported protobuf value accepted")
	}
}
