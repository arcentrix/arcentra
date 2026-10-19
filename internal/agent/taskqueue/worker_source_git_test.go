// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"bufio"
	"bytes"
	"context"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/stretchr/testify/require"
)

func TestSourceGitIsolatesAmbientExecution(t *testing.T) {
	repository, _, initial := sourceTestRepository(t, "isolation")
	marker := filepath.Join(t.TempDir(), "executed")
	evil := filepath.Join(t.TempDir(), "evil")
	require.NoError(t, os.WriteFile(evil, []byte("#!/bin/sh\nprintf x >> '"+marker+"'\nexit 1\n"), 0700))
	templates := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(templates, "hooks"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(templates, "hooks", "post-checkout"), []byte("#!/bin/sh\nexec '"+evil+"'\n"), 0700))
	home := t.TempDir()
	config := "[core]\n hooksPath = " + filepath.Dir(evil) + "\n fsmonitor = " + evil + "\n[init]\n templateDir = " + templates + "\n[credential]\n helper = !" + evil + "\n[filter \"evil\"]\n smudge = " + evil + "\n process = " + evil + "\n[uploadpack]\n packObjectsHook = " + evil + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(config), 0600))
	// Source repository config is separate from client config. A local server
	// must not execute its configured pack-objects hook either.
	sourceTestGit(t, repository, "config", "uploadpack.packObjectsHook", evil)
	sourceTestGit(t, repository, "config", "core.hooksPath", templates+"/hooks")
	fakeBin := t.TempDir()
	for _, name := range []string{"git", "git-upload-pack", "git-pack-objects", "sh", "env"} {
		require.NoError(t, os.WriteFile(filepath.Join(fakeBin, name), []byte("#!/bin/sh\nexec '"+evil+"'\n"), 0700))
	}
	for key, value := range map[string]string{"PATH": fakeBin, "HOME": home, "XDG_CONFIG_HOME": home, "GIT_CONFIG_GLOBAL": filepath.Join(home, ".gitconfig"), "GIT_CONFIG_SYSTEM": filepath.Join(home, ".gitconfig"), "GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.hooksPath", "GIT_CONFIG_VALUE_0": templates + "/hooks", "GIT_TEMPLATE_DIR": templates, "GIT_SSH_COMMAND": evil, "GIT_EXEC_PATH": fakeBin, "GIT_ASKPASS": evil, "SSH_ASKPASS": evil, "BASH_ENV": evil, "ENV": evil, "LD_PRELOAD": evil, "DYLD_INSERT_LIBRARIES": evil} {
		t.Setenv(key, value)
	}
	for _, pin := range []string{"", initial} {
		payload := sourceTestPayload(t, repository, "isolated")
		workspace := t.TempDir()
		receipt, err := handleSourceCloneAtCommit(context.Background(), payload.Source, workspace, pin)
		require.NoError(t, err)
		require.Equal(t, initial, receipt.ResolvedCommit)
		require.Equal(t, "isolation-initial", artifactTestRead(t, workspace, "source.txt"))
	}
	_, err := os.Stat(marker)
	require.True(t, os.IsNotExist(err), "ambient command executed")
}

func TestSourceGitRejectsUnsupportedTreeBeforeCheckout(t *testing.T) {
	for _, kind := range []string{"filter", "lfs-config", "lfs-pointer", "submodule-file", "gitlink"} {
		t.Run(kind, func(t *testing.T) {
			repository, _, _ := sourceTestRepository(t, kind)
			switch kind {
			case "filter":
				artifactTestWrite(t, repository, ".gitattributes", "*.txt filter=evil\n")
			case "lfs-config":
				artifactTestWrite(t, repository, ".lfsconfig", "[lfs]\nurl=https://example.invalid\n")
			case "lfs-pointer":
				artifactTestWrite(t, repository, "large.bin", "version https://git-lfs.github.com/spec/v1\noid sha256:"+strings.Repeat("a", 64)+"\nsize 1\n")
			case "submodule-file":
				artifactTestWrite(t, repository, ".gitmodules", "[submodule \"x\"]\npath=x\nurl=ext::evil\n")
			case "gitlink":
				sourceTestGit(t, repository, "update-index", "--add", "--cacheinfo", "160000,"+sourceTestGit(t, repository, "rev-parse", "HEAD")+",child")
			}
			if kind != "gitlink" {
				sourceTestGit(t, repository, "add", ".")
			}
			sourceTestGit(t, repository, "commit", "-m", "unsupported")
			workspace := t.TempDir()
			_, err := handleSourceClone(context.Background(), &taskpayload.SourcePayload{Type: "git", Repo: repository}, workspace)
			require.Error(t, err)
			_, err = os.Stat(filepath.Join(workspace, "source.txt"))
			require.True(t, os.IsNotExist(err), "unsupported tree materialized")
		})
	}
}

