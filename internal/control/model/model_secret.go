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

// Secret secret model
type Secret struct {
	BaseModel
	SecretID    string `gorm:"column:secret_id;type:varchar(64);uniqueIndex:uk_secret_business_id" json:"secretId"`
	Name        string `gorm:"column:name;type:varchar(128);uniqueIndex:uk_secret_scope_name,priority:3" json:"name"`
	SecretType  string `gorm:"column:secret_type" json:"secretType"`
	SecretValue string `gorm:"column:secret_value" json:"-"`
	Description string `gorm:"column:description" json:"description"`
	Scope       string `gorm:"column:scope;type:varchar(16);uniqueIndex:uk_secret_scope_name,priority:1" json:"scope"`
	ScopeID     string `gorm:"column:scope_id;type:varchar(64);not null;default:'';uniqueIndex:uk_secret_scope_name,priority:2" json:"scopeId"`
	Version     uint64 `gorm:"column:version;not null;default:1" json:"-"`
	IsEnabled   int    `gorm:"column:is_enabled;not null;default:1" json:"isEnabled"`
	CreatedBy   string `gorm:"column:created_by" json:"createdBy"`
}

func (Secret) TableName() string {
	return "secret"
}
