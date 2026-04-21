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

package executor

import (
	"strings"
)

// EventContext carries attributes for CloudEvents extensions.
type EventContext struct {
	PipelineID string
	StepID     string
	StepName   string
	PluginName string
	AgentID    string
	TraceID    string
	SpanID     string
}

// Extensions builds extension attributes.
func (c EventContext) Extensions() map[string]any {
	ext := make(map[string]any)
	if c.PipelineID != "" {
		ext["pipelineId"] = c.PipelineID
	}
	if c.StepID != "" {
		ext["stepId"] = c.StepID
	}
	if c.StepName != "" {
		ext["stepName"] = c.StepName
	}
	if c.PluginName != "" {
		ext["pluginName"] = c.PluginName
	}
	if c.AgentID != "" {
		ext["agentId"] = c.AgentID
	}
	if c.TraceID != "" {
		ext["traceId"] = c.TraceID
	}
	if c.SpanID != "" {
		ext["spanId"] = c.SpanID
	}
	return ext
}

// Subject builds the CloudEvent subject value.
func (c EventContext) Subject() string {
	if c.PipelineID == "" || c.StepID == "" {
		return ""
	}
	return "pipeline/" + c.PipelineID + "/step/" + c.StepID
}

func buildEventContext(req *ExecutionRequest) EventContext {
	ctx := EventContext{}
	if req == nil {
		return ctx
	}
	if req.Pipeline != nil {
		ctx.PipelineID = req.Pipeline.Namespace
	}
	if req.Step != nil {
		stepName := strings.TrimSpace(req.Step.Name)
		if stepName != "" {
			ctx.StepID = stepName
			ctx.StepName = stepName
		}
		if req.Step.Uses != "" {
			ctx.PluginName = req.Step.Uses
		}
	}
	return ctx
}
