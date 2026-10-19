// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// This acceptance-only observer never retains SQL, arguments, error messages,
// user identifiers, or driver objects. Only static schema names are retained.
type containerDatabaseDiagnosticEvent struct {
	Operation     string
	Table         string
	Column        string
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
	case "audit_log", "step_run_logs", "inbox_delivery", "inbox_work", "inbox_cursor", "inbox_sweep", "pipeline", "project", "pipeline_run", "job_run", "job_run_generation", "step_run", "project_artifact", "approval_request", "approval_decision", "copilot_conversation", "copilot_turn", "copilot_action_plan", "copilot_execution_intent", "user", "role", "role_grant", "permission", "role_permission_binding":
		event.Table = table
	}
	var mysqlErr *mysqldriver.MySQLError
	switch {
	case errors.As(err, &mysqlErr):
		event.Class = "mysql"
		event.MySQLNumber = mysqlErr.Number
		if mysqlErr.Number == 1406 {
			for _, column := range []string{"reason", "scope_id", "scope_type", "actor_user_id", "result", "action", "resource_id", "resource_type", "log_id", "step_run_id", "plugin_name", "stream", "level", "content", "recipient_id", "source_id", "event_kind", "source_kind", "status", "error_code", "owner_user_id", "request_id", "intent_id", "decision_id"} {
				suffix, ok := strings.CutPrefix(mysqlErr.Message, "Data too long for column '"+column+"' at row ")
				if !ok || suffix == "" || len(suffix) > 10 {
					continue
				}
				digits := true
				for _, c := range suffix {
					if c < '0' || c > '9' {
						digits = false
					}
				}
				if digits {
					event.Column = column
					break
				}
			}
		}
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
	mu                sync.Mutex
	started           time.Time
	events            [64]containerDatabaseDiagnosticEvent
	count             uint64
	firstContention   [2]*containerDatabaseDiagnosticEvent
	firstSchemaLength *containerDatabaseDiagnosticEvent
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
		if event.MySQLNumber == 1406 && d.firstSchemaLength == nil {
			saved := event
			d.firstSchemaLength = &saved
		}
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

func (d *containerDatabaseDiagnostics) schemaLengthSnapshot() *containerDatabaseDiagnosticEvent {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.firstSchemaLength == nil {
		return nil
	}
	copy := *d.firstSchemaLength
	return &copy
}
func (d *containerDatabaseDiagnostics) reportFailure(t *testing.T) {
	if event := d.schemaLengthSnapshot(); event != nil {
		t.Errorf("unexpected database schema-length rejection operation=%s table=%s column=%s mysql_number=%d", event.Operation, event.Table, event.Column, event.MySQLNumber)
	}
	if !t.Failed() {
		return
	}
	for _, event := range d.contentionSnapshot() {
		t.Logf("first database contention elapsed_ms=%d operation=%s table=%s column=%s class=%s mysql_number=%d", event.ElapsedMillis, event.Operation, event.Table, event.Column, event.Class, event.MySQLNumber)
	}
	events, dropped := d.snapshot()
	t.Logf("bounded database diagnostics: retained=%d dropped=%d", len(events), dropped)
	for _, event := range events {
		t.Logf("database diagnostic elapsed_ms=%d operation=%s table=%s column=%s class=%s mysql_number=%d", event.ElapsedMillis, event.Operation, event.Table, event.Column, event.Class, event.MySQLNumber)
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

func TestContainerDatabaseDiagnosticLengthColumnIsStaticOnly(t *testing.T) {
	for _, input := range []struct{ message, want string }{
		{"Data too long for column 'action' at row 1", "action"},
		{"Data too long for column 'event_kind' at row 99", "event_kind"},
		{"Data too long for column 'private_secret' at row 1", ""},
		{"Data too long for column 'action' at row 1 secret", ""},
		{"secret Data too long for column 'action' at row 1", ""},
		{"Data too long for column 'action' at row ", ""},
	} {
		event, _ := containerDatabaseDiagnosticClassify("create", "audit_log", &mysqldriver.MySQLError{Number: 1406, Message: input.message})
		if event.Column != input.want || event.Table != "audit_log" {
			t.Fatal("length diagnostic retained non-static metadata or lost permitted schema")
		}
	}
}

func TestContainerDatabaseDiagnosticSchemaLengthCannotDisappear(t *testing.T) {
	d := &containerDatabaseDiagnostics{started: time.Now()}
	d.record("create", &gorm.DB{Error: &mysqldriver.MySQLError{Number: 1406, Message: "Data too long for column 'action' at row 1"}})
	for n := 0; n < 80; n++ {
		d.record("query", &gorm.DB{Error: context.Canceled})
	}
	first := d.schemaLengthSnapshot()
	if first == nil || first.MySQLNumber != 1406 || first.Column != "action" {
		t.Fatal("schema rejection was dropped")
	}
	first.Column = "changed"
	if d.schemaLengthSnapshot().Column != "action" {
		t.Fatal("diagnostic alias mutation")
	}
}
