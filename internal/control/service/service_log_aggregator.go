// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/safe"
)

// LogEntry 日志条目，沿用 WebSocket 历史字段协议
type LogEntry struct {
	LogID      string `json:"-"`
	StepRunID  string `json:"step_run_id"`
	Timestamp  int64  `json:"timestamp"`
	LineNumber int32  `json:"line_number"`
	Level      string `json:"level"`
	Content    string `json:"content"`
	Stream     string `json:"stream"`
	PluginName string `json:"plugin_name"`
	AgentID    string `json:"agent_id"`
}

// LogAggregator 聚合实时日志并在持久化成功前保留缓冲区
type LogAggregator struct {
	repository    repo.ILogRepository
	mu            sync.RWMutex
	streams       map[string]*LogStream
	subscribers   map[string][]chan *LogEntry
	bufferSize    int
	flushInterval time.Duration
	done          chan struct{}
	stopOnce      sync.Once
	closed        bool
}

// LogStream 保存单个步骤的待写日志和行号
type LogStream struct {
	stepRunID    string
	buffer       []*LogEntry
	mu           sync.Mutex
	lineCount    int32
	initialized  bool
	lastActivity time.Time
	closed       bool
}

// NewLogAggregator 创建使用仓储接口的日志聚合器
func NewLogAggregator(repository repo.ILogRepository) *LogAggregator {
	la := &LogAggregator{
		repository:    repository,
		streams:       make(map[string]*LogStream),
		subscribers:   make(map[string][]chan *LogEntry),
		bufferSize:    100,
		flushInterval: 3 * time.Second,
		done:          make(chan struct{}),
	}
	safe.Go(la.periodicFlush)
	return la
}

// PushLog 推送日志，持久化错误会返回且保留已接受的日志供重试
func (la *LogAggregator) PushLog(entry *LogEntry) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return la.PushLogContext(ctx, entry)
}

