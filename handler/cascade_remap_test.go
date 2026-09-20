package handler

import (
	"context"
	"encoding/json"
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
// 版本化级联引用重映射 回归测试
//
// 需求：版本化实体更新时子表被复制重建为新 ULID，子记录之间的横向引用
// （field_access.field_ulid → write_field.field_ulid 等）必须一并重写指向本版本
// 新记录，否则新版本引用悬空/指向旧版本。
//
// 覆盖 REQ「验收用例」5 条 + 四种引用形态 + 映射优先级与报错口径。
// ============================================================

// -------- 测试实体 --------

// remapParent 版本化父实体。
type remapParent struct {
	ULID          string    `gorm:"column:parent_ulid;primaryKey;size:26" json:"ulid"`
	Code          string    `gorm:"column:code;size:64" json:"code"`
	Name          string    `gorm:"column:name;size:100" json:"name"`
	VersionCode   string    `gorm:"column:version_code;size:20" json:"version_code"`
	VersionStatus string    `gorm:"column:version_status;size:20" json:"version_status"`
	IsCurrent     int8      `gorm:"column:is_current;default:0" json:"is_current"`
	ParentVersion string    `gorm:"column:parent_version;size:26" json:"parent_version"`
	Remark        string    `gorm:"column:version_remark;size:200" json:"version_remark"`
	CreatedAt     time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt     time.Time `gorm:"column:updated_at" json:"updated_at"`
	IsDeleted     int8      `gorm:"column:is_deleted;default:0" json:"-"`
}

func (d *remapParent) SetDefaults()             {}
func (d *remapParent) SetCreatedAt(t time.Time) { d.CreatedAt = t }
func (d *remapParent) SetCreatedBy(string)      {}
func (d *remapParent) SetUpdatedAt(t time.Time) { d.UpdatedAt = t }
func (d *remapParent) SetUpdatedBy(string)      {}
func (d *remapParent) SupportsDraft() bool      { return false }
func (d *remapParent) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *remapParent) PKField() string          { return "parent_ulid" }
func (d *remapParent) SelfFKField() string      { return "" }

// remapField 子实体（被引用的「字段」），模拟 heims write_field。
// 引用形态用字符串列承载 JSON（贴近 heims 的 type:json 列）。
type remapField struct {
	ULID      string `gorm:"column:field_ulid;primaryKey;size:26" json:"ulid"`
	ParentID  string `gorm:"column:parent_ulid;size:26" json:"parent_ulid"`
	FieldCode string `gorm:"column:field_code;size:64" json:"field_code"`
	Name      string `gorm:"column:name;size:100" json:"name"`

	// 四种引用形态的载体
	RefScalar  string `gorm:"column:ref_scalar;size:26" json:"ref_scalar"`     // 标量 ULID
	RefArray   string `gorm:"column:ref_array;type:json" json:"ref_array"`     // ULID 数组
	RefObjects string `gorm:"column:ref_objects;type:json" json:"ref_objects"` // 对象数组
	RefNested  string `gorm:"column:ref_nested;type:json" json:"ref_nested"`   // 嵌套 JSON

	IsDeleted int8 `gorm:"column:is_deleted;default:0" json:"-"`
}

func (d *remapField) SetDefaults()             {}
func (d *remapField) SetCreatedAt(_ time.Time) {}
func (d *remapField) SetCreatedBy(string)      {}
func (d *remapField) SetUpdatedAt(_ time.Time) {}
func (d *remapField) SetUpdatedBy(string)      {}
func (d *remapField) SupportsDraft() bool      { return false }
func (d *remapField) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *remapField) PKField() string          { return "field_ulid" }
func (d *remapField) SelfFKField() string      { return "" }

// -------- 测试装置 --------

func openRemapDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sqlDB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&remapParent{}, &remapField{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return db
}

