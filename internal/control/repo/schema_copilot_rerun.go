// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

var errRerunSchemaShape = errors.New("copilot rerun partial or unknown schema requires operator review")
var errRerunSchemaUnavailable = errors.New("copilot rerun schema preflight unavailable")

const rerunStepJobIndex = "idx_step_run_job_run"
const rerunStepArtifactJobIndex = "idx_step_run_artifact_job_run"
const rerunProjectArtifactJobIndex = "idx_project_artifact_job_run"

func copilotRerunSchemaModels() []any {
	return []any{&model.CopilotDefinitionSnapshot{}, &model.CopilotRerunIntent{}, &model.CopilotRerunAdmissionGate{}, &model.CopilotRerunSchedule{}}
}

type rerunSchemaColumn struct {
	ColumnName, ColumnType, IsNullable string
	ColumnDefault                      sql.NullString
	Extra, GenerationExpression        string
	CollationName                      sql.NullString
}
type rerunSchemaIndex struct {
	IndexName             string
	NonUnique, SeqInIndex int
	ColumnName            sql.NullString
	SubPart               sql.NullInt64
	Collation             sql.NullString
	IndexType, IsVisible  string
	Expression            sql.NullString
}
type rerunSchemaMigration struct{ addStepJobIndex, addStepArtifactJobIndex, addProjectArtifactJobIndex, addAdmissionNonce bool }

