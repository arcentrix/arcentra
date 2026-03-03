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
	"context"
	"fmt"
	"strings"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	streamv1 "github.com/arcentrix/arcentra/api/stream/v1"
	"github.com/arcentrix/arcentra/internal/engine/model"
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
	kafkaCfg      KafkaSettings
}

const (
	streamStatusTopic   = "EVENT_PIPELINE"
	streamConsumerGroup = "arcentra-stream"
)

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
		kafkaCfg:      kafkaSettings,
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
				StepRunID:  payload.StepRunID,
				Timestamp:  payload.Timestamp,
				LineNumber: payload.LineNumber,
				Level:      payload.Level,
				Content:    payload.Content,
				Stream:     payload.Stream,
				PluginName: payload.PluginName,
				AgentID:    payload.AgentID,
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

// StreamStepRunStatus Agent端流式上报日志给Server
// StreamStepRunStatus 实时获取步骤执行状态流
func (s *StreamServiceImpl) StreamStepRunStatus(
	req *streamv1.StreamStepRunStatusRequest,
	stream grpc.ServerStreamingServer[streamv1.StreamStepRunStatusResponse],
) error {
	consumer, err := s.newTopicConsumer("steprun-status")
	if err != nil {
		return err
	}
	defer func() { _ = consumer.Close() }()
	if err := consumer.Subscribe([]string{streamStatusTopic}); err != nil {
		return err
	}

	for {
		select {
		case <-stream.Context().Done():
			return nil
		default:
			msg, err := consumer.ReadMessage(200 * time.Millisecond)
			if err != nil {
				continue
			}
			event := map[string]any{}
			if err := sonic.Unmarshal(msg.Value, &event); err != nil {
				continue
			}
			info := parseEventInfo(event)
			if !matchStepRunStreamRequest(req, info) {
				continue
			}
			data := getMapAny(event, "data")
			if err := stream.Send(&streamv1.StreamStepRunStatusResponse{
				StepRunId:      info.stepRunId,
				PipelineId:     info.pipelineId,
				PipelineRunId:  getString(event, "pipelineRunId"),
				JobId:          info.jobId,
				JobName:        info.jobName,
				StepName:       info.stepName,
				Status:         toStepRunStatus(info.status, info.eventType),
				PreviousStatus: steprunv1.StepRunStatus_STEP_RUN_STATUS_UNSPECIFIED,
				Timestamp:      time.Now().Unix(),
				AgentId:        getString(data, "agentId"),
				ExitCode:       int32(getInt(data, "exitCode")),
				ErrorMessage:   getString(data, "error"),
				Duration:       int64(getInt(data, "duration")),
				Metrics:        toStringMap(getMapAny(data, "metrics")),
			}); err != nil {
				return err
			}
		}
	}
}

// StreamJobStatus 实时获取作业状态流
func (s *StreamServiceImpl) StreamJobStatus(
	req *streamv1.StreamJobStatusRequest,
	stream grpc.ServerStreamingServer[streamv1.StreamJobStatusResponse],
) error {
	consumer, err := s.newTopicConsumer("job-status")
	if err != nil {
		return err
	}
	defer func() { _ = consumer.Close() }()
	if err := consumer.Subscribe([]string{streamStatusTopic}); err != nil {
		return err
	}

	for {
		select {
		case <-stream.Context().Done():
			return nil
		default:
			msg, err := consumer.ReadMessage(200 * time.Millisecond)
			if err != nil {
				continue
			}
			event := map[string]any{}
			if err := sonic.Unmarshal(msg.Value, &event); err != nil {
				continue
			}
			info := parseEventInfo(event)
			if !matchJobStreamRequest(req, info) {
				continue
			}
			data := getMapAny(event, "data")
			if err := stream.Send(&streamv1.StreamJobStatusResponse{
				JobId:            info.jobId,
				JobName:          info.jobName,
				PipelineId:       info.pipelineId,
				PipelineRunId:    getString(event, "pipelineRunId"),
				Status:           toPipelineStatus(info.status, info.eventType),
				PreviousStatus:   pipelinev1.PipelineStatus_PIPELINE_STATUS_UNSPECIFIED,
				Timestamp:        time.Now().Unix(),
				TotalSteps:       int32(getInt(data, "totalSteps")),
				CompletedSteps:   int32(getInt(data, "completedSteps")),
				FailedSteps:      int32(getInt(data, "failedSteps")),
				RunningSteps:     int32(getInt(data, "runningSteps")),
				CurrentStepIndex: int32(getInt(data, "currentStepIndex")),
				Duration:         int64(getInt(data, "duration")),
			}); err != nil {
				return err
			}
		}
	}
}

