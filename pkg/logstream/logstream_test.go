// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package logstream

import (
	"strings"
	"testing"

	"github.com/bytedance/sonic"
)

func TestValidateEventID(t *testing.T) {
	for _, eventID := range []string{"", "073b7b54-629d-4a91-bfd8-e0777c66c909", "agent_1.log-42", strings.Repeat("a", MaxEventIDLength)} {
		if err := ValidateEventID(eventID); err != nil {
			t.Errorf("valid event id %q: %v", eventID, err)
		}
	}
	for _, eventID := range []string{" ", "event id", "event/id", "event:id", "event\x00id", "event\nid", "event\u00e9", strings.Repeat("a", MaxEventIDLength+1)} {
		if err := ValidateEventID(eventID); err == nil {
			t.Errorf("invalid event id %q was accepted", eventID)
		}
	}
}

func TestBuildLogMessageEventIDJSONCompatibility(t *testing.T) {
	legacy := BuildLogMessage{StepRunID: "step", Content: "same content"}
	encoded, err := sonic.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "eventId") {
		t.Fatalf("legacy message gained an eventId: %s", encoded)
	}
	legacy.EventID = "event-1"
	encoded, err = sonic.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"eventId":"event-1"`) {
		t.Fatalf("event id was not encoded in camelCase: %s", encoded)
	}
	var decoded BuildLogMessage
	if err := sonic.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != legacy {
		t.Fatalf("event identity changed during JSON round trip: %#v", decoded)
	}
}
