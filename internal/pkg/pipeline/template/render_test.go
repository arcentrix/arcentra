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
	"strings"
	"testing"
)

func TestRenderSpec_BasicSubstitution(t *testing.T) {
	spec := `namespace: "prod"
jobs:
  - name: build
    steps:
      - name: test
        uses: shell
        args:
          command: "go test ${{ params.test_flags }} ./..."
    env:
      GO_VERSION: "${{ params.go_version }}"`

	schema := []ParamSchema{
		{Name: "go_version", Type: "string", Default: "1.22"},
		{Name: "test_flags", Type: "string", Default: "-v"},
	}

	rendered, err := RenderSpec(spec, map[string]any{
		"go_version": "1.24",
		"test_flags": "-v -race",
	}, schema)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rendered, "1.24") {
		t.Error("expected go_version to be replaced with 1.24")
	}
	if !strings.Contains(rendered, "-v -race") {
		t.Error("expected test_flags to be replaced with -v -race")
	}
	if strings.Contains(rendered, "${{ params.") {
		t.Error("placeholders should be resolved")
	}
}

func TestRenderSpec_DefaultValues(t *testing.T) {
	spec := `image: "${{ params.image }}"`
	schema := []ParamSchema{
		{Name: "image", Type: "string", Default: "golang:1.22"},
	}

	rendered, err := RenderSpec(spec, map[string]any{}, schema)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rendered, "golang:1.22") {
		t.Errorf("expected default value, got: %s", rendered)
	}
}

func TestRenderSpec_MissingRequiredParam(t *testing.T) {
	spec := `name: "${{ params.app }}"`
	schema := []ParamSchema{
		{Name: "app", Type: "string", Required: true},
	}

	_, err := RenderSpec(spec, map[string]any{}, schema)
	if err == nil {
		t.Error("expected error for missing required param")
	}
}

func TestRenderSpec_UnknownPlaceholderLeftUnchanged(t *testing.T) {
	spec := `value: "${{ params.unknown }}"`
	rendered, err := RenderSpec(spec, map[string]any{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rendered, "${{ params.unknown }}") {
		t.Error("unknown placeholder should be left unchanged")
	}
}

func TestValidateParams_EnumValidation(t *testing.T) {
	schema := []ParamSchema{
		{Name: "env", Type: "enum", Options: []string{"dev", "staging", "prod"}},
	}
	if err := ValidateParams(map[string]any{"env": "prod"}, schema); err != nil {
		t.Errorf("valid enum should pass: %v", err)
	}
	if err := ValidateParams(map[string]any{"env": "invalid"}, schema); err == nil {
		t.Error("invalid enum should fail")
	}
}