// StreamPipelineStatus 实时获取流水线状态流
func (s *StreamServiceImpl) StreamPipelineStatus(
	req *streamv1.StreamPipelineStatusRequest,
	stream grpc.ServerStreamingServer[streamv1.StreamPipelineStatusResponse],
) error {
	consumer, err := s.newTopicConsumer("pipeline-status")
	if err != nil {
		return err
	}
	defer func() { _ = consumer.Close() }()
	if err := consumer.Subscribe([]string{streamStatusTopic}); err != nil {
		return err
	}

	for {
		select {
		case <-stream.Context().Done():
			return nil
		default:
			msg, err := consumer.ReadMessage(200 * time.Millisecond)
			if err != nil {
				continue
			}
			event := map[string]any{}
			if err := sonic.Unmarshal(msg.Value, &event); err != nil {
				continue
			}
			info := parseEventInfo(event)
			if !matchPipelineStreamRequest(req, info.pipelineId, getString(event, "pipelineRunId")) {
				continue
			}
			data := getMapAny(event, "data")
			runId := getString(event, "pipelineRunId")
			if runId == "" {
				runId = info.pipelineId
			}
			if err := stream.Send(&streamv1.StreamPipelineStatusResponse{
				PipelineId:     info.pipelineId,
				RunId:          runId,
				Namespace:      info.pipelineId,
				Status:         toPipelineStatus(info.status, info.eventType),
				PreviousStatus: pipelinev1.PipelineStatus_PIPELINE_STATUS_UNSPECIFIED,
				Timestamp:      time.Now().Unix(),
				TotalJobs:      int32(getInt(data, "totalJobs")),
				CompletedJobs:  int32(getInt(data, "completedJobs")),
				FailedJobs:     int32(getInt(data, "failedJobs")),
				RunningJobs:    int32(getInt(data, "runningJobs")),
				Duration:       int64(getInt(data, "duration")),
			}); err != nil {
				return err
			}
		}
	}
}

// AgentChannel Agent与Server双向通信流
func (s *StreamServiceImpl) AgentChannel(
	stream grpc.BidiStreamingServer[streamv1.AgentChannelRequest,
		streamv1.AgentChannelResponse],
) error {
	for {
		req, err := stream.Recv()
		if err != nil {
			return nil
		}
		if req.GetHeartbeat() != nil {
			if strings.TrimSpace(req.AgentId) != "" {
				_ = s.mysql.WithContext(context.Background()).
					Table((&model.Agent{}).TableName()).
					Where("agent_id = ?", req.AgentId).
					Updates(map[string]any{
						"status":         int(req.GetHeartbeat().Status),
						"last_heartbeat": time.Now(),
						"updated_at":     time.Now(),
					}).Error
			}
			if err := stream.Send(&streamv1.AgentChannelResponse{
				AgentId:    req.AgentId,
				ResponseId: req.RequestId,
				Payload: &streamv1.AgentChannelResponse_HeartbeatAck{
					HeartbeatAck: &streamv1.HeartbeatAck{
						ServerTime: time.Now().Unix(),
						Success:    true,
					},
				},
			}); err != nil {
				return err
			}
			continue
		}
		if req.GetStepRunFetch() != nil {
			stepRuns, err := s.fetchAgentStepRuns(stream.Context(), req.AgentId, int(req.GetStepRunFetch().MaxStepRuns))
			if err != nil {
				return err
			}
			if err := stream.Send(&streamv1.AgentChannelResponse{
				AgentId:    req.AgentId,
				ResponseId: req.RequestId,
				Payload: &streamv1.AgentChannelResponse_StepRunAssignment{
					StepRunAssignment: &streamv1.StepRunAssignment{
						StepRuns: stepRuns,
					},
				},
			}); err != nil {
				return err
			}
			continue
		}
		if req.GetStepRunStatus() != nil {
			update := map[string]any{
				"status":        int(req.GetStepRunStatus().Status),
				"exit_code":     req.GetStepRunStatus().ExitCode,
				"error_message": req.GetStepRunStatus().ErrorMessage,
				"updated_at":    time.Now(),
			}
			_ = s.mysql.WithContext(context.Background()).
				Table((&model.StepRun{}).TableName()).
				Where("step_run_id = ?", req.GetStepRunStatus().StepRunId).
				Updates(update).Error
			continue
		}
	}
}

