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

package dsl

import (
	"context"
	"fmt"

	"github.com/arcentrix/arcentra/internal/pkg/pipeline"
	"github.com/arcentrix/arcentra/internal/pkg/pipeline/interceptor"
	"github.com/arcentrix/arcentra/internal/pkg/pipeline/spec"
	"github.com/arcentrix/arcentra/internal/pkg/pipeline/validation"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/plugin"
)

// DSLProcessor processes Pipeline DSL with variable resolution and validation
type DSLProcessor struct {
	parser    *DSLParser
	validator *validation.Validator
	logger    log.Logger
}

// NewDSLProcessor creates a new DSL processor
func NewDSLProcessor(logger log.Logger) *DSLProcessor {
	parser := NewDSLParser(logger)
	basicValidator := NewPipelineBasicValidatorAdapter(parser)
	validator := validation.NewValidator(basicValidator)

	return &DSLProcessor{
		parser:    parser,
		validator: validator,
		logger:    logger,
	}
}

// ProcessConfig processes DSL config from database and returns ready-to-execute Pipeline
// This is the main entry point for processing pipeline DSL
func (p *DSLProcessor) ProcessConfig(
	ctx context.Context,
	dslConfig string,
	pluginMgr *plugin.Manager,
	workspace string,
	additionalEnv map[string]string,
) (*spec.Pipeline, *pipeline.ExecutionContext, error) {
	pl, err := p.parser.Parse(dslConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("parse DSL config: %w", err)
	}

	execCtx := pipeline.NewExecutionContext(pl, pluginMgr, workspace, p.logger)

	for k, v := range additionalEnv {
		execCtx.Env[k] = v
	}

	if err := p.resolvePipelineVariables(pl, execCtx); err != nil {
		return nil, nil, fmt.Errorf("resolve variables: %w", err)
	}

	if err := p.validator.ValidateWithContext(pl, execCtx); err != nil {
		return nil, nil, fmt.Errorf("validate pipeline: %w", err)
	}

	if p.logger.Log != nil {
		p.logger.Log.Infow("processed pipeline DSL",
			"namespace", pl.Namespace,
			"version", pl.Version,
			"jobs_count", len(pl.Jobs),
		)
	}

	return pl, execCtx, nil
}

// resolvePipelineVariables resolves all variables in pipeline structure
func (p *DSLProcessor) resolvePipelineVariables(pl *spec.Pipeline, ctx *pipeline.ExecutionContext) error {
	// Create variable interpreter
	interpreter := interceptor.NewVariableInterpreter(ctx.Env)

	// Resolve pipeline-level variables
	if pl.Variables != nil {
		resolvedVars, err := interpreter.ResolveMap(convertStringMapToAnyMap(pl.Variables))
		if err != nil {
			return fmt.Errorf("resolve pipeline variables: %w", err)
		}
		pl.Variables = convertAnyMapToStringMap(resolvedVars)
	}

	// Resolve variables in each job
	for i := range pl.Jobs {
		job := &pl.Jobs[i]

		// Resolve job-level variables
		if job.Env != nil {
			resolvedEnv, err := interpreter.ResolveMap(convertStringMapToAnyMap(job.Env))
			if err != nil {
				return fmt.Errorf("job '%s' resolve env: %w", job.Name, err)
			}
			job.Env = convertAnyMapToStringMap(resolvedEnv)
		}

		// Resolve when condition
		if job.When != "" {
			resolved, err := interpreter.Resolve(job.When)
			if err != nil {
				return fmt.Errorf("job '%s' resolve when: %w", job.Name, err)
			}
			job.When = resolved
		}

		// Resolve source configuration
		if job.Source != nil {
			if err := p.resolveSource(job.Source, interpreter); err != nil {
				return fmt.Errorf("job '%s' resolve source: %w", job.Name, err)
			}
		}

		// Resolve approval configuration
		if job.Approval != nil && job.Approval.Params != nil {
			resolvedParams, err := interpreter.ResolveMap(job.Approval.Params)
			if err != nil {
				return fmt.Errorf("job '%s' resolve approval params: %w", job.Name, err)
			}
			job.Approval.Params = resolvedParams
		}

		// Resolve target configuration
		if job.Target != nil && job.Target.Config != nil {
			resolvedConfig, err := interpreter.ResolveMap(job.Target.Config)
			if err != nil {
				return fmt.Errorf("job '%s' resolve target config: %w", job.Name, err)
			}
			job.Target.Config = resolvedConfig
		}

		// Resolve notify configuration
		if job.Notify != nil {
			if err := p.resolveNotify(job.Notify, interpreter); err != nil {
				return fmt.Errorf("job '%s' resolve notify: %w", job.Name, err)
			}
		}

		// Resolve variables in each step
		for j := range job.Steps {
			step := &job.Steps[j]

			// Resolve step-level variables
			if step.Env != nil {
				resolvedEnv, err := interpreter.ResolveMap(convertStringMapToAnyMap(step.Env))
				if err != nil {
					return fmt.Errorf("job '%s' step '%s' resolve env: %w", job.Name, step.Name, err)
				}
				step.Env = convertAnyMapToStringMap(resolvedEnv)
			}

			// Resolve when condition
			if step.When != "" {
				resolved, err := interpreter.Resolve(step.When)
				if err != nil {
					return fmt.Errorf("job '%s' step '%s' resolve when: %w", job.Name, step.Name, err)
				}
				step.When = resolved
			}

			// Resolve uses field (may contain variables)
			if step.Uses != "" {
				resolved, err := interpreter.Resolve(step.Uses)
				if err != nil {
					return fmt.Errorf("job '%s' step '%s' resolve uses: %w", job.Name, step.Name, err)
				}
				step.Uses = resolved
			}

			// Resolve action field
			if step.Action != "" {
				resolved, err := interpreter.Resolve(step.Action)
				if err != nil {
					return fmt.Errorf("job '%s' step '%s' resolve action: %w", job.Name, step.Name, err)
				}
				step.Action = resolved
			}

			// Resolve args
			if step.Args != nil {
				resolvedArgs, err := interpreter.ResolveMap(step.Args)
				if err != nil {
					return fmt.Errorf("job '%s' step '%s' resolve args: %w", job.Name, step.Name, err)
				}
				step.Args = resolvedArgs
			}
		}
	}

	return nil
}

