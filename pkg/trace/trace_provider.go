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

package trace

import (
	"context"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/version"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace/noop"
)

// TraceConfig represents the configuration for OpenTelemetry tracing
type TraceConfig struct {
	// Enabled enables or disables tracing
	Enabled bool `mapstructure:"enabled"`
	// ServiceName is the name of the service
	ServiceName string `mapstructure:"serviceName"`
	// ServiceVersion is the version of the service
	ServiceVersion string `mapstructure:"serviceVersion"`
	// ExporterType specifies the exporter type: "jaeger", "otlp-grpc", "otlp-http", or "none"
	ExporterType string `mapstructure:"exporterType"`
	// Endpoint is the endpoint URL for the exporter
	// For Jaeger: http://localhost:14268/api/traces
	// For OTLP gRPC: localhost:4317
	// For OTLP HTTP: http://localhost:4318
	Endpoint string `mapstructure:"endpoint"`
	// Insecure allows insecure connections (for development)
	Insecure bool `mapstructure:"insecure"`
	// Headers are additional headers to send with the trace export
	Headers map[string]string `mapstructure:"headers"`
	// BatchConfig configures the batch span processor
	BatchConfig BatchConfig `mapstructure:"batch"`
}

// BatchConfig configures the batch span processor
type BatchConfig struct {
	// MaxQueueSize is the maximum queue size
	MaxQueueSize int `mapstructure:"maxQueueSize"`
	// BatchTimeout is the timeout for batching spans
	BatchTimeout time.Duration `mapstructure:"batchTimeout"`
	// ExportTimeout is the timeout for exporting spans
	ExportTimeout time.Duration `mapstructure:"exportTimeout"`
	// MaxExportBatchSize is the maximum batch size for export
	MaxExportBatchSize int `mapstructure:"maxExportBatchSize"`
}

// SetDefaults sets default values for the configuration
func (c *TraceConfig) SetDefaults() {
	if c.ServiceName == "" {
		c.ServiceName = "arcentra"
	}
	c.ServiceVersion = version.GetVersion().Version

	if c.ExporterType == "" {
		c.ExporterType = "none"
	}
	if c.Endpoint == "" {
		switch c.ExporterType {
		case "jaeger":
			c.Endpoint = ""
		case "otlp-grpc":
			c.Endpoint = ""
		case "otlp-http":
			c.Endpoint = ""
		}
	}
	if c.BatchConfig.MaxQueueSize == 0 {
		c.BatchConfig.MaxQueueSize = 2048
	}
	// Handle BatchTimeout: if it's less than 1 second, assume it's configured as seconds (number format)
	if c.BatchConfig.BatchTimeout == 0 {
		c.BatchConfig.BatchTimeout = 5 * time.Second
	} else if c.BatchConfig.BatchTimeout > 0 && c.BatchConfig.BatchTimeout < time.Second {
		// If configured as a number (e.g., 10), viper parses it as nanoseconds
		// Convert to seconds by multiplying by time.Second
		// This handles cases where users configure timeout as integer seconds
		c.BatchConfig.BatchTimeout = c.BatchConfig.BatchTimeout * time.Second
	}
	// Handle ExportTimeout: if it's less than 1 second, assume it's configured as seconds (number format)
	if c.BatchConfig.ExportTimeout == 0 {
		c.BatchConfig.ExportTimeout = 30 * time.Second
	} else if c.BatchConfig.ExportTimeout > 0 && c.BatchConfig.ExportTimeout < time.Second {
		// If configured as a number (e.g., 10), viper parses it as nanoseconds
		// Convert to seconds by multiplying by time.Second
		// This handles cases where users configure timeout as integer seconds
		c.BatchConfig.ExportTimeout = c.BatchConfig.ExportTimeout * time.Second
	}
	if c.BatchConfig.MaxExportBatchSize == 0 {
		c.BatchConfig.MaxExportBatchSize = 512
	}
}

var (
	tracerProvider *sdktrace.TracerProvider
	shutdownFunc   func(context.Context) error
)