// StreamAgentStatus 实时监控Agent状态流
func (s *StreamServiceImpl) StreamAgentStatus(
	req *streamv1.StreamAgentStatusRequest,
	stream grpc.ServerStreamingServer[streamv1.StreamAgentStatusResponse],
) error {
	consumer, err := s.newTopicConsumer("agent-status")
	if err != nil {
		return err
	}
	defer func() { _ = consumer.Close() }()
	if err := consumer.Subscribe([]string{streamStatusTopic}); err != nil {
		return err
	}

	for {
		select {
		case <-stream.Context().Done():
			return nil
		default:
			msg, err := consumer.ReadMessage(200 * time.Millisecond)
			if err != nil {
				continue
			}
			event := map[string]any{}
			if err := sonic.Unmarshal(msg.Value, &event); err != nil {
				continue
			}
			eventType := strings.ToLower(getString(event, "type"))
			if !strings.Contains(eventType, "agent") {
				continue
			}
			agentId := getString(event, "agentId")
			if !containsOrEmpty(req.AgentIds, agentId) {
				continue
			}
			data := getMapAny(event, "data")
			if err := stream.Send(&streamv1.StreamAgentStatusResponse{
				AgentId:               agentId,
				Hostname:              getString(event, "agentName"),
				Ip:                    getString(event, "ip"),
				Status:                toAgentStatus(getString(data, "status"), eventType),
				PreviousStatus:        streamv1.AgentStatus_AGENT_STATUS_UNSPECIFIED,
				Timestamp:             time.Now().Unix(),
				RunningStepRunsCount:  int32(getInt(data, "runningStepRunsCount")),
				MaxConcurrentStepRuns: int32(getInt(data, "maxConcurrentStepRuns")),
				LastHeartbeat:         int64(getInt(data, "lastHeartbeat")),
				Labels:                toStringMap(getMapAny(data, "labels")),
			}); err != nil {
				return err
			}
		}
	}
}

// StreamEvents 实时事件流
func (s *StreamServiceImpl) StreamEvents(
	req *streamv1.StreamEventsRequest,
	stream grpc.ServerStreamingServer[streamv1.StreamEventsResponse],
) error {
	consumer, err := s.newTopicConsumer("events")
	if err != nil {
		return err
	}
	defer func() { _ = consumer.Close() }()
	if err := consumer.Subscribe([]string{streamStatusTopic}); err != nil {
		return err
	}

	for {
		select {
		case <-stream.Context().Done():
			return nil
		default:
			msg, err := consumer.ReadMessage(200 * time.Millisecond)
			if err != nil {
				continue
			}
			event := map[string]any{}
			if err := sonic.Unmarshal(msg.Value, &event); err != nil {
				continue
			}
			eventType := toEventType(getString(event, "type"))
			if !matchEventTypeFilter(req.EventTypes, eventType) {
				continue
			}
			resourceType, resourceId := detectResource(event)
			if strings.TrimSpace(req.ResourceType) != "" && req.ResourceType != resourceType {
				continue
			}
			if len(req.ResourceIds) > 0 && !containsOrEmpty(req.ResourceIds, resourceId) {
				continue
			}
			if err := stream.Send(&streamv1.StreamEventsResponse{
				EventId:      fmt.Sprintf("evt-%d", time.Now().UnixNano()),
				EventType:    eventType,
				Timestamp:    time.Now().Unix(),
				ResourceId:   resourceId,
				ResourceType: resourceType,
				Title:        getString(event, "title"),
				Description:  getString(event, "description"),
				Metadata:     toStringMap(getMapAny(event, "data")),
				UserId:       getString(event, "userId"),
			}); err != nil {
				return err
			}
		}
	}
}

