// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package validation

import "strings"

// This is an early diagnostic for explicit SSH transport, not a replacement for
// the hardened Agent classifier after variable evaluation. Entirely dynamic
// URLs remain subject to that runtime check. Truly local backend jobs do not
// pass through this Agent-only restriction.
func explicitSSHSource(raw string) bool {
	source := strings.TrimSpace(raw)
	if scheme, _, ok := strings.Cut(source, "://"); ok {
		switch strings.ToLower(scheme) {
		case "ssh", "git+ssh", "ssh+git":
			return true
		}
		return false
	}
	// Git interprets a colon before any path separator as scp-like host:path.
	// Preserve Windows drive paths as a separate, unrelated transport contract.
	colon := strings.IndexByte(source, ':')
	if colon <= 0 || strings.ContainsAny(source[:colon], "/\\") {
		return false
	}
	if colon == 1 && len(source) > 2 && ((source[0] >= 'A' && source[0] <= 'Z') || (source[0] >= 'a' && source[0] <= 'z')) && (source[2] == '/' || source[2] == '\\') {
		return false
	}
	return true
}
