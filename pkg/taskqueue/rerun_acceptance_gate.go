//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"os"
)

// Dedicated existing offline-container acceptance build only. Ordinary release
// binaries do not contain this environment-based test capability.
func terminalRerunAcceptanceBuild(runtime OfflineRuntimePolicy) bool {
	return os.Getenv("ARCENTRA_CONTAINERD_ACCEPTANCE") == "1" && os.Getenv("ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE") == "1" && os.Getenv("ARCENTRA_REQUIRE_TERMINAL_RERUN_ACCEPTANCE") == "1" && runtime.Enabled && runtime.AllowWholeJobRetry && runtime.AllowWholeJobRetryApproval && len(runtime.AllowedImages) == 1 && runtime.AllowedImages[0] != "" && runtime.AllowedImages[0] == os.Getenv("ARCENTRA_SANDBOX_TEST_IMAGE") && runtime.ValidateProduction() == nil
}