// PushLogContext 使用调用方上下文接收日志
func (la *LogAggregator) PushLogContext(ctx context.Context, entry *LogEntry) error {
	if entry == nil || strings.TrimSpace(entry.StepRunID) == "" {
		return errors.New("step run id is required for a log entry")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if la.repository == nil {
		return errors.New("log repository is not configured")
	}
	copyEntry := *entry
	if copyEntry.LogID == "" {
		copyEntry.LogID = id.GetUUID()
	}
	if copyEntry.Timestamp == 0 {
		copyEntry.Timestamp = time.Now().Unix()
	}
	la.mu.Lock()
	if la.closed {
		la.mu.Unlock()
		return errors.New("log aggregator is closed")
	}
	stream := la.streams[copyEntry.StepRunID]
	if stream == nil {
		stream = &LogStream{stepRunID: copyEntry.StepRunID, buffer: make([]*LogEntry, 0, la.bufferSize)}
		la.streams[copyEntry.StepRunID] = stream
	}
	stream.mu.Lock()
	la.mu.Unlock()
	if stream.closed {
		stream.mu.Unlock()
		return fmt.Errorf("log stream for step run %s is closed", copyEntry.StepRunID)
	}
	if !stream.initialized {
		lastLine, err := la.repository.LastLineNumber(ctx, copyEntry.StepRunID)
		if err != nil {
			stream.mu.Unlock()
			return fmt.Errorf("initialize log sequence: %w", err)
		}
		stream.lineCount = lastLine
		stream.initialized = true
	}
	for _, buffered := range stream.buffer {
		if buffered.LogID == copyEntry.LogID {
			err := la.flushStream(ctx, stream)
			stream.mu.Unlock()
			return err
		}
	}
	if identities, ok := la.repository.(repo.ILogIdentityRepository); ok {
		persisted, err := identities.HasLogID(ctx, copyEntry.LogID)
		if err != nil {
			stream.mu.Unlock()
			return fmt.Errorf("check persisted log identity: %w", err)
		}
		if persisted {
			stream.mu.Unlock()
			return nil
		}
	}
	if copyEntry.LineNumber <= 0 {
		copyEntry.LineNumber = stream.lineCount + 1
	}
	if copyEntry.LineNumber > stream.lineCount {
		stream.lineCount = copyEntry.LineNumber
	}
	stream.buffer = append(stream.buffer, &copyEntry)
	stream.lastActivity = time.Now()
	var err error
	if len(stream.buffer) >= la.bufferSize {
		err = la.flushStream(ctx, stream)
	}
	stream.mu.Unlock()
	la.broadcastToSubscribers(copyEntry.StepRunID, &copyEntry)
	return err
}

// PushBatch 批量接收日志并返回所有失败原因
func (la *LogAggregator) PushBatch(entries []*LogEntry) error {
	var errs []error
	for _, entry := range entries {
		if err := la.PushLog(entry); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// flushStream 由持有 stream.mu 的调用者同步执行，失败时不清空缓冲
func (la *LogAggregator) flushStream(ctx context.Context, stream *LogStream) error {
	if len(stream.buffer) == 0 {
		return nil
	}
	if la.repository == nil {
		return errors.New("log repository is not configured")
	}
	entries := make([]*model.StepRunLog, 0, len(stream.buffer))
	for _, entry := range stream.buffer {
		entries = append(entries, &model.StepRunLog{
			LogID: entry.LogID, StepRunID: entry.StepRunID, Timestamp: entry.Timestamp,
			LineNumber: entry.LineNumber, Level: entry.Level, Content: entry.Content,
			Stream: entry.Stream, PluginName: entry.PluginName, AgentID: entry.AgentID,
		})
	}
	if err := la.repository.CreateBatch(ctx, entries); err != nil {
		return fmt.Errorf("persist step run logs: %w", err)
	}
	stream.buffer = stream.buffer[:0]
	return nil
}

// FlushStep 等待指定步骤的缓冲日志持久化
func (la *LogAggregator) FlushStep(ctx context.Context, stepRunID string) error {
	la.mu.RLock()
	stream := la.streams[stepRunID]
	la.mu.RUnlock()
	if stream == nil {
		return nil
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return la.flushStream(ctx, stream)
}

// Flush 等待当前所有步骤的日志持久化，可在失败后安全重试
func (la *LogAggregator) Flush(ctx context.Context) error {
	la.mu.RLock()
	streams := make([]*LogStream, 0, len(la.streams))
	for _, stream := range la.streams {
		streams = append(streams, stream)
	}
	la.mu.RUnlock()
	var errs []error
	for _, stream := range streams {
		stream.mu.Lock()
		err := la.flushStream(ctx, stream)
		stream.mu.Unlock()
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (la *LogAggregator) periodicFlush() {
	ticker := time.NewTicker(la.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-la.done:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := la.Flush(ctx)
			cancel()
			if err != nil {
				log.Errorw("failed to persist buffered logs; retaining for retry", "error", err)
			}
			la.releaseIdleStreams(time.Now().Add(-5 * time.Minute))
		}
	}
}

// releaseIdleStreams 仅释放已落库的闲置流，后续日志从数据库恢复行号
func (la *LogAggregator) releaseIdleStreams(before time.Time) {
	la.mu.Lock()
	defer la.mu.Unlock()
	for stepRunID, stream := range la.streams {
		stream.mu.Lock()
		if len(stream.buffer) == 0 && stream.lastActivity.Before(before) {
			stream.closed = true
			delete(la.streams, stepRunID)
		}
		stream.mu.Unlock()
	}
}

// CloseStream 关闭前同步持久化，写入失败时保留可重试的流
func (la *LogAggregator) CloseStream(stepRunID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	la.mu.Lock()
	defer la.mu.Unlock()
	stream := la.streams[stepRunID]
	if stream == nil {
		return nil
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if err := la.flushStream(ctx, stream); err != nil {
		return err
	}
	stream.closed = true
	delete(la.streams, stepRunID)
	return nil
}

// Close 停止后台刷新和订阅，并同步刷新日志；失败后可再次调用
func (la *LogAggregator) Close(ctx context.Context) error {
	la.mu.Lock()
	la.closed = true
	la.stopOnce.Do(func() { close(la.done) })
	for stepRunID, subscribers := range la.subscribers {
		for _, subscriber := range subscribers {
			close(subscriber)
		}
		delete(la.subscribers, stepRunID)
	}
	la.mu.Unlock()
	return la.Flush(ctx)
}

// GetLogsByStepRunID 获取步骤的持久化和缓冲日志
func (la *LogAggregator) GetLogsByStepRunID(stepRunID string, fromLine int32, limit int) ([]*LogEntry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return la.GetLogsByStepRunIDContext(ctx, stepRunID, fromLine, limit)
}

// GetLogsByStepRunIDContext 使用调用方上下文读取步骤日志
func (la *LogAggregator) GetLogsByStepRunIDContext(ctx context.Context, stepRunID string, fromLine int32, limit int) ([]*LogEntry, error) {
	return la.getLogs(ctx, []string{stepRunID}, fromLine, limit, true)
}

// GetLogsForStepRunIDs 合并限定步骤的历史和缓冲日志，按时间、步骤、行号稳定排序
func (la *LogAggregator) GetLogsForStepRunIDs(ctx context.Context, stepRunIDs []string, fromLine int32, limit int) ([]*LogEntry, error) {
	return la.getLogs(ctx, stepRunIDs, fromLine, limit, false)
}

func (la *LogAggregator) getLogs(ctx context.Context, stepRunIDs []string, fromLine int32, limit int, byLine bool) ([]*LogEntry, error) {
	if limit <= 0 {
		limit = 1000
	}
	if limit > 5000 {
		limit = 5000
	}
	entries := make([]*LogEntry, 0)
	ids := make([]string, 0, len(stepRunIDs))
	seenSteps := make(map[string]bool, len(stepRunIDs))
	streams := make([]*LogStream, 0, len(stepRunIDs))
	la.mu.RLock()
	for _, stepRunID := range stepRunIDs {
		if stepRunID == "" || seenSteps[stepRunID] {
			continue
		}
		seenSteps[stepRunID] = true
		ids = append(ids, stepRunID)
		if stream := la.streams[stepRunID]; stream != nil {
			streams = append(streams, stream)
		}
	}
	la.mu.RUnlock()
	if len(ids) == 0 {
		return entries, nil
	}
	// 先复制缓冲再查询数据库，避免恰好刷新时漏掉日志
	for _, stream := range streams {
		stream.mu.Lock()
		for _, entry := range stream.buffer {
			if entry.LineNumber >= fromLine {
				copyEntry := *entry
				entries = append(entries, &copyEntry)
			}
		}
		stream.mu.Unlock()
	}
	if la.repository == nil {
		return nil, errors.New("log repository is not configured")
	}
	var persisted []*model.StepRunLog
	var err error
	if byLine {
		persisted, err = la.repository.ListByStepRunID(ctx, ids[0], fromLine, limit)
	} else {
		persisted, err = la.repository.ListByStepRunIDs(ctx, ids, fromLine, limit)
	}
	if err != nil {
		return nil, err
	}
	seenLogs := make(map[string]int, len(entries))
	for index, entry := range entries {
		seenLogs[entry.LogID] = index
	}
	for _, entry := range persisted {
		logEntry := &LogEntry{
			LogID: entry.LogID, StepRunID: entry.StepRunID, Timestamp: entry.Timestamp,
			LineNumber: entry.LineNumber, Level: entry.Level, Content: entry.Content,
			Stream: entry.Stream, PluginName: entry.PluginName, AgentID: entry.AgentID,
		}
		if index, exists := seenLogs[entry.LogID]; entry.LogID != "" && exists {
			entries[index] = logEntry
			continue
		}
		if entry.LogID != "" {
			seenLogs[entry.LogID] = len(entries)
		}
		entries = append(entries, logEntry)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		left, right := entries[i], entries[j]
		if byLine && left.LineNumber != right.LineNumber {
			return left.LineNumber < right.LineNumber
		}
		if left.Timestamp != right.Timestamp {
			return left.Timestamp < right.Timestamp
		}
		if left.StepRunID != right.StepRunID {
			return left.StepRunID < right.StepRunID
		}
		if left.LineNumber != right.LineNumber {
			return left.LineNumber < right.LineNumber
		}
		return left.LogID < right.LogID
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

// Subscribe 订阅实时日志，慢订阅者可以通过历史接口补取
func (la *LogAggregator) Subscribe(ctx context.Context, stepRunID string) <-chan *LogEntry {
	logChan := make(chan *LogEntry, 100)
	la.mu.Lock()
	if la.closed {
		close(logChan)
		la.mu.Unlock()
		return logChan
	}
	la.subscribers[stepRunID] = append(la.subscribers[stepRunID], logChan)
	la.mu.Unlock()
	safe.Go(func() {
		select {
		case <-ctx.Done():
		case <-la.done:
		}
		la.unsubscribe(stepRunID, logChan)
	})
	return logChan
}

func (la *LogAggregator) unsubscribe(stepRunID string, logChan chan *LogEntry) {
	la.mu.Lock()
	defer la.mu.Unlock()
	subs := la.subscribers[stepRunID]
	for i, ch := range subs {
		if ch == logChan {
			la.subscribers[stepRunID] = append(subs[:i], subs[i+1:]...)
			close(logChan)
			break
		}
	}
	if len(la.subscribers[stepRunID]) == 0 {
		delete(la.subscribers, stepRunID)
	}
}

func (la *LogAggregator) broadcastToSubscribers(stepRunID string, entry *LogEntry) {
	// 发送期间保持读锁，取消订阅只能在发送完成后关闭 channel
	la.mu.RLock()
	defer la.mu.RUnlock()
	for _, ch := range la.subscribers[stepRunID] {
		copyEntry := *entry
		select {
		case ch <- &copyEntry:
		default:
		}
	}
}

// GetStats 返回聚合器缓冲统计
func (la *LogAggregator) GetStats() map[string]interface{} {
	la.mu.RLock()
	defer la.mu.RUnlock()
	streams := make(map[string]interface{}, len(la.streams))
	for stepRunID, stream := range la.streams {
		stream.mu.Lock()
		streams[stepRunID] = map[string]interface{}{"buffer_size": len(stream.buffer), "line_count": stream.lineCount, "closed": stream.closed}
		stream.mu.Unlock()
	}
	return map[string]interface{}{"active_streams": len(la.streams), "streams": streams}
}
