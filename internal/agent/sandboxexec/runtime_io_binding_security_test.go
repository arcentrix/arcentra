// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package sandboxexec

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/pkg/sandbox"
	"github.com/bytedance/sonic"
)

func TestDurableRuntimeIOBindingPreservesLegacyCanonicalEncoding(t *testing.T) {
	opts := controllerOptions(t)
	backend := &ioBoundFake{DurableBackend: newDurableFake()}
	// Keep the previous on-disk field layout explicit: adding the optional IO
	// identity must not change canonical bytes for existing empty bindings.
	legacy := struct {
		Version       int            `json:"version"`
		ControllerID  string         `json:"controllerID"`
		AgentID       string         `json:"agentID"`
		WorkspaceRoot string         `json:"workspaceRoot"`
		Namespace     string         `json:"namespace"`
		Record        *journalRecord `json:"record,omitempty"`
		Checksum      string         `json:"checksum"`
	}{Version: journalVersion, ControllerID: opts.ControllerID, AgentID: opts.AgentID, WorkspaceRoot: opts.WorkspaceRoot, Namespace: backend.Namespace()}
	body, err := sonic.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	legacy.Checksum = hex.EncodeToString(sum[:])
	data, err := sonic.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(opts.JournalRoot, journalBindingFile)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := OpenController(context.Background(), backend, opts)
	if err != nil {
		t.Fatal("legacy empty IO binding no longer opens", err)
	}
	encoded, err := encodeJournal(c.document(nil))
	if err != nil || !bytes.Equal(encoded, data) {
		t.Fatal("empty optional IO identity changed legacy canonical encoding", err)
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	backend.identity = strings.Repeat("a", 64)
	if changed, err := OpenController(context.Background(), backend, opts); err == nil || changed != nil {
		t.Fatal("legacy unbound journal silently adopted an explicit FIFO root")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, data) {
		t.Fatal("rejected binding migration changed original evidence", err)
	}
}

func TestDurableRuntimeIOBindingChangeDuringRecoveryPreventsDeletion(t *testing.T) {
	ctx := context.Background()
	opts := controllerOptions(t)
	runtime := newDurableFake()
	backend := &ioBoundFake{DurableBackend: runtime, identity: strings.Repeat("a", 64)}
	c, err := OpenController(ctx, backend, opts)
	if err != nil {
		t.Fatal(err)
	}
	s := allocateTestSession(t, c, durableOptions())
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	simulateCrash(t, c)
	recordPath := filepath.Join(opts.JournalRoot, s.recordID+".json")
	before, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	// Initial capture and journal checks see the old root. Change the identity
	// while ownership is inspected, before recovery could remove resources.
	runtime.inspect = func(context.Context, sandbox.ResourceRef) error {
		backend.identity = strings.Repeat("b", 64)
		return nil
	}
	if reopened, err := OpenController(ctx, backend, opts); err == nil || reopened != nil {
		t.Fatal("root replacement during startup recovery was accepted")
	}
	if countEvent(runtime.events, "remove-owned") != 0 {
		t.Fatal("changed IO root allowed destructive runtime cleanup")
	}
	if _, err := os.Stat(s.Workspace()); err != nil {
		t.Fatal("changed IO root allowed workspace deletion", err)
	}
	after, err := os.ReadFile(recordPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("changed IO root rewrote durable recovery evidence", err)
	}
	// Restoring the original root permits normal exact-owned recovery.
	runtime.inspect = nil
	backend.identity = strings.Repeat("a", 64)
	recovered, err := OpenController(ctx, backend, opts)
	if err != nil {
		t.Fatal(err)
	}
	if countEvent(runtime.events, "remove-owned") != 1 {
		t.Fatal("original root did not permit retryable owned cleanup")
	}
	if err := recovered.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
