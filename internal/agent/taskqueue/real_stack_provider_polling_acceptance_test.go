// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/scm"
	"github.com/arcentrix/arcentra/pkg/scm/gitea"
	"github.com/arcentrix/arcentra/pkg/scm/github"
	"github.com/arcentrix/arcentra/pkg/scm/pollsnapshot"
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
