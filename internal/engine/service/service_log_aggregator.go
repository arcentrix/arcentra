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

package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// LogAggregator 日志聚合器
type LogAggregator struct {
	clickHouse    *gorm.DB
	mu            sync.RWMutex
	streams       map[string]*LogStream       // stepRunID -> LogStream
	subscribers   map[string][]chan *LogEntry // stepRunID -> subscribers channels
	bufferSize    int
	flushInterval time.Duration
	tableName     string
}

// LogStream 日志流
type LogStream struct {
	stepRunID string
	buffer    []*LogEntry
	mu        sync.Mutex
	lastFlush time.Time
	lineCount int32
	closed    bool
}

// LogEntry 日志条目
type LogEntry struct {
	StepRunID  string `json:"step_run_id"`
	Timestamp  int64  `json:"timestamp"`
	LineNumber int32  `json:"line_number"`
	Level      string `json:"level"`
	Content    string `json:"content"`
	Stream     string `json:"stream"` // stdout/stderr
	PluginName string `json:"plugin_name"`
	AgentID    string `json:"agent_id"`
}

// NewLogAggregator 创建日志聚合器
func NewLogAggregator(redis *redis.Client, clickHouse *gorm.DB) *LogAggregator {
	la := &LogAggregator{
		clickHouse:    clickHouse,
		streams:       make(map[string]*LogStream),
		subscribers:   make(map[string][]chan *LogEntry),
		bufferSize:    100,             // 缓冲100条日志后写入
		flushInterval: 3 * time.Second, // 3秒强制刷新
		tableName:     "step_run_logs",
	}

	// 创建表（如果不存在）
	if clickHouse != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := la.createTableIfNotExists(ctx); err != nil {
			log.Warnw("failed to create step run logs table", "error", err)
		}
	}

	return la
}

