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

package metrics

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/hashicorp/go-metrics"
	"github.com/hibiken/asynq"
)

// AsynqMetricsCollector collects Asynq queue metrics using go-metrics
type AsynqMetricsCollector struct {
	inspector *asynq.Inspector
	sink      metrics.MetricSink
	mu        sync.RWMutex
	stopCh    chan struct{}
	doneCh    chan struct{}
}

// NewAsynqMetricsCollector creates a new Task Queue metrics collector
func NewAsynqMetricsCollector(inspector *asynq.Inspector, sink metrics.MetricSink) *AsynqMetricsCollector {
	return &AsynqMetricsCollector{
		inspector: inspector,
		sink:      sink,
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

// Start starts collecting metrics periodically
func (c *AsynqMetricsCollector) Start(interval time.Duration) {
	go c.collectLoop(interval)
}

// Stop stops collecting metrics
func (c *AsynqMetricsCollector) Stop() {
	close(c.stopCh)
	<-c.doneCh
}

// collectLoop periodically collects queue metrics
func (c *AsynqMetricsCollector) collectLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer close(c.doneCh)

	// Collect immediately
	c.collect()

	for {
		select {
		case <-ticker.C:
			c.collect()
		case <-c.stopCh:
			return
		}
	}
}

// collect collects metrics from Asynq inspector
func (c *AsynqMetricsCollector) collect() {
	c.mu.RLock()
	inspector := c.inspector
	sink := c.sink
	c.mu.RUnlock()

	if inspector == nil || sink == nil {
		log.Debug("Asynq inspector or sink is nil, skipping metrics collection")
		return
	}

	// Get all queues
	queues, err := inspector.Queues()
	if err != nil {
		log.Warnw("Failed to get queues for metrics", "error", err)
		return
	}

	// If no queues exist, still report zero metrics for default queues
	if len(queues) == 0 {
		defaultQueues := []string{"critical", "default", "low"}
		for _, queueName := range defaultQueues {
			c.emitZeroMetrics(sink, queueName)
		}
		return
	}

	// Collect metrics for each queue
	for _, queueName := range queues {
		info, err := inspector.GetQueueInfo(queueName)
		if err != nil {
			log.Warnw("Failed to get queue info", "queue", queueName, "error", err)
			c.emitZeroMetrics(sink, queueName)
			continue
		}

		labels := []metrics.Label{
			{Name: "queue", Value: queueName},
		}

		// Queue size metrics
		sink.SetGaugeWithLabels([]string{"asynq", "queue", "size"}, float32(info.Size), labels)
		sink.SetGaugeWithLabels([]string{"asynq", "queue", "pending"}, float32(info.Pending), labels)
		sink.SetGaugeWithLabels([]string{"asynq", "queue", "active"}, float32(info.Active), labels)
		sink.SetGaugeWithLabels([]string{"asynq", "queue", "scheduled"}, float32(info.Scheduled), labels)
		sink.SetGaugeWithLabels([]string{"asynq", "queue", "retry"}, float32(info.Retry), labels)
		sink.SetGaugeWithLabels([]string{"asynq", "queue", "archived"}, float32(info.Archived), labels)
		sink.SetGaugeWithLabels([]string{"asynq", "queue", "completed"}, float32(info.Completed), labels)
		sink.SetGaugeWithLabels([]string{"asynq", "queue", "aggregating"}, float32(info.Aggregating), labels)

		// Counter metrics (these are cumulative, so we set them as gauges)
		// Note: Asynq provides Processed and Failed as cumulative counters
		sink.SetGaugeWithLabels([]string{"asynq", "queue", "processed", "total"}, float32(info.Processed), labels)
		sink.SetGaugeWithLabels([]string{"asynq", "queue", "failed", "total"}, float32(info.Failed), labels)
	}
}

// emitZeroMetrics emits zero values for all metrics for a given queue
func (c *AsynqMetricsCollector) emitZeroMetrics(sink metrics.MetricSink, queueName string) {
	labels := []metrics.Label{
		{Name: "queue", Value: queueName},
	}

	sink.SetGaugeWithLabels([]string{"asynq", "queue", "size"}, 0, labels)
	sink.SetGaugeWithLabels([]string{"asynq", "queue", "pending"}, 0, labels)
	sink.SetGaugeWithLabels([]string{"asynq", "queue", "active"}, 0, labels)
	sink.SetGaugeWithLabels([]string{"asynq", "queue", "scheduled"}, 0, labels)
	sink.SetGaugeWithLabels([]string{"asynq", "queue", "retry"}, 0, labels)
	sink.SetGaugeWithLabels([]string{"asynq", "queue", "archived"}, 0, labels)
	sink.SetGaugeWithLabels([]string{"asynq", "queue", "completed"}, 0, labels)
	sink.SetGaugeWithLabels([]string{"asynq", "queue", "aggregating"}, 0, labels)
	sink.SetGaugeWithLabels([]string{"asynq", "queue", "processed", "total"}, 0, labels)
	sink.SetGaugeWithLabels([]string{"asynq", "queue", "failed", "total"}, 0, labels)
}

var (
	// asynqMetricsOnce ensures metrics are registered only once
	asynqMetricsOnce sync.Once
	asynqCollector   *AsynqMetricsCollector
)

// SetupAsynqMetrics sets up Task Queue metrics collection
func SetupAsynqMetrics(sink metrics.MetricSink, inspector *asynq.Inspector) error {
	if inspector == nil || sink == nil {
		return nil
	}

	collector := NewAsynqMetricsCollector(inspector, sink)
	collector.Start(15 * time.Second) // Collect every 15 seconds
	asynqCollector = collector
	return nil
}

// RegisterAsynqMetrics registers Task Queue metrics collector
func RegisterAsynqMetrics(sink metrics.MetricSink, inspector *asynq.Inspector) error {
	if sink == nil {
		return fmt.Errorf("sink is nil")
	}
	if inspector == nil {
		return fmt.Errorf("inspector is nil")
	}

	var err error
	asynqMetricsOnce.Do(func() {
		err = SetupAsynqMetrics(sink, inspector)
		if err != nil {
			log.Errorw("Failed to setup Task Queue metrics", "error", err)
		}
	})
	return err
}

// RegisterAsynqMetricsFromQueueServer registers Task Queue metrics from queue server
func RegisterAsynqMetricsFromQueueServer(sink metrics.MetricSink, queueServer interface{}) {
	if sink == nil {
		log.Warn("Metrics sink is nil, cannot register Task Queue metrics")
		return
	}
	if queueServer == nil {
		log.Warn("Queue server is nil, cannot register Task Queue metrics")
		return
	}

	// Use type assertion to get RedisConnOpt
	type QueueServerWithRedis interface {
		GetRedisConnOpt() asynq.RedisConnOpt
	}

	server, ok := queueServer.(QueueServerWithRedis)
	if !ok {
		log.Warn("Queue server does not implement GetRedisConnOpt method")
		return
	}

	redisOpt := server.GetRedisConnOpt()
	if redisOpt == nil {
		log.Warn("Redis connection option is nil")
		return
	}

	log.Infow("Creating Asynq inspector for metrics", "redisOpt", redisOpt)
	inspector := asynq.NewInspector(redisOpt)
	if inspector == nil {
		log.Error("Failed to create Asynq inspector")
		return
	}

	if err := RegisterAsynqMetrics(sink, inspector); err != nil {
		log.Errorw("Failed to register Task Queue metrics", "error", err)
	}
}

// RegisterAsynqMetricsFromQueueClient registers Task Queue metrics from queue client
func RegisterAsynqMetricsFromQueueClient(sink metrics.MetricSink, queueClient interface{}) {
	if sink == nil {
		log.Warn("Metrics sink is nil, cannot register Task Queue metrics")
		return
	}
	if queueClient == nil {
		log.Warn("Queue client is nil, cannot register Task Queue metrics")
		return
	}

	// Use type assertion to get RedisConnOpt
	type QueueClientWithRedis interface {
		GetRedisConnOpt() asynq.RedisConnOpt
	}

	client, ok := queueClient.(QueueClientWithRedis)
	if !ok {
		log.Warn("Queue client does not implement GetRedisConnOpt method")
		return
	}

	redisOpt := client.GetRedisConnOpt()
	if redisOpt == nil {
		log.Warn("Redis connection option is nil")
		return
	}

	log.Infow("Creating Asynq inspector for metrics", "redisOpt", redisOpt)
	inspector := asynq.NewInspector(redisOpt)
	if inspector == nil {
		log.Error("Failed to create Asynq inspector")
		return
	}

	if err := RegisterAsynqMetrics(sink, inspector); err != nil {
		log.Errorw("Failed to register Task Queue metrics", "error", err)
	}
}

// StopAsynqMetricsCollector stops the Asynq metrics collector
func StopAsynqMetricsCollector(ctx context.Context) error {
	if asynqCollector == nil {
		return nil
	}

	asynqCollector.Stop()
	return nil
}
