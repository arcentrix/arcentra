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
	"io"

	gatewayv1 "github.com/arcentrix/arcentra/api/gateway/v1"
	"github.com/arcentrix/arcentra/pkg/log"
	"google.golang.org/grpc"
)

// GatewayServiceImpl implements gateway.v1.GatewayServiceServer.
type GatewayServiceImpl struct {
	gatewayv1.UnimplementedGatewayServiceServer
}

// NewGatewayServiceImpl creates a GatewayServiceImpl instance.
func NewGatewayServiceImpl() *GatewayServiceImpl {
	return &GatewayServiceImpl{}
}

// PushLogs receives log stream from agent.
func (s *GatewayServiceImpl) PushLogs(stream grpc.ClientStreamingServer[gatewayv1.PushLogsRequest, gatewayv1.PushLogsResponse]) error {
	var lastSeq uint64
	for {
		req, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				return stream.SendAndClose(&gatewayv1.PushLogsResponse{
					Ack: &gatewayv1.IngestAck{
						Status:          gatewayv1.IngestAck_STATUS_ACCEPTED,
						LastAcceptedSeq: lastSeq,
					},
				})
			}
			return err
		}
		switch p := req.Payload.(type) {
		case *gatewayv1.PushLogsRequest_Handshake:
			log.Debugw("PushLogs handshake", "agent_id", p.Handshake.AgentId, "last_known_seq", p.Handshake.LastKnownSeq)
		case *gatewayv1.PushLogsRequest_Batch:
			if p.Batch != nil && p.Batch.Meta != nil {
				if p.Batch.Meta.Seq > lastSeq {
					lastSeq = p.Batch.Meta.Seq
				}
				log.Debugw("PushLogs batch received", "batch_id", p.Batch.BatchId, "agent_id", p.Batch.Meta.AgentId)
			}
		}
	}
}

// PushEvents receives event stream from agent.
func (s *GatewayServiceImpl) PushEvents(stream grpc.ClientStreamingServer[gatewayv1.PushEventsRequest, gatewayv1.PushEventsResponse]) error {
	var lastAcceptedSeq uint64
	var agentId string

	for {
		req, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				return stream.SendAndClose(&gatewayv1.PushEventsResponse{
					Ack: &gatewayv1.IngestAck{
						Status:          gatewayv1.IngestAck_STATUS_ACCEPTED,
						LastAcceptedSeq: lastAcceptedSeq,
					},
				})
			}
			return err
		}
		switch p := req.Payload.(type) {
		case *gatewayv1.PushEventsRequest_Handshake:
			agentId = p.Handshake.AgentId
			lastAcceptedSeq = p.Handshake.LastKnownSeq
			log.Debugw("PushEvents handshake", "agent_id", agentId, "last_known_seq", lastAcceptedSeq)
		case *gatewayv1.PushEventsRequest_Batch:
			if p.Batch == nil || len(p.Batch.Events) == 0 {
				continue
			}
			for _, e := range p.Batch.Events {
				if e.Meta != nil {
					if e.Meta.Seq > lastAcceptedSeq {
						lastAcceptedSeq = e.Meta.Seq
					}
					log.Debugw("PushEvents event received",
						"event_id", e.EventId,
						"agent_id", agentId,
						"seq", e.Meta.Seq,
						"event_type", e.EventType)
				}
			}
		}
	}
}