// This is a read-only plan. MigrateSchema applies it only after EVERY schema
// preflight has succeeded. MySQL DDL interruption is never treated as success.
func preflightCopilotRerunSchema(ctx context.Context, db *gorm.DB) (*rerunSchemaMigration, error) {
	db = db.WithContext(ctx).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	// MySQL HasTable ignores views and query failures; neither is absence.
	// Enumerate relevant objects before planning any index DDL.
	objects := map[string]bool{}
	if db.Dialector.Name() == "mysql" {
		var rows []struct{ TableName, TableType, Engine string }
		names := []string{"pipeline_run", "job_run", "step_run", "step_run_artifact", "project_artifact", "copilot_definition_snapshot", "copilot_rerun_intent", "copilot_rerun_admission_gate", "copilot_rerun_schedule"}
		if err := db.Raw("SELECT TABLE_NAME AS table_name,TABLE_TYPE AS table_type,COALESCE(ENGINE,'') AS engine FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name IN ?", names).Scan(&rows).Error; err != nil {
			return nil, errRerunSchemaUnavailable
		}
		expectedNames := make(map[string]bool, len(names))
		for _, name := range names {
			expectedNames[name] = true
		}
		for _, row := range rows {
			if !expectedNames[row.TableName] || row.TableType != "BASE TABLE" || row.Engine != "InnoDB" || objects[row.TableName] {
				return nil, errRerunSchemaShape
			}
			objects[row.TableName] = true
		}
	}
	hasTable := func(entity any) bool {
		if db.Dialector.Name() != "mysql" {
			return db.Migrator().HasTable(entity)
		}
		stmt := &gorm.Statement{DB: db}
		if stmt.Parse(entity) != nil {
			return false
		}
		return objects[stmt.Table]
	}
	plan := &rerunSchemaMigration{}
	tables := copilotRerunSchemaModels()
	present := 0
	for _, entity := range tables {
		if hasTable(entity) {
			present++
		}
	}
	if present != 0 && present != len(tables) {
		return nil, errRerunSchemaShape
	}
	for _, entry := range []struct {
		entity any
		fields []string
	}{
		{&model.PipelineRun{}, []string{"copilot_rerun_intent_id", "copilot_rerun_gate_id"}},
		{&model.JobRun{}, []string{"copilot_rerun_intent_id", "copilot_rerun_gate_id", "expected_source_commit"}},
	} {
		if !hasTable(entry.entity) {
			if present != 0 {
				return nil, errRerunSchemaShape
			}
			continue
		}
		stmt := &gorm.Statement{DB: db}
		if stmt.Parse(entry.entity) != nil {
			return nil, errRerunSchemaUnavailable
		}
		var columns map[string]rerunSchemaColumn
		if db.Dialector.Name() == "mysql" {
			var err error
			columns, err = rerunMySQLColumns(db, stmt.Table)
			if err != nil {
				return nil, err
			}
		}
		count := 0
		for _, field := range entry.fields {
			if db.Dialector.Name() == "mysql" {
				if _, ok := columns[field]; ok {
					count++
				}
			} else if db.Migrator().HasColumn(entry.entity, field) {
				count++
			}
		}
		if count != 0 && count != len(entry.fields) || present != 0 && count != len(entry.fields) || present == 0 && count != 0 {
			return nil, errRerunSchemaShape
		}
		if count == 0 {
			continue
		}
		if db.Dialector.Name() == "mysql" {
			for _, name := range entry.fields {
				column, ok := columns[name]
				if !ok || !rerunMarkerColumnValid(column) {
					return nil, errRerunSchemaShape
				}
			}
		}
		var n int64
		predicates := make([]string, 0, len(entry.fields))
		for _, field := range entry.fields {
			predicates = append(predicates, field+" IS NULL")
		}
		if err := db.Model(entry.entity).Where(strings.Join(predicates, " OR ")).Count(&n).Error; err != nil {
			return nil, errRerunSchemaUnavailable
		}
		if n != 0 {
			return nil, errRerunSchemaShape
		}
	}
	if present != 0 && !hasTable(&model.StepRun{}) {
		return nil, errRerunSchemaShape
	}
	if hasTable(&model.StepRun{}) {
		if db.Dialector.Name() != "mysql" && !db.Migrator().HasColumn(&model.StepRun{}, "job_run_id") {
			return nil, errRerunSchemaShape
		}
		if db.Dialector.Name() == "mysql" {
			columns, err := rerunMySQLColumns(db, "step_run")
			if err != nil {
				return nil, err
			}
			indexes, err := rerunMySQLIndexes(db, "step_run")
			if err != nil {
				return nil, err
			}
			add, err := validateRerunStepJobShape(columns["job_run_id"], indexes)
			if err != nil {
				return nil, err
			}
			plan.addStepJobIndex = add
		} else if db.Dialector.Name() == "sqlite" {
			if db.Migrator().HasIndex(&model.StepRun{}, rerunStepJobIndex) {
				if err := rerunCheckDeclaredIndexes(db, &model.StepRun{}, rerunStepJobIndex); err != nil {
					return nil, err
				}
			}
		} else {
			return nil, errRerunSchemaShape
		}
	}
	// Artifact access indexes are additive only. Inspect both tables before any
	// planned DDL, including when the existing authority ledger is complete.
	for _, entry := range []struct {
		entity             any
		table, index, kind string
		prefix             int64
		add                *bool
	}{
		{&model.StepRunArtifact{}, "step_run_artifact", rerunStepArtifactJobIndex, "longtext", 191, &plan.addStepArtifactJobIndex},
		{&model.ProjectArtifact{}, "project_artifact", rerunProjectArtifactJobIndex, "varchar(64)", 0, &plan.addProjectArtifactJobIndex},
	} {
		if !hasTable(entry.entity) {
			continue
		}
		switch db.Dialector.Name() {
		case "mysql":
			columns, err := rerunMySQLColumns(db, entry.table)
			if err != nil {
				return nil, err
			}
			indexes, err := rerunMySQLIndexes(db, entry.table)
			if err != nil {
				return nil, err
			}
			add, err := validateRerunJobAccessShape(columns["job_run_id"], indexes, entry.index, entry.kind, entry.prefix)
			if err != nil {
				return nil, err
			}
			*entry.add = add
		case "sqlite":
			if !db.Migrator().HasColumn(entry.entity, "job_run_id") {
				return nil, errRerunSchemaShape
			}
			if db.Migrator().HasIndex(entry.entity, entry.index) {
				if err := rerunCheckDeclaredIndexes(db, entry.entity, entry.index); err != nil {
					return nil, err
				}
			}
		default:
			return nil, errRerunSchemaShape
		}
	}

	if present != 0 {
		for _, entity := range tables {
			missingNonce := false
			if _, gate := entity.(*model.CopilotRerunAdmissionGate); gate {
				if db.Dialector.Name() == "mysql" {
					columns, err := rerunMySQLColumns(db, "copilot_rerun_admission_gate")
					if err != nil {
						return nil, err
					}
					_, exists := columns["admission_nonce_digest"]
					missingNonce = !exists
				} else {
					missingNonce = !db.Migrator().HasColumn(entity, "admission_nonce_digest")
				}
			}
			if missingNonce {
				if err := preflightRerunUnclaimedNonceUpgrade(db); err != nil {
					return nil, err
				}
				plan.addAdmissionNonce = true
			}
			if err := preflightRerunLedgerShape(db, entity, missingNonce); err != nil {
				return nil, err
			}
		}
	}
	if present != 0 {
		if err := preflightRerunProtectedLinks(db); err != nil {
			return nil, err
		}
	}
	return plan, nil
}
func rerunMarkerColumnValid(c rerunSchemaColumn) bool {
	return c.ColumnType == "varchar(64)" && c.IsNullable == "NO" && c.ColumnDefault.Valid && c.ColumnDefault.String == "" && c.Extra == "" && c.GenerationExpression == "" && c.CollationName.Valid
}
func rerunMySQLColumns(db *gorm.DB, table string) (map[string]rerunSchemaColumn, error) {
	var tableRows []struct{ TableType, Engine string }
	if err := db.Raw("SELECT TABLE_TYPE AS table_type,ENGINE AS engine FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?", table).Scan(&tableRows).Error; err != nil {
		return nil, errRerunSchemaUnavailable
	}
	if len(tableRows) != 1 || tableRows[0].TableType != "BASE TABLE" || tableRows[0].Engine != "InnoDB" {
		return nil, errRerunSchemaShape
	}
	var rows []rerunSchemaColumn
	if err := db.Raw("SELECT COLUMN_NAME AS column_name,COLUMN_TYPE AS column_type,IS_NULLABLE AS is_nullable,COLUMN_DEFAULT AS column_default,EXTRA AS extra,GENERATION_EXPRESSION AS generation_expression,COLLATION_NAME AS collation_name FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=?", table).Scan(&rows).Error; err != nil {
		return nil, errRerunSchemaUnavailable
	}
	if len(rows) == 0 {
		return nil, errRerunSchemaShape
	}
	result := map[string]rerunSchemaColumn{}
	for _, row := range rows {
		if row.ColumnName == "" {
			return nil, errRerunSchemaShape
		}
		if _, exists := result[row.ColumnName]; exists {
			return nil, errRerunSchemaShape
		}
		result[row.ColumnName] = row
	}
	return result, nil
}
func rerunMySQLIndexes(db *gorm.DB, table string) ([]rerunSchemaIndex, error) {
	var rows []rerunSchemaIndex
	if err := db.Raw("SELECT INDEX_NAME AS index_name,NON_UNIQUE AS non_unique,SEQ_IN_INDEX AS seq_in_index,COLUMN_NAME AS column_name,SUB_PART AS sub_part,COLLATION AS collation,INDEX_TYPE AS index_type,IS_VISIBLE AS is_visible,EXPRESSION AS expression FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? ORDER BY INDEX_NAME,SEQ_IN_INDEX", table).Scan(&rows).Error; err != nil {
		return nil, errRerunSchemaUnavailable
	}
	return rows, nil
}
func validateRerunStepJobShape(column rerunSchemaColumn, indexes []rerunSchemaIndex) (bool, error) {
	return validateRerunJobAccessShape(column, indexes, rerunStepJobIndex, "longtext", 191)
}
func validateRerunJobAccessShape(column rerunSchemaColumn, indexes []rerunSchemaIndex, name, kind string, prefix int64) (bool, error) {
	if column.ColumnName != "job_run_id" || column.ColumnType != kind || column.IsNullable != "YES" || column.ColumnDefault.Valid || column.Extra != "" || column.GenerationExpression != "" || !column.CollationName.Valid {
		return false, errRerunSchemaShape
	}
	relevant := map[string]bool{name: true}
	for _, index := range indexes {
		if index.ColumnName.Valid && index.ColumnName.String == "job_run_id" {
			relevant[index.IndexName] = true
		}
	}
	var found []rerunSchemaIndex
	for _, index := range indexes {
		if relevant[index.IndexName] {
			found = append(found, index)
		}
	}
	if len(found) == 0 {
		return true, nil
	}
	if len(found) != 1 {
		return false, errRerunSchemaShape
	}
	i := found[0]
	if i.IndexName != name || i.NonUnique != 1 || i.SeqInIndex != 1 || !i.ColumnName.Valid || i.ColumnName.String != "job_run_id" || i.SubPart.Valid != (prefix > 0) || (i.SubPart.Valid && i.SubPart.Int64 != prefix) || !i.Collation.Valid || i.Collation.String != "A" || i.IndexType != "BTREE" || i.IsVisible != "YES" || i.Expression.Valid {
		return false, errRerunSchemaShape
	}
	return false, nil
}
func (plan *rerunSchemaMigration) apply(ctx context.Context, db *gorm.DB) error {
	if plan == nil {
		return nil
	}
	if plan.addAdmissionNonce {
		if err := db.WithContext(ctx).Migrator().AddColumn(&model.CopilotRerunAdmissionGate{}, "AdmissionNonceDigest"); err != nil {
			return errors.New("copilot rerun nonce migration failed; inspect schema before retry")
		}
	}
	// Add indexes without modifying column types, collations or historical values.
	for _, entry := range []struct {
		add bool
		ddl string
	}{
		{plan.addStepJobIndex, "ALTER TABLE `step_run` ADD INDEX `idx_step_run_job_run` (`job_run_id`(191))"},
		{plan.addStepArtifactJobIndex, "ALTER TABLE `step_run_artifact` ADD INDEX `idx_step_run_artifact_job_run` (`job_run_id`(191))"},
		{plan.addProjectArtifactJobIndex, "ALTER TABLE `project_artifact` ADD INDEX `idx_project_artifact_job_run` (`job_run_id`)"},
	} {
		if entry.add {
			if err := db.WithContext(ctx).Exec(entry.ddl).Error; err != nil {
				return errors.New("copilot rerun schema migration failed; inspect schema before retry")
			}
		}
	}
	return nil
}
func preflightRerunLedger(db *gorm.DB, entity any) error {
	return preflightRerunLedgerShape(db, entity, false)
}
func preflightRerunLedgerShape(db *gorm.DB, entity any, missingNonce bool) error {
	stmt := &gorm.Statement{DB: db}
	if stmt.Parse(entity) != nil {
		return errRerunSchemaUnavailable
	}
	if db.Dialector.Name() == "mysql" {
		columns, err := rerunMySQLColumns(db, stmt.Table)
		if err != nil {
			return err
		}
		expectedColumns := len(stmt.Schema.DBNames)
		if missingNonce {
			expectedColumns--
		}
		if len(columns) != expectedColumns {
			return errRerunSchemaShape
		}
		for _, field := range stmt.Schema.Fields {
			if field.DBName == "" || (missingNonce && field.DBName == "admission_nonce_digest") {
				continue
			}
			column, ok := columns[field.DBName]
			if !ok || !rerunLedgerColumnValid(db, field, column) {
				return errRerunSchemaShape
			}
		}
		actual, err := rerunMySQLIndexes(db, stmt.Table)
		if err != nil {
			return err
		}
		if err := validateRerunLedgerIndexes(stmt.Schema, actual); err != nil {
			return err
		}
	} else {
		for _, field := range stmt.Schema.Fields {
			if field.DBName != "" && !(missingNonce && field.DBName == "admission_nonce_digest") && !db.Migrator().HasColumn(entity, field.DBName) {
				return errRerunSchemaShape
			}
		}
		if err := rerunCheckDeclaredIndexes(db, entity, ""); err != nil {
			return err
		}
	}
	return nil
}
func rerunLedgerColumnValid(db *gorm.DB, field *schema.Field, c rerunSchemaColumn) bool {
	expectedType := strings.TrimSuffix(strings.ToLower(db.Dialector.DataTypeOf(field)), " auto_increment")
	// COLUMN_TYPE omits DDL nullability and reports BOOL using its MySQL alias.
	// Keep these conversions field-specific; nullable/default/extra remain exact below.
	if field.DataType == schema.Bool && expectedType == "boolean" {
		expectedType = "tinyint(1)"
	}
	if field.DataType == schema.Time {
		expectedType = strings.TrimSuffix(expectedType, " null")
	}
	if expectedType != strings.ToLower(c.ColumnType) || c.GenerationExpression != "" {
		return false
	}
	nullable := !field.NotNull && !field.PrimaryKey
	if (c.IsNullable != "YES" && c.IsNullable != "NO") || (c.IsNullable == "YES") != nullable {
		return false
	}
	expectedExtra := ""
	if field.AutoIncrement {
		expectedExtra = "auto_increment"
	}
	if c.Extra != expectedExtra {
		return false
	}
	if field.AutoIncrement || !field.HasDefaultValue {
		return !c.ColumnDefault.Valid
	}
	expected := field.DefaultValue
	if field.DataType == schema.Bool {
		if expected == "false" {
			expected = "0"
		} else if expected == "true" {
			expected = "1"
		}
	}
	return c.ColumnDefault.Valid && c.ColumnDefault.String == expected
}
func validateRerunLedgerIndexes(parsed *schema.Schema, actual []rerunSchemaIndex) error {
	expected := map[string][]string{"PRIMARY": {"id"}}
	unique := map[string]bool{"PRIMARY": true}
	for _, index := range parsed.ParseIndexes() {
		for _, field := range index.Fields {
			if field.Length != 0 {
				return errRerunSchemaShape
			}
			expected[index.Name] = append(expected[index.Name], field.DBName)
		}
		unique[index.Name] = index.Class == "UNIQUE"
	}
	seen := map[string]int{}
	for _, index := range actual {
		fields, ok := expected[index.IndexName]
		position := seen[index.IndexName]
		if !ok || position >= len(fields) || index.SeqInIndex != position+1 || !index.ColumnName.Valid || index.ColumnName.String != fields[position] || index.SubPart.Valid || index.Expression.Valid || index.IndexType != "BTREE" || index.IsVisible != "YES" || !index.Collation.Valid || index.Collation.String != "A" || (index.NonUnique == 0) != unique[index.IndexName] {
			return errRerunSchemaShape
		}
		seen[index.IndexName]++
	}
	for name, fields := range expected {
		if seen[name] != len(fields) {
			return errRerunSchemaShape
		}
	}
	return nil
}
func rerunCheckDeclaredIndexes(db *gorm.DB, entity any, only string) error {
	stmt := &gorm.Statement{DB: db}
	if stmt.Parse(entity) != nil {
		return errRerunSchemaUnavailable
	}
	actual, err := db.Migrator().GetIndexes(entity)
	if err != nil {
		return errRerunSchemaUnavailable
	}
	for _, expected := range stmt.Schema.ParseIndexes() {
		if only != "" && expected.Name != only {
			continue
		}
		found := false
		for _, index := range actual {
			if index.Name() != expected.Name {
				continue
			}
			columns := index.Columns()
			if len(columns) != len(expected.Fields) {
				return errRerunSchemaShape
			}
			for i, field := range expected.Fields {
				if columns[i] != field.DBName {
					return errRerunSchemaShape
				}
			}
			u, known := index.Unique()
			if !known || u != (expected.Class == "UNIQUE") {
				return errRerunSchemaShape
			}
			found = true
		}
		if !found {
			return errRerunSchemaShape
		}
	}
	return nil
}

