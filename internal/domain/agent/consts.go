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

// AgentStatus represents the operational state of an agent.
type AgentStatus int

const (
	AgentStatusUnknown AgentStatus = 0
	AgentStatusOnline  AgentStatus = 1
	AgentStatusOffline AgentStatus = 2
	AgentStatusBusy    AgentStatus = 3
	AgentStatusIdle    AgentStatus = 4
)

func (s AgentStatus) String() string {
	switch s {
	case AgentStatusOnline:
		return "online"
	case AgentStatusOffline:
		return "offline"
	case AgentStatusBusy:
		return "busy"
	case AgentStatusIdle:
		return "idle"
	default:
		return "unknown"
	}
}

// IsValid returns true if the status is a recognized value.
func (s AgentStatus) IsValid() bool {
	return s >= AgentStatusUnknown && s <= AgentStatusIdle
}

// StorageType represents the type of object storage backend.
type StorageType string

const (
	StorageTypeMinIO StorageType = "minio"
	StorageTypeS3    StorageType = "s3"
	StorageTypeOSS   StorageType = "oss"
	StorageTypeGCS   StorageType = "gcs"
	StorageTypeCOS   StorageType = "cos"
)

func (t StorageType) String() string {
	return string(t)
}

// IsValid returns true if the storage type is a recognized value.
func (t StorageType) IsValid() bool {
	switch t {
	case StorageTypeMinIO, StorageTypeS3, StorageTypeOSS, StorageTypeGCS, StorageTypeCOS:
		return true
	default:
		return false
	}
}
