// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

// TerminalRerunAccepted is independently opened only after real acceptance.
const TerminalRerunAccepted = false

// TerminalRerunAllowed never replaces the independent operator opt-in.
func TerminalRerunAllowed(policy OfflineRuntimePolicy) bool {
	return policy.Enabled && policy.AllowWholeJobRetry && policy.AllowWholeJobRetryApproval && policy.ValidateProduction() == nil && (TerminalRerunAccepted || terminalRerunAcceptanceBuild(policy))
}
