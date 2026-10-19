// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package pollsnapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func giteeTestLink(req *http.Request, page int, relation string, echo bool) string {
	u := *req.URL
	q := u.Query()
	q.Set("page", strconv.Itoa(page))
	if echo {
		q.Set("owner", "owner")
		q.Set("repo", "repo")
		q.Set("source_repo_namespace", "")
		q.Set("source_branch", "")
		q.Set("target_branch", "")
	}
	u.RawQuery = q.Encode()
	return "<" + u.String() + ">; rel='" + relation + "'"
}
func TestV3GiteePaginationCompletion(t *testing.T) {
	for _, mode := range []string{"sentinel", "short_nonterminal", "full_final_total", "single_quote_link", "link_and_totals", "owner_namespace_echo", "empty"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			reader := v3FixtureReader(t, "gitee", Limits{PageSize: 2}, func(req *http.Request, _ int) *http.Response {
				if v3IsRepo(req) {
					return v3RepoResponse("gitee")
				}
				calls++
				if req.URL.Query().Has("source_repo_namespace") {
					t.Fatal("generated echo leaked into inventory filter")
				}
				page, _ := strconv.Atoi(req.URL.Query().Get("page"))
				p := fixturePR()
				p.Merged = nil
				rows := []wirePR{}
				count := 1
				if mode == "full_final_total" {
					count = 2
				}
				if mode == "empty" {
					count = 0
				}
				maxPage := 1
				if mode == "short_nonterminal" || mode == "single_quote_link" || mode == "link_and_totals" || mode == "owner_namespace_echo" {
					maxPage = 2
				}
				if page <= maxPage {
					for i := 0; i < count; i++ {
						p.ID = int64(page*10 + i)
						p.Number = p.ID
						rows = append(rows, p)
					}
				}
				h := http.Header{}
				if mode == "full_final_total" {
					h.Set("total_count", "2")
					h.Set("total_page", "1")
				}
				if mode == "single_quote_link" || mode == "link_and_totals" || mode == "owner_namespace_echo" {
					link := giteeTestLink(req, maxPage, "last", true)
					if page < maxPage {
						link = giteeTestLink(req, page+1, "next", true) + ", " + link
					}
					if mode == "owner_namespace_echo" {
						link = strings.ReplaceAll(link, "source_repo_namespace=", "source_repo_namespace=owner")
					}
					h.Set("Link", link)
				}
				if mode == "link_and_totals" {
					h.Set("total_count", "2")
					h.Set("total_page", "2")
				}
				b, _ := json.Marshal(rows)
				return response(string(b), 200, h)
			})
			out, err := reader.Read(context.Background(), v3Request("gitee"))
			if err != nil {
				t.Fatal(err)
			}
			expected := 1
			if mode == "short_nonterminal" || mode == "single_quote_link" || mode == "link_and_totals" || mode == "owner_namespace_echo" || mode == "full_final_total" {
				expected = 2
			}
			if mode == "empty" {
				expected = 0
			}
			if len(out.Observations) != expected {
				t.Fatal("lost rows")
			}
			if mode == "full_final_total" && calls != 2 {
				t.Fatal("full final page was not terminal")
			}
		})
	}
}
func TestV3GiteePoisonedPaginationZeroResult(t *testing.T) {
	for _, mode := range []string{"duplicate_total", "changed_total", "total_mismatch", "page_total_mismatch", "source_namespace", "source_branch", "target_branch", "owner", "repo", "foreign_token", "duplicate_token", "unknown_filter", "duplicate_relation", "cross_origin", "cross_repo", "encoded_path", "bad_next", "last_disagrees", "empty_with_next", "loop", "limit"} {
		t.Run(mode, func(t *testing.T) {
			limits := Limits{PageSize: 1}
			if mode == "limit" {
				limits.MaxPages = 3
			}
			reader := v3FixtureReader(t, "gitee", limits, func(req *http.Request, _ int) *http.Response {
				if v3IsRepo(req) {
					return v3RepoResponse("gitee")
				}
				page, _ := strconv.Atoi(req.URL.Query().Get("page"))
				p := fixturePR()
				p.ID = int64(page)
				p.Number = p.ID
				body, _ := json.Marshal([]wirePR{p})
				h := http.Header{}
				h.Set("Link", giteeTestLink(req, page+1, "next", true))
				switch mode {
				case "duplicate_total":
					h.Add("total_count", "2")
					h.Add("total_count", "2")
				case "changed_total":
					h.Set("total_count", strconv.Itoa(4+page))
				case "total_mismatch":
					h.Set("total_count", "0")
				case "page_total_mismatch":
					h.Set("total_page", "1")
				case "duplicate_relation":
					h.Add("Link", giteeTestLink(req, page+1, "next", true))
				case "bad_next":
					h.Set("Link", giteeTestLink(req, page+2, "next", true))
				case "last_disagrees":
					h.Add("Link", giteeTestLink(req, page, "last", true))
				case "empty_with_next":
					body = []byte(`[]`)
				case "loop":
					p.ID = 1
					p.Number = 1
					body, _ = json.Marshal([]wirePR{p})
				case "cross_origin":
					h.Set("Link", strings.Replace(h.Get("Link"), "gitee.com", "evil.example", 1))
				case "cross_repo":
					h.Set("Link", strings.Replace(h.Get("Link"), "/owner/repo/", "/owner/other/", 1))
				case "encoded_path":
					h.Set("Link", strings.Replace(h.Get("Link"), "/pulls?", "/%70ulls?", 1))
				case "limit":
				default:
					link := h.Get("Link")
					end := strings.Index(link, ">")
					u, _ := url.Parse(link[1:end])
					q := u.Query()
					key := mode
					value := "poison"
					switch mode {
					case "source_namespace":
						key = "source_repo_namespace"
					case "foreign_token", "duplicate_token":
						key = "access_token"
					case "unknown_filter":
						key = "since"
					}
					if mode == "duplicate_token" {
						q.Add(key, "synthetic-secret")
					} else {
						q.Set(key, value)
					}
					u.RawQuery = q.Encode()
					h.Set("Link", "<"+u.String()+">; rel='next'")
				}
				return response(string(body), 200, h)
			})
			out, err := reader.Read(context.Background(), v3Request("gitee"))
			assertIncomplete(t, out, err)
			if strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatal("token leaked")
			}
		})
	}
}
func TestV3BitbucketOpaqueNextAndOptionalMetadata(t *testing.T) {
	for _, metadata := range []bool{false, true} {
		t.Run(fmt.Sprint(metadata), func(t *testing.T) {
			calls := 0
			reader := v3FixtureReader(t, "bitbucket", Limits{PageSize: 1}, func(req *http.Request, _ int) *http.Response {
				if v3IsRepo(req) {
					return v3RepoResponse("bitbucket")
				}
				calls++
				q := req.URL.Query()
				firstPage := q.Get("page") == ""
				if len(q["state"]) != 4 || q.Get("sort") != "created_on" || q.Get("state") == "ALL" {
					t.Fatal("wrong inventory filters")
				}
				p := bbFixture()
				body := map[string]any{}
				if firstPage {
					u := *req.URL
					q.Set("page", "opaque-cursor_A.9~")
					u.RawQuery = q.Encode()
					body["next"] = u.String()
				} else {
					if q.Get("page") != "opaque-cursor_A.9~" {
						t.Fatal("constructed numeric next")
					}
					p.ID++
				}
				body["values"] = []bitbucketPR{p}
				if metadata {
					body["size"] = 2
					body["pagelen"] = 1
					if firstPage {
						body["page"] = 1
					} else {
						body["page"] = 2
					}
				}
				b, _ := json.Marshal(body)
				return response(string(b), 200, nil)
			})
			out, err := reader.Read(context.Background(), v3Request("bitbucket"))
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Observations) != 2 || calls != 4 {
				t.Fatal("incomplete opaque pagination")
			}
		})
	}
}
func TestV3BitbucketContinuationAndMetadataPoisonZeroResult(t *testing.T) {
	modes := []string{"origin", "port", "userinfo", "fragment", "path", "encoded_path", "filter", "missing_filter", "duplicate_filter", "state_all", "sort", "pagelen", "fields", "unknown_cursor", "oversized_cursor", "encoded_cursor", "loop", "size", "changed_size", "page", "duplicate", "empty_next", "null_next", "missing_values", "empty_with_next", "size_terminal", "oversized_page", "null_size", "missing_hash"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			reader := v3FixtureReader(t, "bitbucket", Limits{PageSize: 1}, func(req *http.Request, _ int) *http.Response {
				if v3IsRepo(req) {
					return v3RepoResponse("bitbucket")
				}
				p := bbFixture()
				u := *req.URL
				q := u.Query()
				old := q.Get("page")
				q.Set("page", "next-token")
				if old != "" {
					p.ID++
					q.Set("page", "last-token")
				}
				body := map[string]any{"values": []bitbucketPR{p}}
				switch mode {
				case "origin":
					u.Host = "evil.example"
				case "port":
					u.Host += " :443"
					u.Host = strings.ReplaceAll(u.Host, " ", "")
				case "userinfo":
					u.User = url.User("user")
				case "fragment":
					u.Fragment = "secret"
				case "path":
					u.Path = strings.ReplaceAll(u.Path, "owner/repo", "owner/other")
				case "encoded_path":
					u.RawPath = strings.Replace(u.Path, "pullrequests", "%70ullrequests", 1)
				case "filter":
					q.Set("q", "source.repository.full_name=evil")
				case "missing_filter":
					q.Del("state")
				case "duplicate_filter":
					q.Add("sort", "created_on")
				case "state_all":
					q.Set("state", "ALL")
				case "sort":
					q.Set("sort", "-created_on")
				case "pagelen":
					q.Set("pagelen", "100")
				case "fields":
					q.Set("fields", "values")
				case "unknown_cursor":
					q.Del("page")
					q.Set("cursor", "token")
				case "oversized_cursor":
					q.Set("page", strings.Repeat("a", 513))
				case "encoded_cursor":
					q.Set("page", "%2F")
				case "loop":
					q.Set("page", "same-token")
				case "size":
					body["size"] = 0
				case "changed_size":
					body["size"] = 3
					if old != "" {
						body["size"] = 4
					}
				case "page":
					body["page"] = 9
				case "duplicate":
					p.ID = 7
					body["values"] = []bitbucketPR{p}
				case "missing_values":
					delete(body, "values")
				case "empty_with_next":
					body["values"] = []bitbucketPR{}
				case "size_terminal":
					body["size"] = 2
				case "oversized_page":
					body["values"] = []bitbucketPR{p, p}
				case "null_size":
					body["size"] = nil
				case "missing_hash":
					p.State = "MERGED"
					body["values"] = []bitbucketPR{p}
				}
				u.RawQuery = q.Encode()
				body["next"] = u.String()
				if mode == "empty_next" {
					body["next"] = ""
				}
				if mode == "null_next" {
					body["next"] = nil
				}
				if mode == "size_terminal" {
					delete(body, "next")
				}
				b, _ := json.Marshal(body)
				return response(string(b), 200, nil)
			})
			out, err := reader.Read(context.Background(), v3Request("bitbucket"))
			assertIncomplete(t, out, err)
		})
	}
}
func TestV3EqualCreationTimesAllowedButReorderingDetected(t *testing.T) {
	for _, provider := range []string{"gitee", "bitbucket"} {
		t.Run(provider, func(t *testing.T) {
			g1, g2 := fixturePR(), fixturePR()
			g2.ID = 102
			g2.Number = 8
			bb1, bb2 := bbFixture(), bbFixture()
			bb2.ID = 8
			var rows any = []wirePR{g1, g2}
			if provider == "bitbucket" {
				rows = []bitbucketPR{bb1, bb2}
			}
			out := v3ReadRows(t, provider, rows)
			if len(out.Observations) != 2 {
				t.Fatal("equal creation timestamps lost")
			}
			g1.Created = "2026-01-01T12:00:00Z"
			bb1.Created = g1.Created
			reader := v3FixtureReader(t, provider, Limits{}, func(req *http.Request, _ int) *http.Response {
				if provider == "gitee" {
					return v3Inventory(provider, req, []wirePR{g1, g2})
				}
				return v3Inventory(provider, req, []bitbucketPR{bb1, bb2})
			})
			out, err := reader.Read(context.Background(), v3Request(provider))
			assertIncomplete(t, out, err)
		})
	}
}

