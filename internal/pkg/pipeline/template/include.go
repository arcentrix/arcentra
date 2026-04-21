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
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"go.yaml.in/yaml/v3"
)

// ITemplateResolver resolves template references from include directives.
// Implemented by PipelineTemplateService in the service layer.
type ITemplateResolver interface {
	// ResolveTemplate returns the rendered spec content for a template
	// identified by name/version/library within the given scope.
	// The params map provides user-supplied parameter values.
	ResolveTemplate(ctx context.Context, name, version, library string, params map[string]any, scope, scopeID string) (string, error)
}

// ResolveIncludes pre-processes raw pipeline YAML/JSON content by
// expanding include directives. For each include entry it fetches the
// template spec from the resolver, renders parameters, and merges the
// resulting jobs and variables into the main spec.
//
// If no include field is present the content is returned unchanged.
// Recursive includes (include inside a template) are forbidden.
func ResolveIncludes(ctx context.Context, content string, resolver ITemplateResolver, scope, scopeID string) (string, error) {
	if resolver == nil {
		return content, nil
	}

	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return content, nil
	}

	var doc map[string]any
	if err := yaml.Unmarshal([]byte(trimmed), &doc); err != nil {
		// JSON fallback
		if jsonErr := sonic.UnmarshalString(trimmed, &doc); jsonErr != nil {
			return content, nil
		}
	}

	rawIncludes, ok := doc["include"]
	if !ok {
		return content, nil
	}

	entries, err := parseIncludeEntries(rawIncludes)
	if err != nil {
		return "", fmt.Errorf("parse include: %w", err)
	}
	if len(entries) == 0 {
		return content, nil
	}

	mainJobs := extractJobList(doc)
	mainVars := extractVariableMap(doc)

	for _, entry := range entries {
		if strings.TrimSpace(entry.Template) == "" {
			return "", fmt.Errorf("include entry missing template name")
		}

		rendered, resolveErr := resolver.ResolveTemplate(ctx, entry.Template, entry.Version, entry.Library, entry.Params, scope, scopeID)
		if resolveErr != nil {
			return "", fmt.Errorf("resolve template %q: %w", entry.Template, resolveErr)
		}

		var tmplDoc map[string]any
		if yamlErr := yaml.Unmarshal([]byte(rendered), &tmplDoc); yamlErr != nil {
			if jsonErr := sonic.UnmarshalString(rendered, &tmplDoc); jsonErr != nil {
				return "", fmt.Errorf("parse rendered template %q: %w", entry.Template, yamlErr)
			}
		}

		tmplJobs := extractJobList(tmplDoc)
		mainJobs = append(tmplJobs, mainJobs...)

		tmplVars := extractVariableMap(tmplDoc)
		for k, v := range tmplVars {
			if _, exists := mainVars[k]; !exists {
				mainVars[k] = v
			}
		}
	}

	delete(doc, "include")

	if len(mainJobs) > 0 {
		doc["jobs"] = mainJobs
	}
	if len(mainVars) > 0 {
		doc["variables"] = mainVars
	}

	out, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("marshal expanded spec: %w", err)
	}
	return string(out), nil
}

// parseIncludeEntries converts the raw include value (typically a YAML
// list) into typed IncludeEntry structs.
func parseIncludeEntries(raw any) ([]IncludeEntry, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("include must be an array")
	}

	var entries []IncludeEntry
	for _, item := range list {
		b, err := sonic.Marshal(item)
		if err != nil {
			return nil, err
		}
		var entry IncludeEntry
		if err := sonic.Unmarshal(b, &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// extractJobList returns the jobs list from a parsed YAML/JSON document.
func extractJobList(doc map[string]any) []any {
	jobs, ok := doc["jobs"]
	if !ok {
		return nil
	}
	list, ok := jobs.([]any)
	if !ok {
		return nil
	}
	return list
}

// extractVariableMap returns the variables map from a parsed document.
func extractVariableMap(doc map[string]any) map[string]any {
	vars, ok := doc["variables"]
	if !ok {
		return make(map[string]any)
	}
	m, ok := vars.(map[string]any)
	if !ok {
		return make(map[string]any)
	}
	result := make(map[string]any, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}
