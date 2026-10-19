// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package authz

import (
	"strings"
	"unicode"

	"github.com/arcentrix/arcentra/internal/control/model"
)

// CanonicalPermissionAction separates dictionary identifiers from policy actions.
// Do not normalize malformed fields into a different, potentially wider grant.
func CanonicalPermissionAction(permission model.Permission) (string, bool) {
	for _, component := range []string{permission.ResourceType, permission.Action} {
		if component == "" || strings.Contains(component, ":") || strings.IndexFunc(component, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			return "", false
		}
	}
	return permission.ResourceType + ":" + permission.Action, true
}