// newRemapHandlers 构造父（版本化，含 Remaps 声明）与子 Handler。
func newRemapHandlers(t *testing.T, db *gorm.DB, remaps []ReferenceRemap) (*GenericHandler[*remapParent], *GenericHandler[*remapField]) {
	t.Helper()

	childRepo := repository.NewCRUDWithDB[*remapField](db)
	childSvc := service.NewGenericService[*remapField](childRepo, service.Config[*remapField]{
		EntityName: "remap_field",
	})
	childH := NewGenericHandlerWithSvc[*remapField](childSvc, "remap_field", HandlerConfig[*remapField]{
		PathPrefix: "/remap/field",
	})
	// 安装落库前重映射钩子（子 Handler 侧执行）
	childH.InstallRemapHook()

	parentRepo := repository.NewCRUDWithDB[*remapParent](db)
	parentSvc := service.NewGenericService[*remapParent](parentRepo, service.Config[*remapParent]{
		EntityName:  "remap_parent",
		VersionMode: true,
		VersionFields: &service.VersionFieldMapping{
			ULIDField: "ULID", CodeField: "Code", VersionField: "VersionCode",
			CurrentField: "IsCurrent", StatusField: "VersionStatus",
			ParentField: "ParentVersion", RemarkField: "Remark",
		},
	})
	reg := NewHandlerRegistry()
	reg.Register("remap_field", childH)
	parentH := NewGenericHandlerWithSvc[*remapParent](parentSvc, "remap_parent", HandlerConfig[*remapParent]{
		PathPrefix: "/remap/parent",
		Cascades: []CascadeRelation{{
			HandlerName: "remap_field", ChildrenField: "fields", FKField: "parent_ulid",
			OnCreate: true, OnUpdate: true, Remaps: remaps,
		}},
	})
	parentH.SetHandlerReg(reg)
	parentH.SetTxCoord(NewTxCoordinator(db, nil))
	return parentH, childH
}

// scalarRemap 单绑定（标量 ULID）的重映射声明。
func scalarRemap() []ReferenceRemap {
	return []ReferenceRemap{{
		SourceCodeField: "field_code",
		Bindings: []ReferenceBinding{
			{Field: "ref_scalar", Mode: RemapModeScalar},
		},
	}}
}

func remapReq(m map[string]any) service.CrudRequest[*remapParent] {
	return &MapRequest[*remapParent]{data: m}
}

// ============================================================
// 用例 1：版本更新重建子表 → 引用指向新版本（核心）
// ============================================================

func TestRemapUpdateRewritesScalarRefToNewVersion(t *testing.T) {
	db := openRemapDB(t)
	parentH, _ := newRemapHandlers(t, db, scalarRemap())

	// 1. create v1：两个字段，f2.ref_scalar → f1
	createRaw := map[string]any{
		"code": "R1", "name": "v1",
		"fields": []map[string]any{
			{"field_code": "f1", "name": "字段1"},
			{"field_code": "f2", "name": "字段2"}, // ref_scalar 稍后由 _beforeCreatePersist 之外的方式填
		},
	}
	ctx := context.WithValue(context.Background(), rawCreateMapsKey{}, []map[string]any{createRaw})
	created, err := parentH._doCreate(ctx, []service.CrudRequest[*remapParent]{remapReq(createRaw)})
	if err != nil {
		t.Fatalf("_doCreate: %v", err)
	}
	v1 := (*created[0]).ULID

	var v1Fields []remapField
	db.Where("parent_ulid = ?", v1).Order("field_code").Find(&v1Fields)
	if len(v1Fields) != 2 {
		t.Fatalf("v1 应有 2 个字段, 实际 %d", len(v1Fields))
	}
	f1v1, f2v1 := v1Fields[0], v1Fields[1]

	// 手工建立「f2 引用 f1」的初始状态（模拟真实业务里子记录间的引用）
	if err := db.Model(&remapField{}).Where("field_ulid = ?", f2v1.ULID).
		Update("ref_scalar", f1v1.ULID).Error; err != nil {
		t.Fatalf("建立初始引用: %v", err)
	}

	// 2. update：版本化重建子表（父表回填旧子数据 → 清除 PK → 新 ULID）
	updateRaw := map[string]any{"id": v1, "name": "v2"}
	uctx := context.WithValue(context.Background(), rawUpdateMapsKey{}, []map[string]any{updateRaw})
	updated, err := parentH._doUpdate(uctx, []service.CrudRequest[*remapParent]{remapReq(updateRaw)}, false)
	if err != nil {
		t.Fatalf("_doUpdate: %v", err)
	}
	v2 := (*updated[0]).ULID
	if v2 == v1 {
		t.Fatal("版本化 update 应产生新版本 ULID")
	}

	var v2Fields []remapField
	db.Where("parent_ulid = ?", v2).Order("field_code").Find(&v2Fields)
	if len(v2Fields) != 2 {
		t.Fatalf("v2 应有 2 个字段（快照复制）, 实际 %d", len(v2Fields))
	}
	f1v2, f2v2 := v2Fields[0], v2Fields[1]

	// ★ 核心断言：v2 中 f2 的引用指向 v2 的 f1，而不是 v1 的 f1
	if f2v2.RefScalar != f1v2.ULID {
		t.Errorf("REQ 用例1: v2 的 ref_scalar 应指向新版本字段 %s，实际 %s（旧版本字段是 %s）",
			f1v2.ULID, f2v2.RefScalar, f1v1.ULID)
	}
	if f2v2.RefScalar == f1v1.ULID {
		t.Error("REQ 用例1: 引用仍指向旧版本子记录（跨版本悬挂引用）")
	}

	// 用例 4：旧快照不可变
	var v1Reloaded remapField
	if err := db.First(&v1Reloaded, "field_ulid = ?", f2v1.ULID).Error; err != nil {
		t.Fatalf("读回 v1 子行: %v", err)
	}
	if v1Reloaded.RefScalar != f1v1.ULID {
		t.Errorf("REQ 用例4: 新版本重写不得修改旧版本子记录，v1 的引用应仍为 %s，实际 %s",
			f1v1.ULID, v1Reloaded.RefScalar)
	}
}

