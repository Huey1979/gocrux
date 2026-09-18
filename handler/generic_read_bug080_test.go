package handler

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	errs "github.com/Huey1979/gocrux/errors"
	"github.com/Huey1979/gocrux/repository"
	"github.com/Huey1979/gocrux/service"
)

// ============================================================
// BUG-080 回归测试：单条 get 的向上引用（References）展开
//
// 缺陷：expandGet 的 References 分支仍用 DoGetByID（BUG-070 四个引用展开
// 调用点只改了 3 个），引用目标不存在时 ErrRecordNotFound 经 %w 穿透，
// 被 handler 统一映射成 404「本条记录不存在」——
//   - 同一主键 get 返回 404，list 照常返回该行（自相矛盾）；
//   - 一个坏引用 return nil 中断整个 expandGet，ChildRefs/Cascades 全部不执行。
//
// 修复（方案 A，与 BUG-070 同构）：References 展开也走 resolveRefs（引用解析
// 模式），缺失落 {<pk>: <id>, "missing": true} 占位；附带把引用解析错误从
// ErrRecordNotFound 的 404 映射里摘出来（errors.IsRefResolveError）。
// ============================================================

// -------- 测试实体 --------

// bug080Child 当前记录（含指向 parent 的逻辑外键）。
type bug080Child struct {
	ULID       string    `gorm:"column:ulid;primaryKey;size:26" json:"ulid"`
	Name       string    `gorm:"column:name;size:100" json:"name"`
	ParentULID string    `gorm:"column:parent_ulid;size:26" json:"parent_ulid"`
	TagULIDs   string    `gorm:"column:tag_ulids;size:200" json:"tag_ulids"`
	IsDeleted  int8      `gorm:"column:is_deleted;default:0" json:"is_deleted"`
	CreatedAt  time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt  time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (d *bug080Child) SetDefaults()             {}
func (d *bug080Child) SetCreatedAt(t time.Time) { d.CreatedAt = t }
func (d *bug080Child) SetCreatedBy(string)      {}
func (d *bug080Child) SetUpdatedAt(t time.Time) { d.UpdatedAt = t }
func (d *bug080Child) SetUpdatedBy(string)      {}
func (d *bug080Child) SupportsDraft() bool      { return false }
func (d *bug080Child) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *bug080Child) PKField() string          { return "ulid" }
func (d *bug080Child) SelfFKField() string      { return "" }

// bug080Parent 被引用的父记录。主键列名与 JSON 名刻意不一致
// （列 parent_ulid vs JSON "parent_ulid" 一致，但另建 bug080ShapeParent 验证差异场景）。
type bug080Parent struct {
	ULID      string    `gorm:"column:parent_ulid;primaryKey;size:26" json:"parent_ulid"`
	Name      string    `gorm:"column:name;size:100" json:"name"`
	IsDeleted int8      `gorm:"column:is_deleted;default:0" json:"is_deleted"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (d *bug080Parent) SetDefaults()             {}
func (d *bug080Parent) SetCreatedAt(t time.Time) { d.CreatedAt = t }
func (d *bug080Parent) SetCreatedBy(string)      {}
func (d *bug080Parent) SetUpdatedAt(t time.Time) { d.UpdatedAt = t }
func (d *bug080Parent) SetUpdatedBy(string)      {}
func (d *bug080Parent) SupportsDraft() bool      { return false }
func (d *bug080Parent) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *bug080Parent) PKField() string          { return "parent_ulid" }
func (d *bug080Parent) SelfFKField() string      { return "" }

// bug080ShapeParent 主键列名 field_ulid ≠ JSON 名 ulid（heims 真实形态），
// 用于验证引用缺失占位用的是目标 Handler 的输出名，而不是当前实体的。
type bug080ShapeParent struct {
	ULID      string    `gorm:"column:field_ulid;primaryKey;size:26" json:"ulid"`
	Name      string    `gorm:"column:name;size:100" json:"name"`
	IsDeleted int8      `gorm:"column:is_deleted;default:0" json:"is_deleted"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (d *bug080ShapeParent) SetDefaults()             {}
func (d *bug080ShapeParent) SetCreatedAt(t time.Time) { d.CreatedAt = t }
func (d *bug080ShapeParent) SetCreatedBy(string)      {}
func (d *bug080ShapeParent) SetUpdatedAt(t time.Time) { d.UpdatedAt = t }
func (d *bug080ShapeParent) SetUpdatedBy(string)      {}
func (d *bug080ShapeParent) SupportsDraft() bool      { return false }
func (d *bug080ShapeParent) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *bug080ShapeParent) PKField() string          { return "field_ulid" }
func (d *bug080ShapeParent) SelfFKField() string      { return "" }