// createTableIfNotExists 创建表（如果不存在）
func (la *LogAggregator) createTableIfNotExists(ctx context.Context) error {
	if la.clickHouse == nil {
		return fmt.Errorf("clickhouse is nil")
	}

	sqlDB, err := la.clickHouse.DB()
	if err != nil {
		return fmt.Errorf("failed to get SQL DB from GORM: %w", err)
	}

	createTableSQL := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			step_run_id String,
			timestamp Int64,
			line_number Int32,
			level String,
			content String,
			stream String,
			plugin_name String,
			agent_id String
		) ENGINE = MergeTree()
		ORDER BY (step_run_id, line_number, timestamp)
		PRIMARY KEY (step_run_id, line_number)
		SETTINGS index_granularity = 8192
	`, la.tableName)

	_, err = sqlDB.ExecContext(ctx, createTableSQL)
	return err
}

// PushLog 推送日志到聚合器
func (la *LogAggregator) PushLog(entry *LogEntry) error {
	la.mu.RLock()
	stream, exists := la.streams[entry.StepRunID]
	la.mu.RUnlock()

	if !exists {
		// 创建新的流
		stream = &LogStream{
			stepRunID: entry.StepRunID,
			buffer:    make([]*LogEntry, 0, la.bufferSize),
			lastFlush: time.Now(),
		}
		la.mu.Lock()
		la.streams[entry.StepRunID] = stream
		la.mu.Unlock()

		// 启动定期刷新
		go la.periodicFlush(entry.StepRunID)
	}

	stream.mu.Lock()
	defer stream.mu.Unlock()

	if stream.closed {
		return fmt.Errorf("log stream for step run %s is closed", entry.StepRunID)
	}

	stream.buffer = append(stream.buffer, entry)
	stream.lineCount++

	// 广播到所有订阅者（异步，不阻塞）
	go la.broadcastToSubscribers(entry.StepRunID, entry)

	// 达到缓冲大小，触发刷新
	if len(stream.buffer) >= la.bufferSize {
		return la.flushStream(stream)
	}

	return nil
}

// PushBatch 批量推送日志
func (la *LogAggregator) PushBatch(entries []*LogEntry) error {
	for _, entry := range entries {
		if err := la.PushLog(entry); err != nil {
			log.Errorw("failed to push log", "stepRunId", entry.StepRunID, "error", err)
		}
	}
	return nil
}

// flushStream 刷新日志流到存储
func (la *LogAggregator) flushStream(stream *LogStream) error {
	if len(stream.buffer) == 0 {
		return nil
	}

	// 复制缓冲区以释放锁
	logs := make([]*LogEntry, len(stream.buffer))
	copy(logs, stream.buffer)
	stream.buffer = stream.buffer[:0]
	stream.lastFlush = time.Now()

	// 异步写入，避免阻塞
	safe.Go(func() {
		// 写入 ClickHouse
		if err := la.writeToClickHouse(logs); err != nil {
			log.Errorw("failed to write logs to clickhouse", "logCount", len(logs), "error", err)
		}
	})

	return nil
}

// writeToClickHouse 写入 ClickHouse
func (la *LogAggregator) writeToClickHouse(logs []*LogEntry) error {
	if la.clickHouse == nil {
		return fmt.Errorf("clickhouse client is nil")
	}

	sqlDB, err := la.clickHouse.DB()
	if err != nil {
		return fmt.Errorf("failed to get SQL DB from GORM: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 批量插入
	insertSQL := fmt.Sprintf(`
		INSERT INTO %s (
			step_run_id, timestamp, line_number, level, content, stream, plugin_name, agent_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, la.tableName)

	// 使用事务批量插入
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, insertSQL)
	if err != nil {
		return fmt.Errorf("prepare statement: %w", err)
	}
	defer stmt.Close()

	for _, logEntry := range logs {
		_, err := stmt.ExecContext(ctx,
			logEntry.StepRunID,
			logEntry.Timestamp,
			logEntry.LineNumber,
			logEntry.Level,
			logEntry.Content,
			logEntry.Stream,
			logEntry.PluginName,
			logEntry.AgentID,
		)
		if err != nil {
			return fmt.Errorf("insert log to clickhouse: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

// periodicFlush 定期刷新日志流
func (la *LogAggregator) periodicFlush(stepRunID string) {
	ticker := time.NewTicker(la.flushInterval)
	defer ticker.Stop()

	for range ticker.C {
		la.mu.RLock()
		stream, exists := la.streams[stepRunID]
		la.mu.RUnlock()

		if !exists {
			return
		}

		stream.mu.Lock()
		if stream.closed {
			stream.mu.Unlock()
			return
		}

		if time.Since(stream.lastFlush) >= la.flushInterval {
			la.flushStream(stream)
		}
		stream.mu.Unlock()
	}
}

// CloseStream 关闭日志流
func (la *LogAggregator) CloseStream(stepRunID string) error {
	la.mu.Lock()
	stream, exists := la.streams[stepRunID]
	if !exists {
		la.mu.Unlock()
		return fmt.Errorf("log stream for step run %s not found", stepRunID)
	}
	delete(la.streams, stepRunID)
	la.mu.Unlock()

	stream.mu.Lock()
	defer stream.mu.Unlock()

	if !stream.closed {
		stream.closed = true
		// 刷新剩余日志
		return la.flushStream(stream)
	}

	return nil
}

// GetLogsByStepRunID 从 ClickHouse 获取步骤执行日志
func (la *LogAggregator) GetLogsByStepRunID(stepRunID string, fromLine int32, limit int) ([]*LogEntry, error) {
	if la.clickHouse == nil {
		return nil, fmt.Errorf("clickhouse client is nil")
	}

	sqlDB, err := la.clickHouse.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get SQL DB from GORM: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 构建查询 SQL
	querySQL := fmt.Sprintf(`
		SELECT step_run_id, timestamp, line_number, level, content, stream, plugin_name, agent_id
		FROM %s
		WHERE step_run_id = ?
	`, la.tableName)

	args := []interface{}{stepRunID}
	if fromLine > 0 {
		querySQL += " AND line_number >= ?"
		args = append(args, fromLine)
	}

	querySQL += " ORDER BY line_number ASC"

	if limit > 0 {
		querySQL += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := sqlDB.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("query logs from clickhouse: %w", err)
	}
	defer rows.Close()

	var logs []*LogEntry
	for rows.Next() {
		var entry LogEntry
		if err := rows.Scan(
			&entry.StepRunID,
			&entry.Timestamp,
			&entry.LineNumber,
			&entry.Level,
			&entry.Content,
			&entry.Stream,
			&entry.PluginName,
			&entry.AgentID,
		); err != nil {
			log.Warnw("failed to scan log entry", "error", err)
			continue
		}
		logs = append(logs, &entry)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return logs, nil
}

// Subscribe 订阅步骤执行日志（创建一个channel接收实时日志）
func (la *LogAggregator) Subscribe(ctx context.Context, stepRunID string) <-chan *LogEntry {
	logChan := make(chan *LogEntry, 100)

	la.mu.Lock()
	la.subscribers[stepRunID] = append(la.subscribers[stepRunID], logChan)
	la.mu.Unlock()

	// 清理订阅
	safe.Go(func() {
		<-ctx.Done()
		la.unsubscribe(stepRunID, logChan)
	})

	return logChan
}

// unsubscribe 取消订阅
func (la *LogAggregator) unsubscribe(stepRunID string, logChan chan *LogEntry) {
	la.mu.Lock()
	defer la.mu.Unlock()

	subs := la.subscribers[stepRunID]
	for i, ch := range subs {
		if ch == logChan {
			// 从切片中移除
			la.subscribers[stepRunID] = append(subs[:i], subs[i+1:]...)
			close(logChan)
			break
		}
	}

	// 如果没有订阅者了，删除key
	if len(la.subscribers[stepRunID]) == 0 {
		delete(la.subscribers, stepRunID)
	}
}

// broadcastToSubscribers 广播日志到所有订阅者
func (la *LogAggregator) broadcastToSubscribers(stepRunID string, entry *LogEntry) {
	la.mu.RLock()
	subscribers := la.subscribers[stepRunID]
	la.mu.RUnlock()

	if len(subscribers) == 0 {
		return
	}

	// 异步发送到所有订阅者
	for _, ch := range subscribers {
		select {
		case ch <- entry:
			// 发送成功
		default:
			// channel满了，跳过（避免阻塞）
			log.Warnw("subscriber channel full for step run, skipping log entry", "stepRunId", stepRunID)
		}
	}
}

// GetStats 获取统计信息
func (la *LogAggregator) GetStats() map[string]interface{} {
	la.mu.RLock()
	defer la.mu.RUnlock()

	stats := map[string]interface{}{
		"active_streams": len(la.streams),
		"streams":        make(map[string]interface{}),
	}

	for stepRunID, stream := range la.streams {
		stream.mu.Lock()
		stats["streams"].(map[string]interface{})[stepRunID] = map[string]interface{}{
			"buffer_size": len(stream.buffer),
			"line_count":  stream.lineCount,
			"closed":      stream.closed,
		}
		stream.mu.Unlock()
	}

	return stats
}
