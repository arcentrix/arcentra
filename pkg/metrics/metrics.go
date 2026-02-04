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

package metrics

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/arcentrix/arcade/pkg/log"
	"github.com/arcentrix/arcade/pkg/safe"
	"github.com/hashicorp/go-metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// MetricsConfig holds metrics server configuration
type MetricsConfig struct {
	Host   string
	Port   int
	Enable bool
	Path   string
}

// SetDefaults sets default values for MetricsConfig
func (m *MetricsConfig) SetDefaults() {
	if m.Host == "" {
		m.Host = "0.0.0.0"
	}
	if m.Port == 0 {
		m.Port = 8082
	}
	if m.Path == "" {
		m.Path = "/metrics"
	}
}

// Server represents a metrics server using hashicorp/go-metrics
type Server struct {
	config     MetricsConfig
	server     *http.Server
	registry   *prometheus.Registry
	sink       *PrometheusSink
	metrics    *metrics.Metrics
	collectors []prometheus.Collector
	mu         sync.Mutex
}

// PrometheusSink implements metrics.MetricSink interface for Prometheus
type PrometheusSink struct {
	registry   *prometheus.Registry
	mu         sync.RWMutex
	counters   map[string]*prometheus.CounterVec
	gauges     map[string]*prometheus.GaugeVec
	histograms map[string]*prometheus.HistogramVec
}

// NewPrometheusSink creates a new Prometheus sink
func NewPrometheusSink(registry *prometheus.Registry) *PrometheusSink {
	return &PrometheusSink{
		registry:   registry,
		counters:   make(map[string]*prometheus.CounterVec),
		gauges:     make(map[string]*prometheus.GaugeVec),
		histograms: make(map[string]*prometheus.HistogramVec),
	}
}

// SetGauge implements metrics.MetricSink
func (s *PrometheusSink) SetGauge(key []string, val float32) {
	s.SetGaugeWithLabels(key, val, nil)
}

// SetGaugeWithLabels implements metrics.MetricSink
func (s *PrometheusSink) SetGaugeWithLabels(key []string, val float32, labels []metrics.Label) {
	s.mu.Lock()
	defer s.mu.Unlock()

	metricName := sanitizeMetricName(key)
	labelNames := extractLabelNames(labels)

	gauge, exists := s.gauges[metricName]
	if !exists {
		gauge = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: metricName,
				Help: fmt.Sprintf("Gauge metric for %s", metricName),
			},
			labelNames,
		)
		s.registry.MustRegister(gauge)
		s.gauges[metricName] = gauge
	}

	gauge.With(convertLabels(labels)).Set(float64(val))
}

// EmitKey implements metrics.MetricSink
func (s *PrometheusSink) EmitKey(key []string, val float32) {
	// EmitKey is typically used for gauges
	s.SetGauge(key, val)
}

// IncrCounter implements metrics.MetricSink
func (s *PrometheusSink) IncrCounter(key []string, val float32) {
	s.IncrCounterWithLabels(key, val, nil)
}

// IncrCounterWithLabels implements metrics.MetricSink
func (s *PrometheusSink) IncrCounterWithLabels(key []string, val float32, labels []metrics.Label) {
	s.mu.Lock()
	defer s.mu.Unlock()

	metricName := sanitizeMetricName(key)
	labelNames := extractLabelNames(labels)

	counter, exists := s.counters[metricName]
	if !exists {
		counter = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: metricName,
				Help: fmt.Sprintf("Counter metric for %s", metricName),
			},
			labelNames,
		)
		s.registry.MustRegister(counter)
		s.counters[metricName] = counter
	}

	counter.With(convertLabels(labels)).Add(float64(val))
}

// AddSample implements metrics.MetricSink
func (s *PrometheusSink) AddSample(key []string, val float32) {
	s.AddSampleWithLabels(key, val, nil)
}

// AddSampleWithLabels implements metrics.MetricSink
func (s *PrometheusSink) AddSampleWithLabels(key []string, val float32, labels []metrics.Label) {
	s.mu.Lock()
	defer s.mu.Unlock()

	metricName := sanitizeMetricName(key)
	labelNames := extractLabelNames(labels)

	histogram, exists := s.histograms[metricName]
	if !exists {
		histogram = prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    metricName,
				Help:    fmt.Sprintf("Histogram metric for %s", metricName),
				Buckets: prometheus.ExponentialBuckets(0.001, 2, 15),
			},
			labelNames,
		)
		s.registry.MustRegister(histogram)
		s.histograms[metricName] = histogram
	}

	histogram.With(convertLabels(labels)).Observe(float64(val))
}

// NewServer creates a new metrics server
func NewServer(config MetricsConfig) *Server {
	config.SetDefaults()

	registry := prometheus.NewRegistry()
	// Register default collectors
	registry.MustRegister(collectors.NewGoCollector())
	registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	sink := NewPrometheusSink(registry)

	// Configure go-metrics with runtime metrics enabled
	cfg := metrics.DefaultConfig("")
	cfg.EnableRuntimeMetrics = true
	cfg.EnableHostname = false

	// Create metrics instance with the Prometheus sink
	metricsInstance, err := metrics.New(cfg, sink)
	if err != nil {
		log.Warnw("Failed to create metrics instance", "error", err)
		// Continue without metrics instance, sink will still work
		metricsInstance = nil
	}

	return &Server{
		config:     config,
		registry:   registry,
		sink:       sink,
		metrics:    metricsInstance,
		collectors: make([]prometheus.Collector, 0),
	}
}

// GetSink returns the metrics sink
func (s *Server) GetSink() metrics.MetricSink {
	return s.sink
}

// RegisterCollector registers a prometheus collector
func (s *Server) RegisterCollector(collector prometheus.Collector) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.registry.Register(collector); err != nil {
		return fmt.Errorf("failed to register collector: %w", err)
	}
	s.collectors = append(s.collectors, collector)
	return nil
}

// Start starts the metrics HTTP server
func (s *Server) Start() error {
	if !s.config.Enable {
		log.Info("Metrics server is disabled")
		return nil
	}

	path := s.config.Path

	mux := http.NewServeMux()
	mux.Handle(path, promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	}))

	addr := fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)
	s.server = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	safe.Go(func() {
		log.Infow("Metrics listener started", "address", addr)
		if err := s.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			panic(err)
		}
	})

	return nil
}

// Stop stops the metrics HTTP server
func (s *Server) Stop(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

// GetRegistry returns the prometheus registry (for backward compatibility)
func (s *Server) GetRegistry() *prometheus.Registry {
	return s.registry
}

// Helper functions

func sanitizeMetricName(key []string) string {
	if len(key) == 0 {
		return "unknown"
	}
	// Join all keys with underscore and convert to Prometheus-compatible metric name
	name := ""
	for i, k := range key {
		if i > 0 {
			name += "_"
		}
		name += k
	}
	// Convert to Prometheus-compatible metric name
	name = prometheus.BuildFQName("", "", name)
	return name
}

func extractLabelNames(labels []metrics.Label) []string {
	if len(labels) == 0 {
		return nil
	}
	names := make([]string, len(labels))
	for i, label := range labels {
		names[i] = label.Name
	}
	return names
}

func convertLabels(labels []metrics.Label) prometheus.Labels {
	if len(labels) == 0 {
		return nil
	}
	result := make(prometheus.Labels)
	for _, label := range labels {
		result[label.Name] = label.Value
	}
	return result
}
