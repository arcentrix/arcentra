// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

// RerunShellStep identifies the existing offline shell action contract. It does
// not validate arguments, runtime policy, approval or execution authority.
func RerunShellStep(uses, action string) bool {
	return (uses == "shell" || uses == "builtin:shell") && (action == "" || action == "run")
}

// RerunArtifactUploadStep identifies output publication only. Downloads and
// arbitrary plugins remain outside the initial terminal re-execution contract.
func RerunArtifactUploadStep(uses, action string) bool {
	return uses == "builtin:artifacts" && action == "upload"
}
