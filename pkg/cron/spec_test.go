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

func TestSpecSchedule_Next(t *testing.T) {
	tests := []struct {
		name     string
		schedule *SpecSchedule
		now      time.Time
		want     time.Time
	}{
		{
			name: "every minute",
			schedule: &SpecSchedule{
				Second: all(seconds),
				Minute: all(minutes),
				Hour:   all(hours),
				Dom:    all(dom),
				Month:  all(months),
				Dow:    all(dow),
			},
			now:  time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC),
			want: time.Date(2023, 1, 1, 12, 0, 1, 0, time.UTC),
		},
		{
			name: "every hour at minute 0",
			schedule: &SpecSchedule{
				Second: 1 << 0,
				Minute: 1 << 0,
				Hour:   all(hours),
				Dom:    all(dom),
				Month:  all(months),
				Dow:    all(dow),
			},
			now:  time.Date(2023, 1, 1, 12, 30, 0, 0, time.UTC),
			want: time.Date(2023, 1, 1, 13, 0, 0, 0, time.UTC),
		},
		{
			name: "specific minute",
			schedule: &SpecSchedule{
				Second: 1 << 0,
				Minute: 1 << 15, // minute 15
				Hour:   all(hours),
				Dom:    all(dom),
				Month:  all(months),
				Dow:    all(dow),
			},
			now:  time.Date(2023, 1, 1, 12, 10, 0, 0, time.UTC),
			want: time.Date(2023, 1, 1, 12, 15, 0, 0, time.UTC),
		},
		{
			name: "specific hour and minute",
			schedule: &SpecSchedule{
				Second: 1 << 0,
				Minute: 1 << 30,
				Hour:   1 << 14, // hour 14
				Dom:    all(dom),
				Month:  all(months),
				Dow:    all(dow),
			},
			now:  time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC),
			want: time.Date(2023, 1, 1, 14, 30, 0, 0, time.UTC),
		},
		{
			name: "next day",
			schedule: &SpecSchedule{
				Second: 1 << 0,
				Minute: 1 << 0,
				Hour:   1 << 0, // hour 0
				Dom:    all(dom),
				Month:  all(months),
				Dow:    all(dow),
			},
			now:  time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC),
			want: time.Date(2023, 1, 2, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "specific day of month",
			schedule: &SpecSchedule{
				Second: 1 << 0,
				Minute: 1 << 0,
				Hour:   1 << 0,
				Dom:    1 << 15, // day 15
				Month:  all(months),
				Dow:    all(dow),
			},
			now:  time.Date(2023, 1, 10, 0, 0, 0, 0, time.UTC),
			want: time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "specific month",
			schedule: &SpecSchedule{
				Second: 1 << 0,
				Minute: 1 << 0,
				Hour:   1 << 0,
				Dom:    1 << 1,
				Month:  1 << 6, // June
				Dow:    all(dow),
			},
			now:  time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC),
			want: time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.schedule.Next(tt.now)
			if !got.Equal(tt.want) {
				t.Errorf("SpecSchedule.Next(%v) = %v, want %v", tt.now, got, tt.want)
			}
		})
	}
}

func TestSpecSchedule_Next_WithNanoseconds(t *testing.T) {
	schedule := &SpecSchedule{
		Second: all(seconds),
		Minute: all(minutes),
		Hour:   all(hours),
		Dom:    all(dom),
		Month:  all(months),
		Dow:    all(dow),
	}

	now := time.Date(2023, 1, 1, 12, 0, 0, 500000000, time.UTC)
	next := schedule.Next(now)

	// Should round up to the next second
	expected := time.Date(2023, 1, 1, 12, 0, 1, 0, time.UTC)
	if !next.Equal(expected) {
		t.Errorf("SpecSchedule.Next(%v) = %v, want %v", now, next, expected)
	}
}

func TestSpecSchedule_Next_YearLimit(t *testing.T) {
	// Create a schedule that will never match (impossible combination)
	schedule := &SpecSchedule{
		Second: 0, // No seconds match
		Minute: all(minutes),
		Hour:   all(hours),
		Dom:    all(dom),
		Month:  all(months),
		Dow:    all(dow),
	}

	now := time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC)
	next := schedule.Next(now)

	// Should return zero time when no match found within 5 years
	if !next.IsZero() {
		t.Errorf("SpecSchedule.Next(%v) = %v, want zero time", now, next)
	}
}

func TestDayMatches(t *testing.T) {
	tests := []struct {
		name     string
		schedule *SpecSchedule
		time     time.Time
		want     bool
	}{
		{
			name: "dom match with star",
			schedule: &SpecSchedule{
				Dom: all(dom) | starBit,
				Dow: all(dow) | starBit,
			},
			time: time.Date(2023, 1, 15, 12, 0, 0, 0, time.UTC),
			want: true,
		},
		{
			name: "dow match with star",
			schedule: &SpecSchedule{
				Dom: all(dom) | starBit,
				Dow: all(dow) | starBit,
			},
			time: time.Date(2023, 1, 15, 12, 0, 0, 0, time.UTC), // Sunday
			want: true,
		},
		{
			name: "specific dom match",
			schedule: &SpecSchedule{
				Dom: 1 << 15, // day 15
				Dow: 0,       // no dow specified
			},
			time: time.Date(2023, 1, 15, 12, 0, 0, 0, time.UTC),
			want: true,
		},
		{
			name: "specific dow match",
			schedule: &SpecSchedule{
				Dom: 0,      // no dom specified
				Dow: 1 << 0, // Sunday
			},
			time: time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC), // Sunday
			want: true,
		},
		{
			name: "no match",
			schedule: &SpecSchedule{
				Dom: 1 << 20, // day 20
				Dow: 1 << 1,  // Monday
			},
			time: time.Date(2023, 1, 15, 12, 0, 0, 0, time.UTC), // Sunday, day 15
			want: false,
		},
		{
			name: "either dom or dow match",
			schedule: &SpecSchedule{
				Dom: 1 << 15, // day 15
				Dow: 1 << 1,  // Monday
			},
			time: time.Date(2023, 1, 15, 12, 0, 0, 0, time.UTC), // Sunday, day 15
			want: true,                                          // dom matches
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dayMatches(tt.schedule, tt.time)
			if got != tt.want {
				t.Errorf("dayMatches(%v, %v) = %v, want %v", tt.schedule, tt.time, got, tt.want)
			}
		})
	}
}

func TestSpecSchedule_Next_MultipleIterations(t *testing.T) {
	schedule := &SpecSchedule{
		Second: 1 << 0,
		Minute: 1 << 0, // minute 0
		Hour:   all(hours),
		Dom:    all(dom),
		Month:  all(months),
		Dow:    all(dow),
	}

	now := time.Date(2023, 1, 1, 12, 30, 0, 0, time.UTC)

	// First iteration: should be next hour
	next1 := schedule.Next(now)
	expected1 := time.Date(2023, 1, 1, 13, 0, 0, 0, time.UTC)
	if !next1.Equal(expected1) {
		t.Errorf("First Next() = %v, want %v", next1, expected1)
	}

	// Second iteration: should be hour after that
	next2 := schedule.Next(next1)
	expected2 := time.Date(2023, 1, 1, 14, 0, 0, 0, time.UTC)
	if !next2.Equal(expected2) {
		t.Errorf("Second Next() = %v, want %v", next2, expected2)
	}
}
