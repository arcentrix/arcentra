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

package model

import (
	"time"

	"gorm.io/datatypes"
)

type Agent struct {
	BaseModel
	AgentID       string         `gorm:"column:agent_id" json:"agentId"`
	AgentName     string         `gorm:"column:agent_name" json:"agentName"`
	Address       string         `gorm:"column:address" json:"address"`
	Port          string         `gorm:"column:port" json:"port"`
	OS            string         `gorm:"column:os" json:"os"`
	Arch          string         `gorm:"column:arch" json:"arch"`
	Version       string         `gorm:"column:version" json:"version"`
	Status        int            `gorm:"column:status" json:"status"` // 0: unknown, 1: online, 2: offline, 3: busy, 4: idle
	Labels        datatypes.JSON `gorm:"column:labels" json:"labels"`
	Metrics       string         `gorm:"column:metrics" json:"metrics"`
	LastHeartbeat *time.Time     `gorm:"column:last_heartbeat" json:"lastHeartbeat,omitempty"` // 最后一次心跳时间
	IsEnabled     int            `gorm:"column:is_enabled" json:"isEnabled"`                   // 0: disable, 1: enable
	RegisteredBy  string         `gorm:"column:registered_by" json:"registeredBy"`             // admin, dynamic
}

func (a *Agent) TableName() string {
	return "agent"
}

// UpdateAgentReq request for updating agent (AgentId is not allowed to be modified)
type UpdateAgentReq struct {
	AgentName *string        `json:"agentName,omitempty"`
	Address   *string        `json:"address,omitempty"`
	Port      *string        `json:"port,omitempty"`
	OS        *string        `json:"os,omitempty"`
	Arch      *string        `json:"arch,omitempty"`
	Version   *string        `json:"version,omitempty"`
	Status    *int           `json:"status,omitempty"`
	Labels    datatypes.JSON `json:"labels,omitempty"`
	Metrics   datatypes.JSON `json:"metrics,omitempty"`
	IsEnabled *int           `json:"isEnabled,omitempty"`
}

// AgentDetail response for agent detail
type AgentDetail struct {
	Agent
}
