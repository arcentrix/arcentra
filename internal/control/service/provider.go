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

package service

import (
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/google/wire"
)

// ProviderSet 提供服务层相关的依赖
var ProviderSet = wire.NewSet(
	ProvideServices,
)

// ProvideServices 提供统一的 Services 实例
func ProvideServices(
	db database.IDatabase,
	cacheStore cache.ICache,
	repos *repo.Repositories,
	runtimePolicy taskqueue.OfflineRuntimePolicy,
) (*Services, error) {
	if err := runtimePolicy.ValidateProduction(); err != nil {
		return nil, err
	}
	services := NewServices(db, cacheStore, repos)
	// 只信任配置 provider 的启动快照，不读取请求中的策略或设置
	services.offlineRuntimePolicy = runtimePolicy.Clone()
	return services, nil
}
