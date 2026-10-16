package repo

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/driver/mysql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func userCreateDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "users.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.WithContext(context.Background()).AutoMigrate(&model.User{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestAddUserPersistsOnlyUserModel(t *testing.T) {
	db := userCreateDatabase(t)
	r := NewUserRepo(statusTestDB{db}, nil)
	req := &model.AddUserReq{
		UserID: "synthetic-user", Username: "synthetic", FullName: "Synthetic User",
		Password: "synthetic-stored-hash", Avatar: "synthetic-avatar", Email: "synthetic@example.invalid",
		Phone: "synthetic-phone", IsEnabled: 1, CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	before := *req
	if err := r.AddUser(req); err != nil {
		t.Fatal(err)
	}
	saved, err := r.GetUserByUserID(req.UserID)
	if err != nil {
		t.Fatal(err)
	}
	want := model.User{BaseModel: saved.BaseModel, UserID: req.UserID, Username: req.Username, FullName: req.FullName,
		Password: req.Password, Avatar: req.Avatar, Email: req.Email, Phone: req.Phone, IsEnabled: req.IsEnabled}
	if !reflect.DeepEqual(*saved, want) || !saved.CreatedAt.Equal(req.CreatedAt) || saved.UpdatedAt.IsZero() || saved.ID == 0 {
		t.Fatal("stored user fields or model timestamps differ from the request")
	}
	if *req != before {
		t.Fatal("persistence mutated the caller's request")
	}
	disabled := &model.AddUserReq{UserID: "disabled", Username: "disabled", IsEnabled: 0}
	if err := r.AddUser(disabled); err != nil {
		t.Fatal(err)
	}
	saved, err = r.GetUserByUserID(disabled.UserID)
	if err != nil || saved.IsEnabled != 0 || saved.CreatedAt.IsZero() {
		t.Fatal("disabled state or generated creation time was not preserved", err)
	}
	if db.Migrator().HasTable(&model.AddUserReq{}) {
		t.Fatal("request DTO must never become a database table")
	}
}

func TestAddUserMySQLStatementUsesUserTable(t *testing.T) {
	// Render the real MySQL dialect without opening a connection or executing SQL.
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "synthetic@tcp(127.0.0.1:1)/synthetic", SkipInitializeWithVersion: true}), &gorm.Config{
		DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true, Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	var statement *gorm.Statement
	capture := func(tx *gorm.DB) { statement = tx.Statement }
	if err := db.Callback().Create().After("gorm:create").Register("test:capture_user_insert", capture); err != nil {
		t.Fatal(err)
	}
	if err := NewUserRepo(statusTestDB{db}, nil).AddUser(&model.AddUserReq{UserID: "synthetic", Username: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	if statement == nil {
		t.Fatal("MySQL create statement was not captured")
	}
	if statement.Table != "user" || !strings.HasPrefix(statement.SQL.String(), "INSERT INTO `user` ") {
		t.Fatalf("MySQL insert table = %q, want user", statement.Table)
	}
	values := statement.Clauses["VALUES"].Expression.(clause.Values)
	columns := make([]string, 0, len(values.Columns))
	for _, column := range values.Columns {
		columns = append(columns, column.Name)
	}
	wantColumns := []string{
		"created_at", "updated_at", "user_id", "username", "full_name", "password", "avatar", "email", "phone", "is_enabled",
	}
	if !reflect.DeepEqual(columns, wantColumns) {
		t.Fatalf("unexpected persistence columns: %v", columns)
	}
}

type constrainedUserCreate struct {
	model.User
	Username string `gorm:"column:username;uniqueIndex"`
}

func (constrainedUserCreate) TableName() string { return "user" }

func TestAddUserPreservesDatabaseErrors(t *testing.T) {
	db := userCreateDatabase(t)
	// Model an existing installation's unique constraint; do not change product migrations.
	if err := db.WithContext(context.Background()).AutoMigrate(&constrainedUserCreate{}); err != nil {
		t.Fatal(err)
	}
	r := NewUserRepo(statusTestDB{db}, nil)
	if err := r.AddUser(&model.AddUserReq{UserID: "one", Username: "same", IsEnabled: 1}); err != nil {
		t.Fatal(err)
	}
	err := r.AddUser(&model.AddUserReq{UserID: "two", Username: "same", IsEnabled: 1})
	if err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("unique constraint failure was hidden: %v", err)
	}
	var count int64
	if err := db.WithContext(context.Background()).Model(&model.User{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("failed insert altered existing users: count=%d, error=%v", count, err)
	}
	if err := r.AddUser(nil); err == nil {
		t.Fatal("nil request accepted")
	}
}
