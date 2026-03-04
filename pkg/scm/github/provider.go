// Copyright 2026 Arcentra Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/pkg/request"
	"github.com/arcentrix/arcentra/pkg/scm"
	"github.com/bytedance/sonic"
	"github.com/valyala/fasthttp"
)

type Provider struct {
	cfg scm.ProviderConfig
}

func New(cfg scm.ProviderConfig) (scm.Provider, error) {
	return &Provider{cfg: cfg}, nil
}

func (p *Provider) Kind() scm.ProviderKind { return scm.ProviderKindGitHub }

func (p *Provider) Capabilities() scm.CapSet {
	return scm.CapSet{
		scm.CapWebhookVerify: true,
		scm.CapWebhookParse:  true,
		scm.CapPollEvents:    true,
	}
}

func (p *Provider) VerifyWebhook(_ context.Context, req scm.WebhookRequest, secret string) error {
	sig := req.Header("X-Hub-Signature-256")
	return scm.VerifyHmacSha256Hex(req.Body, secret, sig, "sha256=")
}

func (p *Provider) ParseWebhook(_ context.Context, req scm.WebhookRequest) ([]scm.Event, error) {
	eventName := req.Header("X-GitHub-Event")
	switch eventName {
	case "pull_request":
		return p.parsePullRequest(req.Body)
	case "push":
		return p.parsePush(req.Body)
	case "create":
		return p.parseCreate(req.Body)
	default:
		return nil, nil
	}
}

