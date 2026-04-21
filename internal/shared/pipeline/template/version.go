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

package template

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// semver holds parsed semantic version components.
type semver struct {
	Major      int
	Minor      int
	Patch      int
	PreRelease string
	Raw        string
}

// ParseSemver parses a version string (with optional "v" prefix) into
// its major.minor.patch components. Pre-release suffixes (e.g. -rc1) are
// preserved but rank lower during comparison.
func ParseSemver(version string) (semver, error) {
	raw := version
	v := strings.TrimPrefix(version, "v")
	parts := strings.SplitN(v, "-", 2)
	core := parts[0]
	pre := ""
	if len(parts) > 1 {
		pre = parts[1]
	}

	segments := strings.SplitN(core, ".", 3)
	if len(segments) != 3 {
		return semver{Raw: raw}, fmt.Errorf("invalid semver: %s", version)
	}

	major, err := strconv.Atoi(segments[0])
	if err != nil {
		return semver{Raw: raw}, fmt.Errorf("invalid major version: %s", version)
	}
	minor, err := strconv.Atoi(segments[1])
	if err != nil {
		return semver{Raw: raw}, fmt.Errorf("invalid minor version: %s", version)
	}
	patch, err := strconv.Atoi(segments[2])
	if err != nil {
		return semver{Raw: raw}, fmt.Errorf("invalid patch version: %s", version)
	}

	return semver{Major: major, Minor: minor, Patch: patch, PreRelease: pre, Raw: raw}, nil
}

// Less returns true if s ranks lower than other according to semver rules.
func (s semver) Less(other semver) bool {
	if s.Major != other.Major {
		return s.Major < other.Major
	}
	if s.Minor != other.Minor {
		return s.Minor < other.Minor
	}
	if s.Patch != other.Patch {
		return s.Patch < other.Patch
	}
	// pre-release < release (e.g. v1.0.0-rc1 < v1.0.0)
	if s.PreRelease != "" && other.PreRelease == "" {
		return true
	}
	if s.PreRelease == "" && other.PreRelease != "" {
		return false
	}
	return s.PreRelease < other.PreRelease
}

// SortVersionsDesc sorts a list of version strings in descending semver
// order. Non-parseable versions are placed at the end.
func SortVersionsDesc(versions []string) {
	sort.Slice(versions, func(i, j int) bool {
		vi, ei := ParseSemver(versions[i])
		vj, ej := ParseSemver(versions[j])
		if ei != nil && ej != nil {
			return versions[i] > versions[j]
		}
		if ei != nil {
			return false
		}
		if ej != nil {
			return true
		}
		return vj.Less(vi)
	})
}

// LatestVersion returns the highest semver tag from a list. If the list
// is empty an empty string is returned.
func LatestVersion(versions []string) string {
	if len(versions) == 0 {
		return ""
	}
	sorted := make([]string, len(versions))
	copy(sorted, versions)
	SortVersionsDesc(sorted)
	return sorted[0]
}

// BranchVersion generates a development version string for untagged
// branch content, e.g. "0.0.0-main".
func BranchVersion(branch string) string {
	if branch == "" {
		branch = "main"
	}
	return "0.0.0-" + branch
}
