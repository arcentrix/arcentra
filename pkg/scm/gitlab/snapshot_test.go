// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/pkg/scm"
	"github.com/bytedance/sonic"
)

const snapshotTestToken = "synthetic-token-never-a-live-credential"
const snapshotTestRepository = "team/deep/group/repository"
const snapshotTestSource = "0123456789abcdef0123456789abcdef01234567"
const snapshotTestMerge = "1123456789abcdef0123456789abcdef01234567"
const snapshotTestSquash = "2123456789abcdef0123456789abcdef01234567"
const snapshotTestUpdated = "2026-10-16T10:20:30.123456789Z"

func snapshotTestTime(t *testing.T, raw string) time.Time {
	t.Helper()
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func snapshotTestRequest(t *testing.T) SnapshotRequest {
	t.Helper()
	return SnapshotRequest{Repository: snapshotTestRepository, ProviderProjectID: 91, LowerBound: snapshotTestTime(t, snapshotTestUpdated), UpperBound: snapshotTestTime(t, "2026-10-16T11:00:00.123456789Z")}
}

func snapshotTestMR(id int64) map[string]any {
	return map[string]any{
		"id": id, "iid": id, "project_id": int64(91), "title": "Synthetic MR",
		"state": "opened", "sha": snapshotTestSource, "merge_commit_sha": nil, "squash_commit_sha": nil,
		"updated_at": snapshotTestUpdated, "merged_at": nil,
		"source_branch": "feature/new-source", "target_branch": "release/main", "author": map[string]any{"name": "Fixture"},
	}
}

func snapshotWrite(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	encoded, err := sonic.Marshal(value)
	if err != nil {
		t.Error(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(encoded)
}

func newSnapshotFixture(t *testing.T, limits SnapshotLimits, handler http.HandlerFunc) (*SnapshotReader, *httptest.Server) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("PRIVATE-TOKEN") != snapshotTestToken || req.Method != http.MethodGet {
			t.Error("missing synthetic token or non-read-only method")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		base := "/api/v4/projects/" + url.PathEscape(snapshotTestRepository)
		if req.URL.EscapedPath() == base {
			snapshotWrite(t, w, map[string]any{"id": 91, "path_with_namespace": snapshotTestRepository})
			return
		}
		if req.URL.EscapedPath() != base+"/merge_requests" {
			t.Errorf("unexpected endpoint %q", req.URL.EscapedPath())
			w.WriteHeader(http.StatusNotFound)
			return
		}
		handler(w, req)
	}))
	t.Cleanup(server.Close)
	reader, err := NewSnapshotReader(scm.ProviderConfig{Kind: scm.ProviderKindGitLab, BaseURL: server.URL, Token: snapshotTestToken}, server.Client(), limits)
	if err != nil {
		t.Fatal(err)
	}
	return reader, server
}

func snapshotRequireIncomplete(t *testing.T, result SnapshotResult, err error) *SnapshotError {
	t.Helper()
	if !errors.Is(err, ErrSnapshotIncomplete) {
		t.Fatalf("expected incomplete error, got %v", err)
	}
	if !reflect.DeepEqual(result, SnapshotResult{}) {
		t.Fatalf("failure returned partial result: %+v", result)
	}
	var failure *SnapshotError
	if !errors.As(err, &failure) {
		t.Fatalf("expected typed error, got %T", err)
	}
	if strings.Contains(err.Error(), snapshotTestToken) || strings.Contains(err.Error(), "response-secret") {
		t.Fatal("error leaked provider details")
	}
	return failure
}

