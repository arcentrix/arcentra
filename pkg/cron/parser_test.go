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

package cron

import (
	"testing"
	"time"
)

func TestNewParser(t *testing.T) {
	tests := []struct {
		name    string
		options ParseOption
		want    int
	}{
		{
			name:    "standard options",
			options: Minute | Hour | Dom | Month | Dow,
			want:    0,
		},
		{
			name:    "with dow optional",
			options: Minute | Hour | Dom | Month | DowOptional,
			want:    1,
		},
		{
			name:    "with second",
			options: Second | Minute | Hour | Dom | Month | Dow,
			want:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewParser(tt.options)
			if got.optionals != tt.want {
				t.Errorf("NewParser(%v).optionals = %v, want %v", tt.options, got.optionals, tt.want)
			}
		})
	}
}

func TestParser_Parse(t *testing.T) {
	tests := []struct {
		name    string
		parser  Parser
		spec    string
		wantErr bool
	}{
		{
			name:    "standard 5 field",
			parser:  NewParser(Minute | Hour | Dom | Month | Dow),
			spec:    "0 0 * * *",
			wantErr: false,
		},
		{
			name:    "6 field with second",
			parser:  NewParser(Second | Minute | Hour | Dom | Month | Dow),
			spec:    "0 0 0 * * *",
			wantErr: false,
		},
		{
			name:    "empty spec",
			parser:  NewParser(Minute | Hour | Dom | Month | Dow),
			spec:    "",
			wantErr: true,
		},
		{
			name:    "too few fields",
			parser:  NewParser(Minute | Hour | Dom | Month | Dow),
			spec:    "0 0 *",
			wantErr: true,
		},
		{
			name:    "too many fields",
			parser:  NewParser(Minute | Hour | Dom | Month | Dow),
			spec:    "0 0 * * * *",
			wantErr: true,
		},
		{
			name:    "with dow optional",
			parser:  NewParser(Minute | Hour | Dom | Month | DowOptional),
			spec:    "0 0 * *",
			wantErr: false,
		},
		{
			name:    "descriptor",
			parser:  NewParser(Minute | Hour | Dom | Month | Dow | Descriptor),
			spec:    "@daily",
			wantErr: false,
		},
		{
			name:    "descriptor without option",
			parser:  NewParser(Minute | Hour | Dom | Month | Dow),
			spec:    "@daily",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.parser.Parse(tt.spec)
			if (err != nil) != tt.wantErr {
				t.Errorf("Parser.Parse(%q) error = %v, wantErr %v", tt.spec, err, tt.wantErr)
			}
		})
	}
}

func TestParseStandard(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		wantErr bool
	}{
		{
			name:    "valid standard spec",
			spec:    "0 0 * * *",
			wantErr: false,
		},
		{
			name:    "with question mark",
			spec:    "0 0 * * ?",
			wantErr: false,
		},
		{
			name:    "descriptor",
			spec:    "@daily",
			wantErr: false,
		},
		{
			name:    "invalid spec",
			spec:    "invalid",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseStandard(tt.spec)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseStandard(%q) error = %v, wantErr %v", tt.spec, err, tt.wantErr)
			}
		})
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		wantErr bool
	}{
		{
			name:    "valid 6 field spec",
			spec:    "0 0 0 * * *",
			wantErr: false,
		},
		{
			name:    "valid 5 field spec",
			spec:    "0 0 * * *",
			wantErr: false,
		},
		{
			name:    "descriptor",
			spec:    "@every 1h",
			wantErr: false,
		},
		{
			name:    "invalid spec",
			spec:    "invalid",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.spec)
			if (err != nil) != tt.wantErr {
				t.Errorf("Parse(%q) error = %v, wantErr %v", tt.spec, err, tt.wantErr)
			}
		})
	}
}

