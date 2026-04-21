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

package executor

import (
	"context"
	"errors"

	"github.com/arcentrix/arcentra/pkg/log"
)

// EventPublisher publishes CloudEvents to a transport.
type EventPublisher interface {
	Publish(ctx context.Context, event map[string]any) error
	Close() error
}

// LogEventPublisher is a fallback publisher that writes events to structured
// logs when Kafka (or other transport) is not configured.
type LogEventPublisher struct{}

// Publish logs the event at debug level.
func (l *LogEventPublisher) Publish(_ context.Context, event map[string]any) error {
	log.Debugw("cloud event (no transport)", "event", event)
	return nil
}

// Close is a no-op.
func (l *LogEventPublisher) Close() error { return nil }

// MultiPublisher publishes events to multiple publishers.
type MultiPublisher struct {
	publishers []EventPublisher
}

// NewMultiPublisher creates a MultiPublisher.
func NewMultiPublisher(publishers ...EventPublisher) *MultiPublisher {
	filtered := make([]EventPublisher, 0, len(publishers))
	for _, publisher := range publishers {
		if publisher != nil {
			filtered = append(filtered, publisher)
		}
	}
	return &MultiPublisher{publishers: filtered}
}

// Publish publishes to all publishers.
func (m *MultiPublisher) Publish(ctx context.Context, event map[string]any) error {
	if m == nil || len(m.publishers) == 0 {
		return nil
	}
	var errs []error
	for _, publisher := range m.publishers {
		if err := publisher.Publish(ctx, event); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Close closes all publishers.
func (m *MultiPublisher) Close() error {
	if m == nil || len(m.publishers) == 0 {
		return nil
	}
	var errs []error
	for _, publisher := range m.publishers {
		if err := publisher.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