func (p *Provider) PollEvents(ctx context.Context, repo scm.Repo, cursor scm.Cursor) ([]scm.Event, scm.Cursor, error) {
	if repo.Owner == "" || repo.Name == "" {
		return nil, cursor, fmt.Errorf("repo owner/name is required")
	}

	since := cursor.Since
	if since.IsZero() {
		since = time.Now().Add(-30 * time.Minute)
	}

	path := fmt.Sprintf("/repos/%s/%s/events", url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	var resp []githubEvent
	httpResp, err := request.NewRequest(
		p.apiBaseURL()+path,
		fasthttp.MethodGet,
		map[string]string{
			"Accept":        "application/vnd.github+json",
			"Authorization": "Bearer " + strings.TrimSpace(p.cfg.Token),
		},
		nil,
	).WithQueryParams(map[string]string{
		"per_page": "100",
	}).WithResult(&resp).Do(ctx)
	if err != nil {
		return nil, cursor, err
	}
	if httpResp == nil || httpResp.StatusCode() >= 400 {
		return nil, cursor, fmt.Errorf("github api error: %d", httpResp.StatusCode())
	}

	events := make([]scm.Event, 0, len(resp))
	next := since
	for _, e := range resp {
		if e.CreatedAt.IsZero() {
			continue
		}
		if !e.CreatedAt.After(since) {
			continue
		}
		if e.CreatedAt.After(next) {
			next = e.CreatedAt
		}
		evs := p.mapEvent(repo, e)
		events = append(events, evs...)
	}

	return events, scm.Cursor{Since: next}, nil
}

// CreateChangeRequest creates GitHub pull request and returns web URL.
func (p *Provider) CreateChangeRequest(ctx context.Context, req scm.ChangeRequestInput) (string, error) {
	repoInfo, ok := scm.ParseRepoFromURL(req.PipelineRepoURL)
	if !ok {
		return "", fmt.Errorf("invalid repository url: %s", req.PipelineRepoURL)
	}
	var out struct {
		HtmlURL string `json:"html_url"`
	}
	apiBase := strings.TrimSpace(p.cfg.APIBaseURL)
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	endpoint := strings.TrimRight(
		apiBase,
		"/",
	) + fmt.Sprintf(
		"/repos/%s/%s/pulls",
		url.PathEscape(repoInfo.Owner),
		url.PathEscape(repoInfo.Name),
	)
	resp, err := request.NewRequest(
		endpoint,
		fasthttp.MethodPost,
		map[string]string{
			"Accept":        "application/vnd.github+json",
			"Authorization": "Bearer " + strings.TrimSpace(p.cfg.Token),
		},
		nil,
	).
		WithBodyJSON(map[string]any{
			"title": req.Title,
			"head":  req.SourceBranch,
			"base":  req.TargetBranch,
			"body":  "created by arcentra pipeline editor",
		}).
		WithResult(&out).
		Do(ctx)
	if err != nil {
		return "", err
	}
	if resp == nil || resp.StatusCode() >= 400 {
		return "", fmt.Errorf("github create pr failed: %d", resp.StatusCode())
	}
	return out.HtmlURL, nil
}

func (p *Provider) apiBaseURL() string {
	apiBase := strings.TrimSpace(p.cfg.APIBaseURL)
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	return strings.TrimRight(apiBase, "/")
}

type githubEvent struct {
	Type      string          `json:"type"`
	Actor     githubActor     `json:"actor"`
	Repo      githubRepo      `json:"repo"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type githubActor struct {
	Login string `json:"login"`
}

type githubRepo struct {
	Name string `json:"name"`
}

func (p *Provider) mapEvent(repo scm.Repo, e githubEvent) []scm.Event {
	actor := e.Actor.Login
	if actor == "" {
		actor = "unknown"
	}
	switch e.Type {
	case "PullRequestEvent":
		var payload struct {
			Action      string `json:"action"`
			PullRequest struct {
				Number   int        `json:"number"`
				Title    string     `json:"title"`
				State    string     `json:"state"`
				Merged   bool       `json:"merged"`
				MergedAt *time.Time `json:"merged_at"`
				MergeSha string     `json:"merge_commit_sha"`
				Base     struct {
					Ref string `json:"ref"`
				} `json:"base"`
				Head struct {
					Ref string `json:"ref"`
				} `json:"head"`
			} `json:"pull_request"`
		}
		_ = sonic.Unmarshal(e.Payload, &payload)
		eventType := scm.EventTypePullRequest
		change := &scm.Change{
			Number:        payload.PullRequest.Number,
			Title:         payload.PullRequest.Title,
			SourceBranch:  payload.PullRequest.Head.Ref,
			TargetBranch:  payload.PullRequest.Base.Ref,
			State:         payload.PullRequest.State,
			IsMerged:      payload.PullRequest.Merged,
			MergeCommitID: payload.PullRequest.MergeSha,
		}
		if payload.Action == "closed" && payload.PullRequest.Merged {
			eventType = scm.EventTypePullMerged
		}
		occurred := e.CreatedAt
		if payload.PullRequest.MergedAt != nil && !payload.PullRequest.MergedAt.IsZero() {
			occurred = *payload.PullRequest.MergedAt
		}
		return []scm.Event{{
			ProviderKind: scm.ProviderKindGitHub,
			EventType:    eventType,
			Repo:         repo,
			ActorName:    actor,
			CommitID:     payload.PullRequest.MergeSha,
			OccurredAt:   occurred,
			Change:       change,
		}}
	case "PushEvent":
		var payload struct {
			Ref  string `json:"ref"`
			Head string `json:"head"`
		}
		_ = sonic.Unmarshal(e.Payload, &payload)
		t := scm.EventTypePush
		if strings.HasPrefix(payload.Ref, "refs/tags/") {
			t = scm.EventTypeTag
		}
		return []scm.Event{{
			ProviderKind: scm.ProviderKindGitHub,
			EventType:    t,
			Repo:         repo,
			ActorName:    actor,
			CommitID:     payload.Head,
			Ref:          payload.Ref,
			OccurredAt:   e.CreatedAt,
		}}
	case "CreateEvent":
		var payload struct {
			Ref     string `json:"ref"`
			RefType string `json:"ref_type"`
		}
		_ = sonic.Unmarshal(e.Payload, &payload)
		if payload.RefType != "tag" {
			return nil
		}
		return []scm.Event{{
			ProviderKind: scm.ProviderKindGitHub,
			EventType:    scm.EventTypeTag,
			Repo:         repo,
			ActorName:    actor,
			Ref:          "refs/tags/" + payload.Ref,
			OccurredAt:   e.CreatedAt,
		}}
	default:
		return nil
	}
}

func (p *Provider) parsePullRequest(body []byte) ([]scm.Event, error) {
	var payload struct {
		Action     string `json:"action"`
		Repository struct {
			HtmlUrl string `json:"html_url"`
			Name    string `json:"name"`
			Owner   struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repository"`
		Sender struct {
			Login string `json:"login"`
		} `json:"sender"`
		PullRequest struct {
			Number    int        `json:"number"`
			Title     string     `json:"title"`
			State     string     `json:"state"`
			Merged    bool       `json:"merged"`
			MergedAt  *time.Time `json:"merged_at"`
			MergeSha  string     `json:"merge_commit_sha"`
			UpdatedAt *time.Time `json:"updated_at"`
			Base      struct {
				Ref string `json:"ref"`
			} `json:"base"`
			Head struct {
				Ref string `json:"ref"`
			} `json:"head"`
		} `json:"pull_request"`
	}
	if err := sonic.Unmarshal(body, &payload); err != nil {
		return nil, err
	}

	repo := scm.Repo{
		Host:     "github.com",
		Owner:    payload.Repository.Owner.Login,
		Name:     payload.Repository.Name,
		FullName: payload.Repository.Owner.Login + "/" + payload.Repository.Name,
		URL:      payload.Repository.HtmlUrl,
	}

	occurred := time.Now()
	if payload.PullRequest.UpdatedAt != nil && !payload.PullRequest.UpdatedAt.IsZero() {
		occurred = *payload.PullRequest.UpdatedAt
	}
	if payload.PullRequest.MergedAt != nil && !payload.PullRequest.MergedAt.IsZero() {
		occurred = *payload.PullRequest.MergedAt
	}

	t := scm.EventTypePullRequest
	if payload.Action == "closed" && payload.PullRequest.Merged {
		t = scm.EventTypePullMerged
	}
	return []scm.Event{{
		ProviderKind: scm.ProviderKindGitHub,
		EventType:    t,
		Repo:         repo,
		ActorName:    payload.Sender.Login,
		CommitID:     payload.PullRequest.MergeSha,
		OccurredAt:   occurred,
		Change: &scm.Change{
			Number:        payload.PullRequest.Number,
			Title:         payload.PullRequest.Title,
			SourceBranch:  payload.PullRequest.Head.Ref,
			TargetBranch:  payload.PullRequest.Base.Ref,
			State:         payload.PullRequest.State,
			IsMerged:      payload.PullRequest.Merged,
			MergeCommitID: payload.PullRequest.MergeSha,
		},
	}}, nil
}

