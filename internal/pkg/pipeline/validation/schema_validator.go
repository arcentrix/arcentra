// Copyright 2026 Arcentra Authors.
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

	"github.com/arcentrix/arcentra/internal/pkg/pipeline/spec"
)

// SchemaValidator validates static schema constraints of pipeline spec.
// It does not evaluate runtime context/expressions.
type SchemaValidator struct{}

func NewSchemaValidator() *SchemaValidator { return &SchemaValidator{} }

func (v *SchemaValidator) Validate(pipeline *spec.Pipeline) error {
	if pipeline == nil {
		return fmt.Errorf("pipeline is nil")
	}
	if err := v.validateNamespace(pipeline.Namespace); err != nil {
		return fmt.Errorf("namespace: %w", err)
	}
	if err := v.validateVersion(pipeline.Version); err != nil {
		return fmt.Errorf("version: %w", err)
	}
	if len(pipeline.Jobs) == 0 {
		return fmt.Errorf("pipeline must have at least one job")
	}
	if err := v.validateUniqueJobNames(pipeline.Jobs); err != nil {
		return err
	}
	for i, job := range pipeline.Jobs {
		if err := v.validateJobAdvanced(job, i); err != nil {
			return err
		}
	}
	return nil
}

func (v *SchemaValidator) validateNamespace(namespace string) error {
	if strings.TrimSpace(namespace) == "" {
		return fmt.Errorf("namespace is required")
	}
	namespaceRegex := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	if !namespaceRegex.MatchString(namespace) {
		return fmt.Errorf("namespace must contain only alphanumeric characters, hyphens, and underscores")
	}
	return nil
}

func (v *SchemaValidator) validateVersion(version string) error {
	if version == "" {
		return nil
	}
	versionRegex := regexp.MustCompile(`^\d+\.\d+\.\d+(-[a-zA-Z0-9]+)?$`)
	if !versionRegex.MatchString(version) {
		return fmt.Errorf("version must follow semantic versioning format (e.g., 1.0.0)")
	}
	return nil
}

func (v *SchemaValidator) validateUniqueJobNames(jobs []*spec.Job) error {
	jobNames := make(map[string]int)
	for i, job := range jobs {
		if job == nil {
			return fmt.Errorf("job[%d] is nil", i)
		}
		name := strings.TrimSpace(job.Name)
		if name == "" {
			return fmt.Errorf("job[%d] name: job name is required", i)
		}
		if existingIndex, exists := jobNames[name]; exists {
			return fmt.Errorf("duplicate job name '%s' at index %d and %d", name, existingIndex, i)
		}
		jobNames[name] = i
	}
	return nil
}

func (v *SchemaValidator) validateJobAdvanced(job *spec.Job, index int) error {
	if err := v.validateName(job.Name, "job name"); err != nil {
		return fmt.Errorf("job[%d] '%s' name: %w", index, job.Name, err)
	}
	if len(job.Steps) == 0 {
		return fmt.Errorf("job[%d] '%s': job must have at least one step", index, job.Name)
	}
	if job.Timeout != "" {
		if err := v.validateTimeout(job.Timeout); err != nil {
			return fmt.Errorf("job[%d] '%s' timeout: %w", index, job.Name, err)
		}
	}
	if job.Retry != nil {
		if err := v.validateRetry(job.Retry); err != nil {
			return fmt.Errorf("job[%d] '%s' retry: %w", index, job.Name, err)
		}
	}
	if err := v.validateUniqueStepNames(job.Steps); err != nil {
		return fmt.Errorf("job[%d] '%s' steps: %w", index, job.Name, err)
	}
	for i, step := range job.Steps {
		if err := v.validateStepAdvanced(step, i); err != nil {
			return fmt.Errorf("job[%d] '%s' step[%d] '%s': %w", index, job.Name, i, step.Name, err)
		}
	}
	return nil
}

func (v *SchemaValidator) validateUniqueStepNames(steps []*spec.Step) error {
	stepNames := make(map[string]int)
	for i, step := range steps {
		if step == nil {
			return fmt.Errorf("step[%d] is nil", i)
		}
		name := strings.TrimSpace(step.Name)
		if name == "" {
			return fmt.Errorf("step[%d] name: step name is required", i)
		}
		if existingIndex, exists := stepNames[name]; exists {
			return fmt.Errorf("duplicate step name '%s' at index %d and %d", name, existingIndex, i)
		}
		stepNames[name] = i
	}
	return nil
}

