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

package storage

import (
	"context"

	agentcase "github.com/arcentrix/arcentra/internal/case/agent"
	domain "github.com/arcentrix/arcentra/internal/domain/agent"
	"github.com/google/wire"
)

var ProviderSet = wire.NewSet(
	ProvideStorageFromDB,
	NewFileUploaderAdapter,
	wire.Bind(new(agentcase.IFileUploader), new(*FileUploaderAdapter)),
)

func ProvideStorageFromDB(storageRepo domain.IStorageRepository) (domain.IStorage, error) {
	dbProvider, err := NewStorageDBProvider(context.Background(), storageRepo)
	if err != nil {
		return nil, err
	}
	return dbProvider.GetStorageProvider()
}
