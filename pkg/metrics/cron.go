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

package metrics

import (
	"sync"
	"time"

	"github.com/arcentrix/arcentra/pkg/cron"
	"github.com/hashicorp/go-metrics"
)

// cronMetricsOnce ensures metrics are registered only once
var cronMetricsOnce sync.Once

// CronMetricsRecorder implements cron.MetricsRecorder interface
type CronMetricsRecorder struct {
	sink metrics.MetricSink
}

// NewCronMetricsRecorder creates a new cron metrics recorder
func NewCronMetricsRecorder(sink metrics.MetricSink) *CronMetricsRecorder {
	return &CronMetricsRecorder{sink: sink}
}

// RecordJobRun records a cron job run
func (r *CronMetricsRecorder) RecordJobRun(jobName string, duration time.Duration, err error) {
	RecordCronJobRun(r.sink, jobName, duration, err)
}

// UpdateNextRun updates the next run time for a cron job
func (r *CronMetricsRecorder) UpdateNextRun(jobName string, nextRun time.Time) {
	UpdateCronJobNextRun(r.sink, jobName, nextRun)
}

// UpdateJobsCount updates the total number of registered cron jobs
func (r *CronMetricsRecorder) UpdateJobsCount(count int) {
	UpdateCronJobsCount(r.sink, count)
}

// SetupCronMetrics sets up cron metrics recording
func SetupCronMetrics(metricsSink metrics.MetricSink) {
	cronMetricsOnce.Do(func() {
		cron.SetMetricsRecorder(NewCronMetricsRecorder(metricsSink))
	})
}

// RecordCronJobRun records a cron job run
func RecordCronJobRun(metricsSink metrics.MetricSink, jobName string, duration time.Duration, err error) {
	if metricsSink == nil {
		return
	}

	labels := []metrics.Label{
		{Name: "job_name", Value: jobName},
	}

	// Increment total runs counter
	metricsSink.IncrCounterWithLabels([]string{"cron", "job", "runs", "total"}, 1, labels)

	// Record duration as histogram
	metricsSink.AddSampleWithLabels([]string{"cron", "job", "run", "duration", "seconds"}, float32(duration.Seconds()), labels)

	// Record last run time as gauge
	metricsSink.SetGaugeWithLabels([]string{"cron", "job", "last", "run", "time", "seconds"}, float32(time.Now().Unix()), labels)

	// Increment error counter if there's an error
	if err != nil {
		metricsSink.IncrCounterWithLabels([]string{"cron", "job", "errors", "total"}, 1, labels)
	}
}

// UpdateCronJobNextRun updates the next run time for a cron job
func UpdateCronJobNextRun(metricsSink metrics.MetricSink, jobName string, nextRun time.Time) {
	if metricsSink == nil || nextRun.IsZero() {
		return
	}

	labels := []metrics.Label{
		{Name: "job_name", Value: jobName},
	}

	metricsSink.SetGaugeWithLabels([]string{"cron", "job", "next", "run", "time", "seconds"}, float32(nextRun.Unix()), labels)
}

// UpdateCronJobsCount updates the total number of registered cron jobs
func UpdateCronJobsCount(metricsSink metrics.MetricSink, count int) {
	if metricsSink == nil {
		return
	}

	metricsSink.SetGauge([]string{"cron", "jobs", "total"}, float32(count))
}
