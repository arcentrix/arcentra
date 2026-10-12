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
	"google.golang.org/protobuf/proto"
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
	return sendGatewayEvents(ctx, client, s.agentID, s.pipelineID, lastKnownSeq, events)
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
	authCtx, cancel := s.wrapper.WithTimeoutAndAuth(ctx)
	defer cancel()
	return s.sendWithClient(authCtx, lastKnownSeq, events, client)
}

func (s *GatewaySenderFromWrapper) sendWithClient(
	ctx context.Context,
	lastKnownSeq uint64,
	events []outbox.Event,
	client gatewayv1.GatewayServiceClient,
) (outbox.SendResult, error) {
	return sendGatewayEvents(ctx, client, s.agentID, s.pipelineID, lastKnownSeq, events)
}

func sendGatewayEvents(ctx context.Context, client gatewayv1.GatewayServiceClient, agentID, pipelineID string, lastKnownSeq uint64, events []outbox.Event) (outbox.SendResult, error) {
	if len(events) == 0 {
		return outbox.SendResult{LastSeq: lastKnownSeq}, nil
	}
	if client == nil {
		return outbox.SendResult{}, fmt.Errorf("gateway client not ready")
	}
	batches, err := buildEventBatches(events, agentID, pipelineID)
	if err != nil {
		return outbox.SendResult{}, err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := client.PushEvents(streamCtx)
	if err != nil {
		return outbox.SendResult{}, fmt.Errorf("push events stream: %w", err)
	}
	handshake := &gatewayv1.PushEventsRequest{Payload: &gatewayv1.PushEventsRequest_Handshake{
		Handshake: &gatewayv1.AgentHandshake{AgentId: agentID, LastKnownSeq: lastKnownSeq},
	}}
	if err := stream.Send(handshake); err != nil {
		return outbox.SendResult{}, fmt.Errorf("send handshake: %w", err)
	}
	for _, batch := range batches {
		if err := stream.Send(batch); err != nil {
			return outbox.SendResult{}, fmt.Errorf("send event batch: %w", err)
		}
	}
	response, err := stream.CloseAndRecv()
	if err != nil {
		return outbox.SendResult{}, fmt.Errorf("close and recv: %w", err)
	}
	result, err := parsePushEventsResponse(response)
	if err != nil {
		return outbox.SendResult{}, err
	}
	if result.LastSeq < lastKnownSeq {
		return outbox.SendResult{}, fmt.Errorf("gateway checkpoint regressed")
	}
	// A global server cursor may include other streams. Never discard local records
	// that were not sent and checked for conflicts in this stream.
	if lastSent := events[len(events)-1].Seq; result.LastSeq > lastSent {
		result.LastSeq = lastSent
	}
	return result, nil
}

func buildEventBatches(events []outbox.Event, agentID, pipelineID string) ([]*gatewayv1.PushEventsRequest, error) {
	batches := make([]*gatewayv1.PushEventsRequest, 0)
	var batch *gatewayv1.EventBatch
	var request *gatewayv1.PushEventsRequest
	for _, event := range events {
		if err := outbox.ValidateEventPayload(event.Payload); err != nil {
			return nil, fmt.Errorf("event %d: %w", event.Seq, err)
		}
		payload, err := structpb.NewStruct(event.Payload)
		if err != nil {
			return nil, fmt.Errorf("event %d payload: %w", event.Seq, err)
		}
		value := &gatewayv1.Event{EventId: event.EventID, EventType: event.EventType, Payload: payload,
			Meta: &gatewayv1.Meta{AgentId: agentID, PipelineId: eventPipelineID(event.Payload, pipelineID),
				StepId: event.StepID, Timestamp: timestamppb.Now(), Seq: event.Seq},
		}
		if batch == nil {
			batch = &gatewayv1.EventBatch{BatchId: fmt.Sprintf("batch-%d", event.Seq)}
			request = &gatewayv1.PushEventsRequest{Payload: &gatewayv1.PushEventsRequest_Batch{Batch: batch}}
		}
		batch.Events = append(batch.Events, value)
		if len(batch.Events) > outbox.MaxGatewayBatchEvents || proto.Size(request) > outbox.MaxGatewayMessageBytes {
			batch.Events = batch.Events[:len(batch.Events)-1]
			if len(batch.Events) == 0 {
				return nil, fmt.Errorf("event %d: %w", event.Seq, outbox.ErrEventPayloadTooLarge)
			}
			batches = append(batches, request)
			batch = &gatewayv1.EventBatch{BatchId: fmt.Sprintf("batch-%d", event.Seq), Events: []*gatewayv1.Event{value}}
			request = &gatewayv1.PushEventsRequest{Payload: &gatewayv1.PushEventsRequest_Batch{Batch: batch}}
			if proto.Size(request) > outbox.MaxGatewayMessageBytes {
				return nil, fmt.Errorf("event %d: %w", event.Seq, outbox.ErrEventPayloadTooLarge)
			}
		}
	}
	if batch != nil {
		batches = append(batches, request)
	}
	return batches, nil
}

func parsePushEventsResponse(resp *gatewayv1.PushEventsResponse) (outbox.SendResult, error) {
	if resp == nil || resp.Ack == nil {
		return outbox.SendResult{}, fmt.Errorf("gateway response is missing an acknowledgment")
	}
	ack := resp.Ack
	if ack.Status != gatewayv1.IngestAck_STATUS_ACCEPTED && ack.Status != gatewayv1.IngestAck_STATUS_PARTIAL_ACCEPTED {
		return outbox.SendResult{}, fmt.Errorf("gateway rejected events: %s", ack.Status.String())
	}
	return outbox.SendResult{
		LastSeq:     ack.LastAcceptedSeq,
		ExpectedSeq: ack.ExpectedSeq,
		RejectedSeq: append([]uint64(nil), ack.RejectedSeqs...),
	}, nil
}

func eventPipelineID(payload map[string]any, fallback string) string {
	if value, ok := payload["pipelineId"].(string); ok && value != "" {
		return value
	}
	return fallback
}