func TestSnapshotReaderFrozenBoundsNestedNamespaceAndProvenance(t *testing.T) {
	request := snapshotTestRequest(t)
	const bigID int64 = 9223372036854775000
	var pages atomic.Int32
	reader, _ := newSnapshotFixture(t, SnapshotLimits{}, func(w http.ResponseWriter, req *http.Request) {
		pages.Add(1)
		q := req.URL.Query()
		if q.Get("updated_after") != request.LowerBound.Format(time.RFC3339Nano) || q.Get("updated_before") != request.UpperBound.Format(time.RFC3339Nano) || q.Get("scope") != "all" || q.Get("state") != "all" || q.Get("sort") != "asc" || q.Get("order_by") != "updated_at" {
			t.Errorf("query lost frozen bounds or scope: %v", q)
		}
		mr := snapshotTestMR(bigID)
		mr["iid"] = int64(17)
		w.Header().Set("X-Next-Page", "")
		snapshotWrite(t, w, []any{mr})
	})
	result, err := reader.Read(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if pages.Load() != 2 || result.Version != SnapshotVersion || len(result.Observations) != 1 || result.Repository != snapshotTestRepository || result.ProviderProjectID != 91 || result.Digest == "" || result.UpperBound != request.UpperBound {
		t.Fatalf("bad complete result: %+v, pages=%d", result, pages.Load())
	}
	mr := result.Observations[0]
	if mr.ID != bigID || mr.IID != 17 || mr.ProviderProjectID != 91 || mr.SourceSHA != snapshotTestSource || mr.MergeSHA != "" || mr.SquashSHA != "" || mr.MergedAt != nil || !mr.UpdatedAt.Equal(request.LowerBound) || mr.UpdatedAt.Nanosecond() != 123456789 || mr.SourceBranch != "feature/new-source" || mr.TargetBranch != "release/main" || !mr.DispatchEligible() || mr.ObservationID == "" {
		t.Fatalf("source evidence lost: %+v", mr)
	}
	again, err := reader.Read(context.Background(), request)
	if err != nil || !reflect.DeepEqual(again, result) {
		t.Fatalf("processing time changed stable result: %+v, %v", again, err)
	}
}

func TestSnapshotReaderBaselineAndEmptyCollection(t *testing.T) {
	request := snapshotTestRequest(t)
	request.LowerBound = time.Time{}
	request.ProviderProjectID = 0
	var calls atomic.Int32
	reader, _ := newSnapshotFixture(t, SnapshotLimits{}, func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		if req.URL.Query().Has("updated_after") {
			t.Error("baseline omitted historical inventory")
		}
		snapshotWrite(t, w, []any{})
	})
	result, err := reader.Read(context.Background(), request)
	if err != nil || result.ProviderProjectID != 91 || result.Observations == nil || len(result.Observations) != 0 || !result.LowerBound.IsZero() || calls.Load() != 2 {
		t.Fatalf("empty complete baseline failed: %+v %v", result, err)
	}
}

func TestSnapshotReaderPaginationCompletion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rows    int
		size    int
		headers bool
		link    bool
		clamp   bool
	}{
		{name: "exactly full last without headers", rows: 100, size: 100},
		{name: "short pages with omitted headers", rows: 5, size: 2},
		{name: "empty terminal after multiple full pages", rows: 6, size: 2},
		{name: "optional next page headers", rows: 5, size: 2, headers: true},
		{name: "same-origin link with omitted totals", rows: 5, size: 2, link: true},
		{name: "server page size clamp", rows: 5, size: 2, headers: true, link: true, clamp: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var pages atomic.Int32
			reader, _ := newSnapshotFixture(t, SnapshotLimits{}, func(w http.ResponseWriter, req *http.Request) {
				pages.Add(1)
				page, _ := strconv.Atoi(req.URL.Query().Get("page"))
				start := (page - 1) * tc.size
				end := min(start+tc.size, tc.rows)
				wire := []any{}
				for i := start; i < end; i++ {
					wire = append(wire, snapshotTestMR(int64(i+1)))
				}
				if tc.headers {
					w.Header().Set("X-Page", strconv.Itoa(page))
					w.Header().Set("X-Next-Page", "")
					if end < tc.rows {
						w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
					}
				}
				if tc.clamp {
					w.Header().Set("X-Per-Page", strconv.Itoa(tc.size))
				}
				if tc.link {
					u := *req.URL
					u.Scheme, u.Host = "https", req.Host
					q := u.Query()
					if tc.clamp {
						q.Set("per_page", strconv.Itoa(tc.size))
					}
					rel := "prev"
					if end < tc.rows {
						q.Set("page", strconv.Itoa(page+1))
						rel = "next"
					} else {
						q.Set("page", strconv.Itoa(max(1, page-1)))
					}
					u.RawQuery = q.Encode()
					w.Header().Set("Link", "<"+u.String()+">; rel=\""+rel+"\"")
				}
				snapshotWrite(t, w, wire)
			})
			result, err := reader.Read(context.Background(), snapshotTestRequest(t))
			if err != nil || len(result.Observations) != tc.rows {
				t.Fatalf("pagination failed: %d rows, %v", len(result.Observations), err)
			}
			wantPages := (tc.rows + tc.size - 1) / tc.size
			if !tc.headers && !tc.link {
				wantPages++
			}
			if pages.Load() != int32(2*wantPages) {
				t.Fatalf("pages=%d, want %d", pages.Load(), 2*wantPages)
			}
		})
	}
}