func TestGetField(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		bounds  bounds
		wantErr bool
	}{
		{
			name:    "single number",
			field:   "5",
			bounds:  minutes,
			wantErr: false,
		},
		{
			name:    "range",
			field:   "5-10",
			bounds:  minutes,
			wantErr: false,
		},
		{
			name:    "star",
			field:   "*",
			bounds:  minutes,
			wantErr: false,
		},
		{
			name:    "question mark",
			field:   "?",
			bounds:  dom,
			wantErr: false,
		},
		{
			name:    "comma separated",
			field:   "1,5,10",
			bounds:  minutes,
			wantErr: false,
		},
		{
			name:    "with step",
			field:   "*/5",
			bounds:  minutes,
			wantErr: false,
		},
		{
			name:    "range with step",
			field:   "5-10/2",
			bounds:  minutes,
			wantErr: false,
		},
		{
			name:    "invalid number",
			field:   "abc",
			bounds:  minutes,
			wantErr: true,
		},
		{
			name:    "out of range",
			field:   "100",
			bounds:  minutes,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := getField(tt.field, tt.bounds)
			if (err != nil) != tt.wantErr {
				t.Errorf("getField(%q, %v) error = %v, wantErr %v", tt.field, tt.bounds, err, tt.wantErr)
			}
		})
	}
}

func TestGetRange(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		bounds  bounds
		wantErr bool
	}{
		{
			name:    "single number",
			expr:    "5",
			bounds:  minutes,
			wantErr: false,
		},
		{
			name:    "range",
			expr:    "5-10",
			bounds:  minutes,
			wantErr: false,
		},
		{
			name:    "star",
			expr:    "*",
			bounds:  minutes,
			wantErr: false,
		},
		{
			name:    "star with step",
			expr:    "*/5",
			bounds:  minutes,
			wantErr: false,
		},
		{
			name:    "number with step",
			expr:    "5/10",
			bounds:  minutes,
			wantErr: false,
		},
		{
			name:    "range with step",
			expr:    "5-10/2",
			bounds:  minutes,
			wantErr: false,
		},
		{
			name:    "too many hyphens",
			expr:    "5-10-15",
			bounds:  minutes,
			wantErr: true,
		},
		{
			name:    "too many slashes",
			expr:    "5/10/15",
			bounds:  minutes,
			wantErr: true,
		},
		{
			name:    "invalid start",
			expr:    "abc-10",
			bounds:  minutes,
			wantErr: true,
		},
		{
			name:    "start below minimum",
			expr:    "-1",
			bounds:  minutes,
			wantErr: true,
		},
		{
			name:    "end above maximum",
			expr:    "100",
			bounds:  minutes,
			wantErr: true,
		},
		{
			name:    "start after end",
			expr:    "10-5",
			bounds:  minutes,
			wantErr: true,
		},
		{
			name:    "zero step",
			expr:    "5/0",
			bounds:  minutes,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := getRange(tt.expr, tt.bounds)
			if (err != nil) != tt.wantErr {
				t.Errorf("getRange(%q, %v) error = %v, wantErr %v", tt.expr, tt.bounds, err, tt.wantErr)
			}
		})
	}
}

func TestParseIntOrName(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		names   map[string]uint
		want    uint
		wantErr bool
	}{
		{
			name:    "numeric",
			expr:    "5",
			names:   nil,
			want:    5,
			wantErr: false,
		},
		{
			name:    "month name",
			expr:    "jan",
			names:   months.names,
			want:    1,
			wantErr: false,
		},
		{
			name:    "month name uppercase",
			expr:    "JAN",
			names:   months.names,
			want:    1,
			wantErr: false,
		},
		{
			name:    "dow name",
			expr:    "mon",
			names:   dow.names,
			want:    1,
			wantErr: false,
		},
		{
			name:    "invalid name",
			expr:    "invalid",
			names:   months.names,
			want:    0,
			wantErr: true,
		},
		{
			name:    "invalid number",
			expr:    "abc",
			names:   nil,
			want:    0,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseIntOrName(tt.expr, tt.names)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseIntOrName(%q, %v) error = %v, wantErr %v", tt.expr, tt.names, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("parseIntOrName(%q, %v) = %v, want %v", tt.expr, tt.names, got, tt.want)
			}
		})
	}
}

