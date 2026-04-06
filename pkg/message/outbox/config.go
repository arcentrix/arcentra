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

package outbox

import "time"

const (
	// DefaultWALDir is the default WAL root directory.
	DefaultWALDir = "./outbox"
	// DefaultSegmentMaxSeq is the default max seq count per segment.
	DefaultSegmentMaxSeq = 10000
	// DefaultFsyncInterval is the default batch fsync interval.
	DefaultFsyncInterval = 100 * time.Millisecond
	// DefaultSendBatchSize is the default batch size for sending.
	DefaultSendBatchSize = 100
	// DefaultSendInterval is the default send poll interval.
	DefaultSendInterval = 50 * time.Millisecond
	// DefaultMaxDiskUsageMB is the default max disk usage in MB.
	DefaultMaxDiskUsageMB = 5120
	// MaxScopeLen is the max length for project_id, pipeline_id, agent_id.
	MaxScopeLen = 128
)

// Config holds outbox configuration.
type Config struct {
	// WALDir is the WAL root directory.
	WALDir string
	// AgentID is the agent ID, seq scope (required).
	AgentID string
	// ProjectID is optional; when set with PipelineID, path becomes {dir}/{project_id}/{pipeline_id}/{agent_id}/
	ProjectID string
	// PipelineID is optional.
	PipelineID string
	// SegmentMaxSeq is the max seq count per segment before creating a new one.
	SegmentMaxSeq int
	// FsyncInterval is the batch fsync interval.
	FsyncInterval time.Duration
	// SendBatchSize is the batch size for each send call.
	SendBatchSize int
	// SendInterval is the send loop poll interval.
	SendInterval time.Duration
	// MaxDiskUsageMB is the max disk usage in MB; Append blocks when exceeded.
	MaxDiskUsageMB int64
}

// SetDefaults applies default values to unset fields.
func (c *Config) SetDefaults() {
	if c.WALDir == "" {
		c.WALDir = DefaultWALDir
	}
	if c.SegmentMaxSeq <= 0 {
		c.SegmentMaxSeq = DefaultSegmentMaxSeq
	}
	if c.FsyncInterval <= 0 {
		c.FsyncInterval = DefaultFsyncInterval
	}
	if c.SendBatchSize <= 0 {
		c.SendBatchSize = DefaultSendBatchSize
	}
	if c.SendInterval <= 0 {
		c.SendInterval = DefaultSendInterval
	}
	if c.MaxDiskUsageMB <= 0 {
		c.MaxDiskUsageMB = DefaultMaxDiskUsageMB
	}
}

// Validate checks config validity.
func (c *Config) Validate() error {
	if c.AgentID == "" {
		return ErrAgentIDRequired
	}
	if len(c.AgentID) > MaxScopeLen {
		return ErrScopeTooLong
	}
	if c.ProjectID != "" && len(c.ProjectID) > MaxScopeLen {
		return ErrScopeTooLong
	}
	if c.PipelineID != "" && len(c.PipelineID) > MaxScopeLen {
		return ErrScopeTooLong
	}
	return nil
}