func TestSnapshotReaderStatesAndStableMergedIdentity(t *testing.T) {
	request := snapshotTestRequest(t)
	reader, _ := newSnapshotFixture(t, SnapshotLimits{}, func(w http.ResponseWriter, req *http.Request) { snapshotWrite(t, w, []any{}) })
	normalize := func(t *testing.T, fields map[string]any) MRSnapshot {
		t.Helper()
		body, _ := sonic.Marshal(fields)
		var wire snapshotWireMR
		if err := sonic.Unmarshal(body, &wire); err != nil {
			t.Fatal(err)
		}
		mr, err := reader.normalize(request, wire)
		if err != nil {
			t.Fatal(err)
		}
		return mr
	}
	opened := normalize(t, snapshotTestMR(1))
	for _, state := range []string{"closed", "locked"} {
		fields := snapshotTestMR(1)
		fields["state"] = state
		mr := normalize(t, fields)
		if mr.DispatchEligible() || mr.State != state || mr.ObservationID == opened.ObservationID {
			t.Fatalf("state was lost: %+v", mr)
		}
	}
	fields := snapshotTestMR(1)
	fields["state"], fields["merged_at"], fields["merge_commit_sha"], fields["squash_commit_sha"] = "merged", "2026-10-16T10:00:00.123456789Z", snapshotTestMerge, snapshotTestSquash
	merged := normalize(t, fields)
	fields["updated_at"], fields["title"] = "2026-10-16T10:55:00.000000001Z", "post-merge edit"
	later := normalize(t, fields)
	if !merged.DispatchEligible() || merged.MergeSHA != snapshotTestMerge || merged.SquashSHA != snapshotTestSquash || merged.SourceSHA != snapshotTestSource || merged.MergeIdentity == "" || merged.ObservationID != merged.MergeIdentity || later.ObservationID != merged.ObservationID || later.MergeIdentity != merged.MergeIdentity {
		t.Fatalf("merge identity changed on edit: %+v %+v", merged, later)
	}
	fields = snapshotTestMR(1)
	fields["sha"] = snapshotTestMerge
	changedSource := normalize(t, fields)
	if opened.Fingerprint == changedSource.Fingerprint || opened.ObservationID == changedSource.ObservationID {
		t.Fatal("source revision change was not identified")
	}
	fields = snapshotTestMR(1)
	fields["updated_at"] = "2026-10-16T10:20:30.123456790Z"
	changedTime := normalize(t, fields)
	if opened.Fingerprint != changedTime.Fingerprint || opened.ObservationID == changedTime.ObservationID {
		t.Fatal("exact update timestamp was not preserved in observation identity")
	}
	fields = snapshotTestMR(1)
	fields["id"], fields["iid"] = 2, 2
	if normalize(t, fields).ObservationID == opened.ObservationID {
		t.Fatal("different MRs at equal timestamp collided")
	}
}

