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

import (
	"fmt"
	"strings"
)

// MaxEventIDLength 限制构建日志事件标识的最大字节数
const MaxEventIDLength = 128

// BuildLogMessage 表示通过 Kafka 传递的构建日志
type BuildLogMessage struct {
	// EventID 在首次持久化前生成，并在所有发布重试中保持不变
	EventID       string `json:"eventId,omitempty"`
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

// ValidateEventID 校验可选事件标识，空字符串兼容旧版生产者
func ValidateEventID(eventID string) error {
	if len(eventID) > MaxEventIDLength {
		return fmt.Errorf("build log event id exceeds %d bytes", MaxEventIDLength)
	}
	for i := range len(eventID) {
		c := eventID[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' {
			continue
		}
		return fmt.Errorf("build log event id contains an invalid character at byte %d", i)
	}
	return nil
}

// BuildLogKey 返回用于日志消息分区的组合键
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