// ============================================================
// 用例 2：ULID 数组
// ============================================================

func TestRemapULIDArray(t *testing.T) {
	db := openRemapDB(t)
	remaps := []ReferenceRemap{{
		SourceCodeField: "field_code",
		Bindings: []ReferenceBinding{
			{Field: "ref_array", Mode: RemapModeULIDArray},
		},
	}}
	parentH, _ := newRemapHandlers(t, db, remaps)

	createRaw := map[string]any{
		"code": "R2", "name": "v1",
		"fields": []map[string]any{
			{"field_code": "a", "name": "A"},
			{"field_code": "b", "name": "B"},
			{"field_code": "c", "name": "C"},
		},
	}
	ctx := context.WithValue(context.Background(), rawCreateMapsKey{}, []map[string]any{createRaw})
	created, _ := parentH._doCreate(ctx, []service.CrudRequest[*remapParent]{remapReq(createRaw)})
	v1 := (*created[0]).ULID

	var v1Fields []remapField
	db.Where("parent_ulid = ?", v1).Order("field_code").Find(&v1Fields)
	// c 引用 [a, b]
	arrJSON := fmt.Sprintf(`[%q,%q]`, v1Fields[0].ULID, v1Fields[1].ULID)
	if err := db.Model(&remapField{}).Where("field_ulid = ?", v1Fields[2].ULID).
		Update("ref_array", arrJSON).Error; err != nil {
		t.Fatalf("建立数组引用: %v", err)
	}

	updateRaw := map[string]any{"id": v1, "name": "v2"}
	uctx := context.WithValue(context.Background(), rawUpdateMapsKey{}, []map[string]any{updateRaw})
	updated, err := parentH._doUpdate(uctx, []service.CrudRequest[*remapParent]{remapReq(updateRaw)}, false)
	if err != nil {
		t.Fatalf("_doUpdate: %v", err)
	}
	v2 := (*updated[0]).ULID

	var v2Fields []remapField
	db.Where("parent_ulid = ?", v2).Order("field_code").Find(&v2Fields)
	var arr []string
	if err := json.Unmarshal([]byte(v2Fields[2].RefArray), &arr); err != nil {
		t.Fatalf("解析数组失败: %v（原文 %s）", err, v2Fields[2].RefArray)
	}
	if len(arr) != 2 || arr[0] != v2Fields[0].ULID || arr[1] != v2Fields[1].ULID {
		t.Errorf("REQ 用例2: ULID 数组应全部指向新版本节点\n want [%s %s]\n got  %v",
			v2Fields[0].ULID, v2Fields[1].ULID, arr)
	}
}

// ============================================================
// 用例 3：对象数组（含 code 兜底）
// ============================================================

