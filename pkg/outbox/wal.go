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
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/edsrzf/mmap-go"
	"golang.org/x/sync/errgroup"
)

const segmentNameFmt = "%016d.wal"

var segmentNameRe = regexp.MustCompile(`^(\d{16})\.wal$`)

type writeReq struct {
	record []byte
	seq    uint64
	done   chan error
}

// WAL holds the segment WAL with single writer and flush boundary.
type WAL struct {
	dir          string
	cfg          *Config
	writeCh      chan writeReq
	commit       *CommitStore
	nextSeq      uint64
	writtenSeq   uint64
	flushedSeq   uint64
	segmentCount int
	currentFile  *os.File
	currentStart uint64
	mu           sync.Mutex
	ctx          context.Context
	cancel       context.CancelFunc
	eg           *errgroup.Group
}

// NewWAL creates a WAL with the given config.
func NewWAL(cfg *Config) (*WAL, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.SetDefaults()
	dir := buildWALDir(cfg)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create wal dir: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &WAL{
		dir:          dir,
		cfg:          cfg,
		writeCh:      make(chan writeReq, 1024),
		commit:       NewCommitStore(dir),
		segmentCount: 0,
		ctx:          ctx,
		cancel:       cancel,
	}
	maxSeq, err := w.scanMaxSeq()
	if err != nil {
		cancel()
		return nil, err
	}
	w.nextSeq = maxSeq + 1
	w.writtenSeq = maxSeq
	w.flushedSeq = maxSeq
	w.eg, _ = errgroup.WithContext(ctx)
	w.eg.Go(func() error {
		w.runWriter()
		return nil
	})
	return w, nil
}

func (w *WAL) runWriter() {
	ticker := time.NewTicker(w.cfg.FsyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.ctx.Done():
			w.drainWrites()
			w.flushCurrent()
			return
		case req := <-w.writeCh:
			if err := w.appendRecord(req.record, req.seq); err != nil {
				if req.done != nil {
					req.done <- err
				}
				continue
			}
			atomic.StoreUint64(&w.writtenSeq, req.seq)
			if req.done != nil {
				req.done <- nil
			}
		case <-ticker.C:
			w.flushCurrent()
		}
	}
}

func (w *WAL) drainWrites() {
	for {
		select {
		case req := <-w.writeCh:
			if err := w.appendRecord(req.record, req.seq); err != nil {
				if req.done != nil {
					req.done <- err
				}
			} else {
				atomic.StoreUint64(&w.writtenSeq, req.seq)
				if req.done != nil {
					req.done <- nil
				}
			}
		default:
			return
		}
	}
}

func (w *WAL) appendRecord(data []byte, seq uint64) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.currentFile == nil || w.segmentCount >= w.cfg.SegmentMaxSeq {
		if w.currentFile != nil {
			_ = w.currentFile.Sync()
			_ = w.currentFile.Close()
			w.currentFile = nil
		}
		startSeq := seq
		fpath := filepath.Join(w.dir, fmt.Sprintf(segmentNameFmt, startSeq))
		f, err := os.OpenFile(fpath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return err
		}
		w.currentFile = f
		w.currentStart = startSeq
		w.segmentCount = 0
	}
	_, err := w.currentFile.Write(data)
	if err != nil {
		return err
	}
	w.segmentCount++
	return nil
}

func (w *WAL) flushCurrent() {
	w.mu.Lock()
	f := w.currentFile
	ws := atomic.LoadUint64(&w.writtenSeq)
	w.mu.Unlock()
	if f != nil {
		if err := f.Sync(); err != nil {
			log.Errorw("wal fsync failed", "error", err)
		}
	}
	atomic.StoreUint64(&w.flushedSeq, ws)
}

// diskUsageBytes returns total size of segment files.
func (w *WAL) diskUsageBytes() (int64, error) {
	segs, err := w.listSegments()
	if err != nil {
		return 0, err
	}
	var total int64
	for _, path := range segs {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		total += info.Size()
	}
	return total, nil
}

