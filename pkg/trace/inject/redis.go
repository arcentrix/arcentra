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

package inject

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tracecontext "github.com/arcentrix/arcentra/pkg/trace/context"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const (
	redisTracerName = "github.com/arcentrix/arcentra/pkg/trace/inject/redis"
)

var redisTracer = otel.Tracer(redisTracerName)

// RedisHook implements redis.Hook interface for OpenTelemetry tracing
type RedisHook struct {
	// WithArgs enables command arguments tracing
	WithArgs bool
}

// BeforeProcess is called before processing a command
func (h *RedisHook) BeforeProcess(ctx context.Context, cmd redis.Cmder) (context.Context, error) {
	if ctx == nil {
		// If no context provided, try to get from goroutine context
		if goroutineCtx := tracecontext.GetContext(); goroutineCtx != nil {
			ctx = goroutineCtx
		} else {
			ctx = context.Background()
		}
	}

	// Ensure trace context is propagated (from goroutine context if needed)
	ctx = tracecontext.WithSpan(ctx)

	cmdName := cmd.Name()
	spanName := "redis." + cmdName

	ctx, span := redisTracer.Start(ctx, spanName, trace.WithSpanKind(trace.SpanKindClient))
	ctx = context.WithValue(ctx, "redis.span", span)
	ctx = context.WithValue(ctx, "redis.start", time.Now())

	// Set database attributes (only set non-empty values to avoid null in trace data)
	attrs := []attribute.KeyValue{
		attribute.String("db.system", "redis"),
	}

	// Set operation name if not empty
	if cmdName != "" {
		attrs = append(attrs, attribute.String("db.operation", cmdName))
	}

	span.SetAttributes(attrs...)

	if statement := redisStatementFromCmd(cmd); statement != "" {
		span.SetAttributes(attribute.String("db.statement", statement))
	}

	// Set command arguments if enabled and available
	if h.WithArgs && len(cmd.Args()) > 1 {
		args := cmd.Args()[1:]
		if len(args) > 0 {
			argsStr := stringSliceFromArgs(args)
			if len(argsStr) > 0 {
				span.SetAttributes(attribute.StringSlice("db.redis.args", argsStr))
			}
		}
	}

	return ctx, nil
}

// AfterProcess is called after processing a command
func (h *RedisHook) AfterProcess(ctx context.Context, cmd redis.Cmder) error {
	span, ok := ctx.Value("redis.span").(trace.Span)
	if !ok {
		return nil
	}
	defer span.End()

	// Calculate duration
	if start, ok := ctx.Value("redis.start").(time.Time); ok {
		duration := time.Since(start)
		span.SetAttributes(
			attribute.Int64("db.redis.duration_ms", duration.Milliseconds()),
			attribute.Float64("db.redis.duration_seconds", duration.Seconds()),
		)
	}

	if err := cmd.Err(); err != nil {
		if errors.Is(err, redis.Nil) {
			span.SetStatus(codes.Ok, "")
			span.SetAttributes(attribute.Bool("db.redis.nil", true))
		} else {
			span.SetStatus(codes.Error, err.Error())
			span.RecordError(err)
		}
	} else {
		span.SetStatus(codes.Ok, "")
	}

	return nil
}

// BeforeProcessPipeline is called before processing a pipeline
func (h *RedisHook) BeforeProcessPipeline(ctx context.Context, cmds []redis.Cmder) (context.Context, error) {
	if ctx == nil {
		// If no context provided, try to get from goroutine context
		if goroutineCtx := tracecontext.GetContext(); goroutineCtx != nil {
			ctx = goroutineCtx
		} else {
			ctx = context.Background()
		}
	}

	// Ensure trace context is propagated (from goroutine context if needed)
	ctx = tracecontext.WithSpan(ctx)

	ctx, span := redisTracer.Start(ctx, "redis.pipeline", trace.WithSpanKind(trace.SpanKindClient))
	ctx = context.WithValue(ctx, "redis.pipeline.span", span)
	ctx = context.WithValue(ctx, "redis.pipeline.start", time.Now())

	// Set database attributes (only set non-empty values to avoid null in trace data)
	attrs := []attribute.KeyValue{
		attribute.String("db.system", "redis"),
		attribute.String("db.operation", "pipeline"),
		attribute.Int("db.redis.pipeline.commands", len(cmds)),
	}
	span.SetAttributes(attrs...)

	if statement := redisStatementFromPipeline(cmds); statement != "" {
		span.SetAttributes(attribute.String("db.statement", statement))
	}

	// Set command names if enabled and available
	if h.WithArgs && len(cmds) > 0 {
		cmdNames := make([]string, 0, len(cmds))
		for _, cmd := range cmds {
			if name := cmd.Name(); name != "" {
				cmdNames = append(cmdNames, name)
			}
		}
		if len(cmdNames) > 0 {
			span.SetAttributes(attribute.StringSlice("db.redis.pipeline.commands", cmdNames))
		}
	}

	return ctx, nil
}