func TestRemapObjectArrayWithCodeFallback(t *testing.T) {
	db := openRemapDB(t)
	remaps := []ReferenceRemap{{
		SourceCodeField: "field_code",
		Bindings: []ReferenceBinding{
			{Field: "ref_objects", Mode: RemapModeObjectArray, ULIDKey: "field_ulid", CodeKey: "field_code"},
		},
	}}
	parentH, _ := newRemapHandlers(t, db, remaps)

	createRaw := map[string]any{
		"code": "R3", "name": "v1",
		"fields": []map[string]any{
			{"field_code": "amount", "name": "金额"},
			{"field_code": "val", "name": "校验"}, // 引用 amount
		},
	}
	ctx := context.WithValue(context.Background(), rawCreateMapsKey{}, []map[string]any{createRaw})
	created, _ := parentH._doCreate(ctx, []service.CrudRequest[*remapParent]{remapReq(createRaw)})
	v1 := (*created[0]).ULID

	var v1Fields []remapField
	db.Where("parent_ulid = ?", v1).Order("field_code").Find(&v1Fields)
	// val 的 error_on 引用 amount（同时带 ULID 与 code）
	objJSON := fmt.Sprintf(`[{"field_ulid":%q,"field_code":"amount"}]`, v1Fields[0].ULID)
	if err := db.Model(&remapField{}).Where("field_ulid = ?", v1Fields[1].ULID).
		Update("ref_objects", objJSON).Error; err != nil {
		t.Fatalf("建立对象数组引用: %v", err)
	}

	updateRaw := map[string]any{"id": v1, "name": "v2"}
	uctx := context.WithValue(context.Background(), rawUpdateMapsKey{}, []map[string]any{updateRaw})
	updated, err := parentH._doUpdate(uctx, []service.CrudRequest[*remapParent]{remapReq(updateRaw)}, false)
	if err != nil {
		t.Fatalf("_doUpdate: %v", err)
	}
	v2 := (*updated[0]).ULID

	var v2Fields []remapField
	db.Where("parent_ulid = ?", v2).Order("field_code").Find(&v2Fields)
	// 结构化断言（不比较 JSON 字面量键序）
	items := parseObjectArray(t, v2Fields[1].RefObjects)
	if len(items) != 1 {
		t.Fatalf("REQ 用例3: 对象数组应有 1 项，实际 %d: %s", len(items), v2Fields[1].RefObjects)
	}
	if got := items[0]["field_ulid"]; got != v2Fields[0].ULID {
		t.Errorf("REQ 用例3: 对象数组内 ULID 应指向新版本字段 %s，实际 %v", v2Fields[0].ULID, got)
	}
	if items[0]["field_code"] != "amount" {
		t.Errorf("REQ 用例3: 非 ULID 键（field_code）不应被破坏，实际 %v", items[0]["field_code"])
	}
}

// TestRemapObjectArrayCodeOnlyFallback 老数据只有 code（无 ULID）时用 code 兜底。
func TestRemapObjectArrayCodeOnlyFallback(t *testing.T) {
	db := openRemapDB(t)
	remaps := []ReferenceRemap{{
		SourceCodeField: "field_code",
		Bindings: []ReferenceBinding{
			{Field: "ref_objects", Mode: RemapModeObjectArray, ULIDKey: "field_ulid", CodeKey: "field_code"},
		},
	}}
	parentH, _ := newRemapHandlers(t, db, remaps)

	createRaw := map[string]any{
		"code": "R3b", "name": "v1",
		"fields": []map[string]any{
			{"field_code": "amount", "name": "金额"},
			{"field_code": "val", "name": "校验"},
		},
	}
	ctx := context.WithValue(context.Background(), rawCreateMapsKey{}, []map[string]any{createRaw})
	created, _ := parentH._doCreate(ctx, []service.CrudRequest[*remapParent]{remapReq(createRaw)})
	v1 := (*created[0]).ULID

	var v1Fields []remapField
	db.Where("parent_ulid = ?", v1).Order("field_code").Find(&v1Fields)
	// 只有 code，没有 ULID（老数据形态）
	if err := db.Model(&remapField{}).Where("field_ulid = ?", v1Fields[1].ULID).
		Update("ref_objects", `[{"field_code":"amount"}]`).Error; err != nil {
		t.Fatalf("建立 code-only 引用: %v", err)
	}

	updateRaw := map[string]any{"id": v1, "name": "v2"}
	uctx := context.WithValue(context.Background(), rawUpdateMapsKey{}, []map[string]any{updateRaw})
	updated, err := parentH._doUpdate(uctx, []service.CrudRequest[*remapParent]{remapReq(updateRaw)}, false)
	if err != nil {
		t.Fatalf("_doUpdate: %v", err)
	}
	v2 := (*updated[0]).ULID

	var v2Fields []remapField
	db.Where("parent_ulid = ?", v2).Order("field_code").Find(&v2Fields)
	items := parseObjectArray(t, v2Fields[1].RefObjects)
	if len(items) != 1 {
		t.Fatalf("应有 1 项，实际 %d: %s", len(items), v2Fields[1].RefObjects)
	}
	if got := items[0]["field_ulid"]; got != v2Fields[0].ULID {
		t.Errorf("code 兜底应填出新版本 ULID %s，实际 %v", v2Fields[0].ULID, got)
	}
}

