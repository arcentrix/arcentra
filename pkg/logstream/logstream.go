// Copyright 2025 Arcentra Team
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

package logstream

import "strings"

// BuildLogMessage represents a build log message sent through Kafka.
type BuildLogMessage struct {
	ProjectId     string `json:"projectId,omitempty"`
	PipelineId    string `json:"pipelineId,omitempty"`
	PipelineRunId string `json:"pipelineRunId,omitempty"`
	JobId         string `json:"jobId,omitempty"`
	JobName       string `json:"jobName,omitempty"`
	StepName      string `json:"stepName,omitempty"`
	StepRunId     string `json:"stepRunId,omitempty"`
	Timestamp     int64  `json:"timestamp"`
	LineNumber    int32  `json:"lineNumber"`
	Level         string `json:"level,omitempty"`
	Stream        string `json:"stream,omitempty"`
	Content       string `json:"content,omitempty"`
	AgentId       string `json:"agentId,omitempty"`
	PluginName    string `json:"pluginName,omitempty"`
}

// BuildLogKey returns a composite key for log message partitioning.
func (m *BuildLogMessage) BuildLogKey() string {
	if m == nil {
		return ""
	}
	parts := []string{
		m.ProjectId,
		m.PipelineId,
		m.PipelineRunId,
		m.StepRunId,
	}
	return strings.Trim(strings.Join(parts, ":"), ":")
}
