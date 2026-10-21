//go:build !sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

func terminalRerunAcceptanceBuild(OfflineRuntimePolicy) bool { return false }
