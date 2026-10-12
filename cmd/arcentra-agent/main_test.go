// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTerminalOperatorProcess(t *testing.T) {
	if os.Getenv("ARCENTRA_OPERATOR_TEST_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{os.Args[0]}, os.Args[i+1:]...)
			main()
			t.Fatal("operator main returned instead of exiting")
		}
	}
	t.Fatal("helper command arguments missing")
}

func invokeAgentMain(t *testing.T, args []string) (int, string, string) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	childArgs := append([]string{"-test.run=^TestTerminalOperatorProcess$", "--"}, args...)
	command := exec.CommandContext(ctx, binary, childArgs...)
	command.Env = append(os.Environ(), "ARCENTRA_OPERATOR_TEST_PROCESS=1")
	// 默认配置不存在，因此错误进入 Bootstrap 会失败而不是被有效配置隐藏
	command.Dir = t.TempDir()
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	if ctx.Err() != nil {
		t.Fatalf("operator command did not exit promptly: %v", ctx.Err())
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), stdout.String(), stderr.String()
	}
	if err != nil {
		t.Fatal(err)
	}
	return 0, stdout.String(), stderr.String()
}

func TestTerminalOperatorDispatchPrecedesBootstrap(t *testing.T) {
	code, stdout, stderr := invokeAgentMain(t, []string{"terminal-quarantine", "--help"})
	if code != 0 || !strings.Contains(stdout, "Usage:") || stderr != "" {
		t.Fatalf("help entered bootstrap: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	code, stdout, stderr = invokeAgentMain(t, []string{
		"terminal-quarantine", "list", "--directory", dir, "--agent-id", "agent-1", "--controller", "unused.invalid:9090",
	})
	if code != 1 || stdout != "" || !strings.Contains(stderr, "could not open existing outbox") || strings.Contains(stderr, dir) {
		t.Fatalf("invalid offline path entered bootstrap or leaked: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("operator command created missing journal: %v", err)
	}
}

func TestTerminalOperatorListsExistingJournalWithoutConfiguration(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("terminal journal locking is supported only on Unix")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	binding := sha256.Sum256([]byte("agent-1\x00unused.invalid:9090"))
	if err := os.WriteFile(filepath.Join(dir, "identity"), []byte(hex.EncodeToString(binding[:])+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := invokeAgentMain(t, []string{
		"terminal-quarantine", "list", "--directory", dir, "--agent-id", "agent-1", "--controller", "unused.invalid:9090",
	})
	if code != 0 || stdout != "{\"records\":[],\"total\":0,\"offset\":0}\n" || stderr != "" {
		t.Fatalf("offline list failed: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestUnknownAgentCommandDoesNotBootstrap(t *testing.T) {
	code, stdout, stderr := invokeAgentMain(t, []string{"terminal-quarantin"})
	if code != 2 || stdout != "" || !strings.Contains(stderr, "unexpected Agent arguments") {
		t.Fatalf("unknown command entered bootstrap: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}
