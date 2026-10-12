// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package sourcefile

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
)

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func readerFixture(t *testing.T, mutate func(string, map[string]any)) (*Reader, *int) {
	t.Helper()
	return readerFixtureContent(t, []byte("package sample\n"), mutate)
}

func readerFixtureContent(t *testing.T, content []byte, mutate func(string, map[string]any)) (*Reader, *int) {
	t.Helper()
	h := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(content))), content...))
	blob := hex.EncodeToString(h[:])
	commit := strings.Repeat("a", 40)
	tree := strings.Repeat("b", 40)
	subtree := strings.Repeat("c", 40)
	calls := 0
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: fixtureTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host != "api.github.com" || req.URL.Scheme != "https" || req.URL.RawQuery != "" || req.Method != "GET" {
			t.Fatalf("unsafe request %s", req.URL)
		}
		if req.Header.Get("Authorization") != "Bearer synthetic-fixture-token" || req.Header.Get("X-GitHub-Api-Version") != "2026-03-10" {
			t.Fatal("missing fixed API or fixture authorization")
		}
		var response map[string]any
		switch req.URL.Path {
		case "/repos/owner/repo/git/commits/" + commit:
			response = map[string]any{"sha": commit, "tree": map[string]any{"sha": tree}}
		case "/repos/owner/repo/git/trees/" + tree:
			response = map[string]any{"sha": tree, "truncated": false, "tree": []map[string]any{{"path": "src", "mode": "040000", "type": "tree", "sha": subtree}}}
		case "/repos/owner/repo/git/trees/" + subtree:
			response = map[string]any{"sha": subtree, "truncated": false, "tree": []map[string]any{{"path": "main.go", "mode": "100644", "type": "blob", "sha": blob, "size": len(content)}}}
		case "/repos/owner/repo/git/blobs/" + blob:
			response = map[string]any{"sha": blob, "encoding": "base64", "size": len(content), "content": base64.StdEncoding.EncodeToString(content)}
		default:
			t.Fatalf("unexpected path: %s", req.URL.Path)
		}
		if mutate != nil {
			mutate(req.URL.Path, response)
		}
		data, err := sonic.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(data))), ContentLength: int64(len(data)), Request: req}, nil
	})}
	return &Reader{client: client}, &calls
}

func TestPinnedRegularFileAndFixedRequests(t *testing.T) {
	r, calls := readerFixture(t, nil)
	out, err := r.ReadFileAtCommit(context.Background(), "https://github.com/Owner/Repo.git", strings.Repeat("a", 40), "src/main.go", "synthetic-fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	if out.Repository != "https://github.com/owner/repo" || out.Path != "src/main.go" || string(out.Content) != "package sample\n" || out.Mode != "100644" || *calls != 4 {
		t.Fatalf("unexpected source: %+v requests=%d", out, *calls)
	}
}

func TestRejectObjectAndModeAmbiguity(t *testing.T) {
	cases := map[string]func(string, map[string]any){
		"commit mismatch": func(p string, v map[string]any) {
			if strings.Contains(p, "/commits/") {
				v["sha"] = strings.Repeat("d", 40)
			}
		},
		"tree mismatch": func(p string, v map[string]any) {
			if strings.Contains(p, "/trees/") {
				v["sha"] = strings.Repeat("d", 40)
			}
		},
		"truncated tree": func(p string, v map[string]any) {
			if strings.Contains(p, "/trees/") {
				v["truncated"] = true
			}
		},
		"symlink": func(p string, v map[string]any) {
			if strings.HasSuffix(p, strings.Repeat("c", 40)) {
				v["tree"].([]map[string]any)[0]["mode"] = "120000"
			}
		},
		"submodule": func(p string, v map[string]any) {
			if strings.HasSuffix(p, strings.Repeat("b", 40)) {
				e := v["tree"].([]map[string]any)[0]
				e["mode"] = "160000"
				e["type"] = "commit"
			}
		},
		"duplicate path": func(p string, v map[string]any) {
			if strings.Contains(p, "/trees/") {
				entries := v["tree"].([]map[string]any)
				v["tree"] = append(entries, entries[0])
			}
		},
		"oversize": func(p string, v map[string]any) {
			if strings.HasSuffix(p, strings.Repeat("c", 40)) {
				v["tree"].([]map[string]any)[0]["size"] = 8193
			}
		},
		"missing size": func(p string, v map[string]any) {
			if strings.HasSuffix(p, strings.Repeat("c", 40)) {
				delete(v["tree"].([]map[string]any)[0], "size")
			}
		},
		"blob sha mismatch": func(p string, v map[string]any) {
			if strings.Contains(p, "/blobs/") {
				v["sha"] = strings.Repeat("d", 40)
			}
		},
		"blob integrity": func(p string, v map[string]any) {
			if strings.Contains(p, "/blobs/") {
				v["content"] = base64.StdEncoding.EncodeToString([]byte("package edited\n"))
			}
		},
		"blob encoding": func(p string, v map[string]any) {
			if strings.Contains(p, "/blobs/") {
				v["encoding"] = "utf-8"
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r, _ := readerFixture(t, mutate)
			if _, err := r.ReadFileAtCommit(context.Background(), "https://github.com/owner/repo", strings.Repeat("a", 40), "src/main.go", "synthetic-fixture-token"); err != ErrUnavailable {
				t.Fatalf("want fixed refusal got %v", err)
			}
		})
	}
}