// Validate only identity links; never read, decrypt or reconstruct historical
// definition/dispatch ciphertext during migration.
func preflightRerunProtectedLinks(db *gorm.DB) error {
	queries := []string{
		`SELECT COUNT(*) FROM pipeline_run r LEFT JOIN copilot_rerun_intent i ON i.intent_id=r.copilot_rerun_intent_id LEFT JOIN copilot_rerun_admission_gate g ON g.gate_id=r.copilot_rerun_gate_id WHERE (r.copilot_rerun_intent_id<>'' OR r.copilot_rerun_gate_id<>'') AND (i.id IS NULL OR g.id IS NULL OR HEX(i.intent_id)<>HEX(r.copilot_rerun_intent_id) OR HEX(i.new_run_id)<>HEX(r.run_id) OR HEX(i.gate_id)<>HEX(r.copilot_rerun_gate_id) OR HEX(g.run_id)<>HEX(r.run_id) OR HEX(g.gate_id)<>HEX(r.copilot_rerun_gate_id))`,
		`SELECT COUNT(*) FROM job_run j LEFT JOIN copilot_rerun_intent i ON i.intent_id=j.copilot_rerun_intent_id LEFT JOIN pipeline_run r ON r.run_id=j.pipeline_run_id WHERE (j.copilot_rerun_intent_id<>'' OR j.copilot_rerun_gate_id<>'' OR j.expected_source_commit<>'') AND (i.id IS NULL OR r.id IS NULL OR HEX(i.intent_id)<>HEX(j.copilot_rerun_intent_id) OR HEX(i.new_job_run_id)<>HEX(j.job_run_id) OR HEX(i.new_run_id)<>HEX(j.pipeline_run_id) OR HEX(i.gate_id)<>HEX(j.copilot_rerun_gate_id) OR HEX(i.expected_source_commit)<>HEX(j.expected_source_commit) OR HEX(r.copilot_rerun_intent_id)<>HEX(j.copilot_rerun_intent_id) OR HEX(r.copilot_rerun_gate_id)<>HEX(j.copilot_rerun_gate_id))`,
	}
	for _, query := range queries {
		var n int64
		if err := db.Raw(query).Scan(&n).Error; err != nil {
			return errRerunSchemaUnavailable
		}
		if n != 0 {
			return errRerunSchemaShape
		}
	}
	return nil
}

