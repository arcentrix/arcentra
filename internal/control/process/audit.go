// Copyright 2026 Arcentra Authors.
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

package process

import (
	"context"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/log"
	"gorm.io/gorm"
)

// AuditWriter writes audit log entries to the database.
type AuditWriter struct {
	db *gorm.DB
}

// NewAuditWriter creates an audit writer backed by gorm.
func NewAuditWriter(db *gorm.DB) *AuditWriter {
	if db == nil {
		return nil
	}
	return &AuditWriter{db: db}
}

// Write persists a single audit log entry.
func (w *AuditWriter) Write(ctx context.Context, entry *model.AuditLog) {
	if w == nil || entry == nil {
		return
	}
	if err := w.db.WithContext(ctx).Create(entry).Error; err != nil {
		log.Warnw("failed to write audit log", "action", entry.Action, "resource", entry.ResourceID, "error", err)
	}
}

// PipelineAudit is a convenience helper to create a pipeline audit log entry.
func PipelineAudit(action, userID, resourceID, resourceName string) *model.AuditLog {
	return &model.AuditLog{
		UserID:       userID,
		Action:       action,
		ResourceType: "pipeline_run",
		ResourceID:   resourceID,
		ResourceName: resourceName,
	}
}