// -------- 构造辅助 --------

// openBug080DB 打开独立 sqlite 内存库并迁移给定表。
func openBug080DB(t *testing.T, objs ...any) *gorm.DB {
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
	if err := db.AutoMigrate(objs...); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return db
}

// newBug080ParentHandler 构造普通父 Handler（引用目标）。
func newBug080ParentHandler(t *testing.T, db *gorm.DB) *GenericHandler[*bug080Parent] {
	t.Helper()
	svc := service.NewGenericService[*bug080Parent](repository.NewCRUDWithDB[*bug080Parent](db), service.Config[*bug080Parent]{})
	return NewGenericHandlerWithSvc[*bug080Parent](svc, "bug080_parent", HandlerConfig[*bug080Parent]{})
}

// newBug080ChildHandler 构造带 References 的子 Handler，
// 并注入 refHandler（handlerReg 中的引用目标）与可选的级联子 Handler 名。
func newBug080ChildHandler(
	t *testing.T,
	db *gorm.DB,
	parentName string,
	parent CascadeHandler,
	cascades []CascadeRelation,
	cascadeChildName string,
	cascadeChild CascadeHandler,
) *GenericHandler[*bug080Child] {
	t.Helper()
	svc := service.NewGenericService[*bug080Child](repository.NewCRUDWithDB[*bug080Child](db), service.Config[*bug080Child]{})
	h := NewGenericHandlerWithSvc[*bug080Child](svc, "bug080_child", HandlerConfig[*bug080Child]{
		References: []ReferenceRelation{
			{Field: "parent_ulid", HandlerName: parentName, ResultField: "parent_info"},
		},
		Cascades: cascades,
	})
	reg := NewHandlerRegistry()
	reg.Register(parentName, parent)
	if cascadeChildName != "" {
		reg.Register(cascadeChildName, cascadeChild)
	}
	h.SetHandlerReg(reg)
	return h
}

// bug080CascadeStub 级联子 Handler 桩：记录是否被调用，并返回固定子记录，
// 用于验证「一个坏引用不得中断同一次 expandGet 的后续 Cascades」（BUG-080 §五·4）。
type bug080CascadeStub struct {
	called   bool
	fkField  string
	fkValue  any
	children []map[string]any
}

func (s *bug080CascadeStub) DoCreate(context.Context, []map[string]any) ([]any, error) {
	return nil, nil
}
func (s *bug080CascadeStub) DoDelete(context.Context, []any) error             { return nil }
func (s *bug080CascadeStub) DoDeleteByFK(context.Context, string, []any) error { return nil }
func (s *bug080CascadeStub) DoUpdate(context.Context, string, any, []map[string]any, bool) error {
	return nil
}
func (s *bug080CascadeStub) DoList(_ context.Context, fkField string, fkValue any, _ bool) ([]map[string]any, error) {
	s.called = true
	s.fkField = fkField
	s.fkValue = fkValue
	return s.children, nil
}
func (s *bug080CascadeStub) DoGetByID(context.Context, any) (map[string]any, error) { return nil, nil }
func (s *bug080CascadeStub) DoActivate(context.Context, any) error                  { return nil }
func (s *bug080CascadeStub) DoListVersions(context.Context, any, string) ([]map[string]any, error) {
	return nil, nil
}
func (s *bug080CascadeStub) DoEditVersion(context.Context, any, map[string]any) (map[string]any, error) {
	return nil, nil
}
func (s *bug080CascadeStub) PKField() string     { return "child_id" }
func (s *bug080CascadeStub) SelfFKField() string { return "" }

