package gitea

import (
	"context"
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
		base := strings.TrimRight(strings.TrimSpace(cfg.BaseUrl), "/")
		if base == "" {
			return nil, fmt.Errorf("gitea baseUrl or apiBaseUrl is required")
		}
		apiBase = base + "/api/v1"
	}
	p.client.SetBaseURL(strings.TrimRight(apiBase, "/"))
	if strings.TrimSpace(cfg.Token) != "" {
		p.client.SetAuthToken(cfg.Token)
	}
	return p, nil
}

func (p *Provider) Kind() scm.ProviderKind { return scm.ProviderKindGitea }

func (p *Provider) Capabilities() scm.CapSet {
	return scm.CapSet{
		scm.CapWebhookVerify: true,
		scm.CapWebhookParse:  true,
		scm.CapPollEvents:    true,
	}
}

func (p *Provider) VerifyWebhook(_ context.Context, req scm.WebhookRequest, secret string) error {
	return scm.VerifyHmacSha256Hex(req.Body, secret, req.Header("X-Gitea-Signature"), "")
}

func (p *Provider) ParseWebhook(_ context.Context, req scm.WebhookRequest) ([]scm.Event, error) {
	eventName := req.Header("X-Gitea-Event")
	if eventName == "" {
		eventName = req.Header("X-GitHub-Event")
	}
	switch eventName {
	case "pull_request":
		return p.parsePullRequest(req.Body)
	case "push":
		return p.parsePush(req.Body)
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
	next := since
	events := make([]scm.Event, 0)

	var prs []struct {
		Number    int        `json:"number"`
		Title     string     `json:"title"`
		State     string     `json:"state"`
		Merged    bool       `json:"merged"`
		MergedAt  *time.Time `json:"merged_at"`
		MergeSha  string     `json:"merge_commit_sha"`
		UpdatedAt time.Time  `json:"updated_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls", url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	r, err := p.client.R().
		SetContext(ctx).
		SetQueryParams(map[string]string{
			"state": "all",
			"page":  "1",
			"limit": "50",
			"sort":  "recentupdate",
		}).
		SetResult(&prs).
		Get(path)
	if err != nil {
		return nil, cursor, err
	}
	if r.IsError() {
		return nil, cursor, fmt.Errorf("gitea api error: %s", r.Status())
	}
	for _, pr := range prs {
		occurred := pr.UpdatedAt
		t := scm.EventTypePullRequest
		if pr.MergedAt != nil && !pr.MergedAt.IsZero() {
			occurred = *pr.MergedAt
			t = scm.EventTypePullMerged
		}
		if occurred.IsZero() || !occurred.After(since) {
			continue
		}
		if occurred.After(next) {
			next = occurred
		}
		events = append(events, scm.Event{
			ProviderKind: scm.ProviderKindGitea,
			EventType:    t,
			Repo:         repo,
			ActorName:    pr.User.Login,
			CommitId:     pr.MergeSha,
			OccurredAt:   occurred,
			Change: &scm.Change{
				Number:        pr.Number,
				Title:         pr.Title,
				SourceBranch:  pr.Head.Ref,
				TargetBranch:  pr.Base.Ref,
				State:         pr.State,
				IsMerged:      t == scm.EventTypePullMerged,
				MergeCommitId: pr.MergeSha,
			},
		})
	}

	var tags []struct {
		Name   string `json:"name"`
		Commit struct {
			Sha string `json:"sha"`
		} `json:"commit"`
	}
	r, err = p.client.R().
		SetContext(ctx).
		SetQueryParams(map[string]string{
			"page":  "1",
			"limit": "50",
		}).
		SetResult(&tags).
		Get(fmt.Sprintf("/repos/%s/%s/tags", url.PathEscape(repo.Owner), url.PathEscape(repo.Name)))
	if err != nil {
		return nil, cursor, err
	}
	if r.IsError() {
		return nil, cursor, fmt.Errorf("gitea api error: %s", r.Status())
	}
	for _, tag := range tags {
		commitDate, commitId := p.tagCommitTime(ctx, repo, tag.Commit.Sha)
		if commitDate.IsZero() || !commitDate.After(since) {
			continue
		}
		if commitDate.After(next) {
			next = commitDate
		}
		events = append(events, scm.Event{
			ProviderKind: scm.ProviderKindGitea,
			EventType:    scm.EventTypeTag,
			Repo:         repo,
			CommitId:     commitId,
			Ref:          "refs/tags/" + tag.Name,
			OccurredAt:   commitDate,
		})
	}

	return events, scm.Cursor{Since: next}, nil
}

func (p *Provider) tagCommitTime(ctx context.Context, repo scm.Repo, sha string) (time.Time, string) {
	if strings.TrimSpace(sha) == "" {
		return time.Time{}, ""
	}
	var out struct {
		Sha    string `json:"sha"`
		Commit struct {
			Committer struct {
				Date time.Time `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	}
	path := fmt.Sprintf("/repos/%s/%s/commits/%s", url.PathEscape(repo.Owner), url.PathEscape(repo.Name), url.PathEscape(sha))
	r, err := p.client.R().SetContext(ctx).SetResult(&out).Get(path)
	if err == nil && r != nil && !r.IsError() {
		return out.Commit.Committer.Date, out.Sha
	}

	var out2 struct {
		Sha       string `json:"sha"`
		Committer struct {
			Date time.Time `json:"date"`
		} `json:"committer"`
	}
	path = fmt.Sprintf("/repos/%s/%s/git/commits/%s", url.PathEscape(repo.Owner), url.PathEscape(repo.Name), url.PathEscape(sha))
	r, err = p.client.R().SetContext(ctx).SetResult(&out2).Get(path)
	if err != nil || r == nil || r.IsError() {
		return time.Time{}, sha
	}
	return out2.Committer.Date, out2.Sha
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
		ProviderKind: scm.ProviderKindGitea,
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
			IsMerged:      t == scm.EventTypePullMerged,
			MergeCommitId: payload.PullRequest.MergeSha,
		},
	}}, nil
}