// AfterProcessPipeline is called after processing a pipeline
func (h *RedisHook) AfterProcessPipeline(ctx context.Context, cmds []redis.Cmder) error {
	span, ok := ctx.Value("redis.pipeline.span").(trace.Span)
	if !ok {
		return nil
	}
	defer span.End()

	// Calculate duration
	if start, ok := ctx.Value("redis.pipeline.start").(time.Time); ok {
		duration := time.Since(start)
		span.SetAttributes(
			attribute.Int64("db.redis.pipeline.duration_ms", duration.Milliseconds()),
			attribute.Float64("db.redis.pipeline.duration_seconds", duration.Seconds()),
		)
	}

	var errCount int
	for _, cmd := range cmds {
		if err := cmd.Err(); err != nil && !errors.Is(err, redis.Nil) {
			errCount++
		}
	}

	if errCount > 0 {
		span.SetStatus(codes.Error, fmt.Sprintf("%d commands failed", errCount))
		span.SetAttributes(attribute.Int("db.redis.pipeline.errors", errCount))
	} else {
		span.SetStatus(codes.Ok, "")
	}

	return nil
}

// ProcessHook wraps BeforeProcess and AfterProcess (required by redis.Hook interface)
func (h *RedisHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		ctx, err := h.BeforeProcess(ctx, cmd)
		if err != nil {
			return err
		}
		if err = next(ctx, cmd); err != nil {
			return err
		}
		return h.AfterProcess(ctx, cmd)
	}
}

// ProcessPipelineHook wraps BeforeProcessPipeline and AfterProcessPipeline (required by redis.Hook interface)
func (h *RedisHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		ctx, err := h.BeforeProcessPipeline(ctx, cmds)
		if err != nil {
			return err
		}
		if err = next(ctx, cmds); err != nil {
			return err
		}
		return h.AfterProcessPipeline(ctx, cmds)
	}
}

// DialHook is called when dialing a connection (required by redis.Hook interface)
func (h *RedisHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

// stringSliceFromArgs converts command arguments to string slice
func stringSliceFromArgs(args []interface{}) []string {
	result := make([]string, 0, len(args))
	for _, arg := range args {
		switch v := arg.(type) {
		case string:
			result = append(result, v)
		case []byte:
			result = append(result, string(v))
		case int, int8, int16, int32, int64:
			result = append(result, fmt.Sprintf("%d", v))
		case uint, uint8, uint16, uint32, uint64:
			result = append(result, fmt.Sprintf("%d", v))
		case float32, float64:
			result = append(result, fmt.Sprintf("%f", v))
		case bool:
			result = append(result, fmt.Sprintf("%t", v))
		default:
			result = append(result, fmt.Sprintf("%v", v))
		}
	}
	return result
}

func redisStatementFromCmd(cmd redis.Cmder) string {
	if cmd == nil {
		return ""
	}
	return cmd.String()
}

func redisStatementFromPipeline(cmds []redis.Cmder) string {
	if len(cmds) == 0 {
		return ""
	}
	statements := make([]string, 0, len(cmds))
	for _, cmd := range cmds {
		if cmd == nil {
			continue
		}
		if statement := cmd.String(); statement != "" {
			statements = append(statements, statement)
		}
	}
	if len(statements) == 0 {
		return ""
	}
	return strings.Join(statements, "; ")
}

// RegisterRedisHook registers the OpenTelemetry hook to Redis client
func RegisterRedisHook(client redis.Cmdable, withArgs bool) {
	hook := &RedisHook{
		WithArgs: withArgs,
	}

	switch c := client.(type) {
	case *redis.Client:
		c.AddHook(hook)
	case *redis.ClusterClient:
		c.AddHook(hook)
	default:
		// For other implementations, try to add hook if it supports it
		if hookClient, ok := client.(interface{ AddHook(redis.Hook) }); ok {
			hookClient.AddHook(hook)
		}
	}
}
