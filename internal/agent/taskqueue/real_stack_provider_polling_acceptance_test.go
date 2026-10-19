// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/polling"
	"github.com/arcentrix/arcentra/pkg/scm"
	"github.com/arcentrix/arcentra/pkg/scm/bitbucket"
	"github.com/arcentrix/arcentra/pkg/scm/gitea"
	"github.com/arcentrix/arcentra/pkg/scm/gitee"
	"github.com/arcentrix/arcentra/pkg/scm/github"
	"github.com/arcentrix/arcentra/pkg/scm/pollsnapshot"
	"github.com/bytedance/sonic"
)

// realStackGitHubTLSMap is an explicit test-only mapping of the fixed public API
// origin to one synthetic TLS fixture. It neither changes production origin policy
// nor turns off certificate verification, and cannot send requests to the internet.
// The underlying transport trusts only httptest's locally generated certificate.
type realStackGitHubTLSMap struct {
	local     *url.URL
	transport http.RoundTripper
}

func (m realStackGitHubTLSMap) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "https" || request.URL.Host != "api.github.com" || request.URL.User != nil || request.URL.Fragment != "" || request.Method != http.MethodGet || request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" || request.Header.Get("X-GitHub-Api-Version") != "2026-03-10" {
		return nil, fmt.Errorf("unexpected synthetic GitHub API request")
	}
	const repositoryPath = "/repos/acceptance/configured-repository"
	if request.URL.Path != repositoryPath && request.URL.Path != repositoryPath+"/pulls" {
		return nil, fmt.Errorf("unexpected synthetic GitHub API path")
	}
	mapped := request.Clone(request.Context())
	mapped.URL.Scheme, mapped.URL.Host, mapped.Host = m.local.Scheme, m.local.Host, m.local.Host
	return m.transport.RoundTrip(mapped)
}

