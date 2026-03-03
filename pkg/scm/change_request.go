package scm

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// CreateChangeRequest creates a provider PR/MR using unified input.
func CreateChangeRequest(req ChangeRequestInput) (string, error) {
	token := strings.TrimSpace(req.Credential)
	if req.AuthType != AuthTypeToken || token == "" {
		return "", fmt.Errorf("project token credential is required for PR mode")
	}
	kind, err := providerKindFromRepoType(req.RepoType)
	if err != nil {
		return "", err
	}

	apiBase := ""
	if kind == ProviderKindGitLab || kind == ProviderKindGitea {
		apiBase = extractBaseURL(req.PipelineRepoURL)
	}
	provider, err := NewProvider(ProviderConfig{
		Kind:       kind,
		BaseURL:    extractBaseURL(req.ProjectRepoURL),
		APIBaseURL: apiBase,
		Token:      token,
	})
	if err != nil {
		return "", err
	}
	return provider.CreateChangeRequest(context.Background(), req)
}

func providerKindFromRepoType(repoType string) (ProviderKind, error) {
	switch strings.ToLower(strings.TrimSpace(repoType)) {
	case string(ProviderKindGitHub):
		return ProviderKindGitHub, nil
	case string(ProviderKindGitLab):
		return ProviderKindGitLab, nil
	case string(ProviderKindGitea):
		return ProviderKindGitea, nil
	case string(ProviderKindGitee):
		return ProviderKindGitee, nil
	case string(ProviderKindBitbucket):
		return ProviderKindBitbucket, nil
	default:
		return "", fmt.Errorf("repo type '%s' does not support api pull request creation", repoType)
	}
}

type RepoRef struct {
	Owner string
	Name  string
}

// ParseRepoFromURL parses owner/name from HTTPS or SSH repository URL.
func ParseRepoFromURL(repoURL string) (RepoRef, bool) {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return RepoRef{}, false
	}
	if strings.HasPrefix(repoURL, "http://") || strings.HasPrefix(repoURL, "https://") {
		u, err := url.Parse(repoURL)
		if err != nil {
			return RepoRef{}, false
		}
		path := strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/")
		parts := strings.Split(path, "/")
		if len(parts) < 2 {
			return RepoRef{}, false
		}
		return RepoRef{Owner: parts[len(parts)-2], Name: parts[len(parts)-1]}, true
	}
	if strings.HasPrefix(repoURL, "git@") {
		parts := strings.SplitN(repoURL, ":", 2)
		if len(parts) != 2 {
			return RepoRef{}, false
		}
		path := strings.Trim(strings.TrimSuffix(parts[1], ".git"), "/")
		sub := strings.Split(path, "/")
		if len(sub) < 2 {
			return RepoRef{}, false
		}
		return RepoRef{Owner: sub[len(sub)-2], Name: sub[len(sub)-1]}, true
	}
	return RepoRef{}, false
}

// extractBaseURL returns scheme://host part from repository URL.
func extractBaseURL(repoURL string) string {
	repoURL = strings.TrimSpace(repoURL)
	if strings.HasPrefix(repoURL, "http://") || strings.HasPrefix(repoURL, "https://") {
		u, err := url.Parse(repoURL)
		if err != nil {
			return ""
		}
		return u.Scheme + "://" + u.Host
	}
	if strings.HasPrefix(repoURL, "git@") {
		parts := strings.SplitN(repoURL, ":", 2)
		if len(parts) != 2 {
			return ""
		}
		host := strings.TrimPrefix(parts[0], "git@")
		if host == "" {
			return ""
		}
		return "https://" + host
	}
	return ""
}
