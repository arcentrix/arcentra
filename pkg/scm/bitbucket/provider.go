package bitbucket

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
		apiBase = "https://api.bitbucket.org/2.0"
	}
	p.client.SetBaseURL(strings.TrimRight(apiBase, "/"))
	if strings.TrimSpace(cfg.Token) != "" {
		p.client.SetAuthToken(cfg.Token)
	}
	return p, nil
}

func (p *Provider) Kind() scm.ProviderKind { return scm.ProviderKindBitbucket }

func (p *Provider) Capabilities() scm.CapSet {
	return scm.CapSet{
		scm.CapWebhookVerify: true,
		scm.CapWebhookParse:  true,
		scm.CapPollEvents:    true,
	}
}

func (p *Provider) VerifyWebhook(_ context.Context, req scm.WebhookRequest, secret string) error {
	return scm.VerifyHmacSha256Hex(req.Body, secret, req.Header("X-Hub-Signature"), "sha256=")
}

func (p *Provider) ParseWebhook(_ context.Context, req scm.WebhookRequest) ([]scm.Event, error) {
	key := req.Header("X-Event-Key")
	switch key {
	case "pullrequest:created", "pullrequest:updated", "pullrequest:fulfilled":
		return p.parsePullRequest(req.Body, key)
	case "repo:push":
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

	var prResp struct {
		Values []struct {
			Id          int       `json:"id"`
			Title       string    `json:"title"`
			State       string    `json:"state"`
			UpdatedOn   time.Time `json:"updated_on"`
			MergeCommit *struct {
				Hash string `json:"hash"`
			} `json:"merge_commit"`
			Author struct {
				User struct {
					DisplayName string `json:"display_name"`
				} `json:"user"`
			} `json:"author"`
			Source struct {
				Branch struct {
					Name string `json:"name"`
				} `json:"branch"`
			} `json:"source"`
			Destination struct {
				Branch struct {
					Name string `json:"name"`
				} `json:"branch"`
			} `json:"destination"`
		} `json:"values"`
	}

	path := fmt.Sprintf("/repositories/%s/%s/pullrequests", url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	r, err := p.client.R().
		SetContext(ctx).
		SetQueryParams(map[string]string{
			"state":   "ALL",
			"sort":    "-updated_on",
			"pagelen": "50",
		}).
		SetResult(&prResp).
		Get(path)
	if err != nil {
		return nil, cursor, err
	}
	if r.IsError() {
		return nil, cursor, fmt.Errorf("bitbucket api error: %s", r.Status())
	}
	for _, pr := range prResp.Values {
		if pr.UpdatedOn.IsZero() || !pr.UpdatedOn.After(since) {
			continue
		}
		if pr.UpdatedOn.After(next) {
			next = pr.UpdatedOn
		}
		t := scm.EventTypePullRequest
		merged := pr.State == "MERGED"
		if merged {
			t = scm.EventTypePullMerged
		}
		mergeSha := ""
		if pr.MergeCommit != nil {
			mergeSha = pr.MergeCommit.Hash
		}
		events = append(events, scm.Event{
			ProviderKind: scm.ProviderKindBitbucket,
			EventType:    t,
			Repo:         repo,
			ActorName:    pr.Author.User.DisplayName,
			CommitId:     mergeSha,
			OccurredAt:   pr.UpdatedOn,
			Change: &scm.Change{
				Number:        pr.Id,
				Title:         pr.Title,
				SourceBranch:  pr.Source.Branch.Name,
				TargetBranch:  pr.Destination.Branch.Name,
				State:         pr.State,
				IsMerged:      merged,
				MergeCommitId: mergeSha,
			},
		})
	}

	var tagResp struct {
		Values []struct {
			Name   string `json:"name"`
			Target struct {
				Hash string    `json:"hash"`
				Date time.Time `json:"date"`
			} `json:"target"`
		} `json:"values"`
	}
	r, err = p.client.R().
		SetContext(ctx).
		SetQueryParams(map[string]string{
			"sort":    "-target.date",
			"pagelen": "50",
		}).
		SetResult(&tagResp).
		Get(fmt.Sprintf("/repositories/%s/%s/refs/tags", url.PathEscape(repo.Owner), url.PathEscape(repo.Name)))
	if err != nil {
		return nil, cursor, err
	}
	if r.IsError() {
		return nil, cursor, fmt.Errorf("bitbucket api error: %s", r.Status())
	}
	for _, t := range tagResp.Values {
		if t.Target.Date.IsZero() || !t.Target.Date.After(since) {
			continue
		}
		if t.Target.Date.After(next) {
			next = t.Target.Date
		}
		events = append(events, scm.Event{
			ProviderKind: scm.ProviderKindBitbucket,
			EventType:    scm.EventTypeTag,
			Repo:         repo,
			CommitId:     t.Target.Hash,
			Ref:          "refs/tags/" + t.Name,
			OccurredAt:   t.Target.Date,
		})
	}

	return events, scm.Cursor{Since: next}, nil
}

func (p *Provider) parsePullRequest(body []byte, key string) ([]scm.Event, error) {
	var payload struct {
		PullRequest struct {
			Id          int    `json:"id"`
			Title       string `json:"title"`
			State       string `json:"state"`
			MergeCommit *struct {
				Hash string `json:"hash"`
			} `json:"merge_commit"`
			Author struct {
				DisplayName string `json:"display_name"`
			} `json:"author"`
			Source struct {
				Branch struct {
					Name string `json:"name"`
				} `json:"branch"`
			} `json:"source"`
			Destination struct {
				Branch struct {
					Name string `json:"name"`
				} `json:"branch"`
			} `json:"destination"`
			UpdatedOn time.Time `json:"updated_on"`
		} `json:"pullrequest"`
		Repository struct {
			FullName string `json:"full_name"`
			Name     string `json:"name"`
		} `json:"repository"`
		Actor struct {
			DisplayName string `json:"display_name"`
		} `json:"actor"`
	}
	if err := sonic.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	owner := ""
	name := payload.Repository.Name
	if parts := strings.SplitN(payload.Repository.FullName, "/", 2); len(parts) == 2 {
		owner = parts[0]
		name = parts[1]
	}
	repo := scm.Repo{
		Owner:    owner,
		Name:     name,
		FullName: payload.Repository.FullName,
	}
	mergeSha := ""
	if payload.PullRequest.MergeCommit != nil {
		mergeSha = payload.PullRequest.MergeCommit.Hash
	}
	t := scm.EventTypePullRequest
	if key == "pullrequest:fulfilled" || strings.ToUpper(payload.PullRequest.State) == "MERGED" {
		t = scm.EventTypePullMerged
	}
	occurred := payload.PullRequest.UpdatedOn
	if occurred.IsZero() {
		occurred = time.Now()
	}
	return []scm.Event{{
		ProviderKind: scm.ProviderKindBitbucket,
		EventType:    t,
		Repo:         repo,
		ActorName:    payload.Actor.DisplayName,
		CommitId:     mergeSha,
		OccurredAt:   occurred,
		Change: &scm.Change{
			Number:        payload.PullRequest.Id,
			Title:         payload.PullRequest.Title,
			SourceBranch:  payload.PullRequest.Source.Branch.Name,
			TargetBranch:  payload.PullRequest.Destination.Branch.Name,
			State:         payload.PullRequest.State,
			IsMerged:      t == scm.EventTypePullMerged,
			MergeCommitId: mergeSha,
		},
	}}, nil
}

func (p *Provider) parsePush(body []byte) ([]scm.Event, error) {
	var payload struct {
		Repository struct {
			FullName string `json:"full_name"`
			Name     string `json:"name"`
		} `json:"repository"`
		Actor struct {
			DisplayName string `json:"display_name"`
		} `json:"actor"`
		Push struct {
			Changes []struct {
				New *struct {
					Type   string `json:"type"`
					Name   string `json:"name"`
					Target struct {
						Hash string    `json:"hash"`
						Date time.Time `json:"date"`
					} `json:"target"`
				} `json:"new"`
			} `json:"changes"`
		} `json:"push"`
	}
	if err := sonic.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	owner := ""
	name := payload.Repository.Name
	if parts := strings.SplitN(payload.Repository.FullName, "/", 2); len(parts) == 2 {
		owner = parts[0]
		name = parts[1]
	}
	repo := scm.Repo{
		Owner:    owner,
		Name:     name,
		FullName: payload.Repository.FullName,
	}
	events := make([]scm.Event, 0)
	for _, c := range payload.Push.Changes {
		if c.New == nil {
			continue
		}
		if strings.ToLower(c.New.Type) != "tag" {
			continue
		}
		occurred := c.New.Target.Date
		if occurred.IsZero() {
			occurred = time.Now()
		}
		events = append(events, scm.Event{
			ProviderKind: scm.ProviderKindBitbucket,
			EventType:    scm.EventTypeTag,
			Repo:         repo,
			ActorName:    payload.Actor.DisplayName,
			CommitId:     c.New.Target.Hash,
			Ref:          "refs/tags/" + c.New.Name,
			OccurredAt:   occurred,
		})
	}
	return events, nil
}

func init() {
	scm.Register(scm.ProviderKindBitbucket, New)
}