func newRealStackPRPollingSource(t *testing.T, f *controllerWorkerFixture, directory, provider string) *realStackPollingSource {
	t.Helper()
	if provider == model.RepoTypeGitee || provider == model.RepoTypeBitbucket {
		return newRealStackV3PollingSource(t, f, directory, provider)
	}
	if provider != model.RepoTypeGitHub && provider != model.RepoTypeGitea {
		t.Fatal("unexpected synthetic PR provider")
	}
	const name = "acceptance/configured-repository"
	s := &realStackPollingSource{
		directory: directory, projectID: f.projectID, pipelineID: f.pipelineID,
		provider: provider, repositoryName: name, snapshotVersion: pollsnapshot.Version,
		// Each of two inventories requires a populated and an empty page, plus
		// stable repository identity reads before and after both inventories.
		requestsPerSnapshot: 6,
	}
	path := "/repos/" + name
	if provider == model.RepoTypeGitea {
		path = "/api/v1" + path
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.requests++
		if r.TLS == nil || r.Method != http.MethodGet || r.Header.Get("PRIVATE-TOKEN") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			http.Error(w, "fixture accepts only unauthenticated TLS reads", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case path:
			if r.URL.RawQuery != "" {
				http.Error(w, "unexpected repository query", http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, `{"id":731,"full_name":"`+name+`"}`)
		case path + "/pulls":
			q := r.URL.Query()
			valid := q.Get("state") == "all" && (q.Get("page") == "1" || q.Get("page") == "2")
			if provider == model.RepoTypeGitHub {
				valid = valid && q.Get("sort") == "created" && q.Get("direction") == "asc" && q.Get("per_page") == "100" && len(q) == 5
			} else {
				valid = valid && q.Get("sort") == "oldest" && q.Get("limit") == "100" && len(q) == 4
			}
			if !valid {
				http.Error(w, "fixture requires a bounded complete inventory request", http.StatusBadRequest)
				return
			}
			if q.Get("page") == "1" {
				_, _ = w.Write(s.body)
			} else {
				// Do not claim that one short page proves a complete inventory.
				_, _ = io.WriteString(w, `[]`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	cfg := scm.ProviderConfig{Kind: scm.ProviderKind(provider), BaseURL: server.URL}
	client := server.Client()
	s.repository, s.apiOrigin = server.URL+"/"+name, server.URL
	if provider == model.RepoTypeGitHub {
		local, err := url.Parse(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		client = &http.Client{Transport: realStackGitHubTLSMap{local: local, transport: client.Transport}}
		cfg.BaseURL = "https://github.com"
		s.repository, s.apiOrigin = cfg.BaseURL+"/"+name, "https://api.github.com"
	}
	var err error
	if provider == model.RepoTypeGitHub {
		s.reader, err = github.NewSnapshotReader(cfg, client, pollsnapshot.Limits{})
	} else {
		s.reader, err = gitea.NewSnapshotReader(cfg, client, pollsnapshot.Limits{})
	}
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const realStackBitbucketProjectKey = "11111111-2222-4333-8444-555555555555"
const realStackBitbucketSourceKey = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"

// realStackV3TLSMap can reach only the fixture. Public origins are still passed
// to the production readers, and the local certificate is verified normally.
// This mapping is test-only and never authenticates to an external account.
type realStackV3TLSMap struct {
	local            *url.URL
	transport        http.RoundTripper
	host, path, list string
}

func (m realStackV3TLSMap) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "https" || request.URL.Host != m.host || request.URL.User != nil || request.URL.Fragment != "" || request.URL.RawPath != "" || request.Method != http.MethodGet || request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" || request.URL.Query().Has("access_token") || request.URL.Path != m.path && request.URL.Path != m.path+m.list {
		return nil, fmt.Errorf("unexpected synthetic v3 API request")
	}
	mapped := request.Clone(request.Context())
	mapped.URL.Scheme, mapped.URL.Host, mapped.Host = m.local.Scheme, m.local.Host, m.local.Host
	return m.transport.RoundTrip(mapped)
}

func newRealStackV3PollingSource(t *testing.T, f *controllerWorkerFixture, directory, provider string) *realStackPollingSource {
	t.Helper()
	const name = "acceptance/configured-repository"
	s := &realStackPollingSource{directory: directory, projectID: f.projectID, pipelineID: f.pipelineID, provider: provider, repositoryName: name, snapshotVersion: pollsnapshot.Version3,
		// Exactly two repository-binding reads plus two two-page inventories.
		// Gitee uses an empty sentinel; Bitbucket follows an opaque next cursor.
		requestsPerSnapshot: 6}
	public, host, path, list := "https://gitee.com", "gitee.com", "/api/v5/repos/"+name, "/pulls"
	if provider == model.RepoTypeBitbucket {
		public, host, path, list = "https://bitbucket.org", "api.bitbucket.org", "/2.0/repositories/"+name, "/pullrequests"
	} else if provider != model.RepoTypeGitee {
		t.Fatal("unexpected synthetic v3 provider")
	}
	s.repository, s.apiOrigin = public+"/"+name, "https://"+host
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.requests++
		if r.TLS == nil || r.Method != http.MethodGet || r.Header.Get("PRIVATE-TOKEN") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.URL.Query().Has("access_token") {
			http.Error(w, "fixture accepts only unauthenticated TLS reads", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case path:
			if r.URL.RawQuery != "" {
				http.Error(w, "unexpected repository query", http.StatusBadRequest)
				return
			}
			if provider == model.RepoTypeGitee {
				_, _ = io.WriteString(w, `{"id":731,"full_name":"`+name+`"}`)
			} else {
				_, _ = io.WriteString(w, `{"uuid":"{`+realStackBitbucketProjectKey+`}","full_name":"`+name+`"}`)
			}
		case path + list:
			q := r.URL.Query()
			if provider == model.RepoTypeGitee {
				if q.Get("state") != "all" || q.Get("sort") != "created" || q.Get("direction") != "asc" || q.Get("per_page") != "100" || len(q) != 5 || q.Get("page") != "1" && q.Get("page") != "2" {
					http.Error(w, "unexpected Gitee inventory query", http.StatusBadRequest)
					return
				}
				if q.Get("page") == "1" {
					_, _ = w.Write(s.body)
				} else {
					_, _ = io.WriteString(w, `[]`)
				}
				return
			}
			page := q.Get("page")
			q.Del("page")
			want := url.Values{"state": {"OPEN", "MERGED", "DECLINED", "SUPERSEDED"}, "sort": {"created_on"}, "pagelen": {"100"}}
			if !reflect.DeepEqual(q, want) || page != "" && page != "opaque-next-page" {
				http.Error(w, "unexpected Bitbucket inventory query", http.StatusBadRequest)
				return
			}
			if page != "" {
				// Missing next is explicit terminal evidence; no invented numeric page.
				_, _ = io.WriteString(w, `{"values":[]}`)
				return
			}
			q.Set("page", "opaque-next-page")
			next, _ := sonic.Marshal(s.apiOrigin + path + list + "?" + q.Encode())
			_, _ = fmt.Fprintf(w, `{"values":%s,"next":%s}`, s.body, next)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	local, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: realStackV3TLSMap{local: local, transport: server.Client().Transport, host: host, path: path, list: list}}
	cfg := scm.ProviderConfig{Kind: scm.ProviderKind(provider), BaseURL: public}
	if provider == model.RepoTypeGitee {
		s.reader, err = gitee.NewSnapshotReader(cfg, client, pollsnapshot.Limits{})
	} else {
		s.reader, err = bitbucket.NewSnapshotReader(cfg, client, pollsnapshot.Limits{})
	}
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *realStackPollingSource) observeV3(t *testing.T, sha, target string, updated time.Time, merged *time.Time, mergeSHA string) {
	t.Helper()
	stamp := updated.UTC().Format(time.RFC3339Nano)
	item := map[string]any{"id": 901, "title": "Synthetic metadata at " + stamp}
	if s.provider == model.RepoTypeGitee {
		item["number"], item["state"], item["created_at"], item["updated_at"] = 17, "open", "2025-01-01T00:00:00Z", stamp
		item["head"] = map[string]any{"ref": "feature/observed-mr", "sha": sha, "repo": map[string]any{"id": 732, "full_name": "contributor/source-fork"}}
		item["base"] = map[string]any{"ref": target, "sha": s.targetSHA, "repo": map[string]any{"id": 731, "full_name": s.repositoryName}}
		if merged != nil {
			item["state"], item["merged_at"] = "merged", merged.UTC().Format(time.RFC3339Nano)
		}
		if mergeSHA != "" {
			t.Fatal("Gitee fixture must not fabricate an unavailable merge SHA")
		}
	} else if s.provider == model.RepoTypeBitbucket {
		item["state"], item["created_on"], item["updated_on"] = "OPEN", "2025-01-01T00:00:00Z", stamp
		item["source"] = map[string]any{"branch": map[string]any{"name": "feature/observed-mr"}, "commit": map[string]any{"hash": sha}, "repository": map[string]any{"uuid": "{" + strings.ToUpper(realStackBitbucketSourceKey) + "}", "full_name": "contributor/source-fork"}}
		item["destination"] = map[string]any{"branch": map[string]any{"name": target}, "commit": map[string]any{"hash": s.targetSHA}, "repository": map[string]any{"uuid": "{" + realStackBitbucketProjectKey + "}", "full_name": s.repositoryName}}
		if mergeSHA != "" {
			item["state"], item["merge_commit"] = "MERGED", map[string]any{"hash": mergeSHA}
		}
		if merged != nil {
			t.Fatal("Bitbucket fixture must not fabricate an unavailable merge time")
		}
	} else {
		t.Fatal("unexpected v3 observation provider")
	}
	body, err := sonic.Marshal([]map[string]any{item})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.body = body
	s.mu.Unlock()
}

func realStackAssertV3PollingEvidence(t *testing.T, source *realStackPollingSource, scope polling.Scope, target polling.Target, result pollsnapshot.Result) {
	t.Helper()
	key, mode := "732", pollsnapshot.MergeEvidenceStateTimestamp
	if source.provider == model.RepoTypeBitbucket {
		key, mode = realStackBitbucketSourceKey, pollsnapshot.MergeEvidenceStateCommit
	}
	if len(result.Observations) != 1 {
		t.Fatal("v3 reader did not return the exact single PR inventory")
	}
	o := result.Observations[0]
	if scope.Version != pollsnapshot.Version3 || result.Version != pollsnapshot.Version3 || result.Provider != source.provider || result.APIOrigin != source.apiOrigin || result.Repository != source.repositoryName || result.ProviderProjectID != 0 || result.ProviderProjectKey != scope.ProviderProjectID || o.ProviderProjectID != 0 || o.SourceProjectID != 0 || o.EvidenceVersion != pollsnapshot.Version3 || o.ProviderProjectKey != scope.ProviderProjectID || o.SourceProjectKey != key || o.MergeEvidence != mode || o.SourceRepository != "contributor/source-fork" || o.TargetSHA != source.targetSHA || o.CreatedAt == nil {
		t.Fatal("v3 reader lost its version, string repository/fork identity, or provider evidence mode")
	}
	mergedAt := ""
	if o.MergedAt != nil {
		mergedAt = o.MergedAt.UTC().Format(time.RFC3339Nano)
	}
	if err := target.ValidateForScope(scope); err != nil || target.EvidenceVersion != pollsnapshot.Version3 || target.SourceProjectID != 0 || target.ProviderProjectKey != scope.ProviderProjectID || target.SourceProjectKey != key || target.MergeEvidence != mode || target.SourceRepository != o.SourceRepository || target.TargetSHA != o.TargetSHA || target.SourceSHA != o.SourceSHA || target.MRID != o.ID || target.MRIID != o.IID || target.ObservationID != o.ObservationID || target.State != o.State || target.UpdatedAt != o.UpdatedAt.UTC().Format(time.RFC3339Nano) || target.MergeSHA != o.MergeSHA || target.MergedAt != mergedAt {
		t.Fatalf("v3 frozen/run provenance differs from the observed provider evidence: %v", err)
	}
}