// Append enqueues a record for writing. Returns when enqueued; actual flush is async.
func (w *WAL) Append(ctx context.Context, r *Record) (uint64, error) {
	usage, err := w.diskUsageBytes()
	if err == nil && usage >= w.cfg.MaxDiskUsageMB*1024*1024 {
		return 0, ErrDiskFull
	}
	seq := atomic.AddUint64(&w.nextSeq, 1) - 1
	r.Seq = seq
	data := EncodeRecord(r)
	done := make(chan error, 1)
	select {
	case w.writeCh <- writeReq{record: data, seq: seq, done: done}:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	select {
	case err := <-done:
		return seq, err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// WrittenSeq returns the last written seq.
func (w *WAL) WrittenSeq() uint64 {
	return atomic.LoadUint64(&w.writtenSeq)
}

// FlushedSeq returns the last flushed seq.
func (w *WAL) FlushedSeq() uint64 {
	return atomic.LoadUint64(&w.flushedSeq)
}

// ReadRecords reads records with seq in (lastAcked, flushedSeq], up to limit.
func (w *WAL) ReadRecords(lastAcked, flushedSeq uint64, limit int) ([]*Record, error) {
	segments, err := w.listSegments()
	if err != nil {
		return nil, err
	}
	var out []*Record
	for _, seg := range segments {
		if out != nil && len(out) >= limit {
			break
		}
		recs, err := w.readSegment(seg, lastAcked, flushedSeq, limit-len(out))
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}

func (w *WAL) listSegments() ([]string, error) {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return nil, err
	}
	var segs []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := segmentNameRe.FindStringSubmatch(e.Name())
		if m != nil {
			segs = append(segs, filepath.Join(w.dir, e.Name()))
		}
	}
	sort.Strings(segs)
	return segs, nil
}

func (w *WAL) readSegment(path string, lastAcked, flushedSeq uint64, limit int) ([]*Record, error) {
	recs, err := w.readSegmentMmap(path, lastAcked, flushedSeq, limit)
	if err == nil {
		return recs, nil
	}
	return w.readSegmentRead(path, lastAcked, flushedSeq, limit)
}

// readSegmentMmap reads segment via memory-mapped file for zero-copy scan.
func (w *WAL) readSegmentMmap(path string, lastAcked, flushedSeq uint64, limit int) ([]*Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() == 0 {
		return nil, fmt.Errorf("empty file")
	}
	m, err := mmap.Map(f, mmap.RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = m.Unmap() }()
	data := []byte(m)
	var out []*Record
	offset := 0
	for len(out) < limit && offset < len(data) {
		rec, next, ok := ReadNextRecordFromSlice(data, offset)
		if !ok {
			break
		}
		offset = next
		if rec.Seq > lastAcked && rec.Seq <= flushedSeq {
			out = append(out, rec)
		}
	}
	return out, nil
}

// readSegmentRead reads segment via os.File (fallback when mmap is unavailable).
func (w *WAL) readSegmentRead(path string, lastAcked, flushedSeq uint64, limit int) ([]*Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func(f *os.File) {
		_ = f.Close()
	}(f)
	var out []*Record
	var rec *Record
	for len(out) < limit {
		rec, err = ReadNextRecord(f)
		if err != nil || rec == nil {
			break
		}
		if rec.Seq > lastAcked && rec.Seq <= flushedSeq {
			out = append(out, rec)
		}
	}
	return out, nil
}

func (w *WAL) scanMaxSeq() (uint64, error) {
	segs, err := w.listSegments()
	if err != nil {
		return 0, err
	}
	var maxSeq uint64
	for _, path := range segs {
		segMax, err := w.segmentMaxSeqMmap(path)
		if err != nil {
			segMax, err = w.segmentMaxSeqRead(path)
			if err != nil {
				return 0, err
			}
		}
		if segMax > maxSeq {
			maxSeq = segMax
		}
	}
	return maxSeq, nil
}

// DeleteSegmentsUpTo deletes segments whose maxSeq <= lastAcked.
func (w *WAL) DeleteSegmentsUpTo(lastAcked uint64) error {
	segs, err := w.listSegments()
	if err != nil {
		return err
	}
	for _, path := range segs {
		maxSeq, err := w.segmentMaxSeq(path)
		if err != nil {
			continue
		}
		if maxSeq <= lastAcked {
			_ = os.Remove(path)
		}
	}
	return nil
}

func (w *WAL) segmentMaxSeq(path string) (uint64, error) {
	maxSeq, err := w.segmentMaxSeqMmap(path)
	if err == nil {
		return maxSeq, nil
	}
	return w.segmentMaxSeqRead(path)
}

// segmentMaxSeqMmap scans segment via mmap for max seq.
func (w *WAL) segmentMaxSeqMmap(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if info.Size() == 0 {
		return 0, nil
	}
	m, err := mmap.Map(f, mmap.RDONLY, 0)
	if err != nil {
		return 0, err
	}
	defer func() { _ = m.Unmap() }()
	data := []byte(m)
	var maxSeq uint64
	offset := 0
	for offset < len(data) {
		rec, next, ok := ReadNextRecordFromSlice(data, offset)
		if !ok {
			break
		}
		offset = next
		if rec.Seq > maxSeq {
			maxSeq = rec.Seq
		}
	}
	return maxSeq, nil
}

// segmentMaxSeqRead scans segment via os.File (fallback).
func (w *WAL) segmentMaxSeqRead(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func(f *os.File) {
		_ = f.Close()
	}(f)
	var maxSeq uint64
	var rec *Record
	for {
		rec, err = ReadNextRecord(f)
		if err != nil || rec == nil {
			break
		}
		if rec.Seq > maxSeq {
			maxSeq = rec.Seq
		}
	}
	return maxSeq, nil
}

// Commit returns the CommitStore.
func (w *WAL) Commit() *CommitStore {
	return w.commit
}

// Close stops the writer and waits for cleanup.
func (w *WAL) Close() error {
	w.cancel()
	err := w.eg.Wait()
	w.mu.Lock()
	if w.currentFile != nil {
		_ = w.currentFile.Sync()
		_ = w.currentFile.Close()
		w.currentFile = nil
	}
	w.mu.Unlock()
	return err
}