// The only recognized predecessor has never admitted execution. Empty defaults
// preserve that fact; claimed history can never be upgraded into a nonce receipt.
func preflightRerunUnclaimedNonceUpgrade(db *gorm.DB) error {
	var broken int64
	if err := db.Raw(`SELECT COUNT(*) FROM copilot_rerun_admission_gate g LEFT JOIN job_run j ON j.job_run_id=g.job_run_id LEFT JOIN pipeline_run r ON r.run_id=g.run_id LEFT JOIN copilot_rerun_schedule s ON s.schedule_id=g.schedule_id WHERE j.id IS NULL OR r.id IS NULL OR s.id IS NULL OR HEX(j.copilot_rerun_gate_id)<>HEX(g.gate_id) OR HEX(r.copilot_rerun_gate_id)<>HEX(g.gate_id) OR HEX(j.pipeline_run_id)<>HEX(g.run_id) OR HEX(s.job_run_id)<>HEX(g.job_run_id) OR HEX(s.run_id)<>HEX(g.run_id)`).Scan(&broken).Error; err != nil {
		return errRerunSchemaUnavailable
	}
	if broken != 0 {
		return errRerunSchemaShape
	}
	checks := []struct{ table, predicate string }{
		{"copilot_rerun_admission_gate", "state NOT IN ('unclaimed','closed') OR claim_id <> '' OR claimed_at IS NOT NULL"},
		{"copilot_rerun_schedule", "state NOT IN ('pending','sealed','cancelled') OR claimed_id <> '' OR claimed_at IS NOT NULL"},
		{"job_run", "copilot_rerun_gate_id <> '' AND (status IS NULL OR status NOT IN (1,6) OR claim_id <> '' OR start_time IS NOT NULL OR source_resolved_commit <> '' OR input_manifest <> '' OR input_manifest_digest <> '' OR retry_approval_id <> '' OR current_generation_id <> '' OR current_generation <> 0 OR terminal_report_digest <> '' OR cancellation_claim_id <> '')"},
		{"pipeline_run", "copilot_rerun_gate_id <> '' AND (status IS NULL OR status NOT IN (1,5) OR start_time IS NOT NULL)"},
		{"step_run", "job_run_id IN (SELECT job_run_id FROM job_run WHERE copilot_rerun_gate_id <> '') AND (status IS NULL OR status NOT IN (1,6) OR claim_id <> '' OR start_time IS NOT NULL OR terminal_report_digest <> '')"},
	}
	for _, check := range checks {
		var n int64
		if err := db.Table(check.table).Where(check.predicate).Count(&n).Error; err != nil {
			return errRerunSchemaUnavailable
		}
		if n != 0 {
			return errRerunSchemaShape
		}
	}
	return nil
}
