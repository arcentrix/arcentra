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
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/shared/storage"
	"github.com/arcentrix/arcentra/pkg/log"
	"gorm.io/gorm"
)

// ArtifactCleaner scans for expired step-run artifacts and removes them
// from both the database and object storage.
type ArtifactCleaner struct {
	db      *gorm.DB
	storage storage.IStorage
}

// NewArtifactCleaner creates a new cleaner.
func NewArtifactCleaner(db *gorm.DB, st storage.IStorage) *ArtifactCleaner {
	return &ArtifactCleaner{db: db, storage: st}
}

// CleanExpired finds artifacts past their expired_at and deletes them.
// Returns the number of artifacts cleaned.
func (ac *ArtifactCleaner) CleanExpired(ctx context.Context) int {
	if ac.db == nil {
		return 0
	}

	var artifacts []model.StepRunArtifact
	now := time.Now()

	if err := ac.db.WithContext(ctx).
		Where("expired_at IS NOT NULL AND expired_at < ?", now).
		Limit(500).
		Find(&artifacts).Error; err != nil {
		log.Warnw("failed to query expired artifacts", "error", err)
		return 0
	}

	if len(artifacts) == 0 {
		return 0
	}

	cleaned := 0
	for i := range artifacts {
		art := &artifacts[i]

		// Remove from object storage if available.
		if ac.storage != nil && art.Destination != "" {
			if err := ac.storage.Delete(ctx, art.Destination); err != nil {
				log.Warnw("failed to delete artifact from storage",
					"artifactId", art.ArtifactID, "destination", art.Destination, "error", err)
			}
		}

		// Remove the DB record.
		if err := ac.db.WithContext(ctx).
			Where("artifact_id = ?", art.ArtifactID).
			Delete(&model.StepRunArtifact{}).Error; err != nil {
			log.Warnw("failed to delete artifact record", "artifactId", art.ArtifactID, "error", err)
			continue
		}
		cleaned++
	}

	if cleaned > 0 {
		log.Infow("expired artifacts cleaned", "count", cleaned)
	}
	return cleaned
}
