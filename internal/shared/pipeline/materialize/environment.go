// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package materialize

import (
	"maps"
	"regexp"

	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
)

func (c *Context) ResolveStepEnv(job *spec.Job, step *spec.Step) map[string]string {
	env := maps.Clone(c.Env)
	if env == nil {
		env = make(map[string]string)
	}
	if !c.jobEnvResolved && job != nil {
		env = resolveEnvLayer(env, job.Env)
	}
	if step != nil {
		env = resolveEnvLayer(env, step.Env)
	}
	return env
}

func resolveEnvLayer(base, overrides map[string]string) map[string]string {
	env := maps.Clone(base)
	if env == nil {
		env = make(map[string]string)
	}
	for key, value := range overrides {
		env[key] = resolveVariable(value, base)
	}
	return env
}

// ResolveVariable resolves variable substitution: ${{ variable }}
var varRegex = regexp.MustCompile(`\${{?\s*(\w+(?:\.\w+)?)\s*}}?`)

func (c *Context) ResolveVariable(value string) string {
	return resolveVariable(value, c.Env)
}

func resolveVariable(value string, env map[string]string) string {
	return varRegex.ReplaceAllStringFunc(value, func(match string) string {
		submatch := varRegex.FindStringSubmatch(match)
		if len(submatch) == 2 {
			key := submatch[1]
			if val, ok := env[key]; ok {
				return val
			}
		}
		return match
	})
}

// ResolveVariables resolves variables in a map recursively
func (c *Context) ResolveVariables(params map[string]any) map[string]any {
	resolved := make(map[string]any)
	for k, v := range params {
		resolved[k] = c.resolveValue(v)
	}
	return resolved
}

func (c *Context) resolveValue(v any) any {
	switch val := v.(type) {
	case string:
		return c.ResolveVariable(val)
	case map[string]any:
		resolved := make(map[string]any)
		for k, v := range val {
			resolved[k] = c.resolveValue(v)
		}
		return resolved
	case []any:
		resolved := make([]any, len(val))
		for i, v := range val {
			resolved[i] = c.resolveValue(v)
		}
		return resolved
	default:
		return v
	}
}

func ResolveVariable(value string, env map[string]string) string { return resolveVariable(value, env) }
func ResolveEnvLayer(base, overrides map[string]string) map[string]string {
	return resolveEnvLayer(base, overrides)
}
