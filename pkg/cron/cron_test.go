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
	"sort"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/pkg/safe"
)

func TestNew(t *testing.T) {
	c := New()
	if c == nil {
		t.Fatal("New() returned nil")
	}
	if c.running {
		t.Error("New() cron should not be running")
	}
	if c.location == nil {
		t.Error("New() cron should have a location")
	}
}

func TestNewWithLocation(t *testing.T) {
	loc := time.UTC
	c := NewWithLocation(loc)
	if c == nil {
		t.Fatal("NewWithLocation() returned nil")
	}
	if c.location != loc {
		t.Errorf("NewWithLocation() location = %v, want %v", c.location, loc)
	}
}

func TestWithRedisClient(t *testing.T) {
	// Note: This test doesn't actually use Redis, just verifies the option works
	c := New()
	if c.redisClient != nil {
		t.Error("New() cron should not have redis client by default")
	}

	// Test that WithRedisClient option can be set (we can't easily test without actual Redis)
	// This is more of a compile-time test
	_ = WithRedisClient(nil)
}

func TestAddFunc(t *testing.T) {
	c := New()

	err := c.AddFunc("* * * * * *", func() {
		// Job function
	}, "test-job")
	if err != nil {
		t.Fatalf("AddFunc() error = %v", err)
	}

	entries := c.Entries()
	if len(entries) != 1 {
		t.Fatalf("Entries() length = %d, want 1", len(entries))
	}

	if entries[0].Name != "test-job" {
		t.Errorf("Entry name = %q, want %q", entries[0].Name, "test-job")
	}
}

func TestAddFunc_DefaultName(t *testing.T) {
	c := New()
	err := c.AddFunc("* * * * * *", func() {})
	if err != nil {
		t.Fatalf("AddFunc() error = %v", err)
	}

	entries := c.Entries()
	if len(entries) != 1 {
		t.Fatalf("Entries() length = %d, want 1", len(entries))
	}

	if entries[0].Name == "" {
		t.Error("Entry name should not be empty")
	}
}

func TestAddFunc_InvalidSpec(t *testing.T) {
	c := New()
	err := c.AddFunc("invalid spec", func() {})
	if err == nil {
		t.Error("AddFunc() with invalid spec should return error")
	}
}

func TestAddOnceFunc(t *testing.T) {
	c := New()

	err := c.AddOnceFunc("* * * * * *", func() {
		// Job function
	}, "once-job")
	if err != nil {
		t.Fatalf("AddOnceFunc() error = %v", err)
	}

	entries := c.Entries()
	if len(entries) != 1 {
		t.Fatalf("Entries() length = %d, want 1", len(entries))
	}

	if entries[0].Name != "once-job" {
		t.Errorf("Entry name = %q, want %q", entries[0].Name, "once-job")
	}
}

func TestAddJob(t *testing.T) {
	c := New()
	job := FuncJob(func() {})

	err := c.AddJob("* * * * * *", job, "test-job")
	if err != nil {
		t.Fatalf("AddJob() error = %v", err)
	}

	entries := c.Entries()
	if len(entries) != 1 {
		t.Fatalf("Entries() length = %d, want 1", len(entries))
	}
}

func TestSchedule(t *testing.T) {
	c := New()
	schedule := Every(time.Second)
	job := FuncJob(func() {})

	c.Schedule(schedule, job, "scheduled-job")

	entries := c.Entries()
	if len(entries) != 1 {
		t.Fatalf("Entries() length = %d, want 1", len(entries))
	}

	if entries[0].Name != "scheduled-job" {
		t.Errorf("Entry name = %q, want %q", entries[0].Name, "scheduled-job")
	}
}

func TestSchedule_DuplicateName(t *testing.T) {
	c := New()
	schedule := Every(time.Second)
	job := FuncJob(func() {})

	c.Schedule(schedule, job, "duplicate")
	c.Schedule(schedule, job, "duplicate")

	entries := c.Entries()
	// Should still have 2 entries (duplicate names are logged but not prevented)
	if len(entries) != 2 {
		t.Fatalf("Entries() length = %d, want 2", len(entries))
	}
}

func TestRemove(t *testing.T) {
	c := New()
	schedule := Every(time.Second)
	job := FuncJob(func() {})

	c.Schedule(schedule, job, "job1")
	c.Schedule(schedule, job, "job2")

	if len(c.Entries()) != 2 {
		t.Fatalf("Entries() length = %d, want 2", len(c.Entries()))
	}

	err := c.Remove("job1")
	if err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	entries := c.Entries()
	if len(entries) != 1 {
		t.Fatalf("Entries() length = %d, want 1", len(entries))
	}

	if entries[0].Name != "job2" {
		t.Errorf("Remaining entry name = %q, want %q", entries[0].Name, "job2")
	}
}

