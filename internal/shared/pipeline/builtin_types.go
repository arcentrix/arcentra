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

package pipeline

import (
	"context"
	"encoding/json"

	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
)

// IBuiltinManager defines the interface for builtin function management.
type IBuiltinManager interface {
	Execute(ctx context.Context, builtin, action string, params json.RawMessage, opts *BuiltinOptions) (json.RawMessage, error)
	GetInfo(name string) (*BuiltinInfo, error)
	ListBuiltins() map[string]*BuiltinInfo
	IsBuiltin(uses string) (string, bool)
}

// BuiltinExecutionContext is the interface for execution context to avoid circular imports.
type BuiltinExecutionContext interface {
	GetPipeline() *spec.Pipeline
	GetWorkspaceRoot() string
}

// ActionHandler handles a specific action for builtin functions.
type ActionHandler func(ctx context.Context, params json.RawMessage, opts *BuiltinOptions) (json.RawMessage, error)

// BuiltinOptions contains runtime options for builtin functions.
type BuiltinOptions struct {
	Workspace        string
	Env              map[string]string
	Job              *spec.Job
	Step             *spec.Step
	ExecutionContext BuiltinExecutionContext
}

// BuiltinInfo contains metadata about a builtin function.
type BuiltinInfo struct {
	Name        string
	Description string
	Actions     []string
}