// parseObjectArray 解析对象数组字段（JSON 字符串或原生数组皆可）。
func parseObjectArray(t *testing.T, s string) []map[string]any {
	t.Helper()
	var items []map[string]any
	if err := json.Unmarshal([]byte(s), &items); err != nil {
		t.Fatalf("解析对象数组失败: %v（原文 %s）", err, s)
	}
	return items
}

// ============================================================
// 形态 4：嵌套 JSON
// ============================================================

func TestRemapNestedJSON(t *testing.T) {
	db := openRemapDB(t)
	remaps := []ReferenceRemap{{
		SourceCodeField: "field_code",
		Bindings: []ReferenceBinding{
			{Field: "ref_nested.target.field_ulid", Mode: RemapModeScalar},
		},
	}}
	parentH, _ := newRemapHandlers(t, db, remaps)

	createRaw := map[string]any{
		"code": "R4", "name": "v1",
		"fields": []map[string]any{
			{"field_code": "f1", "name": "F1"},
			{"field_code": "f2", "name": "F2"},
		},
	}
	ctx := context.WithValue(context.Background(), rawCreateMapsKey{}, []map[string]any{createRaw})
	created, _ := parentH._doCreate(ctx, []service.CrudRequest[*remapParent]{remapReq(createRaw)})
	v1 := (*created[0]).ULID

	var v1Fields []remapField
	db.Where("parent_ulid = ?", v1).Order("field_code").Find(&v1Fields)
	nested := fmt.Sprintf(`{"target":{"type":"form","field_ulid":%q}}`, v1Fields[0].ULID)
	if err := db.Model(&remapField{}).Where("field_ulid = ?", v1Fields[1].ULID).
		Update("ref_nested", nested).Error; err != nil {
		t.Fatalf("建立嵌套引用: %v", err)
	}

	updateRaw := map[string]any{"id": v1, "name": "v2"}
	uctx := context.WithValue(context.Background(), rawUpdateMapsKey{}, []map[string]any{updateRaw})
	updated, err := parentH._doUpdate(uctx, []service.CrudRequest[*remapParent]{remapReq(updateRaw)}, false)
	if err != nil {
		t.Fatalf("_doUpdate: %v", err)
	}
	v2 := (*updated[0]).ULID

	var v2Fields []remapField
	db.Where("parent_ulid = ?", v2).Order("field_code").Find(&v2Fields)
	// 断言结构内的 ULID 值（不比较 JSON 字面量与键序）
	var nestedDoc struct {
		Target struct {
			Type      string `json:"type"`
			FieldULID string `json:"field_ulid"`
		} `json:"target"`
	}
	if err := json.Unmarshal([]byte(v2Fields[1].RefNested), &nestedDoc); err != nil {
		t.Fatalf("v2 嵌套 JSON 解析失败: %v（原文 %s）", err, v2Fields[1].RefNested)
	}
	if nestedDoc.Target.FieldULID != v2Fields[0].ULID {
		t.Errorf("嵌套 JSON 内的 ULID 应指向新版本字段 %s，实际 %s",
			v2Fields[0].ULID, nestedDoc.Target.FieldULID)
	}
	if nestedDoc.Target.Type != "form" {
		t.Errorf("嵌套 JSON 的非引用字段不应被破坏，type = %q", nestedDoc.Target.Type)
	}

	// 旧快照同样不可变（REQ 用例4 的嵌套形态）
	var v1Reloaded remapField
	db.First(&v1Reloaded, "field_ulid = ?", v1Fields[1].ULID)
	if v1Reloaded.RefNested != nested {
		t.Errorf("嵌套场景下旧版本不应被改写\n want %s\n got  %s", nested, v1Reloaded.RefNested)
	}
}

