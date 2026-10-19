// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package pollsnapshot

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/arcentrix/arcentra/pkg/scm"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(body string, status int, h http.Header) *http.Response {
	if h == nil {
		h = http.Header{}
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: h}
}
func fixturePR() wirePR {
	no := false
	return wirePR{ID: 101, Number: 7, State: "open", Created: "2026-01-01T00:00:00Z", Updated: "2026-01-02T00:00:00Z", Merged: &no, Head: wireBranch{Ref: "feature", SHA: strings.Repeat("a", 40), Repo: &wireRepo{ID: 22, FullName: "fork/repo"}, RepoID: 22}, Base: wireBranch{Ref: "main", SHA: strings.Repeat("b", 40), Repo: &wireRepo{ID: 11, FullName: "owner/repo"}, RepoID: 11}}
}
func fixtureRequest() Request {
	return Request{Repository: "owner/repo", UpperBound: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)}
}
func fixtureReader(t *testing.T, provider string, limits Limits, handle func(*http.Request, int) *http.Response) *Reader {
	t.Helper()
	base := "https://github.com"
	if provider == "gitea" {
		base = "https://forge.example"
	}
	count := 0
	r, err := NewReader(scm.ProviderConfig{Kind: scm.ProviderKind(provider), BaseURL: base, Token: "secret-token"}, &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		count++
		host := "api.github.com"
		auth := "Bearer secret-token"
		if provider == "gitea" {
			host = "forge.example"
			auth = "token secret-token"
		}
		if req.URL.Scheme != "https" || req.URL.Host != host || req.Header.Get("Authorization") != auth || req.Header.Get("Cookie") != "" {
			t.Fatal("credential boundary violated")
		}
		if provider == "github" && req.Header.Get("X-GitHub-Api-Version") != "2026-03-10" {
			t.Fatal("API version missing")
		}
		return handle(req, count), nil
	})}, limits)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func inventoryResponse(req *http.Request, pr wirePR) *http.Response {
	if !strings.HasSuffix(req.URL.Path, "/pulls") {
		return response(`{"id":11,"full_name":"owner/repo"}`, 200, nil)
	}
	if req.URL.Query().Get("page") != "1" {
		return response(`[]`, 200, nil)
	}
	b, _ := json.Marshal([]wirePR{pr})
	return response(string(b), 200, nil)
}
func assertIncomplete(t *testing.T, out Result, err error) {
	t.Helper()
	var e *Error
	if !errors.Is(err, ErrIncomplete) || !errors.As(err, &e) || !reflect.DeepEqual(out, Result{}) {
		t.Fatalf("partial or untyped error: %+v %v", out, err)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "https:") {
		t.Fatalf("leaked error: %v", err)
	}
}
func TestReadersCompleteBoundedInventory(t *testing.T) {
	for _, provider := range []string{"github", "gitea"} {
		t.Run(provider, func(t *testing.T) {
			calls := 0
			r := fixtureReader(t, provider, Limits{}, func(req *http.Request, n int) *http.Response {
				calls = n
				if strings.HasSuffix(req.URL.Path, "/pulls") {
					q := req.URL.Query()
					if q.Get("state") != "all" || q.Get("page") == "" {
						t.Fatal(q)
					}
					if provider == "github" && (q.Get("sort") != "created" || q.Get("direction") != "asc") {
						t.Fatal(q)
					}
					if provider == "gitea" && (q.Get("sort") != "oldest" || q.Get("limit") != "100") {
						t.Fatal(q)
					}
				}
				return inventoryResponse(req, fixturePR())
			})
			out, err := r.Read(context.Background(), fixtureRequest())
			if err != nil {
				t.Fatal(err)
			}
			if calls != 6 || out.Version != 2 || out.Provider != provider || len(out.Observations) != 1 || out.Digest == "" || !strings.HasPrefix(out.Observations[0].ObservationID, provider+":pr:v2:") {
				t.Fatalf("bad snapshot %+v calls %d", out, calls)
			}
			o := out.Observations[0]
			if o.Title != "" || o.ActorName != "" || o.SourceProjectID != 22 || o.SourceRepository != "fork/repo" || o.TargetSHA == "" {
				t.Fatal(o)
			}
		})
	}
}
func TestMalformedAndMissingPREvidence(t *testing.T) {
	tests := map[string]func(*wirePR){"deleted_fork": func(p *wirePR) { p.Head.Repo = nil }, "wrong_target": func(p *wirePR) { p.Base.Repo.ID = 99 }, "source_id": func(p *wirePR) { p.Head.RepoID = 99 }, "short_sha": func(p *wirePR) { p.Head.SHA = "abc" }, "bad_target_sha": func(p *wirePR) { p.Base.SHA = "bad" }, "bad_ref": func(p *wirePR) { p.Head.Ref = "../x" }, "time": func(p *wirePR) { p.Updated = "2026-01-02T00:00:00,1Z" }, "future": func(p *wirePR) { p.Updated = "2026-02-02T00:00:00Z" }, "created_after_update": func(p *wirePR) { p.Created = "2026-01-03T00:00:00Z" }, "state": func(p *wirePR) { p.State = "secret-state" }, "missing_number": func(p *wirePR) { p.Number = 0 }, "missing_merged": func(p *wirePR) { p.Merged = nil }, "contradictory_merged": func(p *wirePR) { yes := true; p.Merged = &yes }, "merged_without_sha": func(p *wirePR) { m := "2026-01-02T00:00:00Z"; p.MergedAt = &m; p.State = "closed" }}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			pr := fixturePR()
			mutate(&pr)
			r := fixtureReader(t, "gitea", Limits{}, func(req *http.Request, _ int) *http.Response { return inventoryResponse(req, pr) })
			out, err := r.Read(context.Background(), fixtureRequest())
			assertIncomplete(t, out, err)
		})
	}
}
func TestMovementAndIdentity(t *testing.T) {
	for _, mode := range []string{"sha", "identity", "rename", "old_item", "duplicate", "order"} {
		t.Run(mode, func(t *testing.T) {
			r := fixtureReader(t, "github", Limits{}, func(req *http.Request, n int) *http.Response {
				p := fixturePR()
				if n >= 4 {
					switch mode {
					case "sha", "old_item":
						p.Head.SHA = strings.Repeat("c", 40)
					case "identity":
						if n == 6 {
							return response(`{"id":99,"full_name":"owner/repo"}`, 200, nil)
						}
					case "rename":
						if n == 6 {
							return response(`{"id":11,"full_name":"owner/renamed"}`, 200, nil)
						}
					}
				}
				if strings.HasSuffix(req.URL.Path, "/pulls") && req.URL.Query().Get("page") == "1" && (mode == "duplicate" || mode == "order") {
					p2 := p
					if mode == "order" {
						p2.ID = 102
						p2.Number = 8
						p2.Created = "2025-12-01T00:00:00Z"
					}
					b, _ := json.Marshal([]wirePR{p, p2})
					return response(string(b), 200, nil)
				}
				return inventoryResponse(req, p)
			})
			req := fixtureRequest()
			if mode == "old_item" {
				req.LowerBound = req.UpperBound
			}
			out, err := r.Read(context.Background(), req)
			assertIncomplete(t, out, err)
		})
	}
}
func TestBoundsAndRedaction(t *testing.T) {
	for _, mode := range []string{"redirect", "rate", "auth", "compressed", "bytes", "pages", "items", "totalbytes", "transport", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			limits := Limits{}
			switch mode {
			case "bytes":
				limits.MaxResponseBytes = 10
			case "pages":
				limits.MaxPages = 1
			case "items":
				limits.MaxItems = 1
			case "totalbytes":
				limits.MaxTotalBytes = 60
			}
			calls := 0
			r := fixtureReader(t, "github", limits, func(req *http.Request, n int) *http.Response {
				calls = n
				switch mode {
				case "redirect":
					return response("secret-body", 302, http.Header{"Location": {"https://evil.example/secret-token"}})
				case "rate":
					return response("secret-body", 429, http.Header{"Retry-After": {"999999999999"}})
				case "auth":
					return response("secret-body", 401, nil)
				case "compressed":
					return response("[]", 200, http.Header{"Content-Encoding": {"gzip"}})
				case "items":
					if strings.HasSuffix(req.URL.Path, "/pulls") {
						p := fixturePR()
						p2 := p
						p2.ID++
						p2.Number++
						b, _ := json.Marshal([]wirePR{p, p2})
						return response(string(b), 200, nil)
					}
				}
				return inventoryResponse(req, fixturePR())
			})
			ctx := context.Background()
			if mode == "transport" {
				r.client.Transport = roundTrip(func(*http.Request) (*http.Response, error) {
					return nil, fmt.Errorf("secret-token https://evil.example")
				})
			}
			if mode == "cancel" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			out, err := r.Read(ctx, fixtureRequest())
			assertIncomplete(t, out, err)
			if mode == "redirect" && calls != 1 {
				t.Fatal("redirect followed")
			}
			if mode == "rate" {
				e := err.(*Error)
				if e.RetryAt.Before(time.Now().Add(23*time.Hour)) || e.RetryAt.After(time.Now().Add(25*time.Hour)) {
					t.Fatal(e)
				}
			}
		})
	}
}
func TestConfigurationBoundaries(t *testing.T) {
	for _, cfg := range []scm.ProviderConfig{{Kind: scm.ProviderKindGitHub, BaseURL: "https://enterprise.example"}, {Kind: scm.ProviderKindGitHub, BaseURL: "https://github.com", APIBaseURL: "https://evil.example"}, {Kind: scm.ProviderKindGitHub, BaseURL: "https://github.com:443"}, {Kind: scm.ProviderKindGitea, BaseURL: "http://forge.example"}, {Kind: scm.ProviderKindGitea, BaseURL: "https://user:secret@forge.example"}, {Kind: scm.ProviderKindGitea, BaseURL: "https://forge.example", Token: "secret\r\n"}, {Kind: scm.ProviderKindGitea, BaseURL: "https://forge.example/x/../y"}} {
		if _, err := NewReader(cfg, nil, Limits{}); err == nil {
			t.Fatalf("accepted %+v", cfg)
		}
	}
	cfg := scm.ProviderConfig{Kind: scm.ProviderKindGitHub, BaseURL: "https://github.com"}
	if _, err := NewReader(cfg, &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}, Limits{}); err == nil {
		t.Fatal("insecure TLS accepted")
	}
	proxy := func(*http.Request) (*url.URL, error) { t.Fatal("proxy invoked"); return nil, nil }
	r, err := NewReader(cfg, &http.Client{Transport: &http.Transport{Proxy: proxy}}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	tr := r.client.Transport.(*http.Transport)
	if tr.Proxy != nil || tr.TLSClientConfig.MinVersion < tls.VersionTLS12 || r.client.Jar != nil {
		t.Fatal("unsafe transport")
	}
}
func TestMergeEvidenceAndBounds(t *testing.T) {
	pr := fixturePR()
	yes := true
	pr.Merged = &yes
	pr.State = "closed"
	m := "2026-01-02T00:00:00Z"
	pr.MergedAt = &m
	pr.MergeSHA = strings.Repeat("c", 40)
	r := fixtureReader(t, "gitea", Limits{}, func(req *http.Request, _ int) *http.Response { return inventoryResponse(req, pr) })
	out, err := r.Read(context.Background(), fixtureRequest())
	if err != nil {
		t.Fatal(err)
	}
	o := out.Observations[0]
	if o.State != "merged" || o.MergeIdentity == "" || o.MergeIdentity != o.ObservationID || o.MergeSHA != pr.MergeSHA {
		t.Fatal(o)
	}
	req := fixtureRequest()
	req.LowerBound = req.UpperBound
	out, err = r.Read(context.Background(), req)
	if err != nil || len(out.Observations) != 0 {
		t.Fatalf("lower bound: %+v %v", out, err)
	}
}

