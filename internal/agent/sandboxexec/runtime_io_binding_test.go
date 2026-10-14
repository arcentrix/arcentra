// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package sandboxexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type ioBoundFake struct {
	DurableBackend
	identity string
}

func (b *ioBoundFake) RuntimeIOIdentity() (string, error) { return b.identity, nil }

func TestDurableRuntimeIORootBindingRejectsChange(t *testing.T) {
	opts := controllerOptions(t)
	backend := &ioBoundFake{DurableBackend: newDurableFake(), identity: strings.Repeat("a", 64)}
	c, err := OpenController(context.Background(), backend, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(opts.JournalRoot, journalBindingFile)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	backend.identity = strings.Repeat("b", 64)
	if other, err := OpenController(context.Background(), backend, opts); err == nil || other != nil {
		t.Fatal("changed FIFO root silently adopted previous journal")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("failed IO root binding changed evidence")
	}
	backend.identity = strings.Repeat("a", 64)
	c, err = OpenController(context.Background(), backend, opts)
	if err != nil {
		t.Fatal(err)
	}
	backend.identity = strings.Repeat("c", 64)
	if _, err := c.Allocate(context.Background(), durableOptions()); err == nil {
		t.Fatal("live FIFO root replacement accepted")
	}
	_ = c.Close(context.Background())
}
