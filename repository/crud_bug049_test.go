package repository

import (
	"testing"
	"time"
)

// Bug049Audit 模拟 heims AuditFields（导出类型，匿名嵌入后反射可遍历；未导出类型
// PkgPath 非空会被字段遍历跳过）。
type Bug049Audit struct {
	CreatedBy string    `gorm:"column:created_by" json:"created_by"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updated_at"`
}

type bug049Doc struct {
	ID        string `gorm:"column:id;primaryKey" json:"id"`
	Name      string `gorm:"column:name" json:"name"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updated_at"`
	Bug049Audit
}

// TestEnsureAuditTime 验证 BUG-049 Save 收口：零值审计时间字段写前回填，
// 非零值保持不动，嵌入 struct 的字段同样处理。
func TestEnsureAuditTime(t *testing.T) {
	before := time.Now().Add(-time.Hour)

	// 零值 CreatedAt/UpdatedAt（含嵌入 struct）→ 全部回填为当前时间
	doc := &bug049Doc{ID: "ulid1", Name: "x"}
	ensureAuditTime(doc)
	if doc.CreatedAt.IsZero() {
		t.Error("CreatedAt must be filled")
	}
	if doc.UpdatedAt.IsZero() {
		t.Error("UpdatedAt must be filled")
	}
	if doc.Bug049Audit.CreatedAt.IsZero() {
		t.Error("embedded CreatedAt must be filled")
	}
	if doc.Bug049Audit.UpdatedAt.IsZero() {
		t.Error("embedded UpdatedAt must be filled")
	}

	// 非零 CreatedAt/UpdatedAt 不动
	doc2 := &bug049Doc{ID: "ulid2", CreatedAt: before, UpdatedAt: before}
	ensureAuditTime(doc2)
	if !doc2.CreatedAt.Equal(before) {
		t.Errorf("non-zero CreatedAt must be preserved, got %v", doc2.CreatedAt)
	}
	if !doc2.UpdatedAt.Equal(before) {
		t.Errorf("non-zero UpdatedAt must be preserved, got %v", doc2.UpdatedAt)
	}

	// nil 实体不 panic
	ensureAuditTime((*bug049Doc)(nil))
	ensureAuditTime(nil)
}

// bug049PtrDoc *time.Time 形态。
type bug049PtrDoc struct {
	ID        string     `gorm:"column:id;primaryKey" json:"id"`
	CreatedAt *time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt *time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func TestEnsureAuditTimePtr(t *testing.T) {
	doc := &bug049PtrDoc{ID: "ulid1"}
	ensureAuditTime(doc)
	if doc.CreatedAt == nil {
		t.Fatal("nil *time.Time CreatedAt must be filled")
	}
	if doc.UpdatedAt == nil {
		t.Fatal("nil *time.Time UpdatedAt must be filled")
	}

	// 非零指针不动
	before := time.Now().Add(-time.Hour)
	doc2 := &bug049PtrDoc{ID: "ulid2", CreatedAt: &before, UpdatedAt: &before}
	ensureAuditTime(doc2)
	if !doc2.CreatedAt.Equal(before) {
		t.Errorf("non-zero *time.Time must be preserved, got %v", doc2.CreatedAt)
	}
	if !doc2.UpdatedAt.Equal(before) {
		t.Errorf("non-zero *time.Time must be preserved, got %v", doc2.UpdatedAt)
	}
}
