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
	"testing"
)

func TestParseSemver(t *testing.T) {
	tests := []struct {
		input   string
		major   int
		minor   int
		patch   int
		pre     string
		wantErr bool
	}{
		{"v1.2.3", 1, 2, 3, "", false},
		{"1.0.0", 1, 0, 0, "", false},
		{"v0.1.0-rc1", 0, 1, 0, "rc1", false},
		{"v2.0.0-beta.1", 2, 0, 0, "beta.1", false},
		{"invalid", 0, 0, 0, "", true},
		{"v1.2", 0, 0, 0, "", true},
	}

	for _, tt := range tests {
		sv, err := ParseSemver(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseSemver(%q) expected error", tt.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSemver(%q) unexpected error: %v", tt.input, err)
			continue
		}
		if sv.Major != tt.major || sv.Minor != tt.minor || sv.Patch != tt.patch {
			t.Errorf("ParseSemver(%q) = %d.%d.%d, want %d.%d.%d",
				tt.input, sv.Major, sv.Minor, sv.Patch, tt.major, tt.minor, tt.patch)
		}
		if sv.PreRelease != tt.pre {
			t.Errorf("ParseSemver(%q) prerelease = %q, want %q", tt.input, sv.PreRelease, tt.pre)
		}
	}
}

func TestSortVersionsDesc(t *testing.T) {
	versions := []string{"v1.0.0", "v2.1.0", "v1.2.3", "v2.0.0", "v0.1.0-rc1", "v2.1.0-alpha"}
	SortVersionsDesc(versions)

	expected := []string{"v2.1.0", "v2.1.0-alpha", "v2.0.0", "v1.2.3", "v1.0.0", "v0.1.0-rc1"}
	for i, v := range versions {
		if v != expected[i] {
			t.Errorf("SortVersionsDesc[%d] = %q, want %q", i, v, expected[i])
		}
	}
}

func TestLatestVersion(t *testing.T) {
	tests := []struct {
		versions []string
		want     string
	}{
		{[]string{"v1.0.0", "v2.0.0", "v1.5.0"}, "v2.0.0"},
		{[]string{"v0.1.0-rc1", "v0.1.0"}, "v0.1.0"},
		{[]string{}, ""},
		{[]string{"v1.0.0"}, "v1.0.0"},
	}

	for _, tt := range tests {
		got := LatestVersion(tt.versions)
		if got != tt.want {
			t.Errorf("LatestVersion(%v) = %q, want %q", tt.versions, got, tt.want)
		}
	}
}

func TestBranchVersion(t *testing.T) {
	if got := BranchVersion("main"); got != "0.0.0-main" {
		t.Errorf("BranchVersion(main) = %q", got)
	}
	if got := BranchVersion(""); got != "0.0.0-main" {
		t.Errorf("BranchVersion('') = %q", got)
	}
	if got := BranchVersion("develop"); got != "0.0.0-develop" {
		t.Errorf("BranchVersion(develop) = %q", got)
	}
}

func TestSemverLess(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"v1.0.0", "v2.0.0", true},
		{"v2.0.0", "v1.0.0", false},
		{"v1.0.0-rc1", "v1.0.0", true},
		{"v1.0.0", "v1.0.0-rc1", false},
		{"v1.0.0", "v1.0.0", false},
	}

	for _, tt := range tests {
		a, _ := ParseSemver(tt.a)
		b, _ := ParseSemver(tt.b)
		if got := a.Less(b); got != tt.want {
			t.Errorf("%s.Less(%s) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
