// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// This acceptance-only observer never retains SQL, arguments, error messages,
// identifiers, or driver objects. Unknown operation/table names are discarded.
type containerDatabaseDiagnosticEvent struct {
	Operation     string
	Table         string
	Class         string
	MySQLNumber   uint16
	ElapsedMillis int64
}

func containerDatabaseDiagnosticClassify(operation, table string, err error) (containerDatabaseDiagnosticEvent, bool) {
	event := containerDatabaseDiagnosticEvent{Operation: "other", Table: "other"}
	if err == nil || errors.Is(err, gorm.ErrRecordNotFound) {
		return event, false
	}
	switch operation {
	case "query", "create", "update", "delete", "raw", "row":
		event.Operation = operation
	}
	switch table {
	case "pipeline", "project", "pipeline_run", "job_run", "job_run_generation", "step_run", "project_artifact", "approval_request", "approval_decision", "copilot_conversation", "copilot_turn", "copilot_action_plan", "copilot_execution_intent", "user", "role", "role_grant", "permission", "role_permission_binding":
		event.Table = table
	}
	var mysqlErr *mysqldriver.MySQLError
	switch {
	case errors.As(err, &mysqlErr):
		event.Class = "mysql"
		event.MySQLNumber = mysqlErr.Number
	case errors.Is(err, context.Canceled):
		event.Class = "context_cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		event.Class = "context_deadline"
	case errors.Is(err, sql.ErrTxDone):
		event.Class = "transaction_done"
	case errors.Is(err, gorm.ErrInvalidTransaction):
		event.Class = "transaction_invalid"
	default:
		event.Class = "other"
	}
	return event, true
}

type containerDatabaseDiagnostics struct {
	mu              sync.Mutex
	started         time.Time
	events          [64]containerDatabaseDiagnosticEvent
	count           uint64
	firstContention [2]*containerDatabaseDiagnosticEvent
}

func (d *containerDatabaseDiagnostics) record(operation string, tx *gorm.DB) {
	table := ""
	if tx.Statement != nil {
		table = tx.Statement.Table
	}
	event, ok := containerDatabaseDiagnosticClassify(operation, table, tx.Error)
	if !ok {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	event.ElapsedMillis = time.Since(d.started).Milliseconds()
	// Preserve the earliest exact deadlock/lock-timeout witness even when later
	// deliberately failing scenarios fill the rolling window. At most two values.
	if event.Class == "mysql" {
		slot := -1
		if event.MySQLNumber == 1213 {
			slot = 0
		}
		if event.MySQLNumber == 1205 {
			slot = 1
		}
		if slot >= 0 && d.firstContention[slot] == nil {
			saved := event
			d.firstContention[slot] = &saved
		}
	}
	d.events[d.count%uint64(len(d.events))] = event
	d.count++
}
func (d *containerDatabaseDiagnostics) snapshot() ([]containerDatabaseDiagnosticEvent, uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	start := uint64(0)
	if d.count > uint64(len(d.events)) {
		start = d.count - uint64(len(d.events))
	}
	out := make([]containerDatabaseDiagnosticEvent, 0, d.count-start)
	for n := start; n < d.count; n++ {
		out = append(out, d.events[n%uint64(len(d.events))])
	}
	return out, start
}

// Install before starting any workers: GORM callback registration itself is not
// concurrency safe. Sessions (including historyDB's silent logger) share these
// callbacks. After("*") also observes default write begin/commit errors. Explicit
// db.Transaction begin/commit failures and delayed sql.Row.Scan failures do not
// expose a callback in GORM; this observer does not claim to capture those.
func installContainerDatabaseDiagnostics(t *testing.T, db *gorm.DB) *containerDatabaseDiagnostics {
	t.Helper()
	d := &containerDatabaseDiagnostics{started: time.Now()}
	callbacks := db.Callback()
	registrations := []struct {
		operation string
		register  func(string, func(*gorm.DB)) error
	}{
		{"query", callbacks.Query().After("*").Register},
		{"create", callbacks.Create().After("*").Register},
		{"update", callbacks.Update().After("*").Register},
		{"delete", callbacks.Delete().After("*").Register},
		{"raw", callbacks.Raw().After("*").Register},
		{"row", callbacks.Row().After("*").Register},
	}
	for _, registration := range registrations {
		operation := registration.operation
		if err := registration.register("acceptance:safe_database_diagnostic", func(tx *gorm.DB) { d.record(operation, tx) }); err != nil {
			t.Fatal("register acceptance database diagnostic")
		}
	}

	return d
}

func (d *containerDatabaseDiagnostics) contentionSnapshot() []containerDatabaseDiagnosticEvent {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]containerDatabaseDiagnosticEvent, 0, 2)
	for _, event := range d.firstContention {
		if event != nil {
			out = append(out, *event)
		}
	}
	return out
}

