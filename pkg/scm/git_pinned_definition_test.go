// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package scm

import (
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const definitionFixtureURL = "https://gitlab.invalid/team/nested/example.git"

func fixtureDefinitionGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git failed: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func fixtureDefinitionRepository(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	fixtureDefinitionGit(t, dir, "init", "--initial-branch=main")
	writeDefinitionFixture(t, dir, ".arcentra/pipeline.yml", "name: first\n")
	fixtureDefinitionGit(t, dir, "add", ".")
	fixtureDefinitionGit(t, dir, "commit", "-m", "fixture first")
	return dir, fixtureDefinitionGit(t, dir, "rev-parse", "HEAD")
}

func writeDefinitionFixture(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The only transport override is private to this test. Public production entry points always require HTTPS.
func fixtureDefinitionTransport(t *testing.T, repository string, workdir *string) definitionGitExecutor {
	t.Helper()
	return func(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
		if workdir != nil {
			*workdir = dir
		}
		if len(args) > 0 && args[0] == "fetch" {
			args = append([]string(nil), args...)
			if args[len(args)-2] != definitionFixtureURL {
				t.Fatal("unexpected production repository target")
			}
			args[len(args)-2] = repository
			args = append([]string{"-c", "protocol.file.allow=always"}, args...)
		}
		return executeDefinitionGit(ctx, dir, env, args...)
	}
}

func TestPinnedDefinitionSurvivesBranchAdvance(t *testing.T) {
	repository, firstSHA := fixtureDefinitionRepository(t)
	writeDefinitionFixture(t, repository, ".arcentra/pipeline.yml", "name: second\n")
	fixtureDefinitionGit(t, repository, "add", ".")
	fixtureDefinitionGit(t, repository, "commit", "-m", "fixture second")
	secondSHA := fixtureDefinitionGit(t, repository, "rev-parse", "HEAD")
	var temporary string
	runner := fixtureDefinitionTransport(t, repository, &temporary)
	req := GitDefinitionRequest{RepoURL: definitionFixtureURL, Branch: "main", CommitSHA: firstSHA, FilePath: ".arcentra/pipeline.yml"}
	content, actual, err := readHTTPSDefinitionContext(context.Background(), req, runner)
	if err != nil || content != "name: first\n" || actual != firstSHA {
		t.Fatalf("immutable revision was not restored: %q %q %v", content, actual, err)
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatalf("private repository was retained: %v", err)
	}
	req.CommitSHA = ""
	content, actual, err = readHTTPSDefinitionContext(context.Background(), req, runner)
	if err != nil || content != "name: second\n" || actual != secondSHA {
		t.Fatalf("configured branch did not return its actual revision: %q %q %v", content, actual, err)
	}
	if fixtureDefinitionGit(t, repository, "rev-parse", "HEAD") != secondSHA {
		t.Fatal("source repository was modified")
	}
}

func TestPinnedDefinitionRejectsUnsafeInputsBeforeGit(t *testing.T) {
	base := GitDefinitionRequest{RepoURL: definitionFixtureURL, Branch: "main", FilePath: ".arcentra/pipeline.yml"}
	cases := map[string]func(*GitDefinitionRequest){
		"http":               func(r *GitDefinitionRequest) { r.RepoURL = "http://gitlab.invalid/team/example.git" },
		"ssh":                func(r *GitDefinitionRequest) { r.RepoURL = "ssh://git@gitlab.invalid/team/example.git" },
		"file":               func(r *GitDefinitionRequest) { r.RepoURL = "file:///tmp/team/example.git" },
		"userinfo":           func(r *GitDefinitionRequest) { r.RepoURL = "https://oauth2:secret@gitlab.invalid/team/example.git" },
		"query":              func(r *GitDefinitionRequest) { r.RepoURL += "?token=secret" },
		"empty-query":        func(r *GitDefinitionRequest) { r.RepoURL += "?" },
		"fragment":           func(r *GitDefinitionRequest) { r.RepoURL += "#secret" },
		"encoded-path":       func(r *GitDefinitionRequest) { r.RepoURL = "https://gitlab.invalid/team/%2e%2e/example.git" },
		"dot-path":           func(r *GitDefinitionRequest) { r.RepoURL = "https://gitlab.invalid/team/../example.git" },
		"scp":                func(r *GitDefinitionRequest) { r.RepoURL = "git@gitlab.invalid:team/example.git" },
		"abbreviated-sha":    func(r *GitDefinitionRequest) { r.CommitSHA = "deadbeef" },
		"ref-instead-of-sha": func(r *GitDefinitionRequest) { r.CommitSHA = "refs/heads/main" },
		"sha-expression":     func(r *GitDefinitionRequest) { r.CommitSHA = strings.Repeat("a", 40) + "^{commit}" },
		"uppercase-sha":      func(r *GitDefinitionRequest) { r.CommitSHA = strings.Repeat("A", 40) },
		"empty-branch":       func(r *GitDefinitionRequest) { r.Branch = "" },
		"option-branch":      func(r *GitDefinitionRequest) { r.Branch = "--upload-pack=evil" },
		"empty-path":         func(r *GitDefinitionRequest) { r.FilePath = "" },
		"absolute-path":      func(r *GitDefinitionRequest) { r.FilePath = "/etc/passwd" },
		"path-escape":        func(r *GitDefinitionRequest) { r.FilePath = "dir/../../outside" },
		"backslash-path":     func(r *GitDefinitionRequest) { r.FilePath = `dir\outside` },
		"git-metadata":       func(r *GitDefinitionRequest) { r.FilePath = ".git/config" },
		"nested-metadata":    func(r *GitDefinitionRequest) { r.FilePath = "dir/.Git/config" },
		"ssh-credential":     func(r *GitDefinitionRequest) { r.Auth.SSHKey = "synthetic key" },
		"credential-newline": func(r *GitDefinitionRequest) { r.Auth.Token = "synthetic\nsecret" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			req := base
			mutate(&req)
			called := false
			_, _, err := readHTTPSDefinitionContext(context.Background(), req,
				func(context.Context, string, []string, ...string) ([]byte, error) { called = true; return nil, nil })
			if err == nil || called {
				t.Fatalf("unsafe input reached Git: %v, called=%v", err, called)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("input secret leaked in error")
			}
		})
	}
}

func TestPinnedDefinitionRequiresRegularBoundedBlobAndTreeParents(t *testing.T) {
	repository, firstSHA := fixtureDefinitionRepository(t)
	writeDefinitionFixture(t, repository, "large.yml", strings.Repeat("x", definitionMaxBytes+1))
	writeDefinitionFixture(t, repository, "exact.yml", strings.Repeat("x", definitionMaxBytes))
	writeDefinitionFixture(t, repository, "literal[1].yml", "literal\n")
	if err := os.Symlink(".arcentra/pipeline.yml", filepath.Join(repository, "link.yml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".arcentra", filepath.Join(repository, "linked-parent")); err != nil {
		t.Fatal(err)
	}
	fixtureDefinitionGit(t, repository, "add", ".")
	fixtureDefinitionGit(t, repository, "update-index", "--add", "--cacheinfo", "160000,"+firstSHA+",module")
	fixtureDefinitionGit(t, repository, "commit", "-m", "fixture unsafe modes and limits")
	sha := fixtureDefinitionGit(t, repository, "rev-parse", "HEAD")
	for _, path := range []string{"large.yml", "link.yml", "linked-parent/pipeline.yml", "module", "module/pipeline.yml", ".arcentra", "missing.yml"} {
		t.Run(path, func(t *testing.T) {
			_, _, err := readHTTPSDefinitionContext(context.Background(), GitDefinitionRequest{
				RepoURL: definitionFixtureURL, CommitSHA: sha, FilePath: path,
			}, fixtureDefinitionTransport(t, repository, nil))
			if err == nil {
				t.Fatalf("unsafe definition accepted: %s", path)
			}
		})
	}
	for _, path := range []string{"exact.yml", "literal[1].yml"} {
		content, actual, err := readHTTPSDefinitionContext(context.Background(), GitDefinitionRequest{
			RepoURL: definitionFixtureURL, CommitSHA: sha, FilePath: path,
		}, fixtureDefinitionTransport(t, repository, nil))
		if err != nil || actual != sha || content == "" {
			t.Fatalf("valid bounded or literal definition rejected: %s %v", path, err)
		}
	}
}

func TestPinnedDefinitionIgnoresInheritedGitConfiguration(t *testing.T) {
	repository, sha := fixtureDefinitionRepository(t)
	external, externalSHA := fixtureDefinitionRepository(t)
	beforeTree := definitionFixtureSnapshot(t, external)
	beforeConfig, err := os.ReadFile(filepath.Join(external, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "hook-executed")
	hostileConfig := filepath.Join(t.TempDir(), "hostile.gitconfig")
	if err := os.WriteFile(hostileConfig, []byte("[credential]\n helper = !touch "+marker+"\n[url \"file:///unapproved/\"]\n insteadOf = https://\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"GIT_DIR": filepath.Join(external, ".git"), "GIT_WORK_TREE": external,
		"GIT_COMMON_DIR": filepath.Join(external, ".git"), "GIT_INDEX_FILE": filepath.Join(external, ".git", "index"),
		"GIT_OBJECT_DIRECTORY": filepath.Join(external, ".git", "objects"), "GIT_CONFIG_GLOBAL": hostileConfig,
		"GIT_CONFIG_SYSTEM": hostileConfig, "GIT_CONFIG": hostileConfig, "GIT_CONFIG_NOSYSTEM": "0",
		"GIT_CONFIG_COUNT": "2", "GIT_CONFIG_KEY_0": "http.sslVerify", "GIT_CONFIG_VALUE_0": "false",
		"GIT_CONFIG_KEY_1": "core.hooksPath", "GIT_CONFIG_VALUE_1": external,
		"GIT_CONFIG_PARAMETERS": "'http.followRedirects=true'", "GIT_ASKPASS": "touch " + marker,
		"GIT_SSH_COMMAND": "touch " + marker, "GIT_EXEC_PATH": external,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": filepath.Join(external, ".git", "objects"), "GIT_SSL_NO_VERIFY": "1",
	} {
		t.Setenv(key, value)
	}
	runner := fixtureDefinitionTransport(t, repository, nil)
	checked := false
	content, actual, err := readHTTPSDefinitionContext(context.Background(), GitDefinitionRequest{
		RepoURL: definitionFixtureURL, CommitSHA: sha, FilePath: ".arcentra/pipeline.yml", Auth: GitAuth{Token: "synthetic-token"},
	}, func(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
		for _, entry := range env {
			for _, forbidden := range []string{"GIT_DIR=", "GIT_WORK_TREE=", "GIT_CONFIG_PARAMETERS=", "GIT_CONFIG_COUNT=", "GIT_SSL_NO_VERIFY="} {
				if strings.HasPrefix(entry, forbidden) {
					t.Fatalf("hostile override survived: %s", forbidden)
				}
			}
		}
		checked = true
		return runner(ctx, dir, env, args...)
	})
	if err != nil || actual != sha || content != "name: first\n" || !checked {
		t.Fatalf("isolated load failed: %q %q %v", content, actual, err)
	}
	afterConfig, err := os.ReadFile(filepath.Join(external, ".git", "config"))
	if err != nil || string(afterConfig) != string(beforeConfig) || fixtureDefinitionGit(t, external, "rev-parse", "HEAD") != externalSHA {
		t.Fatal("inherited external repository was modified")
	}
	if !reflect.DeepEqual(beforeTree, definitionFixtureSnapshot(t, external)) {
		t.Fatal("inherited external repository files, refs, index or worktree were modified")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("inherited hook or credential helper was executed")
	}
}

func definitionFixtureSnapshot(t *testing.T, dir string) map[string][32]byte {
	t.Helper()
	snapshot := map[string][32]byte{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		snapshot[strings.TrimPrefix(path, dir)] = sha256.Sum256(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestPinnedDefinitionCancellationCleanupAndSanitizedFailure(t *testing.T) {
	req := GitDefinitionRequest{RepoURL: definitionFixtureURL, CommitSHA: strings.Repeat("a", 40), FilePath: "pipeline.yml"}
	t.Run("already-cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err := ReadHTTPSDefinitionContext(ctx, req)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	})
	t.Run("during-operation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var dir string
		_, _, err := readHTTPSDefinitionContext(ctx, req, func(_ context.Context, workdir string, _ []string, _ ...string) ([]byte, error) {
			dir = workdir
			cancel()
			return nil, errors.New("raw secret remote failure")
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("cancelled workspace was retained")
		}
	})
	t.Run("failure", func(t *testing.T) {
		var dir string
		_, _, err := readHTTPSDefinitionContext(context.Background(), req,
			func(_ context.Context, workdir string, _ []string, _ ...string) ([]byte, error) {
				dir = workdir
				return nil, errors.New("raw secret https://user:password@example.invalid")
			})
		if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "password") || strings.Contains(err.Error(), "https") {
			t.Fatalf("raw failure was exposed: %v", err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("failed workspace was retained")
		}
	})
}

func TestDefinitionOutputIsBounded(t *testing.T) {
	out := &definitionOutput{limit: 10}
	n, err := out.Write([]byte(strings.Repeat("x", 100)))
	if err != nil || n != 100 || out.Len() != 10 || !out.exceeded {
		t.Fatalf("output was not safely bounded: %d %d %v", n, out.Len(), err)
	}
	_, _ = out.Write([]byte("more"))
	if out.Len() != 10 {
		t.Fatal("overflow grew output buffer")
	}
}

func TestPinnedDefinitionRejectsSubstitutedFetchedCommit(t *testing.T) {
	repository, sha := fixtureDefinitionRepository(t)
	runner := fixtureDefinitionTransport(t, repository, nil)
	_, _, err := readHTTPSDefinitionContext(context.Background(), GitDefinitionRequest{
		RepoURL: definitionFixtureURL, CommitSHA: sha, FilePath: ".arcentra/pipeline.yml",
	}, func(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
		if args[0] == "rev-parse" && args[len(args)-1] == "FETCH_HEAD^{commit}" {
			return []byte(strings.Repeat("b", 40)), nil
		}
		return runner(ctx, dir, env, args...)
	})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("substituted revision was accepted: %v", err)
	}
}

func TestDefinitionGitExecutionBoundsOutputAndCancelsProcesses(t *testing.T) {
	repository, _ := fixtureDefinitionRepository(t)
	writeDefinitionFixture(t, repository, "large.yml", strings.Repeat("x", definitionMaxBytes+1))
	blob := fixtureDefinitionGit(t, repository, "hash-object", "-w", "large.yml")
	env, err := definitionGitEnvironment(repository, GitAuth{})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := executeDefinitionGit(context.Background(), repository, env, "cat-file", "blob", blob); err == nil || out != nil {
		t.Fatal("actual subprocess output escaped the memory bound")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = executeDefinitionGit(ctx, repository, env, "-c", "alias.fixturewait=!sleep 30", "fixturewait")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 3*time.Second {
		t.Fatalf("subprocess cancellation failed: %v elapsed=%s", err, time.Since(started))
	}
}

func TestPinnedDefinitionDoesNotFallBackWhenCommitUnavailable(t *testing.T) {
	repository, _ := fixtureDefinitionRepository(t)
	runner := fixtureDefinitionTransport(t, repository, nil)
	missing := strings.Repeat("f", 40)
	fetches := 0
	content, actual, err := readHTTPSDefinitionContext(context.Background(), GitDefinitionRequest{
		RepoURL: definitionFixtureURL, Branch: "main", CommitSHA: missing, FilePath: ".arcentra/pipeline.yml",
	}, func(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
		if args[0] == "fetch" {
			fetches++
			if args[len(args)-1] != missing {
				t.Fatal("pinned load substituted a branch ref")
			}
		}
		return runner(ctx, dir, env, args...)
	})
	if err == nil || content != "" || actual != "" || fetches != 1 {
		t.Fatalf("unavailable pinned commit did not fail closed: actual=%q fetches=%d err=%v", actual, fetches, err)
	}
}

func TestPinnedDefinitionRejectsSubstitutedHEAD(t *testing.T) {
	repository, sha := fixtureDefinitionRepository(t)
	runner := fixtureDefinitionTransport(t, repository, nil)
	_, _, err := readHTTPSDefinitionContext(context.Background(), GitDefinitionRequest{
		RepoURL: definitionFixtureURL, CommitSHA: sha, FilePath: ".arcentra/pipeline.yml",
	}, func(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
		if args[0] == "rev-parse" && args[len(args)-1] == "HEAD^{commit}" {
			return []byte(strings.Repeat("b", 40)), nil
		}
		return runner(ctx, dir, env, args...)
	})
	if err == nil || !strings.Contains(err.Error(), "HEAD verification failed") {
		t.Fatalf("substituted HEAD was accepted: %v", err)
	}
}
