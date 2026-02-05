// Copyright 2025 Arcentra Team
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

package duration

import (
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expected  time.Duration
		wantError bool
	}{
		// seconds
		{"1 second", "1s", time.Second, false},
		{"5 seconds", "5s", 5 * time.Second, false},
		{"0 seconds", "0s", 0, false},
		{"60 seconds", "60s", 60 * time.Second, false},

		// minutes
		{"1 minute", "1m", time.Minute, false},
		{"30 minutes", "30m", 30 * time.Minute, false},
		{"60 minutes", "60m", 60 * time.Minute, false},

		// hours
		{"1 hour", "1h", time.Hour, false},
		{"12 hours", "12h", 12 * time.Hour, false},
		{"24 hours", "24h", 24 * time.Hour, false},

		// days
		{"1 day", "1d", 24 * time.Hour, false},
		{"7 days", "7d", 7 * 24 * time.Hour, false},
		{"30 days", "30d", 30 * 24 * time.Hour, false},

		// weeks
		{"1 week", "1w", 7 * 24 * time.Hour, false},
		{"2 weeks", "2w", 2 * 7 * 24 * time.Hour, false},
		{"4 weeks", "4w", 4 * 7 * 24 * time.Hour, false},

		// months
		{"1 month", "1M", 30 * 24 * time.Hour, false},
		{"6 months", "6M", 6 * 30 * 24 * time.Hour, false},
		{"12 months", "12M", 12 * 30 * 24 * time.Hour, false},

		// years
		{"1 year", "1y", 365 * 24 * time.Hour, false},
		{"2 years", "2y", 2 * 365 * 24 * time.Hour, false},

		// error cases
		{"empty string", "", 0, true},
		{"invalid format", "abc", 0, true},
		{"no unit", "100", 0, true},
		{"no number", "s", 0, true},
		{"invalid unit", "1x", 0, true},
		{"negative number", "-1s", 0, true},
		{"float number", "1.5s", 0, true},
		{"space in string", "1 s", 0, true},
		{"multiple units", "1h2m", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Parse(tt.input)
			if tt.wantError {
				if err == nil {
					t.Errorf("Parse(%q) expected error but got none", tt.input)
				}
			} else {
				if err != nil {
					t.Errorf("Parse(%q) unexpected error: %v", tt.input, err)
				}
				if result != tt.expected {
					t.Errorf("Parse(%q) = %v, want %v", tt.input, result, tt.expected)
				}
			}
		})
	}
}

func TestMustParse(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expected  time.Duration
		wantPanic bool
	}{
		{"valid duration", "1h", time.Hour, false},
		{"valid duration 2", "30m", 30 * time.Minute, false},
		{"invalid format", "invalid", 0, true},
		{"empty string", "", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantPanic {
				defer func() {
					if r := recover(); r == nil {
						t.Errorf("MustParse(%q) expected to panic but didn't", tt.input)
					}
				}()
			}
			result := MustParse(tt.input)
			if !tt.wantPanic && result != tt.expected {
				t.Errorf("MustParse(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestParseSeconds(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expected  int64
		wantError bool
	}{
		{"1 second", "1s", 1, false},
		{"1 minute", "1m", 60, false},
		{"1 hour", "1h", 3600, false},
		{"1 day", "1d", 86400, false},
		{"1 week", "1w", 604800, false},
		{"invalid format", "invalid", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseSeconds(tt.input)
			if tt.wantError {
				if err == nil {
					t.Errorf("ParseSeconds(%q) expected error but got none", tt.input)
				}
			} else {
				if err != nil {
					t.Errorf("ParseSeconds(%q) unexpected error: %v", tt.input, err)
				}
				if result != tt.expected {
					t.Errorf("ParseSeconds(%q) = %d, want %d", tt.input, result, tt.expected)
				}
			}
		})
	}
}

func TestMustParseSeconds(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expected  int64
		wantPanic bool
	}{
		{"valid duration", "1h", 3600, false},
		{"valid duration 2", "30m", 1800, false},
		{"invalid format", "invalid", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantPanic {
				defer func() {
					if r := recover(); r == nil {
						t.Errorf("MustParseSeconds(%q) expected to panic but didn't", tt.input)
					}
				}()
			}
			result := MustParseSeconds(tt.input)
			if !tt.wantPanic && result != tt.expected {
				t.Errorf("MustParseSeconds(%q) = %d, want %d", tt.input, result, tt.expected)
			}
		})
	}
}

func TestParseEdgeCases(t *testing.T) {
	// test large values
	largeTests := []struct {
		name     string
		input    string
		expected time.Duration
	}{
		{"large seconds", "999999s", 999999 * time.Second},
		{"large days", "365d", 365 * 24 * time.Hour},
	}

	for _, tt := range largeTests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Parse(tt.input)
			if err != nil {
				t.Errorf("Parse(%q) unexpected error: %v", tt.input, err)
			}
			if result != tt.expected {
				t.Errorf("Parse(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func BenchmarkParse(b *testing.B) {
	testCases := []string{"1s", "1m", "1h", "1d", "1w", "1M", "1y"}

	for _, tc := range testCases {
		b.Run(tc, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, _ = Parse(tc)
			}
		})
	}
}