// ============================================================
// 用例 5：无法解析 → 事务失败，不静默落库
// ============================================================

func TestRemapUnresolvedFailsTransaction(t *testing.T) {
	db := openRemapDB(t)
	parentH, _ := newRemapHandlers(t, db, scalarRemap())

	createRaw := map[string]any{
		"code": "R5", "name": "v1",
		"fields": []map[string]any{
			{"field_code": "f1", "name": "F1"},
			{"field_code": "f2", "name": "F2"},
		},
	}
	ctx := context.WithValue(context.Background(), rawCreateMapsKey{}, []map[string]any{createRaw})
	created, _ := parentH._doCreate(ctx, []service.CrudRequest[*remapParent]{remapReq(createRaw)})
	v1 := (*created[0]).ULID

	var v1Fields []remapField
	db.Where("parent_ulid = ?", v1).Order("field_code").Find(&v1Fields)
	// 引用一个**不存在于本批次**的 ULID
	if err := db.Model(&remapField{}).Where("field_ulid = ?", v1Fields[1].ULID).
		Update("ref_scalar", "01NOTINBATCH0000000000000").Error; err != nil {
		t.Fatalf("建立悬空引用: %v", err)
	}

	var v1CountBefore int64
	db.Model(&remapParent{}).Count(&v1CountBefore)
	var fieldCountBefore int64
	db.Model(&remapField{}).Count(&fieldCountBefore)

	updateRaw := map[string]any{"id": v1, "name": "v2"}
	uctx := context.WithValue(context.Background(), rawUpdateMapsKey{}, []map[string]any{updateRaw})
	_, err := parentH._doUpdate(uctx, []service.CrudRequest[*remapParent]{remapReq(updateRaw)}, false)

	// ★ 用例 5：必须失败，且错误可识别
	if err == nil {
		t.Fatal("REQ 用例5: 引用无法解析时必须让事务失败，实际成功了")
	}
	if !errors.Is(err, errs.ErrRemapUnresolved) {
		t.Errorf("REQ 用例5: 错误应可经 errors.Is(err, ErrRemapUnresolved) 识别，实际: %v", err)
	}

	// 落库验证：事务回滚，未产生新版本、未新增子记录
	var v1CountAfter, fieldCountAfter int64
	db.Model(&remapParent{}).Count(&v1CountAfter)
	db.Model(&remapField{}).Count(&fieldCountAfter)
	if v1CountAfter != v1CountBefore {
		t.Errorf("REQ 用例5: 事务应整体回滚，父表记录数 %d → %d", v1CountBefore, v1CountAfter)
	}
	if fieldCountAfter != fieldCountBefore {
		t.Errorf("REQ 用例5: 事务应整体回滚，子表记录数 %d → %d", fieldCountBefore, fieldCountAfter)
	}
}

// TestRemapCtxCanceled 用例 5 的伴生：无法解析的子实体/字段名出现在错误信息里，
// 便于 heims 定位是哪条引用坏了。
func TestRemapUnresolvedErrorMentionsHandlerAndField(t *testing.T) {
	plan := &RemapPlan{
		oldToNew:    map[string]string{"OLD1": "NEW1"},
		codeToNew:   map[string]string{"c1": "NEW1"},
		handlerName: "form_write_field",
		bindings: []ReferenceBinding{
			{Field: "field_access", Mode: RemapModeObjectArray, ULIDKey: "field_ulid"},
		},
	}
	data := []map[string]any{
		{"field_access": []any{map[string]any{"field_ulid": "GHOST"}}},
	}
	err := applyRemap(plan, data)
	if err == nil {
		t.Fatal("引用不存在于本批次时应报错")
	}
	msg := err.Error()
	for _, want := range []string{"form_write_field", "field_access", "GHOST"} {
		if !contains(msg, want) {
			t.Errorf("错误信息应包含 %q 便于定位，实际: %s", want, msg)
		}
	}
}

