// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package pipeline

import (
	"fmt"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/validation"
)

func (c *ExecutionContext) validateRuntimeContext() error {
	if c == nil {
		return fmt.Errorf("execution context is required")
	}
	if c.Pipeline == nil {
		if c.Runtime != nil {
			return fmt.Errorf("runtime requires a pipeline")
		}
		return nil
	}
	expected, err := validation.ResolveRuntime(c.Pipeline, c.RuntimePolicy)
	if err != nil {
		return err
	}
	if expected == nil {
		if c.Runtime != nil {
			return fmt.Errorf("unexpected runtime execution snapshot")
		}
		return nil
	}
	if c.Runtime == nil || *expected != *c.Runtime {
		return fmt.Errorf("runtime execution requires the unchanged validated snapshot")
	}
	return nil
}

func (c *ExecutionContext) rejectStandaloneRuntime() error {
	if err := c.validateRuntimeContext(); err != nil {
		return err
	}
	if c.Runtime != nil {
		return fmt.Errorf("offline runtime requires complete-job dispatch; standalone and local runners are unsupported")
	}
	return nil
}