func TestRejectUnsafeInputsWithoutRequests(t *testing.T) {
	r, calls := readerFixture(t, nil)
	for _, repository := range []string{"http://github.com/a/b", "https://github.com:443/a/b", "https://github.com.evil.test/a/b", "https://user:secret@github.com/a/b", "https://github.com/a/b?token=secret", "https://github.com/a/b#x", "https://github.com/a/b/", "https://github.com/a/%62", "https://github.com/a/../b", "git@github.com:a/b", "/tmp/repo"} {
		if _, err := r.ReadFileAtCommit(context.Background(), repository, strings.Repeat("a", 40), "main.go", ""); err == nil {
			t.Fatalf("accepted %q", repository)
		}
	}
	for _, name := range []string{"../main.go", "/main.go", "a/../b.go", "a//b.go", "a\\b.go", "a%2fb.go", "a\x00.go", "a\u0085.go", "a/./b.go", strings.Repeat("a/", 8) + "main.go", strings.Repeat("x", 513)} {
		if ValidPath(name) {
			t.Fatalf("accepted %q", name)
		}
	}
	for _, commit := range []string{"main", strings.Repeat("A", 40), strings.Repeat("a", 39), strings.Repeat("a", 64)} {
		if _, err := r.ReadFileAtCommit(context.Background(), "https://github.com/a/b", commit, "main.go", ""); err == nil {
			t.Fatal("accepted invalid commit")
		}
	}
	if *calls != 0 {
		t.Fatal("invalid inputs reached transport")
	}
}

func TestTransportRejectsRedirectLargeBodyAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		length int64
	}{
		{"redirect", 302, "secret-response", 15}, {"upstream", 403, "secret-response", 15}, {"too large", 200, strings.Repeat("x", maxMetadataBytes+1), -1}, {"declared too large", 200, "", maxMetadataBytes + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			r := &Reader{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: fixtureTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Location": []string{"https://evil.invalid/secret"}}, Body: io.NopCloser(strings.NewReader(tc.body)), ContentLength: tc.length, Request: req}, nil
			})}}
			_, err := r.ReadFileAtCommit(context.Background(), "https://github.com/a/b", strings.Repeat("a", 40), "main.go", "")
			if err != ErrUnavailable || calls != 1 {
				t.Fatalf("error=%v calls=%d", err, calls)
			}
		})
	}
}

func TestPublicAddressFilteringAndProductionTransport(t *testing.T) {
	for _, raw := range []string{"4000::1", "::ffff:0:127.0.0.1", "0.0.0.1", "100::1", "fec0::1", "192.88.99.1", "3fff::1", "127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.0.1", "169.254.169.254", "100.64.0.1", "0.0.0.0", "224.0.0.1", "192.0.2.1", "198.18.0.1", "::1", "fc00::1", "fe80::1", "::ffff:127.0.0.1", "64:ff9b::7f00:1", "2002:7f00:1::", "2001:db8::1"} {
		if publicIP(netip.MustParseAddr(raw)) {
			t.Fatalf("accepted unsafe address %s", raw)
		}
	}
	for _, raw := range []string{"140.82.112.6", "2606:50c0:8000::154"} {
		if !publicIP(netip.MustParseAddr(raw)) {
			t.Fatalf("rejected public address %s", raw)
		}
	}
	r := NewReader()
	transport := r.client.Transport.(*http.Transport)
	if transport.Proxy != nil || transport.TLSClientConfig.InsecureSkipVerify || transport.DialContext == nil || !transport.DisableCompression {
		t.Fatal("unsafe transport")
	}
	if err := r.client.CheckRedirect(nil, nil); err != http.ErrUseLastResponse {
		t.Fatal("redirect enabled")
	}
	for _, address := range []string{"localhost:443", "api.github.com:80", "127.0.0.1:443"} {
		if _, err := publicGitHubDial(context.Background(), "tcp", address); err != ErrUnavailable {
			t.Fatal("unexpected dial")
		}
	}
}

