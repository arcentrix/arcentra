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

package version

import (
	"encoding/json"
	"fmt"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

var (
	Version   = ""
	GitBranch = ""
	GitCommit = ""
	BuildTime = ""
	GoVersion = ""
	Compiler  = ""
	Platform  = ""
)

var VersionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the application version information",
	Run: func(cmd *cobra.Command, args []string) {
		v := GetVersion()
		fmt.Println(string(v.Json()))
	},
}

// VersionInfo represents a version in YY.Major.Minor.Patch format (YY is 2-digit year)
type VersionInfo struct {
	Year  int `json:"year"` // 2-digit year (e.g., 25 for 2025)
	Major int `json:"major"`
	Minor int `json:"minor"`
	Patch int `json:"patch"`
}

// String returns the version string in YY.Major.Minor.Patch format
func (v *VersionInfo) String() string {
	return fmt.Sprintf("%02d.%d.%d.%d", v.Year, v.Major, v.Minor, v.Patch)
}

// ParseVersion parses a version string in YY.Major.Minor.Patch format (YY is 2-digit year)
// Returns error if the format is invalid
func ParseVersion(versionStr string) (*VersionInfo, error) {
	if versionStr == "" {
		return nil, fmt.Errorf("version string is empty")
	}

	// Remove 'v' prefix if present
	versionStr = strings.TrimPrefix(versionStr, "v")
	versionStr = strings.TrimSpace(versionStr)

	// Match YY.Major.Minor.Patch format
	// Year: 2 digits (e.g., 25 for 2025)
	// Major, Minor, Patch: non-negative integers
	versionRegex := regexp.MustCompile(`^(\d{2})\.(\d+)\.(\d+)\.(\d+)$`)
	matches := versionRegex.FindStringSubmatch(versionStr)
	if len(matches) != 5 {
		return nil, fmt.Errorf("invalid version format: %s, expected YY.Major.Minor.Patch (e.g., 25.1.2.3)", versionStr)
	}

	year, err := strconv.Atoi(matches[1])
	if err != nil {
		return nil, fmt.Errorf("invalid year: %s", matches[1])
	}

	major, err := strconv.Atoi(matches[2])
	if err != nil {
		return nil, fmt.Errorf("invalid major version: %s", matches[2])
	}

	minor, err := strconv.Atoi(matches[3])
	if err != nil {
		return nil, fmt.Errorf("invalid minor version: %s", matches[3])
	}

	patch, err := strconv.Atoi(matches[4])
	if err != nil {
		return nil, fmt.Errorf("invalid patch version: %s", matches[4])
	}

	// Validate year (should be reasonable, e.g., 20-99 for years 2020-2099)
	if year < 20 || year > 99 {
		return nil, fmt.Errorf("year must be between 20 and 99 (representing 2020-2099), got: %d", year)
	}

	return &VersionInfo{
		Year:  year,
		Major: major,
		Minor: minor,
		Patch: patch,
	}, nil
}

// ValidateVersion validates if a version string follows YY.Major.Minor.Patch format (YY is 2-digit year)
func ValidateVersion(versionStr string) error {
	_, err := ParseVersion(versionStr)
	return err
}

// Compare compares two versions
// Returns: -1 if v < other, 0 if v == other, 1 if v > other
func (v *VersionInfo) Compare(other *VersionInfo) int {
	if v.Year != other.Year {
		if v.Year < other.Year {
			return -1
		}
		return 1
	}
	if v.Major != other.Major {
		if v.Major < other.Major {
			return -1
		}
		return 1
	}
	if v.Minor != other.Minor {
		if v.Minor < other.Minor {
			return -1
		}
		return 1
	}
	if v.Patch != other.Patch {
		if v.Patch < other.Patch {
			return -1
		}
		return 1
	}
	return 0
}

type Info struct {
	Version   string `json:"version"`
	GitBranch string `json:"gitBranch"`
	GitCommit string `json:"gitCommit"`
	BuildTime string `json:"buildTime"`
	GoVersion string `json:"goVersion"`
	Compiler  string `json:"compiler"`
	Platform  string `json:"platform"`
}

func GetVersion() *Info {
	return &Info{
		Version:   Version,
		GitBranch: GitBranch,
		GitCommit: GitCommit,
		BuildTime: BuildTime,
		GoVersion: runtime.Version(),
		Compiler:  runtime.Compiler,
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
}

// GetParsedVersion returns the parsed version object
// Returns nil if version string is empty or invalid
func GetParsedVersion() (*VersionInfo, error) {
	if Version == "" {
		return nil, fmt.Errorf("version is not set")
	}
	return ParseVersion(Version)
}

func (v *Info) Json() json.RawMessage {
	j, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Println(err)
		return nil
	}
	return j
}
