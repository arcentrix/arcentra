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

package database

import (
	"fmt"
	"time"

	"github.com/arcentrix/arcade/pkg/log"
	"github.com/arcentrix/arcade/pkg/trace/inject"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
	"gorm.io/plugin/dbresolver"
)

// Manager defines the unified database interface for managing MySQL and ClickHouse connections
type Manager interface {
	// MySQL returns the MySQL database connection
	MySQL() *gorm.DB

	// ClickHouse returns the ClickHouse database connection
	ClickHouse() *gorm.DB

	// Close closes all database connections
	Close() error
}

// managerImpl implements the Manager interface
type managerImpl struct {
	mysql      *gorm.DB
	clickHouse *gorm.DB
}

// MySQL returns the MySQL database connection
func (m *managerImpl) MySQL() *gorm.DB {
	return m.mysql
}

// ClickHouse returns the ClickHouse database connection
func (m *managerImpl) ClickHouse() *gorm.DB {
	return m.clickHouse
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

	if m.clickHouse != nil {
		sqlDB, err := m.clickHouse.DB()
		if err == nil {
			if err := sqlDB.Close(); err != nil {
				errs = append(errs, fmt.Errorf("failed to close ClickHouse: %w", err))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing databases: %v", errs)
	}

	return nil
}

// NewManager creates a new database manager with MySQL and ClickHouse connections
func NewManager(cfg Database) (Manager, error) {
	m := &managerImpl{}

	// Initialize MySQL connection
	mysqlDB, err := newMySQLConnection(cfg.MySQL, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to connect MySQL: %w", err)
	}
	m.mysql = mysqlDB
	log.Info("MySQL database connected successfully")
	if err := inject.RegisterGormPlugin(m.mysql, false, false); err != nil {
		log.Warnw("failed to register OpenTelemetry gorm plugin (mysql)", "error", err)
	}

	// Initialize ClickHouse connection if configured
	if cfg.ClickHouse.Host != "" && cfg.ClickHouse.DBName != "" && cfg.ClickHouse.Username != "" {
		chDB, err := newClickHouseConnection(cfg.ClickHouse, cfg)
		if err != nil {
			// Cleanup MySQL connection if ClickHouse fails
			if sqlDB, closeErr := mysqlDB.DB(); closeErr == nil {
				_ = sqlDB.Close()
			}
			return nil, fmt.Errorf("failed to connect ClickHouse: %w", err)
		}
		m.clickHouse = chDB
		log.Info("ClickHouse database connected successfully")
		if err := inject.RegisterGormPlugin(m.clickHouse, false, false); err != nil {
			log.Warnw("failed to register OpenTelemetry gorm plugin (clickhouse)", "error", err)
		}
	}

	return m, nil
}

// newMySQLConnection creates a MySQL connection using GORM with DBResolver support
func newMySQLConnection(mysqlCfg MySQLConfig, commonCfg Database) (*gorm.DB, error) {
	// Determine default DSN (used as primary source if no Primary configured)
	defaultDSN := buildMySQLDSN(mysqlCfg.User, mysqlCfg.Password, mysqlCfg.Host, mysqlCfg.Port, mysqlCfg.DBName)

	logConfig := gormlogger.Config{
		SlowThreshold:             time.Second,
		LogLevel:                  gormlogger.Silent,
		Colorful:                  false,
		IgnoreRecordNotFoundError: true,
		ParameterizedQueries:      true,
	}

	var gormLogger gormlogger.Interface
	if commonCfg.OutPut {
		gormLogger = NewGormLoggerAdapter(logConfig, gormlogger.Info)
	} else {
		gormLogger = gormlogger.Default.LogMode(gormlogger.Silent)
	}

	// Open primary connection
	db, err := gorm.Open(mysql.Open(defaultDSN), &gorm.Config{
		Logger: gormLogger,
		NamingStrategy: schema.NamingStrategy{
			TablePrefix:   dataTablePrefix,
			SingularTable: true,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open MySQL connection: %w", err)
	}

	// Configure DBResolver if Primary or Replicas are provided
	hasPrimary := len(mysqlCfg.Primary) > 0
	hasReplicas := len(mysqlCfg.Replicas) > 0

	if hasPrimary || hasReplicas {
		resolverConfig := dbresolver.Config{
			TraceResolverMode: commonCfg.OutPut,
		}

		// Build primary dialectors
		if hasPrimary {
			primaryDialectors, err := buildDialectors(mysqlCfg.Primary)
			if err != nil {
				return nil, fmt.Errorf("failed to build primary dialectors: %w", err)
			}
			resolverConfig.Sources = primaryDialectors
		}

		// Build replicas dialectors
		if hasReplicas {
			replicasDialectors, err := buildDialectors(mysqlCfg.Replicas)
			if err != nil {
				return nil, fmt.Errorf("failed to build replicas dialectors: %w", err)
			}
			resolverConfig.Replicas = replicasDialectors
		}

		// Register DBResolver plugin
		err = db.Use(dbresolver.Register(resolverConfig).
			SetConnMaxIdleTime(GetConnMaxIdleTime(commonCfg.MaxIdleTime)).
			SetConnMaxLifetime(GetConnMaxLifetime(commonCfg.MaxLifetime)).
			SetMaxIdleConns(commonCfg.MaxIdleConns).
			SetMaxOpenConns(commonCfg.MaxOpenConns))
		if err != nil {
			return nil, fmt.Errorf("failed to register DBResolver plugin: %w", err)
		}
	}

	// Configure connection pool for primary connection
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get underlying sql.DB handle: %w", err)
	}

	sqlDB.SetMaxOpenConns(commonCfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(commonCfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(GetConnMaxLifetime(commonCfg.MaxLifetime))
	sqlDB.SetConnMaxIdleTime(GetConnMaxIdleTime(commonCfg.MaxIdleTime))

	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping MySQL: %w", err)
	}

	if hasPrimary || hasReplicas {
		log.Info("MySQL database connected successfully with DBResolver (read-write separation enabled)")
	}

	return db, nil
}

// newClickHouseConnection creates a ClickHouse connection using GORM
func newClickHouseConnection(chCfg ClickHouseConfig, commonCfg Database) (*gorm.DB, error) {
	return NewClickHouseConnection(chCfg, commonCfg)
}
