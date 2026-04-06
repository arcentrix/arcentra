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
	dal "github.com/arcentrix/arcentra/internal/dal/queries"
	"github.com/arcentrix/arcentra/pkg/telemetry/log"
	"github.com/google/wire"
	"gorm.io/gorm"
)

// ProviderSet provides database-related dependencies
var ProviderSet = wire.NewSet(
	ProvideManager,
	ProvideMySQL,
	ProvideIDatabase,
	ProvideDALQueries,
	ProvideDBTX,
)

// ProvideManager creates and returns a database Manager instance
func ProvideManager(conf Database, _ *log.Logger) (Manager, error) {
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

// ProvideDALQueries creates a *dal.Queries backed by the default connection's *sql.DB.
func ProvideDALQueries(db IDatabase) (*dal.Queries, error) {
	sqlDB, err := db.SQL()
	if err != nil {
		return nil, err
	}
	return dal.New(sqlDB), nil
}

// ProvideDBTX exposes the underlying *sql.DB as dal.DBTX for repositories that
// need to execute hand-crafted SQL (e.g. dynamic partial-field updates).
func ProvideDBTX(db IDatabase) (dal.DBTX, error) {
	return db.SQL()
}