func TestSnapshotReaderRejectsMalformedObservations(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing id", func(m map[string]any) { delete(m, "id") }},
		{"negative iid", func(m map[string]any) { m["iid"] = -1 }},
		{"wrong project", func(m map[string]any) { m["project_id"] = 92 }},
		{"missing source SHA", func(m map[string]any) { delete(m, "sha") }},
		{"abbreviated source SHA", func(m map[string]any) { m["sha"] = "123abcd" }},
		{"malformed optional SHA", func(m map[string]any) { m["merge_commit_sha"] = "123abcd" }},
		{"unknown state", func(m map[string]any) { m["state"] = "reopened" }},
		{"missing source branch", func(m map[string]any) { m["source_branch"] = "" }},
		{"invalid target branch", func(m map[string]any) { m["target_branch"] = "bad\nbranch" }},
		{"missing updated time", func(m map[string]any) { delete(m, "updated_at") }},
		{"excess precision", func(m map[string]any) { m["updated_at"] = "2026-10-16T10:20:30.1234567891Z" }},
		{"below lower bound", func(m map[string]any) { m["updated_at"] = "2026-10-16T10:20:30.123456788Z" }},
		{"above upper bound", func(m map[string]any) { m["updated_at"] = "2026-10-16T11:00:00.123456790Z" }},
		{"merge with no result", func(m map[string]any) { m["state"] = "merged"; m["merged_at"] = "2026-10-16T10:00:00Z" }},
		{"merge with no time", func(m map[string]any) { m["state"] = "merged"; m["merge_commit_sha"] = snapshotTestMerge }},
		{"merge time after update", func(m map[string]any) {
			m["state"] = "merged"
			m["merge_commit_sha"] = snapshotTestMerge
			m["merged_at"] = "2026-10-16T11:00:00Z"
		}},
		{"unmerged with merge time", func(m map[string]any) { m["merged_at"] = "2026-10-16T10:00:00Z" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader, _ := newSnapshotFixture(t, SnapshotLimits{}, func(w http.ResponseWriter, req *http.Request) {
				m := snapshotTestMR(1)
				tc.mutate(m)
				w.Header().Set("X-Next-Page", "")
				snapshotWrite(t, w, []any{m})
			})
			result, err := reader.Read(context.Background(), snapshotTestRequest(t))
			snapshotRequireIncomplete(t, result, err)
		})
	}
	for _, body := range []string{"null", `{}`, `[`, `[null]`, `[{"id":1.5}]`, `[] []`, `[{"id":9223372036854775808}]`, string([]byte{'[', '"', 0xff, '"', ']'})} {
		t.Run("invalid JSON "+strconv.Itoa(len(body))+body[:min(3, len(body))], func(t *testing.T) {
			reader, _ := newSnapshotFixture(t, SnapshotLimits{}, func(w http.ResponseWriter, req *http.Request) { _, _ = w.Write([]byte(body)) })
			result, err := reader.Read(context.Background(), snapshotTestRequest(t))
			snapshotRequireIncomplete(t, result, err)
		})
	}
}

