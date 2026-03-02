// Copyright 2025 Arcentra Authors.
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

package validation

import (
	"fmt"

	"github.com/arcentrix/arcentra/internal/pkg/pipeline/spec"
)

// PipelineBasicValidator defines the interface for basic pipeline validation
// This interface allows Validator to be decoupled from specific parser implementations
type PipelineBasicValidator interface {
	// ValidateBasic performs basic validation on a pipeline
	// This includes checking required fields, basic structure, etc.
	ValidateBasic(pipeline *spec.Pipeline) error
}

// ConditionContextEvaluator defines the minimum context capability required by semantic validation.
type ConditionContextEvaluator interface {
	EvalConditionWithContext(conditionExpr string, context map[string]any) (bool, error)
}

// IPipelineValidator defines the interface for pipeline validation
type IPipelineValidator interface {
	// Validate performs comprehensive validation on a pipeline
	Validate(pipeline *spec.Pipeline) error
	// ValidateWithContext validates pipeline with execution context
	ValidateWithContext(pipeline *spec.Pipeline, ctx ConditionContextEvaluator) error
}

// Validator is the semantic validator.
// It orchestrates basic checks + schema checks + context-aware semantic checks.
type Validator struct {
	basicValidator PipelineBasicValidator
}

// NewValidator creates a new validator
func NewValidator(basicValidator PipelineBasicValidator) *Validator {
	return &Validator{
		basicValidator: basicValidator,
	}
}

// Ensure Validator implements IPipelineValidator interface
var _ IPipelineValidator = (*Validator)(nil)

// Validate performs comprehensive validation on a pipeline
func (v *Validator) Validate(pipeline *spec.Pipeline) error {
	if pipeline == nil {
		return fmt.Errorf("pipeline is nil")
	}

	// Basic validation (already done by parser, but double-check)
	if v.basicValidator != nil {
		if err := v.basicValidator.ValidateBasic(pipeline); err != nil {
			return err
		}
	}

	return NewSchemaValidator().Validate(pipeline)
}

// ValidateWithContext validates pipeline with execution context
// This allows validation of dynamic expressions like when conditions
func (v *Validator) ValidateWithContext(pipeline *spec.Pipeline, ctx ConditionContextEvaluator) error {
	// First perform static validation
	if err := v.Validate(pipeline); err != nil {
		return err
	}
	if ctx == nil {
		return fmt.Errorf("validation context is nil")
	}

	// Validate when conditions can be parsed (but don't evaluate them)
	for i, job := range pipeline.Jobs {
		if job.When != "" {
			if _, err := ctx.EvalConditionWithContext(job.When, map[string]any{
				"job": map[string]any{
					"name": job.Name,
				},
			}); err != nil {
				return fmt.Errorf("job[%d] '%s' when condition: %w", i, job.Name, err)
			}
		}

		for j, step := range job.Steps {
			if step.When != "" {
				if _, err := ctx.EvalConditionWithContext(step.When, map[string]any{
					"job": map[string]any{
						"name": job.Name,
					},
					"step": map[string]any{
						"name": step.Name,
					},
				}); err != nil {
					return fmt.Errorf("job[%d] '%s' step[%d] '%s' when condition: %w", i, job.Name, j, step.Name, err)
				}
			}
		}
	}

	return nil
}
