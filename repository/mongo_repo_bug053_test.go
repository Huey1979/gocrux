package repository

import (
	"testing"
)

// bug053Doc 模拟 heims 通知实体：bson tag 带 omitempty 选项。
type bug053Doc struct {
	DocULID    string `bson:"doc_ulid"`
	LinkURL    string `bson:"link_url,omitempty"`
	EntityCode string `bson:"entity_code,omitempty"`
	SenderName string `bson:"sender_name,omitempty"`
	Hidden     string `bson:"-"`
	NoTag      string
}

// TestToBsonDocStripsBsonOptions BUG-053：toBsonDoc 必须取 bson tag 逗号前段作 key，
// 不能把 "link_url,omitempty" 原样写入（否则读取按 link_url 读不到，create 后字段全空）。
func TestToBsonDocStripsBsonOptions(t *testing.T) {
	doc := &bug053Doc{
		DocULID:    "ulid-1",
		LinkURL:    "/test/notify",
		EntityCode: "demo",
		SenderName: "tester",
		Hidden:     "should-not-appear",
		NoTag:      "no-tag-field",
	}
	repo := &MongoCRUDRepository[bug053Doc]{}
	out := toBsonDoc(repo, doc)

	got := map[string]any{}
	for _, e := range out {
		got[e.Key] = e.Value
	}

	// 带 omitempty 的字段：key 必须为逗号前段，且值原样保留
	for k, want := range map[string]any{
		"link_url":    "/test/notify",
		"entity_code": "demo",
		"sender_name": "tester",
	} {
		if got[k] != want {
			t.Errorf("key %q = %#v, want %#v (raw bson tag must be stripped)", k, got[k], want)
		}
		if _, ok := got[k+",omitempty"]; ok {
			t.Errorf("raw bson tag with suffix %q must not be used as key", k+",omitempty")
		}
	}
	// 无选项 tag 正常；"-" 与无 tag 字段均跳过
	if got["doc_ulid"] != "ulid-1" {
		t.Errorf("doc_ulid = %#v, want ulid-1", got["doc_ulid"])
	}
	if _, ok := got["hidden"]; ok {
		t.Error("bson:\"-\" field must be skipped")
	}
	if _, ok := got["NoTag"]; ok {
		t.Error("field without bson tag must be skipped")
	}
}

// TestExtractPKValWithBsonOption BUG-053 关联：extractPKVal 解析主键 bson tag 也应忽略选项。
func TestExtractPKValWithBsonOption(t *testing.T) {
	type pkDoc struct {
		ReadULID string `bson:"read_ulid,omitempty"`
		Name     string `bson:"name"`
	}
	got := extractPKVal(&pkDoc{ReadULID: "pk-1", Name: "n"}, "read_ulid")
	if got != "pk-1" {
		t.Errorf("extractPKVal = %#v, want pk-1 (bson tag option must be stripped)", got)
	}
}