func TestSnapshotReaderRejectsUnsafeAndContradictoryPagination(t *testing.T) {
	cases := []struct {
		name   string
		change func(http.Header, *url.URL)
	}{
		{"repeated page", func(h http.Header, u *url.URL) { h.Set("X-Next-Page", "1") }},
		{"skipped page", func(h http.Header, u *url.URL) { h.Set("X-Next-Page", "3") }},
		{"wrong current page", func(h http.Header, u *url.URL) { h.Set("X-Page", "2") }},
		{"wrong previous page", func(h http.Header, u *url.URL) { h.Set("X-Prev-Page", "1") }},
		{"duplicate header", func(h http.Header, u *url.URL) { h.Add("X-Page", "1"); h.Add("X-Page", "1") }},
		{"invalid total", func(h http.Header, u *url.URL) { h.Set("X-Total", "-1") }},
		{"incomplete total", func(h http.Header, u *url.URL) { h.Set("X-Total", "2"); h.Set("X-Next-Page", "") }},
		{"contradictory count pages", func(h http.Header, u *url.URL) {
			h.Set("X-Total", "1")
			h.Set("X-Total-Pages", "2")
			h.Set("X-Per-Page", "100")
		}},
		{"unexpected per page", func(h http.Header, u *url.URL) { h.Set("X-Per-Page", "101") }},
		{"cross origin", func(h http.Header, u *url.URL) {
			u.Host = "other.example"
			h.Set("Link", "<"+u.String()+">; rel=\"next\"")
		}},
		{"scheme downgrade", func(h http.Header, u *url.URL) { u.Scheme = "http"; h.Set("Link", "<"+u.String()+">; rel=\"next\"") }},
		{"different repository", func(h http.Header, u *url.URL) {
			u.Path = "/api/v4/projects/other/merge_requests"
			u.RawPath = ""
			h.Set("Link", "<"+u.String()+">; rel=\"next\"")
		}},
		{"userinfo", func(h http.Header, u *url.URL) {
			u.User = url.UserPassword("user", "response-secret")
			h.Set("Link", "<"+u.String()+">; rel=\"next\"")
		}},
		{"removed bounds", func(h http.Header, u *url.URL) {
			q := u.Query()
			q.Del("updated_after")
			u.RawQuery = q.Encode()
			h.Set("Link", "<"+u.String()+">; rel=\"next\"")
		}},
		{"added query", func(h http.Header, u *url.URL) {
			q := u.Query()
			q.Set("search", "unexpected")
			u.RawQuery = q.Encode()
			h.Set("Link", "<"+u.String()+">; rel=\"next\"")
		}},
		{"duplicate page query", func(h http.Header, u *url.URL) {
			u.RawQuery += "&page=2"
			h.Set("Link", "<"+u.String()+">; rel=\"next\"")
		}},
		{"duplicate next links", func(h http.Header, u *url.URL) {
			link := "<" + u.String() + ">; rel=\"next\""
			h.Set("Link", link+","+link)
		}},
		{"malformed link", func(h http.Header, u *url.URL) { h.Set("Link", "malformed") }},
		{"disagreeing headers", func(h http.Header, u *url.URL) {
			h.Set("X-Next-Page", "")
			h.Set("Link", "<"+u.String()+">; rel=\"next\"")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			reader, _ := newSnapshotFixture(t, SnapshotLimits{}, func(w http.ResponseWriter, req *http.Request) {
				calls.Add(1)
				u := *req.URL
				u.Scheme, u.Host = "https", req.Host
				q := u.Query()
				q.Set("page", "2")
				u.RawQuery = q.Encode()
				tc.change(w.Header(), &u)
				snapshotWrite(t, w, []any{snapshotTestMR(1)})
			})
			result, err := reader.Read(context.Background(), snapshotTestRequest(t))
			snapshotRequireIncomplete(t, result, err)
			if calls.Load() != 1 {
				t.Fatalf("unsafe continuation requested %d pages", calls.Load())
			}
		})
	}
}

func TestSnapshotReaderDetectsPaginationMovement(t *testing.T) {
	for _, kind := range []string{"duplicate", "decreasing update", "changed totals", "second pass changed"} {
		t.Run(kind, func(t *testing.T) {
			var pass atomic.Int32
			reader, _ := newSnapshotFixture(t, SnapshotLimits{}, func(w http.ResponseWriter, req *http.Request) {
				page, _ := strconv.Atoi(req.URL.Query().Get("page"))
				if page == 1 {
					pass.Add(1)
				}
				w.Header().Set("X-Next-Page", "")
				if page == 1 {
					w.Header().Set("X-Next-Page", "2")
				}
				mr := snapshotTestMR(int64(page))
				switch kind {
				case "duplicate":
					if page == 2 {
						mr = snapshotTestMR(1)
					}
				case "decreasing update":
					if page == 1 {
						mr["updated_at"] = "2026-10-16T10:30:00Z"
					}
				case "changed totals":
					w.Header().Set("X-Total", strconv.Itoa(1+page))
				case "second pass changed":
					if pass.Load() == 2 && page == 2 {
						mr["sha"] = snapshotTestMerge
					}
				}
				snapshotWrite(t, w, []any{mr})
			})
			result, err := reader.Read(context.Background(), snapshotTestRequest(t))
			snapshotRequireIncomplete(t, result, err)
		})
	}
}

