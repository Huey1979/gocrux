package repository

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// bug047Doc 模拟 heims SysBiChart（方案 B 形态）：default tag 只允许 0 值/无，
// 非零默认值（is_enabled=1 / list_page_size=20）的语义由 SetDefaults() 在 Go 层承担。
type bug047Doc struct {
	ID           string    `gorm:"column:id;primaryKey" json:"id"`
	Name         string    `gorm:"column:name" json:"name"`
	IsEnabled    int8      `gorm:"column:is_enabled" json:"is_enabled"`
	ListPageSize int       `gorm:"column:list_page_size" json:"list_page_size"`
	FormConfig   string    `gorm:"type:json" json:"form_config"`
	Extra        string    `bson:"extra_data" json:"extra"`
	CreatedAt    time.Time `gorm:"column:created_at" json:"created_at"`
}

// bug047LegacyDoc 模拟方案 B 之前的旧实体（default:<非零> tag），用于对照验证：
// 存在非零 default tag 时 struct+Select 路径仍会被 GORM 默认填充覆盖显式零值
// → 这正是方案 B 必须移除非零 default tag（由 SetDefaults 承担语义）的原因。
type bug047LegacyDoc struct {
	ID           string    `gorm:"column:id;primaryKey" json:"id"`
	Name         string    `gorm:"column:name" json:"name"`
	IsEnabled    int8      `gorm:"column:is_enabled;default:1" json:"is_enabled"`
	ListPageSize int       `gorm:"column:list_page_size;default:20" json:"list_page_size"`
	CreatedAt    time.Time `gorm:"column:created_at" json:"created_at"`
}

// TestInsertBatchWhitelistPreservesExplicitZero 集成验证方案 B：
// 无非零 default tag 的实体，struct+Select 白名单插入显式零值原样落库（0 就是 0）。
// 这是 BUG-045/047 修复在方案 B 下的最终形态——GORM default 填充仅在
// DefaultValueInterface != nil 时触发，0 值/无 default tag 不触发，
// 白名单 Select 强制显式零值真实落库（BUG-047 方案 A 的 map 绕过不再必要）。
func TestInsertBatchWhitelistPreservesExplicitZero(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sqlDB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&bug047Doc{}, &bug047LegacyDoc{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	repo := NewCRUDWithDB[bug047Doc](db)
	ctx := context.Background()

	doc := &bug047Doc{ID: "ulid1", Name: "x", IsEnabled: 0, ListPageSize: 0, FormConfig: "{}", CreatedAt: time.Now()}
	cols := []string{"id", "name", "is_enabled", "list_page_size", "form_config", "created_at"}
	if err := repo.InsertBatch(ctx, []*bug047Doc{doc}, cols...); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}

	var got bug047Doc
	if err := db.First(&got, "id = ?", "ulid1").Error; err != nil {
		t.Fatalf("query: %v", err)
	}
	if got.IsEnabled != 0 {
		t.Errorf("is_enabled must be 0 in DB (explicit zero, no default tag), got %d", got.IsEnabled)
	}
	if got.ListPageSize != 0 {
		t.Errorf("list_page_size must be 0 in DB (explicit zero, no default tag), got %d", got.ListPageSize)
	}
	if got.FormConfig != "{}" {
		t.Errorf("form_config must be {} in DB, got %q", got.FormConfig)
	}

	// 对照：非零 default tag 的实体 struct+Select 仍被 GORM 默认填充覆盖显式零值
	// → 0 变 1/20。证明方案 B「default 只允许 0 值/无」的必要性（BUG-047 根因）。
	legacy := &bug047LegacyDoc{ID: "ulid2", Name: "x", IsEnabled: 0, ListPageSize: 0, CreatedAt: time.Now()}
	legacyCols := []string{"id", "name", "is_enabled", "list_page_size", "created_at"}
	if err := db.Create(legacy).Select(legacyCols).Error; err != nil {
		t.Fatalf("legacy struct+Select create: %v", err)
	}
	var got2 bug047LegacyDoc
	if err := db.First(&got2, "id = ?", "ulid2").Error; err != nil {
		t.Fatalf("query2: %v", err)
	}
	if got2.IsEnabled != 1 || got2.ListPageSize != 20 {
		t.Errorf("sanity: legacy default:1/20 should still be filled (1/20), got %d/%d", got2.IsEnabled, got2.ListPageSize)
	}
}

// TestInsertPreservesExplicitZero 单条 Insert 白名单同样原样落库（方案 B 形态）。
func TestInsertPreservesExplicitZero(t *testing.T) {
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

	repo := NewCRUDWithDB[bug047Doc](db)
	ctx := context.Background()

	doc := &bug047Doc{ID: "ulid2", Name: "x", IsEnabled: 0, CreatedAt: time.Now()}
	cols := []string{"id", "name", "is_enabled", "created_at"}
	if err := repo.Insert(ctx, doc, cols...); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	var got bug047Doc
	if err := db.First(&got, "id = ?", "ulid2").Error; err != nil {
		t.Fatalf("query: %v", err)
	}
	if got.IsEnabled != 0 {
		t.Errorf("is_enabled must be 0 in DB (explicit zero, no default tag), got %d", got.IsEnabled)
	}
}
