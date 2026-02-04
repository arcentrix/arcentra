// Copyright 2025 Arcade Team
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

package dsl

import (
	"encoding/json"
	"fmt"

	"github.com/arcentrix/arcade/internal/pkg/pipeline/spec"
	"github.com/arcentrix/arcade/pkg/log"
	"github.com/bytedance/sonic"
)

// DSLParser parses Pipeline DSL from JSON format
type DSLParser struct {
	logger log.Logger
}

// NewDSLParser creates a new DSL parser
func NewDSLParser(logger log.Logger) *DSLParser {
	return &DSLParser{
		logger: logger,
	}
}

// Parse parses DSL JSON string into Pipeline structure
func (p *DSLParser) Parse(dslJSON string) (*spec.Pipeline, error) {
	if dslJSON == "" {
		return nil, fmt.Errorf("dsl config is empty")
	}

	var pipeline spec.Pipeline

	// Use sonic for better performance (as per project requirements)
	if err := sonic.UnmarshalString(dslJSON, &pipeline); err != nil {
		return nil, fmt.Errorf("unmarshal DSL JSON: %w", err)
	}

	// Validate parsed pipeline
	if err := p.validate(&pipeline); err != nil {
		return nil, fmt.Errorf("validate pipeline: %w", err)
	}

	if p.logger.Log != nil {
		p.logger.Log.Debugw("parsed pipeline DSL",
			"namespace", pipeline.Namespace,
			"version", pipeline.Version,
			"jobs_count", len(pipeline.Jobs),
		)
	}

	return &pipeline, nil
}

// ParseFromBytes parses DSL from byte array
func (p *DSLParser) ParseFromBytes(data []byte) (*spec.Pipeline, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("dsl config is empty")
	}

	var pipeline spec.Pipeline

	if err := sonic.Unmarshal(data, &pipeline); err != nil {
		return nil, fmt.Errorf("unmarshal DSL JSON: %w", err)
	}

	// Validate parsed pipeline
	if err := p.validate(&pipeline); err != nil {
		return nil, fmt.Errorf("validate pipeline: %w", err)
	}

	return &pipeline, nil
}

// ParseFromMap parses DSL from map structure (useful for testing or dynamic config)
func (p *DSLParser) ParseFromMap(data map[string]any) (*spec.Pipeline, error) {
	if data == nil {
		return nil, fmt.Errorf("dsl config is nil")
	}

	// Convert map to JSON bytes
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal map to JSON: %w", err)
	}

	return p.ParseFromBytes(jsonBytes)
}

// validate performs basic validation on parsed pipeline
func (p *DSLParser) validate(pipeline *spec.Pipeline) error {
	// Validate namespace (required)
	if pipeline.Namespace == "" {
		return fmt.Errorf("pipeline namespace is required")
	}

	// Validate jobs (at least one job required)
	if len(pipeline.Jobs) == 0 {
		return fmt.Errorf("pipeline must have at least one job")
	}

	// Validate each job
	for i, job := range pipeline.Jobs {
		if err := p.validateJob(&job, i); err != nil {
			return fmt.Errorf("job[%d]: %w", i, err)
		}
	}

	return nil
}

// validateJob validates a single job
func (p *DSLParser) validateJob(job *spec.Job, index int) error {
	// Job name is required
	if job.Name == "" {
		return fmt.Errorf("job name is required")
	}

	// Job must have at least one step
	if len(job.Steps) == 0 {
		return fmt.Errorf("job '%s' must have at least one step", job.Name)
	}

	// Validate each step
	for i, step := range job.Steps {
		if err := p.validateStep(&step, i); err != nil {
			return fmt.Errorf("step[%d]: %w", i, err)
		}
	}

	// Validate source if present
	if job.Source != nil {
		if err := p.validateSource(job.Source); err != nil {
			return fmt.Errorf("source: %w", err)
		}
	}

	// Validate approval if present
	if job.Approval != nil {
		if err := p.validateApproval(job.Approval); err != nil {
			return fmt.Errorf("approval: %w", err)
		}
	}

	// Validate target if present
	if job.Target != nil {
		if err := p.validateTarget(job.Target); err != nil {
			return fmt.Errorf("target: %w", err)
		}
	}

	// Validate notify if present
	if job.Notify != nil {
		if err := p.validateNotify(job.Notify); err != nil {
			return fmt.Errorf("notify: %w", err)
		}
	}

	return nil
}

