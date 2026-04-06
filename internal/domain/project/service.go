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

package project

import "fmt"

// ProjectDomainService contains pure domain logic for the project context.
type ProjectDomainService struct{}

func NewProjectDomainService() *ProjectDomainService {
	return &ProjectDomainService{}
}

// ValidateStatusTransition checks whether a project status change is allowed.
func (s *ProjectDomainService) ValidateStatusTransition(from, to ProjectStatus) error {
	allowed := map[ProjectStatus][]ProjectStatus{
		ProjectStatusInactive: {ProjectStatusActive},
		ProjectStatusActive:   {ProjectStatusArchived, ProjectStatusDisabled},
		ProjectStatusArchived: {ProjectStatusActive},
		ProjectStatusDisabled: {ProjectStatusActive},
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

// CanDeleteProject checks whether a project can be safely deleted.
func (s *ProjectDomainService) CanDeleteProject(p *Project) error {
	if p.Status == ProjectStatusActive && p.TotalPipelines > 0 {
		return fmt.Errorf("cannot delete active project with %d pipelines", p.TotalPipelines)
	}
	return nil
}