func (s *StreamServiceImpl) newTopicConsumer(suffix string) (*kafka.Consumer, error) {
	if strings.TrimSpace(s.kafkaCfg.BootstrapServers) == "" {
		return nil, fmt.Errorf("kafka bootstrap servers are not configured")
	}
	return kafka.NewConsumer(
		s.kafkaCfg.BootstrapServers,
		streamStatusTopic,
		streamConsumerGroup+"-"+suffix,
		kafka.WithConsumerClientOptions(
			kafka.WithSecurityProtocol(s.kafkaCfg.SecurityProtocol),
			kafka.WithSaslMechanism(s.kafkaCfg.Sasl.Mechanism),
			kafka.WithSaslUsername(s.kafkaCfg.Sasl.Username),
			kafka.WithSaslPassword(s.kafkaCfg.Sasl.Password),
			kafka.WithSslCaFile(s.kafkaCfg.Ssl.CaFile),
			kafka.WithSslCertFile(s.kafkaCfg.Ssl.CertFile),
			kafka.WithSslKeyFile(s.kafkaCfg.Ssl.KeyFile),
			kafka.WithSslPassword(s.kafkaCfg.Ssl.Password),
		),
		kafka.WithConsumerAutoOffsetReset("latest"),
	)
}

func matchStepRunStreamRequest(req *streamv1.StreamStepRunStatusRequest, info eventInfo) bool {
	if req == nil {
		return false
	}
	if len(req.StepRunIds) > 0 && !containsOrEmpty(req.StepRunIds, info.stepRunId) {
		return false
	}
	if strings.TrimSpace(req.PipelineId) != "" && req.PipelineId != info.pipelineId {
		return false
	}
	if strings.TrimSpace(req.JobId) != "" && req.JobId != info.jobId {
		return false
	}
	if strings.TrimSpace(req.JobName) != "" && req.JobName != info.jobName {
		return false
	}
	return info.stepRunId != ""
}

func matchJobStreamRequest(req *streamv1.StreamJobStatusRequest, info eventInfo) bool {
	if req == nil {
		return false
	}
	if len(req.JobIds) > 0 && !containsOrEmpty(req.JobIds, info.jobId) {
		return false
	}
	if strings.TrimSpace(req.PipelineId) != "" && req.PipelineId != info.pipelineId {
		return false
	}
	return info.jobId != ""
}

func matchPipelineStreamRequest(req *streamv1.StreamPipelineStatusRequest, pipelineId, pipelineRunId string) bool {
	if req == nil {
		return false
	}
	if len(req.PipelineIds) > 0 && !containsOrEmpty(req.PipelineIds, pipelineId) {
		return false
	}
	if len(req.PipelineRunIds) > 0 && !containsOrEmpty(req.PipelineRunIds, pipelineRunId) {
		return false
	}
	return strings.TrimSpace(pipelineId) != ""
}

func containsOrEmpty(list []string, value string) bool {
	if len(list) == 0 {
		return true
	}
	for _, item := range list {
		if strings.TrimSpace(item) == strings.TrimSpace(value) {
			return true
		}
	}
	return false
}

func getString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch value := v.(type) {
	case string:
		return value
	case fmt.Stringer:
		return value.String()
	default:
		return fmt.Sprintf("%v", value)
	}
}

func getMapAny(m map[string]any, key string) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	v, ok := m[key]
	if !ok || v == nil {
		return map[string]any{}
	}
	if typed, ok := v.(map[string]any); ok {
		return typed
	}
	return map[string]any{}
}

