// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package pollsnapshot

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/pkg/scm"
)

const targetUUID = "11111111-2222-4333-8444-555555555555"
const sourceUUID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"

func v3FixtureReader(t *testing.T, provider string, limits Limits, handle func(*http.Request, int) *http.Response) *V3Reader {
	t.Helper()
	base := "https://gitee.com"
	if provider == "bitbucket" {
		base = "https://bitbucket.org"
	}
	count := 0
	reader, err := NewV3Reader(scm.ProviderConfig{Kind: scm.ProviderKind(provider), BaseURL: base, Token: "synthetic-secret"}, &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		count++
		if req.URL.Scheme != "https" || req.Header.Get("Cookie") != "" {
			t.Fatal("unsafe transport")
		}
		if provider == "gitee" {
			if req.URL.Host != "gitee.com" || req.Header.Get("Authorization") != "" || req.URL.Query().Get("access_token") != "synthetic-secret" {
				t.Fatal("wrong Gitee auth boundary")
			}
		} else {
			if req.URL.Host != "api.bitbucket.org" || req.Header.Get("Authorization") != "Bearer synthetic-secret" || req.URL.Query().Has("access_token") {
				t.Fatal("wrong Bitbucket auth boundary")
			}
		}
		return handle(req, count), nil
	})}, limits)
	if err != nil {
		t.Fatal(err)
	}
	return reader
}
func v3Request(provider string) Request {
	req := fixtureRequest()
	if provider == "gitee" {
		req.ProviderProjectKey = "11"
	} else {
		req.ProviderProjectKey = targetUUID
	}
	return req
}
func bbFixture() bitbucketPR {
	w := bitbucketPR{ID: 7, State: "OPEN", Created: "2026-01-01T00:00:00Z", Updated: "2026-01-02T00:00:00Z"}
	w.Source.Repository = &bitbucketRepo{UUID: "{" + strings.ToUpper(sourceUUID) + "}", FullName: "fork/repo"}
	w.Destination.Repository = &bitbucketRepo{UUID: "{" + targetUUID + "}", FullName: "owner/repo"}
	w.Source.Branch.Name = "feature"
	w.Destination.Branch.Name = "main"
	w.Source.Commit.Hash = strings.Repeat("a", 40)
	w.Destination.Commit.Hash = strings.Repeat("b", 40)
	return w
}
func v3RepoResponse(provider string) *http.Response {
	if provider == "gitee" {
		return response(`{"id":11,"full_name":"owner/repo"}`, 200, nil)
	}
	return response(`{"uuid":"{`+targetUUID+`}","full_name":"owner/repo"}`, 200, nil)
}
func v3IsRepo(req *http.Request) bool { return strings.HasSuffix(req.URL.Path, "owner/repo") }
func v3Inventory(provider string, req *http.Request, rows any) *http.Response {
	if v3IsRepo(req) {
		return v3RepoResponse(provider)
	}
	if provider == "gitee" {
		if req.URL.Query().Get("page") != "1" {
			return response(`[]`, 200, nil)
		}
		body, _ := json.Marshal(rows)
		return response(string(body), 200, nil)
	}
	body, _ := json.Marshal(map[string]any{"values": rows})
	return response(string(body), 200, nil)
}
func v3ReadRows(t *testing.T, provider string, rows any) Result {
	t.Helper()
	reader := v3FixtureReader(t, provider, Limits{}, func(req *http.Request, _ int) *http.Response { return v3Inventory(provider, req, rows) })
	out, err := reader.Read(context.Background(), v3Request(provider))
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestV3AllStatesAndHonestMergeEvidence(t *testing.T) {
	g := []wirePR{fixturePR(), fixturePR(), fixturePR()}
	for i := range g {
		g[i].ID += int64(i)
		g[i].Number += int64(i)
		g[i].Merged = nil
	}
	g[1].State = "closed"
	g[2].State = "merged"
	stamp := "2026-01-02T00:00:00Z"
	g[2].MergedAt = &stamp
	out := v3ReadRows(t, "gitee", g)
	if out.Version != 3 || out.ProviderProjectID != 0 || out.ProviderProjectKey != "11" || out.APIOrigin != "https://gitee.com" || len(out.Observations) != 3 {
		t.Fatalf("bad result: %+v", out)
	}
	for i, o := range out.Observations {
		if o.EvidenceVersion != 3 || o.ProviderProjectID != 0 || o.SourceProjectID != 0 || o.SourceProjectKey != "22" || o.MergeEvidence != MergeEvidenceStateTimestamp || o.MergeSHA != "" || o.TargetSHA == "" {
			t.Fatalf("bad observation %+v", o)
		}
		if (i == 2) != (o.MergedAt != nil) {
			t.Fatal("synthetic or absent merge time")
		}
	}
	bb := []bitbucketPR{bbFixture(), bbFixture(), bbFixture(), bbFixture()}
	for i := range bb {
		bb[i].ID += int64(i)
	}
	bb[1].State = "MERGED"
	bb[1].MergeCommit = &bitbucketCommit{Hash: strings.Repeat("c", 40)}
	bb[2].State = "DECLINED"
	bb[3].State = "SUPERSEDED"
	out = v3ReadRows(t, "bitbucket", bb)
	if out.APIOrigin != "https://api.bitbucket.org" || out.ProviderProjectKey != targetUUID || len(out.Observations) != 4 {
		t.Fatal("bad Bitbucket result")
	}
	for _, o := range out.Observations {
		if o.MergedAt != nil || o.SourceProjectKey != sourceUUID || o.MergeEvidence != MergeEvidenceStateCommit {
			t.Fatal("invented time or lost UUID")
		}
	}
	if out.Observations[2].DispatchEligible() || out.Observations[3].DispatchEligible() {
		t.Fatal("closed dispatched")
	}
}
func TestV3StableMergeIdentityAndInclusiveUpdateCutoff(t *testing.T) {
	for _, provider := range []string{"gitee", "bitbucket"} {
		t.Run(provider, func(t *testing.T) {
			var rows any
			g := fixturePR()
			g.State = "merged"
			g.Merged = nil
			m := "2026-01-01T23:00:00Z"
			g.MergedAt = &m
			bb := bbFixture()
			bb.State = "MERGED"
			bb.MergeCommit = &bitbucketCommit{Hash: strings.Repeat("c", 40)}
			if provider == "gitee" {
				rows = []wirePR{g}
			} else {
				rows = []bitbucketPR{bb}
			}
			first := v3ReadRows(t, provider, rows)
			g.Updated = "2026-01-02T12:00:00Z"
			bb.Updated = g.Updated
			if provider == "gitee" {
				rows = []wirePR{g}
			} else {
				rows = []bitbucketPR{bb}
			}
			second := v3ReadRows(t, provider, rows)
			if first.Observations[0].MergeIdentity != second.Observations[0].MergeIdentity || first.Observations[0].ObservationID != second.Observations[0].ObservationID {
				t.Fatal("metadata update changed merge identity")
			}
			reader := v3FixtureReader(t, provider, Limits{}, func(req *http.Request, _ int) *http.Response { return v3Inventory(provider, req, rows) })
			req := v3Request(provider)
			req.LowerBound = second.Observations[0].UpdatedAt
			equal, err := reader.Read(context.Background(), req)
			if err != nil || len(equal.Observations) != 1 {
				t.Fatal("inclusive lower bound lost event")
			}
			req.LowerBound = req.LowerBound.Add(time.Nanosecond)
			after, err := reader.Read(context.Background(), req)
			if err != nil || len(after.Observations) != 0 {
				t.Fatal("lower bound ignored")
			}
		})
	}
}
func TestV3RejectProviderConfusionAndIdentities(t *testing.T) {
	for _, provider := range []string{"gitee", "bitbucket"} {
		base := "https://gitee.com"
		api := "https://gitee.com/api/v5"
		if provider == "bitbucket" {
			base = "https://bitbucket.org"
			api = "https://api.bitbucket.org/2.0"
		}
		for _, bad := range []string{"http" + strings.TrimPrefix(base, "https"), base + ":443", base + "/path", base + "?q=x", base + "#x", strings.Replace(base, "https://", "https://u@", 1), base + "%2f", base + ".", strings.ToUpper(base)} {
			if _, err := NewV3Reader(scm.ProviderConfig{Kind: scm.ProviderKind(provider), BaseURL: bad}, nil, Limits{}); err == nil {
				t.Fatalf("accepted origin %s", bad)
			}
		}
		for _, bad := range []string{base, api + "?x=1", api + "/other", "https://other.example"} {
			if _, err := NewV3Reader(scm.ProviderConfig{Kind: scm.ProviderKind(provider), BaseURL: base, APIBaseURL: bad}, nil, Limits{}); err == nil {
				t.Fatal("accepted wrong api")
			}
		}
		if _, err := NewReader(scm.ProviderConfig{Kind: scm.ProviderKind(provider), BaseURL: base}, nil, Limits{}); err == nil {
			t.Fatal("legacy accepted v3 provider")
		}
		reader := v3FixtureReader(t, provider, Limits{}, func(req *http.Request, _ int) *http.Response { return v3RepoResponse(provider) })
		for _, mutate := range []func(*Request){func(r *Request) { r.ProviderProjectID = 11 }, func(r *Request) { r.ProviderProjectKey = "0001" }, func(r *Request) { r.Repository = "owner/../repo" }, func(r *Request) { r.ProviderProjectKey = "{ " + targetUUID + " }" }} {
			req := v3Request(provider)
			mutate(&req)
			out, err := reader.Read(context.Background(), req)
			assertIncomplete(t, out, err)
		}
	}
	for _, raw := range []string{"00000000-0000-0000-0000-000000000000", "bad", " " + targetUUID, targetUUID + " ", "{{" + targetUUID + "}}"} {
		if _, err := CanonicalProjectKey("bitbucket", raw); err == nil {
			t.Fatal("bad UUID accepted")
		}
	}
	if key, err := CanonicalProjectKey("bitbucket", "{"+strings.ToUpper(sourceUUID)+"}"); err != nil || key != sourceUUID {
		t.Fatal("UUID canonicalization failed")
	}
}
func TestV3GiteeMalformedEvidenceZeroResult(t *testing.T) {
	cases := map[string]func(*wirePR){
		"missing_fork": func(p *wirePR) { p.Head.Repo = nil }, "wrong_base": func(p *wirePR) { p.Base.Repo.ID = 33 }, "renamed_base": func(p *wirePR) { p.Base.Repo.FullName = "owner/renamed" }, "same_key_foreign_name": func(p *wirePR) { p.Head.Repo.ID = 11 }, "same_name_foreign_key": func(p *wirePR) { p.Head.Repo.FullName = "owner/repo" }, "short_hash": func(p *wirePR) { p.Head.SHA = "abcdef" }, "target_hash": func(p *wirePR) { p.Base.SHA = "" }, "unsafe_ref": func(p *wirePR) { p.Head.Ref = "../../main" }, "state": func(p *wirePR) { p.State = "locked" }, "time": func(p *wirePR) { p.Updated = "2026-01-04T00:00:00Z" }, "missing_merge_time": func(p *wirePR) { p.State = "merged" }, "contradictory_time": func(p *wirePR) { m := "2026-01-02T00:00:00Z"; p.MergedAt = &m }, "invented_merge_hash": func(p *wirePR) { p.MergeSHA = strings.Repeat("c", 40) }, "contradictory_bool": func(p *wirePR) { v := true; p.Merged = &v }, "bad_merge_time": func(p *wirePR) { p.State = "merged"; p.Merged = nil; m := "2025-01-01T00:00:00Z"; p.MergedAt = &m },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := fixturePR()
			mutate(&p)
			reader := v3FixtureReader(t, "gitee", Limits{}, func(req *http.Request, _ int) *http.Response { return v3Inventory("gitee", req, []wirePR{p}) })
			out, err := reader.Read(context.Background(), v3Request("gitee"))
			assertIncomplete(t, out, err)
		})
	}
}
func TestV3BitbucketMalformedEvidenceZeroResult(t *testing.T) {
	cases := map[string]func(*bitbucketPR){"missing_fork": func(p *bitbucketPR) { p.Source.Repository = nil }, "wrong_base": func(p *bitbucketPR) { p.Destination.Repository.UUID = sourceUUID }, "renamed_base": func(p *bitbucketPR) { p.Destination.Repository.FullName = "owner/renamed" }, "same_key_foreign_name": func(p *bitbucketPR) { p.Source.Repository.UUID = targetUUID }, "same_name_foreign_key": func(p *bitbucketPR) { p.Source.Repository.FullName = "owner/repo" }, "short_hash": func(p *bitbucketPR) { p.Source.Commit.Hash = "abcdef" }, "target_hash": func(p *bitbucketPR) { p.Destination.Commit.Hash = "" }, "unsafe_ref": func(p *bitbucketPR) { p.Source.Branch.Name = "x.lock" }, "state": func(p *bitbucketPR) { p.State = "ALL" }, "time": func(p *bitbucketPR) { p.Updated = "bad" }, "missing_merge_hash": func(p *bitbucketPR) { p.State = "MERGED" }, "short_merge_hash": func(p *bitbucketPR) { p.State = "MERGED"; p.MergeCommit = &bitbucketCommit{Hash: "abc"} }, "contradictory_hash": func(p *bitbucketPR) { p.MergeCommit = &bitbucketCommit{Hash: strings.Repeat("c", 40)} }}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := bbFixture()
			mutate(&p)
			reader := v3FixtureReader(t, "bitbucket", Limits{}, func(req *http.Request, _ int) *http.Response { return v3Inventory("bitbucket", req, []bitbucketPR{p}) })
			out, err := reader.Read(context.Background(), v3Request("bitbucket"))
			assertIncomplete(t, out, err)
		})
	}
}
func TestV3TwoPassMutationAndFinalIdentityZeroResult(t *testing.T) {
	for _, provider := range []string{"gitee", "bitbucket"} {
		for _, mode := range []string{"insert", "delete", "replace", "update", "source_key", "target_hash", "rename", "replacement", "old_item"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				pass := 0
				repos := 0
				reader := v3FixtureReader(t, provider, Limits{}, func(req *http.Request, _ int) *http.Response {
					if v3IsRepo(req) {
						repos++
						if repos == 2 && mode == "rename" {
							if provider == "gitee" {
								return response(`{"id":11,"full_name":"owner/renamed"}`, 200, nil)
							}
							return response(`{"uuid":"`+targetUUID+`","full_name":"owner/renamed"}`, 200, nil)
						}
						if repos == 2 && mode == "replacement" {
							if provider == "gitee" {
								return response(`{"id":99,"full_name":"owner/repo"}`, 200, nil)
							}
							return response(`{"uuid":"`+sourceUUID+`","full_name":"owner/repo"}`, 200, nil)
						}
						return v3RepoResponse(provider)
					}
					if provider == "gitee" && req.URL.Query().Get("page") != "1" {
						return response(`[]`, 200, nil)
					}
					pass++
					g := fixturePR()
					bb := bbFixture()
					if pass == 2 {
						switch mode {
						case "replace":
							g.ID = 999
							bb.ID = 999
						case "update", "old_item":
							g.Head.SHA = strings.Repeat("d", 40)
							bb.Source.Commit.Hash = g.Head.SHA
						case "source_key":
							g.Head.Repo.ID = 99
							bb.Source.Repository.UUID = "99999999-2222-4333-8444-555555555555"
						case "target_hash":
							g.Base.SHA = strings.Repeat("e", 40)
							bb.Destination.Commit.Hash = g.Base.SHA
						}
					}
					gs := []wirePR{g}
					bbs := []bitbucketPR{bb}
					if pass == 2 && mode == "delete" {
						gs = []wirePR{}
						bbs = []bitbucketPR{}
					}
					if pass == 2 && mode == "insert" {
						g.ID++
						g.Number++
						bb.ID++
						gs = append(gs, g)
						bbs = append(bbs, bb)
					}
					if provider == "gitee" {
						return v3Inventory(provider, req, gs)
					}
					return v3Inventory(provider, req, bbs)
				})
				req := v3Request(provider)
				if mode == "old_item" {
					req.LowerBound = req.UpperBound
				}
				out, err := reader.Read(context.Background(), req)
				assertIncomplete(t, out, err)
			})
		}
	}
}
func TestV3TransportCredentialsBudgetsAndStatuses(t *testing.T) {
	for _, provider := range []string{"gitee", "bitbucket"} {
		for _, status := range []int{301, 302, 307, 308, 401, 403, 429, 500} {
			t.Run(fmt.Sprintf("%s/%d", provider, status), func(t *testing.T) {
				calls := 0
				reader := v3FixtureReader(t, provider, Limits{}, func(req *http.Request, _ int) *http.Response {
					calls++
					h := http.Header{}
					h.Set("Location", "https://evil.example/secret")
					h.Set("Retry-After", "999999999999999")
					h.Set("X-RateLimit-Limit", "0")
					h.Set("X-RateLimit-NearLimit", "true")
					return response("synthetic-secret", status, h)
				})
				out, err := reader.Read(context.Background(), v3Request(provider))
				assertIncomplete(t, out, err)
				var e *Error
				if !errors.As(err, &e) || strings.Contains(err.Error(), "synthetic-secret") || calls != 1 {
					t.Fatal("unsafe error or redirect")
				}
				if status == 403 && e.Reason != ReasonForbidden {
					t.Fatal("false rate limit")
				}
				if status == 429 && (e.Reason != ReasonRateLimit || e.RetryAt.After(time.Now().Add(24*time.Hour))) {
					t.Fatal("unbounded retry")
				}
			})
		}
		for _, mode := range []string{"requests", "response", "aggregate", "headers", "encoding", "utf8", "timeout", "allocation", "duplicate_json"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				limits := Limits{}
				switch mode {
				case "requests":
					limits.MaxPages = 3
				case "response":
					limits.MaxResponseBytes = 1
				case "aggregate":
					limits.MaxTotalBytes = 80
				case "timeout":
					limits.ScanTimeout = time.Nanosecond
				}
				reader := v3FixtureReader(t, provider, limits, func(req *http.Request, _ int) *http.Response {
					h := http.Header{}
					switch mode {
					case "headers":
						h.Set("X-Huge", strings.Repeat("x", 32769))
						return response(`{}`, 200, h)
					case "encoding":
						h.Set("Content-Encoding", "gzip")
						return response(`{}`, 200, h)
					case "utf8":
						return response(string([]byte{255}), 200, nil)
					}
					if v3IsRepo(req) {
						return v3RepoResponse(provider)
					}
					if mode == "allocation" {
						body := "[" + strings.Repeat("{},", 100) + "{}]"
						if provider == "bitbucket" {
							body = `{"values":` + body + `}`
						}
						return response(body, 200, nil)
					}
					if mode == "duplicate_json" {
						if provider == "gitee" {
							return response(`[{"id":1,"id":2}]`, 200, nil)
						}
						return response(`{"values":[],"values":[]}`, 200, nil)
					}
					if provider == "gitee" {
						return v3Inventory(provider, req, []wirePR{fixturePR()})
					}
					return v3Inventory(provider, req, []bitbucketPR{bbFixture()})
				})
				out, err := reader.Read(context.Background(), v3Request(provider))
				assertIncomplete(t, out, err)
			})
		}
	}
	base := scm.ProviderConfig{Kind: scm.ProviderKindGitee, BaseURL: "https://gitee.com"}
	if _, err := NewV3Reader(base, &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}, Limits{}); err == nil {
		t.Fatal("insecure TLS")
	}
	jar, _ := cookiejar.New(nil)
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	reader, err := NewV3Reader(base, &http.Client{Jar: jar, Transport: transport}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	safe := reader.client.Transport.(*http.Transport)
	if reader.client.Jar != nil || safe.Proxy != nil || safe == transport || !safe.DisableCompression || safe.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatal("ambient transport leaked")
	}
}
func TestV3NoRawTransportErrorOrTokenInResult(t *testing.T) {
	for _, provider := range []string{"gitee", "bitbucket"} {
		base := "https://gitee.com"
		if provider == "bitbucket" {
			base = "https://bitbucket.org"
		}
		reader, err := NewV3Reader(scm.ProviderConfig{Kind: scm.ProviderKind(provider), BaseURL: base, Token: "synthetic-secret"}, &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
			return nil, errors.New(req.URL.String() + " synthetic-secret")
		})}, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		out, err := reader.Read(context.Background(), v3Request(provider))
		assertIncomplete(t, out, err)
		b, _ := json.Marshal(err)
		if strings.Contains(string(b)+err.Error(), "synthetic-secret") {
			t.Fatal("secret leaked")
		}
	}
}
func TestV3PresentationFieldsExcluded(t *testing.T) {
	for _, provider := range []string{"gitee", "bitbucket"} {
		reader := v3FixtureReader(t, provider, Limits{}, func(req *http.Request, n int) *http.Response {
			resp := v3Inventory(provider, req, []wirePR{fixturePR()})
			if provider == "bitbucket" {
				resp = v3Inventory(provider, req, []bitbucketPR{bbFixture()})
			}
			if v3IsRepo(req) || provider == "gitee" && req.URL.Query().Get("page") != "1" {
				return resp
			}
			var raw []byte
			if provider == "gitee" {
				raw, _ = json.Marshal([]wirePR{fixturePR()})
			} else {
				raw, _ = json.Marshal(map[string]any{"values": []bitbucketPR{bbFixture()}})
			}
			value := fmt.Sprintf(`"title":"title-%d","author":{"name":"author-%d"},`, n, n)
			body := strings.Replace(string(raw), `"id":`, value+`"id":`, 1)
			return response(body, 200, nil)
		})
		out, err := reader.Read(context.Background(), v3Request(provider))
		if err != nil || len(out.Observations) != 1 || out.Observations[0].Title != "" || out.Observations[0].ActorName != "" {
			t.Fatalf("presentation affected inventory: %v", err)
		}
	}
}
func TestV3EmptyBindingAndResultRoundTrip(t *testing.T) {
	for _, provider := range []string{"gitee", "bitbucket"} {
		reader := v3FixtureReader(t, provider, Limits{}, func(req *http.Request, _ int) *http.Response {
			if provider == "gitee" {
				return v3Inventory(provider, req, []wirePR{fixturePR()})
			}
			return v3Inventory(provider, req, []bitbucketPR{bbFixture()})
		})
		req := v3Request(provider)
		req.ProviderProjectKey = ""
		out, err := reader.Read(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(out)
		var decoded Result
		if json.Unmarshal(b, &decoded) != nil || !reflect.DeepEqual(out, decoded) {
			t.Fatal("v3 roundtrip failed")
		}
		if strings.Contains(string(b), "synthetic-secret") {
			t.Fatal("token in evidence")
		}
	}
}

func TestV3DecoderStructuralLimitsAndDuplicates(t *testing.T) {
	for _, raw := range []string{`{"id":1,"ID":2}`, `{"values":[]} {}`, `{"values":null}`, `{"values":[{},{}]}`, `{"values":[],"next":{}}`, `{"values":[],"size":1.5}`, `{"values":[],"size":1e3}`, `{"values":[],"size":-1}`, `{"values":[],"page":0}`, `{"values":[],"pagelen":0}`, `{"values":[],"nested":` + strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66) + "}"} {
		if _, err := decodeBitbucketPage([]byte(raw), 1); err == nil {
			t.Fatalf("accepted malformed envelope: %.80s", raw)
		}
	}
	for _, provider := range []string{"gitee", "bitbucket"} {
		reader := v3FixtureReader(t, provider, Limits{MaxItems: 1}, func(req *http.Request, _ int) *http.Response {
			p1, p2 := fixturePR(), fixturePR()
			p2.ID++
			p2.Number++
			b1, b2 := bbFixture(), bbFixture()
			b2.ID++
			if provider == "gitee" {
				return v3Inventory(provider, req, []wirePR{p1, p2})
			}
			return v3Inventory(provider, req, []bitbucketPR{b1, b2})
		})
		out, err := reader.Read(context.Background(), v3Request(provider))
		assertIncomplete(t, out, err)
	}
}

func TestV3PersistedNameAndBranchBounds(t *testing.T) {
	for _, name := range []string{"owner/a..b", "owner/" + strings.Repeat("a", 101), strings.Repeat("a", 101) + "/repo", "owner.git/repo", "-owner/repo", "owner/repo.git"} {
		if validV3Repository(name) {
			t.Fatalf("invalid v3 repository %q", name)
		}
	}
	for _, ref := range []string{"@", "\u00a0main", "main\u00a0", strings.Repeat("a", 257), "feature/.hidden", "main.lock", "bad\\branch"} {
		if validV3Branch(ref) {
			t.Fatalf("invalid v3 ref %q", ref)
		}
	}
	if !validV3Repository("owner/my.repo") || !validV3Branch(strings.Repeat("a", 256)) {
		t.Fatal("valid boundary rejected")
	}
}
