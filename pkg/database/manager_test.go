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
	"testing"
)

func TestNewManager_SQLiteOnly(t *testing.T) {
	cfg := Database{
		Driver:  "sqlite",
		SQLite:  SQLiteConfig{DSN: "file::memory:?cache=shared"},
		Options: DatabaseOptions{MaxOpenConns: 1, MaxIdleConns: 1},
	}
	m, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer func() { _ = m.Close() }()

	if m.Default() == nil {
		t.Error("Default() should not be nil")
	}
	if m.SQLite() == nil {
		t.Error("SQLite() should not be nil")
	}
	if m.MySQL() != nil {
		t.Error("MySQL() should be nil when using SQLite only")
	}

	sqlDB, err := m.SQLite().DB()
	if err != nil {
		t.Fatalf("SQLite().DB(): %v", err)
	}
	if err := sqlDB.Ping(); err != nil {
		t.Errorf("SQLite Ping: %v", err)
	}
}

func TestNewManager_SQLiteDriverButNoDSN(t *testing.T) {
	cfg := Database{
		Driver: "sqlite",
		SQLite: SQLiteConfig{DSN: ""},
	}
	_, err := NewManager(cfg)
	if err == nil {
		t.Error("expected error when driver is sqlite but dsn is empty")
	}
}

func TestNewManager_NoDatabaseConfigured(t *testing.T) {
	cfg := Database{
		Driver: "mysql",
		MySQL:  MySQLConfig{},
	}
	_, err := NewManager(cfg)
	if err == nil {
		t.Error("expected error when mysql is not configured")
	}
}
