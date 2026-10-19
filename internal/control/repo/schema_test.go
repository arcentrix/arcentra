package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/driver/mysql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestMigrateSchemaIsIdempotent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	for i := 0; i < 2; i++ {
		if err := MigrateSchema(context.Background(), db); err != nil {
			t.Fatal(err)
		}
	}
	for _, entity := range []any{&model.Pipeline{}, &model.PipelineRun{}, &model.JobRun{}, &model.StepRun{}, &model.StepRunLog{}, &model.GatewayEvent{}, &model.GatewayEventCursor{}} {
		if !db.Migrator().HasTable(entity) {
			t.Fatalf("missing execution table %T", entity)
		}
	}
	var users, grants int64
	db.Model(&model.User{}).Count(&users)
	db.Model(&model.RoleGrant{}).Count(&grants)
	if users != 0 || grants != 0 {
		t.Fatal("schema migration must not grant user access")
	}
	var roles int64
	db.Model(&model.Role{}).Count(&roles)
	if roles == 0 {
		t.Fatal("built-in roles missing")
	}
}

func TestMySQLIndexedStringsHaveBoundedTypes(t *testing.T) {
	dialect := mysql.Dialector{Config: &mysql.Config{}}
	for _, value := range schemaModels() {
		parsed, err := schema.Parse(value, &sync.Map{}, schema.NamingStrategy{})
		if err != nil {
			t.Fatal(err)
		}
		for _, index := range parsed.ParseIndexes() {
			total := 0
			for _, field := range index.Fields {
				if field.Field.IndirectFieldType.Kind().String() != "string" {
					continue
				}
				kind := strings.ToLower(dialect.DataTypeOf(field.Field))
				if kind == "longtext" && parsed.Table == "audit_log" && field.DBName == "action" && index.Name == auditActionIndex && field.Length == 191 {
					total += field.Length * 4
					continue
				}
				var size int
				if _, err := fmt.Sscanf(kind, "varchar(%d)", &size); err != nil || !strings.HasPrefix(kind, "varchar(") {
					t.Errorf("%s.%s index %s uses unbounded type %s", parsed.Table, field.Name, index.Name, kind)
					continue
				}
				total += size * 4
			}
			if total > 3072 {
				t.Errorf("%s index %s exceeds MySQL utf8mb4 limit: %d", parsed.Table, index.Name, total)
			}
		}
	}
}

func TestAdministratorBootstrapIsExplicitAndOneTime(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err := MigrateSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := BootstrapAdmin(context.Background(), db, "absent"); err == nil {
		t.Fatal("nonexistent user granted access")
	}
	if err := db.Create(&model.User{UserID: "owner", Username: "chosen", IsEnabled: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := BootstrapAdmin(context.Background(), db, "chosen"); err != nil {
		t.Fatal(err)
	}
	if err := BootstrapAdmin(context.Background(), db, "chosen"); err == nil {
		t.Fatal("bootstrap ran twice")
	}
	var count int64
	db.Model(&model.RoleGrant{}).Count(&count)
	if count != 1 {
		t.Fatalf("unexpected grants: %d", count)
	}
}

func TestTerminalReceiptMigrationAddsEmptyReceiptWithoutAcknowledgingHistory(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	ctx := context.Background()
	if err := db.WithContext(ctx).AutoMigrate(&model.JobRun{}, &model.StepRun{}); err != nil {
		t.Fatal(err)
	}
	for _, entity := range []any{&model.JobRun{}, &model.StepRun{}} {
		if err := db.Migrator().DropColumn(entity, "TerminalReportDigest"); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.WithContext(ctx).Model(&model.JobRun{}).Omit("TerminalReportDigest").Create(&model.JobRun{JobRunID: "old-job", Status: 5, ClaimID: "old-claim"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Model(&model.StepRun{}).Omit("TerminalReportDigest").Create(&model.StepRun{StepRunID: "old-step", Status: 4, ClaimID: "old-claim"}).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := MigrateSchema(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	job, err := (&JobRunRepo{IDatabase: statusTestDB{db}}).GetByJobRunID(ctx, "old-job")
	if err != nil || job.Status != 5 || job.ClaimID != "old-claim" || job.TerminalReportDigest != "" {
		t.Fatalf("migration synthesized job receipt: %+v %v", job, err)
	}
	step, err := (&StepRunRepo{IDatabase: statusTestDB{db}}).GetByStepRunID(ctx, "old-step")
	if err != nil || step.Status != 4 || step.ClaimID != "old-claim" || step.TerminalReportDigest != "" {
		t.Fatalf("migration synthesized step receipt: %+v %v", step, err)
	}
}

func TestSourceProvenanceMigrationDoesNotInventHistoricalCheckout(t *testing.T) {
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err := db.WithContext(ctx).AutoMigrate(&model.JobRun{}); err != nil {
		t.Fatal(err)
	}
	fields := []string{"SourceExpectationCaptured", "SourceType", "SourceRepository", "SourceRef", "SourceBindingDigest", "SourceResolvedCommit", "SourceProvenanceState"}
	for _, field := range fields {
		if err := db.Migrator().DropColumn(&model.JobRun{}, field); err != nil {
			t.Fatal(err)
		}
	}
	old := &model.JobRun{JobRunID: "historical", PipelineID: "pipeline", PipelineRunID: "run", AgentID: "agent", ClaimID: "original", Status: 4, TerminalReportDigest: strings.Repeat("c", 64)}
	if err := db.WithContext(ctx).Omit(fields...).Create(old).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := MigrateSchema(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	var got model.JobRun
	if err := db.WithContext(ctx).Select(append(jobSnapshotColumns, "id")).Where("job_run_id = ?", "historical").First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.SourceExpectationCaptured || got.SourceType != "" || got.SourceRepository != "" || got.SourceRef != "" || got.SourceBindingDigest != "" || got.SourceResolvedCommit != "" || got.SourceProvenanceState != "unknown" {
		t.Fatalf("migration inferred checkout %+v", got)
	}
	if got.Status != 4 || got.ClaimID != "original" || got.TerminalReportDigest != old.TerminalReportDigest {
		t.Fatal("migration changed historical receipt")
	}
}

func TestMenuSeedsUseValidJSONAndPreserveExistingMetadata(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	ctx := context.Background()
	if err := MigrateSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	var menus []model.Menu
	if err := db.Find(&menus).Error; err != nil {
		t.Fatal(err)
	}
	if len(menus) == 0 {
		t.Fatal("no menu seeds")
	}
	for _, menu := range menus {
		if !json.Valid([]byte(menu.Meta)) || menu.Meta != "{}" {
			t.Fatalf("menu %s has invalid initial metadata %q", menu.MenuID, menu.Meta)
		}
	}
	original := ` {"custom":{"visible":true},"items":[1,2]} `
	if err := db.Model(&model.Menu{}).Where("menu_id = ?", menus[0].MenuID).Update("meta_json", original).Error; err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	var existing model.Menu
	if err := db.Where("menu_id = ?", menus[0].MenuID).First(&existing).Error; err != nil {
		t.Fatal(err)
	}
	if existing.Meta != original {
		t.Fatalf("migration overwrote user metadata: %q", existing.Meta)
	}
	var invalid int64
	if err := db.Model(&model.Menu{}).Where("NOT json_valid(meta_json)").Count(&invalid).Error; err != nil {
		t.Fatal(err)
	}
	if invalid != 0 {
		t.Fatalf("%d menu seeds contain invalid JSON", invalid)
	}
}
