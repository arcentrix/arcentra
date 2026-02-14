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

package service

import (
	"fmt"
	"time"

	streamv1 "github.com/arcentrix/arcentra/api/stream/v1"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/logstream"
	"github.com/arcentrix/arcentra/pkg/mq/kafka"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/bytedance/sonic"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"gorm.io/gorm"
)

// StreamServiceImpl Stream 服务实现
type StreamServiceImpl struct {
	streamv1.UnimplementedStreamServiceServer
	logAggregator *LogAggregator
	redis         *redis.Client
	mysql         *gorm.DB
	logConsumer   *kafka.Consumer
}

type KafkaSettings struct {
	BootstrapServers string
	SecurityProtocol string
	Sasl             SaslSettings
	Ssl              SslSettings
}

type SaslSettings struct {
	Mechanism string
	Username  string
	Password  string
}

type SslSettings struct {
	CaFile   string
	CertFile string
	KeyFile  string
	Password string
}

// NewStreamService 创建Stream服务实例
func NewStreamService(redis *redis.Client, mysql *gorm.DB, kafkaSettings KafkaSettings) *StreamServiceImpl {
	service := &StreamServiceImpl{
		logAggregator: NewLogAggregator(redis, mysql),
		redis:         redis,
		mysql:         mysql,
	}
	service.startKafkaLogConsumer(kafkaSettings)
	return service
}

func (s *StreamServiceImpl) startKafkaLogConsumer(cfg KafkaSettings) {
	if cfg.BootstrapServers == "" {
		return
	}

	clientOptions := []kafka.ClientOption{
		kafka.WithSecurityProtocol(cfg.SecurityProtocol),
		kafka.WithSaslMechanism(cfg.Sasl.Mechanism),
		kafka.WithSaslUsername(cfg.Sasl.Username),
		kafka.WithSaslPassword(cfg.Sasl.Password),
		kafka.WithSslCaFile(cfg.Ssl.CaFile),
		kafka.WithSslCertFile(cfg.Ssl.CertFile),
		kafka.WithSslKeyFile(cfg.Ssl.KeyFile),
		kafka.WithSslPassword(cfg.Ssl.Password),
	}

	consumer, err := kafka.NewConsumer(
		cfg.BootstrapServers,
		"BUILD_LOGS",
		"arcentra",
		kafka.WithConsumerClientOptions(clientOptions...),
		kafka.WithConsumerAutoOffsetReset("earliest"),
	)
	if err != nil {
		log.Warnw("failed to create kafka log consumer", "error", err)
		return
	}

	s.logConsumer = consumer

	safe.Go(func() {
		if err := consumer.Subscribe([]string{"BUILD_LOGS"}); err != nil {
			log.Warnw("failed to subscribe build logs topic", "error", err)
			return
		}

		for {
			msg, err := consumer.ReadMessage(200 * time.Millisecond)
			if err != nil {
				continue
			}
			var payload logstream.BuildLogMessage
			if err := sonic.Unmarshal(msg.Value, &payload); err != nil {
				log.Warnw("failed to unmarshal build log message", "error", err)
				continue
			}
			entry := &LogEntry{
				StepRunID:  payload.StepRunId,
				Timestamp:  payload.Timestamp,
				LineNumber: payload.LineNumber,
				Level:      payload.Level,
				Content:    payload.Content,
				Stream:     payload.Stream,
				PluginName: payload.PluginName,
				AgentID:    payload.AgentId,
			}
			if err := s.logAggregator.PushLog(entry); err != nil {
				log.Warnw("failed to push log entry", "error", err)
			}
		}
	})
}

// GetLogAggregator 获取日志聚合器
func (s *StreamServiceImpl) GetLogAggregator() *LogAggregator {
	return s.logAggregator
}

// UploadStepRunLog Agent端流式上报日志给Server
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
