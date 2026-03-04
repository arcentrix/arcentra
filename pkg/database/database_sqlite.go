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
	"fmt"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const (
	sqliteDefaultMaxOpen = 1
	sqliteDefaultMaxIdle = 1
)

// newSQLiteConnection creates a SQLite connection using GORM
func newSQLiteConnection(sqliteCfg SQLiteConfig, opts DatabaseOptions) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(sqliteCfg.DSN), defaultGormConfig(opts))
	if err != nil {
		return nil, fmt.Errorf("failed to open SQLite connection: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get underlying sql.DB handle: %w", err)
	}

	applyConnPool(sqlDB, opts, sqliteDefaultMaxOpen, sqliteDefaultMaxIdle)

	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping SQLite: %w", err)
	}

	return db, nil
}
