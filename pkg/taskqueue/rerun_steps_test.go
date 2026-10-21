// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import "testing"

func TestRerunStepPredicatesAreExactAllowlist(t *testing.T) {
	for _, tc := range []struct {
		uses, action  string
		shell, upload bool
	}{
		{"shell", "", true, false}, {"shell", "run", true, false}, {"builtin:shell", "run", true, false},
		{"builtin:artifacts", "upload", false, true}, {"builtin:artifacts", "download", false, false},
		{"builtin:artifacts", "", false, false}, {"shell", "upload", false, false}, {"plugin:artifacts", "upload", false, false},
		{"builtin:artifacts ", "upload", false, false}, {"BUILTIN:ARTIFACTS", "upload", false, false}, {"http", "request", false, false},
	} {
		if RerunShellStep(tc.uses, tc.action) != tc.shell || RerunArtifactUploadStep(tc.uses, tc.action) != tc.upload {
			t.Fatalf("unexpected predicate for %q/%q", tc.uses, tc.action)
		}
	}
}
