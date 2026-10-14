// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import "testing"

func TestOfflineIORootsCannotExposePipesToJobsOrJournals(t *testing.T) {
	for _, fifo := range []string{"", "relative", "/", "/private/work", "/private/work/fifo", "/private", "/private/journal", "/private/journal/fifo", "/private/work/../fifo"} {
		if err := validateOfflineIORoot(fifo, "/private/work", "/private/journal"); err == nil {
			t.Fatalf("unsafe FIFO root accepted: %q", fifo)
		}
	}
	if err := validateOfflineIORoot("/private/fifo", "/private/work", "/private/journal"); err != nil {
		t.Fatal(err)
	}
	if err := validateOfflineIORoot("/private/work-fifo", "/private/work", "/private/journal"); err != nil {
		t.Fatal("lexical-prefix sibling incorrectly rejected", err)
	}
}
