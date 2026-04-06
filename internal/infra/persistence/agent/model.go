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

	domain "github.com/arcentrix/arcentra/internal/domain/agent"
	"gorm.io/datatypes"
)

// AgentPO is the GORM persistence object for the t_agent table.
type AgentPO struct {
	ID        uint64         `gorm:"column:id;primaryKey;autoIncrement"`
	AgentID   string         `gorm:"column:agent_id"`
	AgentName string         `gorm:"column:agent_name"`
	Address   string         `gorm:"column:address"`
	Port      string         `gorm:"column:port"`
	OS        string         `gorm:"column:os"`
	Arch      string         `gorm:"column:arch"`
	Version   string         `gorm:"column:version"`
	Status    int            `gorm:"column:status"`
	Labels    datatypes.JSON `gorm:"column:labels"`
	Metrics   string         `gorm:"column:metrics"`
	IsEnabled int            `gorm:"column:is_enabled"`
	CreatedAt time.Time      `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt time.Time      `gorm:"column:updated_at;autoUpdateTime"`
}

func (AgentPO) TableName() string { return "t_agent" }

// ToDomain converts the persistence object to a domain entity.
func (po *AgentPO) ToDomain() *domain.Agent {
	labels := make(map[string]string)
	if len(po.Labels) > 0 {
		_ = json.Unmarshal(po.Labels, &labels)
	}

	return &domain.Agent{
		ID:        po.ID,
		AgentID:   po.AgentID,
		AgentName: po.AgentName,
		Address:   po.Address,
		Port:      po.Port,
		OS:        po.OS,
		Arch:      po.Arch,
		Version:   po.Version,
		Status:    domain.AgentStatus(po.Status),
		Labels:    labels,
		Metrics:   po.Metrics,
		IsEnabled: po.IsEnabled == 1,
		CreatedAt: po.CreatedAt,
		UpdatedAt: po.UpdatedAt,
	}
}

// AgentPOFromDomain creates a persistence object from a domain entity.
func AgentPOFromDomain(a *domain.Agent) *AgentPO {
	var labelsJSON datatypes.JSON
	if a.Labels != nil {
		labelsJSON, _ = json.Marshal(a.Labels)
	}

	isEnabled := 0
	if a.IsEnabled {
		isEnabled = 1
	}

	return &AgentPO{
		ID:        a.ID,
		AgentID:   a.AgentID,
		AgentName: a.AgentName,
		Address:   a.Address,
		Port:      a.Port,
		OS:        a.OS,
		Arch:      a.Arch,
		Version:   a.Version,
		Status:    int(a.Status),
		Labels:    labelsJSON,
		Metrics:   a.Metrics,
		IsEnabled: isEnabled,
		CreatedAt: a.CreatedAt,
		UpdatedAt: a.UpdatedAt,
	}
}

// StorageConfigPO is the GORM persistence object for the t_storage_config table.
type StorageConfigPO struct {
	ID          uint64         `gorm:"column:id;primaryKey;autoIncrement"`
	StorageID   string         `gorm:"column:storage_id"`
	Name        string         `gorm:"column:name"`
	StorageType string         `gorm:"column:storage_type"`
	Config      datatypes.JSON `gorm:"column:config"`
	Description string         `gorm:"column:description"`
	IsDefault   int            `gorm:"column:is_default"`
	IsEnabled   int            `gorm:"column:is_enabled"`
	CreatedAt   time.Time      `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt   time.Time      `gorm:"column:updated_at;autoUpdateTime"`
}

func (StorageConfigPO) TableName() string { return "t_storage_config" }

// ToDomain converts the persistence object to a domain entity.
func (po *StorageConfigPO) ToDomain() *domain.StorageConfig {
	return &domain.StorageConfig{
		ID:          po.ID,
		StorageID:   po.StorageID,
		Name:        po.Name,
		StorageType: domain.StorageType(po.StorageType),
		Config:      json.RawMessage(po.Config),
		Description: po.Description,
		IsDefault:   po.IsDefault == 1,
		IsEnabled:   po.IsEnabled == 1,
		CreatedAt:   po.CreatedAt,
		UpdatedAt:   po.UpdatedAt,
	}
}

// StorageConfigPOFromDomain creates a persistence object from a domain entity.
func StorageConfigPOFromDomain(sc *domain.StorageConfig) *StorageConfigPO {
	isDefault := 0
	if sc.IsDefault {
		isDefault = 1
	}
	isEnabled := 0
	if sc.IsEnabled {
		isEnabled = 1
	}

	return &StorageConfigPO{
		ID:          sc.ID,
		StorageID:   sc.StorageID,
		Name:        sc.Name,
		StorageType: string(sc.StorageType),
		Config:      datatypes.JSON(sc.Config),
		Description: sc.Description,
		IsDefault:   isDefault,
		IsEnabled:   isEnabled,
		CreatedAt:   sc.CreatedAt,
		UpdatedAt:   sc.UpdatedAt,
	}
}