func getInt(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch value := v.(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	case float32:
		return int(value)
	default:
		return 0
	}
}

func toStringMap(m map[string]any) map[string]string {
	if len(m) == 0 {
		return map[string]string{}
	}
	resp := make(map[string]string, len(m))
	for k, v := range m {
		resp[k] = fmt.Sprintf("%v", v)
	}
	return resp
}

func toStepRunStatus(status, eventType string) steprunv1.StepRunStatus {
	s := strings.ToLower(strings.TrimSpace(status))
	e := strings.ToLower(strings.TrimSpace(eventType))
	switch {
	case s == "pending" || strings.Contains(e, "created"):
		return steprunv1.StepRunStatus_STEP_RUN_STATUS_PENDING
	case s == "queued":
		return steprunv1.StepRunStatus_STEP_RUN_STATUS_QUEUED
	case s == "running" || strings.Contains(e, "started"):
		return steprunv1.StepRunStatus_STEP_RUN_STATUS_RUNNING
	case s == "success" || s == "completed" || strings.Contains(e, "completed"):
		return steprunv1.StepRunStatus_STEP_RUN_STATUS_SUCCESS
	case s == "failed" || strings.Contains(e, "failed"):
		return steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED
	case s == "cancelled" || strings.Contains(e, "cancelled"):
		return steprunv1.StepRunStatus_STEP_RUN_STATUS_CANCELLED
	case s == "timeout":
		return steprunv1.StepRunStatus_STEP_RUN_STATUS_TIMEOUT
	case s == "skipped":
		return steprunv1.StepRunStatus_STEP_RUN_STATUS_SKIPPED
	default:
		return steprunv1.StepRunStatus_STEP_RUN_STATUS_UNSPECIFIED
	}
}

func toPipelineStatus(status, eventType string) pipelinev1.PipelineStatus {
	s := strings.ToLower(strings.TrimSpace(status))
	e := strings.ToLower(strings.TrimSpace(eventType))
	switch {
	case s == "pending" || strings.Contains(e, "created"):
		return pipelinev1.PipelineStatus_PIPELINE_STATUS_PENDING
	case s == "running" || strings.Contains(e, "started"):
		return pipelinev1.PipelineStatus_PIPELINE_STATUS_RUNNING
	case s == "success" || s == "completed" || strings.Contains(e, "completed"):
		return pipelinev1.PipelineStatus_PIPELINE_STATUS_SUCCESS
	case s == "failed" || strings.Contains(e, "failed"):
		return pipelinev1.PipelineStatus_PIPELINE_STATUS_FAILED
	case s == "cancelled" || strings.Contains(e, "cancelled"):
		return pipelinev1.PipelineStatus_PIPELINE_STATUS_CANCELLED
	default:
		return pipelinev1.PipelineStatus_PIPELINE_STATUS_UNSPECIFIED
	}
}

func toAgentStatus(status, eventType string) streamv1.AgentStatus {
	s := strings.ToLower(strings.TrimSpace(status))
	e := strings.ToLower(strings.TrimSpace(eventType))
	switch {
	case s == "online" || strings.Contains(e, "registered"):
		return streamv1.AgentStatus_AGENT_STATUS_ONLINE
	case s == "offline" || strings.Contains(e, "offline") || strings.Contains(e, "unregistered"):
		return streamv1.AgentStatus_AGENT_STATUS_OFFLINE
	case s == "busy":
		return streamv1.AgentStatus_AGENT_STATUS_BUSY
	case s == "idle":
		return streamv1.AgentStatus_AGENT_STATUS_IDLE
	default:
		return streamv1.AgentStatus_AGENT_STATUS_UNSPECIFIED
	}
}

