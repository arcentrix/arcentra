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
	"database/sql"
	"fmt"

	"gorm.io/gorm"
)

// IDatabase defines database interface for backward compatibility
// It provides access to the default database connection (MySQL or SQLite per config)
type IDatabase interface {
	// Database returns the default *gorm.DB (MySQL or SQLite)
	Database() *gorm.DB

	// SQL returns the underlying *sql.DB from the default connection.
	// The returned handle shares the same connection pool and lifecycle as Database().
	SQL() (*sql.DB, error)
}

// databaseAdapter adapts Manager to IDatabase interface
type databaseAdapter struct {
	manager Manager
}

// NewDatabaseAdapter creates an IDatabase adapter from Manager
func NewDatabaseAdapter(manager Manager) IDatabase {
	return &databaseAdapter{manager: manager}
}

// Database returns the default database connection
func (d *databaseAdapter) Database() *gorm.DB {
	return d.manager.Default()
}

// SQL extracts the underlying *sql.DB from the default GORM connection.
func (d *databaseAdapter) SQL() (*sql.DB, error) {
	gormDB := d.manager.Default()
	if gormDB == nil {
		return nil, fmt.Errorf("database: default connection is nil")
	}
	return gormDB.DB()
}
