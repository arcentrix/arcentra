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
