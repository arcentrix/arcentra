package service

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/internal/engine/repo"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/scm"
	_ "github.com/arcentrix/arcentra/pkg/scm/builtin"
	"github.com/bytedance/sonic"
	"gorm.io/datatypes"
)

const settingsScmKey = "scm"

type ScmService struct {
	projectRepo repo.IProjectRepository
}

// NewScmService creates a new scm service
func NewScmService(projectRepo repo.IProjectRepository) *ScmService {
	return &ScmService{projectRepo: projectRepo}
}

// HandleWebhook handles the scm webhook
func (s *ScmService) HandleWebhook(ctx context.Context, projectId string, headers map[string]string, body []byte) ([]scm.Event, error) {
	if projectId == "" {
		return nil, fmt.Errorf("project id is required")
	}
	project, err := s.projectRepo.GetProjectById(projectId)
	if err != nil {
		return nil, err
	}

	kind := mapRepoTypeToProviderKind(project.RepoType)
	if kind == "" {
		return nil, fmt.Errorf("unsupported repo type: %s", project.RepoType)
	}

	prov, err := scm.NewProvider(s.providerConfigFromProject(project, kind))
	if err != nil {
		return nil, err
	}

	req := scm.WebhookRequest{Headers: headers, Body: body}
	if err := prov.VerifyWebhook(ctx, req, project.WebhookSecret); err != nil {
		return nil, err
	}
	events, err := prov.ParseWebhook(ctx, req)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	for i := range events {
		if events[i].OccurredAt.IsZero() {
			events[i].OccurredAt = now
		}
		// ensure providerKind is filled
		if events[i].ProviderKind == "" {
			events[i].ProviderKind = kind
		}
	}
	return events, nil
}

// PollOnce polls the scm events
func (s *ScmService) PollOnce(ctx context.Context) error {
	query := &model.ProjectQueryReq{PageNum: 1, PageSize: 100}
	for {
		projects, total, err := s.projectRepo.ListProjects(query)
		if err != nil {
			return err
		}
		for _, p := range projects {
			if p == nil || p.IsEnabled != 1 {
				continue
			}
			if p.TriggerMode&(model.TriggerModeMR|model.TriggerModeTag) == 0 {
				continue
			}
			if err := s.pollProject(ctx, p); err != nil {
				log.Warnw("poll project scm events failed", "projectId", p.ProjectId, "error", err)
			}
		}
		if int64(query.PageNum*query.PageSize) >= total {
			break
		}
		query.PageNum++
	}
	return nil
}

// pollProject polls the scm events for a project
func (s *ScmService) pollProject(ctx context.Context, p *model.Project) error {
	kind := mapRepoTypeToProviderKind(p.RepoType)
	if kind == "" {
		return nil
	}
	repo, ok := parseRepoFromUrl(p.RepoUrl)
	if ok {
		repo.Url = p.RepoUrl
	}
	cursor, err := s.loadCursor(p, kind)
	if err != nil {
		return err
	}

	prov, err := scm.NewProvider(s.providerConfigFromProject(p, kind))
	if err != nil {
		return err
	}
	events, next, err := prov.PollEvents(ctx, repo, cursor)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}
	if err := s.saveCursor(p.ProjectId, kind, next); err != nil {
		return err
	}
	log.Infow("polled scm events", "projectId", p.ProjectId, "kind", kind, "count", len(events))
	return nil
}

// providerConfigFromProject creates the provider config from the project
func (s *ScmService) providerConfigFromProject(p *model.Project, kind scm.ProviderKind) scm.ProviderConfig {
	cfg := scm.ProviderConfig{
		Kind: kind,
	}
	base := baseUrlFromRepoUrl(p.RepoUrl)
	if base != "" {
		cfg.BaseUrl = base
	}
	if p.AuthType == model.AuthTypeToken && strings.TrimSpace(p.Credential) != "" {
		cfg.Token = strings.TrimSpace(p.Credential)
	}
	return cfg
}

// mapRepoTypeToProviderKind maps the repository type to the provider kind
func mapRepoTypeToProviderKind(repoType string) scm.ProviderKind {
	switch strings.ToLower(strings.TrimSpace(repoType)) {
	case model.RepoTypeGitHub:
		return scm.ProviderKindGitHub
	case model.RepoTypeGitLab:
		return scm.ProviderKindGitLab
	case model.RepoTypeGitee:
		return scm.ProviderKindGitee
	case "bitbucket":
		return scm.ProviderKindBitbucket
	case "gitea":
		return scm.ProviderKindGitea
	default:
		return ""
	}
}

