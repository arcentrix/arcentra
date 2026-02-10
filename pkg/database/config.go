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
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

const (
	dataTablePrefix = "t_"
)

// DatabaseSourceConfig represents a single database source/replica configuration
type DatabaseSourceConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	DBName   string
}

// MySQLConfig represents MySQL data source configuration
type MySQLConfig struct {
	Host     string `mapstructure:"host"`
	Port     string `mapstructure:"port"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
	DBName   string `mapstructure:"dbname"`
	// Primary and Replicas for DBResolver support
	// If Primary is empty, use Host/Port/User/Password/DBName as the default source
	// If Replicas is empty, no read-write separation will be configured
	Primary  []DatabaseSourceConfig `mapstructure:"primary"`
	Replicas []DatabaseSourceConfig `mapstructure:"replicas"`
}

// Database represents the database configuration with common settings and data sources
type Database struct {
	// Common configuration for all data sources
	OutPut       bool `mapstructure:"output"`
	MaxOpenConns int  `mapstructure:"maxOpenConns"`
	MaxIdleConns int  `mapstructure:"maxIdleConns"`
	MaxLifetime  int  `mapstructure:"maxLifeTime"`
	MaxIdleTime  int  `mapstructure:"maxIdleTime"`
	// Data source configurations
	MySQL MySQLConfig `mapstructure:"mysql"`
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

// buildMySQLDSN builds MySQL DSN string from configuration
func buildMySQLDSN(user, password, host, port, db string) string {
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		user, password, host, port, db)
}

// buildDialectors converts DatabaseSourceConfig slice to gorm.Dialector slice
func buildDialectors(configs []DatabaseSourceConfig) ([]gorm.Dialector, error) {
	if len(configs) == 0 {
		return nil, nil
	}
	dialectors := make([]gorm.Dialector, 0, len(configs))
	for _, c := range configs {
		if c.Host == "" || c.User == "" || c.DBName == "" {
			return nil, fmt.Errorf("incomplete database source config: host, user, and dbname are required")
		}
		port := c.Port
		if port == "" {
			port = "3306"
		}
		dsn := buildMySQLDSN(c.User, c.Password, c.Host, port, c.DBName)
		dialectors = append(dialectors, mysql.Open(dsn))
	}
	return dialectors, nil
}
