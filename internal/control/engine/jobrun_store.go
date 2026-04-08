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

package engine

import (
	"context"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
)

// IJobRunStore abstracts DB operations needed by the execution engine
// (TaskFramework) to manage JobRun and StepRun records without importing
// the full repo layer directly.
type IJobRunStore interface {
	CreateJobRun(ctx context.Context, jr *model.JobRun) error
	GetJobRunStatus(ctx context.Context, jobRunID string) (int, error)
	UpdateJobRun(ctx context.Context, jobRunID string, updates map[string]any) error
	CreateStepRun(ctx context.Context, sr *model.StepRun) error
	UpdateStepRun(ctx context.Context, stepRunID string, updates map[string]any) error
}

// JobRunStore adapts repo interfaces into IJobRunStore.
type JobRunStore struct {
	jobRunRepo  repo.IJobRunRepository
	stepRunRepo repo.IStepRunRepository
}

// NewJobRunStore creates a new store backed by the given repositories.
func NewJobRunStore(jr repo.IJobRunRepository, sr repo.IStepRunRepository) *JobRunStore {
	return &JobRunStore{jobRunRepo: jr, stepRunRepo: sr}
}

// CreateJobRun persists a new job run record.
func (s *JobRunStore) CreateJobRun(ctx context.Context, jr *model.JobRun) error {
	return s.jobRunRepo.Create(ctx, jr)
}

// GetJobRunStatus returns the current status of a job run (lightweight).
func (s *JobRunStore) GetJobRunStatus(ctx context.Context, jobRunID string) (int, error) {
	return s.jobRunRepo.GetStatus(ctx, jobRunID)
}

// UpdateJobRun patches a job run record by its business ID.
func (s *JobRunStore) UpdateJobRun(ctx context.Context, jobRunID string, updates map[string]any) error {
	return s.jobRunRepo.UpdateByJobRunID(ctx, jobRunID, updates)
}

// CreateStepRun persists a new step run record.
func (s *JobRunStore) CreateStepRun(ctx context.Context, sr *model.StepRun) error {
	return s.stepRunRepo.Create(ctx, sr)
}

// UpdateStepRun patches a step run record by its business ID.
func (s *JobRunStore) UpdateStepRun(ctx context.Context, stepRunID string, updates map[string]any) error {
	return s.stepRunRepo.PatchByStepRunID(ctx, stepRunID, updates)
}
