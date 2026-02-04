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

//go:build wireinject
// +build wireinject

package main

import (
	"github.com/arcentrix/arcade/internal/agent/bootstrap"
	"github.com/arcentrix/arcade/internal/agent/config"
	"github.com/arcentrix/arcade/internal/agent/router"
	"github.com/arcentrix/arcade/internal/pkg/grpc"
	"github.com/arcentrix/arcade/internal/pkg/queue"
	"github.com/arcentrix/arcade/pkg/cache"
	"github.com/arcentrix/arcade/pkg/log"
	"github.com/arcentrix/arcade/pkg/metrics"
	"github.com/google/wire"
)

func initAgent(configPath string) (*bootstrap.Agent, func(), error) {
	panic(wire.Build(
		// 配置层
		config.ProviderSet,
		// 日志层（依赖 config）
		log.ProviderSet,
		// 缓存层（依赖 config）
		cache.ProviderSet,
		// 任务队列层（依赖 config, cache）
		queue.AgentProviderSet,
		// 指标层（依赖 config）
		metrics.ProviderSet,
		// gRPC 客户端层（依赖 config 和 log）
		grpc.ProviderSet,
		router.ProviderSet,
		// 应用层
		bootstrap.NewAgent,
	))
}
