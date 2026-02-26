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

package storage

import (
	"context"

	"github.com/arcentrix/arcentra/internal/engine/repo"
	"github.com/google/wire"
)

// ProviderSet 提供存储层相关的依赖
var ProviderSet = wire.NewSet(
	ProvideStorageFromDB,
)

// ProvideStorageFromDB 从数据库提供存储提供者
func ProvideStorageFromDB(repos *repo.Repositories) (IStorage, error) {
	dbProvider, err := NewStorageDBProvider(context.Background(), repos.Storage)
	if err != nil {
		return nil, err
	}
	return dbProvider.GetStorageProvider()
}