func TestPageDecodeStopsAtItemBudget(t *testing.T) {
	// The invalid suffix must never be decoded: the fourth member already proves
	// the page exceeds its three-item budget. No proportional typed slice is made.
	for _, body := range []string{`[{},{},{},NOT_JSON`, "[" + strings.Repeat("{},", 10000) + "{}]"} {
		items, err := decodePage([]byte(body), 3)
		var failure *Error
		if len(items) != 0 || !errors.As(err, &failure) || failure.Reason != ReasonLimit {
			t.Fatalf("decoder did not stop at item budget: %d %v", len(items), err)
		}
	}
	for _, body := range []string{`null`, `{}`, `[`, `[{}] {}`, `[{}] []`, `[{}] garbage`, `[{},]`, `[{}] 1`} {
		if _, err := decodePage([]byte(body), 3); err == nil {
			t.Fatalf("malformed page accepted: %s", body)
		}
	}
	items, err := decodePage([]byte(`[{},{}]`), 2)
	if err != nil || len(items) != 2 {
		t.Fatalf("exact budget rejected: %d %v", len(items), err)
	}
	items, err = decodePage([]byte(`[]`), 2)
	if err != nil || items == nil || len(items) != 0 {
		t.Fatalf("empty sentinel rejected: %v %v", items, err)
	}
}

