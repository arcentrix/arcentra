package gitee

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
		apiBase = "https://gitee.com/api/v5"
	}
	p.client.SetBaseURL(strings.TrimRight(apiBase, "/"))
	return p, nil
}

func (p *Provider) Kind() scm.ProviderKind { return scm.ProviderKindGitee }

func (p *Provider) Capabilities() scm.CapSet {
	return scm.CapSet{
		scm.CapWebhookVerify: true,
		scm.CapWebhookParse:  true,
		scm.CapPollEvents:    true,
	}
}

func (p *Provider) VerifyWebhook(_ context.Context, req scm.WebhookRequest, secret string) error {
	return scm.VerifyTokenHeader(secret, req.Header("X-Gitee-Token"))
}

func (p *Provider) ParseWebhook(_ context.Context, req scm.WebhookRequest) ([]scm.Event, error) {
	ev := req.Header("X-Gitee-Event")
	switch ev {
	case "Merge Request Hook", "Pull Request Hook":
		return p.parsePullRequest(req.Body)
	case "Push Hook", "Tag Push Hook":
		return p.parsePush(req.Body)
	default:
		// gitee sends human readable values; keep unknown as nil
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
		UpdatedAt time.Time  `json:"updated_at"`
		MergedAt  *time.Time `json:"merged_at"`
		MergeSha  string     `json:"merge_commit_sha"`
		User      struct {
			Login string `json:"login"`
			Name  string `json:"name"`
		} `json:"user"`
		Head struct {
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}

	r, err := p.client.R().
		SetContext(ctx).
		SetQueryParams(map[string]string{
			"access_token": strings.TrimSpace(p.cfg.Token),
			"state":        "all",
			"sort":         "updated",
			"direction":    "desc",
			"per_page":     "50",
			"page":         "1",
		}).
		SetResult(&prs).
		Get(fmt.Sprintf("/repos/%s/%s/pulls", url.PathEscape(repo.Owner), url.PathEscape(repo.Name)))
	if err != nil {
		return nil, cursor, err
	}
	if r.IsError() {
		return nil, cursor, fmt.Errorf("gitee api error: %s", r.Status())
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
		actor := pr.User.Login
		if actor == "" {
			actor = pr.User.Name
		}
		events = append(events, scm.Event{
			ProviderKind: scm.ProviderKindGitee,
			EventType:    t,
			Repo:         repo,
			ActorName:    actor,
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
			"access_token": strings.TrimSpace(p.cfg.Token),
			"per_page":     "50",
			"page":         "1",
		}).
		SetResult(&tags).
		Get(fmt.Sprintf("/repos/%s/%s/tags", url.PathEscape(repo.Owner), url.PathEscape(repo.Name)))
	if err != nil {
		return nil, cursor, err
	}
	if r.IsError() {
		return nil, cursor, fmt.Errorf("gitee api error: %s", r.Status())
	}
	for _, tag := range tags {
		commitDate := p.commitTime(ctx, repo, tag.Commit.Sha)
		if commitDate.IsZero() || !commitDate.After(since) {
			continue
		}
		if commitDate.After(next) {
			next = commitDate
		}
		events = append(events, scm.Event{
			ProviderKind: scm.ProviderKindGitee,
			EventType:    scm.EventTypeTag,
			Repo:         repo,
			CommitId:     tag.Commit.Sha,
			Ref:          "refs/tags/" + tag.Name,
			OccurredAt:   commitDate,
		})
	}

	return events, scm.Cursor{Since: next}, nil
}

func (p *Provider) commitTime(ctx context.Context, repo scm.Repo, sha string) time.Time {
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return time.Time{}
	}
	var out struct {
		Commit struct {
			Committer struct {
				Date time.Time `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	}
	r, err := p.client.R().
		SetContext(ctx).
		SetQueryParam("access_token", strings.TrimSpace(p.cfg.Token)).
		SetResult(&out).
		Get(fmt.Sprintf("/repos/%s/%s/commits/%s", url.PathEscape(repo.Owner), url.PathEscape(repo.Name), url.PathEscape(sha)))
	if err != nil || r == nil || r.IsError() {
		return time.Time{}
	}
	return out.Commit.Committer.Date
}

func (p *Provider) parsePullRequest(body []byte) ([]scm.Event, error) {
	var payload struct {
		PullRequest struct {
			Number    int        `json:"number"`
			Title     string     `json:"title"`
			State     string     `json:"state"`
			Merged    bool       `json:"merged"`
			MergedAt  *time.Time `json:"merged_at"`
			MergeSha  string     `json:"merge_commit_sha"`
			UpdatedAt *time.Time `json:"updated_at"`
			Head      struct {
				Ref string `json:"ref"`
			} `json:"head"`
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
		} `json:"pull_request"`
		Repository struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
			HtmlUrl   string `json:"html_url"`
		} `json:"repository"`
		Sender struct {
			UserName string `json:"user_name"`
			Name     string `json:"name"`
		} `json:"sender"`
	}
	if err := sonic.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	owner := payload.Repository.Namespace
	name := payload.Repository.Name
	repo := scm.Repo{
		Owner:    owner,
		Name:     name,
		FullName: owner + "/" + name,
		Url:      payload.Repository.HtmlUrl,
	}

	occurred := time.Now()
	if payload.PullRequest.UpdatedAt != nil && !payload.PullRequest.UpdatedAt.IsZero() {
		occurred = *payload.PullRequest.UpdatedAt
	}
	t := scm.EventTypePullRequest
	if payload.PullRequest.MergedAt != nil && !payload.PullRequest.MergedAt.IsZero() {
		occurred = *payload.PullRequest.MergedAt
		t = scm.EventTypePullMerged
	}
	actor := payload.Sender.UserName
	if actor == "" {
		actor = payload.Sender.Name
	}
	return []scm.Event{{
		ProviderKind: scm.ProviderKindGitee,
		EventType:    t,
		Repo:         repo,
		ActorName:    actor,
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
		HookName   string `json:"hook_name"`
		Repository struct {
			PathWithNamespace string `json:"path_with_namespace"`
			Namespace         string `json:"namespace"`
			Name              string `json:"name"`
			HtmlUrl           string `json:"html_url"`
		} `json:"repository"`
		Commits []struct {
			Id        string    `json:"id"`
			Timestamp time.Time `json:"timestamp"`
		} `json:"commits"`
		Sender struct {
			UserName string `json:"user_name"`
			Name     string `json:"name"`
		} `json:"sender"`
	}
	if err := sonic.Unmarshal(body, &payload); err != nil {
		return nil, err
	}

	owner := payload.Repository.Namespace
	if owner == "" {
		if parts := strings.SplitN(payload.Repository.PathWithNamespace, "/", 2); len(parts) == 2 {
			owner = parts[0]
		}
	}
	repo := scm.Repo{
		Owner:    owner,
		Name:     payload.Repository.Name,
		FullName: owner + "/" + payload.Repository.Name,
		Url:      payload.Repository.HtmlUrl,
	}
	t := scm.EventTypePush
	if strings.HasPrefix(payload.Ref, "refs/tags/") || payload.HookName == "tag_push_hooks" {
		t = scm.EventTypeTag
	}
	occurred := time.Now()
	if len(payload.Commits) > 0 && !payload.Commits[len(payload.Commits)-1].Timestamp.IsZero() {
		occurred = payload.Commits[len(payload.Commits)-1].Timestamp
	}
	actor := payload.Sender.UserName
	if actor == "" {
		actor = payload.Sender.Name
	}
	return []scm.Event{{
		ProviderKind: scm.ProviderKindGitee,
		EventType:    t,
		Repo:         repo,
		ActorName:    actor,
		CommitId:     payload.After,
		Ref:          payload.Ref,
		OccurredAt:   occurred,
	}}, nil
}

func init() {
	scm.Register(scm.ProviderKindGitee, New)
}
