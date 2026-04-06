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

import "time"

// AgentRegistered is raised when a new agent is registered in the system.
type AgentRegistered struct {
	AgentID    string
	AgentName  string
	OccurredAt time.Time
}

func (e AgentRegistered) EventType() string { return "agent.registered" }

// AgentStatusChanged is raised when an agent transitions between statuses.
type AgentStatusChanged struct {
	AgentID    string
	OldStatus  AgentStatus
	NewStatus  AgentStatus
	OccurredAt time.Time
}

func (e AgentStatusChanged) EventType() string { return "agent.status_changed" }

// AgentDeleted is raised when an agent is removed from the system.
type AgentDeleted struct {
	AgentID    string
	OccurredAt time.Time
}

func (e AgentDeleted) EventType() string { return "agent.deleted" }

// StorageConfigCreated is raised when a new storage configuration is added.
type StorageConfigCreated struct {
	StorageID   string
	StorageType StorageType
	OccurredAt  time.Time
}

func (e StorageConfigCreated) EventType() string { return "storage_config.created" }

// StorageConfigDefaultChanged is raised when the default storage configuration changes.
type StorageConfigDefaultChanged struct {
	OldStorageID string
	NewStorageID string
	OccurredAt   time.Time
}

func (e StorageConfigDefaultChanged) EventType() string { return "storage_config.default_changed" }
