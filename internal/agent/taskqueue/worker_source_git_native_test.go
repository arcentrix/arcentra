// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/stretchr/testify/require"
)

func TestSourceGitNativeTransportBoundary(t *testing.T) {
	for _, raw := range []string{"git://git:9418/acceptance.git", "git://example.invalid/repo.git", "git://127.0.0.1:1234/repo", "git://[::1]:1234/repo"} {
		protocol, err := sourceGitTransport(raw, false)
		require.NoError(t, err, raw)
		require.Equal(t, "git", protocol)
	}
	for _, raw := range []string{"ext::evil", "git+ssh://host/repo", "git::evil", "git:host/repo", "git:///repo", "git://host", "git://host/", "git://host:/repo", "git://host:0/repo", "git://host:65536/repo", "git://host:bad/repo", "git://user@host/repo", "git://user:secret@host/repo", "git://host/repo?", "git://host/repo#", "git://host/repo?q=x", "git://host/repo#x", "git://host/repo%20x", "git://host/a/../repo", "git://host//repo", "git://::1/repo", "git://-host/repo", "git://host./repo", "git://host/repo\\x"} {
		_, err := sourceGitTransport(raw, false)
		require.Error(t, err, raw)
	}
	_, err := sourceGitTransport("git://host/repo", true)
	require.Error(t, err)
	for _, auth := range []map[string]string{{"username": "user"}, {"password": "secret"}, {"token": "secret"}, {"unsupported": "secret"}, {"token": ""}} {
		_, err := newSourceGit(&taskpayload.SourcePayload{Type: "git", Repo: "git://host/repo", Auth: auth}, t.TempDir())
		require.Error(t, err)
	}
}

// The fixture daemon is loopback-only, ephemeral and upload-pack-only. Client
// config/environment traps must not redirect native transport to an executable.
func TestSourceGitNativeDaemonCloneAndExactCommitIsolateHelpers(t *testing.T) {
	repository, first, initial := sourceTestRepository(t, "native")
	bare := filepath.Join(t.TempDir(), "acceptance.git")
	sourceTestGit(t, repository, "clone", "--bare", repository, bare)
	repository = bare
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, sourceGitPath, "-c", "core.hooksPath=/dev/null", "daemon", "--verbose", "--log-destination=stderr", "--listen=127.0.0.1", "--port="+strconv.Itoa(port), "--reuseaddr", "--export-all", "--strict-paths", "--base-path="+filepath.Dir(repository), "--disable=receive-pack", "--disable=upload-archive", "--enable=upload-pack", repository)
	configureSourceGitCancellation(cmd)
	cmd.Env = []string{"PATH=" + sourceGitPATH, "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null"}
	var daemonLog bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.Discard, &daemonLog
	require.NoError(t, cmd.Start())
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
			if t.Failed() {
				t.Logf("fixture daemon: %s", daemonLog.String())
			}
		case <-time.After(5 * time.Second):
			t.Error("native fixture daemon did not stop")
		}
	})
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(5 * time.Second)
	for {
		connection, dialErr := net.DialTimeout("tcp4", address, 50*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			break
		}
		select {
		case <-done:
			t.Fatal("native fixture daemon exited before ready")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("native fixture daemon not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	marker := filepath.Join(t.TempDir(), "executed")
	evil := filepath.Join(t.TempDir(), "evil")
	require.NoError(t, os.WriteFile(evil, []byte("#!/bin/sh\nprintf x >> '"+marker+"'\nexit 1\n"), 0700))
	fakeBin := t.TempDir()
	for _, name := range []string{"git", "git-remote-git", "git-remote-ext", "git-upload-pack", "git-pack-objects", "sh"} {
		require.NoError(t, os.WriteFile(filepath.Join(fakeBin, name), []byte("#!/bin/sh\nexec '"+evil+"'\n"), 0700))
	}
	home := t.TempDir()
	config := filepath.Join(home, ".gitconfig")
	require.NoError(t, os.WriteFile(config, []byte("[url \"ext::"+evil+"\"]\n insteadOf = git://\n[core]\n gitProxy = "+evil+"\n[protocol \"ext\"]\n allow = always\n"), 0600))
	for k, v := range map[string]string{"PATH": fakeBin, "HOME": home, "XDG_CONFIG_HOME": home, "GIT_CONFIG_GLOBAL": config, "GIT_CONFIG_SYSTEM": config, "GIT_CONFIG_COUNT": "2", "GIT_CONFIG_KEY_0": "url.ext::" + evil + ".insteadOf", "GIT_CONFIG_VALUE_0": "git://", "GIT_CONFIG_KEY_1": "core.gitProxy", "GIT_CONFIG_VALUE_1": evil, "GIT_EXEC_PATH": fakeBin, "GIT_PROXY_COMMAND": evil, "GIT_SSH_COMMAND": evil, "GIT_ASKPASS": evil} {
		t.Setenv(k, v)
	}
	raw := fmt.Sprintf("git://%s/%s", address, filepath.Base(repository))
	for _, pin := range []string{"", first} {
		workspace := t.TempDir()
		checkoutCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		payload := sourceTestPayload(t, raw, "native-daemon")
		receipt, checkoutErr := handleSourceCloneAtCommit(checkoutCtx, payload.Source, workspace, pin)
		stop()
		require.NoError(t, checkoutErr)
		require.NotNil(t, receipt)
		expected, content := initial, "native-initial"
		if pin != "" {
			expected, content = first, "native-first"
		}
		require.Equal(t, expected, receipt.ResolvedCommit)
		require.Equal(t, content, artifactTestRead(t, workspace, "source.txt"))
	}
	_, err = os.Stat(marker)
	require.True(t, os.IsNotExist(err), "inherited native transport helper executed")
}
