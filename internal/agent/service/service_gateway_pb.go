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
	"github.com/arcentrix/arcentra/pkg/outbox"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GatewayServiceImpl implements outbox.Sender by calling gateway.v1.GatewayServiceImpl.PushEvents.
type GatewayServiceImpl struct {
	client     gatewayv1.GatewayServiceClient
	agentId    string
	pipelineId string
}

// NewGatewayServiceImpl creates a GatewayServiceClientImpl for the given agent.
func NewGatewayServiceImpl(cc grpc.ClientConnInterface, agentId, pipelineId string) *GatewayServiceImpl {
	return &GatewayServiceImpl{
		client:     gatewayv1.NewGatewayServiceClient(cc),
		agentId:    agentId,
		pipelineId: pipelineId,
	}
}

// Send streams events to the Gateway via PushEvents and returns the ACK result.
func (s *GatewayServiceImpl) Send(ctx context.Context, events []outbox.Event) (outbox.SendResult, error) {
	if len(events) == 0 {
		return outbox.SendResult{}, nil
	}
	stream, err := s.client.PushEvents(ctx)
	if err != nil {
		return outbox.SendResult{}, fmt.Errorf("push events stream: %w", err)
	}
	handshake := &gatewayv1.PushEventsRequest{
		Payload: &gatewayv1.PushEventsRequest_Handshake{
			Handshake: &gatewayv1.AgentHandshake{
				AgentId:      s.agentId,
				LastKnownSeq: 0,
			},
		},
	}
	if err := stream.Send(handshake); err != nil {
		return outbox.SendResult{}, fmt.Errorf("send handshake: %w", err)
	}
	batch := &gatewayv1.EventBatch{
		BatchId: fmt.Sprintf("batch-%d", events[0].Seq),
		Events:  make([]*gatewayv1.Event, 0, len(events)),
	}
	for _, e := range events {
		payload, err := structpb.NewStruct(e.Payload)
		if err != nil {
			payload, _ = structpb.NewStruct(map[string]any{})
		}
		batch.Events = append(batch.Events, &gatewayv1.Event{
			EventId:   e.EventId,
			EventType: e.EventType,
			Payload:   payload,
			Meta: &gatewayv1.Meta{
				AgentId:    s.agentId,
				PipelineId: s.pipelineId,
				StepId:     e.StepId,
				Timestamp:  timestamppb.Now(),
				Seq:        e.Seq,
			},
		})
	}
	req := &gatewayv1.PushEventsRequest{
		Payload: &gatewayv1.PushEventsRequest_Batch{Batch: batch},
	}
	if err := stream.Send(req); err != nil {
		return outbox.SendResult{}, fmt.Errorf("send batch: %w", err)
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		return outbox.SendResult{}, fmt.Errorf("close and recv: %w", err)
	}
	return parsePushEventsResponse(resp)
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
