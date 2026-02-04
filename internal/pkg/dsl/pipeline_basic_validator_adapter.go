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
	"github.com/arcentrix/arcade/internal/pkg/pipeline/spec"
	"github.com/arcentrix/arcade/internal/pkg/pipeline/validation"
)

// PipelineBasicValidatorAdapter adapts DSLParser to implement PipelineBasicValidator interface
// This adapter decouples the Validator from the specific DSLParser implementation
type PipelineBasicValidatorAdapter struct {
	parser *DSLParser
}

// NewPipelineBasicValidatorAdapter creates a new adapter
func NewPipelineBasicValidatorAdapter(parser *DSLParser) *PipelineBasicValidatorAdapter {
	return &PipelineBasicValidatorAdapter{
		parser: parser,
	}
}

// ValidateBasic performs basic validation on a pipeline
// This delegates to the parser's validate method
func (a *PipelineBasicValidatorAdapter) ValidateBasic(p *spec.Pipeline) error {
	return a.parser.validate(p)
}

// Ensure PipelineBasicValidatorAdapter implements PipelineBasicValidator interface
var _ validation.PipelineBasicValidator = (*PipelineBasicValidatorAdapter)(nil)
