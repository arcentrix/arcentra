//go:build unix

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package terminalops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func existingEmptyOutbox(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".terminal-report-outbox")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	binding := sha256.Sum256([]byte("agent-1\x00controller:9090"))
	if err := os.WriteFile(filepath.Join(dir, "identity"), []byte(hex.EncodeToString(binding[:])+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func actualArgs(action, dir string, extras ...string) []string {
	args := []string{commandName, action, "--directory", dir, "--agent-id", "agent-1", "--controller", "controller:9090"}
	return append(args, extras...)
}

func runActual(t *testing.T, args []string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	handled, code := Run(context.Background(), args, &out, &errOut)
	if !handled {
		t.Fatal("operator command was not handled")
	}
	return code, out.String(), errOut.String()
}

func TestExistingJournalListsWithoutChangingBytesOrPermissions(t *testing.T) {
	dir := existingEmptyOutbox(t)
	before := snapshotDirectory(t, dir)
	code, out, errOut := runActual(t, actualArgs("list", dir))
	if code != 0 || errOut != "" || out != "{\"records\":[],\"total\":0,\"offset\":0}\n" {
		t.Fatalf("list existing journal: code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	after := snapshotDirectory(t, dir)
	if before != after {
		t.Fatalf("list changed existing journal: before=%q after=%q", before, after)
	}
}

func TestMissingDirectoryAndIdentityDoNotCreateJournal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secret-mistyped-directory")
	code, out, errOut := runActual(t, actualArgs("list", dir))
	if code != 1 || out != "" || strings.Contains(errOut, dir) {
		t.Fatalf("missing directory failure: code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("mistyped directory was created: %v", err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	code, _, _ = runActual(t, actualArgs("list", dir))
	if code != 1 {
		t.Fatal("directory without identity was accepted")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("identity-less directory changed: entries=%v err=%v", entries, err)
	}
}

func TestWrongIdentityDoesNotChangeJournal(t *testing.T) {
	dir := existingEmptyOutbox(t)
	before := snapshotDirectory(t, dir)
	args := actualArgs("list", dir)
	for i := range args {
		if args[i] == "agent-1" {
			args[i] = "wrong-agent"
		}
	}
	code, out, _ := runActual(t, args)
	if code != 1 || out != "" || snapshotDirectory(t, dir) != before {
		t.Fatal("wrong identity did not fail closed without journal mutation")
	}
}

func TestRunningAgentLockPreventsOfflineOperations(t *testing.T) {
	dir := existingEmptyOutbox(t)
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"list", "inspect", "mark-reviewed"} {
		args := actualArgs(action, dir)
		if action != "list" {
			args = append(args, "--kind", "job", "--run-id", "run-1")
		}
		if action == "mark-reviewed" {
			args = append(args, "--disposition", "investigated", "--confirm-reviewed")
		}
		code, out, _ := runActual(t, args)
		if code != 1 || out != "" {
			t.Fatalf("running Agent lock did not block %s", action)
		}
	}
}

func TestOfflineReadDoesNotRecoverPendingWrite(t *testing.T) {
	dir := existingEmptyOutbox(t)
	if err := os.WriteFile(filepath.Join(dir, ".pending"), []byte("preserve-partial-write"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotDirectory(t, dir)
	code, out, _ := runActual(t, actualArgs("list", dir))
	if code != 1 || out != "" || snapshotDirectory(t, dir) != before {
		t.Fatal("offline read modified an incomplete journal write")
	}
}

func snapshotDirectory(t *testing.T, dir string) string {
	t.Helper()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	out.WriteString(info.Mode().String())
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out.WriteString(entry.Name())
		out.WriteString(info.Mode().String())
		out.Write(data)
	}
	return out.String()
}