// ============================================================
// 口径与边界
// ============================================================

// TestRemapULIDTakesPrecedenceOverCode ULID 命中时优先用 ULID，不看 code。
func TestRemapULIDTakesPrecedenceOverCode(t *testing.T) {
	plan := &RemapPlan{
		oldToNew:  map[string]string{"OLD_A": "NEW_A", "OLD_B": "NEW_B"},
		codeToNew: map[string]string{"ca": "NEW_A", "cb": "NEW_B"},
		bindings:  nil,
	}
	// ULID 指 A，code 指 b → 两者指向不同目标 → 报错（不静默取其一）
	data := []map[string]any{
		{"ref": []any{map[string]any{"field_ulid": "OLD_A", "field_code": "cb"}}},
	}
	err := applyRemap(&RemapPlan{
		oldToNew:  plan.oldToNew,
		codeToNew: plan.codeToNew,
		bindings: []ReferenceBinding{
			{Field: "ref", Mode: RemapModeObjectArray, ULIDKey: "field_ulid", CodeKey: "field_code"},
		},
	}, data)
	if err == nil {
		t.Fatal("ULID 与 code 指向不同目标时应报错")
	}
	if !errors.Is(err, errs.ErrRemapInconsistent) {
		t.Errorf("应可经 errors.Is(err, ErrRemapInconsistent) 识别，实际: %v", err)
	}

	// 一致时不报错，且用 ULID 的结果
	data2 := []map[string]any{
		{"ref": []any{map[string]any{"field_ulid": "OLD_A", "field_code": "ca"}}},
	}
	if err := applyRemap(&RemapPlan{
		oldToNew:  map[string]string{"OLD_A": "NEW_A"},
		codeToNew: map[string]string{"ca": "NEW_A"},
		bindings: []ReferenceBinding{
			{Field: "ref", Mode: RemapModeObjectArray, ULIDKey: "field_ulid", CodeKey: "field_code"},
		},
	}, data2); err != nil {
		t.Fatalf("ULID 与 code 一致时不应报错: %v", err)
	}
	got := data2[0]["ref"].([]any)[0].(map[string]any)["field_ulid"]
	if got != "NEW_A" {
		t.Errorf("应重写为 NEW_A，实际 %v", got)
	}
}

// TestRemapIdempotent 已是本批次新 ULID 时幂等跳过（回填场景可能重复应用）。
func TestRemapIdempotent(t *testing.T) {
	plan := &RemapPlan{
		oldToNew: map[string]string{"OLD1": "NEW1"},
		bindings: []ReferenceBinding{{Field: "ref", Mode: RemapModeScalar}},
	}
	data := []map[string]any{{"ref": "NEW1"}}
	if err := applyRemap(plan, data); err != nil {
		t.Fatalf("新 ULID 应幂等跳过而非报错: %v", err)
	}
	if data[0]["ref"] != "NEW1" {
		t.Errorf("值不应被改动，实际 %v", data[0]["ref"])
	}
}

// TestRemapMissingFieldIsNoop 记录上没有该引用字段时跳过（正常形态，不报错）。
func TestRemapMissingFieldIsNoop(t *testing.T) {
	plan := &RemapPlan{
		oldToNew: map[string]string{"OLD1": "NEW1"},
		bindings: []ReferenceBinding{{Field: "absent_field", Mode: RemapModeScalar}},
	}
	data := []map[string]any{{"name": "x"}}
	if err := applyRemap(plan, data); err != nil {
		t.Errorf("缺少引用字段应跳过，实际报错: %v", err)
	}
}

// TestRemapEmptyScalarIsNoop 空值不参与解析（无引用）。
func TestRemapEmptyScalarIsNoop(t *testing.T) {
	plan := &RemapPlan{
		oldToNew: map[string]string{"OLD1": "NEW1"},
		bindings: []ReferenceBinding{{Field: "ref", Mode: RemapModeScalar}},
	}
	for _, v := range []any{"", nil} {
		data := []map[string]any{{"ref": v}}
		if err := applyRemap(plan, data); err != nil {
			t.Errorf("空引用值 %v 应跳过，实际报错: %v", v, err)
		}
	}
}

