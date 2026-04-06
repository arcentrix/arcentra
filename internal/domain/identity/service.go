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

package identity

import (
	"fmt"
	"unicode"
)

// IdentityDomainService contains pure domain logic for the identity context.
type IdentityDomainService struct{}

func NewIdentityDomainService() *IdentityDomainService {
	return &IdentityDomainService{}
}

const minPasswordLength = 8

// ValidatePasswordStrength checks that the password meets minimum complexity requirements.
func (s *IdentityDomainService) ValidatePasswordStrength(password string) error {
	if len(password) < minPasswordLength {
		return fmt.Errorf("password must be at least %d characters", minPasswordLength)
	}

	var hasUpper, hasLower, hasDigit bool
	for _, ch := range password {
		switch {
		case unicode.IsUpper(ch):
			hasUpper = true
		case unicode.IsLower(ch):
			hasLower = true
		case unicode.IsDigit(ch):
			hasDigit = true
		}
	}

	if !hasUpper {
		return fmt.Errorf("password must contain at least one uppercase letter")
	}
	if !hasLower {
		return fmt.Errorf("password must contain at least one lowercase letter")
	}
	if !hasDigit {
		return fmt.Errorf("password must contain at least one digit")
	}
	return nil
}

// IsBuiltinRole returns true if the role ID is a system built-in.
func (s *IdentityDomainService) IsBuiltinRole(roleID string) bool {
	switch roleID {
	case RoleOwner, RoleAdmin, RoleMember:
		return true
	default:
		return false
	}
}

// CanDeleteTeam checks whether a team can be deleted based on its current state.
func (s *IdentityDomainService) CanDeleteTeam(team *Team) error {
	if team.TotalMembers > 0 {
		return fmt.Errorf("cannot delete team with %d active members", team.TotalMembers)
	}
	if team.TotalProjects > 0 {
		return fmt.Errorf("cannot delete team with %d associated projects", team.TotalProjects)
	}
	return nil
}