// Init initializes OpenTelemetry tracing with the given configuration
func Init(cfg TraceConfig) error {
	cfg.SetDefaults()

	if !cfg.Enabled || cfg.ExporterType == "none" {
		// Use noop tracer provider
		otel.SetTracerProvider(noop.NewTracerProvider())
		log.Info("OpenTelemetry tracing disabled, using noop tracer")
		return nil
	}

	// Create resource
	res, err := resource.New(
		context.Background(),
		resource.WithAttributes(
			semconv.ServiceNameKey.String(cfg.ServiceName),
			semconv.ServiceVersionKey.String(cfg.ServiceVersion),
		),
	)
	if err != nil {
		return fmt.Errorf("failed to create resource: %w", err)
	}

	// Create exporter
	var exporter sdktrace.SpanExporter
	switch cfg.ExporterType {
	case "jaeger":
		exporter, err = createJaegerExporter(cfg)
	case "otlp-grpc":
		exporter, err = createOTLPGRPCExporter(cfg)
	case "otlp-http":
		exporter, err = createOTLPHTTPExporter(cfg)
	default:
		return fmt.Errorf("unsupported exporter type: %s", cfg.ExporterType)
	}

	if err != nil {
		return fmt.Errorf("failed to create exporter: %w", err)
	}

	// Create batch span processor
	bsp := sdktrace.NewBatchSpanProcessor(
		exporter,
		sdktrace.WithMaxQueueSize(cfg.BatchConfig.MaxQueueSize),
		sdktrace.WithBatchTimeout(cfg.BatchConfig.BatchTimeout),
		sdktrace.WithExportTimeout(cfg.BatchConfig.ExportTimeout),
		sdktrace.WithMaxExportBatchSize(cfg.BatchConfig.MaxExportBatchSize),
	)

	// Create tracer provider
	tracerProvider = sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(bsp),
		sdktrace.WithSampler(sdktrace.AlwaysSample()), // Sample all spans
	)

	// Set global tracer provider
	otel.SetTracerProvider(tracerProvider)

	// Set global propagator
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// Set shutdown function
	shutdownFunc = func(ctx context.Context) error {
		if tracerProvider != nil {
			return tracerProvider.Shutdown(ctx)
		}
		return nil
	}

	log.Infow("OpenTelemetry tracing initialized",
		"exporter", cfg.ExporterType,
		"endpoint", cfg.Endpoint,
		"service", cfg.ServiceName,
		"version", cfg.ServiceVersion,
	)

	return nil
}

// createJaegerExporter creates a Jaeger exporter using OTLP HTTP
func createJaegerExporter(cfg TraceConfig) (sdktrace.SpanExporter, error) {
	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(cfg.Endpoint),
	}

	if cfg.Insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}

	if len(cfg.Headers) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(cfg.Headers))
	}

	client := otlptracehttp.NewClient(opts...)
	return otlptrace.New(context.Background(), client)
}

// createOTLPGRPCExporter creates an OTLP gRPC exporter
func createOTLPGRPCExporter(cfg TraceConfig) (sdktrace.SpanExporter, error) {
	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(cfg.Endpoint),
	}

	if cfg.Insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}

	if len(cfg.Headers) > 0 {
		opts = append(opts, otlptracegrpc.WithHeaders(cfg.Headers))
	}

	client := otlptracegrpc.NewClient(opts...)
	return otlptrace.New(context.Background(), client)
}

// createOTLPHTTPExporter creates an OTLP HTTP exporter
func createOTLPHTTPExporter(cfg TraceConfig) (sdktrace.SpanExporter, error) {
	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(cfg.Endpoint),
	}

	if cfg.Insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}

	if len(cfg.Headers) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(cfg.Headers))
	}

	client := otlptracehttp.NewClient(opts...)
	return otlptrace.New(context.Background(), client)
}

// Shutdown gracefully shuts down the tracer provider
func Shutdown(ctx context.Context) error {
	if shutdownFunc != nil {
		return shutdownFunc(ctx)
	}
	return nil
}

// GetTracerProvider returns the current tracer provider
func GetTracerProvider() *sdktrace.TracerProvider {
	return tracerProvider
}
