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

package time

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

var (
	// durationRegex matches duration strings, e.g., "1s", "2h", "3d", "1w", "1M", "1y"
	durationRegex = regexp.MustCompile(`^(\d+)([smhdwMy])$`)

	// ErrInvalidFormat indicates an invalid duration format
	ErrInvalidFormat = errors.New("invalid duration format")
	// ErrInvalidUnit indicates an invalid duration unit
	ErrInvalidUnit = errors.New("invalid duration unit")
)

// Parse parses a duration string and returns time.Duration.
// Supported units:
//   - s: second
//   - m: minute
//   - h: hour
//   - d: day
//   - w: week
//   - M: month (30 days)
//   - y: year (365 days)
//
// Examples:
//   - "1s" -> 1 second
//   - "5m" -> 5 minutes
//   - "2h" -> 2 hours
//   - "3d" -> 3 days
//   - "1w" -> 1 week
//   - "1M" -> 1 month (30 days)
//   - "1y" -> 1 year (365 days)
func Parse(s string) (time.Duration, error) {
	if s == "" {
		return 0, ErrInvalidFormat
	}

	matches := durationRegex.FindStringSubmatch(s)
	if len(matches) != 3 {
		return 0, fmt.Errorf("%w: %s", ErrInvalidFormat, s)
	}

	value, err := strconv.ParseInt(matches[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %s", ErrInvalidFormat, s)
	}

	unit := matches[2]
	var duration time.Duration

	switch unit {
	case "s":
		duration = time.Duration(value) * time.Second
	case "m":
		duration = time.Duration(value) * time.Minute
	case "h":
		duration = time.Duration(value) * time.Hour
	case "d":
		duration = time.Duration(value) * 24 * time.Hour
	case "w":
		duration = time.Duration(value) * 7 * 24 * time.Hour
	case "M":
		duration = time.Duration(value) * 30 * 24 * time.Hour
	case "y":
		duration = time.Duration(value) * 365 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("%w: %s", ErrInvalidUnit, unit)
	}

	return duration, nil
}

// MustParse parses a duration string and panics if parsing fails
func MustParse(s string) time.Duration {
	d, err := Parse(s)
	if err != nil {
		panic(fmt.Sprintf("duration: parse error: %v", err))
	}
	return d
}

// ParseSeconds parses a duration string and returns the number of seconds
func ParseSeconds(s string) (int64, error) {
	d, err := Parse(s)
	if err != nil {
		return 0, err
	}
	return int64(d.Seconds()), nil
}

// MustParseSeconds parses a duration string and returns the number of seconds, panics if parsing fails
func MustParseSeconds(s string) int64 {
	return int64(MustParse(s).Seconds())
}
