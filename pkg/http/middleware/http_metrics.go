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

package middleware

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	httpDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "request_duration_seconds",
			Help:    "HTTP request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "route", "status_class"},
	)

	httpRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "requests_total",
			Help: "Total number of HTTP requests",
		},
		[]string{"method", "route", "status_class"},
	)
)

func RegisterHttpMetrics(registry *prometheus.Registry) error {
	if err := registry.Register(httpDuration); err != nil {
		return err
	}
	if err := registry.Register(httpRequests); err != nil {
		return err
	}
	return nil
}

func HttpMetricsMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		dur := time.Since(start).Seconds()

		route := "unknown"
		if r := c.Route(); r != nil && r.Path != "" {
			route = r.Path
		}

		status := c.Response().StatusCode()
		statusClass := strconv.Itoa(status/100) + "xx"

		method := c.Method()

		httpDuration.WithLabelValues(method, route, statusClass).Observe(dur)
		httpRequests.WithLabelValues(method, route, statusClass).Inc()

		return err
	}
}