func TestMustParseInt(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		want    uint
		wantErr bool
	}{
		{
			name:    "valid number",
			expr:    "5",
			want:    5,
			wantErr: false,
		},
		{
			name:    "zero",
			expr:    "0",
			want:    0,
			wantErr: false,
		},
		{
			name:    "large number",
			expr:    "59",
			want:    59,
			wantErr: false,
		},
		{
			name:    "invalid string",
			expr:    "abc",
			want:    0,
			wantErr: true,
		},
		{
			name:    "negative number",
			expr:    "-5",
			want:    0,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mustParseInt(tt.expr)
			if (err != nil) != tt.wantErr {
				t.Errorf("mustParseInt(%q) error = %v, wantErr %v", tt.expr, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("mustParseInt(%q) = %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

func TestGetBits(t *testing.T) {
	tests := []struct {
		name     string
		min, max uint
		step     uint
		want     uint64
	}{
		{
			name: "single bit",
			min:  5,
			max:  5,
			step: 1,
			want: 1 << 5,
		},
		{
			name: "range step 1",
			min:  5,
			max:  10,
			step: 1,
			want: (1 << 5) | (1 << 6) | (1 << 7) | (1 << 8) | (1 << 9) | (1 << 10),
		},
		{
			name: "range step 2",
			min:  0,
			max:  10,
			step: 2,
			want: (1 << 0) | (1 << 2) | (1 << 4) | (1 << 6) | (1 << 8) | (1 << 10),
		},
		{
			name: "full range",
			min:  0,
			max:  59,
			step: 1,
			want: (1 << 60) - 1, // All bits 0-59 set (2^60 - 1)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getBits(tt.min, tt.max, tt.step)
			if got != tt.want {
				t.Errorf("getBits(%d, %d, %d) = %b, want %b", tt.min, tt.max, tt.step, got, tt.want)
			}
		})
	}
}

func TestAll(t *testing.T) {
	tests := []struct {
		name   string
		bounds bounds
	}{
		{
			name:   "seconds",
			bounds: seconds,
		},
		{
			name:   "minutes",
			bounds: minutes,
		},
		{
			name:   "hours",
			bounds: hours,
		},
		{
			name:   "dom",
			bounds: dom,
		},
		{
			name:   "months",
			bounds: months,
		},
		{
			name:   "dow",
			bounds: dow,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := all(tt.bounds)
			// Should have starBit set
			if got&starBit == 0 {
				t.Errorf("all(%v) should have starBit set", tt.bounds)
			}
			// Should have all bits in range set
			expectedBits := getBits(tt.bounds.min, tt.bounds.max, 1)
			if got&^starBit != expectedBits {
				t.Errorf("all(%v) bits = %b, want %b", tt.bounds, got&^starBit, expectedBits)
			}
		})
	}
}

func TestParseDescriptor(t *testing.T) {
	tests := []struct {
		name    string
		desc    string
		wantErr bool
	}{
		{
			name:    "@yearly",
			desc:    "@yearly",
			wantErr: false,
		},
		{
			name:    "@annually",
			desc:    "@annually",
			wantErr: false,
		},
		{
			name:    "@monthly",
			desc:    "@monthly",
			wantErr: false,
		},
		{
			name:    "@weekly",
			desc:    "@weekly",
			wantErr: false,
		},
		{
			name:    "@daily",
			desc:    "@daily",
			wantErr: false,
		},
		{
			name:    "@midnight",
			desc:    "@midnight",
			wantErr: false,
		},
		{
			name:    "@hourly",
			desc:    "@hourly",
			wantErr: false,
		},
		{
			name:    "@every 1h",
			desc:    "@every 1h",
			wantErr: false,
		},
		{
			name:    "@every 30m",
			desc:    "@every 30m",
			wantErr: false,
		},
		{
			name:    "@every 1h30m",
			desc:    "@every 1h30m",
			wantErr: false,
		},
		{
			name:    "invalid descriptor",
			desc:    "@invalid",
			wantErr: true,
		},
		{
			name:    "invalid every",
			desc:    "@every invalid",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseDescriptor(tt.desc)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseDescriptor(%q) error = %v, wantErr %v", tt.desc, err, tt.wantErr)
			}
		})
	}
}

func TestExpandFields(t *testing.T) {
	tests := []struct {
		name    string
		fields  []string
		options ParseOption
		want    []string
	}{
		{
			name:    "standard 5 fields",
			fields:  []string{"0", "0", "*", "*", "*"},
			options: Minute | Hour | Dom | Month | Dow,
			want:    []string{"0", "0", "0", "*", "*", "*"},
		},
		{
			name:    "with second",
			fields:  []string{"0", "0", "0", "*", "*", "*"},
			options: Second | Minute | Hour | Dom | Month | Dow,
			want:    []string{"0", "0", "0", "*", "*", "*"},
		},
		{
			name:    "partial fields",
			fields:  []string{"0", "0"},
			options: Minute | Hour | Dom | Month | Dow,
			want:    []string{"0", "0", "0", "*", "*", "*"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandFields(tt.fields, tt.options)
			if len(got) != len(tt.want) {
				t.Errorf("expandFields(%v, %v) length = %d, want %d", tt.fields, tt.options, len(got), len(tt.want))
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("expandFields(%v, %v)[%d] = %q, want %q", tt.fields, tt.options, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseDescriptor_Schedules(t *testing.T) {
	now := time.Date(2023, 6, 15, 12, 30, 45, 0, time.UTC)

	tests := []struct {
		name     string
		desc     string
		validate func(t *testing.T, s Schedule)
	}{
		{
			name: "@yearly",
			desc: "@yearly",
			validate: func(t *testing.T, s Schedule) {
				next := s.Next(now)
				// Should be next year, Jan 1, 00:00:00
				expected := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
				if !next.Equal(expected) {
					t.Errorf("@yearly Next() = %v, want %v", next, expected)
				}
			},
		},
		{
			name: "@monthly",
			desc: "@monthly",
			validate: func(t *testing.T, s Schedule) {
				next := s.Next(now)
				// Should be next month, day 1, 00:00:00
				expected := time.Date(2023, 7, 1, 0, 0, 0, 0, time.UTC)
				if !next.Equal(expected) {
					t.Errorf("@monthly Next() = %v, want %v", next, expected)
				}
			},
		},
		{
			name: "@weekly",
			desc: "@weekly",
			validate: func(t *testing.T, s Schedule) {
				next := s.Next(now)
				// Should be next Sunday, 00:00:00
				expected := time.Date(2023, 6, 18, 0, 0, 0, 0, time.UTC) // Next Sunday
				if !next.Equal(expected) {
					t.Errorf("@weekly Next() = %v, want %v", next, expected)
				}
			},
		},
		{
			name: "@daily",
			desc: "@daily",
			validate: func(t *testing.T, s Schedule) {
				next := s.Next(now)
				// Should be next day, 00:00:00
				expected := time.Date(2023, 6, 16, 0, 0, 0, 0, time.UTC)
				if !next.Equal(expected) {
					t.Errorf("@daily Next() = %v, want %v", next, expected)
				}
			},
		},
		{
			name: "@hourly",
			desc: "@hourly",
			validate: func(t *testing.T, s Schedule) {
				next := s.Next(now)
				// Should be next hour, minute 0, second 0
				expected := time.Date(2023, 6, 15, 13, 0, 0, 0, time.UTC)
				if !next.Equal(expected) {
					t.Errorf("@hourly Next() = %v, want %v", next, expected)
				}
			},
		},
		{
			name: "@every 1h",
			desc: "@every 1h",
			validate: func(t *testing.T, s Schedule) {
				next := s.Next(now)
				// Should be 1 hour from now, rounded to second
				expected := time.Date(2023, 6, 15, 13, 30, 45, 0, time.UTC)
				if !next.Equal(expected) {
					t.Errorf("@every 1h Next() = %v, want %v", next, expected)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schedule, err := parseDescriptor(tt.desc)
			if err != nil {
				t.Fatalf("parseDescriptor(%q) error = %v", tt.desc, err)
			}
			tt.validate(t, schedule)
		})
	}
}
