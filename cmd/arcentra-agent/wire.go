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

//go:build wireinject

package main

import (
	grpc "github.com/arcentrix/arcentra/internal/adapter/grpc"
	"github.com/arcentrix/arcentra/internal/agent/bootstrap"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/agent/outbox"
	"github.com/arcentrix/arcentra/internal/agent/router"
	"github.com/arcentrix/arcentra/pkg/telemetry/log"
	"github.com/arcentrix/arcentra/pkg/telemetry/metrics"
	"github.com/google/wire"
)

func initAgent(configPath string) (*bootstrap.Agent, func(), error) {
	panic(wire.Build(
		// 配置层
		config.ProviderSet,
		// 日志层（依赖 config）
		log.ProviderSet,
		// 指标层（依赖 config）
		metrics.ProviderSet,
		// gRPC 客户端层（依赖 config 和 log）
		grpc.ClientProviderSet,
		// 路由层（依赖 config 和 log）
		router.ProviderSet,
		// Outbox（依赖 config 和 grpc）
		outbox.ProvideOutbox,
		// 执行器（依赖 Outbox，ShellExecutor + Publisher）
		outbox.ProvideExecutorManager,
		// 应用层
		bootstrap.NewAgent,
	))
}
