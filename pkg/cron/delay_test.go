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

package cron

import (
	"testing"
	"time"
)

func TestEvery(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     time.Duration
	}{
		{
			name:     "one second",
			duration: time.Second,
			want:     time.Second,
		},
		{
			name:     "five seconds",
			duration: 5 * time.Second,
			want:     5 * time.Second,
		},
		{
			name:     "one minute",
			duration: time.Minute,
			want:     time.Minute,
		},
		{
			name:     "one hour",
			duration: time.Hour,
			want:     time.Hour,
		},
		{
			name:     "less than one second rounds up",
			duration: 500 * time.Millisecond,
			want:     time.Second,
		},
		{
			name:     "zero duration rounds up",
			duration: 0,
			want:     time.Second,
		},
		{
			name:     "duration with nanoseconds truncated",
			duration: 5*time.Second + 123*time.Nanosecond,
			want:     5 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Every(tt.duration)
			if got.Delay != tt.want {
				t.Errorf("Every(%v) = %v, want %v", tt.duration, got.Delay, tt.want)
			}
		})
	}
}

func TestConstantDelaySchedule_Next(t *testing.T) {
	tests := []struct {
		name     string
		schedule ConstantDelaySchedule
		now      time.Time
		want     time.Time
	}{
		{
			name:     "one second delay",
			schedule: ConstantDelaySchedule{Delay: time.Second},
			now:      time.Date(2023, 1, 1, 12, 0, 0, 500000000, time.UTC),
			want:     time.Date(2023, 1, 1, 12, 0, 1, 0, time.UTC),
		},
		{
			name:     "five second delay",
			schedule: ConstantDelaySchedule{Delay: 5 * time.Second},
			now:      time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC),
			want:     time.Date(2023, 1, 1, 12, 0, 5, 0, time.UTC),
		},
		{
			name:     "one minute delay",
			schedule: ConstantDelaySchedule{Delay: time.Minute},
			now:      time.Date(2023, 1, 1, 12, 0, 30, 123456789, time.UTC),
			want:     time.Date(2023, 1, 1, 12, 1, 30, 0, time.UTC),
		},
		{
			name:     "rounds to second",
			schedule: ConstantDelaySchedule{Delay: time.Second},
			now:      time.Date(2023, 1, 1, 12, 0, 0, 999999999, time.UTC),
			want:     time.Date(2023, 1, 1, 12, 0, 1, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.schedule.Next(tt.now)
			if !got.Equal(tt.want) {
				t.Errorf("ConstantDelaySchedule.Next(%v) = %v, want %v", tt.now, got, tt.want)
			}
			// Verify that nanoseconds are zero
			if got.Nanosecond() != 0 {
				t.Errorf("ConstantDelaySchedule.Next(%v) has nanoseconds %d, want 0", tt.now, got.Nanosecond())
			}
		})
	}
}

func TestConstantDelaySchedule_Next_MultipleCalls(t *testing.T) {
	schedule := ConstantDelaySchedule{Delay: 2 * time.Second}
	now := time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC)

	next1 := schedule.Next(now)
	if !next1.Equal(time.Date(2023, 1, 1, 12, 0, 2, 0, time.UTC)) {
		t.Errorf("First Next() = %v, want %v", next1, time.Date(2023, 1, 1, 12, 0, 2, 0, time.UTC))
	}

	next2 := schedule.Next(next1)
	if !next2.Equal(time.Date(2023, 1, 1, 12, 0, 4, 0, time.UTC)) {
		t.Errorf("Second Next() = %v, want %v", next2, time.Date(2023, 1, 1, 12, 0, 4, 0, time.UTC))
	}
}