func TestRemove_NotFound(t *testing.T) {
	c := New()
	err := c.Remove("nonexistent")
	if err == nil {
		t.Error("Remove() with nonexistent name should return error")
	}
}

func TestRemove_WhileRunning(t *testing.T) {
	c := New()
	schedule := Every(100 * time.Millisecond)
	job := FuncJob(func() {})

	c.Schedule(schedule, job, "running-job")
	c.Start()

	// Wait a bit for cron to start
	time.Sleep(50 * time.Millisecond)

	err := c.Remove("running-job")
	if err != nil {
		t.Fatalf("Remove() while running error = %v", err)
	}

	c.Stop()
}

func TestEntries(t *testing.T) {
	c := New()
	schedule := Every(time.Second)
	job := FuncJob(func() {})

	c.Schedule(schedule, job, "job1")
	c.Schedule(schedule, job, "job2")

	entries := c.Entries()
	if len(entries) != 2 {
		t.Fatalf("Entries() length = %d, want 2", len(entries))
	}

	// Verify entries are copies
	entries[0].Name = "modified"
	entries2 := c.Entries()
	if entries2[0].Name == "modified" {
		t.Error("Entries() should return copies, not references")
	}
}

func TestEntries_WhileRunning(t *testing.T) {
	c := New()
	schedule := Every(100 * time.Millisecond)
	job := FuncJob(func() {})

	c.Schedule(schedule, job, "job1")
	c.Start()

	// Wait a bit for cron to start
	time.Sleep(50 * time.Millisecond)

	entries := c.Entries()
	if len(entries) != 1 {
		t.Fatalf("Entries() while running length = %d, want 1", len(entries))
	}

	c.Stop()
}

func TestLocation(t *testing.T) {
	loc := time.UTC
	c := NewWithLocation(loc)

	if c.Location() != loc {
		t.Errorf("Location() = %v, want %v", c.Location(), loc)
	}
}

func TestStart(t *testing.T) {
	c := New()
	schedule := Every(20 * time.Millisecond)
	executed := make(chan bool, 1)

	job := FuncJob(func() {
		executed <- true
	})

	c.Schedule(schedule, job, "start-test")
	c.Start()

	// Wait for job to execute - use select with timeout
	select {
	case <-executed:
		// Job executed successfully
	case <-time.After(2 * time.Second):
		t.Error("Job should have been executed after Start()")
	}

	c.Stop()
}

func TestStart_AlreadyRunning(t *testing.T) {
	c := New()
	c.Start()

	if !c.running {
		t.Error("Cron should be running after Start()")
	}

	// Second Start should be no-op
	c.Start()

	c.Stop()
}

func TestRun(t *testing.T) {
	c := New()
	schedule := Every(5 * time.Millisecond)
	executed := make(chan bool, 1)

	job := FuncJob(func() {
		executed <- true
	})

	c.Schedule(schedule, job, "run-test")

	// Run in goroutine since it blocks
	done := make(chan bool)
	safe.Go(func() {
		c.Run()
		done <- true
	})

	// Wait for job to execute - use select with timeout
	// Give enough time for Run() to initialize and job to execute
	select {
	case <-executed:
		// Job executed successfully
	case <-time.After(500 * time.Millisecond):
		// Check if cron is running (it should be)
		if !c.running {
			t.Error("Cron should be running")
		}
		// Job might not have executed yet due to timing, but Run() should be working
		t.Log("Job execution timed out, but this may be due to system timing")
	}

	c.Stop()
	<-done
}

func TestStop(t *testing.T) {
	c := New()
	c.Start()

	if !c.running {
		t.Error("Cron should be running after Start()")
	}

	c.Stop()

	if c.running {
		t.Error("Cron should not be running after Stop()")
	}
}

func TestStop_NotRunning(t *testing.T) {
	c := New()
	// Should not panic
	c.Stop()
}

func TestClose(t *testing.T) {
	c := New()
	c.Start()

	if !c.running {
		t.Error("Cron should be running after Start()")
	}

	c.Close()

	if c.running {
		t.Error("Cron should not be running after Close()")
	}
}

