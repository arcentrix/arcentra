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

package database

import (
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/google/wire"
	"gorm.io/gorm"
)

// ProviderSet provides database-related dependencies
var ProviderSet = wire.NewSet(
	ProvideManager,
	ProvideMySQL,
	ProvideIDatabase,
)

// ProvideManager creates and returns a database Manager instance
func ProvideManager(conf Database, logger *log.Logger) (Manager, error) {
	return NewManager(conf)
}

// ProvideMySQL provides MySQL database instance from Manager
func ProvideMySQL(manager Manager) *gorm.DB {
	return manager.MySQL()
}

// ProvideIDatabase provides IDatabase interface instance for backward compatibility
func ProvideIDatabase(manager Manager) IDatabase {
	return NewDatabaseAdapter(manager)
}
