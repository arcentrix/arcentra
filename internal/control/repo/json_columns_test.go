package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestExecutionJSONWriteBoundaries(t *testing.T) {
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.Pipeline{}, &model.PipelineRun{}, &model.JobRun{}, &model.StepRun{}); err != nil {
		t.Fatal(err)
	}
	pipeline := &PipelineRepo{IDatabase: statusTestDB{db}}
	job := &JobRunRepo{IDatabase: statusTestDB{db}}
	step := &StepRunRepo{IDatabase: statusTestDB{db}}
	cases := []struct {
		name, table, key string
		defaults         map[string]string
		newRow           func(string) any
		create           func(any) error
		patch            func(string, map[string]any) error
	}{
		{"pipeline", "pipeline", "pipeline_id", pipelineJSONDefaults, func(id string) any { return &model.Pipeline{PipelineID: id} }, func(v any) error { return pipeline.Create(ctx, v.(*model.Pipeline)) }, func(id string, updates map[string]any) error { return pipeline.Update(ctx, id, updates) }},
		{"run", "pipeline_run", "run_id", pipelineRunJSONDefaults, func(id string) any { return &model.PipelineRun{RunID: id} }, func(v any) error { return pipeline.CreateRun(ctx, v.(*model.PipelineRun)) }, func(id string, updates map[string]any) error { return pipeline.UpdateRun(ctx, id, updates) }},
		{"job", "job_run", "job_run_id", jobRunJSONDefaults, func(id string) any { return &model.JobRun{JobRunID: id} }, func(v any) error { return job.Create(ctx, v.(*model.JobRun)) }, func(id string, updates map[string]any) error { return job.UpdateByJobRunID(ctx, id, updates) }},
		{"step", "step_run", "step_run_id", stepRunJSONDefaults, func(id string) any { return &model.StepRun{StepRunID: id} }, func(v any) error { return step.Create(ctx, v.(*model.StepRun)) }, func(id string, updates map[string]any) error { return step.PatchByStepRunID(ctx, id, updates) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// SQLite normally accepts empty strings in JSON columns. These checks
			// model strict JSON storage without changing the production schema.
			for column := range tc.defaults {
				for _, operation := range []string{"INSERT", "UPDATE"} {
					query := fmt.Sprintf("CREATE TRIGGER strict_%s_%s_%s BEFORE %s ON %s WHEN NEW.%s IS NOT NULL AND NOT json_valid(NEW.%s) BEGIN SELECT RAISE(ABORT, 'invalid JSON'); END", tc.table, column, operation, operation, tc.table, column, column)
					if err := db.Exec(query).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			row := tc.newRow("empty")
			if err := tc.create(row); err != nil {
				t.Fatal(err)
			}
			read := func(column string) sql.NullString {
				t.Helper()
				var value sql.NullString
				if err := db.Table(tc.table).Select(column).Where(tc.key+" = ?", "empty").Row().Scan(&value); err != nil {
					t.Fatal(err)
				}
				return value
			}
			for column, fallback := range tc.defaults {
				if got := read(column); !got.Valid || got.String != fallback || !json.Valid([]byte(got.String)) {
					t.Fatalf("%s default = %#v, want %s", column, got, fallback)
				}
			}
			// Preserve valid submitted documents byte-for-byte, including JSON
			// null and arrays: this repair does not impose a new data contract.
			for _, document := range []string{` {"kept":"value"} `, `[]`, `null`} {
				updates := map[string]any{}
				for column := range tc.defaults {
					updates[column] = document
				}
				before := len(updates)
				if err := tc.patch("empty", updates); err != nil {
					t.Fatal(err)
				}
				if len(updates) != before {
					t.Fatal("patch mutated caller map")
				}
				if err := tc.patch("empty", map[string]any{"status": 2}); err != nil {
					t.Fatal(err)
				}
				for column := range tc.defaults {
					if got := read(column); !got.Valid || got.String != document {
						t.Fatalf("omitted %s was overwritten: %#v", column, got)
					}
				}
			}
			for column, fallback := range tc.defaults {
				if err := tc.patch("empty", map[string]any{column: ""}); err != nil {
					t.Fatal(err)
				}
				if got := read(column); !got.Valid || got.String != fallback {
					t.Fatalf("explicit empty %s = %#v", column, got)
				}
				if err := tc.patch("empty", map[string]any{column: nil}); err != nil {
					t.Fatal(err)
				}
				if got := read(column); got.Valid {
					t.Fatalf("SQL NULL changed: %#v", got)
				}
				if err := tc.patch("empty", map[string]any{column: `{"kept":"value"}`}); err != nil {
					t.Fatal(err)
				}
				for _, invalid := range []string{"  ", "{invalid-secret-canary"} {
					updates := map[string]any{column: invalid, "status": 3}
					err := tc.patch("empty", updates)
					if err == nil || strings.Contains(err.Error(), "secret-canary") {
						t.Fatalf("invalid %s error = %v", column, err)
					}
					if got := read(column); got.String != `{"kept":"value"}` {
						t.Fatal("rejected patch modified existing JSON")
					}
					var status int
					if err := db.Table(tc.table).Select("status").Where(tc.key+" = ?", "empty").Row().Scan(&status); err != nil {
						t.Fatal(err)
					}
					if status != 2 {
						t.Fatal("rejected JSON patch changed another field")
					}
					bad := tc.newRow("invalid-" + column)
					value := reflect.ValueOf(bad).Elem()
					for i := 0; i < value.NumField(); i++ {
						if strings.Contains(value.Type().Field(i).Tag.Get("gorm"), "column:"+column+";") {
							value.Field(i).SetString(invalid)
						}
					}
					if err := tc.create(bad); err == nil {
						t.Fatalf("invalid %s creation accepted", column)
					}
					var count int64
					if err := db.Table(tc.table).Where(tc.key+" = ?", "invalid-"+column).Count(&count).Error; err != nil {
						t.Fatal(err)
					}
					if count != 0 {
						t.Fatal("rejected creation inserted a row")
					}
				}
			}
		})
	}
}

func TestNormalizeJSONUpdates(t *testing.T) {
	for _, input := range []any{`{"x":1}`, []byte(`{"x":1}`), json.RawMessage(`{"x":1}`), datatypes.JSON(`{"x":1}`)} {
		got, err := normalizeJSONUpdates(map[string]any{"ArtifactURIs": input}, jobRunJSONDefaults)
		if err != nil || got["ArtifactURIs"] != `{"x":1}` {
			t.Fatalf("encoded JSON changed: %#v, %v", got, err)
		}
	}
	if _, err := normalizeJSONUpdates(map[string]any{"env": map[string]string{"x": "y"}}, stepRunJSONDefaults); err == nil {
		t.Fatal("unencoded JSON accepted")
	}
	valid, invalid := "", "{invalid"
	if err := normalizeJSONFields(map[string]*string{"env": &valid, "args": &invalid}, stepRunJSONDefaults); err == nil {
		t.Fatal("malformed document accepted")
	}
	if valid != "" || invalid != "{invalid" {
		t.Fatal("validation failure mutated caller fields")
	}
}
