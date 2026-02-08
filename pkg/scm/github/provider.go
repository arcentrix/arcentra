package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/pkg/scm"
	"github.com/bytedance/sonic"
	"github.com/go-resty/resty/v2"
)

type Provider struct {
	cfg    scm.ProviderConfig
	client *resty.Client
}

func New(cfg scm.ProviderConfig) (scm.Provider, error) {
	p := &Provider{cfg: cfg}
	p.client = resty.New().SetTimeout(15 * time.Second)

	apiBase := strings.TrimSpace(cfg.ApiBaseUrl)
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	p.client.SetBaseURL(strings.TrimRight(apiBase, "/"))

	if strings.TrimSpace(cfg.Token) != "" {
		p.client.SetAuthToken(cfg.Token)
	}

	return p, nil
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
	r, err := p.client.R().
		SetContext(ctx).
		SetQueryParam("per_page", "100").
		SetResult(&resp).
		Get(path)
	if err != nil {
		return nil, cursor, err
	}
	if r.IsError() {
		return nil, cursor, fmt.Errorf("github api error: %s", r.Status())
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
			MergeCommitId: payload.PullRequest.MergeSha,
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
			CommitId:     payload.PullRequest.MergeSha,
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
			CommitId:     payload.Head,
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
		Url:      payload.Repository.HtmlUrl,
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
		CommitId:     payload.PullRequest.MergeSha,
		OccurredAt:   occurred,
		Change: &scm.Change{
			Number:        payload.PullRequest.Number,
			Title:         payload.PullRequest.Title,
			SourceBranch:  payload.PullRequest.Head.Ref,
			TargetBranch:  payload.PullRequest.Base.Ref,
			State:         payload.PullRequest.State,
			IsMerged:      payload.PullRequest.Merged,
			MergeCommitId: payload.PullRequest.MergeSha,
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
		Url:      payload.Repository.HtmlUrl,
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
		CommitId:     payload.HeadCommit.Id,
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
		Url:      payload.Repository.HtmlUrl,
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
