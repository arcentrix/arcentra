// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package pollsnapshot

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"testing"
)

func TestPaginationRejectsAdversarialMetadata(t *testing.T) {
	base, _ := url.Parse("https://api.github.com/repos/owner/repo/pulls?direction=asc&page=1&per_page=100&sort=created&state=all")
	link := func(raw, rel string) http.Header { return http.Header{"Link": {"<" + raw + ">; rel=\"" + rel + "\""}} }
	for name, h := range map[string]http.Header{
		"foreign_origin":      link("https://evil.example/secret?direction=asc&page=2&per_page=100&sort=created&state=all", "next"),
		"foreign_path":        link("https://api.github.com/repos/other/repo/pulls?direction=asc&page=2&per_page=100&sort=created&state=all", "next"),
		"user_info":           link("https://secret@api.github.com/repos/owner/repo/pulls?direction=asc&page=2&per_page=100&sort=created&state=all", "next"),
		"changed_filter":      link("?direction=asc&page=2&per_page=100&sort=updated&state=all", "next"),
		"duplicate_page":      link("?direction=asc&page=2&page=3&per_page=100&sort=created&state=all", "next"),
		"skip_page":           link("?direction=asc&page=3&per_page=100&sort=created&state=all", "next"),
		"missing_next":        link("?direction=asc&page=3&per_page=100&sort=created&state=all", "last"),
		"wrong_first":         link("?direction=asc&page=2&per_page=100&sort=created&state=all", "first"),
		"invalid_rel":         link("?direction=asc&page=2&per_page=100&sort=created&state=all", "secret"),
		"duplicate_total":     {"X-Total": {"1", "1"}},
		"contradictory_total": {"X-Total": {"1"}, "X-Total-Count": {"2"}},
		"invalid_total":       {"X-Total-Count": {"-1"}},
		"changing_page":       {"X-Page": {"2"}},
		"oversized_perpage":   {"X-Per-Page": {"101"}},
		"false_terminal":      {"X-Next-Page": {""}, "X-Total-Count": {"2"}},
		"contradictory_next":  {"X-Next-Page": {""}, "Link": {"<?direction=asc&page=2&per_page=100&sort=created&state=all>; rel=\"next\""}},
	} {
		t.Run(name, func(t *testing.T) {
			meta := snapshotPagination{total: -1, totalPages: -1, perPage: -1}
			if _, err := meta.next(h, base, 1, 1, 1, 100); err == nil {
				t.Fatal("unsafe metadata accepted")
			}
		})
	}
}
func TestPaginationMultiPageAndClampedShortPages(t *testing.T) {
	for _, provider := range []string{"github", "gitea"} {
		for _, links := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s_links_%t", provider, links), func(t *testing.T) {
				calls := 0
				r := fixtureReader(t, provider, Limits{}, func(req *http.Request, n int) *http.Response {
					calls = n
					if req.URL.Query().Get("page") == "" {
						return inventoryResponse(req, fixturePR())
					}
					page, _ := strconv.Atoi(req.URL.Query().Get("page"))
					if page == 3 {
						return response("[]", 200, nil)
					}
					pr := fixturePR()
					pr.ID += int64(page)
					pr.Number += int64(page)
					resp := inventoryResponse(req, pr)
					if page == 2 {
						q := req.URL.Query()
						q.Set("page", "1")
						copyReq := req.Clone(req.Context())
						u := *req.URL
						u.RawQuery = q.Encode()
						copyReq.URL = &u
						resp = inventoryResponse(copyReq, pr)
					}
					if links {
						u := *req.URL
						q := u.Query()
						rel := "next"
						p := 2
						if page == 2 {
							rel = "prev"
							p = 1
						}
						q.Set("page", strconv.Itoa(p))
						u.RawQuery = q.Encode()
						resp.Header.Set("Link", "<"+u.String()+">; rel=\""+rel+"\"")
					}
					return resp
				})
				out, err := r.Read(context.Background(), fixtureRequest())
				if err != nil || len(out.Observations) != 2 {
					t.Fatalf("%+v %v", out, err)
				}
				expected := 8
				if links {
					expected = 6
				}
				if calls != expected {
					t.Fatalf("calls=%d want=%d", calls, expected)
				}
			})
		}
	}
}
func TestPaginationMovingTotalsAndEmptyWithNext(t *testing.T) {
	u, _ := url.Parse("https://forge.example/api/v1/repos/o/r/pulls?page=1&limit=100&sort=oldest&state=all")
	m := snapshotPagination{total: -1, totalPages: -1, perPage: -1}
	if more, err := m.next(http.Header{"X-Total-Count": {"2"}}, u, 1, 1, 1, 100); !more || err != nil {
		t.Fatal(more, err)
	}
	if _, err := m.next(http.Header{"X-Total-Count": {"3"}}, u, 2, 1, 2, 100); err == nil {
		t.Fatal("moving total accepted")
	}
	m = snapshotPagination{total: -1, totalPages: -1, perPage: -1}
	if _, err := m.next(http.Header{"X-Next-Page": {"2"}}, u, 1, 0, 0, 100); err == nil {
		t.Fatal("empty with next accepted")
	}
}

