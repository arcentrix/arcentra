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

package taskqueue

import "strings"

const (
	TaskTypeStepRun = "pipeline.step_run"
)

// StepRunTaskPayload is the payload for step run execution.
type StepRunTaskPayload struct {
	ProjectId     string            `json:"projectId,omitempty"`
	PipelineId    string            `json:"pipelineId,omitempty"`
	PipelineRunId string            `json:"pipelineRunId,omitempty"`
	JobId         string            `json:"jobId,omitempty"`
	JobName       string            `json:"jobName,omitempty"`
	StepName      string            `json:"stepName,omitempty"`
	StepIndex     int32             `json:"stepIndex,omitempty"`
	StepRunId     string            `json:"stepRunId,omitempty"`
	Uses          string            `json:"uses,omitempty"`
	Action        string            `json:"action,omitempty"`
	Args          map[string]any    `json:"args,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Workspace     string            `json:"workspace,omitempty"`
	Timeout       string            `json:"timeout,omitempty"`
	AgentId       string            `json:"agentId,omitempty"`
}

// StepRunKey returns a composite key for the step run task.
func StepRunKey(payload *StepRunTaskPayload) string {
	if payload == nil {
		return ""
	}
	parts := []string{
		payload.ProjectId,
		payload.PipelineId,
		payload.PipelineRunId,
		payload.StepRunId,
	}
	return strings.Trim(strings.Join(parts, ":"), ":")
}
