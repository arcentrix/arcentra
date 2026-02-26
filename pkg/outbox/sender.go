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

package outbox

import (
	"context"
	"fmt"

	"github.com/bytedance/sonic"
)

// Event represents an outbox event for sending.
type Event struct {
	Seq        uint64
	EventId    string
	EventType  string
	Payload    map[string]any
	AgentId    string
	PipelineId string
	StepId     string
}

// SendResult holds the result of a send operation with continuous ACK semantics.
type SendResult struct {
	// LastSeq is the last continuously acknowledged seq.
	LastSeq uint64
	// ExpectedSeq is the next expected seq when partial accepted.
	ExpectedSeq uint64
	// RejectedSeq lists rejected seqs that need retry.
	RejectedSeq []uint64
}

// Sender sends events to the Gateway.
// Implementations (e.g. gateway.GrpcSender) live in the service layer.
type Sender interface {
	Send(ctx context.Context, events []Event) (SendResult, error)
}

// RecordToEvent converts a WAL Record to Event for sending.
func RecordToEvent(r *Record, agentId, pipelineId string) (Event, error) {
	var payload map[string]any
	if r.Codec == CodecJSON {
		if err := sonic.Unmarshal(r.Payload, &payload); err != nil {
			return Event{}, err
		}
	}
	if payload == nil {
		payload = make(map[string]any)
	}
	return Event{
		Seq:        r.Seq,
		EventId:    fmt.Sprintf("evt-%d", r.Seq),
		EventType:  "outbox",
		Payload:    payload,
		AgentId:    agentId,
		PipelineId: pipelineId,
		StepId:     "",
	}, nil
}
