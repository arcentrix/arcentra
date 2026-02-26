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

package outbox

import (
	"path/filepath"
	"strings"
	"unicode"
)

// sanitizeScope replaces invalid path characters with underscore and limits length.
func sanitizeScope(s string) string {
	if len(s) > MaxScopeLen {
		s = s[:MaxScopeLen]
	}
	var b strings.Builder
	for _, r := range s {
		if r == '.' || r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// buildWALDir builds the WAL directory from config.
func buildWALDir(cfg *Config) string {
	parts := []string{cfg.WALDir}
	if cfg.ProjectId != "" && cfg.PipelineId != "" {
		parts = append(parts, sanitizeScope(cfg.ProjectId), sanitizeScope(cfg.PipelineId))
	}
	parts = append(parts, sanitizeScope(cfg.AgentId))
	return filepath.Join(parts...)
}
