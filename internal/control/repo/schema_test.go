package repo

import (
	"context"
	"fmt"
	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/driver/mysql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
	"strings"
	"sync"
	"testing"
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
