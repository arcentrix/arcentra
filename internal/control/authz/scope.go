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

package authz

import (
	"fmt"

	"github.com/arcentrix/arcentra/internal/control/consts"
)

const platformScopeKey = "platform:"

func buildScopeKey(scopeType, scopeID string) string {
	if scopeType == "" || scopeType == string(consts.ScopeTypePlatform) {
		return platformScopeKey
	}
	return fmt.Sprintf("%s:%s", scopeType, scopeID)
}

// buildCandidateScopeKeys 构造作用域候选链，顺序从细粒度到粗粒度
func buildCandidateScopeKeys(resource ResourceRef) []string {
	keys := make([]string, 0, 5)
	if resource.Type == string(consts.ScopeTypePipeline) && resource.ID != "" {
		keys = append(keys, buildScopeKey(string(consts.ScopeTypePipeline), resource.ID))
	}
	if resource.TeamID != "" {
		keys = append(keys, buildScopeKey(string(consts.ScopeTypeTeam), resource.TeamID))
	}
	if resource.ProjectID != "" {
		keys = append(keys, buildScopeKey(string(consts.ScopeTypeProject), resource.ProjectID))
	}
	if resource.OrgID != "" {
		keys = append(keys, buildScopeKey(string(consts.ScopeTypeOrganization), resource.OrgID))
	}
	keys = append(keys, platformScopeKey)
	return keys
}
