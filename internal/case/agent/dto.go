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

package agent

import (
	"time"

	"github.com/arcentrix/arcentra/internal/domain/agent"
)

// RegisterAgentInput is the input DTO for registering a new agent.
type RegisterAgentInput struct {
	AgentName string            `json:"agentName"`
	Labels    map[string]string `json:"labels"`
}

// RegisterAgentOutput is the output DTO after registering a new agent.
type RegisterAgentOutput struct {
	Agent agent.Agent `json:"agent"`
	Token string      `json:"token"`
}

// UpdateAgentInput is the input DTO for updating an agent.
type UpdateAgentInput struct {
	AgentName *string           `json:"agentName"`
	Labels    map[string]string `json:"labels"`
}

// ListAgentsInput is the input DTO for listing agents with pagination.
type ListAgentsInput struct {
	Page int `json:"page"`
	Size int `json:"size"`
}

// ListAgentsOutput is the output DTO for a paginated agent list.
type ListAgentsOutput struct {
	Agents []agent.Agent `json:"agents"`
	Total  int64         `json:"total"`
}

// StatisticsOutput is the output DTO for agent statistics.
type StatisticsOutput struct {
	Total   int64 `json:"total"`
	Online  int64 `json:"online"`
	Offline int64 `json:"offline"`
}

// UploadFileInput is the input DTO for uploading a file.
type UploadFileInput struct {
	FileName    string `json:"fileName"`
	FileSize    int64  `json:"fileSize"`
	ContentType string `json:"contentType"`
	StorageID   string `json:"storageId"`
	CustomPath  string `json:"customPath"`
}

// UploadFileOutput is the output DTO for a file upload result.
type UploadFileOutput struct {
	ObjectName   string    `json:"objectName"`
	FileURL      string    `json:"fileUrl"`
	OriginalName string    `json:"originalName"`
	Size         int64     `json:"size"`
	ContentType  string    `json:"contentType"`
	StorageID    string    `json:"storageId"`
	StorageType  string    `json:"storageType"`
	UploadTime   time.Time `json:"uploadTime"`
}
