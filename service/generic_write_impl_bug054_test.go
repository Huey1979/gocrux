package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Huey1979/gocrux/repository"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// Bug054Audit 模拟 heims AuditFields（导出类型，匿名嵌入后反射可遍历；
// 未导出类型 PkgPath 非空会被反射遍历跳过，BUG-048 经验）。
type Bug054Audit struct {
	CreatedBy string    `gorm:"column:created_by;size:26" json:"created_by"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedBy string    `gorm:"column:updated_by;size:26" json:"updated_by"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (a *Bug054Audit) SetCreatedAt(t time.Time) { a.CreatedAt = t }
func (a *Bug054Audit) SetCreatedBy(uid string)  { a.CreatedBy = uid }
func (a *Bug054Audit) SetUpdatedAt(t time.Time) { a.UpdatedAt = t }
func (a *Bug054Audit) SetUpdatedBy(uid string)  { a.UpdatedBy = uid }

// Bug054Doc 模拟 heims SysRole：嵌入审计字段 + 普通字段 + 显式零值字段。
type Bug054Doc struct {
	ID        string `gorm:"column:id;primaryKey" json:"id"`
	Name      string `gorm:"column:name" json:"name"`
	IsEnabled int8   `gorm:"column:is_enabled" json:"is_enabled"`
	Bug054Audit
}

func (d *Bug054Doc) SetDefaults()        {}
func (d *Bug054Doc) SupportsDraft() bool { return false }
func (d *Bug054Doc) SetDelete() bool     { return false }
func (d *Bug054Doc) PKField() string     { return "id" }
func (d *Bug054Doc) SelfFKField() string { return "" }

// Bug054Req 实现 CrudRequest + RequestFields：data 通过 JSON 中介合并进实体。
type Bug054Req struct {
	data map[string]any
}

func (r *Bug054Req) MergeTo(target **Bug054Doc) error {
	if len(r.data) == 0 {
		return nil
	}
	b, err := json.Marshal(r.data)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, *target)
}
func (r *Bug054Req) GetID() any           { return nil }
func (r *Bug054Req) Validate() error      { return nil }
func (r *Bug054Req) Data() map[string]any { return r.data }

// TestCollectNonZeroColumnsExpandsAuditFields BUG-054 单元级：
// 匿名嵌入 AuditFields 的非零子字段（_beforeCreate 已设置）必须递归展开进白名单；
// 零值子字段（updated_by）不展开；嵌入 struct 本身不作为列（不回归 BUG-048）。
func TestCollectNonZeroColumnsExpandsAuditFields(t *testing.T) {
	doc := &Bug054Doc{
		ID:          "ulid-1",
		Name:        "x",
		IsEnabled:   1,
		Bug054Audit: Bug054Audit{CreatedBy: "u1", CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}
	cols := collectNonZeroColumns(doc)
	got := map[string]bool{}
	for _, c := range cols {
		got[c] = true
	}
	if !got["created_by"] || !got["created_at"] || !got["updated_at"] {
		t.Errorf("BUG-054: non-zero embedded audit cols must be expanded: %v", cols)
	}
	if got["updated_by"] {
		t.Errorf("zero-value embedded col updated_by must not be collected: %v", cols)
	}
	if got["bug054_audit"] || got["audit_fields"] {
		t.Errorf("BUG-048: embedded struct itself must not be collected as a column: %v", cols)
	}
	if !got["id"] || !got["name"] {
		t.Errorf("normal non-zero columns must be kept: %v", cols)
	}
}

// Bug054DocWithDeleted 模拟嵌入 gorm.DeletedAt 的实体（GORM 特殊列，白名单不可列出）。
type Bug054DocWithDeleted struct {
	Bug054Doc
	gorm.DeletedAt
}

// TestCollectNonZeroColumnsSkipsZeroDeletedAt BUG-054 防御：
// 零值 gorm.DeletedAt（Time/Valid 均零值）不产生任何白名单列；
// 非零情况下列 deleted_at 由 GORM 管线管理，白名单不含 time/valid。
func TestCollectNonZeroColumnsSkipsZeroDeletedAt(t *testing.T) {
	doc := &Bug054DocWithDeleted{
		Bug054Doc: Bug054Doc{ID: "ulid-2", Name: "x", Bug054Audit: Bug054Audit{CreatedBy: "u1"}},
	}
	cols := collectNonZeroColumns(doc)
	got := map[string]bool{}
	for _, c := range cols {
		got[c] = true
	}
	if got["time"] || got["valid"] || got["deleted_at"] {
		t.Errorf("gorm.DeletedAt must not produce columns in whitelist: %v", cols)
	}
	if !got["name"] || !got["created_by"] {
		t.Errorf("normal/embedded non-zero columns must be kept: %v", cols)
	}
}

// TestBug054CreatePersistsAuditFields BUG-054 集成级（sqlite）：
// 嵌入 AuditFields 的实体 + 请求含显式字段（is_enabled=0）→ Create 白名单
// 必须包含 _beforeCreate 设置的审计列 → Select 落库后审计字段非空、
// 显式零值原样落库（BUG-045 语义不回归）。
func TestBug054CreatePersistsAuditFields(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sqlDB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&Bug054Doc{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	repo := repository.NewCRUDWithDB[*Bug054Doc](db)
	svc := NewGenericService[*Bug054Doc](repo, Config[*Bug054Doc]{})
	ctx := context.WithValue(context.Background(), CtxKeyUserULID, "user-ulid-054")

	req := &Bug054Req{data: map[string]any{"name": "role-054", "is_enabled": 0}}
	result, err := svc.Create(ctx, []CrudRequest[*Bug054Doc]{req})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("Create result len = %d, want 1", len(result))
	}

	var got Bug054Doc
	if err := db.First(&got, "id = ?", (*result[0]).ID).Error; err != nil {
		t.Fatalf("query created record: %v", err)
	}

	// BUG-054 核心：审计字段必须落库（此前 Select 白名单不含 → 库中为空）
	if got.CreatedBy != "user-ulid-054" {
		t.Errorf("BUG-054: created_by must persist, got %q", got.CreatedBy)
	}
	if got.CreatedAt.IsZero() {
		t.Error("created_at must persist")
	}
	if got.UpdatedAt.IsZero() {
		t.Error("updated_at must persist")
	}
	// 请求字段 + 显式零值（BUG-045 语义不回归）
	if got.Name != "role-054" {
		t.Errorf("name = %q, want role-054", got.Name)
	}
	if got.IsEnabled != 0 {
		t.Errorf("explicit zero is_enabled must persist as 0, got %d", got.IsEnabled)
	}
}
