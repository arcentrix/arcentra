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

package router

import (
	"github.com/arcentrix/arcade/internal/engine/service"
	"github.com/arcentrix/arcade/pkg/cache"
	"github.com/arcentrix/arcade/pkg/http"
	"github.com/arcentrix/arcade/pkg/shutdown"
	"github.com/google/wire"
)

// ProviderSet 提供路由层相关的依赖
var ProviderSet = wire.NewSet(
	ProvideShutdownManager,
	ProvideRouter,
)

// ProvideShutdownManager 提供优雅下线管理器
func ProvideShutdownManager() *shutdown.Manager {
	return shutdown.NewManager()
}

// ProvideRouter 提供路由实例
func ProvideRouter(
	httpConf *http.Http,
	cache cache.ICache,
	services *service.Services,
	shutdownMgr *shutdown.Manager,
) *Router {
	return NewRouter(
		httpConf,
		cache,
		services,
		shutdownMgr,
	)
}
