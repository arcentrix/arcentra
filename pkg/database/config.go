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
	"time"
)

const (
	dataTablePrefix = ""
)

// Options holds common connection pool and logging options
type Options struct {
	OutPut       bool `mapstructure:"output"`
	MaxOpenConns int  `mapstructure:"maxOpenConns"`
	MaxIdleConns int  `mapstructure:"maxIdleConns"`
	MaxLifetime  int  `mapstructure:"maxLifeTime"`
	MaxIdleTime  int  `mapstructure:"maxIdleTime"`
}

// SQLiteConfig represents SQLite data source configuration (DSN only)
type SQLiteConfig struct {
	DSN string `mapstructure:"dsn"`
}

// MySQLConfig represents MySQL data source configuration (DSN only)
type MySQLConfig struct {
	DSN string `mapstructure:"dsn"`
}

// Database represents the database configuration
type Database struct {
	// Driver is the database driver: "mysql" or "sqlite". Defaults to "mysql" when empty.
	Driver string `mapstructure:"driver"`
	// Data source configurations (only the one matching driver is required)
	MySQL  MySQLConfig  `mapstructure:"mysql"`
	SQLite SQLiteConfig `mapstructure:"sqlite"`
	// Common options for connection pool and logging
	Options Options `mapstructure:"options"`
}

// GetConnMaxLifetime returns ConnMaxLifetime as time.Duration from common config
func GetConnMaxLifetime(maxLifetime int) time.Duration {
	if maxLifetime > 0 {
		return time.Duration(maxLifetime) * time.Second
	}
	return 300 * time.Second // Default 5 minutes
}

// GetConnMaxIdleTime returns ConnMaxIdleTime as time.Duration from common config
func GetConnMaxIdleTime(maxIdleTime int) time.Duration {
	if maxIdleTime > 0 {
		return time.Duration(maxIdleTime) * time.Second
	}
	return 60 * time.Second // Default 1 minute
}