func (p *Provider) parsePush(body []byte) ([]scm.Event, error) {
	var payload struct {
		Ref        string `json:"ref"`
		HeadCommit struct {
			Id        string    `json:"id"`
			Timestamp time.Time `json:"timestamp"`
		} `json:"head_commit"`
		Repository struct {
			HtmlUrl string `json:"html_url"`
			Name    string `json:"name"`
			Owner   struct {
				Name  string `json:"name"`
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repository"`
		Pusher struct {
			Name string `json:"name"`
		} `json:"pusher"`
	}
	if err := sonic.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	owner := payload.Repository.Owner.Login
	if owner == "" {
		owner = payload.Repository.Owner.Name
	}
	repo := scm.Repo{
		Host:     "github.com",
		Owner:    owner,
		Name:     payload.Repository.Name,
		FullName: owner + "/" + payload.Repository.Name,
		URL:      payload.Repository.HtmlUrl,
	}

	t := scm.EventTypePush
	if strings.HasPrefix(payload.Ref, "refs/tags/") {
		t = scm.EventTypeTag
	}
	occurred := time.Now()
	if !payload.HeadCommit.Timestamp.IsZero() {
		occurred = payload.HeadCommit.Timestamp
	}
	return []scm.Event{{
		ProviderKind: scm.ProviderKindGitHub,
		EventType:    t,
		Repo:         repo,
		ActorName:    payload.Pusher.Name,
		CommitID:     payload.HeadCommit.Id,
		Ref:          payload.Ref,
		OccurredAt:   occurred,
	}}, nil
}

func (p *Provider) parseCreate(body []byte) ([]scm.Event, error) {
	var payload struct {
		Ref        string `json:"ref"`
		RefType    string `json:"ref_type"`
		Repository struct {
			HtmlUrl string `json:"html_url"`
			Name    string `json:"name"`
			Owner   struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repository"`
		Sender struct {
			Login string `json:"login"`
		} `json:"sender"`
	}
	if err := sonic.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if payload.RefType != "tag" {
		return nil, nil
	}
	repo := scm.Repo{
		Host:     "github.com",
		Owner:    payload.Repository.Owner.Login,
		Name:     payload.Repository.Name,
		FullName: payload.Repository.Owner.Login + "/" + payload.Repository.Name,
		URL:      payload.Repository.HtmlUrl,
	}
	return []scm.Event{{
		ProviderKind: scm.ProviderKindGitHub,
		EventType:    scm.EventTypeTag,
		Repo:         repo,
		ActorName:    payload.Sender.Login,
		Ref:          "refs/tags/" + payload.Ref,
		OccurredAt:   time.Now(),
	}}, nil
}

func init() {
	scm.Register(scm.ProviderKindGitHub, New)
}
