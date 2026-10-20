//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
)

const terminalRerunDefinitionPath = ".arcentra.yaml"
const terminalRerunCommand = `set -eu
 test "$(cat source.txt)" = 'initial checkout'
 now=$(date -u +%s)
 printf 'terminal-rerun-at:%s\n' "$now"
 if [ "$now" -lt "$RERUN_CUTOFF" ]; then exit 23; fi
 printf 'terminal-rerun-command\n'
 mkdir -p proof
 printf 'terminal-rerun-source:' > proof/result.txt
 cat source.txt >> proof/result.txt`
const terminalRerunOutput = "terminal-rerun-source:initial checkout"

// Only the external UTC clock changes between executions. The caller freezes
// cutoff before the authenticated manual trigger and must reject an original
// completion at or after it. Advancing main later proves checkout uses the
// captured full SHA; it never rewrites the captured definition or variables.
type terminalRerunSource struct {
	URL, Commit, LaterCommit string
	Definition               string
	Cutoff                   time.Time
	Variables                map[string]string
	advance                  func(context.Context) error
}

func newTerminalRerunSource(t *testing.T, ctx context.Context, image string, cutoff time.Time) terminalRerunSource {
	t.Helper()
	if cutoff.UnixMilli()%1000 != 0 || !cutoff.After(time.Now()) {
		t.Fatal("terminal rerun cutoff must be a future fixed UTC second")
	}
	binary := "/usr/bin/git"
	if info, err := os.Stat(binary); err != nil || !info.Mode().IsRegular() {
		t.Fatal("fixed system Git is required for the disposable fixture")
	}
	root, work, home, templates := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	gitEnv := []string{"PATH=/usr/bin:/bin", "HOME=" + home, "XDG_CONFIG_HOME=" + home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_SYSTEM=" + os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=Checkout Test", "GIT_AUTHOR_EMAIL=checkout@example.invalid", "GIT_COMMITTER_NAME=Checkout Test", "GIT_COMMITTER_EMAIL=checkout@example.invalid"}
	gitArgs := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "init.templateDir=" + templates, "-c", "protocol.allow=never", "-c", "protocol.file.allow=always"}
	git := func(directory string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, append(append([]string(nil), gitArgs...), args...)...)
		cmd.Dir, cmd.Env = directory, gitEnv
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal("controlled disposable Git command failed")
		}
		return strings.TrimSpace(string(output))
	}
	// CGI builds request metadata itself; InheritEnv is empty and our explicit
	// PATH overrides its only Unix ambient default. No inherited GIT_* survives.
	handler := &cgi.Handler{Path: binary, Args: append(append([]string(nil), gitArgs...), "http-backend"), Env: append(append([]string(nil), gitEnv...), "GIT_PROJECT_ROOT="+root, "GIT_HTTP_EXPORT_ALL=1")}
	server := httptest.NewServer(http.HandlerFunc(handler.ServeHTTP))
	t.Cleanup(server.Close)
	url := server.URL + "/source.git"
	definition := &spec.Pipeline{Namespace: "terminal-rerun", Runtime: &spec.Runtime{Type: "containerd-offline-v1", Image: image, Resources: &spec.Resources{CpuLimit: "500m", MemoryLimit: "64Mi"}}, Jobs: []*spec.Job{{Name: "terminal-rerun", Source: &spec.Source{Type: "git", Repo: url, Branch: "main"}, Retry: &spec.Retry{Mode: "whole_job", MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{23}, Delay: "1s"}, Approval: &spec.Approval{Required: true, Type: "manual", Plugin: retryapproval.Plugin}, Steps: []*spec.Step{controllerWorkerShell("rerun-command", terminalRerunCommand), containerAcceptanceArtifactStep("rerun-output", "upload", map[string]any{"paths": []any{"proof/result.txt"}, "expireIn": "7d"})}}}}
	content, err := spec.MarshalProtoByFormat(definition, pipelinev1.SpecFormat_SPEC_FORMAT_YAML, terminalRerunDefinitionPath)
	if err != nil {
		t.Fatal(err)
	}
	git(work, "init", "--object-format=sha1", "-b", "main")
	for path, value := range map[string]string{terminalRerunDefinitionPath: content, "source.txt": "initial checkout"} {
		if err := os.WriteFile(filepath.Join(work, path), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git(work, "add", ".")
	git(work, "commit", "-m", "fixed terminal rerun fixture")
	initial := git(work, "rev-parse", "HEAD")
	git(work, "checkout", "-b", "later")
	if err := os.WriteFile(filepath.Join(work, "source.txt"), []byte("changed branch must not execute"), 0600); err != nil {
		t.Fatal(err)
	}
	git(work, "add", "source.txt")
	git(work, "commit", "-m", "later branch evidence")
	later := git(work, "rev-parse", "HEAD")
	git(work, "checkout", "main")
	bare := filepath.Join(root, "source.git")
	git(root, "clone", "--bare", work, bare)
	if len(initial) != 40 || len(later) != 40 || initial == later {
		t.Fatal("fixture must have two distinct full commits")
	}
	return terminalRerunSource{URL: url, Commit: initial, LaterCommit: later, Definition: content, Cutoff: cutoff, Variables: map[string]string{"RERUN_CUTOFF": strconv.FormatInt(cutoff.Unix(), 10)}, advance: func(ctx context.Context) error {
		cmd := exec.CommandContext(ctx, binary, append(append([]string(nil), gitArgs...), "--git-dir", bare, "update-ref", "refs/heads/main", later, initial)...)
		cmd.Env = gitEnv
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("advance exact disposable source ref: %w", err)
		}
		return nil
	}}
}