func TestManualMergeProviderTimestampSemantics(t *testing.T) {
	for _, test := range []struct {
		name, provider, mergedAt string
		accepted                 bool
	}{
		{"gitea_author_time_before_creation", "gitea", "2020-03-04T05:06:07Z", true},
		{"gitea_author_time_after_update", "gitea", "2026-01-02T00:00:01Z", false},
		{"gitea_future_author_time", "gitea", "2027-01-01T00:00:00Z", false},
		{"github_before_creation", "github", "2020-03-04T05:06:07Z", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			pr := fixturePR()
			yes := true
			pr.Merged = &yes
			pr.State = "closed"
			pr.MergedAt = &test.mergedAt
			pr.MergeSHA = strings.Repeat("c", 40)
			r := fixtureReader(t, test.provider, Limits{}, func(req *http.Request, _ int) *http.Response { return inventoryResponse(req, pr) })
			out, err := r.Read(context.Background(), fixtureRequest())
			if !test.accepted {
				assertIncomplete(t, out, err)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			observed := out.Observations[0]
			if observed.State != "merged" || observed.MergedAt == nil || observed.MergedAt.Format(time.RFC3339Nano) != test.mergedAt || observed.MergeIdentity == "" || observed.MergeSHA != pr.MergeSHA {
				t.Fatalf("raw manual-merge evidence changed: %+v", observed)
			}
		})
	}
}
