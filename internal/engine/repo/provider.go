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

package repo

import (
	"github.com/arcentrix/arcentra/pkg/cache"
	"github.com/arcentrix/arcentra/pkg/database"
	"github.com/google/wire"
	"gorm.io/gorm"
)

// ProviderSet 提供仓储层相关的依赖
var ProviderSet = wire.NewSet(
	ProvideRepositories,
)

// ProvideRepositories 提供统一的 Repositories 实例
func ProvideRepositories(db database.IDatabase, clickHouse *gorm.DB, cache cache.ICache) *Repositories {
	return NewRepositories(db, clickHouse, cache)
}