func (v *SchemaValidator) validateStepAdvanced(step *spec.Step, index int) error {
	if err := v.validateName(step.Name, "step name"); err != nil {
		return fmt.Errorf("step[%d] '%s' name: %w", index, step.Name, err)
	}
	if err := v.validateUses(step.Uses); err != nil {
		return fmt.Errorf("step[%d] '%s' uses: %w", index, step.Name, err)
	}
	if step.Timeout != "" {
		if err := v.validateTimeout(step.Timeout); err != nil {
			return fmt.Errorf("step[%d] '%s' timeout: %w", index, step.Name, err)
		}
	}
	if step.AgentSelector != nil {
		if err := v.validateAgentSelector(step.AgentSelector); err != nil {
			return fmt.Errorf("step[%d] '%s' agent_selector: %w", index, step.Name, err)
		}
	}
	return nil
}

func (v *SchemaValidator) validateName(name, field string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%s is required", field)
	}
	nameRegex := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	if !nameRegex.MatchString(name) {
		return fmt.Errorf("%s must contain only alphanumeric characters, hyphens, and underscores", field)
	}
	return nil
}

func (v *SchemaValidator) validateTimeout(timeout string) error {
	_, err := time.ParseDuration(timeout)
	if err != nil {
		return fmt.Errorf("invalid timeout format '%s': %w (expected format: 30s, 5m, 1h)", timeout, err)
	}
	return nil
}

func (v *SchemaValidator) validateRetry(retry *spec.Retry) error {
	if retry.MaxAttempts <= 0 {
		return fmt.Errorf("max_attempts must be greater than 0")
	}
	if retry.Delay != "" {
		if err := v.validateTimeout(retry.Delay); err != nil {
			return fmt.Errorf("delay: %w", err)
		}
	}
	return nil
}

func (v *SchemaValidator) validateUses(uses string) error {
	if strings.TrimSpace(uses) == "" {
		return fmt.Errorf("uses field is required")
	}
	if strings.Contains(uses, "@") {
		parts := strings.Split(uses, "@")
		if len(parts) != 2 {
			return fmt.Errorf("invalid uses format: %s (expected: plugin-name@version)", uses)
		}
		if parts[0] == "" {
			return fmt.Errorf("plugin name cannot be empty")
		}
		versionRegex := regexp.MustCompile(`^\d+\.\d+\.\d+(-[a-zA-Z0-9]+)?$`)
		if !versionRegex.MatchString(parts[1]) {
			return fmt.Errorf("invalid version format in uses: %s", parts[1])
		}
	}
	pluginNameRegex := regexp.MustCompile(`^[a-zA-Z0-9_-]+(@\d+\.\d+\.\d+(-[a-zA-Z0-9]+)?)?$`)
	if !pluginNameRegex.MatchString(uses) {
		return fmt.Errorf("invalid uses format: %s (expected: plugin-name or plugin-name@version)", uses)
	}
	return nil
}

func (v *SchemaValidator) validateAgentSelector(selector *spec.AgentSelector) error {
	if len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0 {
		return fmt.Errorf("agent selector must have at least one match criteria")
	}
	for i, expr := range selector.MatchExpressions {
		if err := v.validateLabelExpression(expr, i); err != nil {
			return fmt.Errorf("agent selector %w", err)
		}
	}
	return nil
}

func (v *SchemaValidator) validateLabelExpression(expr *spec.LabelExpression, index int) error {
	if strings.TrimSpace(expr.Key) == "" {
		return fmt.Errorf("matchExpressions[%d] label expression key is required", index)
	}
	validOperators := map[string]bool{"In": true, "NotIn": true, "Exists": true, "NotExists": true, "Gt": true, "Lt": true}
	if !validOperators[expr.Operator] {
		return fmt.Errorf("matchExpressions[%d] label expression operator '%s' is invalid (valid: In, NotIn, Exists, NotExists, Gt, Lt)", index, expr.Operator)
	}
	needsValues := map[string]bool{"In": true, "NotIn": true, "Gt": true, "Lt": true}
	if needsValues[expr.Operator] && len(expr.Values) == 0 {
		return fmt.Errorf("matchExpressions[%d] label expression operator '%s' requires at least one value", index, expr.Operator)
	}
	if (expr.Operator == "Exists" || expr.Operator == "NotExists") && len(expr.Values) > 0 {
		return fmt.Errorf("matchExpressions[%d] label expression operator '%s' does not require values", index, expr.Operator)
	}
	return nil
}
