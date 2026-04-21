// Copyright 2026 Arcentra Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package template

import (
	"context"
	"strings"
	"testing"
)

// mockResolver is a test double for ITemplateResolver.
type mockResolver struct {
	templates map[string]string
}

func (m *mockResolver) ResolveTemplate(_ context.Context, name, _, _ string, _ map[string]any, _, _ string) (string, error) {
	if spec, ok := m.templates[name]; ok {
		return spec, nil
	}
	return "", nil
}

func TestResolveIncludes_NoInclude(t *testing.T) {
	content := `namespace: "prod"
jobs:
  - name: build
    steps:
      - name: test
        uses: shell`

	result, err := ResolveIncludes(context.Background(), content, &mockResolver{}, "project", "proj-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "build") {
		t.Error("original content should be preserved")
	}
}

func TestResolveIncludes_MergeJobs(t *testing.T) {
	content := `namespace: "prod"
include:
  - template: "go-ci"
jobs:
  - name: deploy
    steps:
      - name: apply
        uses: k8s-deploy`

	resolver := &mockResolver{
		templates: map[string]string{
			"go-ci": `jobs:
  - name: build
    steps:
      - name: test
        uses: shell`,
		},
	}

	result, err := ResolveIncludes(context.Background(), content, resolver, "project", "proj-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(result, "include") {
		t.Error("include directive should be removed from output")
	}
	if !strings.Contains(result, "build") {
		t.Error("template jobs should be merged")
	}
	if !strings.Contains(result, "deploy") {
		t.Error("main jobs should be preserved")
	}
}

func TestResolveIncludes_MergeVariables(t *testing.T) {
	content := `namespace: "prod"
variables:
  APP_NAME: "my-app"
  REGISTRY: "docker.io"
include:
  - template: "base"
jobs:
  - name: build
    steps:
      - name: test
        uses: shell`

	resolver := &mockResolver{
		templates: map[string]string{
			"base": `variables:
  REGISTRY: "quay.io"
  LOG_LEVEL: "info"
jobs:
  - name: setup
    steps:
      - name: init
        uses: shell`,
		},
	}

	result, err := ResolveIncludes(context.Background(), content, resolver, "system", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "my-app") {
		t.Error("main APP_NAME should be present")
	}
	if !strings.Contains(result, "info") {
		t.Error("template LOG_LEVEL should be merged")
	}
	// main REGISTRY should win over template's
	if strings.Contains(result, "quay.io") {
		t.Error("main REGISTRY should override template's REGISTRY")
	}
}

func TestResolveIncludes_NilResolver(t *testing.T) {
	content := `namespace: "prod"
include:
  - template: "go-ci"
jobs:
  - name: build
    steps:
      - name: test
        uses: shell`

	result, err := ResolveIncludes(context.Background(), content, nil, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != content {
		t.Error("nil resolver should return content unchanged")
	}
}

func TestResolveIncludes_MissingTemplateName(t *testing.T) {
	content := `namespace: "prod"
include:
  - version: "v1.0.0"
jobs:
  - name: build
    steps:
      - name: test
        uses: shell`

	_, err := ResolveIncludes(context.Background(), content, &mockResolver{}, "", "")
	if err == nil {
		t.Error("expected error for missing template name")
	}
}
