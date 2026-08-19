package repository

import (
	"testing"
)

// P3Audit 模拟 heims 计划新增的 MongoAuditFields（子字段带 bson tag）。
// 注意：必须用导出类型匿名嵌入（未导出类型 PkgPath 非空会被反射遍历跳过，见 BUG-048 经验）。
type P3Audit struct {
	CreatedBy string `bson:"created_by"`
	CreatedAt string `bson:"created_at"`
	UpdatedBy string `bson:"updated_by"`
	UpdatedAt string `bson:"updated_at"`
}

// P3Doc 模拟嵌入审计字段的 Mongo 实体：匿名字段带 bson:",inline" 标记。
type P3Doc struct {
	DocULID string `bson:"doc_ulid"`
	Title   string `bson:"title"`
	P3Audit `bson:",inline"`
}

// P3DocNoInline 模拟未标记 inline 的匿名字段：不得展开（与 mongo-driver 显式语义一致）。
type P3DocNoInline struct {
	DocULID string `bson:"doc_ulid"`
	P3Audit // 无 bson tag
}

// P3Nested 模拟嵌套 inline：外层匿名字段展开后其内部 P3Audit 再展开。
type P3Nested struct {
	P3Doc `bson:",inline"`
}

// TestToBsonDocExpandsInlineEmbedded P3：bson:",inline" 标记的匿名嵌入 struct 必须递归展开为顶层 key。
func TestToBsonDocExpandsInlineEmbedded(t *testing.T) {
	doc := &P3Doc{
		DocULID: "ulid-1",
		Title:   "hello",
		P3Audit: P3Audit{
			CreatedBy: "u1",
			CreatedAt: "2026-08-19",
		},
	}
	repo := &MongoCRUDRepository[P3Doc]{}
	out := toBsonDoc(repo, doc)

	got := map[string]any{}
	for _, e := range out {
		got[e.Key] = e.Value
	}

	// 顶层字段
	if got["doc_ulid"] != "ulid-1" {
		t.Errorf("doc_ulid = %#v, want ulid-1", got["doc_ulid"])
	}
	if got["title"] != "hello" {
		t.Errorf("title = %#v, want hello", got["title"])
	}
	// inline 展开的审计字段
	if got["created_by"] != "u1" {
		t.Errorf("created_by = %#v, want u1", got["created_by"])
	}
	if got["created_at"] != "2026-08-19" {
		t.Errorf("created_at = %#v, want 2026-08-19", got["created_at"])
	}
	// 零值字段也写入（与顶层规则一致：非 omitempty 不省略）
	if got["updated_by"] != "" {
		t.Errorf("updated_by = %#v, want empty string", got["updated_by"])
	}
	// 嵌入 struct 本身不得作为独立 key 出现
	if _, ok := got["audit"]; ok {
		t.Error("inline embedded must not appear as its own key")
	}
}

// TestToBsonDocSkipsNonInlineEmbedded P3：未标记 inline 的匿名字段不展开（避免隐式行为变化）。
func TestToBsonDocSkipsNonInlineEmbedded(t *testing.T) {
	doc := &P3DocNoInline{
		DocULID: "ulid-2",
		P3Audit: P3Audit{CreatedBy: "u1"},
	}
	repo := &MongoCRUDRepository[P3DocNoInline]{}
	out := toBsonDoc(repo, doc)

	got := map[string]any{}
	for _, e := range out {
		got[e.Key] = e.Value
	}

	if got["doc_ulid"] != "ulid-2" {
		t.Errorf("doc_ulid = %#v, want ulid-2", got["doc_ulid"])
	}
	if _, ok := got["created_by"]; ok {
		t.Error(`anonymous embedded without bson:",inline" must NOT be expanded`)
	}
	if _, ok := got["updated_at"]; ok {
		t.Error(`anonymous embedded without bson:",inline" must NOT be expanded`)
	}
}

// TestToBsonDocExpandsNestedInline P3：嵌套 inline（inline 展开后的 struct 内部还有 inline）逐层展开。
func TestToBsonDocExpandsNestedInline(t *testing.T) {
	doc := &P3Nested{
		P3Doc: P3Doc{
			DocULID: "n-1",
			Title:   "nested",
			P3Audit: P3Audit{UpdatedAt: "2026-08-19"},
		},
	}
	repo := &MongoCRUDRepository[P3Nested]{}
	out := toBsonDoc(repo, doc)

	got := map[string]any{}
	for _, e := range out {
		got[e.Key] = e.Value
	}

	if got["doc_ulid"] != "n-1" {
		t.Errorf("doc_ulid = %#v, want n-1", got["doc_ulid"])
	}
	if got["title"] != "nested" {
		t.Errorf("title = %#v, want nested", got["title"])
	}
	if got["updated_at"] != "2026-08-19" {
		t.Errorf("updated_at = %#v, want 2026-08-19", got["updated_at"])
	}
}

// P3PKEmbed 主键嵌入类型（导出）。
type P3PKEmbed struct {
	ReadULID string `bson:"read_ulid"`
	Name     string `bson:"name"`
}

// TestExtractPKValFindsEmbeddedPK P3：主键位于匿名嵌入 struct 中时也能提取。
func TestExtractPKValFindsEmbeddedPK(t *testing.T) {
	type pkDoc struct {
		DocULID string `bson:"doc_ulid"`
		P3PKEmbed
	}
	got := extractPKVal(&pkDoc{DocULID: "x", P3PKEmbed: P3PKEmbed{ReadULID: "pk-1", Name: "n"}}, "read_ulid")
	if got != "pk-1" {
		t.Errorf("extractPKVal = %#v, want pk-1 (embedded pk must be found)", got)
	}
}

// P3PKIDEmbed bson:"_id" 嵌入类型（导出）。
type P3PKIDEmbed struct {
	ID   string `bson:"_id"`
	Meta string `bson:"meta"`
}

// TestDetectPKFindsEmbeddedID P3：bson:"_id" 位于匿名嵌入 struct 中时也能自动推导主键。
func TestDetectPKFindsEmbeddedID(t *testing.T) {
	type idDoc struct {
		Title string `bson:"title"`
		P3PKIDEmbed
	}
	repo := &MongoCRUDRepository[idDoc]{}
	repo.detectPK()
	if repo.pkField != "_id" {
		t.Errorf("detectPK = %q, want _id (embedded bson:\"_id\" must be detected)", repo.pkField)
	}
}