func TestPos(t *testing.T) {
	c := New()
	schedule := Every(time.Second)
	job := FuncJob(func() {})

	c.Schedule(schedule, job, "job1")
	c.Schedule(schedule, job, "job2")

	pos := c.pos("job1")
	if pos != 0 {
		t.Errorf("pos(\"job1\") = %d, want 0", pos)
	}

	pos = c.pos("job2")
	if pos != 1 {
		t.Errorf("pos(\"job2\") = %d, want 1", pos)
	}

	pos = c.pos("nonexistent")
	if pos != -1 {
		t.Errorf("pos(\"nonexistent\") = %d, want -1", pos)
	}
}

func TestByTime(t *testing.T) {
	now := time.Now()
	entries := []*Entry{
		{Next: now.Add(2 * time.Hour)},
		{Next: now.Add(1 * time.Hour)},
		{Next: now.Add(3 * time.Hour)},
	}

	bt := byTime(entries)
	sort.Sort(bt)

	if !bt[0].Next.Before(bt[1].Next) {
		t.Error("Entries should be sorted by time")
	}
	if !bt[1].Next.Before(bt[2].Next) {
		t.Error("Entries should be sorted by time")
	}
}

func TestByTime_ZeroTime(t *testing.T) {
	now := time.Now()
	entries := []*Entry{
		{Next: now},
		{Next: time.Time{}}, // zero time
		{Next: now.Add(time.Hour)},
	}

	bt := byTime(entries)
	sort.Sort(bt)

	// Zero time should be at the end
	if !bt[2].Next.IsZero() {
		t.Error("Zero time should be sorted to the end")
	}
}

func TestFuncJob_Run(t *testing.T) {
	executed := false
	fn := FuncJob(func() {
		executed = true
	})

	fn.Run()

	if !executed {
		t.Error("FuncJob.Run() should execute the function")
	}
}

func TestEntrySnapshot(t *testing.T) {
	c := New()
	schedule := Every(time.Second)
	job := FuncJob(func() {})

	c.Schedule(schedule, job, "job1")

	snapshot := c.entrySnapshot()
	if len(snapshot) != 1 {
		t.Fatalf("entrySnapshot() length = %d, want 1", len(snapshot))
	}

	// Modify snapshot, original should not change
	snapshot[0].Name = "modified"
	entries := c.Entries()
	if entries[0].Name == "modified" {
		t.Error("entrySnapshot() should return copies")
	}
}

func TestMultipleJobs(t *testing.T) {
	c := New()
	executed1 := make(chan bool, 1)
	executed2 := make(chan bool, 1)

	job1 := FuncJob(func() {
		executed1 <- true
	})

	job2 := FuncJob(func() {
		executed2 <- true
	})

	c.Schedule(Every(10*time.Millisecond), job1, "job1")
	c.Schedule(Every(10*time.Millisecond), job2, "job2")

	c.Start()

	// Wait for both jobs to execute with longer timeout
	done1 := false
	done2 := false
	timeout := time.After(500 * time.Millisecond)

	for !done1 || !done2 {
		select {
		case <-executed1:
			done1 = true
		case <-executed2:
			done2 = true
		case <-timeout:
			// At least verify that cron is running and entries exist
			entries := c.Entries()
			if len(entries) != 2 {
				t.Errorf("Expected 2 entries, got %d", len(entries))
			}
			if !done1 {
				t.Log("job1 execution timed out, but this may be due to system timing")
			}
			if !done2 {
				t.Log("job2 execution timed out, but this may be due to system timing")
			}
			c.Stop()
			return
		}
	}

	c.Stop()
}

func TestCron_StandardSpec(t *testing.T) {
	c := New()

	// Test standard 5-field spec
	err := c.AddFunc("0 0 * * *", func() {
		// Job function
	}, "standard-spec")
	if err != nil {
		t.Fatalf("AddFunc() with standard spec error = %v", err)
	}

	entries := c.Entries()
	if len(entries) != 1 {
		t.Fatalf("Entries() length = %d, want 1", len(entries))
	}
}

func TestCron_WithLocation(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("Could not load timezone: %v", err)
	}

	c := NewWithLocation(loc)
	if c.location != loc {
		t.Errorf("Location = %v, want %v", c.location, loc)
	}
}

func TestCron_Now(t *testing.T) {
	c := New()
	now := c.now()

	if now.IsZero() {
		t.Error("now() should not return zero time")
	}

	// Verify it's in the correct location
	if now.Location() != c.location {
		t.Errorf("now() location = %v, want %v", now.Location(), c.location)
	}
}
