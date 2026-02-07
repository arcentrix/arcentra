// Copyright 2025 Arcentra Team
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

package executor

import (
	"strings"
	"unicode"
)

func normalizeAnyMap(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	default:
		return nil, false
	}
}

func normalizeMapKeys(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	result := make(map[string]any, len(input))
	for key, value := range input {
		if key == "" {
			continue
		}
		newKey := toCamelCase(key)
		result[newKey] = value
	}
	return result
}

func toCamelCase(value string) string {
	if value == "" {
		return value
	}
	parts := strings.Split(value, "_")
	if len(parts) == 1 {
		return value
	}
	builder := strings.Builder{}
	for index, part := range parts {
		if part == "" {
			continue
		}
		if index == 0 {
			builder.WriteString(part)
			continue
		}
		builder.WriteString(upperFirst(part))
	}
	return builder.String()
}

func upperFirst(value string) string {
	if value == "" {
		return value
	}
	runes := []rune(value)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}