func TestV3MovementAtPageBoundaries(t *testing.T) {
	for _, provider := range []string{"gitee", "bitbucket"} {
		for _, mode := range []string{"insert", "delete", "replace", "update", "duplicate"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				pass := 0
				reader := v3FixtureReader(t, provider, Limits{PageSize: 1}, func(req *http.Request, _ int) *http.Response {
					if v3IsRepo(req) {
						return v3RepoResponse(provider)
					}
					page := 1
					raw := req.URL.Query().Get("page")
					if raw != "" {
						page, _ = strconv.Atoi(raw)
					}
					if page == 1 {
						pass++
					}
					ids := []int64{7, 8, 9}
					if pass == 2 {
						switch mode {
						case "insert":
							ids = []int64{6, 7, 8, 9}
						case "delete":
							ids = []int64{7, 9}
						case "replace":
							ids = []int64{7, 10, 9}
						case "duplicate":
							ids = []int64{7, 7, 9}
						}
					}
					if provider == "gitee" {
						rows := []wirePR{}
						if page <= len(ids) {
							p := fixturePR()
							p.ID = ids[page-1]
							p.Number = p.ID
							if pass == 2 && page == 2 && mode == "update" {
								p.Head.SHA = strings.Repeat("e", 40)
							}
							rows = append(rows, p)
						}
						b, _ := json.Marshal(rows)
						return response(string(b), 200, nil)
					}
					rows := []bitbucketPR{}
					if page <= len(ids) {
						p := bbFixture()
						p.ID = ids[page-1]
						if pass == 2 && page == 2 && mode == "update" {
							p.Source.Commit.Hash = strings.Repeat("e", 40)
						}
						rows = append(rows, p)
					}
					body := map[string]any{"values": rows}
					if page < len(ids) {
						u := *req.URL
						q := u.Query()
						q.Set("page", strconv.Itoa(page+1))
						u.RawQuery = q.Encode()
						body["next"] = u.String()
					}
					b, _ := json.Marshal(body)
					return response(string(b), 200, nil)
				})
				out, err := reader.Read(context.Background(), v3Request(provider))
				assertIncomplete(t, out, err)
			})
		}
	}
}
