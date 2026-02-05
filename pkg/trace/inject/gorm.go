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

package inject

import (
	"context"
	"fmt"
	"time"

	tracecontext "github.com/arcentrix/arcentra/pkg/trace/context"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"
)

const (
	gormTracerName = "github.com/arcentrix/arcentra/pkg/trace/inject/gorm"
)

var (
	gormTracer = otel.Tracer(gormTracerName)
)

// GormPlugin implements gorm.Plugin interface for OpenTelemetry tracing
type GormPlugin struct {
	// WithQuery enables SQL query tracing
	WithQuery bool
	// WithRows enables rows affected tracing
	WithRows bool
}

// Name returns the plugin name
func (p *GormPlugin) Name() string {
	return "opentelemetry"
}

// Initialize initializes the plugin
func (p *GormPlugin) Initialize(db *gorm.DB) error {
	// Register before callbacks
	db.Callback().Create().Before("gorm:create").Register("opentelemetry:before", p.beforeCallback)
	db.Callback().Query().Before("gorm:query").Register("opentelemetry:before", p.beforeCallback)
	db.Callback().Update().Before("gorm:update").Register("opentelemetry:before", p.beforeCallback)
	db.Callback().Delete().Before("gorm:delete").Register("opentelemetry:before", p.beforeCallback)
	db.Callback().Row().Before("gorm:row").Register("opentelemetry:before", p.beforeCallback)
	db.Callback().Raw().Before("gorm:raw").Register("opentelemetry:before", p.beforeCallback)

	// Register after callbacks
	db.Callback().Create().After("gorm:create").Register("opentelemetry:after", p.afterCallback)
	db.Callback().Query().After("gorm:query").Register("opentelemetry:after", p.afterCallback)
	db.Callback().Update().After("gorm:update").Register("opentelemetry:after", p.afterCallback)
	db.Callback().Delete().After("gorm:delete").Register("opentelemetry:after", p.afterCallback)
	db.Callback().Row().After("gorm:row").Register("opentelemetry:after", p.afterCallback)
	db.Callback().Raw().After("gorm:raw").Register("opentelemetry:after", p.afterCallback)

	return nil
}

// beforeCallback is called before executing a GORM operation
func (p *GormPlugin) beforeCallback(db *gorm.DB) {
	if db.Statement == nil {
		return
	}

	// Get context from statement, or use goroutine context if not available
	ctx := db.Statement.Context
	if ctx == nil {
		// If no context in statement, try to get from goroutine context
		if goroutineCtx := tracecontext.GetContext(); goroutineCtx != nil {
			ctx = goroutineCtx
		} else {
			ctx = context.Background()
		}
	}

	// Ensure trace context is propagated (from goroutine context if needed)
	ctx = tracecontext.ContextWithSpan(ctx)

	operation := getOperationName(db)
	spanName := fmt.Sprintf("gorm.%s", operation)
	ctx, span := gormTracer.Start(ctx, spanName, trace.WithSpanKind(trace.SpanKindClient))

	// Store span and start time in context
	db.Statement.Context = context.WithValue(ctx, "opentelemetry.span", span)
	db.Statement.Context = context.WithValue(db.Statement.Context, "opentelemetry.start", time.Now())

	// Set database attributes (only set non-empty values to avoid null in trace data)
	attrs := []attribute.KeyValue{
		attribute.String("db.system", "mysql"),
		attribute.String("db.operation", operation),
	}

	// Set SQL query if enabled and not empty
	if p.WithQuery {
		if statement := gormStatementFromDB(db); statement != "" {
			attrs = append(attrs, attribute.String("db.statement", statement))
		}
	}

	// Set table name if available and not empty
	if db.Statement != nil && db.Statement.Schema != nil && db.Statement.Schema.Table != "" {
		attrs = append(attrs, attribute.String("db.sql.table", db.Statement.Schema.Table))
	}

	span.SetAttributes(attrs...)
}

// afterCallback is called after executing a GORM operation
func (p *GormPlugin) afterCallback(db *gorm.DB) {
	if db.Statement == nil || db.Statement.Context == nil {
		return
	}

	span, ok := db.Statement.Context.Value("opentelemetry.span").(trace.Span)
	if !ok {
		return
	}
	defer span.End()

	start, ok := db.Statement.Context.Value("opentelemetry.start").(time.Time)
	if ok {
		duration := time.Since(start)
		span.SetAttributes(
			attribute.Int64("db.duration_ms", duration.Milliseconds()),
			attribute.Float64("db.duration_seconds", duration.Seconds()),
		)
	}

	// Set rows affected if enabled
	if p.WithRows && db.Statement.RowsAffected > 0 {
		span.SetAttributes(attribute.Int64("db.rows_affected", db.Statement.RowsAffected))
	}

	// Ensure SQL statement is recorded after execution
	if p.WithQuery {
		if statement := gormStatementFromDB(db); statement != "" {
			span.SetAttributes(attribute.String("db.statement", statement))
		}
	}

	// Set error status if operation failed
	if err := db.Error; err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
	} else {
		span.SetStatus(codes.Ok, "")
	}
}

// getOperationName extracts operation name from GORM statement
func getOperationName(db *gorm.DB) string {
	if db.Statement == nil {
		return "query"
	}

	// Try to get operation from SQL statement type
	sql := db.Statement.SQL.String()
	if len(sql) > 0 {
		upperSQL := ""
		if len(sql) >= 6 {
			upperSQL = sql[:6]
		}
		if len(upperSQL) >= 6 {
			if upperSQL[0:6] == "INSERT" || upperSQL[0:6] == "insert" {
				return "create"
			}
			if upperSQL[0:6] == "UPDATE" || upperSQL[0:6] == "update" {
				return "update"
			}
			if upperSQL[0:6] == "DELETE" || upperSQL[0:6] == "delete" {
				return "delete"
			}
		}
		if len(sql) >= 6 && (sql[0] == 'S' || sql[0] == 's') {
			return "query"
		}
	}

	return "query"
}

func gormStatementFromDB(db *gorm.DB) string {
	if db == nil || db.Statement == nil {
		return ""
	}
	sql := db.Statement.SQL.String()
	if sql == "" {
		return ""
	}
	if db.Dialector != nil {
		return db.Dialector.Explain(sql, db.Statement.Vars...)
	}
	return sql
}

// RegisterGormPlugin registers the OpenTelemetry plugin to GORM database instance
func RegisterGormPlugin(db *gorm.DB, withQuery bool, withRows bool) error {
	plugin := &GormPlugin{
		WithQuery: withQuery,
		WithRows:  withRows,
	}
	return db.Use(plugin)
}
