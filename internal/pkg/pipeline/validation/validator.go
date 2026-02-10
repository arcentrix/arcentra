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
	"regexp"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/pkg/pipeline"
	"github.com/arcentrix/arcentra/internal/pkg/pipeline/spec"
)

// PipelineBasicValidator defines the interface for basic pipeline validation
// This interface allows Validator to be decoupled from specific parser implementations
type PipelineBasicValidator interface {
	// ValidateBasic performs basic validation on a pipeline
	// This includes checking required fields, basic structure, etc.
	ValidateBasic(pipeline *spec.Pipeline) error
}

// IPipelineValidator defines the interface for pipeline validation
type IPipelineValidator interface {
	// Validate performs comprehensive validation on a pipeline
	Validate(pipeline *spec.Pipeline) error
	// ValidateWithContext validates pipeline with execution context
	ValidateWithContext(pipeline *spec.Pipeline, ctx *pipeline.ExecutionContext) error
}

// Validator provides advanced validation for Pipeline DSL
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

	// Advanced validations
	if err := v.validateNamespace(pipeline.Namespace); err != nil {
		return fmt.Errorf("namespace: %w", err)
	}

	if err := v.validateVersion(pipeline.Version); err != nil {
		return fmt.Errorf("version: %w", err)
	}

	// Validate job names are unique
	if err := v.validateUniqueJobNames(pipeline.Jobs); err != nil {
		return err
	}

	// Validate each job in detail
	for i, job := range pipeline.Jobs {
		if err := v.validateJobAdvanced(&job, i); err != nil {
			return err
		}
	}

	return nil
}

// validateNamespace validates namespace format
func (v *Validator) validateNamespace(namespace string) error {
	if namespace == "" {
		return fmt.Errorf("namespace is required")
	}

	// Namespace should be alphanumeric with hyphens and underscores
	namespaceRegex := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	if !namespaceRegex.MatchString(namespace) {
		return fmt.Errorf("namespace must contain only alphanumeric characters, hyphens, and underscores")
	}

	return nil
}

// validateVersion validates semantic version format (if provided)
func (v *Validator) validateVersion(version string) error {
	if version == "" {
		return nil // Version is optional
	}

	// Basic semantic version validation: major.minor.patch
	versionRegex := regexp.MustCompile(`^\d+\.\d+\.\d+(-[a-zA-Z0-9]+)?$`)
	if !versionRegex.MatchString(version) {
		return fmt.Errorf("version must follow semantic versioning format (e.g., 1.0.0)")
	}

	return nil
}

// validateUniqueJobNames ensures all job names are unique
func (v *Validator) validateUniqueJobNames(jobs []spec.Job) error {
	jobNames := make(map[string]int)
	for i, job := range jobs {
		if job.Name == "" {
			continue
		}
		if existingIndex, exists := jobNames[job.Name]; exists {
			return fmt.Errorf("duplicate job name '%s' at index %d and %d", job.Name, existingIndex, i)
		}
		jobNames[job.Name] = i
	}
	return nil
}

// validateJobAdvanced performs advanced validation on a job
func (v *Validator) validateJobAdvanced(job *spec.Job, index int) error {
	// Validate job name format
	if err := v.validateJobName(job.Name); err != nil {
		return fmt.Errorf("job[%d] '%s' name: %w", index, job.Name, err)
	}

	// Validate timeout format if provided
	if job.Timeout != "" {
		if err := v.validateTimeout(job.Timeout); err != nil {
			return fmt.Errorf("job[%d] '%s' timeout: %w", index, job.Name, err)
		}
	}

	// Validate retry configuration if provided
	if job.Retry != nil {
		if err := v.validateRetry(job.Retry); err != nil {
			return fmt.Errorf("job[%d] '%s' retry: %w", index, job.Name, err)
		}
	}

	// Validate step names are unique within job
	if err := v.validateUniqueStepNames(job.Steps); err != nil {
		return fmt.Errorf("job[%d] '%s' steps: %w", index, job.Name, err)
	}

	// Validate each step in detail
	for i, step := range job.Steps {
		if err := v.validateStepAdvanced(&step, i); err != nil {
			return fmt.Errorf("job[%d] '%s' step[%d] '%s': %w", index, job.Name, i, step.Name, err)
		}
	}

	return nil
}

// validateJobName validates job name format
func (v *Validator) validateJobName(name string) error {
	if name == "" {
		return fmt.Errorf("job name is required")
	}

	// Job name should be alphanumeric with hyphens and underscores
	nameRegex := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	if !nameRegex.MatchString(name) {
		return fmt.Errorf("job name must contain only alphanumeric characters, hyphens, and underscores")
	}

	return nil
}

// validateTimeout validates timeout duration format
func (v *Validator) validateTimeout(timeout string) error {
	if timeout == "" {
		return nil
	}

	_, err := time.ParseDuration(timeout)
	if err != nil {
		return fmt.Errorf("invalid timeout format '%s': %w (expected format: 30s, 5m, 1h)", timeout, err)
	}

	return nil
}

// validateRetry validates retry configuration
func (v *Validator) validateRetry(retry *spec.Retry) error {
	if retry.MaxAttempts < 0 {
		return fmt.Errorf("max_attempts must be non-negative")
	}

	if retry.MaxAttempts == 0 {
		return fmt.Errorf("max_attempts must be greater than 0")
	}

	if retry.Delay != "" {
		if err := v.validateTimeout(retry.Delay); err != nil {
			return fmt.Errorf("delay: %w", err)
		}
	}

	return nil
}

