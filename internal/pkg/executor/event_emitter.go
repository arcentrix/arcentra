// Copyright 2025 Arcentra Team
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
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/plugin"
	"github.com/arcentrix/arcentra/pkg/safe"
)

const defaultEventSourcePrefix = "urn:arcentra:executor"

// EventEmitterConfig defines CloudEvents emitter configuration.
type EventEmitterConfig struct {
	SourcePrefix   string
	PublishTimeout time.Duration
}

// EventEmitter emits CloudEvents through a publisher.
type EventEmitter struct {
	publisher EventPublisher
	config    EventEmitterConfig
}

// NewEventEmitter creates a new EventEmitter.
func NewEventEmitter(publisher EventPublisher, config EventEmitterConfig) *EventEmitter {
	return &EventEmitter{
		publisher: publisher,
		config:    config,
	}
}

// Emit sends a CloudEvent without blocking execution.
func (e *EventEmitter) Emit(ctx context.Context, eventType, source, subject string, data map[string]any, extensions map[string]any) {
	if e == nil || e.publisher == nil {
		return
	}

	event := plugin.NewCloudEvent(
		eventType,
		source,
		data,
		plugin.WithCloudEventSubject(subject),
		plugin.WithCloudEventExtensions(extensions),
	)

	timeout := e.config.PublishTimeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}

	safe.Go(func() {
		publishCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := e.publisher.Publish(publishCtx, event.ToMap()); err != nil {
			log.Warnw("publish event failed", "type", eventType, "error", err)
		}
	})
}

// BuildSource builds the CloudEvent source value.
func (e *EventEmitter) BuildSource(executorName string) string {
	prefix := e.config.SourcePrefix
	if prefix == "" {
		prefix = defaultEventSourcePrefix
	}
	if executorName == "" {
		return prefix
	}
	return prefix + ":" + executorName
}
