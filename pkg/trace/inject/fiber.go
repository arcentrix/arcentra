// Copyright 2025 Arcade Team
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

package inject

import (
	"context"
	"fmt"
	"time"

	tracecontext "github.com/arcentrix/arcade/pkg/trace/context"
	"github.com/gofiber/fiber/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const fiberTracerName = "github.com/arcentrix/arcade/pkg/trace/inject/fiber"

var (
	fiberTracer     = otel.Tracer(fiberTracerName)
	fiberPropagator = otel.GetTextMapPropagator()
)

// FiberMiddleware returns a Fiber middleware for OpenTelemetry tracing
func FiberMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx := c.UserContext()
		if ctx == nil {
			// Fiber 的 Context() 返回 *fasthttp.RequestCtx，不实现 context.Context
			// 为了链路串联，这里默认使用 background context
			ctx = context.Background()
		}

		// Extract trace context from HTTP headers
		headers := make(map[string]string)
		for key, value := range c.Request().Header.All() {
			headers[string(key)] = string(value)
		}
		ctx = fiberPropagator.Extract(ctx, &headerCarrier{headers: headers})

		// Start span
		spanName := string(c.Method()) + " " + c.Path()
		start := time.Now()
		ctx, span := fiberTracer.Start(ctx, spanName, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()

		// Set context for goroutine (for goroutine-local context access)
		tracecontext.SetContext(ctx)
		defer tracecontext.ClearContext()

		// Set trace context back to Fiber UserContext so handlers can access it
		// This ensures database operations can inherit the trace context
		c.SetUserContext(ctx)

		// Set HTTP attributes (only set non-empty values to avoid null in trace data)
		attrs := []attribute.KeyValue{
			attribute.String("http.method", c.Method()),
			attribute.String("http.scheme", c.Protocol()),
			attribute.String("http.target", string(c.Request().URI().RequestURI())),
		}

		// Set request ID if available (try Locals first, then header)
		var requestId string
		if id, ok := c.Locals("request_id").(string); ok {
			requestId = id
		}
		if requestId != "" {
			attrs = append(attrs, attribute.String("http.request.id", requestId))
		}

		span.SetAttributes(attrs...)

		// Set user agent if available
		if userAgent := c.Get("User-Agent"); userAgent != "" {
			span.SetAttributes(attribute.String("http.user_agent", userAgent))
		}

		// Set user ip if available
		if ip, ok := c.Locals("ip").(string); ok && ip != "" {
			span.SetAttributes(attribute.String("net.peer.ip", ip))
		}

		// Inject trace context into response headers
		// TODO: 需要考虑是否需要注入响应头
		//responseCarrier := &responseHeaderCarrier{c: c}
		// fiberPropagator.Inject(ctx, responseCarrier)

		// Execute handler
		err := c.Next()

		// Calculate duration
		duration := time.Since(start)
		span.SetAttributes(
			attribute.Int64("http.duration_ms", duration.Milliseconds()),
			attribute.Float64("http.duration_seconds", duration.Seconds()),
		)

		// Set response status
		statusCode := c.Response().StatusCode()
		span.SetAttributes(attribute.Int("http.status_code", statusCode))

		// Set status based on HTTP status code
		if statusCode >= 400 {
			if err != nil {
				span.SetStatus(codes.Error, err.Error())
				span.RecordError(err)
			} else {
				span.SetStatus(codes.Error, fmt.Sprintf("HTTP %d", statusCode))
			}
		} else {
			span.SetStatus(codes.Ok, "")
		}

		return err
	}
}

// headerCarrier adapts HTTP request headers to propagation.TextMapCarrier
type headerCarrier struct {
	headers map[string]string
}

func (c *headerCarrier) Get(key string) string {
	return c.headers[key]
}

func (c *headerCarrier) Set(key, value string) {
	c.headers[key] = value
}

func (c *headerCarrier) Keys() []string {
	keys := make([]string, 0, len(c.headers))
	for k := range c.headers {
		keys = append(keys, k)
	}
	return keys
}

// responseHeaderCarrier adapts HTTP response headers to propagation.TextMapCarrier
type responseHeaderCarrier struct {
	c *fiber.Ctx
}

func (c *responseHeaderCarrier) Get(key string) string {
	return c.c.Get(key)
}

func (c *responseHeaderCarrier) Set(key, value string) {
	c.c.Set(key, value)
}

func (c *responseHeaderCarrier) Keys() []string {
	// For response headers, we don't need to enumerate keys
	return nil
}
