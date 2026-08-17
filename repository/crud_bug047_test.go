package repository

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// bug047Doc 模拟 heims SysBiChart：default:<非零> 字段 + type:json 字段。
type bug047Doc struct {
	ID           string    `gorm:"column:id;primaryKey" json:"id"`
	Name         string    `gorm:"column:name" json:"name"`
	IsEnabled    int8      `gorm:"column:is_enabled;default:1" json:"is_enabled"`
	ListPageSize int       `gorm:"column:list_page_size;default:20" json:"list_page_size"`
	FormConfig   string    `gorm:"type:json" json:"form_config"`
	Extra        string    `bson:"extra_data" json:"extra"`
	CreatedAt    time.Time `gorm:"column:created_at" json:"created_at"`
}

func TestEntityToMapByColumns(t *testing.T) {
	doc := &bug047Doc{
		ID:           "ulid1",
		Name:         "x",
		IsEnabled:    0, // 显式零值，default:1 → 必须原样保留 0（BUG-047）
		ListPageSize: 0, // 显式零值，default:20 → 必须原样保留 0（BUG-047）
		FormConfig:   "{}",
		Extra:        "e",
		CreatedAt:    time.Now(),
	}
	cols := []string{"id", "name", "is_enabled", "list_page_size", "form_config", "extra_data", "created_at", "not_a_column"}
	row := EntityToMapByColumns(doc, cols)

	if v, ok := row["is_enabled"]; !ok || v != int8(0) {
		t.Errorf("is_enabled must be preserved as 0 (BUG-047), got %#v", v)
	}
	if v, ok := row["list_page_size"]; !ok || v != 0 {
		t.Errorf("list_page_size must be preserved as 0 (BUG-047), got %#v", v)
	}
	if v, ok := row["id"]; !ok || v != "ulid1" {
		t.Errorf("id missing/wrong: %#v", v)
	}
	if v, ok := row["form_config"]; !ok || v != "{}" {
		t.Errorf("type:json form_config missing/wrong: %#v", v)
	}
	if v, ok := row["extra_data"]; !ok || v != "e" {
		t.Errorf("bson-tagged extra_data missing/wrong: %#v", v)
	}
	if _, ok := row["created_at"]; !ok {
		t.Error("created_at missing")
	}
	if _, ok := row["not_a_column"]; ok {
		t.Error("unknown column must be skipped")
	}

	// 批量键集一致性：第二个实体部分字段零值，按同一 cols 构造 → 键集必须相同
	doc2 := &bug047Doc{ID: "ulid2", Name: "", IsEnabled: 0, ListPageSize: 20, FormConfig: "{}", Extra: "", CreatedAt: time.Now()}
	row2 := EntityToMapByColumns(doc2, cols)
	if len(row) != len(row2) {
		t.Fatalf("rows must have identical key sets, got %d vs %d", len(row), len(row2))
	}
	if v, ok := row2["name"]; !ok || v != "" {
		t.Errorf("zero-value name must still be present in map (key-set consistency): %#v", v)
	}
	if v, ok := row2["extra_data"]; !ok || v != "" {
		t.Errorf("zero-value extra_data must still be present in map (key-set consistency): %#v", v)
	}

	// nil 实体 → 空 map 不 panic
	if row3 := EntityToMapByColumns((*bug047Doc)(nil), cols); len(row3) != 0 {
		t.Errorf("nil entity should yield empty map, got %#v", row3)
	}
}

