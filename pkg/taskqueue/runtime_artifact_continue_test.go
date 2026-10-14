// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package taskqueue

import (
	"strings"
	"testing"
)

func TestRuntimeArtifactContractRejectsContinueOnError(t *testing.T) {
	shell := StepPayload{Uses: "shell", Args: map[string]any{"script": "true"}, ContinueOnError: true}
	for _, action := range []string{"download", "upload"} {
		artifact := StepPayload{Uses: "builtin:artifacts", Action: action, ContinueOnError: true, Args: map[string]any{"paths": []any{"result.txt"}}}
		if action == "download" {
			artifact.Args["job"] = "producer"
		}
		steps := []StepPayload{shell, artifact}
		if action == "download" {
			steps = []StepPayload{artifact, shell}
		}
		if err := ValidateRuntimeSteps(steps); err == nil || !strings.Contains(err.Error(), "artifact staging cannot continue on error") {
			t.Fatalf("%s error=%v", action, err)
		}
	}
	if err := ValidateRuntimeSteps([]StepPayload{shell}); err != nil {
		t.Fatalf("shell continueOnError was rejected: %v", err)
	}
}
