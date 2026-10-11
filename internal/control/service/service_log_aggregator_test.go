// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/safe"
)

type memoryLogRepository struct {
	mu        sync.Mutex
	logs      map[string]*model.StepRunLog
	fail      bool
	afterRead func()
}

func (r *memoryLogRepository) EnsureSchema(context.Context) error { return nil }
func (r *memoryLogRepository) CreateBatch(_ context.Context, entries []*model.StepRunLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return errors.New("database unavailable")
	}
	if r.logs == nil {
		r.logs = make(map[string]*model.StepRunLog)
	}
	for _, entry := range entries {
		if r.logs[entry.LogID] == nil {
			copyEntry := *entry
			r.logs[entry.LogID] = &copyEntry
		}
	}
	return nil
}
func (r *memoryLogRepository) LastLineNumber(_ context.Context, stepRunID string) (int32, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var last int32
	for _, entry := range r.logs {
		if entry.StepRunID == stepRunID && entry.LineNumber > last {
			last = entry.LineNumber
		}
	}
	return last, nil
}
func (r *memoryLogRepository) ListByStepRunIDs(_ context.Context, stepRunIDs []string, fromLine int32, limit int) ([]*model.StepRunLog, error) {
	if r.afterRead != nil {
		r.afterRead()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]*model.StepRunLog, 0)
	for _, entry := range r.logs {
		for _, stepRunID := range stepRunIDs {
			if entry.StepRunID == stepRunID && entry.LineNumber >= fromLine {
				copyEntry := *entry
				result = append(result, &copyEntry)
				break
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Timestamp < result[j].Timestamp })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func newTestLogAggregator(t *testing.T, repository *memoryLogRepository) *LogAggregator {
	t.Helper()
	aggregator := NewLogAggregator(repository)
	t.Cleanup(func() {
		repository.mu.Lock()
		repository.fail = false
		repository.mu.Unlock()
		if err := aggregator.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return aggregator
}

func TestLogAggregatorRetainsFailedFlush(t *testing.T) {
	repository := &memoryLogRepository{fail: true}
	aggregator := newTestLogAggregator(t, repository)
	aggregator.bufferSize = 1
	entry := &LogEntry{LogID: "retry", StepRunID: "step", Content: "keep this", Timestamp: 1}
	if err := aggregator.PushLog(entry); err == nil {
		t.Fatal("flush failure must be returned")
	}
	if err := aggregator.CloseStream("step"); err == nil {
		t.Fatal("close must not claim persistence on error")
	}
	logs, err := aggregator.GetLogsByStepRunID("step", 0, 100)
	if err != nil || len(logs) != 1 || logs[0].Content != "keep this" {
		t.Fatalf("failed logs disappeared: %#v, %v", logs, err)
	}
	repository.mu.Lock()
	repository.fail = false
	repository.mu.Unlock()
	if err := aggregator.PushLog(entry); err != nil {
		t.Fatal(err)
	}
	if err := aggregator.CloseStream("step"); err != nil {
		t.Fatal(err)
	}
	logs, err = aggregator.GetLogsByStepRunID("step", 0, 100)
	if err != nil || len(logs) != 1 {
		t.Fatalf("retry duplicated logs: %#v, %v", logs, err)
	}
}

func TestLogAggregatorConcurrentFirstPush(t *testing.T) {
	repository := &memoryLogRepository{}
	aggregator := newTestLogAggregator(t, repository)
	var wg sync.WaitGroup
	const count = 200
	for n := range count {
		wg.Add(1)
		safe.Go(func() {
			defer wg.Done()
			if err := aggregator.PushLog(&LogEntry{LogID: fmt.Sprint(n), StepRunID: "step", Timestamp: 1}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := aggregator.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	logs, err := aggregator.GetLogsByStepRunID("step", 0, count+1)
	if err != nil || len(logs) != count {
		t.Fatalf("lost concurrent logs: count=%d, err=%v", len(logs), err)
	}
	for i, entry := range logs {
		if entry.LineNumber != int32(i+1) {
			t.Fatalf("sequence[%d] = %d", i, entry.LineNumber)
		}
	}
}

func TestLogAggregatorMergeDuringFlush(t *testing.T) {
	repository := &memoryLogRepository{}
	aggregator := newTestLogAggregator(t, repository)
	if err := aggregator.PushLog(&LogEntry{LogID: "pending", StepRunID: "allowed", Timestamp: 2}); err != nil {
		t.Fatal(err)
	}
	if err := aggregator.PushLog(&LogEntry{LogID: "private", StepRunID: "forbidden", Timestamp: 1}); err != nil {
		t.Fatal(err)
	}
	repository.afterRead = func() {
		if err := aggregator.Flush(context.Background()); err != nil {
			t.Error(err)
		}
	}
	logs, err := aggregator.GetLogsForStepRunIDs(context.Background(), []string{"allowed"}, 0, 10)
	if err != nil || len(logs) != 1 || logs[0].LogID != "pending" {
		t.Fatalf("snapshot race or scope leak: %#v, %v", logs, err)
	}
}

func TestLogAggregatorCancelSubscriptionDuringBroadcast(t *testing.T) {
	aggregator := newTestLogAggregator(t, &memoryLogRepository{})
	var wg sync.WaitGroup
	for range 100 {
		ctx, cancel := context.WithCancel(context.Background())
		_ = aggregator.Subscribe(ctx, "step")
		wg.Add(2)
		safe.Go(func() { defer wg.Done(); cancel() })
		safe.Go(func() { defer wg.Done(); aggregator.broadcastToSubscribers("step", &LogEntry{StepRunID: "step"}) })
	}
	wg.Wait()
}

func TestLogAggregatorRejectsInvalidLogsAndCopiesInput(t *testing.T) {
	aggregator := newTestLogAggregator(t, &memoryLogRepository{})
	if err := aggregator.PushBatch([]*LogEntry{nil, {StepRunID: ""}}); err == nil {
		t.Fatal("invalid batch errors must be reported")
	}
	entry := &LogEntry{StepRunID: "step", Content: "original"}
	if err := aggregator.PushLog(entry); err != nil {
		t.Fatal(err)
	}
	entry.Content = "changed"
	logs, err := aggregator.GetLogsByStepRunID("step", 0, 10)
	if err != nil || len(logs) != 1 || logs[0].Content != "original" {
		t.Fatal("buffer retained mutable caller input")
	}
}

func (r *memoryLogRepository) ListByStepRunID(ctx context.Context, stepRunID string, fromLine int32, limit int) ([]*model.StepRunLog, error) {
	entries, err := r.ListByStepRunIDs(ctx, []string{stepRunID}, fromLine, 5000)
	sort.Slice(entries, func(i, j int) bool { return entries[i].LineNumber < entries[j].LineNumber })
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, err
}

func TestLogAggregatorHistoryPaginationUsesLineOrder(t *testing.T) {
	aggregator := newTestLogAggregator(t, &memoryLogRepository{})
	for index, timestamp := range []int64{30, 10, 20} {
		if err := aggregator.PushLog(&LogEntry{LogID: fmt.Sprint(index), StepRunID: "step", Timestamp: timestamp}); err != nil {
			t.Fatal(err)
		}
	}
	if err := aggregator.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	for line := int32(1); line <= 3; line++ {
		entries, err := aggregator.GetLogsByStepRunID("step", line, 1)
		if err != nil || len(entries) != 1 || entries[0].LineNumber != line {
			t.Fatalf("pagination lost line %d: %#v, %v", line, entries, err)
		}
	}
}

func TestLogAggregatorReleasesOnlyPersistedIdleStreams(t *testing.T) {
	repository := &memoryLogRepository{}
	aggregator := newTestLogAggregator(t, repository)
	if err := aggregator.PushLog(&LogEntry{LogID: "one", StepRunID: "step"}); err != nil {
		t.Fatal(err)
	}
	aggregator.releaseIdleStreams(time.Now().Add(time.Hour))
	if len(aggregator.streams) != 1 {
		t.Fatal("idle cleanup discarded buffered logs")
	}
	if err := aggregator.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	aggregator.releaseIdleStreams(time.Now().Add(time.Hour))
	if len(aggregator.streams) != 0 {
		t.Fatal("persisted idle stream was not released")
	}
	if err := aggregator.PushLog(&LogEntry{LogID: "two", StepRunID: "step"}); err != nil {
		t.Fatal(err)
	}
	entries, err := aggregator.GetLogsByStepRunID("step", 0, 10)
	if err != nil || len(entries) != 2 || entries[1].LineNumber != 2 {
		t.Fatalf("reopened stream did not restore sequence: %#v, %v", entries, err)
	}
}

type identityAwareLogRepository struct {
	*memoryLogRepository
	lookupErr error
}

func (r *identityAwareLogRepository) HasLogID(ctx context.Context, logID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lookupErr != nil {
		return false, r.lookupErr
	}
	return r.logs[logID] != nil, nil
}

func TestLogAggregatorIdentityLookupFailureIsRetryable(t *testing.T) {
	lookupErr := errors.New("identity store unavailable")
	repository := &identityAwareLogRepository{memoryLogRepository: &memoryLogRepository{}, lookupErr: lookupErr}
	aggregator := NewLogAggregator(repository)
	t.Cleanup(func() {
		if err := aggregator.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subscription := aggregator.Subscribe(ctx, "step")
	entry := &LogEntry{LogID: "event-1", StepRunID: "step", Content: "same content"}
	if err := aggregator.PushLogContext(ctx, entry); !errors.Is(err, lookupErr) {
		t.Fatalf("identity lookup failure was not returned: %v", err)
	}
	select {
	case got := <-subscription:
		t.Fatalf("failed lookup broadcast a log: %#v", got)
	default:
	}
	repository.mu.Lock()
	repository.lookupErr = nil
	repository.mu.Unlock()
	if err := aggregator.PushLogContext(ctx, entry); err != nil {
		t.Fatal(err)
	}
	if err := aggregator.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-subscription:
		if got.LogID != "event-1" || got.LineNumber != 1 {
			t.Fatalf("failed lookup advanced line sequence: %#v", got)
		}
	default:
		t.Fatal("recovered lookup did not broadcast accepted log")
	}
	// 已落库的重放不能使用更大的输入行号推进计数器
	entry.LineNumber = 1000
	if err := aggregator.PushLogContext(ctx, entry); err != nil {
		t.Fatal(err)
	}
	entry.LogID, entry.LineNumber = "event-2", 0
	if err := aggregator.PushLogContext(ctx, entry); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-subscription:
		if got.LogID != "event-2" || got.LineNumber != 2 {
			t.Fatalf("replay changed sequence or rebroadcast: %#v", got)
		}
	default:
		t.Fatal("distinct event was not broadcast")
	}
	select {
	case got := <-subscription:
		t.Fatalf("duplicate broadcast remains: %#v", got)
	default:
	}
}

func TestLogAggregatorConcurrentReplayBroadcastsOnce(t *testing.T) {
	repository := &identityAwareLogRepository{memoryLogRepository: &memoryLogRepository{}}
	aggregator := NewLogAggregator(repository)
	t.Cleanup(func() {
		if err := aggregator.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subscription := aggregator.Subscribe(ctx, "step")
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		safe.Go(func() {
			defer wg.Done()
			if err := aggregator.PushLogContext(ctx, &LogEntry{LogID: "event", StepRunID: "step", Content: "once"}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := aggregator.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	entries, err := aggregator.GetLogsByStepRunIDContext(ctx, "step", 0, 10)
	if err != nil || len(entries) != 1 || entries[0].LineNumber != 1 {
		t.Fatalf("concurrent replay duplicated or renumbered the log: %#v, %v", entries, err)
	}
	select {
	case got := <-subscription:
		if got.LogID != "event" || got.LineNumber != 1 {
			t.Fatalf("unexpected log broadcast: %#v", got)
		}
	default:
		t.Fatal("accepted log was not broadcast")
	}
	select {
	case got := <-subscription:
		t.Fatalf("concurrent replay duplicated broadcast: %#v", got)
	default:
	}
}