func TestSnapshotReaderNoPartialSuccessOnBudgetsOrFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits SnapshotLimits
		status int
		body   string
	}{
		{name: "item bound", limits: SnapshotLimits{MaxItems: 1}},
		{name: "page bound including verification pass", limits: SnapshotLimits{MaxPages: 3}},
		{name: "body bound", limits: SnapshotLimits{MaxResponseBytes: 200}},
		{name: "aggregate byte bound", limits: SnapshotLimits{MaxTotalBytes: 1000}},
		{name: "late provider failure", status: 500},
		{name: "late malformed page", body: "["},
		{name: "not modified is not complete", status: 304},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader, _ := newSnapshotFixture(t, tc.limits, func(w http.ResponseWriter, req *http.Request) {
				page := req.URL.Query().Get("page")
				if page == "2" && tc.status != 0 {
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte("response-secret " + snapshotTestToken))
					return
				}
				if page == "2" && tc.body != "" {
					_, _ = w.Write([]byte(tc.body))
					return
				}
				w.Header().Set("X-Next-Page", "")
				if page == "1" {
					w.Header().Set("X-Next-Page", "2")
				}
				id, _ := strconv.ParseInt(page, 10, 64)
				snapshotWrite(t, w, []any{snapshotTestMR(id)})
			})
			result, err := reader.Read(context.Background(), snapshotTestRequest(t))
			snapshotRequireIncomplete(t, result, err)
		})
	}
}

func TestSnapshotReaderRateLimitKeepsRetryMetadata(t *testing.T) {
	before := time.Now()
	reader, _ := newSnapshotFixture(t, SnapshotLimits{}, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Get("page") == "1" {
			w.Header().Set("X-Next-Page", "2")
			snapshotWrite(t, w, []any{snapshotTestMR(1)})
			return
		}
		w.Header().Set("Retry-After", "120")
		w.Header().Set("RateLimit-Remaining", "0")
		w.Header().Set("RateLimit-Reset", "1792141320")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("response-secret"))
	})
	result, err := reader.Read(context.Background(), snapshotTestRequest(t))
	failure := snapshotRequireIncomplete(t, result, err)
	if failure.StatusCode != 429 || failure.RetryAt.Before(before.Add(120*time.Second)) || failure.RetryAt.After(time.Now().Add(120*time.Second)) || failure.RateLimitRemaining == nil || *failure.RateLimitRemaining != 0 || failure.RateLimitResetAt.Unix() != 1792141320 {
		t.Fatalf("rate limit metadata lost: %+v", failure)
	}
}

func TestSnapshotReaderRedirectNeverSendsCredentialsToTarget(t *testing.T) {
	var reached atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { reached.Add(1); snapshotWrite(t, w, []any{}) }))
	defer target.Close()
	reader, _ := newSnapshotFixture(t, SnapshotLimits{}, func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, target.URL+"/response-secret", http.StatusFound)
	})
	result, err := reader.Read(context.Background(), snapshotTestRequest(t))
	failure := snapshotRequireIncomplete(t, result, err)
	if failure.StatusCode != 302 || reached.Load() != 0 {
		t.Fatalf("redirect followed, target calls %d", reached.Load())
	}
}

func TestSnapshotReaderTimeoutAndCancellationReturnNoPartialSuccess(t *testing.T) {
	reader, _ := newSnapshotFixture(t, SnapshotLimits{RequestTimeout: 40 * time.Millisecond}, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Get("page") == "1" {
			w.Header().Set("X-Next-Page", "2")
			snapshotWrite(t, w, []any{snapshotTestMR(1)})
			return
		}
		<-req.Context().Done()
	})
	result, err := reader.Read(context.Background(), snapshotTestRequest(t))
	snapshotRequireIncomplete(t, result, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = reader.Read(ctx, snapshotTestRequest(t))
	snapshotRequireIncomplete(t, result, err)
}

