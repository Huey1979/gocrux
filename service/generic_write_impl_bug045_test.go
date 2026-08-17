package service

import (
	"testing"
	"time"
)

// bug045Doc 模拟带非零 DB 默认值的数值字段实体（如 is_enabled default:1）。
type bug045Doc struct {
	ID         string    `gorm:"column:id;primaryKey" json:"id"`
	Name       string    `gorm:"column:name" json:"name"`
	IsEnabled  int8      `gorm:"column:is_enabled;default:1" json:"is_enabled"`
	IsDeleted  int8      `gorm:"column:is_deleted" json:"is_deleted"`
	CreatedAt  time.Time `gorm:"column:created_at" json:"created_at"`
	FormConfig string    `gorm:"type:json" json:"form_config"`  // BUG-046：无 column: 名，snake 兜底
	Extra      string    `bson:"extra_data" json:"extra"`        // BUG-046：无 gorm tag，bson 兜底
}

func (d *bug045Doc) SetDefaults()                {}
func (d *bug045Doc) SetCreatedAt(t time.Time)    { d.CreatedAt = t }
func (d *bug045Doc) SetCreatedBy(userID string)  {}
func (d *bug045Doc) SetUpdatedAt(t time.Time)    {}
func (d *bug045Doc) SetUpdatedBy(userID string)  {}
func (d *bug045Doc) SupportsDraft() bool         { return false }
func (d *bug045Doc) SetDelete() bool             { return false }
func (d *bug045Doc) PKField() string             { return "id" }
func (d *bug045Doc) SelfFKField() string         { return "" }

// bug045Req 本地实现 CrudRequest[*bug045Doc] + RequestFields（模拟 MapRequest 行为）。
type bug045Req struct {
	data map[string]any
}

func (r *bug045Req) MergeTo(target **bug045Doc) error { return nil }
func (r *bug045Req) GetID() any                       { return nil }
func (r *bug045Req) Validate() error                  { return nil }
func (r *bug045Req) Data() map[string]any             { return r.data }

func TestCollectExplicitColumns(t *testing.T) {
	req := &bug045Req{data: map[string]any{
		"is_enabled":      0,   // 显式零值，必须收集
		"name":            "x", // 普通字段
		"idempotency_key": "k", // 控制键，实体无此列 → 过滤
		"not_a_column":    1,   // 未知键 → 过滤
	}}
	cols := collectExplicitColumns[*bug045Doc](req)
	got := map[string]bool{}
	for _, c := range cols {
		got[c] = true
	}
	if !got["is_enabled"] {
		t.Errorf("is_enabled missing: %v", cols)
	}
	if !got["name"] {
		t.Errorf("name missing: %v", cols)
	}
	if got["idempotency_key"] || got["not_a_column"] {
		t.Errorf("control/unknown keys must be filtered: %v", cols)
	}

	// 非 RequestFields 请求 → nil
	cols2 := collectExplicitColumns[*bug045Doc](&bug045PlainReq{})
	if cols2 != nil {
		t.Errorf("non-RequestFields should return nil, got %v", cols2)
	}
}

// bug045PlainReq 仅实现 CrudRequest，不实现 RequestFields。
type bug045PlainReq struct{}

func (r *bug045PlainReq) MergeTo(target **bug045Doc) error { return nil }
func (r *bug045PlainReq) GetID() any                       { return nil }
func (r *bug045PlainReq) Validate() error                  { return nil }

func TestCollectNonZeroColumns(t *testing.T) {
	doc := &bug045Doc{ID: "ulid1", Name: "x", IsEnabled: 0, CreatedAt: time.Now(), FormConfig: "{}", Extra: "x"}
	cols := collectNonZeroColumns(doc)
	got := map[string]bool{}
	for _, c := range cols {
		got[c] = true
	}
	if !got["id"] || !got["name"] || !got["created_at"] {
		t.Errorf("non-zero columns expected id/name/created_at: %v", cols)
	}
	// BUG-046：gorm tag 仅 type:json（无 column: 名）的非零字段 → snake 兜底进白名单
	if !got["form_config"] {
		t.Errorf("type:json non-zero column form_config must be collected (BUG-046): %v", cols)
	}
	// BUG-046：无 gorm column 但有 bson tag 的非零字段 → bson tag 进白名单
	if !got["extra_data"] {
		t.Errorf("bson-tagged non-zero column extra_data must be collected (BUG-046): %v", cols)
	}
	if got["is_enabled"] {
		t.Error("is_enabled=0 is zero value, must not be collected")
	}
	if got["is_deleted"] {
		t.Error("is_deleted=0 is zero value, must not be collected")
	}
}

func TestCreateColumnWhitelist(t *testing.T) {
	doc := &bug045Doc{ID: "ulid1", Name: "x", IsEnabled: 0, CreatedAt: time.Now()}

	// 无显式字段 → nil（行为与旧版完全一致）
	if cols := createColumnWhitelist[*bug045Doc]([]**bug045Doc{&doc}, nil); cols != nil {
		t.Errorf("no explicit cols should return nil, got %v", cols)
	}

	// 有显式字段：白名单 = 非零字段列 ∪ 显式字段列
	cols := createColumnWhitelist[*bug045Doc]([]**bug045Doc{&doc}, []string{"is_enabled"})
	got := map[string]bool{}
	for _, c := range cols {
		got[c] = true
	}
	if !got["is_enabled"] {
		t.Errorf("explicit zero-value col is_enabled missing: %v", cols)
	}
	if !got["id"] || !got["name"] || !got["created_at"] {
		t.Errorf("non-zero cols must be kept: %v", cols)
	}
	if got["is_deleted"] {
		t.Errorf("zero-value non-explicit col is_deleted must not be in whitelist: %v", cols)
	}
}