// validateUniqueStepNames ensures all step names are unique within a job
func (v *Validator) validateUniqueStepNames(steps []spec.Step) error {
	stepNames := make(map[string]int)
	for i, step := range steps {
		if step.Name == "" {
			continue
		}
		if existingIndex, exists := stepNames[step.Name]; exists {
			return fmt.Errorf("duplicate step name '%s' at index %d and %d", step.Name, existingIndex, i)
		}
		stepNames[step.Name] = i
	}
	return nil
}

// validateStepAdvanced performs advanced validation on a step
func (v *Validator) validateStepAdvanced(step *spec.Step, index int) error {
	// Validate step name format
	if err := v.validateStepName(step.Name); err != nil {
		return fmt.Errorf("step[%d] '%s' name: %w", index, step.Name, err)
	}

	// Validate uses format (should be plugin-name or plugin-name@version)
	if err := v.validateUses(step.Uses); err != nil {
		return fmt.Errorf("step[%d] '%s' uses: %w", index, step.Name, err)
	}

	// Validate timeout format if provided
	if step.Timeout != "" {
		if err := v.validateTimeout(step.Timeout); err != nil {
			return fmt.Errorf("step[%d] '%s' timeout: %w", index, step.Name, err)
		}
	}

	// Validate agent selector if provided
	if step.AgentSelector != nil {
		if err := v.validateAgentSelector(step.AgentSelector); err != nil {
			return fmt.Errorf("step[%d] '%s' agent_selector: %w", index, step.Name, err)
		}
	}

	return nil
}

// validateStepName validates step name format
func (v *Validator) validateStepName(name string) error {
	if name == "" {
		return fmt.Errorf("step name is required")
	}

	// Step name should be alphanumeric with hyphens and underscores
	nameRegex := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	if !nameRegex.MatchString(name) {
		return fmt.Errorf("step name must contain only alphanumeric characters, hyphens, and underscores")
	}

	return nil
}

// validateUses validates uses field format
// Format: plugin-name or plugin-name@version
func (v *Validator) validateUses(uses string) error {
	if uses == "" {
		return fmt.Errorf("uses field is required")
	}

	// Check if it contains version
	if strings.Contains(uses, "@") {
		parts := strings.Split(uses, "@")
		if len(parts) != 2 {
			return fmt.Errorf("invalid uses format: %s (expected: plugin-name@version)", uses)
		}
		pluginName := parts[0]
		version := parts[1]

		if pluginName == "" {
			return fmt.Errorf("plugin name cannot be empty")
		}

		// Validate version format (semantic versioning)
		versionRegex := regexp.MustCompile(`^\d+\.\d+\.\d+(-[a-zA-Z0-9]+)?$`)
		if !versionRegex.MatchString(version) {
			return fmt.Errorf("invalid version format in uses: %s", version)
		}
	}

	// Validate plugin name format
	pluginNameRegex := regexp.MustCompile(`^[a-zA-Z0-9_-]+(@\d+\.\d+\.\d+(-[a-zA-Z0-9]+)?)?$`)
	if !pluginNameRegex.MatchString(uses) {
		return fmt.Errorf("invalid uses format: %s (expected: plugin-name or plugin-name@version)", uses)
	}

	return nil
}

// validateAgentSelector validates agent selector configuration
func (v *Validator) validateAgentSelector(selector *spec.AgentSelector) error {
	// At least one selector criteria should be provided
	if len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0 {
		return fmt.Errorf("agent selector must have at least one match criteria")
	}

	// Validate match expressions
	for i, expr := range selector.MatchExpressions {
		if err := v.validateLabelExpression(&expr, i); err != nil {
			return fmt.Errorf("agent selector %w", err)
		}
	}

	return nil
}

// validateLabelExpression validates a label expression
func (v *Validator) validateLabelExpression(expr *spec.LabelExpression, index int) error {
	if expr.Key == "" {
		return fmt.Errorf("matchExpressions[%d] label expression key is required", index)
	}

	validOperators := map[string]bool{
		"In":        true,
		"NotIn":     true,
		"Exists":    true,
		"NotExists": true,
		"Gt":        true,
		"Lt":        true,
	}

	if !validOperators[expr.Operator] {
		return fmt.Errorf("matchExpressions[%d] label expression operator '%s' is invalid (valid: In, NotIn, Exists, NotExists, Gt, Lt)", index, expr.Operator)
	}

	// Operators that require values
	operatorsRequiringValues := map[string]bool{
		"In":    true,
		"NotIn": true,
		"Gt":    true,
		"Lt":    true,
	}

	if operatorsRequiringValues[expr.Operator] {
		if len(expr.Values) == 0 {
			return fmt.Errorf("matchExpressions[%d] label expression operator '%s' requires at least one value", index, expr.Operator)
		}
	}

	// Operators that don't require values
	operatorsNotRequiringValues := map[string]bool{
		"Exists":    true,
		"NotExists": true,
	}

	if operatorsNotRequiringValues[expr.Operator] && len(expr.Values) > 0 {
		return fmt.Errorf("matchExpressions[%d] label expression operator '%s' does not require values", index, expr.Operator)
	}

	return nil
}

// ValidateWithContext validates pipeline with execution context
// This allows validation of dynamic expressions like when conditions
func (v *Validator) ValidateWithContext(pipeline *spec.Pipeline, ctx *pipeline.ExecutionContext) error {
	// First perform static validation
	if err := v.Validate(pipeline); err != nil {
		return err
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
