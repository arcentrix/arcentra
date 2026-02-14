package gitlab

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
			base = "https://gitlab.com"
		}
		apiBase = base + "/api/v4"
	}
	p.client.SetBaseURL(strings.TrimRight(apiBase, "/"))

	if strings.TrimSpace(cfg.Token) != "" {
		p.client.SetHeader("PRIVATE-TOKEN", cfg.Token)
	}
	return p, nil
}

func (p *Provider) Kind() scm.ProviderKind { return scm.ProviderKindGitLab }

func (p *Provider) Capabilities() scm.CapSet {
	return scm.CapSet{
		scm.CapWebhookVerify: true,
		scm.CapWebhookParse:  true,
		scm.CapPollEvents:    true,
	}
}

func (p *Provider) VerifyWebhook(_ context.Context, req scm.WebhookRequest, secret string) error {
	return scm.VerifyTokenHeader(secret, req.Header("X-Gitlab-Token"))
}

func (p *Provider) ParseWebhook(_ context.Context, req scm.WebhookRequest) ([]scm.Event, error) {
	eventName := req.Header("X-Gitlab-Event")
	switch eventName {
	case "Merge Request Hook":
		return p.parseMergeRequest(req.Body)
	case "Push Hook":
		return p.parsePush(req.Body)
	case "Tag Push Hook":
		return p.parseTagPush(req.Body)
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

	project := url.PathEscape(repo.Owner + "/" + repo.Name)

	events := make([]scm.Event, 0)

	var mrs []struct {
		Iid            int        `json:"iid"`
		Title          string     `json:"title"`
		State          string     `json:"state"`
		UpdatedAt      time.Time  `json:"updated_at"`
		MergedAt       *time.Time `json:"merged_at"`
		MergeCommitSha string     `json:"merge_commit_sha"`
		SourceBranch   string     `json:"source_branch"`
		TargetBranch   string     `json:"target_branch"`
		Author         struct {
			Name string `json:"name"`
		} `json:"author"`
	}

	r, err := p.client.R().
		SetContext(ctx).
		SetQueryParams(map[string]string{
			"scope":         "all",
			"state":         "all",
			"per_page":      "100",
			"order_by":      "updated_at",
			"sort":          "desc",
			"updated_after": since.Format(time.RFC3339),
		}).
		SetResult(&mrs).
		Get("/projects/" + project + "/merge_requests")
	if err != nil {
		return nil, cursor, err
	}
	if r.IsError() {
		return nil, cursor, fmt.Errorf("gitlab api error: %s", r.Status())
	}
	for _, mr := range mrs {
		occurred := mr.UpdatedAt
		t := scm.EventTypeMergeRequest
		if mr.MergedAt != nil && !mr.MergedAt.IsZero() {
			occurred = *mr.MergedAt
			t = scm.EventTypeMergeRequestMerged
		}
		if !occurred.After(since) {
			continue
		}
		if occurred.After(next) {
			next = occurred
		}
		events = append(events, scm.Event{
			ProviderKind: scm.ProviderKindGitLab,
			EventType:    t,
			Repo:         repo,
			ActorName:    mr.Author.Name,
			CommitId:     mr.MergeCommitSha,
			OccurredAt:   occurred,
			Change: &scm.Change{
				Number:        mr.Iid,
				Title:         mr.Title,
				SourceBranch:  mr.SourceBranch,
				TargetBranch:  mr.TargetBranch,
				State:         mr.State,
				IsMerged:      t == scm.EventTypeMergeRequestMerged,
				MergeCommitId: mr.MergeCommitSha,
			},
		})
	}

	var tags []struct {
		Name   string `json:"name"`
		Commit struct {
			Id            string    `json:"id"`
			CommittedDate time.Time `json:"committed_date"`
		} `json:"commit"`
	}
	r, err = p.client.R().
		SetContext(ctx).
		SetQueryParam("per_page", "100").
		SetResult(&tags).
		Get("/projects/" + project + "/repository/tags")
	if err != nil {
		return nil, cursor, err
	}
	if r.IsError() {
		return nil, cursor, fmt.Errorf("gitlab api error: %s", r.Status())
	}
	for _, tag := range tags {
		if tag.Commit.CommittedDate.IsZero() || !tag.Commit.CommittedDate.After(since) {
			continue
		}
		if tag.Commit.CommittedDate.After(next) {
			next = tag.Commit.CommittedDate
		}
		events = append(events, scm.Event{
			ProviderKind: scm.ProviderKindGitLab,
			EventType:    scm.EventTypeTag,
			Repo:         repo,
			ActorName:    "",
			CommitId:     tag.Commit.Id,
			Ref:          "refs/tags/" + tag.Name,
			OccurredAt:   tag.Commit.CommittedDate,
			Raw: map[string]any{
				"name": tag.Name,
			},
		})
	}

	return events, scm.Cursor{Since: next}, nil
}

func (p *Provider) parseMergeRequest(body []byte) ([]scm.Event, error) {
	var payload struct {
		User struct {
			Name string `json:"name"`
		} `json:"user"`
		Project struct {
			WebUrl            string `json:"web_url"`
			PathWithNamespace string `json:"path_with_namespace"`
		} `json:"project"`
		ObjectAttributes struct {
			Iid          int    `json:"iid"`
			Title        string `json:"title"`
			State        string `json:"state"`
			Action       string `json:"action"`
			SourceBranch string `json:"source_branch"`
			TargetBranch string `json:"target_branch"`
			LastCommit   struct {
				Id string `json:"id"`
			} `json:"last_commit"`
			MergedAt       *time.Time `json:"merged_at"`
			MergeCommitSha string     `json:"merge_commit_sha"`
			UpdatedAt      *time.Time `json:"updated_at"`
		} `json:"object_attributes"`
	}
	if err := sonic.Unmarshal(body, &payload); err != nil {
		return nil, err
	}

	parts := strings.SplitN(payload.Project.PathWithNamespace, "/", 2)
	owner := ""
	name := ""
	if len(parts) == 2 {
		owner, name = parts[0], parts[1]
	}
	repo := scm.Repo{
		Owner:    owner,
		Name:     name,
		FullName: payload.Project.PathWithNamespace,
		Url:      payload.Project.WebUrl,
	}

	occurred := time.Now()
	if payload.ObjectAttributes.UpdatedAt != nil && !payload.ObjectAttributes.UpdatedAt.IsZero() {
		occurred = *payload.ObjectAttributes.UpdatedAt
	}
	t := scm.EventTypeMergeRequest
	if payload.ObjectAttributes.MergedAt != nil && !payload.ObjectAttributes.MergedAt.IsZero() {
		occurred = *payload.ObjectAttributes.MergedAt
		t = scm.EventTypeMergeRequestMerged
	}
	commitId := payload.ObjectAttributes.MergeCommitSha
	if commitId == "" {
		commitId = payload.ObjectAttributes.LastCommit.Id
	}

	return []scm.Event{{
		ProviderKind: scm.ProviderKindGitLab,
		EventType:    t,
		Repo:         repo,
		ActorName:    payload.User.Name,
		CommitId:     commitId,
		OccurredAt:   occurred,
		Change: &scm.Change{
			Number:        payload.ObjectAttributes.Iid,
			Title:         payload.ObjectAttributes.Title,
			SourceBranch:  payload.ObjectAttributes.SourceBranch,
			TargetBranch:  payload.ObjectAttributes.TargetBranch,
			State:         payload.ObjectAttributes.State,
			IsMerged:      t == scm.EventTypeMergeRequestMerged,
			MergeCommitId: payload.ObjectAttributes.MergeCommitSha,
		},
	}}, nil
}

type refPayload struct {
	Ref     string `json:"ref"`
	Project struct {
		WebUrl            string `json:"web_url"`
		PathWithNamespace string `json:"path_with_namespace"`
	} `json:"project"`
	UserName string `json:"user_name"`
	After    string `json:"after"`
}

func (p *Provider) parseRefEvent(body []byte, eventType scm.EventType) ([]scm.Event, error) {
	var payload refPayload
	if err := sonic.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	parts := strings.SplitN(payload.Project.PathWithNamespace, "/", 2)
	owner, name := "", ""
	if len(parts) == 2 {
		owner, name = parts[0], parts[1]
	}
	repo := scm.Repo{
		Owner:    owner,
		Name:     name,
		FullName: payload.Project.PathWithNamespace,
		Url:      payload.Project.WebUrl,
	}
	return []scm.Event{{
		ProviderKind: scm.ProviderKindGitLab,
		EventType:    eventType,
		Repo:         repo,
		ActorName:    payload.UserName,
		CommitId:     payload.After,
		Ref:          payload.Ref,
		OccurredAt:   time.Now(),
	}}, nil
}

func (p *Provider) parsePush(body []byte) ([]scm.Event, error) {
	return p.parseRefEvent(body, scm.EventTypePush)
}

func (p *Provider) parseTagPush(body []byte) ([]scm.Event, error) {
	return p.parseRefEvent(body, scm.EventTypeTag)
}

func init() {
	scm.Register(scm.ProviderKindGitLab, New)
}
