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

package taskqueue

import "strings"

const (
	// TaskTypeJobRun identifies job-run execution tasks.
	// A job is the minimum schedulable unit dispatched to an Agent.
	TaskTypeJobRun = "pipeline.job_run"
)

// JobRunTaskPayload is the Kafka payload for dispatching a complete job to an Agent.
// The Agent receives the entire job definition and executes all steps sequentially.
type JobRunTaskPayload struct {
	PipelineID    string            `json:"pipelineId"`
	PipelineRunID string            `json:"pipelineRunId"`
	JobRunID      string            `json:"jobRunId"`
	JobName       string            `json:"jobName"`
	AgentID       string            `json:"agentId,omitempty"`
	Steps         []StepPayload     `json:"steps"`
	Env           map[string]string `json:"env,omitempty"`
	Workspace     string            `json:"workspace,omitempty"`
	Timeout       string            `json:"timeout,omitempty"`
	ArtifactURIs  map[string]string `json:"artifactUris,omitempty"`
	Source        *SourcePayload    `json:"source,omitempty"`
}

// StepPayload describes a single step inside a JobRunTaskPayload.
type StepPayload struct {
	Name            string            `json:"name"`
	StepIndex       int32             `json:"stepIndex"`
	StepRunID       string            `json:"stepRunId"`
	Uses            string            `json:"uses"`
	Action          string            `json:"action,omitempty"`
	Args            map[string]any    `json:"args,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	ContinueOnError bool              `json:"continueOnError,omitempty"`
	Timeout         string            `json:"timeout,omitempty"`
	When            string            `json:"when,omitempty"`
}

// SourcePayload describes source code configuration for a job.
type SourcePayload struct {
	Type   string `json:"type"`
	Repo   string `json:"repo,omitempty"`
	Branch string `json:"branch,omitempty"`
}

// JobRunKey returns a composite key for the job run task.
func JobRunKey(payload *JobRunTaskPayload) string {
	if payload == nil {
		return ""
	}
	parts := []string{
		payload.PipelineID,
		payload.PipelineRunID,
		payload.JobRunID,
	}
	return strings.Trim(strings.Join(parts, ":"), ":")
}
