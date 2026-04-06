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

package logstream

import "strings"

// BuildLogMessage represents a build log message sent through Kafka.
type BuildLogMessage struct {
	ProjectID     string `json:"projectId,omitempty"`
	PipelineID    string `json:"pipelineId,omitempty"`
	PipelineRunID string `json:"pipelineRunId,omitempty"`
	JobID         string `json:"jobId,omitempty"`
	JobName       string `json:"jobName,omitempty"`
	StepName      string `json:"stepName,omitempty"`
	StepRunID     string `json:"stepRunId,omitempty"`
	Timestamp     int64  `json:"timestamp"`
	LineNumber    int32  `json:"lineNumber"`
	Level         string `json:"level,omitempty"`
	Stream        string `json:"stream,omitempty"`
	Content       string `json:"content,omitempty"`
	AgentID       string `json:"agentId,omitempty"`
	PluginName    string `json:"pluginName,omitempty"`
}

// BuildLogKey returns a composite key for log message partitioning.
func (m *BuildLogMessage) BuildLogKey() string {
	if m == nil {
		return ""
	}
	parts := []string{
		m.ProjectID,
		m.PipelineID,
		m.PipelineRunID,
		m.StepRunID,
	}
	return strings.Trim(strings.Join(parts, ":"), ":")
}