func TestSnapshotReaderProjectIdentityRecheckedEvenWhenEmpty(t *testing.T) {
	for _, kind := range []string{"requested id mismatch", "moved project", "renamed project"} {
		t.Run(kind, func(t *testing.T) {
			var identityCalls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if strings.HasSuffix(req.URL.Path, "/merge_requests") {
					snapshotWrite(t, w, []any{})
					return
				}
				call := identityCalls.Add(1)
				id, repository := 91, snapshotTestRepository
				if kind == "requested id mismatch" || (kind == "moved project" && call == 2) {
					id = 92
				}
				if kind == "renamed project" && call == 2 {
					repository = "other/repository"
				}
				snapshotWrite(t, w, map[string]any{"id": id, "path_with_namespace": repository})
			}))
			defer server.Close()
			reader, err := NewSnapshotReader(scm.ProviderConfig{Kind: scm.ProviderKindGitLab, BaseURL: server.URL, Token: snapshotTestToken}, server.Client(), SnapshotLimits{})
			if err != nil {
				t.Fatal(err)
			}
			result, err := reader.Read(context.Background(), snapshotTestRequest(t))
			snapshotRequireIncomplete(t, result, err)
		})
	}
}

func TestSnapshotReaderRejectsUnsafeConfigurationAndRequests(t *testing.T) {
	for _, cfg := range []scm.ProviderConfig{
		{BaseURL: "https://gitlab.example", Token: snapshotTestToken},
		{Kind: scm.ProviderKindGitHub, BaseURL: "https://gitlab.example", Token: snapshotTestToken},
		{Kind: scm.ProviderKindGitLab, Token: snapshotTestToken},
		{Kind: scm.ProviderKindGitLab, BaseURL: "http://gitlab.example", Token: snapshotTestToken},
		{Kind: scm.ProviderKindGitLab, BaseURL: "https://user:response-secret@gitlab.example", Token: snapshotTestToken},
		{Kind: scm.ProviderKindGitLab, BaseURL: "https://gitlab.example?secret=value", Token: snapshotTestToken},
		{Kind: scm.ProviderKindGitLab, BaseURL: "https://gitlab.example/../other", Token: snapshotTestToken},
		{Kind: scm.ProviderKindGitLab, BaseURL: "https://gitlab.example", APIBaseURL: "https://other.example/api/v4", Token: snapshotTestToken},
		{Kind: scm.ProviderKindGitLab, BaseURL: "https://gitlab.example", APIBaseURL: "https://gitlab.example/unrelated", Token: snapshotTestToken},
		{Kind: scm.ProviderKindGitLab, BaseURL: "https://gitlab.example", Token: "bad\nheader"},
	} {
		if _, err := NewSnapshotReader(cfg, nil, SnapshotLimits{}); !errors.Is(err, ErrSnapshotIncomplete) {
			t.Fatalf("accepted unsafe config: %+v", cfg)
		}
	}
	reader, _ := newSnapshotFixture(t, SnapshotLimits{}, func(w http.ResponseWriter, req *http.Request) { t.Error("invalid request made provider call") })
	for _, repo := range []string{"single", "group/../repo", "group//repo", "group/repo?query", "https://other.example/repo", "group/repo%2fother", "group/repo.git", "group/repo#fragment"} {
		req := snapshotTestRequest(t)
		req.Repository = repo
		result, err := reader.Read(context.Background(), req)
		snapshotRequireIncomplete(t, result, err)
	}
	for _, mutate := range []func(*SnapshotRequest){func(r *SnapshotRequest) { r.UpperBound = time.Time{} }, func(r *SnapshotRequest) { r.LowerBound = r.UpperBound.Add(time.Second) }, func(r *SnapshotRequest) { r.ProviderProjectID = -1 }} {
		req := snapshotTestRequest(t)
		mutate(&req)
		result, err := reader.Read(context.Background(), req)
		snapshotRequireIncomplete(t, result, err)
	}
}

func TestSnapshotReaderConcurrentReadsHaveIndependentBudgets(t *testing.T) {
	reader, _ := newSnapshotFixture(t, SnapshotLimits{MaxPages: 2}, func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("X-Next-Page", "")
		snapshotWrite(t, w, []any{snapshotTestMR(1)})
	})
	for i := 0; i < 8; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			result, err := reader.Read(context.Background(), snapshotTestRequest(t))
			if err != nil || len(result.Observations) != 1 {
				t.Fatalf("concurrent scan failed: %+v %v", result, err)
			}
		})
	}
}

