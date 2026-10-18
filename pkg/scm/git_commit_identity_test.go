// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package scm

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Subprocess isolation guarantees this regression never mutates the test runner's
// Git environment or configuration, including inherited worktree/index variables.
func TestCommitWorksWithoutOperatorGitIdentity(t *testing.T) {
	if root := os.Getenv("ARCENTRA_COMMIT_IDENTITY_FIXTURE"); root != "" {
		if err := Commit(GitCommitRequest{Workdir: root, Message: "synthetic definition edit", Author: os.Getenv("ARCENTRA_COMMIT_AUTHOR_FIXTURE")}); err != nil {
			t.Fatal(err)
		}
		if err := Push(GitPushRequest{Workdir: root, Remote: os.Getenv("ARCENTRA_COMMIT_REMOTE_FIXTURE"), Branch: "main"}); err != nil {
			t.Fatal(err)
		}
		return
	}
	for _, author := range []string{"fixture-actor", ""} {
		t.Run("author-"+author, func(t *testing.T) {
			root := t.TempDir()
			env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
			run := func(args ...string) string {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "git", args...)
				cmd.Dir = root
				cmd.Env = env
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("isolated fixture Git: %v: %s", err, out)
				}
				return strings.TrimSpace(string(out))
			}
			remote := filepath.Join(t.TempDir(), "remote.git")
			run("init", "--bare", "--initial-branch=main", remote)
			run("init", "--initial-branch=main")
			if err := os.WriteFile(filepath.Join(root, "pipeline.json"), []byte("{}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			run("add", "pipeline.json")
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCommitWorksWithoutOperatorGitIdentity$")
			cmd.Env = append(env, "ARCENTRA_COMMIT_IDENTITY_FIXTURE="+root, "ARCENTRA_COMMIT_AUTHOR_FIXTURE="+author, "ARCENTRA_COMMIT_REMOTE_FIXTURE="+remote)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("production Commit with empty HOME: %v: %s", err, out)
			}
			expectedAuthor := author
			if expectedAuthor == "" {
				expectedAuthor = "Arcentra"
			}
			if got := run("show", "-s", "--format=%an|%ae|%cn|%ce", "HEAD"); got != expectedAuthor+"|noreply@arcentra.local|Arcentra|noreply@arcentra.local" {
				t.Fatalf("unexpected commit identity: %q", got)
			}
			if got := run("--git-dir="+remote, "show", "-s", "--format=%an|%ae|%cn|%ce", "refs/heads/main"); got != expectedAuthor+"|noreply@arcentra.local|Arcentra|noreply@arcentra.local" {
				t.Fatalf("pushed commit identity changed: %q", got)
			}
			if got := run("--git-dir="+remote, "rev-parse", "refs/heads/main"); got != run("rev-parse", "HEAD") {
				t.Fatal("pushed commit does not match local commit")
			}
			if got := run("config", "--local", "--list"); strings.Contains(got, "user.name=") || strings.Contains(got, "user.email=") {
				t.Fatal("Commit persisted identity into repository configuration")
			}
			if _, err := os.Stat(filepath.Join(root, ".gitconfig")); !os.IsNotExist(err) {
				t.Fatal("Commit wrote operator HOME configuration")
			}
		})
	}
}
