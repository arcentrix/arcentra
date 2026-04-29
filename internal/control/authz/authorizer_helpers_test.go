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

	"github.com/stretchr/testify/require"
)

func TestBuildSubjectKeys(t *testing.T) {
	t.Parallel()

	subject := Subject{
		UserID:  "user-1",
		TeamIDs: []string{"team-1", "", "team-2"},
	}

	keys := buildSubjectKeys(subject)
	require.Equal(t, []string{
		"user:user-1",
		"team:team-1",
		"team:team-2",
	}, keys)
}

func TestSplitAction(t *testing.T) {
	t.Parallel()

	obj, act := splitAction("pipeline:trigger")
	require.Equal(t, "pipeline", obj)
	require.Equal(t, "trigger", act)

	obj, act = splitAction("admin")
	require.Equal(t, "*", obj)
	require.Equal(t, "admin", act)

	obj, act = splitAction(":read")
	require.Equal(t, "*", obj)
	require.Equal(t, "read", act)
}
