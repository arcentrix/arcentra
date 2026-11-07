// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package validation

import (
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
)

func TestOfflineSourceUsesHardenedAgentTransportContract(t *testing.T) {
	for _, tc := range []struct {
		name, repository string
		auth             *spec.SourceAuth
		allowed          bool
	}{
		{"https", "https://example.invalid/team/repo.git", nil, true},
		{"https_credentials", "https://example.invalid/team/repo.git", &spec.SourceAuth{Token: "synthetic"}, true},
		{"native", "git://example.invalid/team/repo.git", nil, true},
		{"native_loopback", "git://127.0.0.1:19418/repo", nil, true},
		{"native_ipv6", "git://[::1]:19418/repo", nil, true},
		{"http_loopback", "http://127.0.0.1:8080/repo", nil, true},
		{"http_remote", "http://example.invalid/repo", nil, false},
		{"http_named_localhost", "http://localhost/repo", nil, false},
		{"http_auth", "http://127.0.0.1/repo", &spec.SourceAuth{Password: "synthetic"}, false},
		{"native_auth", "git://example.invalid/repo", &spec.SourceAuth{Token: "synthetic"}, false},
		{"native_empty_auth_object", "git://example.invalid/repo", &spec.SourceAuth{}, false},
		{"ssh", "ssh://example.invalid/repo", nil, false},
		{"scp", "git@example.invalid:repo", nil, false},
		{"host_path", "/tmp/repo", nil, false},
		{"file", "file:///tmp/repo", nil, false},
		{"helper", "ext::sh -c synthetic", nil, false},
		{"helper_prefix", "git::https://example.invalid/repo", nil, false},
		{"userinfo", "git://user:synthetic@example.invalid/repo", nil, false},
		{"query", "git://example.invalid/repo?token=synthetic", nil, false},
		{"empty_query", "git://example.invalid/repo?", nil, false},
		{"fragment", "git://example.invalid/repo#x", nil, false},
		{"encoded", "git://example.invalid/%72epo", nil, false},
		{"traversal", "git://example.invalid/a/../repo", nil, false},
		{"empty_path", "git://example.invalid", nil, false},
		{"invalid_port", "git://example.invalid:99999/repo", nil, false},
		{"control", "git://example.invalid/repo\n", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, policy := schemaRuntimeFixture()
			p.Jobs[0].Source = &spec.Source{Type: "git", Repo: tc.repository, Auth: tc.auth}
			err := NewSchemaValidatorWithRuntimePolicy(policy).Validate(p)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v error=%v", tc.allowed, err)
			}
			if err != nil && (strings.Contains(err.Error(), tc.repository) || strings.Contains(err.Error(), "synthetic")) {
				t.Fatal("validation exposed source or credentials")
			}
		})
	}
}
func TestOfflineSourceTransportDoesNotChangeIndependentLocalRuntimeValidation(t *testing.T) {
	p, policy := schemaRuntimeFixture()
	p.Runtime = nil
	p.Jobs[0].Source = &spec.Source{Type: "git", Repo: "ssh://example.invalid/repo"}
	if _, err := ResolveRuntime(p, policy); err != nil {
		t.Fatal(err)
	}
}
