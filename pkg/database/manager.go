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
	"cmp"
	"database/sql"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/trace/inject"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

// Manager defines the unified database interface for managing database connections
type Manager interface {
	// MySQL returns the MySQL database connection (may be nil if using SQLite only)
	MySQL() *gorm.DB
	// SQLite returns the SQLite database connection (may be nil if using MySQL only)
	SQLite() *gorm.DB
	// Default returns the default database connection for the configured driver
	Default() *gorm.DB
	// Close closes all database connections
	Close() error
}

// managerImpl implements the Manager interface
type managerImpl struct {
	mysql  *gorm.DB
	sqlite *gorm.DB
	driver string // "mysql" or "sqlite", used by Default()
}

// MySQL returns the MySQL database connection
func (m *managerImpl) MySQL() *gorm.DB {
	return m.mysql
}

// SQLite returns the SQLite database connection
func (m *managerImpl) SQLite() *gorm.DB {
	return m.sqlite
}

// Default returns the default database connection
func (m *managerImpl) Default() *gorm.DB {
	if m.driver == "sqlite" && m.sqlite != nil {
		return m.sqlite
	}
	return m.mysql
}

// Close closes all database connections
func (m *managerImpl) Close() error {
	var errs []error

	if m.mysql != nil {
		sqlDB, err := m.mysql.DB()
		if err == nil {
			if err := sqlDB.Close(); err != nil {
				errs = append(errs, fmt.Errorf("failed to close MySQL: %w", err))
			}
		}
	}
	if m.sqlite != nil {
		sqlDB, err := m.sqlite.DB()
		if err == nil {
			if err := sqlDB.Close(); err != nil {
				errs = append(errs, fmt.Errorf("failed to close SQLite: %w", err))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing databases: %v", errs)
	}

	return nil
}

// NewManager creates a new database manager with MySQL and/or SQLite connections
func NewManager(cfg Database) (Manager, error) {
	m := &managerImpl{}

	driver := cmp.Or(cfg.Driver, "mysql")
	opts := cfg.Options
	hasMySQL := cfg.MySQL.DSN != ""
	hasSQLite := cfg.SQLite.DSN != ""

	if driver == "sqlite" && !hasSQLite {
		return nil, fmt.Errorf("database driver is sqlite but sqlite.dsn is empty")
	}
	if driver == "mysql" && !hasMySQL {
		return nil, fmt.Errorf("database driver is mysql but mysql.dsn is empty")
	}
	if !hasMySQL && !hasSQLite {
		return nil, fmt.Errorf("no database configured: set either mysql.dsn or sqlite.dsn")
	}

	if hasMySQL {
		mysqlDB, err := newMySQLConnection(cfg.MySQL, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to connect MySQL: %w", err)
		}
		m.mysql = mysqlDB
		log.Info("MySQL database connected successfully")
		if err := inject.RegisterGormPlugin(m.mysql, false, false); err != nil {
			log.Warnw("failed to register OpenTelemetry gorm plugin (mysql)", "error", err)
		}
	}

	if hasSQLite {
		sqliteDB, err := newSQLiteConnection(cfg.SQLite, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to connect SQLite: %w", err)
		}
		m.sqlite = sqliteDB
		log.Info("SQLite database connected successfully")
		if err := inject.RegisterGormPlugin(m.sqlite, false, false); err != nil {
			log.Warnw("failed to register OpenTelemetry gorm plugin (sqlite)", "error", err)
		}
	}

	m.driver = driver
	return m, nil
}

// buildGormLogger returns a GORM logger based on options (used by MySQL and SQLite)
func buildGormLogger(opts Options) gormlogger.Interface {
	logConfig := gormlogger.Config{
		SlowThreshold:             time.Second,
		LogLevel:                  gormlogger.Silent,
		Colorful:                  false,
		IgnoreRecordNotFoundError: true,
		ParameterizedQueries:      true,
	}
	if opts.OutPut {
		return NewGormLoggerAdapter(logConfig, gormlogger.Info)
	}
	return gormlogger.Default.LogMode(gormlogger.Silent)
}

// defaultGormConfig returns common GORM config (logger + naming strategy)
func defaultGormConfig(opts Options) *gorm.Config {
	return &gorm.Config{
		Logger: buildGormLogger(opts),
		NamingStrategy: schema.NamingStrategy{
			TablePrefix:   dataTablePrefix,
			SingularTable: true,
		},
	}
}

// applyConnPool applies connection pool settings to the underlying sql.DB
func applyConnPool(sqlDB *sql.DB, opts Options, defaultMaxOpen, defaultMaxIdle int) {
	maxOpen := opts.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = defaultMaxOpen
	}
	maxIdle := opts.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = defaultMaxIdle
	}
	sqlDB.SetMaxOpenConns(maxOpen)
	sqlDB.SetMaxIdleConns(maxIdle)
	sqlDB.SetConnMaxLifetime(GetConnMaxLifetime(opts.MaxLifetime))
	sqlDB.SetConnMaxIdleTime(GetConnMaxIdleTime(opts.MaxIdleTime))
}