func TestSourceGitRejectsDirtyOrSymlinkWorkspace(t *testing.T) {
	repository, _, _ := sourceTestRepository(t, "workspace")
	for _, kind := range []string{"git-directory", "git-file", "file", "symlink", "parent-symlink"} {
		t.Run(kind, func(t *testing.T) {
			workspace := filepath.Join(t.TempDir(), "workspace")
			if kind == "parent-symlink" {
				require.NoError(t, os.Symlink(t.TempDir(), workspace))
				workspace = filepath.Join(workspace, "child")
			} else if kind == "symlink" {
				require.NoError(t, os.Symlink(t.TempDir(), workspace))
			} else {
				require.NoError(t, os.Mkdir(workspace, 0700))
				switch kind {
				case "git-directory":
					require.NoError(t, os.Mkdir(filepath.Join(workspace, ".git"), 0700))
				case "git-file":
					artifactTestWrite(t, workspace, ".git", "gitdir: "+repository+"/.git")
				default:
					artifactTestWrite(t, workspace, "existing", "keep")
				}
			}
			_, err := handleSourceClone(context.Background(), &taskpayload.SourcePayload{Type: "git", Repo: repository}, workspace)
			require.ErrorContains(t, err, "clean workspace")
		})
	}
}

func TestSourceGitTransportBoundary(t *testing.T) {
	for _, raw := range []string{"ssh://host/repo", "git@host:repo", "ext::sh -c evil", "file:///tmp/repo", "http://localhost/repo", "http://example.invalid/repo", "https://user:password@host/repo", "https://host/repo?token=secret", "relative/repo"} {
		_, err := sourceGitTransport(raw, false)
		require.Error(t, err, raw)
	}
	for _, raw := range []string{"https://example.invalid/repo", "http://127.0.0.1:1234/repo", "http://[::1]:1234/repo", "/tmp/repo"} {
		_, err := sourceGitTransport(raw, false)
		require.NoError(t, err, raw)
	}
	_, err := sourceGitTransport("http://127.0.0.1/repo", true)
	require.Error(t, err)
}

func TestSourceGitLoopbackHTTPAndRedirect(t *testing.T) {
	repository, _, initial := sourceTestRepository(t, "http")
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cmd := exec.CommandContext(r.Context(), sourceGitPath, "http-backend")
		cmd.Env = []string{"PATH=" + sourceGitPATH, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_PROJECT_ROOT=" + filepath.Dir(repository), "GIT_HTTP_EXPORT_ALL=1", "PATH_INFO=" + r.URL.Path, "REQUEST_METHOD=" + r.Method, "QUERY_STRING=" + r.URL.RawQuery, "CONTENT_TYPE=" + r.Header.Get("Content-Type"), "SERVER_PROTOCOL=HTTP/1.1"}
		cmd.Stdin = r.Body
		output, err := cmd.Output()
		if err != nil {
			http.Error(w, "fixture backend failed", 500)
			return
		}
		reader := bufio.NewReader(bytes.NewReader(output))
		headers, err := textproto.NewReader(reader).ReadMIMEHeader()
		if err != nil {
			http.Error(w, "fixture headers failed", 500)
			return
		}
		for key, values := range headers {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		_, _ = io.Copy(w, reader)
	})
	// Test-only CA injection proves source.auth still reaches the isolated
	// HTTPS helper; production uses the system trust store without overrides.
	var authenticated atomic.Bool
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "oauth2" || password != "synthetic-token" {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		authenticated.Store(true)
		backend.ServeHTTP(w, r)
	}))
	defer tlsServer.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsServer.Certificate().Raw}), 0600))
	source := &taskpayload.SourcePayload{Repo: tlsServer.URL + "/" + filepath.Base(repository), Auth: map[string]string{"token": "synthetic-token"}}
	secure, err := newSourceGit(source, t.TempDir())
	require.NoError(t, err)
	secure.options = append(secure.options, "-c", "http.sslCAInfo="+ca)
	_, err = secure.run(context.Background(), "clone", "--no-checkout", "--depth", "1", "--", source.Repo, ".")
	require.NoError(t, err)
	require.True(t, authenticated.Load())
	private := secure.private
	secure.close()
	_, err = os.Stat(private)
	require.True(t, os.IsNotExist(err))
	server := httptest.NewServer(backend)
	defer server.Close()
	payload := sourceTestPayload(t, server.URL+"/"+filepath.Base(repository), "http")
	receipt, err := handleSourceCloneAtCommit(context.Background(), payload.Source, t.TempDir(), initial)
	require.NoError(t, err)
	require.Equal(t, initial, receipt.ResolvedCommit)
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Store(true)
		http.Error(w, "must not reach", 500)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	defer redirect.Close()
	_, err = handleSourceClone(context.Background(), &taskpayload.SourcePayload{Type: "git", Repo: redirect.URL + "/repo"}, t.TempDir())
	require.Error(t, err)
	require.False(t, redirected.Load())
}