func TestUnsafeContentRejected(t *testing.T) {
	for name, content := range map[string][]byte{"binary": {0, 1, 2}, "invalid utf8": {255, 254}, "lfs": []byte("version https://git-lfs.github.com/spec/v1\noid sha256:abc\n"), "large": []byte(strings.Repeat("x", 8193))} {
		t.Run(name, func(t *testing.T) {
			r, _ := readerFixtureContent(t, content, nil)
			if _, err := r.ReadFileAtCommit(context.Background(), "https://github.com/owner/repo", strings.Repeat("a", 40), "src/main.go", "synthetic-fixture-token"); err != ErrUnavailable {
				t.Fatalf("got %v", err)
			}
		})
	}
	for _, name := range []string{".env", "dir/.git/config", "secret.go", "config/api-key.json", "id_rsa", "kubeconfig.yaml", "picture.png"} {
		if ValidPath(name) {
			t.Fatalf("unsafe code path %q", name)
		}
	}
}

func TestDNSCheckedOnceAndOnlyPinnedPublicIPsDialed(t *testing.T) {
	lookups, dials := 0, 0
	lookup := func(context.Context, string, string) ([]netip.Addr, error) {
		lookups++
		return []netip.Addr{netip.MustParseAddr("140.82.112.6")}, nil
	}
	dial := func(_ context.Context, network, address string) (net.Conn, error) {
		dials++
		if network != "tcp" || address != "140.82.112.6:443" {
			t.Fatal("hostname re-resolved or wrong target")
		}
		a, b := net.Pipe()
		_ = b.Close()
		return a, nil
	}
	c, err := dialGitHub(context.Background(), "tcp", "api.github.com:443", lookup, dial)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	if lookups != 1 || dials != 1 {
		t.Fatal("unexpected resolution count")
	}
	lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("140.82.112.6"), netip.MustParseAddr("127.0.0.1")}, nil
	}
	if _, err := dialGitHub(context.Background(), "tcp", "api.github.com:443", lookup, dial); err != ErrUnavailable || dials != 1 {
		t.Fatal("mixed private DNS response reached dial")
	}
}

func TestProviderURLsAreNeverFollowed(t *testing.T) {
	r, calls := readerFixture(t, func(_ string, v map[string]any) {
		v["url"] = "http://169.254.169.254/credentials"
		v["download_url"] = "https://evil.invalid/private"
	})
	if _, err := r.ReadFileAtCommit(context.Background(), "https://github.com/owner/repo", strings.Repeat("a", 40), "src/main.go", "synthetic-fixture-token"); err != nil || *calls != 4 {
		t.Fatalf("fixed-source read: %v calls=%d", err, *calls)
	}
}

func TestCancellationPreservedWithoutFallbackRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, calls := readerFixture(t, nil)
	if _, err := r.ReadFileAtCommit(ctx, "https://github.com/owner/repo", strings.Repeat("a", 40), "main.go", "synthetic-fixture-token"); !errors.Is(err, context.Canceled) || *calls != 0 {
		t.Fatalf("cancelled read %v calls=%d", err, *calls)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	count := 0
	r = &Reader{client: &http.Client{Transport: fixtureTransport(func(req *http.Request) (*http.Response, error) {
		count++
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}}
	if _, err := r.ReadFileAtCommit(ctx, "https://github.com/owner/repo", strings.Repeat("a", 40), "main.go", "synthetic-fixture-token"); !errors.Is(err, context.DeadlineExceeded) || count != 1 {
		t.Fatalf("deadline read %v calls=%d", err, count)
	}
}
