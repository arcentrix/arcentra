// Copyright 2025 Arcade Team
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
	"io"
	"sync"

	streamv1 "github.com/arcentrix/arcade/api/stream/v1"
	"github.com/arcentrix/arcade/pkg/log"
	"github.com/arcentrix/arcade/pkg/safe"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"gorm.io/gorm"
)

// StreamServiceImpl Stream 服务实现
type StreamServiceImpl struct {
	streamv1.UnimplementedStreamServiceServer
	logAggregator *LogAggregator
	redis         *redis.Client
	clickHouse    *gorm.DB
	mu            sync.RWMutex
	subscribers   map[string][]*LogSubscriber // stepRunID -> subscribers
}

// LogSubscriber 日志订阅者
type LogSubscriber struct {
	StepRunID string
	Stream    grpc.ServerStreamingServer[streamv1.StreamStepRunLogResponse]
	Cancel    context.CancelFunc
}

// NewStreamService 创建Stream服务实例
func NewStreamService(redis *redis.Client, clickHouse *gorm.DB) *StreamServiceImpl {
	return &StreamServiceImpl{
		logAggregator: NewLogAggregator(redis, clickHouse),
		redis:         redis,
		clickHouse:    clickHouse,
		subscribers:   make(map[string][]*LogSubscriber),
	}
}

// GetLogAggregator 获取日志聚合器
func (s *StreamServiceImpl) GetLogAggregator() *LogAggregator {
	return s.logAggregator
}

// UploadStepRunLog Agent端流式上报日志给Server
func (s *StreamServiceImpl) UploadStepRunLog(stream grpc.ClientStreamingServer[streamv1.UploadStepRunLogRequest, streamv1.UploadStepRunLogResponse]) error {
	var stepRunID, agentID string
	receivedLines := int32(0)

	for {
		req, err := stream.Recv()
		if err == io.EOF {
			// 客户端关闭流，返回响应
			log.Infow("log upload stream closed", "stepRunId", stepRunID, "agentId", agentID, "receivedLines", receivedLines)
			return stream.SendAndClose(&streamv1.UploadStepRunLogResponse{
				Success:       true,
				Message:       "logs uploaded successfully",
				ReceivedLines: receivedLines,
			})
		}
		if err != nil {
			log.Errorw("failed to receive log upload", "error", err)
			return err
		}

		// 记录步骤执行ID和AgentID
		if stepRunID == "" {
			stepRunID = req.StepRunId
			agentID = req.AgentId
			log.Infow("start receiving logs for step run", "stepRunId", stepRunID, "agentId", agentID)
		}

		// 转换日志条目并推送到聚合器
		for _, logChunk := range req.Logs {
			entry := &LogEntry{
				StepRunID:  req.StepRunId,
				Timestamp:  logChunk.Timestamp,
				LineNumber: logChunk.LineNumber,
				Level:      logChunk.Level,
				Content:    logChunk.Content,
				Stream:     logChunk.Stream,
				AgentID:    req.AgentId,
			}

			if err := s.logAggregator.PushLog(entry); err != nil {
				log.Errorw("failed to push log to aggregator", "stepRunId", req.StepRunId, "error", err)
			}
			receivedLines++

			// 通知订阅者
			s.notifySubscribers(req.StepRunId, logChunk)
		}
	}
}

