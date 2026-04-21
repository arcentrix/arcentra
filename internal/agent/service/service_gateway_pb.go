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
	"fmt"

	gatewayv1 "github.com/arcentrix/arcentra/api/gateway/v1"
	grpcclient "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/pkg/outbox"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GatewayServiceImpl implements outbox.Sender by calling gateway.v1.GatewayServiceImpl.PushEvents.
type GatewayServiceImpl struct {
	client     gatewayv1.GatewayServiceClient
	agentID    string
	pipelineID string
}

// NewGatewayServiceImpl creates a GatewayServiceClientImpl for the given agent (conn must be non-nil).
func NewGatewayServiceImpl(cc grpc.ClientConnInterface, agentID, pipelineID string) *GatewayServiceImpl {
	return &GatewayServiceImpl{
		client:     gatewayv1.NewGatewayServiceClient(cc),
		agentID:    agentID,
		pipelineID: pipelineID,
	}
}

// GatewaySenderFromWrapper implements outbox.Sender by obtaining the connection at Send time.
// Use this when the gRPC connection may not be ready at construction time (e.g. Wire init).
type GatewaySenderFromWrapper struct {
	wrapper    *grpcclient.ClientWrapper
	agentID    string
	pipelineID string
}

// NewGatewaySenderFromWrapper creates an outbox.Sender that uses wrapper.GetConn() on each Send.
func NewGatewaySenderFromWrapper(wrapper *grpcclient.ClientWrapper, agentID, pipelineID string) *GatewaySenderFromWrapper {
	return &GatewaySenderFromWrapper{
		wrapper:    wrapper,
		agentID:    agentID,
		pipelineID: pipelineID,
	}
}

// Send streams events to the Gateway via PushEvents and returns the ACK result.
// lastKnownSeq is the last locally committed seq; sent in handshake so server can align (resume from last_acked+1 on reconnect).
func (s *GatewayServiceImpl) Send(ctx context.Context, lastKnownSeq uint64, events []outbox.Event) (outbox.SendResult, error) {
	return s.send(ctx, lastKnownSeq, events, s.client)
}

func (s *GatewayServiceImpl) send(
	ctx context.Context,
	lastKnownSeq uint64,
	events []outbox.Event,
	client gatewayv1.GatewayServiceClient,
) (outbox.SendResult, error) {
	if len(events) == 0 {
		return outbox.SendResult{}, nil
	}
	if client == nil {
		return outbox.SendResult{}, fmt.Errorf("gateway client not ready")
	}
	stream, err := client.PushEvents(ctx)
	if err != nil {
		return outbox.SendResult{}, fmt.Errorf("push events stream: %w", err)
	}
	handshake := &gatewayv1.PushEventsRequest{
		Payload: &gatewayv1.PushEventsRequest_Handshake{
			Handshake: &gatewayv1.AgentHandshake{
				AgentId:      s.agentID,
				LastKnownSeq: lastKnownSeq,
			},
		},
	}
	if sendErr := stream.Send(handshake); sendErr != nil {
		return outbox.SendResult{}, fmt.Errorf("send handshake: %w", sendErr)
	}
	batch := buildEventBatch(events, s.agentID, s.pipelineID)
	req := &gatewayv1.PushEventsRequest{
		Payload: &gatewayv1.PushEventsRequest_Batch{Batch: batch},
	}
	if sendErr := stream.Send(req); sendErr != nil {
		return outbox.SendResult{}, fmt.Errorf("send batch: %w", sendErr)
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		return outbox.SendResult{}, fmt.Errorf("close and recv: %w", err)
	}
	return parsePushEventsResponse(resp)
}

// Send implements outbox.Sender; obtains conn from wrapper at call time.
func (s *GatewaySenderFromWrapper) Send(ctx context.Context, lastKnownSeq uint64, events []outbox.Event) (outbox.SendResult, error) {
	if s.wrapper == nil {
		return outbox.SendResult{}, fmt.Errorf("grpc wrapper is nil")
	}
	conn := s.wrapper.GetConn()
	if conn == nil {
		return outbox.SendResult{}, fmt.Errorf("grpc not connected")
	}
	client := gatewayv1.NewGatewayServiceClient(conn)
	return s.sendWithClient(ctx, lastKnownSeq, events, client)
}

func (s *GatewaySenderFromWrapper) sendWithClient(
	ctx context.Context,
	lastKnownSeq uint64,
	events []outbox.Event,
	client gatewayv1.GatewayServiceClient,
) (outbox.SendResult, error) {
	if len(events) == 0 {
		return outbox.SendResult{}, nil
	}
	stream, err := client.PushEvents(ctx)
	if err != nil {
		return outbox.SendResult{}, fmt.Errorf("push events stream: %w", err)
	}
	handshake := &gatewayv1.PushEventsRequest{
		Payload: &gatewayv1.PushEventsRequest_Handshake{
			Handshake: &gatewayv1.AgentHandshake{
				AgentId:      s.agentID,
				LastKnownSeq: lastKnownSeq,
			},
		},
	}
	if sendErr := stream.Send(handshake); sendErr != nil {
		return outbox.SendResult{}, fmt.Errorf("send handshake: %w", sendErr)
	}
	batch := buildEventBatch(events, s.agentID, s.pipelineID)
	req := &gatewayv1.PushEventsRequest{
		Payload: &gatewayv1.PushEventsRequest_Batch{Batch: batch},
	}
	if sendErr := stream.Send(req); sendErr != nil {
		return outbox.SendResult{}, fmt.Errorf("send batch: %w", sendErr)
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		return outbox.SendResult{}, fmt.Errorf("close and recv: %w", err)
	}
	return parsePushEventsResponse(resp)
}

func buildEventBatch(events []outbox.Event, agentID, pipelineID string) *gatewayv1.EventBatch {
	if len(events) == 0 {
		return nil
	}
	batch := &gatewayv1.EventBatch{
		BatchId: fmt.Sprintf("batch-%d", events[0].Seq),
		Events:  make([]*gatewayv1.Event, 0, len(events)),
	}
	for _, e := range events {
		payload, _ := structpb.NewStruct(e.Payload)
		if payload == nil {
			payload, _ = structpb.NewStruct(map[string]any{})
		}
		batch.Events = append(batch.Events, &gatewayv1.Event{
			EventId:   e.EventID,
			EventType: e.EventType,
			Payload:   payload,
			Meta: &gatewayv1.Meta{
				AgentId:    agentID,
				PipelineId: pipelineID,
				StepId:     e.StepID,
				Timestamp:  timestamppb.Now(),
				Seq:        e.Seq,
			},
		})
	}
	return batch
}

func parsePushEventsResponse(resp *gatewayv1.PushEventsResponse) (outbox.SendResult, error) {
	if resp == nil || resp.Ack == nil {
		return outbox.SendResult{}, nil
	}
	ack := resp.Ack
	return outbox.SendResult{
		LastSeq:     ack.LastAcceptedSeq,
		ExpectedSeq: ack.ExpectedSeq,
		RejectedSeq: append([]uint64(nil), ack.RejectedSeqs...),
	}, nil
}
