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

// Package template provides Git-backed template library synchronisation,
// parameter rendering, semantic versioning, and the include directive
// resolver for pipeline specs.
package template

// LibraryManifest represents the library.yaml file at the root of a
// template library Git repository.
type LibraryManifest struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
}

// Manifest represents the template.yaml file inside each
// template directory (e.g. templates/go-ci/template.yaml).
type Manifest struct {
	Name        string        `yaml:"name" json:"name"`
	Description string        `yaml:"description" json:"description"`
	Category    string        `yaml:"category" json:"category"`
	Tags        []string      `yaml:"tags" json:"tags"`
	Icon        string        `yaml:"icon" json:"icon"`
	Params      []ParamSchema `yaml:"params" json:"params"`
}

// ParamSchema describes a single template parameter as declared in
// the template.yaml manifest. It mirrors model.TemplateParam but is
// used during Git sync (YAML parsing) rather than DB operations.
type ParamSchema struct {
	Name        string   `yaml:"name" json:"name"`
	Type        string   `yaml:"type" json:"type"` // string / boolean / number / enum
	Default     any      `yaml:"default" json:"default,omitempty"`
	Description string   `yaml:"description" json:"description,omitempty"`
	Required    bool     `yaml:"required" json:"required"`
	Options     []string `yaml:"options" json:"options,omitempty"`
}

// IncludeEntry represents a single item in the pipeline YAML include
// directive, e.g.:
//
//	include:
//	  - template: "go-ci"
//	    version: "v1.2.0"
//	    library: "official"
//	    params:
//	      go_version: "1.24"
type IncludeEntry struct {
	Template string         `yaml:"template" json:"template"`
	Version  string         `yaml:"version" json:"version,omitempty"`
	Library  string         `yaml:"library" json:"library,omitempty"`
	Params   map[string]any `yaml:"params" json:"params,omitempty"`
}

// DiscoveredTemplate holds the parsed content of a single template
// directory discovered during a library sync.
type DiscoveredTemplate struct {
	DirName     string
	Manifest    Manifest
	SpecContent string
	Readme      string
}