// GitHub returns canonical numeric paths even for a named repository request.
// https://docs.github.com/en/rest/using-the-rest-api/using-pagination-in-the-rest-api
// Public API response independently verified on 2026-10-19 used this shape for
// /repos/octocat/Hello-World/pulls and canonical /repositories/1296269/pulls.
func TestGitHubCanonicalRepositoryLinks(t *testing.T) {
	for _, mode := range []string{"valid", "wrong_id", "foreign_origin", "foreign_path", "leading_zero_id", "escaped_path", "changed_query"} {
		t.Run(mode, func(t *testing.T) {
			r := fixtureReader(t, "github", Limits{}, func(req *http.Request, _ int) *http.Response {
				if req.URL.Query().Get("page") == "" {
					return inventoryResponse(req, fixturePR())
				}
				if req.URL.Path != "/repos/owner/repo/pulls" {
					t.Fatal("reader followed provider link instead of reconstructing endpoint")
				}
				page, _ := strconv.Atoi(req.URL.Query().Get("page"))
				pr := fixturePR()
				pr.ID += int64(page)
				pr.Number += int64(page)
				copyReq := req.Clone(req.Context())
				u := *req.URL
				q := u.Query()
				q.Set("page", "1")
				u.RawQuery = q.Encode()
				copyReq.URL = &u
				resp := inventoryResponse(copyReq, pr)
				target := *req.URL
				target.Path = "/repositories/11/pulls"
				q = target.Query()
				relation := "next"
				destination := 2
				if page == 2 {
					relation = "prev"
					destination = 1
				}
				q.Set("page", strconv.Itoa(destination))
				switch mode {
				case "wrong_id":
					target.Path = "/repositories/12/pulls"
				case "foreign_origin":
					target.Host = "evil.example"
				case "foreign_path":
					target.Path = "/repositories/11/issues"
				case "leading_zero_id":
					target.Path = "/repositories/011/pulls"
				case "escaped_path":
					target.RawPath = "/repositories/%31%31/pulls"
				case "changed_query":
					q.Set("state", "open")
				}
				target.RawQuery = q.Encode()
				resp.Header.Set("Link", "<"+target.String()+">; rel=\""+relation+"\"")
				return resp
			})
			out, err := r.Read(context.Background(), fixtureRequest())
			if mode == "valid" {
				if err != nil || len(out.Observations) != 2 {
					t.Fatalf("real-shaped canonical pagination failed: %+v %v", out, err)
				}
			} else {
				assertIncomplete(t, out, err)
			}
		})
	}
}
func TestGiteaCannotUseGitHubCanonicalPath(t *testing.T) {
	r := fixtureReader(t, "gitea", Limits{}, func(req *http.Request, _ int) *http.Response {
		resp := inventoryResponse(req, fixturePR())
		if req.URL.Query().Get("page") != "" {
			target := *req.URL
			target.Path = "/repositories/11/pulls"
			q := target.Query()
			q.Set("page", "2")
			target.RawQuery = q.Encode()
			resp.Header.Set("Link", "<"+target.String()+">; rel=\"next\"")
		}
		return resp
	})
	out, err := r.Read(context.Background(), fixtureRequest())
	assertIncomplete(t, out, err)
}