func TestSourceGitRejectsLocalSourceIndirection(t *testing.T) {
	for _, kind := range []string{"include", "partial-clone", "worktree-config", "promisor", "alternates", "http-alternates", "git-file", "git-symlink", "commondir", "head-symlink", "pack-symlink"} {
		t.Run(kind, func(t *testing.T) {
			repository, _, _ := sourceTestRepository(t, kind)
			switch kind {
			case "commondir":
				artifactTestWrite(t, repository, ".git/commondir", "/untrusted/common\n")
			case "head-symlink":
				require.NoError(t, os.Remove(filepath.Join(repository, ".git/HEAD")))
				require.NoError(t, os.Symlink("/untrusted/HEAD", filepath.Join(repository, ".git/HEAD")))
			case "pack-symlink":
				require.NoError(t, os.Remove(filepath.Join(repository, ".git/objects/pack")))
				require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(repository, ".git/objects/pack")))
			case "include":
				sourceTestGit(t, repository, "config", "include.path", "/untrusted/config")
			case "worktree-config":
				sourceTestGit(t, repository, "config", "extensions.worktreeConfig", "true")
			case "partial-clone":
				sourceTestGit(t, repository, "config", "extensions.partialClone", "origin")
			case "promisor":
				sourceTestGit(t, repository, "config", "remote.other.promisor", "true")
				sourceTestGit(t, repository, "config", "remote.other.url", "ext::untrusted-command")
			case "alternates", "http-alternates":
				artifactTestWrite(t, repository, ".git/objects/info/"+kind, "/untrusted/objects\n")
			case "git-file", "git-symlink":
				other := t.TempDir()
				if kind == "git-file" {
					artifactTestWrite(t, other, ".git", "gitdir: "+repository+"/.git")
				} else {
					require.NoError(t, os.Symlink(filepath.Join(repository, ".git"), filepath.Join(other, ".git")))
				}
				repository = other
			}
			_, err := handleSourceClone(context.Background(), &taskpayload.SourcePayload{Type: "git", Repo: repository}, t.TempDir())
			require.Error(t, err)
		})
	}
}

func TestSourceGitRejectsConfiguredFiltersBeforeHostExecution(t *testing.T) {
	for _, driver := range []string{"smudge", "process"} {
		t.Run(driver, func(t *testing.T) {
			repository, _, _ := sourceTestRepository(t, driver)
			artifactTestWrite(t, repository, ".gitattributes", "*.txt filter=external\n")
			sourceTestGit(t, repository, "add", ".gitattributes")
			sourceTestGit(t, repository, "commit", "-m", "filter")
			marker := filepath.Join(t.TempDir(), "executed")
			script := filepath.Join(t.TempDir(), "driver")
			require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf x > '"+marker+"'\nexit 1\n"), 0700))
			config := filepath.Join(t.TempDir(), "config")
			require.NoError(t, os.WriteFile(config, []byte("[filter \"external\"]\n "+driver+" = "+script+"\n required = true\n"), 0600))
			t.Setenv("GIT_CONFIG_GLOBAL", config)
			workspace := t.TempDir()
			_, err := handleSourceClone(context.Background(), &taskpayload.SourcePayload{Type: "git", Repo: repository}, workspace)
			require.ErrorContains(t, err, "filters unsupported")
			_, err = os.Stat(marker)
			require.True(t, os.IsNotExist(err))
			_, err = os.Stat(filepath.Join(workspace, "source.txt"))
			require.True(t, os.IsNotExist(err))
		})
	}
}

func TestSourceGitAllowsLFSDocumentation(t *testing.T) {
	repository, _, _ := sourceTestRepository(t, "lfs-documentation")
	text := "Documentation: version https://git-lfs.github.com/spec/v1\nThis is ordinary prose.\n"
	artifactTestWrite(t, repository, "README.txt", text)
	artifactTestWrite(t, repository, "example.txt", "before\nversion https://git-lfs.github.com/spec/v1\noid sha256:"+strings.Repeat("a", 64)+"\nsize 1\nafter\n")
	sourceTestGit(t, repository, "add", ".")
	sourceTestGit(t, repository, "commit", "-m", "documentation")
	workspace := t.TempDir()
	_, err := handleSourceClone(context.Background(), &taskpayload.SourcePayload{Type: "git", Repo: repository}, workspace)
	require.NoError(t, err)
	require.Equal(t, text, artifactTestRead(t, workspace, "README.txt"))
}