// TestRemapInvalidMode 未知形态必须报配置错误，不静默忽略。
func TestRemapInvalidMode(t *testing.T) {
	plan := &RemapPlan{
		oldToNew: map[string]string{"OLD1": "NEW1"},
		bindings: []ReferenceBinding{{Field: "ref", Mode: "nonsense"}},
	}
	data := []map[string]any{{"ref": "OLD1"}}
	err := applyRemap(plan, data)
	if err == nil {
		t.Fatal("未知 mode 应报配置错误")
	}
	if !errors.Is(err, errs.ErrRemapInvalidConfig) {
		t.Errorf("应可经 errors.Is(err, ErrRemapInvalidConfig) 识别，实际: %v", err)
	}
}

// TestRemapObjectArrayRequiresKey object_array 必须给出 ULIDKey 或 CodeKey。
func TestRemapObjectArrayRequiresKey(t *testing.T) {
	plan := &RemapPlan{
		oldToNew: map[string]string{"OLD1": "NEW1"},
		bindings: []ReferenceBinding{{Field: "ref", Mode: RemapModeObjectArray}},
	}
	data := []map[string]any{{"ref": []any{map[string]any{"field_ulid": "OLD1"}}}}
	err := applyRemap(plan, data)
	if err == nil {
		t.Fatal("object_array 缺 ULIDKey/CodeKey 应报配置错误")
	}
	if !errors.Is(err, errs.ErrRemapInvalidConfig) {
		t.Errorf("应可经 errors.Is(err, ErrRemapInvalidConfig) 识别，实际: %v", err)
	}
}

// TestRemapPrepareBuildsOldToNewMapping prepareRemap 的映射构建语义：
// 旧 PK 快照与新 ULID 一一对应；无 code 字段时不建 code 映射。
func TestRemapPrepareBuildsOldToNewMapping(t *testing.T) {
	childData := []map[string]any{
		{"ulid": "NEW1", "field_code": "c1"},
		{"ulid": "NEW2", "field_code": "c2"},
	}
	plan := prepareRemap("h", []ReferenceRemap{{
		SourceCodeField: "field_code",
		Bindings:        []ReferenceBinding{{Field: "ref"}},
	}}, childData, []string{"OLD1", "OLD2"})

	if plan == nil {
		t.Fatal("plan 不应为 nil")
	}
	if plan.oldToNew["OLD1"] != "NEW1" || plan.oldToNew["OLD2"] != "NEW2" {
		t.Errorf("旧→新映射错误: %+v", plan.oldToNew)
	}
	if plan.codeToNew["c1"] != "NEW1" || plan.codeToNew["c2"] != "NEW2" {
		t.Errorf("code→新映射错误: %+v", plan.codeToNew)
	}

	// 无 SourceCodeField 时不建 code 映射
	plan2 := prepareRemap("h", []ReferenceRemap{{
		Bindings: []ReferenceBinding{{Field: "ref"}},
	}}, childData, []string{"OLD1", "OLD2"})
	if len(plan2.codeToNew) != 0 {
		t.Errorf("未配置 SourceCodeField 时不应有 code 映射，实际 %+v", plan2.codeToNew)
	}

	// 无绑定 / 无数据 → nil（零成本）
	if p := prepareRemap("h", nil, childData, nil); p != nil {
		t.Error("无声明时应返回 nil")
	}
	if p := prepareRemap("h", []ReferenceRemap{{Bindings: []ReferenceBinding{{Field: "ref"}}}}, nil, nil); p != nil {
		t.Error("无子数据时应返回 nil")
	}
}

// TestRemapSnapshotPKs 旧 PK 快照兼容 gorm 列名与 JSON 名（BUG-060 约定）。
func TestRemapSnapshotPKs(t *testing.T) {
	data := []map[string]any{
		{"field_ulid": "A", "ulid": "A"},
		{"ulid": "B"}, // 只有 JSON 名
		{"field_ulid": "C"},
		{"name": "no-pk"},
	}
	got := snapshotPKs(data, "field_ulid")
	want := []string{"A", "B", "C", ""}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("snapshotPKs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