// validateStep validates a single step
func (p *DSLParser) validateStep(step *spec.Step, index int) error {
	// Step name is required
	if step.Name == "" {
		return fmt.Errorf("step name is required")
	}

	// Step uses is required (specifies which plugin to use)
	if step.Uses == "" {
		return fmt.Errorf("step '%s' uses field is required", step.Name)
	}

	return nil
}

// validateSource validates source configuration
func (p *DSLParser) validateSource(source *spec.Source) error {
	if source.Type == "" {
		return fmt.Errorf("source type is required")
	}

	// Validate source type enum
	validTypes := map[string]bool{
		"git":      true,
		"artifact": true,
		"s3":       true,
		"custom":   true,
	}
	if !validTypes[source.Type] {
		return fmt.Errorf("invalid source type: %s (valid types: git, artifact, s3, custom)", source.Type)
	}

	// Git source requires repo
	if source.Type == "git" && source.Repo == "" {
		return fmt.Errorf("git source requires repo field")
	}

	return nil
}

// validateApproval validates approval configuration
func (p *DSLParser) validateApproval(approval *spec.Approval) error {
	if approval.Required && approval.Plugin == "" {
		return fmt.Errorf("approval plugin is required when approval is required")
	}

	if approval.Type != "" {
		validTypes := map[string]bool{
			"manual": true,
			"auto":   true,
		}
		if !validTypes[approval.Type] {
			return fmt.Errorf("invalid approval type: %s (valid types: manual, auto)", approval.Type)
		}
	}

	return nil
}

// validateTarget validates target configuration
func (p *DSLParser) validateTarget(target *spec.Target) error {
	if target.Type == "" {
		return fmt.Errorf("target type is required")
	}

	validTypes := map[string]bool{
		"k8s":    true,
		"vm":     true,
		"docker": true,
		"s3":     true,
		"custom": true,
	}
	if !validTypes[target.Type] {
		return fmt.Errorf("invalid target type: %s (valid types: k8s, vm, docker, s3, custom)", target.Type)
	}

	return nil
}

// validateNotify validates notify configuration
func (p *DSLParser) validateNotify(notify *spec.Notify) error {
	if notify.OnSuccess != nil {
		if err := p.validateNotifyItem(notify.OnSuccess, "on_success"); err != nil {
			return err
		}
	}

	if notify.OnFailure != nil {
		if err := p.validateNotifyItem(notify.OnFailure, "on_failure"); err != nil {
			return err
		}
	}

	return nil
}

// validateNotifyItem validates a notify item
func (p *DSLParser) validateNotifyItem(item *spec.NotifyItem, context string) error {
	if item.Plugin == "" {
		return fmt.Errorf("%s plugin is required", context)
	}

	if item.Action == "" {
		return fmt.Errorf("%s action is required", context)
	}

	return nil
}

// ToJSON converts Pipeline structure back to JSON string
func (p *DSLParser) ToJSON(pipeline *spec.Pipeline) (string, error) {
	if pipeline == nil {
		return "", fmt.Errorf("pipeline is nil")
	}

	jsonBytes, err := sonic.Marshal(pipeline)
	if err != nil {
		return "", fmt.Errorf("marshal pipeline to JSON: %w", err)
	}

	return string(jsonBytes), nil
}

// ToJSONBytes converts Pipeline structure to JSON bytes
func (p *DSLParser) ToJSONBytes(pipeline *spec.Pipeline) ([]byte, error) {
	if pipeline == nil {
		return nil, fmt.Errorf("pipeline is nil")
	}

	return sonic.Marshal(pipeline)
}