// loadCursor loads the cursor from the project
func (s *ScmService) loadCursor(p *model.Project, kind scm.ProviderKind) (scm.Cursor, error) {
	if p == nil {
		return scm.Cursor{}, nil
	}
	settings, err := unmarshalSettings(p.Settings)
	if err != nil {
		return scm.Cursor{}, err
	}
	scmObj, _ := settings[settingsScmKey].(map[string]any)
	if scmObj == nil {
		return scm.Cursor{}, nil
	}
	cursors, _ := scmObj["cursors"].(map[string]any)
	if cursors == nil {
		return scm.Cursor{}, nil
	}
	raw, ok := cursors[string(kind)]
	if !ok {
		return scm.Cursor{}, nil
	}
	b, err := sonic.Marshal(raw)
	if err != nil {
		return scm.Cursor{}, nil
	}
	var c scm.Cursor
	if err := sonic.Unmarshal(b, &c); err != nil {
		return scm.Cursor{}, nil
	}
	return c, nil
}

// saveCursor saves the cursor to the project
func (s *ScmService) saveCursor(projectId string, kind scm.ProviderKind, cursor scm.Cursor) error {
	p, err := s.projectRepo.GetProjectById(projectId)
	if err != nil {
		return err
	}
	settings, err := unmarshalSettings(p.Settings)
	if err != nil {
		return err
	}
	scmObj, _ := settings[settingsScmKey].(map[string]any)
	if scmObj == nil {
		scmObj = map[string]any{}
		settings[settingsScmKey] = scmObj
	}
	cursors, _ := scmObj["cursors"].(map[string]any)
	if cursors == nil {
		cursors = map[string]any{}
		scmObj["cursors"] = cursors
	}
	cursors[string(kind)] = cursor

	b, err := sonic.Marshal(settings)
	if err != nil {
		return err
	}
	return s.projectRepo.UpdateProject(projectId, map[string]any{
		"settings": datatypes.JSON(b),
	})
}

// unmarshalSettings unmarshals the settings from the project
func unmarshalSettings(settings datatypes.JSON) (map[string]any, error) {
	if len(settings) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := sonic.Unmarshal(settings, &out); err != nil {
		// tolerate legacy non-json
		return map[string]any{}, nil
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

// baseUrlFromRepoUrl extracts the base URL from the repository URL
func baseUrlFromRepoUrl(repoUrl string) string {
	repoUrl = strings.TrimSpace(repoUrl)
	if repoUrl == "" {
		return ""
	}
	if strings.HasPrefix(repoUrl, "http://") || strings.HasPrefix(repoUrl, "https://") {
		u, err := url.Parse(repoUrl)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return ""
		}
		return u.Scheme + "://" + u.Host
	}
	// git@host:owner/name.git
	if i := strings.Index(repoUrl, "@"); i >= 0 {
		rest := repoUrl[i+1:]
		if j := strings.Index(rest, ":"); j >= 0 {
			host := rest[:j]
			if host != "" {
				return "https://" + host
			}
		}
	}
	return ""
}

// parseRepoFromUrl parses the repository from the URL
func parseRepoFromUrl(repoUrl string) (scm.Repo, bool) {
	repoUrl = strings.TrimSpace(repoUrl)
	if repoUrl == "" {
		return scm.Repo{}, false
	}
	if strings.HasPrefix(repoUrl, "http://") || strings.HasPrefix(repoUrl, "https://") {
		u, err := url.Parse(repoUrl)
		if err != nil {
			return scm.Repo{}, false
		}
		path := strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/")
		parts := strings.Split(path, "/")
		if len(parts) < 2 {
			return scm.Repo{}, false
		}
		owner := parts[len(parts)-2]
		name := parts[len(parts)-1]
		return scm.Repo{Host: u.Host, Owner: owner, Name: name, FullName: owner + "/" + name}, true
	}
	// git@host:owner/name.git
	if i := strings.Index(repoUrl, "@"); i >= 0 {
		rest := repoUrl[i+1:]
		host := ""
		path := ""
		if j := strings.Index(rest, ":"); j >= 0 {
			host = rest[:j]
			path = rest[j+1:]
		} else if strings.HasPrefix(rest, "ssh://") {
			u, err := url.Parse(rest)
			if err == nil {
				host = u.Host
				path = u.Path
			}
		}
		path = strings.Trim(strings.TrimSuffix(path, ".git"), "/")
		parts := strings.Split(path, "/")
		if len(parts) < 2 {
			return scm.Repo{}, false
		}
		owner := parts[len(parts)-2]
		name := parts[len(parts)-1]
		return scm.Repo{Host: host, Owner: owner, Name: name, FullName: owner + "/" + name}, true
	}
	return scm.Repo{}, false
}