// StreamStepRunLog 实时获取步骤执行日志流
func (s *StreamServiceImpl) StreamStepRunLog(req *streamv1.StreamStepRunLogRequest, stream grpc.ServerStreamingServer[streamv1.StreamStepRunLogResponse]) error {
	ctx := stream.Context()
	stepRunID := req.StepRunId

	log.Infow("client requesting log stream", "stepRunId", stepRunID, "fromLine", req.FromLine, "follow", req.Follow)

	// 先从 ClickHouse 获取历史日志
	historicalLogs, err := s.logAggregator.GetLogsByStepRunID(stepRunID, req.FromLine, 1000)
	if err != nil {
		log.Errorw("failed to get historical logs", "stepRunId", stepRunID, "error", err)
		return err
	}

	// 发送历史日志
	for _, entry := range historicalLogs {
		logChunk := &streamv1.LogChunk{
			Timestamp:  entry.Timestamp,
			LineNumber: entry.LineNumber,
			Level:      entry.Level,
			Content:    entry.Content,
			Stream:     entry.Stream,
		}

		if err := stream.Send(&streamv1.StreamStepRunLogResponse{
			StepRunId:  stepRunID,
			LogChunk:   logChunk,
			IsComplete: false,
		}); err != nil {
			log.Errorw("failed to send log", "stepRunId", stepRunID, "error", err)
			return err
		}
	}

	// 如果不需要持续跟踪，发送完成标记并返回
	if !req.Follow {
		return stream.Send(&streamv1.StreamStepRunLogResponse{
			StepRunId:  stepRunID,
			IsComplete: true,
		})
	}

	// 订阅实时日志
	subscriber := &LogSubscriber{
		StepRunID: stepRunID,
		Stream:    stream,
	}
	ctx2, cancel := context.WithCancel(ctx)
	subscriber.Cancel = cancel
	defer cancel()

	// 注册订阅者
	s.mu.Lock()
	s.subscribers[stepRunID] = append(s.subscribers[stepRunID], subscriber)
	s.mu.Unlock()

	// 清理订阅者
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		subs := s.subscribers[stepRunID]
		for i, sub := range subs {
			if sub == subscriber {
				s.subscribers[stepRunID] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		if len(s.subscribers[stepRunID]) == 0 {
			delete(s.subscribers, stepRunID)
		}
	}()

	// 订阅实时日志channel
	logChan := s.logAggregator.Subscribe(ctx2, stepRunID)
	log.Infow("subscribed to real-time logs for step run", "stepRunId", stepRunID)

	// 持续发送日志直到上下文取消
	for {
		select {
		case <-ctx.Done():
			log.Infow("log stream cancelled for step run", "stepRunId", stepRunID)
			return ctx.Err()
		case entry, ok := <-logChan:
			if !ok {
				// 频道关闭
				return stream.Send(&streamv1.StreamStepRunLogResponse{
					StepRunId:  stepRunID,
					IsComplete: true,
				})
			}

			logChunk := &streamv1.LogChunk{
				Timestamp:  entry.Timestamp,
				LineNumber: entry.LineNumber,
				Level:      entry.Level,
				Content:    entry.Content,
				Stream:     entry.Stream,
			}

			if err := stream.Send(&streamv1.StreamStepRunLogResponse{
				StepRunId:  stepRunID,
				LogChunk:   logChunk,
				IsComplete: false,
			}); err != nil {
				log.Errorw("failed to send real-time log", "stepRunId", stepRunID, "error", err)
				return err
			}
		}
	}
}

// StreamStepRunStatus 实时获取步骤执行状态流
func (s *StreamServiceImpl) StreamStepRunStatus(req *streamv1.StreamStepRunStatusRequest, stream grpc.ServerStreamingServer[streamv1.StreamStepRunStatusResponse]) error {
	// TODO: 实现步骤执行状态流
	return fmt.Errorf("not implemented")
}

// StreamJobStatus 实时获取作业状态流
func (s *StreamServiceImpl) StreamJobStatus(req *streamv1.StreamJobStatusRequest, stream grpc.ServerStreamingServer[streamv1.StreamJobStatusResponse]) error {
	// TODO: 实现作业状态流
	return fmt.Errorf("not implemented")
}

// StreamPipelineStatus 实时获取流水线状态流
func (s *StreamServiceImpl) StreamPipelineStatus(req *streamv1.StreamPipelineStatusRequest, stream grpc.ServerStreamingServer[streamv1.StreamPipelineStatusResponse]) error {
	// TODO: 实现流水线状态流
	return fmt.Errorf("not implemented")
}

// AgentChannel Agent与Server双向通信流
func (s *StreamServiceImpl) AgentChannel(stream grpc.BidiStreamingServer[streamv1.AgentChannelRequest, streamv1.AgentChannelResponse]) error {
	// TODO: 实现Agent通道
	return fmt.Errorf("not implemented")
}

// StreamAgentStatus 实时监控Agent状态流
func (s *StreamServiceImpl) StreamAgentStatus(req *streamv1.StreamAgentStatusRequest, stream grpc.ServerStreamingServer[streamv1.StreamAgentStatusResponse]) error {
	// TODO: 实现Agent状态流
	return fmt.Errorf("not implemented")
}

// StreamEvents 实时事件流
func (s *StreamServiceImpl) StreamEvents(req *streamv1.StreamEventsRequest, stream grpc.ServerStreamingServer[streamv1.StreamEventsResponse]) error {
	// TODO: 实现事件流
	return fmt.Errorf("not implemented")
}

// notifySubscribers 通知订阅者
func (s *StreamServiceImpl) notifySubscribers(stepRunID string, logChunk *streamv1.LogChunk) {
	s.mu.RLock()
	subs := s.subscribers[stepRunID]
	s.mu.RUnlock()

	if len(subs) == 0 {
		return
	}

	// 异步通知所有订阅者
	for _, sub := range subs {
		subscriber := sub
		safe.Go(func() {
			err := subscriber.Stream.Send(&streamv1.StreamStepRunLogResponse{
				StepRunId:  stepRunID,
				LogChunk:   logChunk,
				IsComplete: false,
			})
			if err != nil {
				log.Errorw("failed to send log to subscriber", "stepRunId", stepRunID, "error", err)
			}
		})
	}
}
