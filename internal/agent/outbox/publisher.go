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

	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/pkg/message/outbox"
)

// Publisher implements executor.EventPublisher by appending events to the local outbox.
// Events are written to WAL first and sent by the outbox sender loop (reliable, resume from last_acked+1).
type Publisher struct {
	o *outbox.Outbox
}

// NewPublisher returns an EventPublisher that appends to the given outbox.
// Returns nil if o is nil (caller may use this when outbox is disabled).
func NewPublisher(o *outbox.Outbox) executor.EventPublisher {
	if o == nil {
		return nil
	}
	return &Publisher{o: o}
}

// Publish appends the event to the local outbox WAL.
func (p *Publisher) Publish(ctx context.Context, event map[string]any) error {
	if p == nil || p.o == nil {
		return nil
	}
	_, err := p.o.AppendMap(ctx, event)
	return err
}

// Close is a no-op; the outbox is closed by the agent bootstrap.
func (p *Publisher) Close() error {
	return nil
}
