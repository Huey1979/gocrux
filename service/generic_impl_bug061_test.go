package service

import (
	"testing"
	"time"
)

// Bug061Doc 模拟 heims Mongo 通知实体（notify_content）：
// 部分字段 bson tag 带 omitempty 选项（BUG-061 触发条件）。
type Bug061Doc struct {
	ID             string `bson:"_id" json:"id"`
	ContentULID    string `bson:"content_ulid" json:"content_ulid"`
	TemplateCode   string `bson:"template_code" json:"template_code"`
	EntityRecordID string `bson:"entity_record_id,omitempty" json:"entity_record_id"`
	EntityCode     string `bson:"entity_code,omitempty" json:"entity_code"`
	LinkURL        string `bson:"link_url,omitempty" json:"link_url"`
}

func (d *Bug061Doc) SetDefaults()               {}
func (d *Bug061Doc) SetCreatedAt(t time.Time)   {}
func (d *Bug061Doc) SetCreatedBy(uid string)    {}
func (d *Bug061Doc) SetUpdatedAt(t time.Time)   {}
func (d *Bug061Doc) SetUpdatedBy(uid string)    {}
func (d *Bug061Doc) SupportsDraft() bool        { return false }
func (d *Bug061Doc) SetDelete() bool            { return false }
func (d *Bug061Doc) PKField() string            { return "id" }
func (d *Bug061Doc) SelfFKField() string        { return "" }

// TestParseBsonKey BUG-061 helper：bson tag 取逗号前段。
func TestParseBsonKey(t *testing.T) {
	cases := []struct{ tag, want string }{
		{"entity_record_id,omitempty", "entity_record_id"},
		{"link_url,omitempty", "link_url"},
		{"content_ulid", "content_ulid"},
		{"_id", "_id"},
		{"", ""},
	}
	for _, c := range cases {
		if got := parseBsonKey(c.tag); got != c.want {
			t.Errorf("parseBsonKey(%q) = %q, want %q", c.tag, got, c.want)
		}
	}
}

// TestBug061ResolveColumns 四个反射解析入口对 bson:",omitempty" 字段统一取逗号前段。
func TestBug061ResolveColumns(t *testing.T) {
	// 1. resolveColumnByName（前端参数 → 存储列名）：omitempty 字段必须返回纯 key
	if got := resolveColumnByName[*Bug061Doc]("entity_record_id"); got != "entity_record_id" {
		t.Errorf("resolveColumnByName(entity_record_id) = %q, want entity_record_id", got)
	}
	if got := resolveColumnByName[*Bug061Doc]("entity_code"); got != "entity_code" {
		t.Errorf("resolveColumnByName(entity_code) = %q, want entity_code", got)
	}
	// 无 omitempty 字段不受影响
	if got := resolveColumnByName[*Bug061Doc]("template_code"); got != "template_code" {
		t.Errorf("resolveColumnByName(template_code) = %q, want template_code", got)
	}

	// 2. resolveColumn（Go 字段名 → 存储列名）
	if got := resolveColumn[*Bug061Doc]("EntityRecordID"); got != "entity_record_id" {
		t.Errorf("resolveColumn(EntityRecordID) = %q, want entity_record_id", got)
	}
	if got := resolveColumn[*Bug061Doc]("LinkURL"); got != "link_url" {
		t.Errorf("resolveColumn(LinkURL) = %q, want link_url", got)
	}

	// 3. resolveColumnFromDB（DB 列名 → Go 字段名）：修复前 bsonTag 带逗号永不相等返回 ""
	if got := resolveColumnFromDB[*Bug061Doc]("entity_record_id"); got != "EntityRecordID" {
		t.Errorf("resolveColumnFromDB(entity_record_id) = %q, want EntityRecordID", got)
	}
	if got := resolveColumnFromDB[*Bug061Doc]("entity_code"); got != "EntityCode" {
		t.Errorf("resolveColumnFromDB(entity_code) = %q, want EntityCode", got)
	}
	if got := resolveColumnFromDB[*Bug061Doc]("link_url"); got != "LinkURL" {
		t.Errorf("resolveColumnFromDB(link_url) = %q, want LinkURL", got)
	}

	// 4. knownColumns：含纯 key，不含 ",omitempty" 错误键
	cols := knownColumns[*Bug061Doc]()
	if !cols["entity_record_id"] {
		t.Error("knownColumns must contain entity_record_id")
	}
	if cols["entity_record_id,omitempty"] {
		t.Error("knownColumns must NOT contain entity_record_id,omitempty")
	}
	if !cols["link_url"] {
		t.Error("knownColumns must contain link_url")
	}
	if cols["link_url,omitempty"] {
		t.Error("knownColumns must NOT contain link_url,omitempty")
	}
	// 无 omitempty 字段正常
	if !cols["content_ulid"] || !cols["template_code"] {
		t.Error("knownColumns must contain content_ulid/template_code")
	}
	if !cols["id"] {
		t.Error("knownColumns must contain id")
	}
}
