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

package outbox

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"valid", Config{AgentID: "agent1"}, false},
		{"missing agent", Config{}, true},
		{"scope too long", Config{AgentID: string(make([]byte, 129))}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.cfg.SetDefaults()
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCommitStore_ReadWrite(t *testing.T) {
	dir := t.TempDir()
	cs := NewCommitStore(dir)
	seq, err := cs.Read()
	if err != nil {
		t.Fatal(err)
	}
	if seq != 0 {
		t.Errorf("initial Read() = %d, want 0", seq)
	}
	if writeErr := cs.Write(42); writeErr != nil {
		t.Fatal(writeErr)
	}
	seq, err = cs.Read()
	if err != nil {
		t.Fatal(err)
	}
	if seq != 42 {
		t.Errorf("after Write(42) Read() = %d, want 42", seq)
	}
}

func TestCommitStore_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	cs := NewCommitStore(dir)
	for i := uint64(1); i <= 10; i++ {
		if err := cs.Write(i); err != nil {
			t.Fatal(err)
		}
	}
	seq, err := cs.Read()
	if err != nil {
		t.Fatal(err)
	}
	if seq != 10 {
		t.Errorf("Read() = %d, want 10", seq)
	}
}

func TestRecord_EncodeDecode(t *testing.T) {
	r := &Record{
		Seq:     1,
		Type:    RecordTypeEvent,
		Codec:   CodecJSON,
		Payload: []byte(`{"foo":"bar"}`),
	}
	data := EncodeRecord(r)
	dec := DecodeRecord(data)
	if dec == nil {
		t.Fatal("DecodeRecord returned nil")
	}
	if dec.Seq != r.Seq || dec.Type != r.Type || string(dec.Payload) != string(r.Payload) {
		t.Errorf("decode mismatch: got %+v", dec)
	}
}

