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

//go:build wireinject
// +build wireinject

package main

import (
	"github.com/arcentrix/arcentra/internal/engine/bootstrap"
	"github.com/arcentrix/arcentra/internal/engine/config"
	"github.com/arcentrix/arcentra/internal/engine/repo"
	"github.com/arcentrix/arcentra/internal/engine/router"
	"github.com/arcentrix/arcentra/internal/engine/service"
	"github.com/arcentrix/arcentra/internal/pkg/grpc"
	"github.com/arcentrix/arcentra/internal/pkg/queue"
	"github.com/arcentrix/arcentra/internal/pkg/storage"
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/metrics"
	"github.com/arcentrix/arcentra/pkg/plugin"
	"github.com/google/wire"
)

func initApp(configPath string, pluginConfigs map[string]any) (*bootstrap.App, func(), error) {
	panic(wire.Build(
		// 配置层
		config.ProviderSet,
		// 日志层（依赖 config）
		log.ProviderSet,
		// 数据库层（依赖 config, log, ctx）
		database.ProviderSet,
		// 缓存层（依赖 config）
		cache.ProviderSet,
		// 任务队列层（依赖 config, cache）
		queue.ProviderSet,
		// 指标层（依赖 config, queue）
		metrics.ProviderSet,
		// 仓储层（依赖 database）
		repo.ProviderSet,
		// 存储层（依赖 repo）
		storage.ProviderSet,
		// 插件层（依赖 config, database）
		plugin.ProviderSet,
		// 服务层（依赖 repo, storage, plugin, database, cache）
		service.ProviderSet,
		// 路由层（依赖 config, repo, service, storage, plugin）
		router.ProviderSet,
		// gRPC 服务层
		grpc.ProviderSet,
		// 应用层
		bootstrap.NewApp,
	))
}