// bug080FailResolveHandler 引用目标 Handler 桩：
// 实现 DoResolve 但恒返回指定错误，用于验证「真错误继续上抛」。
type bug080FailResolveHandler struct {
	err error
}

func (h *bug080FailResolveHandler) DoCreate(context.Context, []map[string]any) ([]any, error) {
	return nil, nil
}
func (h *bug080FailResolveHandler) DoDelete(context.Context, []any) error             { return nil }
func (h *bug080FailResolveHandler) DoDeleteByFK(context.Context, string, []any) error { return nil }
func (h *bug080FailResolveHandler) DoUpdate(context.Context, string, any, []map[string]any, bool) error {
	return nil
}
func (h *bug080FailResolveHandler) DoList(context.Context, string, any, bool) ([]map[string]any, error) {
	return nil, nil
}
func (h *bug080FailResolveHandler) DoGetByID(context.Context, any) (map[string]any, error) {
	return nil, nil
}
func (h *bug080FailResolveHandler) DoActivate(context.Context, any) error { return nil }
func (h *bug080FailResolveHandler) DoListVersions(context.Context, any, string) ([]map[string]any, error) {
	return nil, nil
}
func (h *bug080FailResolveHandler) DoEditVersion(context.Context, any, map[string]any) (map[string]any, error) {
	return nil, nil
}
func (h *bug080FailResolveHandler) DoResolve(context.Context, string, []any) ([]map[string]any, error) {
	return nil, h.err
}
func (h *bug080FailResolveHandler) PKField() string     { return "parent_ulid" }
func (h *bug080FailResolveHandler) SelfFKField() string { return "" }

// bug080Ctx 构造带展开深度的 context —— HTTP 入口由 injectDepth 注入
// （默认 defaultExpandDepth），测试直接调 expandGet 时需自行补齐，
// 否则 effectiveExpandDepth 读到 depth=0 会跳过全部引用展开。
func bug080Ctx() context.Context {
	return withDepth(context.Background(), defaultExpandDepth)
}

// ============================================================
// 用例
// ============================================================

