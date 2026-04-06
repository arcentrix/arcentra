// Copyright 2026 Arcentra Authors.
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

//go:build wireinject

package main

import (
	"github.com/arcentrix/arcentra/internal/adapter"
	"github.com/arcentrix/arcentra/internal/case/agent"
	"github.com/arcentrix/arcentra/internal/case/execution"
	"github.com/arcentrix/arcentra/internal/case/identity"
	"github.com/arcentrix/arcentra/internal/case/pipeline"
	"github.com/arcentrix/arcentra/internal/case/project"
	"github.com/arcentrix/arcentra/internal/control/bootstrap"
	"github.com/arcentrix/arcentra/internal/control/config"
	"github.com/arcentrix/arcentra/internal/infra/persistence"
	infraStorage "github.com/arcentrix/arcentra/internal/infra/storage"
	"github.com/arcentrix/arcentra/pkg/integration/plugin"
	"github.com/arcentrix/arcentra/pkg/lifecycle/shutdown"
	"github.com/arcentrix/arcentra/pkg/store/cache"
	"github.com/arcentrix/arcentra/pkg/store/database"
	"github.com/arcentrix/arcentra/pkg/telemetry/log"
	"github.com/arcentrix/arcentra/pkg/telemetry/metrics"
	"github.com/google/wire"
)

func initApp(configPath string, pluginConfigs map[string]any) (*bootstrap.App, func(), error) {
	panic(wire.Build(
		// control
		config.ProviderSet,
		log.ProviderSet,
		cache.ProviderSet,
		database.ProviderSet,
		metrics.ProviderSet,
		shutdown.NewManager,
		ProvideIDGenerator,
		// infra
		persistence.ProviderSet,
		infraStorage.ProviderSet,
		// case
		agent.ProviderSet,
		identity.ProviderSet,
		project.ProviderSet,
		pipeline.ProviderSet,
		execution.ProviderSet,
		// adapter
		adapter.ProviderSet,
		// plugin
		plugin.ProviderSet,
		// bootstrap
		bootstrap.NewApp,
	))
}
