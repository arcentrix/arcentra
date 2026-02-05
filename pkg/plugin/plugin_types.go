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

// Package plugin type definitions and helper functions
package plugin

import (
	"fmt"
)

// AllPluginTypes returns all supported plugin types
func AllPluginTypes() []PluginType {
	return []PluginType{
		TypeSource,
		TypeBuild,
		TypeTest,
		TypeDeploy,
		TypeSecurity,
		TypeNotify,
		TypeApproval,
		TypeStorage,
		TypeAnalytics,
		TypeIntegration,
		TypeCustom,
	}
}

// IsValidPluginType checks if a plugin type is valid
func IsValidPluginType(t string) bool {
	for _, validType := range AllPluginTypes() {
		if PluginTypeToString(validType) == t {
			return true
		}
	}
	return false
}

// PluginTypeToString converts PluginType enum to string
func PluginTypeToString(pt PluginType) string {
	switch pt {
	case TypeSource:
		return "source"
	case TypeBuild:
		return "build"
	case TypeTest:
		return "test"
	case TypeDeploy:
		return "deploy"
	case TypeSecurity:
		return "security"
	case TypeNotify:
		return "notify"
	case TypeApproval:
		return "approval"
	case TypeStorage:
		return "storage"
	case TypeAnalytics:
		return "analytics"
	case TypeIntegration:
		return "integration"
	case TypeCustom:
		return "custom"
	default:
		return "unknown"
	}
}

// StringToPluginType converts string to PluginType enum
func StringToPluginType(s string) PluginType {
	switch s {
	case "source":
		return TypeSource
	case "build":
		return TypeBuild
	case "test":
		return TypeTest
	case "deploy":
		return TypeDeploy
	case "security":
		return TypeSecurity
	case "notify":
		return TypeNotify
	case "approval":
		return TypeApproval
	case "storage":
		return TypeStorage
	case "analytics":
		return TypeAnalytics
	case "integration":
		return TypeIntegration
	case "custom":
		return TypeCustom
	default:
		return TypeUnspecified
	}
}

// GetPluginTypeDescription returns a description for a plugin type
func GetPluginTypeDescription(t PluginType) string {
	descriptions := map[PluginType]string{
		TypeSource:      "Source code management plugin for repository operations (clone, pull, checkout, etc.)",
		TypeBuild:       "Build plugin for compiling and building projects (compile, package, generate artifacts, etc.)",
		TypeTest:        "Test plugin for running tests and generating reports (unit tests, integration tests, coverage, etc.)",
		TypeDeploy:      "Deployment plugin for application deployment and management (deploy, rollback, scaling, etc.)",
		TypeSecurity:    "Security plugin for security scanning and auditing (vulnerability scanning, compliance checks, etc.)",
		TypeNotify:      "Notification plugin for sending various notifications (email, webhook, instant messaging, etc.)",
		TypeApproval:    "Approval plugin for approval workflow management (create approval, approve, reject, etc.)",
		TypeStorage:     "Storage plugin for data storage and management (save, load, delete, list, etc.)",
		TypeAnalytics:   "Analytics plugin for data analysis and reporting (event tracking, queries, metrics, reports, etc.)",
		TypeIntegration: "Integration plugin for third-party service integration (connect, call, subscribe, etc.)",
		TypeCustom:      "Custom plugin for special-purpose customized functionality",
	}
	return descriptions[t]
}

// PluginTypeString returns the string representation of PluginType
func PluginTypeString(pt PluginType) string {
	return PluginTypeToString(pt)
}

// ValidatePluginType validates the PluginType
func ValidatePluginType(pt PluginType) error {
	if pt == TypeUnspecified {
		return fmt.Errorf("invalid plugin type: %s", PluginTypeToString(pt))
	}
	return nil
}