func TestWAL_AppendAndRead(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		WALDir:         dir,
		AgentID:        "agent1",
		SegmentMaxSeq:  100,
		FsyncInterval:  10 * time.Millisecond,
		MaxDiskUsageMB: 100,
	}
	cfg.SetDefaults()
	wal, err := NewWAL(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := wal.Close(); closeErr != nil {
			t.Errorf("close wal failed: %v", closeErr)
		}
	}()
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		seq, appendErr := wal.Append(ctx, &Record{
			Type:    RecordTypeEvent,
			Codec:   CodecJSON,
			Payload: []byte(`{"n":` + string(rune('0'+i)) + `}`),
		})
		if appendErr != nil {
			t.Fatal(appendErr)
		}
		if seq != uint64(i+1) {
			t.Errorf("Append seq = %d, want %d", seq, i+1)
		}
	}
	time.Sleep(50 * time.Millisecond)
	recs, err := wal.ReadRecords(0, wal.FlushedSeq(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 5 {
		t.Errorf("ReadRecords got %d, want 5", len(recs))
	}
}

func TestWAL_FlushBoundary(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		WALDir:         dir,
		AgentID:        "agent1",
		SegmentMaxSeq:  100,
		FsyncInterval:  5 * time.Millisecond,
		MaxDiskUsageMB: 100,
	}
	cfg.SetDefaults()
	wal, err := NewWAL(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := wal.Close(); closeErr != nil {
			t.Errorf("close wal failed: %v", closeErr)
		}
	}()
	ctx := context.Background()
	seq, err := wal.Append(ctx, &Record{Type: RecordTypeEvent, Codec: CodecJSON, Payload: []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	written := wal.WrittenSeq()
	flushed := wal.FlushedSeq()
	if written < seq {
		t.Errorf("written_seq %d < seq %d", written, seq)
	}
	time.Sleep(20 * time.Millisecond)
	flushed2 := wal.FlushedSeq()
	if flushed2 < flushed {
		t.Errorf("flushed_seq decreased: %d -> %d", flushed, flushed2)
	}
}

func TestWAL_Recovery(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		WALDir:         dir,
		AgentID:        "agent1",
		SegmentMaxSeq:  100,
		FsyncInterval:  5 * time.Millisecond,
		MaxDiskUsageMB: 100,
	}
	cfg.SetDefaults()
	wal1, err := NewWAL(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_, appendErr := wal1.Append(ctx, &Record{Type: RecordTypeEvent, Codec: CodecJSON, Payload: []byte("a")})
		if appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	if closeErr := wal1.Close(); closeErr != nil {
		t.Fatalf("close wal1 failed: %v", closeErr)
	}
	time.Sleep(20 * time.Millisecond)
	wal2, err := NewWAL(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := wal2.Close(); closeErr != nil {
			t.Errorf("close wal2 failed: %v", closeErr)
		}
	}()
	seq, err := wal2.Append(ctx, &Record{Type: RecordTypeEvent, Codec: CodecJSON, Payload: []byte("b")})
	if err != nil {
		t.Fatal(err)
	}
	if seq != 4 {
		t.Errorf("recovery next seq = %d, want 4", seq)
	}
}

func TestOutbox_AppendAndClose(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		WALDir:         dir,
		AgentID:        "agent1",
		FsyncInterval:  5 * time.Millisecond,
		SendInterval:   10 * time.Millisecond,
		MaxDiskUsageMB: 100,
	}
	cfg.SetDefaults()
	mock := &mockSender{}
	o, err := NewOutbox(cfg, mock)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	seq, err := o.Append(ctx, []byte(`{"test":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if seq != 1 {
		t.Errorf("Append seq = %d, want 1", seq)
	}
	time.Sleep(50 * time.Millisecond)
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOutbox_AppendMap(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		WALDir:         dir,
		AgentID:        "agent1",
		FsyncInterval:  5 * time.Millisecond,
		SendInterval:   10 * time.Millisecond,
		MaxDiskUsageMB: 100,
	}
	cfg.SetDefaults()
	o, err := NewOutbox(cfg, &mockSender{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := o.Close(); closeErr != nil {
			t.Errorf("close outbox failed: %v", closeErr)
		}
	}()
	ctx := context.Background()
	seq, err := o.AppendMap(ctx, map[string]any{"type": "test", "data": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if seq != 1 {
		t.Errorf("AppendMap seq = %d, want 1", seq)
	}
}

func TestOutbox_SendUpdatesCommit(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		WALDir:         dir,
		AgentID:        "agent1",
		FsyncInterval:  5 * time.Millisecond,
		SendInterval:   10 * time.Millisecond,
		MaxDiskUsageMB: 100,
	}
	cfg.SetDefaults()
	mock := &mockSender{}
	o, err := NewOutbox(cfg, mock)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_, appendErr := o.Append(ctx, []byte(`{"n":`+string(rune('0'+i))+`}`))
		if appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if closeErr := o.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	cs := NewCommitStore(filepath.Join(dir, "agent1"))
	lastAcked, err := cs.Read()
	if err != nil {
		t.Fatal(err)
	}
	if lastAcked < 1 {
		t.Errorf("commit not updated: lastAcked=%d", lastAcked)
	}
	// Handshake must carry lastKnownSeq from commit (first send gets 0)
	if len(mock.lastKnownSeq) < 1 || mock.lastKnownSeq[0] != 0 {
		t.Errorf("first Send should get lastKnownSeq=0, got %v", mock.lastKnownSeq)
	}
}

// TestOutbox_NoCommitOnSendFailure verifies that when Send returns an error, commit is not updated
// (so the next send batch will resume from last_acked+1 with the same events).
func TestOutbox_NoCommitOnSendFailure(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		WALDir:         dir,
		AgentID:        "agent1",
		FsyncInterval:  5 * time.Millisecond,
		SendInterval:   15 * time.Millisecond,
		MaxDiskUsageMB: 100,
	}
	cfg.SetDefaults()
	mock := &failingThenOKSender{}
	o, err := NewOutbox(cfg, mock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := o.Close(); closeErr != nil {
			t.Errorf("close outbox: %v", closeErr)
		}
	}()
	ctx := context.Background()
	_, _ = o.Append(ctx, []byte(`{"n":1}`))
	_, _ = o.Append(ctx, []byte(`{"n":2}`))
	time.Sleep(20 * time.Millisecond) // let flush
	// First sendBatch: Send fails -> commit must not advance
	time.Sleep(30 * time.Millisecond) // allow 2 ticker cycles
	cs := NewCommitStore(filepath.Join(dir, "agent1"))
	lastAcked, err := cs.Read()
	if err != nil {
		t.Fatal(err)
	}
	if lastAcked != 0 {
		t.Errorf("after first Send failure commit must stay 0, got lastAcked=%d", lastAcked)
	}
	// Next sendBatch: Send succeeds -> commit advances
	time.Sleep(30 * time.Millisecond)
	lastAcked2, _ := cs.Read()
	if lastAcked2 < 2 {
		t.Errorf("after successful Send commit should be >= 2, got lastAcked=%d", lastAcked2)
	}
	// First Send call must have received lastKnownSeq=0 (resume from 0+1)
	if len(mock.lastKnownSeq) < 1 || mock.lastKnownSeq[0] != 0 {
		t.Errorf("first Send should get lastKnownSeq=0, got %v", mock.lastKnownSeq)
	}
	// Second Send call must have received lastKnownSeq=0 again (we had not committed)
	if len(mock.lastKnownSeq) < 2 || mock.lastKnownSeq[1] != 0 {
		t.Errorf("second Send should get lastKnownSeq=0 (no commit after failure), got %v", mock.lastKnownSeq)
	}
}

type failingThenOKSender struct {
	callCount    int
	lastKnownSeq []uint64
}

func (m *failingThenOKSender) Send(_ context.Context, lastKnownSeq uint64, events []Event) (SendResult, error) {
	m.callCount++
	if m.lastKnownSeq == nil {
		m.lastKnownSeq = make([]uint64, 0)
	}
	m.lastKnownSeq = append(m.lastKnownSeq, lastKnownSeq)
	if m.callCount == 1 {
		return SendResult{}, errors.New("network error")
	}
	if len(events) == 0 {
		return SendResult{}, nil
	}
	return SendResult{LastSeq: events[len(events)-1].Seq}, nil
}

func TestPath_Sanitize(t *testing.T) {
	cfg := Config{
		WALDir:     "/tmp/outbox",
		AgentID:    "agent/../evil",
		ProjectID:  "proj",
		PipelineID: "pipe",
	}
	dir := buildWALDir(&cfg)
	if filepath.Base(dir) == "evil" {
		t.Errorf("path traversal not sanitized: %s", dir)
	}
}

type mockSender struct {
	sent         [][]Event
	lastKnownSeq []uint64 // lastKnownSeq passed for each Send call
}

func (m *mockSender) Send(_ context.Context, lastKnownSeq uint64, events []Event) (SendResult, error) {
	if m.sent == nil {
		m.sent = make([][]Event, 0)
		m.lastKnownSeq = make([]uint64, 0)
	}
	m.sent = append(m.sent, append([]Event(nil), events...))
	m.lastKnownSeq = append(m.lastKnownSeq, lastKnownSeq)
	if len(events) == 0 {
		return SendResult{}, nil
	}
	return SendResult{LastSeq: events[len(events)-1].Seq}, nil
}
