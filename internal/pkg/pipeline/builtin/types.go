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

package builtin

import (
	"context"
	"encoding/json"

	"github.com/arcentrix/arcade/internal/pkg/pipeline/spec"
)

// ExecutionContext is the interface for execution context to avoid circular imports
type ExecutionContext interface {
	GetPipeline() *spec.Pipeline
	GetWorkspaceRoot() string
}

// ActionHandler handles a specific action for builtin functions
type ActionHandler func(ctx context.Context, params json.RawMessage, opts *Options) (json.RawMessage, error)

// Options contains runtime options for builtin functions
type Options struct {
	Workspace        string
	Env              map[string]string
	Job              *spec.Job
	Step             *spec.Step
	ExecutionContext ExecutionContext // Execution context for accessing pipeline info
}

// Info contains metadata about a builtin function
type Info struct {
	Name        string
	Description string
	Actions     []string
}
