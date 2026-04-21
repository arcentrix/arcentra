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

package interceptor

import (
	"fmt"
	"maps"
	"regexp"
	"strconv"
	"strings"

	"github.com/expr-lang/expr"
)

// VariableInterpreter interprets and resolves ${{ ... }} variable expressions
type VariableInterpreter struct {
	env map[string]any
}

// NewVariableInterpreter creates a new variable interpreter
func NewVariableInterpreter(env map[string]string) *VariableInterpreter {
	// Convert string map to any map for expr
	envMap := make(map[string]any)
	for k, v := range env {
		envMap[k] = v
	}

	return &VariableInterpreter{
		env: envMap,
	}
}

// VariableRegex matches ${{ ... }} expressions
// Supports: ${{ variable }}, ${{ expression }}, ${{env.VAR}}, etc.
var VariableRegex = regexp.MustCompile(`\${{([^}]+)}}`)

// Resolve resolves all ${{ ... }} expressions in a string
func (vi *VariableInterpreter) Resolve(text string) (string, error) {
	if text == "" {
		return text, nil
	}

	// Find all variable expressions
	matches := VariableRegex.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return text, nil
	}

	result := text
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}

		exprStr := strings.TrimSpace(match[1])
		if exprStr == "" {
			continue
		}

		// Evaluate expression
		value, err := vi.Evaluate(exprStr)
		if err != nil {
			return "", fmt.Errorf("evaluate expression '%s': %w", exprStr, err)
		}

		// Replace in result
		result = strings.ReplaceAll(result, match[0], fmt.Sprintf("%v", value))
	}

	return result, nil
}

// Evaluate evaluates an expression and returns its value
func (vi *VariableInterpreter) Evaluate(exprStr string) (any, error) {
	exprStr = strings.TrimSpace(exprStr)
	if exprStr == "" {
		return "", nil
	}

	// Prepare environment
	env := make(map[string]any)
	maps.Copy(env, vi.env)

	// Add env map for accessing environment variables
	env["env"] = vi.env

	// Compile expression
	program, err := expr.Compile(exprStr, expr.Env(env))
	if err != nil {
		return nil, fmt.Errorf("compile expression '%s': %w", exprStr, err)
	}

	// Run expression
	result, err := expr.Run(program, env)
	if err != nil {
		return nil, fmt.Errorf("evaluate expression '%s': %w", exprStr, err)
	}

	return result, nil
}

// ResolveMap resolves variables in a map recursively
func (vi *VariableInterpreter) ResolveMap(data map[string]any) (map[string]any, error) {
	resolved := make(map[string]any)

	for k, v := range data {
		resolvedKey, err := vi.Resolve(k)
		if err != nil {
			return nil, fmt.Errorf("resolve key '%s': %w", k, err)
		}

		resolvedValue, err := vi.resolveValue(v)
		if err != nil {
			return nil, fmt.Errorf("resolve value for key '%s': %w", k, err)
		}

		resolved[resolvedKey] = resolvedValue
	}

	return resolved, nil
}

// resolveValue resolves variables in a value recursively
func (vi *VariableInterpreter) resolveValue(v any) (any, error) {
	switch val := v.(type) {
	case string:
		return vi.Resolve(val)
	case map[string]any:
		return vi.ResolveMap(val)
	case []any:
		resolved := make([]any, len(val))
		for i, item := range val {
			resolvedItem, err := vi.resolveValue(item)
			if err != nil {
				return nil, err
			}
			resolved[i] = resolvedItem
		}
		return resolved, nil
	default:
		return v, nil
	}
}

// SetVariable sets a variable value
func (vi *VariableInterpreter) SetVariable(key string, value any) {
	if vi.env == nil {
		vi.env = make(map[string]any)
	}
	vi.env[key] = value
}

// SetVariables sets multiple variables
func (vi *VariableInterpreter) SetVariables(vars map[string]any) {
	if vi.env == nil {
		vi.env = make(map[string]any)
	}
	maps.Copy(vi.env, vars)
}

// GetVariable gets a variable value
func (vi *VariableInterpreter) GetVariable(key string) (any, bool) {
	if vi.env == nil {
		return nil, false
	}
	value, ok := vi.env[key]
	return value, ok
}

// GetAllVariables returns all variables
func (vi *VariableInterpreter) GetAllVariables() map[string]any {
	if vi.env == nil {
		return nil
	}
	result := make(map[string]any)
	maps.Copy(result, vi.env)
	return result
}

// ResolveString resolves a string variable expression
// Returns the resolved string and whether it was a variable expression
func (vi *VariableInterpreter) ResolveString(text string) (string, bool, error) {
	if text == "" {
		return text, false, nil
	}

	// Check if it's a simple variable reference: ${{ variable }}
	match := VariableRegex.FindStringSubmatch(text)
	if len(match) < 2 {
		return text, false, nil
	}

	// If the entire text is a variable expression, return the evaluated value
	if strings.TrimSpace(match[0]) == strings.TrimSpace(text) {
		exprStr := strings.TrimSpace(match[1])
		value, err := vi.Evaluate(exprStr)
		if err != nil {
			return "", false, err
		}
		return fmt.Sprintf("%v", value), true, nil
	}

	// Otherwise, resolve all expressions in the text
	resolved, err := vi.Resolve(text)
	return resolved, true, err
}

// ResolveInt resolves an integer variable expression
func (vi *VariableInterpreter) ResolveInt(text string) (int64, error) {
	resolved, _, err := vi.ResolveString(text)
	if err != nil {
		return 0, err
	}

	value, err := strconv.ParseInt(resolved, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse int '%s': %w", resolved, err)
	}

	return value, nil
}

// ResolveBool resolves a boolean variable expression
func (vi *VariableInterpreter) ResolveBool(text string) (bool, error) {
	resolved, _, err := vi.ResolveString(text)
	if err != nil {
		return false, err
	}

	value, err := strconv.ParseBool(resolved)
	if err != nil {
		return false, fmt.Errorf("parse bool '%s': %w", resolved, err)
	}

	return value, nil
}

// HasVariables checks if a string contains variable expressions
func (vi *VariableInterpreter) HasVariables(text string) bool {
	return VariableRegex.MatchString(text)
}

// ExtractVariables extracts all variable expressions from a string
func (vi *VariableInterpreter) ExtractVariables(text string) []string {
	matches := VariableRegex.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}

	variables := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) >= 2 {
			variables = append(variables, strings.TrimSpace(match[1]))
		}
	}

	return variables
}