func TestContainerTerminalRerunFixedSourceFixture(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	source := newTerminalRerunSource(t, ctx, "docker.io/library/alpine@sha256:"+strings.Repeat("a", 64), time.Now().UTC().Add(time.Minute).Truncate(time.Second))
	parsed, err := spec.ParseContentToProto(source.Definition, pipelinev1.SpecFormat_SPEC_FORMAT_YAML)
	if err != nil || len(parsed.Jobs) != 1 || len(parsed.Jobs[0].Steps) != 2 || parsed.Jobs[0].Source.Repo != source.URL || parsed.Jobs[0].Source.Auth != nil || parsed.Jobs[0].Retry.MaxAttempts != 2 || parsed.Jobs[0].Retry.AllowedExitCodes[0] != 23 {
		t.Fatal("fixed fixture definition is inconsistent")
	}
	payload := sourceTestPayload(t, source.URL, "terminal-fixed-source")
	payload.Source.Branch = "main"
	workspace := filepath.Join(t.TempDir(), "original")
	receipt, err := handleSourceCloneAtCommit(ctx, payload.Source, workspace, source.Commit)
	if err != nil || receipt == nil || receipt.ResolvedCommit != source.Commit {
		t.Fatal("actual first checkout did not observe fixed source")
	}
	if err := source.advance(ctx); err != nil {
		t.Fatal(err)
	}
	replay := filepath.Join(t.TempDir(), "new-execution")
	receipt, err = handleSourceCloneAtCommit(ctx, payload.Source, replay, source.Commit)
	if err != nil || receipt == nil || receipt.ResolvedCommit != source.Commit {
		t.Fatal("new checkout followed changed branch instead of expected SHA")
	}
	raw, err := os.ReadFile(filepath.Join(replay, "source.txt"))
	if err != nil || string(raw) != "initial checkout" {
		t.Fatal("new checkout materialized the changed branch")
	}
	raw, err = os.ReadFile(filepath.Join(replay, terminalRerunDefinitionPath))
	if err != nil || string(raw) != source.Definition {
		t.Fatal("new checkout changed the captured definition")
	}
}

func TestContainerTerminalRerunSourceIgnoresInheritedGit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	parent := t.TempDir()
	config := filepath.Join(parent, "config")
	initial := []byte("[fixture]\n value = untouched\n")
	if err := os.WriteFile(config, initial, 0600); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"GIT_DIR": parent, "GIT_COMMON_DIR": parent, "GIT_WORK_TREE": parent,
		"GIT_OBJECT_DIRECTORY": filepath.Join(parent, "objects"), "GIT_INDEX_FILE": filepath.Join(parent, "index"),
		"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.hooksPath", "GIT_CONFIG_VALUE_0": parent,
		"GIT_EXEC_PATH": parent, "PATH": parent, "HOME": parent,
	} {
		t.Setenv(key, value)
	}
	source := newTerminalRerunSource(t, ctx, "docker.io/library/alpine@sha256:"+strings.Repeat("a", 64), time.Now().UTC().Add(time.Minute).Truncate(time.Second))
	if err := source.advance(ctx); err != nil {
		t.Fatal(err)
	}
	payload := sourceTestPayload(t, source.URL, "polluted-terminal-fixture")
	payload.Source.Branch = "main"
	receipt, err := handleSourceCloneAtCommit(ctx, payload.Source, filepath.Join(t.TempDir(), "checkout"), source.Commit)
	if err != nil || receipt == nil || receipt.ResolvedCommit != source.Commit {
		t.Fatal("controlled CGI checkout inherited ambient Git configuration")
	}
	after, err := os.ReadFile(config)
	if err != nil || string(after) != string(initial) {
		t.Fatal("fixture altered inherited parent config")
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 1 || entries[0].Name() != "config" {
		t.Fatal("fixture wrote inherited Git metadata/ref paths")
	}
}
