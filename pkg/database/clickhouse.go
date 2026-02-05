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

package database

import (
	"context"
	"fmt"
	"time"

	chgorm "gorm.io/driver/clickhouse"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

const (
	logTablePrefix = "l_"
)

// NewClickHouseConnection creates a ClickHouse GORM connection
// This is the unified function for creating ClickHouse connections using GORM
func NewClickHouseConnection(chCfg ClickHouseConfig, commonCfg Database) (*gorm.DB, error) {
	// Build ClickHouse DSN
	port := chCfg.Port
	if port == 0 {
		port = 9000 // Default ClickHouse port
	}

	// Set default timeout values if not configured
	dialTimeout := chCfg.DialTimeout
	if dialTimeout == 0 {
		dialTimeout = 10 // Default 10 seconds
	}
	readTimeout := chCfg.ReadTimeout
	if readTimeout == 0 {
		readTimeout = 20 // Default 20 seconds
	}

	dsn := fmt.Sprintf("clickhouse://%s:%s@%s:%d/%s?dial_timeout=%ds&read_timeout=%ds",
		chCfg.Username, chCfg.Password, chCfg.Host, port, chCfg.DBName, dialTimeout, readTimeout)

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

	db, err := gorm.Open(chgorm.Open(dsn), &gorm.Config{
		Logger: gormLogger,
		NamingStrategy: schema.NamingStrategy{
			TablePrefix:   logTablePrefix,
			SingularTable: true,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open ClickHouse connection: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get underlying sql.DB handle: %w", err)
	}

	// Set connection pool settings from common configuration
	sqlDB.SetMaxOpenConns(commonCfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(commonCfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(GetConnMaxLifetime(commonCfg.MaxLifetime))
	sqlDB.SetConnMaxIdleTime(GetConnMaxIdleTime(commonCfg.MaxIdleTime))

	// Ping ClickHouse connection
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping ClickHouse: %w", err)
	}

	return db, nil
}
