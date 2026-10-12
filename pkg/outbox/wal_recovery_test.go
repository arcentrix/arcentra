package outbox

import (
	"context"
	"github.com/arcentrix/arcentra/pkg/safe"
	"strings"
	"testing"
	"time"
)

func TestWALSequenceSurvivesCommittedSegmentDeletion(t *testing.T) {
	cfg := Config{AgentID: "agent", WALDir: t.TempDir(), FsyncInterval: time.Hour}
	wal, err := NewWAL(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := wal.Append(context.Background(), &Record{Payload: []byte("event")}); err != nil {
			t.Fatal(err)
		}
	}
	if err := wal.Commit().Write(3); err != nil {
		t.Fatal(err)
	}
	if err := wal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := wal.DeleteSegmentsUpTo(3); err != nil {
		t.Fatal(err)
	}
	segments, err := wal.listSegments()
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 0 {
		t.Fatalf("expected all closed committed segments removed: %v", segments)
	}
	restarted, err := NewWAL(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	seq, err := restarted.Append(context.Background(), &Record{Payload: []byte("next")})
	if err != nil {
		t.Fatal(err)
	}
	if seq != 4 {
		t.Fatalf("sequence reset after restart: got %d want 4", seq)
	}
}

func TestWALDoesNotUnlinkActiveSegment(t *testing.T) {
	cfg := Config{AgentID: "agent", WALDir: t.TempDir(), FsyncInterval: time.Hour}
	wal, err := NewWAL(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer wal.Close()
	if _, err := wal.Append(context.Background(), &Record{Payload: []byte("first")}); err != nil {
		t.Fatal(err)
	}
	wal.flushCurrent()
	if err := wal.Commit().Write(1); err != nil {
		t.Fatal(err)
	}
	if err := wal.DeleteSegmentsUpTo(1); err != nil {
		t.Fatal(err)
	}
	if _, err := wal.Append(context.Background(), &Record{Payload: []byte("second")}); err != nil {
		t.Fatal(err)
	}
	wal.flushCurrent()
	records, err := wal.ReadRecords(1, wal.FlushedSeq(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Seq != 2 || string(records[0].Payload) != "second" {
		t.Fatalf("record appended after ACK was lost: %+v", records)
	}
}

func TestWALFailedFsyncDoesNotAdvanceFlushedSequence(t *testing.T) {
	cfg := Config{AgentID: "agent", WALDir: t.TempDir(), FsyncInterval: time.Hour}
	wal, err := NewWAL(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer wal.Close()
	if _, err := wal.Append(context.Background(), &Record{Payload: []byte("event")}); err != nil {
		t.Fatal(err)
	}
	wal.mu.Lock()
	err = wal.currentFile.Close()
	wal.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	wal.flushCurrent()
	if wal.FlushedSeq() != 0 {
		t.Fatal("failed fsync advertised unpersisted records as durable")
	}
}

func TestWALConcurrentAppendKeepsSequenceOrder(t *testing.T) {
	cfg := Config{AgentID: "agent", WALDir: t.TempDir(), FsyncInterval: time.Hour}
	wal, err := NewWAL(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer wal.Close()
	const count = 50
	done := make(chan error, count)
	for i := 0; i < count; i++ {
		safe.Go(func() {
			_, err := wal.Append(context.Background(), &Record{Payload: []byte(strings.Repeat("event", 10000))})
			done <- err
		})
	}
	for i := 0; i < count; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	wal.flushCurrent()
	records, err := wal.ReadRecords(0, wal.FlushedSeq(), count)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != count {
		t.Fatalf("lost concurrent records: %d", len(records))
	}
	for i, record := range records {
		if record.Seq != uint64(i+1) {
			t.Fatalf("out-of-order WAL sequence at %d: %d", i, record.Seq)
		}
	}
}
