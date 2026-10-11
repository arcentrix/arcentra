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

package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	gatewayv1 "github.com/arcentrix/arcentra/api/gateway/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"github.com/arcentrix/arcentra/pkg/outbox"
	"github.com/bytedance/sonic"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	maxGatewayBatchEvents  = outbox.MaxGatewayBatchEvents
	maxGatewayPayloadBytes = outbox.MaxEventPayloadBytes
)

var gatewayJSON = sonic.Config{SortMapKeys: true}.Froze()

// GatewayServiceImpl 接收经过认证的 Agent 事件，并在持久化完成后确认
// 事件载荷仅作为不可信原始数据保存，不据此执行命令或改变流水线状态
type GatewayServiceImpl struct {
	gatewayv1.UnimplementedGatewayServiceServer
	events repo.IGatewayEventRepository
}

// NewGatewayServiceImpl 创建使用持久化仓储的网关服务
func NewGatewayServiceImpl(events repo.IGatewayEventRepository) *GatewayServiceImpl {
	return &GatewayServiceImpl{events: events}
}

// PushLogs 明确拒绝尚未支持的网关日志格式，避免丢弃数据后错误确认
func (s *GatewayServiceImpl) PushLogs(stream grpc.ClientStreamingServer[gatewayv1.PushLogsRequest, gatewayv1.PushLogsResponse]) error {
	if _, err := gatewayAgentID(stream.Context()); err != nil {
		return err
	}
	return status.Error(codes.Unimplemented, "gateway log ingestion is not supported; use the step log stream")
}

// PushEvents 校验握手和逐事件身份，以数据库连续游标确认可重试的事件批次
func (s *GatewayServiceImpl) PushEvents(stream grpc.ClientStreamingServer[gatewayv1.PushEventsRequest, gatewayv1.PushEventsResponse]) error {
	ctx := stream.Context()
	agentID, err := gatewayAgentID(ctx)
	if err != nil {
		return err
	}
	if s.events == nil {
		return status.Error(codes.Unavailable, "gateway event storage is not configured")
	}
	handshakeReceived := false
	var lastAcceptedSeq uint64
	for {
		req, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			if !handshakeReceived {
				return status.Error(codes.InvalidArgument, "an agent handshake is required")
			}
			return stream.SendAndClose(&gatewayv1.PushEventsResponse{Ack: &gatewayv1.IngestAck{
				Status:          gatewayv1.IngestAck_STATUS_ACCEPTED,
				LastAcceptedSeq: lastAcceptedSeq,
				ExpectedSeq:     lastAcceptedSeq + 1,
			}})
		}
		if err != nil {
			return err
		}
		if req == nil {
			return status.Error(codes.InvalidArgument, "empty gateway request")
		}
		switch p := req.Payload.(type) {
		case *gatewayv1.PushEventsRequest_Handshake:
			if p == nil || p.Handshake == nil || handshakeReceived {
				return status.Error(codes.InvalidArgument, "exactly one initial handshake is required")
			}
			if p.Handshake.AgentId != agentID {
				return status.Error(codes.PermissionDenied, "handshake agent does not match authenticated identity")
			}
			lastAcceptedSeq, err = s.events.LastAcceptedSeq(ctx, agentID)
			if err != nil {
				return gatewayStorageError(err)
			}
			if p.Handshake.LastKnownSeq > lastAcceptedSeq {
				return status.Error(codes.FailedPrecondition, "client checkpoint is ahead of durable gateway storage")
			}
			handshakeReceived = true
		case *gatewayv1.PushEventsRequest_Batch:
			if !handshakeReceived {
				return status.Error(codes.InvalidArgument, "an agent handshake must precede events")
			}
			if p == nil || p.Batch == nil || len(p.Batch.Events) == 0 {
				return status.Error(codes.InvalidArgument, "an event batch must not be empty")
			}
			if len(p.Batch.Events) > maxGatewayBatchEvents {
				return status.Error(codes.ResourceExhausted, "event batch is too large")
			}
			// Validate the entire message before persisting any of its events.
			entries := make([]*model.GatewayEvent, 0, len(p.Batch.Events))
			for _, event := range p.Batch.Events {
				entry, err := gatewayEventRecord(agentID, event)
				if err != nil {
					return err
				}
				entries = append(entries, entry)
			}
			lastAcceptedSeq, err = s.events.StoreBatch(ctx, agentID, entries)
			if err != nil {
				return gatewayStorageError(err)
			}
		default:
			return status.Error(codes.InvalidArgument, "gateway request has no supported payload")
		}
	}
}

func gatewayAgentID(ctx context.Context) (string, error) {
	identity, ok := agentidentity.FromContext(ctx)
	if !ok || identity == nil || strings.TrimSpace(identity.ID) == "" || len(identity.ID) > 64 {
		return "", status.Error(codes.Unauthenticated, "authenticated agent identity is required")
	}
	return identity.ID, nil
}

func gatewayEventRecord(agentID string, event *gatewayv1.Event) (*model.GatewayEvent, error) {
	if event == nil || event.Meta == nil {
		return nil, status.Error(codes.InvalidArgument, "event metadata is required")
	}
	if event.Meta.AgentId != agentID {
		return nil, status.Error(codes.PermissionDenied, "event agent does not match authenticated identity")
	}
	if event.Meta.Seq == 0 || event.Meta.Seq > math.MaxInt64 ||
		strings.TrimSpace(event.EventId) == "" || len(event.EventId) > 128 ||
		strings.TrimSpace(event.EventType) == "" || len(event.EventType) > 256 ||
		len(event.Meta.PipelineId) > 64 || len(event.Meta.StepId) > 64 || event.Payload == nil {
		return nil, status.Error(codes.InvalidArgument, "event ID, type, payload and a valid sequence are required")
	}
	var timestamp string
	if event.Meta.Timestamp != nil {
		if err := event.Meta.Timestamp.CheckValid(); err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid event timestamp")
		}
		timestamp = event.Meta.Timestamp.AsTime().UTC().Format(time.RFC3339Nano)
	}
	payload, err := gatewayJSON.Marshal(event.Payload.AsMap())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "event payload is not valid JSON")
	}
	if len(payload) > maxGatewayPayloadBytes {
		return nil, status.Error(codes.ResourceExhausted, "event payload is too large")
	}
	// Transport timestamps change on sender retries, so only stable content identifies a replay.
	content, err := gatewayJSON.Marshal([]any{event.EventId, event.EventType, event.Meta.PipelineId, event.Meta.StepId, string(payload)})
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid event content")
	}
	return &model.GatewayEvent{
		AgentID: agentID, Seq: event.Meta.Seq, EventID: event.EventId, EventType: event.EventType,
		PipelineID: event.Meta.PipelineId, StepID: event.Meta.StepId, Timestamp: timestamp,
		Payload: string(payload), Fingerprint: fmt.Sprintf("%x", sha256.Sum256(content)),
	}, nil
}

func gatewayStorageError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "event ingestion canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "event ingestion timed out")
	case errors.Is(err, repo.ErrGatewayEventSequence):
		return status.Error(codes.FailedPrecondition, "events must continue the durable per-agent sequence")
	case errors.Is(err, repo.ErrGatewayEventConflict):
		return status.Error(codes.AlreadyExists, "event sequence or ID conflicts with stored content")
	default:
		return status.Error(codes.Unavailable, "event storage failed; retry without advancing the checkpoint")
	}
}