// resolveSource resolves variables in source configuration
func (p *DSLProcessor) resolveSource(source *spec.Source, interpreter *interceptor.VariableInterpreter) error {
	if source.Repo != "" {
		resolved, err := interpreter.Resolve(source.Repo)
		if err != nil {
			return fmt.Errorf("resolve repo: %w", err)
		}
		source.Repo = resolved
	}

	if source.Branch != "" {
		resolved, err := interpreter.Resolve(source.Branch)
		if err != nil {
			return fmt.Errorf("resolve branch: %w", err)
		}
		source.Branch = resolved
	}

	if source.Auth != nil {
		if source.Auth.Username != "" {
			resolved, err := interpreter.Resolve(source.Auth.Username)
			if err != nil {
				return fmt.Errorf("resolve auth username: %w", err)
			}
			source.Auth.Username = resolved
		}

		if source.Auth.Password != "" {
			resolved, err := interpreter.Resolve(source.Auth.Password)
			if err != nil {
				return fmt.Errorf("resolve auth password: %w", err)
			}
			source.Auth.Password = resolved
		}

		if source.Auth.Token != "" {
			resolved, err := interpreter.Resolve(source.Auth.Token)
			if err != nil {
				return fmt.Errorf("resolve auth token: %w", err)
			}
			source.Auth.Token = resolved
		}
	}

	return nil
}

// resolveNotify resolves variables in notify configuration
func (p *DSLProcessor) resolveNotify(notify *spec.Notify, interpreter *interceptor.VariableInterpreter) error {
	if notify.OnSuccess != nil && notify.OnSuccess.Params != nil {
		resolvedParams, err := interpreter.ResolveMap(notify.OnSuccess.Params)
		if err != nil {
			return fmt.Errorf("resolve on_success params: %w", err)
		}
		notify.OnSuccess.Params = resolvedParams
	}

	if notify.OnFailure != nil && notify.OnFailure.Params != nil {
		resolvedParams, err := interpreter.ResolveMap(notify.OnFailure.Params)
		if err != nil {
			return fmt.Errorf("resolve on_failure params: %w", err)
		}
		notify.OnFailure.Params = resolvedParams
	}

	return nil
}

// convertStringMapToAnyMap converts map[string]string to map[string]any
func convertStringMapToAnyMap(m map[string]string) map[string]any {
	result := make(map[string]any)
	for k, v := range m {
		result[k] = v
	}
	return result
}

// convertAnyMapToStringMap converts map[string]any to map[string]string
func convertAnyMapToStringMap(m map[string]any) map[string]string {
	result := make(map[string]string)
	for k, v := range m {
		if str, ok := v.(string); ok {
			result[k] = str
		} else {
			result[k] = fmt.Sprintf("%v", v)
		}
	}
	return result
}