func toEventType(eventType string) streamv1.EventType {
	value := strings.ToLower(strings.TrimSpace(eventType))
	switch {
	case strings.Contains(value, "steprun.created"):
		return streamv1.EventType_EVENT_TYPE_STEP_RUN_CREATED
	case strings.Contains(value, "steprun.started"):
		return streamv1.EventType_EVENT_TYPE_STEP_RUN_STARTED
	case strings.Contains(value, "steprun.completed"):
		return streamv1.EventType_EVENT_TYPE_STEP_RUN_COMPLETED
	case strings.Contains(value, "steprun.failed"):
		return streamv1.EventType_EVENT_TYPE_STEP_RUN_FAILED
	case strings.Contains(value, "steprun.cancelled"):
		return streamv1.EventType_EVENT_TYPE_STEP_RUN_CANCELLED
	case strings.Contains(value, "job.started"):
		return streamv1.EventType_EVENT_TYPE_JOB_STARTED
	case strings.Contains(value, "job.completed"):
		return streamv1.EventType_EVENT_TYPE_JOB_COMPLETED
	case strings.Contains(value, "job.failed"):
		return streamv1.EventType_EVENT_TYPE_JOB_FAILED
	case strings.Contains(value, "job.cancelled"):
		return streamv1.EventType_EVENT_TYPE_JOB_CANCELLED
	case strings.Contains(value, "pipeline.started"):
		return streamv1.EventType_EVENT_TYPE_PIPELINE_STARTED
	case strings.Contains(value, "pipeline.completed"):
		return streamv1.EventType_EVENT_TYPE_PIPELINE_COMPLETED
	case strings.Contains(value, "pipeline.failed"):
		return streamv1.EventType_EVENT_TYPE_PIPELINE_FAILED
	case strings.Contains(value, "pipeline.cancelled"):
		return streamv1.EventType_EVENT_TYPE_PIPELINE_CANCELLED
	case strings.Contains(value, "agent.registered"):
		return streamv1.EventType_EVENT_TYPE_AGENT_REGISTERED
	case strings.Contains(value, "agent.unregistered"):
		return streamv1.EventType_EVENT_TYPE_AGENT_UNREGISTERED
	case strings.Contains(value, "agent.offline"):
		return streamv1.EventType_EVENT_TYPE_AGENT_OFFLINE
	default:
		return streamv1.EventType_EVENT_TYPE_UNSPECIFIED
	}
}

func matchEventTypeFilter(filters []streamv1.EventType, eventType streamv1.EventType) bool {
	if len(filters) == 0 {
		return true
	}
	for _, one := range filters {
		if one == eventType {
			return true
		}
	}
	return false
}

func detectResource(event map[string]any) (resourceType, resourceId string) {
	info := parseEventInfo(event)
	if info.stepRunId != "" {
		return "step_run", info.stepRunId
	}
	if info.jobId != "" {
		return "job", info.jobId
	}
	if info.pipelineId != "" {
		return "pipeline", info.pipelineId
	}
	if agentId := getString(event, "agentId"); agentId != "" {
		return "agent", agentId
	}
	return "", ""
}

func (s *StreamServiceImpl) fetchAgentStepRuns(ctx context.Context, agentId string, max int) ([]*agentv1.StepRun, error) {
	if max <= 0 {
		max = 1
	}
	var records []model.StepRun
	query := s.mysql.WithContext(ctx).
		Table((&model.StepRun{}).TableName()).
		Where("agent_id = ?", strings.TrimSpace(agentId)).
		Where("status IN ?", []int{1, 2}).
		Order("created_at ASC").
		Limit(max)
	if err := query.Find(&records).Error; err != nil {
		return nil, err
	}
	resp := make([]*agentv1.StepRun, 0, len(records))
	for i := range records {
		resp = append(resp, convertStepRunModelToAgentStepRun(&records[i]))
	}
	return resp, nil
}

func (s *StreamServiceImpl) queryAgents(ctx context.Context, agentIds []string) ([]model.Agent, error) {
	var agents []model.Agent
	query := s.mysql.WithContext(ctx).Table((&model.Agent{}).TableName())
	if len(agentIds) > 0 {
		query = query.Where("agent_id IN ?", agentIds)
	}
	if err := query.Find(&agents).Error; err != nil {
		return nil, err
	}
	return agents, nil
}
