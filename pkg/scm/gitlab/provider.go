package gitlab

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
	return &Provider{cfg: cfg}, nil
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

	httpResp, err := request.NewRequest(
		p.apiBaseURL()+"/projects/"+project+"/merge_requests",
		fasthttp.MethodGet,
		map[string]string{
			"PRIVATE-TOKEN": strings.TrimSpace(p.cfg.Token),
		},
		nil,
	).WithQueryParams(map[string]string{
		"scope":         "all",
		"state":         "all",
		"per_page":      "100",
		"order_by":      "updated_at",
		"sort":          "desc",
		"updated_after": since.Format(time.RFC3339),
	}).WithResult(&mrs).Do(ctx)
	if err != nil {
		return nil, cursor, err
	}
	if httpResp == nil || httpResp.StatusCode() >= 400 {
		return nil, cursor, fmt.Errorf("gitlab api error: %d", httpResp.StatusCode())
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
			CommitID:     mr.MergeCommitSha,
			OccurredAt:   occurred,
			Change: &scm.Change{
				Number:        mr.Iid,
				Title:         mr.Title,
				SourceBranch:  mr.SourceBranch,
				TargetBranch:  mr.TargetBranch,
				State:         mr.State,
				IsMerged:      t == scm.EventTypeMergeRequestMerged,
				MergeCommitID: mr.MergeCommitSha,
			},
		})
	}

	var tags []struct {
		Name   string `json:"name"`
		Commit struct {
			ID            string    `json:"id"`
			CommittedDate time.Time `json:"committed_date"`
		} `json:"commit"`
	}
	httpResp, err = request.NewRequest(
		p.apiBaseURL()+"/projects/"+project+"/repository/tags",
		fasthttp.MethodGet,
		map[string]string{
			"PRIVATE-TOKEN": strings.TrimSpace(p.cfg.Token),
		},
		nil,
	).WithQueryParams(map[string]string{
		"per_page": "100",
	}).WithResult(&tags).Do(ctx)
	if err != nil {
		return nil, cursor, err
	}
	if httpResp == nil || httpResp.StatusCode() >= 400 {
		return nil, cursor, fmt.Errorf("gitlab api error: %d", httpResp.StatusCode())
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
			CommitID:     tag.Commit.ID,
			Ref:          "refs/tags/" + tag.Name,
			OccurredAt:   tag.Commit.CommittedDate,
			Raw: map[string]any{
				"name": tag.Name,
			},
		})
	}

	return events, scm.Cursor{Since: next}, nil
}

// CreateChangeRequest creates GitLab merge request and returns web URL.
func (p *Provider) CreateChangeRequest(ctx context.Context, req scm.ChangeRequestInput) (string, error) {
	repoInfo, ok := scm.ParseRepoFromURL(req.PipelineRepoURL)
	if !ok {
		return "", fmt.Errorf("invalid repository url: %s", req.PipelineRepoURL)
	}
	var out struct {
		WebURL string `json:"web_url"`
	}
	projectPath := url.PathEscape(repoInfo.Owner + "/" + repoInfo.Name)
	apiBase := strings.TrimSpace(p.cfg.APIBaseURL)
	if apiBase == "" {
		base := strings.TrimRight(strings.TrimSpace(p.cfg.BaseURL), "/")
		if base == "" {
			base = "https://gitlab.com"
		}
		apiBase = base + "/api/v4"
	}
	endpoint := strings.TrimRight(apiBase, "/") + "/projects/" + projectPath + "/merge_requests"
	resp, err := request.NewRequest(
		endpoint,
		fasthttp.MethodPost,
		map[string]string{
			"PRIVATE-TOKEN": strings.TrimSpace(p.cfg.Token),
		},
		nil,
	).
		WithBodyJSON(map[string]any{
			"source_branch": req.SourceBranch,
			"target_branch": req.TargetBranch,
			"title":         req.Title,
			"description":   "created by arcentra pipeline editor",
		}).
		WithResult(&out).
		Do(ctx)
	if err != nil {
		return "", err
	}
	if resp == nil || resp.StatusCode() >= 400 {
		return "", fmt.Errorf("gitlab create mr failed: %d", resp.StatusCode())
	}
	return out.WebURL, nil
}

func (p *Provider) apiBaseURL() string {
	apiBase := strings.TrimSpace(p.cfg.APIBaseURL)
	if apiBase == "" {
		base := strings.TrimRight(strings.TrimSpace(p.cfg.BaseURL), "/")
		if base == "" {
			base = "https://gitlab.com"
		}
		apiBase = base + "/api/v4"
	}
	return strings.TrimRight(apiBase, "/")
}

func (p *Provider) parseMergeRequest(body []byte) ([]scm.Event, error) {
	var payload struct {
		User struct {
			Name string `json:"name"`
		} `json:"user"`
		Project struct {
			WebURL            string `json:"web_url"`
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
				ID string `json:"id"`
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
		URL:      payload.Project.WebURL,
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
	commitID := payload.ObjectAttributes.MergeCommitSha
	if commitID == "" {
		commitID = payload.ObjectAttributes.LastCommit.ID
	}

	return []scm.Event{{
		ProviderKind: scm.ProviderKindGitLab,
		EventType:    t,
		Repo:         repo,
		ActorName:    payload.User.Name,
		CommitID:     commitID,
		OccurredAt:   occurred,
		Change: &scm.Change{
			Number:        payload.ObjectAttributes.Iid,
			Title:         payload.ObjectAttributes.Title,
			SourceBranch:  payload.ObjectAttributes.SourceBranch,
			TargetBranch:  payload.ObjectAttributes.TargetBranch,
			State:         payload.ObjectAttributes.State,
			IsMerged:      t == scm.EventTypeMergeRequestMerged,
			MergeCommitID: payload.ObjectAttributes.MergeCommitSha,
		},
	}}, nil
}

type refPayload struct {
	Ref     string `json:"ref"`
	Project struct {
		WebURL            string `json:"web_url"`
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
		URL:      payload.Project.WebURL,
	}
	return []scm.Event{{
		ProviderKind: scm.ProviderKindGitLab,
		EventType:    eventType,
		Repo:         repo,
		ActorName:    payload.UserName,
		CommitID:     payload.After,
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
