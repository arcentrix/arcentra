// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package taskqueue

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

func validateStepExecution(uses, action string, args map[string]any, jobContext bool) error {
	if err := taskqueue.ValidateAgentStepUses(uses, action); err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(uses)) {
	case "", "shell", "builtin:shell":
		if strings.TrimSpace(executor.BuildCommandFromStepArgs(&executor.StepInfo{Args: args})) == "" {
			return fmt.Errorf("agent shell step requires run, script, command, or commands")
		}
	case "builtin:artifacts":
		if !jobContext {
			return fmt.Errorf("agent artifact steps require whole-job execution context")
		}
	case "http":
		raw, _ := args["url"].(string)
		target, err := url.Parse(raw)
		if err != nil || target.Hostname() == "" || target.User != nil || (target.Scheme != "http" && target.Scheme != "https") {
			return fmt.Errorf("agent http step requires an absolute HTTP(S) URL without embedded credentials")
		}
	}
	return nil
}

func validateJobExecution(payload *taskqueue.JobRunTaskPayload) error {
	if err := validateJobAttempts(payload); err != nil {
		return err
	}
	for _, step := range payload.Steps {
		if err := validateStepExecution(step.Uses, step.Action, step.Args, true); err != nil {
			return fmt.Errorf("step %q: %w", step.Name, err)
		}
	}
	return nil
}