func (p *Provider) parsePush(body []byte) ([]scm.Event, error) {
	var payload struct {
		Ref        string `json:"ref"`
		After      string `json:"after"`
		Repository struct {
			HtmlUrl string `json:"html_url"`
			Name    string `json:"name"`
			Owner   struct {
				Username string `json:"username"`
			} `json:"owner"`
		} `json:"repository"`
		Pusher struct {
			FullName string `json:"full_name"`
			UserName string `json:"username"`
		} `json:"pusher"`
		Commits []struct {
			Id        string    `json:"id"`
			Timestamp time.Time `json:"timestamp"`
		} `json:"commits"`
	}
	if err := sonic.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	repo := scm.Repo{
		Owner:    payload.Repository.Owner.Username,
		Name:     payload.Repository.Name,
		FullName: payload.Repository.Owner.Username + "/" + payload.Repository.Name,
		Url:      payload.Repository.HtmlUrl,
	}
	t := scm.EventTypePush
	if strings.HasPrefix(payload.Ref, "refs/tags/") {
		t = scm.EventTypeTag
	}
	occurred := time.Now()
	if len(payload.Commits) > 0 && !payload.Commits[len(payload.Commits)-1].Timestamp.IsZero() {
		occurred = payload.Commits[len(payload.Commits)-1].Timestamp
	}
	actor := payload.Pusher.UserName
	if actor == "" {
		actor = payload.Pusher.FullName
	}
	return []scm.Event{{
		ProviderKind: scm.ProviderKindGitea,
		EventType:    t,
		Repo:         repo,
		ActorName:    actor,
		CommitId:     payload.After,
		Ref:          payload.Ref,
		OccurredAt:   occurred,
	}}, nil
}

func init() {
	scm.Register(scm.ProviderKindGitea, New)
}