func TestSnapshotReaderPublicProjectOmitsTokenAndRejectsUnauthorized(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if _, exists := req.Header["Private-Token"]; exists {
					t.Error("public project sent an empty token header")
				}
				if status != http.StatusOK {
					w.WriteHeader(status)
					_, _ = w.Write([]byte("response-secret"))
					return
				}
				if strings.HasSuffix(req.URL.Path, "/merge_requests") {
					snapshotWrite(t, w, []any{})
					return
				}
				snapshotWrite(t, w, map[string]any{"id": 91, "path_with_namespace": snapshotTestRepository})
			}))
			defer server.Close()
			reader, err := NewSnapshotReader(scm.ProviderConfig{Kind: scm.ProviderKindGitLab, BaseURL: server.URL}, server.Client(), SnapshotLimits{})
			if err != nil {
				t.Fatal(err)
			}
			result, err := reader.Read(context.Background(), snapshotTestRequest(t))
			if status == http.StatusOK {
				if err != nil || result.ProviderProjectID != 91 {
					t.Fatalf("public read failed: %+v %v", result, err)
				}
			} else {
				failure := snapshotRequireIncomplete(t, result, err)
				if failure.StatusCode != status || !strings.Contains(failure.Reason, "authentication") {
					t.Fatalf("missing authentication blocker: %+v", failure)
				}
			}
		})
	}
}

func TestSnapshotReaderStrictTransportBounds(t *testing.T) {
	for _, kind := range []string{"oversized header", "unsupported compression", "truncated body"} {
		t.Run(kind, func(t *testing.T) {
			reader, _ := newSnapshotFixture(t, SnapshotLimits{}, func(w http.ResponseWriter, req *http.Request) {
				switch kind {
				case "oversized header":
					w.Header().Set("X-Large", strings.Repeat("x", 33<<10))
				case "unsupported compression":
					w.Header().Set("Content-Encoding", "gzip")
				case "truncated body":
					w.Header().Set("Content-Length", "9999")
				}
				snapshotWrite(t, w, []any{snapshotTestMR(1)})
			})
			result, err := reader.Read(context.Background(), snapshotTestRequest(t))
			snapshotRequireIncomplete(t, result, err)
		})
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { t.Error("unexpected transport request") }))
	defer server.Close()
	client := server.Client()
	transport := client.Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.InsecureSkipVerify = true
	client.Transport = transport
	if _, err := NewSnapshotReader(scm.ProviderConfig{Kind: scm.ProviderKindGitLab, BaseURL: server.URL}, client, SnapshotLimits{}); !errors.Is(err, ErrSnapshotIncomplete) {
		t.Fatalf("accepted insecure TLS client: %v", err)
	}
}

func TestSnapshotReaderTimestampPrecisionAndOffsetValidation(t *testing.T) {
	for _, raw := range []string{"2026-10-16T1:20:30Z", "2026-10-16T10:20:30,123Z", "2026-10-16T10:20:30.1234567891Z", "2026-10-16T10:20:30+24:00", "2026-10-16T10:20:30+01:60", "0000-01-01T00:00:00Z"} {
		if _, err := snapshotParseTime(raw); err == nil {
			t.Fatalf("accepted malformed time %q", raw)
		}
	}
	utc, err := snapshotParseTime(snapshotTestUpdated)
	if err != nil {
		t.Fatal(err)
	}
	offset, err := snapshotParseTime("2026-10-16T11:20:30.123456789+01:00")
	if err != nil || offset != utc {
		t.Fatalf("equivalent offset was not preserved: %s %v", offset, err)
	}
}

func TestSnapshotReaderInclusiveUpperBoundary(t *testing.T) {
	req := snapshotTestRequest(t)
	reader, _ := newSnapshotFixture(t, SnapshotLimits{}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Next-Page", "")
		mr := snapshotTestMR(2)
		mr["updated_at"] = req.UpperBound.Format(time.RFC3339Nano)
		snapshotWrite(t, w, []any{snapshotTestMR(1), mr})
	})
	result, err := reader.Read(context.Background(), req)
	if err != nil || len(result.Observations) != 2 || !result.Observations[1].UpdatedAt.Equal(req.UpperBound) {
		t.Fatalf("upper boundary excluded: %+v %v", result, err)
	}
}
