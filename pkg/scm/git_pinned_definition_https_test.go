// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package scm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/pem"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func fixtureDefinitionTLS(t *testing.T, handler http.Handler) (*httptest.Server, string) {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)
	ca := filepath.Join(t.TempDir(), "fixture-ca.pem")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(ca, certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	return server, ca
}

func fixtureTrustedDefinitionTransport(ca string) definitionGitExecutor {
	return func(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
		// Tests add only this synthetic server's CA. TLS verification remains enabled.
		args = append([]string{"-c", "http.sslCAInfo=" + ca}, args...)
		return executeDefinitionGit(ctx, dir, env, args...)
	}
}

func fixtureGitHTTPBackend(root string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cmd := exec.CommandContext(r.Context(), "git", "http-backend")
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + root, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull,
			"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "PATH_INFO=" + r.URL.Path,
			"REQUEST_METHOD=" + r.Method, "QUERY_STRING=" + r.URL.RawQuery,
			"CONTENT_TYPE=" + r.Header.Get("Content-Type"), "REMOTE_USER=fixture", "SERVER_PROTOCOL=HTTP/1.1",
		}
		cmd.Stdin = r.Body
		output, err := cmd.Output()
		if err != nil {
			http.Error(w, "fixture backend failed", http.StatusInternalServerError)
			return
		}
		reader := bufio.NewReader(bytes.NewReader(output))
		headers, err := textproto.NewReader(reader).ReadMIMEHeader()
		if err != nil {
			http.Error(w, "fixture headers failed", http.StatusInternalServerError)
			return
		}
		status := http.StatusOK
		for key, values := range headers {
			if key == "Status" {
				status, _ = strconv.Atoi(strings.Fields(values[0])[0])
				continue
			}
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(status)
		_, _ = io.Copy(w, reader)
	})
}

func TestPinnedDefinitionHTTPSUsesAskpassAndVerifiesTLS(t *testing.T) {
	repository, sha := fixtureDefinitionRepository(t)
	root := t.TempDir()
	bare := filepath.Join(root, "team", "nested", "example.git")
	fixtureDefinitionGit(t, repository, "clone", "--bare", "--", repository, bare)
	var authenticated atomic.Int64
	backend := fixtureGitHTTPBackend(root)
	server, ca := fixtureDefinitionTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="synthetic fixture"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if username != "oauth2" || password != "synthetic-token" {
			http.Error(w, "wrong synthetic credentials", http.StatusForbidden)
			return
		}
		authenticated.Add(1)
		backend.ServeHTTP(w, r)
	}))
	req := GitDefinitionRequest{
		RepoURL: server.URL + "/team/nested/example.git", CommitSHA: sha,
		FilePath: ".arcentra/pipeline.yml", Auth: GitAuth{Token: "synthetic-token"},
	}
	// An inherited TLS bypass must never make the untrusted fixture certificate acceptable.
	t.Setenv("GIT_SSL_NO_VERIFY", "1")
	if _, _, err := ReadHTTPSDefinitionContext(context.Background(), req); err == nil {
		t.Fatal("untrusted TLS certificate accepted")
	}
	if authenticated.Load() != 0 {
		t.Fatal("credentials reached an untrusted TLS server")
	}
	trusted := fixtureTrustedDefinitionTransport(ca)
	var privateDir string
	content, actual, err := readHTTPSDefinitionContext(context.Background(), req,
		func(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
			privateDir = dir
			for _, arg := range args {
				if strings.Contains(arg, req.Auth.Token) {
					t.Fatal("credential exposed in process arguments")
				}
			}
			script, readErr := os.ReadFile(filepath.Join(dir, "askpass"))
			if readErr != nil || bytes.Contains(script, []byte(req.Auth.Token)) {
				t.Fatal("credential was persisted in askpass")
			}
			info, statErr := os.Stat(dir)
			if statErr != nil || info.Mode().Perm() != 0o700 {
				t.Fatal("workspace is not private")
			}
			return trusted(ctx, dir, env, args...)
		})
	if err != nil || content != "name: first\n" || actual != sha || authenticated.Load() == 0 {
		t.Fatalf("verified HTTPS/askpass fetch failed: %q %q %v", content, actual, err)
	}
	if _, err := os.Stat(privateDir); !os.IsNotExist(err) {
		t.Fatal("HTTPS credential workspace was retained")
	}
}

func TestPinnedDefinitionHTTPSRefusesCredentialRedirects(t *testing.T) {
	var destinationRequests atomic.Int64
	destination, _ := fixtureDefinitionTLS(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		destinationRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	source, ca := fixtureDefinitionTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := r.BasicAuth(); !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="synthetic fixture"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, destination.URL+r.URL.RequestURI(), http.StatusFound)
	}))
	_, _, err := readHTTPSDefinitionContext(context.Background(), GitDefinitionRequest{
		RepoURL: source.URL + "/team/example.git", CommitSHA: strings.Repeat("a", 40),
		FilePath: "pipeline.yml", Auth: GitAuth{Token: "synthetic-token"},
	}, fixtureTrustedDefinitionTransport(ca))
	if err == nil || destinationRequests.Load() != 0 {
		t.Fatalf("credential redirect was followed: requests=%d err=%v", destinationRequests.Load(), err)
	}
	if strings.Contains(err.Error(), "synthetic-token") || strings.Contains(err.Error(), source.URL) {
		t.Fatal("credential or raw transport error was exposed")
	}
}
