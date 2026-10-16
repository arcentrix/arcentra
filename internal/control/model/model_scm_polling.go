// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package model

import "time"

// SCMPollingStream 保存显式激活、完整游标及未确认派发批次，不携带提供方凭证
type SCMPollingStream struct {
	LastAttemptAt          *time.Time `gorm:"column:last_attempt_at" json:"lastAttemptAt"`
	PendingObservationJSON string     `gorm:"column:pending_observation_json;type:mediumtext" json:"-"`
	BaselineCutoff         string     `gorm:"column:baseline_cutoff;type:varchar(35);not null;default:''" json:"baselineCutoff"`
	LastObservationCount   int        `gorm:"column:last_observation_count;not null;default:0" json:"lastObservationCount"`
	LastTargetCount        int        `gorm:"column:last_target_count;not null;default:0" json:"lastTargetCount"`
	LastCompletedAt        *time.Time `gorm:"column:last_completed_at" json:"lastCompletedAt"`
	NextPollAt             *time.Time `gorm:"column:next_poll_at" json:"nextPollAt"`
	LastPollError          string     `gorm:"column:last_poll_error;type:varchar(64)" json:"lastPollError"`
	BaseModel
	StreamID             string     `gorm:"column:stream_id;type:varchar(64);uniqueIndex:uk_scm_polling_stream" json:"streamId"`
	ProjectID            string     `gorm:"column:project_id;type:varchar(64);uniqueIndex:uk_scm_polling_project" json:"projectId"`
	ScopeJSON            string     `gorm:"column:scope_json;type:mediumtext" json:"-"`
	ScopeDigest          string     `gorm:"column:scope_digest;type:varchar(67);uniqueIndex:uk_scm_polling_scope" json:"scopeDigest"`
	Revision             uint64     `gorm:"column:revision;not null;default:1" json:"revision"`
	State                string     `gorm:"column:state;type:varchar(32);not null;default:disabled;index" json:"state"`
	ActivationGeneration uint64     `gorm:"column:activation_generation;not null;default:1" json:"activationGeneration"`
	EnabledBy            string     `gorm:"column:enabled_by;type:varchar(64)" json:"enabledBy"`
	EnabledAt            *time.Time `gorm:"column:enabled_at" json:"enabledAt"`
	DisabledBy           string     `gorm:"column:disabled_by;type:varchar(64)" json:"disabledBy"`
	DisabledAt           *time.Time `gorm:"column:disabled_at" json:"disabledAt"`
	PauseReason          string     `gorm:"column:pause_reason;type:varchar(64)" json:"pauseReason"`
	BaselineID           string     `gorm:"column:baseline_id;type:varchar(64)" json:"baselineId"`
	BaselineBy           string     `gorm:"column:baseline_by;type:varchar(64)" json:"baselineBy"`
	BaselineStartedAt    *time.Time `gorm:"column:baseline_started_at" json:"baselineStartedAt"`
	BaselineCompletedAt  *time.Time `gorm:"column:baseline_completed_at" json:"baselineCompletedAt"`
	BaselineDigest       string     `gorm:"column:baseline_digest;type:varchar(67)" json:"baselineDigest"`
	BaselineCount        int        `gorm:"column:baseline_count;not null;default:0" json:"baselineCount"`
	CursorJSON           string     `gorm:"column:cursor_json;type:mediumtext" json:"-"`
	SelectionJSON        string     `gorm:"column:selection_json;type:mediumtext" json:"-"`
	SelectionDigest      string     `gorm:"column:selection_digest;type:varchar(67)" json:"selectionDigest"`
	PendingBatchJSON     string     `gorm:"column:pending_batch_json;type:mediumtext" json:"-"`
}

// TableName 返回轮询流表名
func (SCMPollingStream) TableName() string { return "scm_polling_stream" }
