// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package inject

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	tracecontext "github.com/arcentrix/arcentra/pkg/trace/context"
	"github.com/gofiber/fiber/v2"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type fiberTestContextKey struct{}

// TestFiberMiddlewarePreservesRequestContextAndExtractsTrace 验证请求值和大小写无关的链路头传播
func TestFiberMiddlewarePreservesRequestContextAndExtractsTrace(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSyncer(exporter))
	previousTracer, previousPropagator := fiberTracer, fiberPropagator
	fiberTracer = provider.Tracer(fiberTracerName)
	fiberPropagator = propagation.TraceContext{}
	t.Cleanup(func() {
		fiberTracer, fiberPropagator = previousTracer, previousPropagator
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	deadline := time.Now().Add(time.Minute)
	parent, cancel := context.WithDeadline(context.WithValue(context.Background(), fiberTestContextKey{}, "retained"), deadline)
	defer cancel()
	var captured context.Context
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.SetUserContext(parent)
		c.Locals("request_id", "trace-request")
		c.Locals("ip", "192.0.2.1")
		err := c.Next()
		if tracecontext.GetContext() != nil {
			t.Error("goroutine-local request context leaked after tracing returned")
		}
		return err
	})
	app.Use(FiberMiddleware())
	app.Get("/trace", func(c *fiber.Ctx) error {
		captured = c.UserContext()
		if captured.Value(fiberTestContextKey{}) != "retained" {
			t.Error("tracing dropped request values")
		}
		if got, ok := captured.Deadline(); !ok || !got.Equal(deadline) {
			t.Error("tracing dropped request deadline")
		}
		if tracecontext.GetContext() != captured {
			t.Error("goroutine and explicit contexts disagree")
		}
		return c.SendStatus(fiber.StatusNoContent)
	})
	req := httptest.NewRequest(fiber.MethodGet, "/trace", nil)
	req.Header.Set("Traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	resp, err := app.Test(req, 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("status=%d, want 204", resp.StatusCode)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("exported %d spans, want 1", len(spans))
	}
	span := spans[0]
	if span.SpanContext.TraceID().String() != "0af7651916cd43dd8448eb211c80319c" || span.Parent.SpanID().String() != "b7ad6b7169203331" {
		t.Fatal("HTTP trace header was not extracted as the server span parent")
	}
	if span.SpanKind != trace.SpanKindServer || span.Name != "GET /trace" {
		t.Error("unexpected HTTP span identity")
	}
	attrs := make(map[string]string)
	for _, attr := range span.Attributes {
		attrs[string(attr.Key)] = attr.Value.Emit()
	}
	if attrs["http.request.id"] != "trace-request" || attrs["net.peer.ip"] != "192.0.2.1" {
		t.Error("request metadata missing from trace")
	}
	cancel()
	if captured.Err() != context.Canceled {
		t.Error("traced context lost parent cancellation")
	}
}

// TestFiberMiddlewareFallbackDoesNotBorrowRequestCtx 验证默认链路上下文不借用池化请求对象
func TestFiberMiddlewareFallbackDoesNotBorrowRequestCtx(t *testing.T) {
	app := fiber.New()
	app.Use(FiberMiddleware())
	var captured context.Context
	app.Get("/trace", func(c *fiber.Ctx) error {
		captured = c.UserContext()
		c.Locals(fiberTestContextKey{}, "pooled-only")
		if captured.Value(fiberTestContextKey{}) != nil {
			t.Error("fallback context borrowed request-local state")
		}
		return c.SendStatus(fiber.StatusNoContent)
	})
	resp, err := app.Test(httptest.NewRequest(fiber.MethodGet, "/trace", nil), 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if captured == nil || captured.Value(fiberTestContextKey{}) != nil {
		t.Fatal("fallback context is invalid after request completion")
	}
}
