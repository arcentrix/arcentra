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
	"testing"

	"github.com/arcentrix/arcentra/internal/control/consts"
	"github.com/stretchr/testify/require"
)

func TestBuildScopeKey(t *testing.T) {
	t.Parallel()

	require.Equal(t, platformScopeKey, buildScopeKey("", ""))
	require.Equal(t, platformScopeKey, buildScopeKey(string(consts.ScopeTypePlatform), "any"))
	require.Equal(t, "project:proj-1", buildScopeKey(string(consts.ScopeTypeProject), "proj-1"))
}

func TestBuildCandidateScopeKeys(t *testing.T) {
	t.Parallel()

	resource := ResourceRef{
		Type:      string(consts.ScopeTypePipeline),
		ID:        "pipe-1",
		TeamID:    "team-1",
		ProjectID: "proj-1",
		OrgID:     "org-1",
	}

	keys := buildCandidateScopeKeys(resource)
	require.Equal(t, []string{
		"pipeline:pipe-1",
		"team:team-1",
		"project:proj-1",
		"org:org-1",
		platformScopeKey,
	}, keys)
}