func (d *containerDatabaseDiagnostics) reportFailure(t *testing.T) {
	if !t.Failed() {
		return
	}
	for _, event := range d.contentionSnapshot() {
		t.Logf("first database contention elapsed_ms=%d operation=%s table=%s class=%s mysql_number=%d", event.ElapsedMillis, event.Operation, event.Table, event.Class, event.MySQLNumber)
	}
	events, dropped := d.snapshot()
	t.Logf("bounded database diagnostics: retained=%d dropped=%d", len(events), dropped)
	for _, event := range events {
		t.Logf("database diagnostic elapsed_ms=%d operation=%s table=%s class=%s mysql_number=%d", event.ElapsedMillis, event.Operation, event.Table, event.Class, event.MySQLNumber)
	}
}

func TestContainerDatabaseDiagnosticClassifier(t *testing.T) {
	const secret = "SECRET password=https://private.invalid/token?credential=hidden SELECT confidential"
	tests := []struct {
		name   string
		err    error
		class  string
		number uint16
		keep   bool
	}{
		{"nil", nil, "", 0, false},
		{"missing", fmt.Errorf("%s: %w", secret, gorm.ErrRecordNotFound), "", 0, false},
		{"deadlock", fmt.Errorf("%s: %w", secret, &mysqldriver.MySQLError{Number: 1213, Message: secret}), "mysql", 1213, true},
		{"lock_wait", &mysqldriver.MySQLError{Number: 1205, Message: secret}, "mysql", 1205, true},
		{"unknown_mysql", &mysqldriver.MySQLError{Number: 9999, Message: secret}, "mysql", 9999, true},
		{"cancelled", fmt.Errorf("%s: %w", secret, context.Canceled), "context_cancelled", 0, true},
		{"deadline", context.DeadlineExceeded, "context_deadline", 0, true},
		{"transaction", sql.ErrTxDone, "transaction_done", 0, true},
		{"invalid_transaction", gorm.ErrInvalidTransaction, "transaction_invalid", 0, true},
		{"unknown", errors.New(secret), "other", 0, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event, keep := containerDatabaseDiagnosticClassify(secret, secret, test.err)
			want := containerDatabaseDiagnosticEvent{Operation: "other", Table: "other", Class: test.class, MySQLNumber: test.number}
			if keep != test.keep || event != want {
				t.Fatal("classifier did not retain only permitted metadata")
			}
		})
	}
	for _, operation := range []string{"query", "create", "update", "delete", "raw", "row"} {
		event, _ := containerDatabaseDiagnosticClassify(operation, "copilot_turn", errors.New(secret))
		if event.Operation != operation || event.Table != "copilot_turn" {
			t.Fatal("whitelisted metadata missing")
		}
	}
}
func TestContainerDatabaseDiagnosticRingBound(t *testing.T) {
	d := &containerDatabaseDiagnostics{started: time.Now()}
	for n := 0; n < 80; n++ {
		d.record("query", &gorm.DB{Error: &mysqldriver.MySQLError{Number: uint16(1200 + n), Message: "secret"}})
	}
	events, dropped := d.snapshot()
	if len(events) != 64 || dropped != 16 || events[0].MySQLNumber != 1216 || events[63].MySQLNumber != 1279 {
		t.Fatal("diagnostic ring is not bounded and ordered")
	}
}

func TestContainerDatabaseDiagnosticSilentSession(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	d := installContainerDatabaseDiagnostics(t, db)
	var rows []map[string]any
	err = db.Session(&gorm.Session{NewDB: true, Logger: logger.Default.LogMode(logger.Silent)}).Table("copilot_turn").Find(&rows).Error
	if err == nil {
		t.Fatal("expected missing table")
	}
	events, dropped := d.snapshot()
	if dropped != 0 || len(events) != 1 || events[0].Operation != "query" || events[0].Table != "copilot_turn" || events[0].Class != "other" {
		t.Fatal("silent session lost bounded diagnostic callback")
	}
}

func TestContainerDatabaseDiagnosticPreservesFirstContention(t *testing.T) {
	d := &containerDatabaseDiagnostics{started: time.Now()}
	for _, number := range []uint16{1213, 1205} {
		d.record("query", &gorm.DB{Error: &mysqldriver.MySQLError{Number: number, Message: "secret"}})
	}
	for n := 0; n < 100; n++ {
		d.record("update", &gorm.DB{Error: &mysqldriver.MySQLError{Number: 1213, Message: "later secret"}})
	}
	first := d.contentionSnapshot()
	if len(first) != 2 || first[0].MySQLNumber != 1213 || first[1].MySQLNumber != 1205 || first[0].Operation != "query" || first[1].Operation != "query" {
		t.Fatal("first contention witness was replaced")
	}
}
