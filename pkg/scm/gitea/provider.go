package gitea

import (
	"context"
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
	apiBase := strings.TrimSpace(cfg.APIBaseURL)
	if apiBase == "" {
		base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
		if base == "" {
			return nil, fmt.Errorf("gitea baseUrl or apiBaseUrl is required")
		}
	}
	return &Provider{cfg: cfg}, nil
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
	httpResp, err := request.NewRequest(
		p.apiBaseURL()+path,
		fasthttp.MethodGet,
		map[string]string{
			"Authorization": "Bearer " + strings.TrimSpace(p.cfg.Token),
		},
		nil,
	).WithQueryParams(map[string]string{
		"state": "all",
		"page":  "1",
		"limit": "50",
		"sort":  "recentupdate",
	}).WithResult(&prs).Do(ctx)
	if err != nil {
		return nil, cursor, err
	}
	if httpResp == nil || httpResp.StatusCode() >= 400 {
		return nil, cursor, fmt.Errorf("gitea api error: %d", httpResp.StatusCode())
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
			CommitID:     pr.MergeSha,
			OccurredAt:   occurred,
			Change: &scm.Change{
				Number:        pr.Number,
				Title:         pr.Title,
				SourceBranch:  pr.Head.Ref,
				TargetBranch:  pr.Base.Ref,
				State:         pr.State,
				IsMerged:      t == scm.EventTypePullMerged,
				MergeCommitID: pr.MergeSha,
			},
		})
	}

	var tags []struct {
		Name   string `json:"name"`
		Commit struct {
			Sha string `json:"sha"`
		} `json:"commit"`
	}
	httpResp, err = request.NewRequest(
		p.apiBaseURL()+fmt.Sprintf("/repos/%s/%s/tags", url.PathEscape(repo.Owner), url.PathEscape(repo.Name)),
		fasthttp.MethodGet,
		map[string]string{
			"Authorization": "Bearer " + strings.TrimSpace(p.cfg.Token),
		},
		nil,
	).WithQueryParams(map[string]string{
		"page":  "1",
		"limit": "50",
	}).WithResult(&tags).Do(ctx)
	if err != nil {
		return nil, cursor, err
	}
	if httpResp == nil || httpResp.StatusCode() >= 400 {
		return nil, cursor, fmt.Errorf("gitea api error: %d", httpResp.StatusCode())
	}
	for _, tag := range tags {
		commitDate, commitID := p.tagCommitTime(ctx, repo, tag.Commit.Sha)
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
			CommitID:     commitID,
			Ref:          "refs/tags/" + tag.Name,
			OccurredAt:   commitDate,
		})
	}

	return events, scm.Cursor{Since: next}, nil
}

// CreateChangeRequest creates Gitea pull request and returns web URL.
func (p *Provider) CreateChangeRequest(ctx context.Context, req scm.ChangeRequestInput) (string, error) {
	repoInfo, ok := scm.ParseRepoFromURL(req.PipelineRepoURL)
	if !ok {
		return "", fmt.Errorf("invalid repository url: %s", req.PipelineRepoURL)
	}
	var out struct {
		HTMLURL string `json:"html_url"`
	}
	apiBase := strings.TrimSpace(p.cfg.APIBaseURL)
	if apiBase == "" {
		base := strings.TrimRight(strings.TrimSpace(p.cfg.BaseURL), "/")
		if base == "" {
			return "", fmt.Errorf("gitea baseUrl or apiBaseUrl is required")
		}
		apiBase = base + "/api/v1"
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
		return "", fmt.Errorf("gitea create pr failed: %d", resp.StatusCode())
	}
	return out.HTMLURL, nil
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
	httpResp, err := request.NewRequest(
		p.apiBaseURL()+path,
		fasthttp.MethodGet,
		map[string]string{
			"Authorization": "Bearer " + strings.TrimSpace(p.cfg.Token),
		},
		nil,
	).WithResult(&out).Do(ctx)
	if err == nil && httpResp != nil && httpResp.StatusCode() < 400 {
		return out.Commit.Committer.Date, out.Sha
	}

	var out2 struct {
		Sha       string `json:"sha"`
		Committer struct {
			Date time.Time `json:"date"`
		} `json:"committer"`
	}
	path = fmt.Sprintf("/repos/%s/%s/git/commits/%s", url.PathEscape(repo.Owner), url.PathEscape(repo.Name), url.PathEscape(sha))
	httpResp, err = request.NewRequest(
		p.apiBaseURL()+path,
		fasthttp.MethodGet,
		map[string]string{
			"Authorization": "Bearer " + strings.TrimSpace(p.cfg.Token),
		},
		nil,
	).WithResult(&out2).Do(ctx)
	if err != nil || httpResp == nil || httpResp.StatusCode() >= 400 {
		return time.Time{}, sha
	}
	return out2.Committer.Date, out2.Sha
}

func (p *Provider) apiBaseURL() string {
	apiBase := strings.TrimSpace(p.cfg.APIBaseURL)
	if apiBase == "" {
		base := strings.TrimRight(strings.TrimSpace(p.cfg.BaseURL), "/")
		if base == "" {
			return ""
		}
		apiBase = base + "/api/v1"
	}
	return strings.TrimRight(apiBase, "/")
}

func (p *Provider) parsePullRequest(body []byte) ([]scm.Event, error) {
	var payload struct {
		Action     string `json:"action"`
		Repository struct {
			HTMLURL string `json:"html_url"`
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
		URL:      payload.Repository.HTMLURL,
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
		CommitID:     payload.PullRequest.MergeSha,
		OccurredAt:   occurred,
		Change: &scm.Change{
			Number:        payload.PullRequest.Number,
			Title:         payload.PullRequest.Title,
			SourceBranch:  payload.PullRequest.Head.Ref,
			TargetBranch:  payload.PullRequest.Base.Ref,
			State:         payload.PullRequest.State,
			IsMerged:      t == scm.EventTypePullMerged,
			MergeCommitID: payload.PullRequest.MergeSha,
		},
	}}, nil
}

func (p *Provider) parsePush(body []byte) ([]scm.Event, error) {
	var payload struct {
		Ref        string `json:"ref"`
		After      string `json:"after"`
		Repository struct {
			HTMLURL string `json:"html_url"`
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
			ID        string    `json:"id"`
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
		URL:      payload.Repository.HTMLURL,
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
		CommitID:     payload.After,
		Ref:          payload.Ref,
		OccurredAt:   occurred,
	}}, nil
}

func init() {
	scm.Register(scm.ProviderKindGitea, New)
}
