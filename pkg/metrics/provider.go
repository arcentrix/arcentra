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
	"github.com/arcentrix/arcentra/pkg/http/middleware"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/google/wire"
)

// ProviderSet is a Wire provider set for metrics
var ProviderSet = wire.NewSet(
	NewMetricsServer,
)

// NewMetricsServer creates a new metrics server from config
func NewMetricsServer(config MetricsConfig) *Server {
	server := NewServer(config)
	// Setup cron metrics with the sink
	SetupCronMetrics(server.GetSink())
	// Register HTTP metrics
	if err := middleware.RegisterHttpMetrics(server.GetRegistry()); err != nil {
		log.Warnw("failed to register HTTP metrics", "error", err)
	}
	return server
}
