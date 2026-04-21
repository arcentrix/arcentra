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
	"fmt"
	"regexp"
	"strings"
)

// paramPlaceholderRE matches ${{ params.xxx }} placeholders in template spec content.
var paramPlaceholderRE = regexp.MustCompile(`\$\{\{\s*params\.([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}`)

// RenderSpec replaces ${{ params.xxx }} placeholders in specContent with
// values from the provided params map. Unknown placeholders are left
// unchanged; missing required params trigger an error.
func RenderSpec(specContent string, params map[string]any, schema []ParamSchema) (string, error) {
	merged, err := mergeWithDefaults(params, schema)
	if err != nil {
		return "", err
	}

	rendered := paramPlaceholderRE.ReplaceAllStringFunc(specContent, func(match string) string {
		subs := paramPlaceholderRE.FindStringSubmatch(match)
		if len(subs) < 2 {
			return match
		}
		key := subs[1]
		if val, ok := merged[key]; ok {
			return fmt.Sprintf("%v", val)
		}
		return match
	})

	return rendered, nil
}

// ValidateParams checks that all required parameters are provided and
// that enum values are valid.
func ValidateParams(params map[string]any, schema []ParamSchema) error {
	for _, p := range schema {
		val, exists := params[p.Name]
		if p.Required && (!exists || val == nil || val == "") {
			if p.Default == nil {
				return fmt.Errorf("required parameter %q is missing", p.Name)
			}
		}
		if exists && p.Type == "enum" && len(p.Options) > 0 {
			strVal := fmt.Sprintf("%v", val)
			if !stringSliceContains(p.Options, strVal) {
				return fmt.Errorf("parameter %q value %q is not in allowed options %v", p.Name, strVal, p.Options)
			}
		}
	}
	return nil
}

// mergeWithDefaults produces a final parameter map by applying default
// values from schema for any keys not supplied in params.
func mergeWithDefaults(params map[string]any, schema []ParamSchema) (map[string]any, error) {
	result := make(map[string]any, len(params))
	for k, v := range params {
		result[k] = v
	}
	for _, p := range schema {
		if _, exists := result[p.Name]; !exists && p.Default != nil {
			result[p.Name] = p.Default
		}
	}
	if err := ValidateParams(result, schema); err != nil {
		return nil, err
	}
	return result, nil
}

func stringSliceContains(slice []string, s string) bool {
	for _, item := range slice {
		if strings.EqualFold(item, s) {
			return true
		}
	}
	return false
}
