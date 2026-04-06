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

package pipeline

import "fmt"

// PipelineDomainService contains pure domain logic for the pipeline context.
type PipelineDomainService struct{}

func NewPipelineDomainService() *PipelineDomainService {
	return &PipelineDomainService{}
}

// ValidateRunStatusTransition checks whether a pipeline run status change is allowed.
func (s *PipelineDomainService) ValidateRunStatusTransition(from, to PipelineStatus) error {
	allowed := map[PipelineStatus][]PipelineStatus{
		PipelineStatusUnknown:   {PipelineStatusPending},
		PipelineStatusPending:   {PipelineStatusRunning, PipelineStatusCancelled},
		PipelineStatusRunning:   {PipelineStatusSuccess, PipelineStatusFailed, PipelineStatusCancelled, PipelineStatusPaused},
		PipelineStatusPaused:    {PipelineStatusRunning, PipelineStatusCancelled},
		PipelineStatusSuccess:   {},
		PipelineStatusFailed:    {},
		PipelineStatusCancelled: {},
	}

	targets, ok := allowed[from]
	if !ok {
		return fmt.Errorf("no transitions defined from status %s", from)
	}
	for _, t := range targets {
		if t == to {
			return nil
		}
	}
	return fmt.Errorf("transition from %s to %s is not allowed", from, to)
}

// CanTriggerRun checks whether a pipeline can accept a new run.
func (s *PipelineDomainService) CanTriggerRun(p *Pipeline) error {
	if !p.IsEnabled {
		return fmt.Errorf("pipeline %s is disabled", p.PipelineID)
	}
	return nil
}
