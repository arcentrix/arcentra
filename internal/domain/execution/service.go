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

package execution

import "fmt"

// ExecutionDomainService contains pure domain logic for the execution context.
type ExecutionDomainService struct{}

func NewExecutionDomainService() *ExecutionDomainService {
	return &ExecutionDomainService{}
}

// ValidateStepRunStatusTransition checks whether a step run status change is allowed.
func (s *ExecutionDomainService) ValidateStepRunStatusTransition(from, to StepRunStatus) error {
	allowed := map[StepRunStatus][]StepRunStatus{
		StepRunStatusWaiting:   {StepRunStatusQueued, StepRunStatusSkipped, StepRunStatusCancelled},
		StepRunStatusQueued:    {StepRunStatusRunning, StepRunStatusCancelled},
		StepRunStatusRunning:   {StepRunStatusSuccess, StepRunStatusFailed, StepRunStatusCancelled, StepRunStatusTimeout},
		StepRunStatusSuccess:   {},
		StepRunStatusFailed:    {StepRunStatusWaiting},
		StepRunStatusCancelled: {},
		StepRunStatusTimeout:   {StepRunStatusWaiting},
		StepRunStatusSkipped:   {},
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

// CanRetry checks whether a step run is eligible for retry.
func (s *ExecutionDomainService) CanRetry(sr *StepRun) error {
	if !sr.Status.IsTerminal() {
		return fmt.Errorf("step run %s is still in progress (status: %s)", sr.StepRunID, sr.Status)
	}
	if sr.Status == StepRunStatusSuccess || sr.Status == StepRunStatusSkipped {
		return fmt.Errorf("step run %s does not need retry (status: %s)", sr.StepRunID, sr.Status)
	}
	if sr.CurrentRetry >= sr.RetryCount {
		return fmt.Errorf("step run %s has exhausted all %d retries", sr.StepRunID, sr.RetryCount)
	}
	return nil
}
