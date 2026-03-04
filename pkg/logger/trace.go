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

package logger

import (
	"context"
	"log/slog"

	tracectx "github.com/arcentrix/arcentra/pkg/trace/context"
	"go.opentelemetry.io/otel/trace"
)

// logTrace injects trace fields into log records.
type logTrace struct {
	next slog.Handler
}

// newLogTrace creates a handler wrapper that enriches logs with trace metadata.
func newLogTrace(next slog.Handler) slog.Handler {
	return &logTrace{next: next}
}

// Enabled reports whether the wrapped handler handles records at the given level.
func (h *logTrace) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// Handle enriches and forwards records to the wrapped handler.
func (h *logTrace) Handle(ctx context.Context, record slog.Record) error {
	spanCtx := extractSpanContext(ctx)
	if spanCtx.IsValid() {
		record.AddAttrs(
			slog.String("trace_id", spanCtx.TraceID().String()),
			slog.String("span_id", spanCtx.SpanID().String()),
		)
	}
	return h.next.Handle(ctx, record)
}

// WithAttrs returns a new handler with attributes attached.
func (h *logTrace) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &logTrace{next: h.next.WithAttrs(attrs)}
}

// WithGroup returns a new handler with a group name.
func (h *logTrace) WithGroup(name string) slog.Handler {
	return &logTrace{next: h.next.WithGroup(name)}
}

// extractSpanContext extracts an OTel span context from logging context.
func extractSpanContext(ctx context.Context) trace.SpanContext {
	if ctx == nil {
		ctx = tracectx.GetContext()
	}
	if ctx == nil {
		return trace.SpanContext{}
	}

	span := trace.SpanFromContext(ctx)
	if span.SpanContext().IsValid() {
		return span.SpanContext()
	}

	fallback := tracectx.GetContext()
	if fallback == nil {
		return trace.SpanContext{}
	}
	span = trace.SpanFromContext(fallback)
	return span.SpanContext()
}
