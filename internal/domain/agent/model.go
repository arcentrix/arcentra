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
	"encoding/json"
	"time"
)

// Agent represents a build agent in the CI/CD system.
type Agent struct {
	ID        uint64            `json:"id"`
	AgentID   string            `json:"agentId"`
	AgentName string            `json:"agentName"`
	Address   string            `json:"address"`
	Port      string            `json:"port"`
	OS        string            `json:"os"`
	Arch      string            `json:"arch"`
	Version   string            `json:"version"`
	Status    AgentStatus       `json:"status"`
	Labels    map[string]string `json:"labels"`
	Metrics   string            `json:"metrics"`
	IsEnabled bool              `json:"isEnabled"`
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

// IsOnline returns true if the agent is currently online.
func (a *Agent) IsOnline() bool {
	return a.Status == AgentStatusOnline
}

// CanAcceptWork returns true if the agent is enabled, online, and not busy.
func (a *Agent) CanAcceptWork() bool {
	return a.IsEnabled && (a.Status == AgentStatusOnline || a.Status == AgentStatusIdle)
}

// StorageConfig represents an object storage configuration.
type StorageConfig struct {
	ID          uint64          `json:"id"`
	StorageID   string          `json:"storageId"`
	Name        string          `json:"name"`
	StorageType StorageType     `json:"storageType"`
	Config      json.RawMessage `json:"config"`
	Description string          `json:"description"`
	IsDefault   bool            `json:"isDefault"`
	IsEnabled   bool            `json:"isEnabled"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

// StorageConfigDetail holds parsed storage connection details.
type StorageConfigDetail struct {
	Endpoint  string `json:"endpoint"`
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
	Bucket    string `json:"bucket"`
	Region    string `json:"region"`
	UseTLS    bool   `json:"useTLS"`
	BasePath  string `json:"basePath"`
}