// TestInsertBatchWhitelistSkipsGormDefaultFill 集成验证 BUG-047：
// GORM struct Create + Select 会对 default:<非零> 可解析 tag + 零值字段强制填充默认值
// （callbacks/create.go），白名单 map 插入则原样落库（0 就是 0）。
func TestInsertBatchWhitelistSkipsGormDefaultFill(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sqlDB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&bug047Doc{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	repo := NewCRUDWithDB[*bug047Doc](db)
	ctx := context.Background()

	doc := &bug047Doc{ID: "ulid1", Name: "x", IsEnabled: 0, ListPageSize: 0, FormConfig: "{}", CreatedAt: time.Now()}
	cols := []string{"id", "name", "is_enabled", "list_page_size", "form_config", "created_at"}
	if err := repo.InsertBatch(ctx, []**bug047Doc{&doc}, cols...); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}

	var got bug047Doc
	if err := db.First(&got, "id = ?", "ulid1").Error; err != nil {
		t.Fatalf("query: %v", err)
	}
	if got.IsEnabled != 0 {
		t.Errorf("BUG-047: is_enabled must be 0 in DB (explicit zero, default:1), got %d", got.IsEnabled)
	}
	if got.ListPageSize != 0 {
		t.Errorf("BUG-047: list_page_size must be 0 in DB (explicit zero, default:20), got %d", got.ListPageSize)
	}
	if got.FormConfig != "{}" {
		t.Errorf("form_config must be {} in DB, got %q", got.FormConfig)
	}

	// 对照：struct 路径（旧方案 Select）会被 GORM default 填充覆盖零值 → 落库 1/20
	doc2 := &bug047Doc{ID: "ulid2", Name: "x", IsEnabled: 0, ListPageSize: 0, FormConfig: "{}", CreatedAt: time.Now()}
	if err := db.Create(doc2).Select(cols).Error; err != nil {
		t.Fatalf("struct+Select create: %v", err)
	}
	var got2 bug047Doc
	if err := db.First(&got2, "id = ?", "ulid2").Error; err != nil {
		t.Fatalf("query2: %v", err)
	}
	if got2.IsEnabled != 1 || got2.ListPageSize != 20 {
		t.Errorf("sanity: struct+Select should still be filled with defaults (1/20), got %d/%d", got2.IsEnabled, got2.ListPageSize)
	}
}

// Bug048Audit 模拟 heims AuditFields（导出类型，与真实 entity 一致）：匿名嵌入的审计字段 struct。
type Bug048Audit struct {
	CreatedBy string    `gorm:"column:created_by;size:26" json:"created_by"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

// bug048Doc 模拟嵌入 AuditFields 的实体（SysForm/SysBiChart 等 124 个 entity，BUG-048）。
type bug048Doc struct {
	ID   string `gorm:"column:id;primaryKey" json:"id"`
	Name string `gorm:"column:name" json:"name"`
	Bug048Audit
}

// TestEntityToMapByColumnsSkipsEmbeddedStruct 验证 BUG-048：
// 匿名嵌入 struct 本身无 DB 列名，columnFieldIndex 反查其 snake 名必须失败（不产 struct 值），
// 但其带真实列名的子字段（created_by/created_at）仍可递归命中。
func TestEntityToMapByColumnsSkipsEmbeddedStruct(t *testing.T) {
	now := time.Now()
	doc := &bug048Doc{ID: "ulid1", Name: "x", Bug048Audit: Bug048Audit{CreatedBy: "u1", CreatedAt: now}}

	// 嵌入字段本身（ToSnakeCase 兜底为 audit_fields）：不得作为 map key，否则 row[audit_fields]=struct
	row := EntityToMapByColumns(doc, []string{"id", "name", "audit_fields"})
	if _, ok := row["audit_fields"]; ok {
		t.Errorf("BUG-048: embedded struct must not become map key audit_fields: %#v", row)
	}
	if v, ok := row["id"]; !ok || v != "ulid1" {
		t.Errorf("id missing/wrong: %#v", v)
	}
	if v, ok := row["name"]; !ok || v != "x" {
		t.Errorf("name missing/wrong: %#v", v)
	}

	// 嵌入子字段（真实列名）仍可反查命中
	row2 := EntityToMapByColumns(doc, []string{"created_by", "created_at"})
	if v, ok := row2["created_by"]; !ok || v != "u1" {
		t.Errorf("embedded child created_by missing/wrong: %#v", v)
	}
	if v, ok := row2["created_at"]; !ok || v != now {
		t.Errorf("embedded child created_at missing/wrong: %#v", v)
	}

	// columnFieldIndex 直接断言（传解指针后的 struct 类型）
	if _, ok := columnFieldIndex(reflect.TypeOf(doc).Elem(), "audit_fields"); ok {
		t.Error("BUG-048: columnFieldIndex must not resolve embedded struct name audit_fields")
	}
	if _, ok := columnFieldIndex(reflect.TypeOf(doc).Elem(), "created_by"); !ok {
		t.Error("embedded child column created_by must be resolvable")
	}
}
