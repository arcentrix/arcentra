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
	"gorm.io/datatypes"
)

// Setting represents a workspace-scoped configuration entry.
// Unique constraint: (workspace, name).
type Setting struct {
	BaseModel
	Name  string         `gorm:"column:name;type:varchar(255);not null;uniqueIndex:uk_workspace_name" json:"name"`
	Value datatypes.JSON `gorm:"column:value;type:json;not null" json:"value"`
}

// TableName returns the database table name.
func (Setting) TableName() string {
	return "setting"
}
