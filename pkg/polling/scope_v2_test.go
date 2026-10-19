package polling

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestLegacyScopeGolden(t *testing.T) {
	s := testScope()
	raw, err := s.Canonical()
	require.NoError(t, err)
	require.Equal(t, `{"version":1,"projectId":"project","provider":"gitlab","apiOrigin":"https://gitlab.example.com","repository":"https://gitlab.example.com/group/nested/repo","providerProjectId":"42","stream":"merge_requests"}`, raw)
	d, err := s.Digest()
	require.NoError(t, err)
	require.Equal(t, "v1:ae4a5ee145059a373dcb8fa0825de7ee1a078af71aadc6b30d96dbbf52bd1541", d)
	key, err := EventRequestID(s, "mr:42:7:fingerprint")
	require.NoError(t, err)
	require.Equal(t, "poll:v1:8cd2ed9b43e9cd841a8fd146e01a9226617bebfe98ddf4f181240543d54729d0", key)
}

func TestProviderScopeV2Policy(t *testing.T) {
	for _, provider := range []string{"github", "gitea"} {
		t.Run(provider, func(t *testing.T) {
			repository, origin := "https://github.com/owner/repo", "https://api.github.com"
			if provider == "gitea" {
				repository, origin = "https://gitea.example.com:8443/owner/repo", "https://gitea.example.com:8443"
			}
			s := Scope{Version: ScopeVersion2, ProjectID: "project", Provider: provider, APIOrigin: origin, Repository: repository, ProviderProjectID: "42", Stream: "pull_requests"}
			require.NoError(t, s.Validate())
			for name, mutate := range map[string]func(*Scope){
				"legacy_version": func(s *Scope) { s.Version = 1 }, "unknown_version": func(s *Scope) { s.Version = 3 },
				"unknown_provider": func(s *Scope) { s.Provider = "gitee" }, "provider_confusion": func(s *Scope) { s.Provider = "gitlab" },
				"wrong_stream": func(s *Scope) { s.Stream = "merge_requests" }, "custom_api": func(s *Scope) { s.APIOrigin = "https://other.example.com" },
				"api_path": func(s *Scope) { s.APIOrigin += "/api/v1" }, "nested_repo": func(s *Scope) { s.Repository += "/nested" },
				"zero_id": func(s *Scope) { s.ProviderProjectID = "0" }, "negative_id": func(s *Scope) { s.ProviderProjectID = "-1" },
				"padded_id": func(s *Scope) { s.ProviderProjectID = "042" }, "overflow_id": func(s *Scope) { s.ProviderProjectID = "9223372036854775808" },
				"credentials":  func(s *Scope) { s.Repository = "https://user:secret@github.com/owner/repo" },
				"encoded_path": func(s *Scope) { s.Repository += "%2fother" }, "dot_path": func(s *Scope) { s.Repository += "/../repo" },
				"query": func(s *Scope) { s.Repository += "?x=y" }, "fragment": func(s *Scope) { s.Repository += "#x" },
				"invalid_name": func(s *Scope) { s.Repository += ";x" }, "backslash": func(s *Scope) { s.Repository += "\\x" },
				"space": func(s *Scope) { s.Repository += " x" }, "noncanonical": func(s *Scope) { s.Repository += ".git" },
			} {
				t.Run(name, func(t *testing.T) { v := s; mutate(&v); require.Error(t, v.Validate()) })
			}
			original, err := EventRequestID(s, "same-observation")
			require.NoError(t, err)
			for _, mutate := range []func(*Scope){func(s *Scope) { s.ProjectID = "other" }, func(s *Scope) { s.ProviderProjectID = "43" }, func(s *Scope) { s.Repository += "-other" }} {
				v := s
				mutate(&v)
				key, err := EventRequestID(v, "same-observation")
				require.NoError(t, err)
				require.NotEqual(t, original, key)
			}
		})
	}
	for _, repo := range []string{"https://github.com:443/owner/repo", "https://enterprise.example.com/owner/repo", "https://api.github.com/owner/repo", "https://github.com./owner/repo"} {
		_, _, _, err := ProviderRepository("github", repo)
		require.Error(t, err)
	}
	s := testScope()
	s.Version = ScopeVersion2
	require.Error(t, s.Validate())
	github := Scope{Version: 2, ProjectID: "project", Provider: "github", APIOrigin: "https://api.github.com", Repository: "https://github.com/owner/repo", ProviderProjectID: "42", Stream: "pull_requests"}
	gitea := github
	gitea.Provider = "gitea"
	gitea.APIOrigin = "https://github.com"
	a, err := EventRequestID(github, "same")
	require.NoError(t, err)
	b, err := EventRequestID(gitea, "same")
	require.NoError(t, err)
	require.NotEqual(t, a, b)
}