// TestBug080MissingRefBecomesPlaceholder 核心：引用目标不存在时，
// expandGet 不再整行报错，而是落 missing 占位；且与 list 展开结果一致。
func TestBug080MissingRefBecomesPlaceholder(t *testing.T) {
	db := openBug080DB(t, &bug080Child{}, &bug080Parent{})
	// 父表为空（heims 场景：notification_channel 一行都没有）
	parentH := newBug080ParentHandler(t, db)
	h := newBug080ChildHandler(t, db, "bug080_parent", parentH, nil, "", nil)

	child := &bug080Child{ULID: "c1", Name: "child", ParentULID: "no-such-parent"}

	// 修复前：DoGetByID → ErrRecordNotFound → ErrRefResolve → 404
	got, err := h.expandGet(bug080Ctx(), &child)
	if err != nil {
		t.Fatalf("BUG-080: dangling reference must NOT fail the whole get, got err=%v", err)
	}
	ref, ok := got["parent_info"].(map[string]any)
	if !ok {
		t.Fatalf("parent_info must be a placeholder map, got %#v", got["parent_info"])
	}
	if ref["missing"] != true {
		t.Errorf("placeholder must set missing=true, got %#v", ref)
	}
	if fmt.Sprint(ref["parent_ulid"]) != "no-such-parent" {
		t.Errorf("placeholder must keep the reference key/value, got %#v", ref)
	}
	if len(ref) != 2 {
		t.Errorf("placeholder must not leak extra fields, got %#v", ref)
	}
	// 主记录字段仍然完整
	if got["name"] != "child" {
		t.Errorf("main record fields must be intact, got %#v", got["name"])
	}

	// 与 list 侧展开逐字一致（同一行两条读路径答案相同）：
	// 先落库再由 list 路径批量展开 References，结果同样应是 missing 占位。
	if err := db.Create(child).Error; err != nil {
		t.Fatalf("seed child: %v", err)
	}
	items, _, err := h.listPipeline(bug080Ctx(), map[string]any{"ulid": "c1"}, false)
	if err != nil {
		t.Fatalf("listPipeline: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("list must return the row, got %d", len(items))
	}
	listRef, ok := items[0]["parent_info"].(map[string]any)
	if !ok {
		t.Fatalf("list parent_info must be a placeholder map, got %#v", items[0]["parent_info"])
	}
	if listRef["missing"] != true || fmt.Sprint(listRef["parent_ulid"]) != "no-such-parent" {
		t.Errorf("list placeholder must match get placeholder, get=%#v list=%#v", ref, listRef)
	}
}

// TestBug080MissingPlaceholderUsesTargetOutputKey 占位 key 取自引用目标 Handler
// 的输出名（列 field_ulid → json ulid），而不是当前实体的字段表。
func TestBug080MissingPlaceholderUsesTargetOutputKey(t *testing.T) {
	db := openBug080DB(t, &bug080Child{}, &bug080ShapeParent{})
	if err := db.Create(&bug080ShapeParent{ULID: "p-live", Name: "live"}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	svcP := service.NewGenericService[*bug080ShapeParent](repository.NewCRUDWithDB[*bug080ShapeParent](db), service.Config[*bug080ShapeParent]{})
	parentH := NewGenericHandlerWithSvc[*bug080ShapeParent](svcP, "bug080_shape_parent", HandlerConfig[*bug080ShapeParent]{})

	// 子实体的 FK 列名刻意与父主键列名一致（parent_ulid），但父实体的
	// 主键列是 field_ulid —— 若用当前实体 M 解析会取不到，回退列名。
	svc := service.NewGenericService[*bug080Child](repository.NewCRUDWithDB[*bug080Child](db), service.Config[*bug080Child]{})
	h := NewGenericHandlerWithSvc[*bug080Child](svc, "bug080_child", HandlerConfig[*bug080Child]{
		References: []ReferenceRelation{{Field: "parent_ulid", HandlerName: "bug080_shape_parent", ResultField: "parent_info"}},
	})
	reg := NewHandlerRegistry()
	reg.Register("bug080_shape_parent", parentH)
	h.SetHandlerReg(reg)

	// 目标 Handler 的输出名由它自己的实体决定
	if got := refOutputKey(parentH); got != "ulid" {
		t.Fatalf("refOutputKey(parent) = %q, want %q (json tag of parent PK)", got, "ulid")
	}

	// 命中：正常记录，无 missing 字段
	c1 := bug080Child{ULID: "c1", ParentULID: "p-live"}
	c1ptr := &c1
	hit, err := h.expandGet(bug080Ctx(), &c1ptr)
	if err != nil {
		t.Fatalf("expandGet(hit): %v", err)
	}
	refHit, ok := hit["parent_info"].(map[string]any)
	if !ok {
		t.Fatalf("parent_info must be resolved record, got %#v", hit["parent_info"])
	}
	if _, hasMissing := refHit["missing"]; hasMissing {
		t.Errorf("resolved reference must NOT carry missing flag, got %#v", refHit)
	}
	if refHit["name"] != "live" {
		t.Errorf("resolved reference name = %v, want live", refHit["name"])
	}

	// 未命中：占位 key 必须是目标输出名 ulid
	c2 := bug080Child{ULID: "c2", ParentULID: "p-gone"}
	c2ptr := &c2
	miss, err := h.expandGet(bug080Ctx(), &c2ptr)
	if err != nil {
		t.Fatalf("expandGet(miss): %v", err)
	}
	refMiss, ok := miss["parent_info"].(map[string]any)
	if !ok {
		t.Fatalf("parent_info must be placeholder, got %#v", miss["parent_info"])
	}
	if fmt.Sprint(refMiss["ulid"]) != "p-gone" {
		t.Errorf("placeholder key must be the target output name ulid, got %#v", refMiss)
	}
	if refMiss["missing"] != true {
		t.Errorf("placeholder must set missing=true, got %#v", refMiss)
	}
}

// TestBug080SoftDeletedRefStillAnchor 引用目标已软删时，锚点仍返回且带 is_deleted=1
// （对齐 BUG-069/070 读路径口径：读路径不套用「当前有效」过滤）。
func TestBug080SoftDeletedRefStillAnchor(t *testing.T) {
	db := openBug080DB(t, &bug080Child{}, &bug080Parent{})
	if err := db.Create(&bug080Parent{ULID: "p-deleted", Name: "gone", IsDeleted: 1}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	parentH := newBug080ParentHandler(t, db)
	h := newBug080ChildHandler(t, db, "bug080_parent", parentH, nil, "", nil)

	child := &bug080Child{ULID: "c1", ParentULID: "p-deleted"}
	got, err := h.expandGet(bug080Ctx(), &child)
	if err != nil {
		t.Fatalf("expandGet: %v", err)
	}
	ref, ok := got["parent_info"].(map[string]any)
	if !ok || ref["missing"] == true {
		t.Fatalf("soft-deleted anchor must still be returned, got %#v", got["parent_info"])
	}
	if fmt.Sprint(ref["is_deleted"]) != "1" {
		t.Errorf("anchor must expose is_deleted=1, got %#v", ref["is_deleted"])
	}
	if ref["name"] != "gone" {
		t.Errorf("anchor name = %v, want gone", ref["name"])
	}
}

// TestBug080BadRefDoesNotAbortCascades 一个坏引用不得中断同一次 expandGet 的
// ChildRefs / Cascades（修复前 return nil, err 会全部跳过）。
func TestBug080BadRefDoesNotAbortCascades(t *testing.T) {
	db := openBug080DB(t, &bug080Child{}, &bug080Parent{})
	// 父表为空 → 引用必然缺失
	parentH := newBug080ParentHandler(t, db)
	stub := &bug080CascadeStub{children: []map[string]any{{"child_id": "k1"}}}

	h := newBug080ChildHandler(t, db, "bug080_parent", parentH,
		[]CascadeRelation{{HandlerName: "bug080_kids", ChildrenField: "kids", FKField: "parent_ulid", FollowPublished: false}},
		"bug080_kids", stub)

	child := &bug080Child{ULID: "c1", ParentULID: "missing-parent"}
	got, err := h.expandGet(bug080Ctx(), &child)
	if err != nil {
		t.Fatalf("BUG-080: a single bad reference must not abort the whole expandGet: %v", err)
	}
	if _, ok := got["parent_info"]; !ok {
		t.Error("bad reference must still produce a placeholder")
	}
	if !stub.called {
		t.Fatal("BUG-080: Cascades must still run after a dangling reference")
	}
	if stub.fkField != "parent_ulid" {
		t.Errorf("cascade FKField = %q, want parent_ulid", stub.fkField)
	}
	kids, ok := got["kids"].([]map[string]any)
	if !ok || len(kids) != 1 {
		t.Errorf("cascade children must be attached, got %#v", got["kids"])
	}
}

// TestBug080RealErrorStillPropagates 真错误（DB 故障、权限失败等）必须继续上抛，
// 不得静默降级为占位。
func TestBug080RealErrorStillPropagates(t *testing.T) {
	db := openBug080DB(t, &bug080Child{})
	boom := errors.New("storage unavailable")
	failH := &bug080FailResolveHandler{err: boom}

	svc := service.NewGenericService[*bug080Child](repository.NewCRUDWithDB[*bug080Child](db), service.Config[*bug080Child]{})
	h := NewGenericHandlerWithSvc[*bug080Child](svc, "bug080_child", HandlerConfig[*bug080Child]{
		References: []ReferenceRelation{{Field: "parent_ulid", HandlerName: "bug080_fail", ResultField: "parent_info"}},
	})
	reg := NewHandlerRegistry()
	reg.Register("bug080_fail", failH)
	h.SetHandlerReg(reg)

	child := &bug080Child{ULID: "c1", ParentULID: "p1"}
	_, err := h.expandGet(bug080Ctx(), &child)
	if err == nil {
		t.Fatal("BUG-080: non-not-found resolve errors must still propagate")
	}
	if !errors.Is(err, boom) {
		t.Errorf("original error must be preserved in chain, got %v", err)
	}
	if !errs.IsRefResolveError(err) {
		t.Errorf("error must be marked as ref-resolve failure, got %v", err)
	}
}

// TestBug080RefResolveErrorNotMappedTo404 引用解析失败即使包装了
// ErrRecordNotFound，也不能被映射成 404（报错宾语是引用，不是主记录）。
func TestBug080RefResolveErrorNotMappedTo404(t *testing.T) {
	wrapped := errs.ErrRefResolve("notification_channel", errs.ErrRecordNotFound)
	if !errors.Is(wrapped, errs.ErrRecordNotFound) {
		t.Fatal("sanity: wrapped error should still expose ErrRecordNotFound in chain")
	}
	// 关键：先于 ErrRecordNotFound 判定
	if got := mapServiceError(wrapped); got == 404 {
		t.Errorf("BUG-080: ErrRefResolve(ErrRecordNotFound) must NOT map to 404, got %d", got)
	}
	if got := mapServiceError(wrapped); got != 500 {
		t.Errorf("ref resolve failure = %d, want 500", got)
	}
	// 四个构造函数同族
	for name, e := range map[string]error{
		"ErrRefBatchResolve":      errs.ErrRefBatchResolve("h", errs.ErrRecordNotFound),
		"ErrChildRefResolve":      errs.ErrChildRefResolve("h", errs.ErrRecordNotFound),
		"ErrChildRefBatchResolve": errs.ErrChildRefBatchResolve("h", errs.ErrRecordNotFound),
	} {
		if !errs.IsRefResolveError(e) {
			t.Errorf("%s must be recognized as ref-resolve error", name)
		}
		if got := mapServiceError(e); got == 404 {
			t.Errorf("BUG-080: %s must NOT map to 404, got %d", name, got)
		}
	}
	// 不回归：普通 not-found 仍是 404
	if got := mapServiceError(errs.ErrRecordNotFound); got != 404 {
		t.Errorf("plain ErrRecordNotFound must stay 404, got %d", got)
	}
	if got := mapServiceError(fmt.Errorf("查询待更新记录失败: %w", errs.ErrRecordNotFound)); got != 404 {
		t.Errorf("plain wrapped ErrRecordNotFound must stay 404, got %d", got)
	}
}

// TestBug080NormalRefHasNoMissing 不命中为 0：正常引用（目标存在且未删）
// 的响应中不得出现 missing 字段。
func TestBug080NormalRefHasNoMissing(t *testing.T) {
	db := openBug080DB(t, &bug080Child{}, &bug080Parent{})
	if err := db.Create(&bug080Parent{ULID: "p1", Name: "ok"}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	parentH := newBug080ParentHandler(t, db)
	h := newBug080ChildHandler(t, db, "bug080_parent", parentH, nil, "", nil)

	child := &bug080Child{ULID: "c1", ParentULID: "p1"}
	got, err := h.expandGet(bug080Ctx(), &child)
	if err != nil {
		t.Fatalf("expandGet: %v", err)
	}
	ref, ok := got["parent_info"].(map[string]any)
	if !ok {
		t.Fatalf("parent_info must be resolved, got %#v", got["parent_info"])
	}
	if _, has := ref["missing"]; has {
		t.Errorf("resolved reference must not carry missing, got %#v", ref)
	}
	if ref["name"] != "ok" {
		t.Errorf("resolved name = %v, want ok", ref["name"])
	}
}
