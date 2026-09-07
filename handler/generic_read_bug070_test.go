package handler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/Huey1979/gocrux/repository"
	"github.com/Huey1979/gocrux/service"
)

// ============================================================
// BUG-070 回归测试（handler 层）：引用解析（DoResolve）与列表查询（DoList）分流
//
// - DoResolve（References / ChildRefs 展开）：不套用「当前有效」过滤，
//   已软删 / 历史版本的引用目标仍作为锚点返回，并保留 is_deleted 等状态字段。
// - DoList（Cascades 向下级联）：维持当前有效过滤，删掉的子记录不再出现。
// ============================================================

// bug070Doc 非版本化引用目标测试实体（含软删列）。
type bug070Doc struct {
	ULID      string    `gorm:"column:ulid;primaryKey;size:26" json:"ulid"`
	Name      string    `gorm:"column:name;size:100" json:"name"`
	IsDeleted int8      `gorm:"column:is_deleted;default:0" json:"is_deleted"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (d *bug070Doc) SetDefaults()             {}
func (d *bug070Doc) SetCreatedAt(_ time.Time) {}
func (d *bug070Doc) SetCreatedBy(string)      {}
func (d *bug070Doc) SetUpdatedAt(t time.Time) { d.UpdatedAt = t }
func (d *bug070Doc) SetUpdatedBy(string)      {}
func (d *bug070Doc) SupportsDraft() bool      { return false }
func (d *bug070Doc) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *bug070Doc) PKField() string          { return "ulid" }
func (d *bug070Doc) SelfFKField() string      { return "" }

// 编译期断言：GenericHandler 必须实现引用解析能力。
var _ refResolveHandler = (*GenericHandler[*bug070Doc])(nil)

// openBug070DB 打开独立 sqlite 内存库。
func openBug070DB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sqlDB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&bug070Doc{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return db
}

// TestBug070DoResolveKeepsDeletedAnchor DoResolve 保留已软删锚点，
// DoList（级联语义）仍按当前有效过滤 —— 两者分流正确。
func TestBug070DoResolveKeepsDeletedAnchor(t *testing.T) {
	db := openBug070DB(t)
	if err := db.Create(&bug070Doc{ULID: "live-id", Name: "live"}).Error; err != nil {
		t.Fatalf("seed live: %v", err)
	}
	if err := db.Create(&bug070Doc{ULID: "gone-id", Name: "gone", IsDeleted: 1}).Error; err != nil {
		t.Fatalf("seed deleted: %v", err)
	}

	svc := service.NewGenericService[*bug070Doc](repository.NewCRUDWithDB[*bug070Doc](db), service.Config[*bug070Doc]{})
	h := NewGenericHandlerWithSvc[*bug070Doc](svc, "bug070", HandlerConfig[*bug070Doc]{})
	ctx := context.Background()
	ids := []any{"live-id", "gone-id"}

	// DoList：向下级联语义 → 只返回未删（不回归）
	listed, err := h.DoList(ctx, "ulid", ids, false)
	if err != nil {
		t.Fatalf("DoList: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("DoList must keep current-effective filtering, got %d records", len(listed))
	}

	// BUG-070 核心：DoResolve 保留两个锚点，且已删项携带 is_deleted=1
	resolved, err := h.DoResolve(ctx, "ulid", ids)
	if err != nil {
		t.Fatalf("DoResolve: %v", err)
	}
	if len(resolved) != 2 {
		t.Fatalf("BUG-070: DoResolve must keep both anchors, got %d", len(resolved))
	}
	for _, m := range resolved {
		if m["ulid"] == "gone-id" {
			// 经 JSON 往返后类型可能不是 int8，按值比较
			if fmt.Sprint(m["is_deleted"]) != "1" {
				t.Errorf("deleted anchor must expose is_deleted=1, got %v", m["is_deleted"])
			}
			if m["name"] != "gone" {
				t.Errorf("deleted anchor name = %v, want gone", m["name"])
			}
		}
	}
}

// bug070ShapeDoc 主键**列名与 JSON 名不一致**的实体（列 field_ulid vs JSON "ulid"）。
// heims 真实实体正是这种形态，用于验证占位对象与正常记录输出结构一致（BUG-070 复核）。
type bug070ShapeDoc struct {
	ULID string `gorm:"column:field_ulid;primaryKey;size:26" json:"ulid"`
	Name string `gorm:"column:name;size:100" json:"name"`
}

func (d *bug070ShapeDoc) SetDefaults()             {}
func (d *bug070ShapeDoc) SetCreatedAt(_ time.Time) {}
func (d *bug070ShapeDoc) SetCreatedBy(string)      {}
func (d *bug070ShapeDoc) SetUpdatedAt(_ time.Time) {}
func (d *bug070ShapeDoc) SetUpdatedBy(string)      {}
func (d *bug070ShapeDoc) SupportsDraft() bool      { return false }
func (d *bug070ShapeDoc) SetDelete() bool          { return false }
func (d *bug070ShapeDoc) PKField() string          { return "field_ulid" }
func (d *bug070ShapeDoc) SelfFKField() string      { return "" }

// TestBug070PlaceholderKeyMatchesOutputShape BUG-070 复核：
// 占位对象的字段名必须与正常记录的输出结构一致（json tag 名），
// 不能用数据库列名 —— 否则同一展开数组里两种对象 shape 不一致，前端难以处理。
func TestBug070PlaceholderKeyMatchesOutputShape(t *testing.T) {
	// 1. pkOutputKey 解析为 json tag 名（列 field_ulid → "ulid"）
	if got := pkOutputKey[*bug070ShapeDoc]("field_ulid"); got != "ulid" {
		t.Errorf("pkOutputKey(field_ulid) = %q, want %q (json tag)", got, "ulid")
	}
	// 列名与 json 名一致时原样返回
	if got := pkOutputKey[*bug070Doc]("ulid"); got != "ulid" {
		t.Errorf("pkOutputKey(ulid) = %q, want ulid", got)
	}

	// 2. 正常记录序列化后的 key 就是 json tag 名
	rec, err := marshalToMap(&bug070ShapeDoc{ULID: "01ANCHOR", Name: "n"})
	if err != nil {
		t.Fatalf("marshalToMap: %v", err)
	}
	if _, ok := rec["ulid"]; !ok {
		t.Errorf("marshaled record must expose json key ulid, got keys %v", rec)
	}
	if _, ok := rec["field_ulid"]; ok {
		t.Errorf("marshaled record must NOT expose db column name, got keys %v", rec)
	}

	// 3. 占位对象使用同一个 key
	ph := missingRefPlaceholder(pkOutputKey[*bug070ShapeDoc]("field_ulid"), "01ANCHOR")
	if _, ok := ph["ulid"]; !ok {
		t.Errorf("placeholder must use output key ulid, got %v", ph)
	}
	if ph["ulid"] != "01ANCHOR" || ph["missing"] != true || len(ph) != 2 {
		t.Errorf("placeholder = %v, want {ulid: 01ANCHOR, missing: true}", ph)
	}

	// 4. 索引取值：列名不同时仍能按输出 key 命中
	if k, ok := refAnchorKey(rec, "ulid", "field_ulid"); !ok || k != "01ANCHOR" {
		t.Errorf("refAnchorKey = (%q, %v), want (01ANCHOR, true)", k, ok)
	}
}

// TestBug070MissingRefPlaceholder 缺失引用的占位对象只含引用键与 missing 标记。
func TestBug070MissingRefPlaceholder(t *testing.T) {
	ph := missingRefPlaceholder("product_ulid", "01PRODUCT")
	if ph["product_ulid"] != "01PRODUCT" {
		t.Errorf("placeholder must keep the reference key, got %v", ph)
	}
	if ph["missing"] != true {
		t.Errorf("placeholder must set missing=true, got %v", ph)
	}
	if len(ph) != 2 {
		t.Errorf("placeholder must not leak extra fields, got %v", ph)
	}
}

// bug070LegacyHandler 模拟「未实现 DoResolve」的第三方 CascadeHandler 实现。
// 用于验证 resolveRefs 的回退路径（不破坏既有接口兼容性）。
type bug070LegacyHandler struct {
	doListCalled bool
}

func (h *bug070LegacyHandler) DoCreate(context.Context, []map[string]any) ([]any, error) {
	return nil, nil
}
func (h *bug070LegacyHandler) DoDelete(context.Context, []any) error             { return nil }
func (h *bug070LegacyHandler) DoDeleteByFK(context.Context, string, []any) error { return nil }
func (h *bug070LegacyHandler) DoUpdate(context.Context, string, any, []map[string]any, bool) error {
	return nil
}
func (h *bug070LegacyHandler) DoList(context.Context, string, any, bool) ([]map[string]any, error) {
	h.doListCalled = true
	return nil, nil
}
func (h *bug070LegacyHandler) DoGetByID(context.Context, any) (map[string]any, error) {
	return nil, nil
}
func (h *bug070LegacyHandler) DoActivate(context.Context, any) error { return nil }
func (h *bug070LegacyHandler) DoListVersions(context.Context, any, string) ([]map[string]any, error) {
	return nil, nil
}
func (h *bug070LegacyHandler) DoEditVersion(context.Context, any, map[string]any) (map[string]any, error) {
	return nil, nil
}
func (h *bug070LegacyHandler) PKField() string     { return "ulid" }
func (h *bug070LegacyHandler) SelfFKField() string { return "" }

// 编译期断言：stub 满足 CascadeHandler，但不满足 refResolveHandler。
var _ CascadeHandler = (*bug070LegacyHandler)(nil)

// TestBug070ResolveRefsFallback 向后兼容：未实现 DoResolve 的 CascadeHandler
// （如第三方/自定义实现）自动回退 DoList，不破坏既有接口。
func TestBug070ResolveRefsFallback(t *testing.T) {
	stub := &bug070LegacyHandler{}
	if _, ok := any(stub).(refResolveHandler); ok {
		t.Fatal("stub must NOT implement DoResolve (fallback path under test)")
	}
	got, err := resolveRefs(context.Background(), stub, "ulid", []any{"a"})
	if err != nil {
		t.Fatalf("resolveRefs fallback: %v", err)
	}
	if got != nil {
		t.Errorf("fallback delegates to stub.DoList which returns nil, got %v", got)
	}
	if !stub.doListCalled {
		t.Error("fallback must call DoList on handlers without DoResolve")
	}
}
