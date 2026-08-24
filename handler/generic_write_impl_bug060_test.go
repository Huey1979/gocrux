package handler

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/Huey1979/gocrux/repository"
	"github.com/Huey1979/gocrux/service"
)

// ============================================================
// BUG-060 回归测试：版本化父表 Update 未携带子表时，回填子表必须清除
// 旧 PK——childData 的 key 是 JSON 字段名（marshalToMap），而 PKField()
// 返回 gorm 列名（child_ulid）≠ JSON 名（ulid，gentity 统一约定）。
// 修复前：清除块 delete(PKField()) 是空操作 → 旧 PK（JSON key ulid）残留
// → MergeTo 灌入旧 ULID → service _beforeCreate 判定非空复用旧 PK →
// MySQL 1062 Duplicate entry（heims D-40 复测 flow_list_field）。
//
// 测试实体（父复用 BUG-059 的 *bug059VersionedParent，版本化）：
//   bug060Child — PK gorm 列名=child_ulid、JSON 名=ulid、FK=parent_id
//
// 修复后：版本化回填走 CREATE 复制重建，v2 子行 ChildULID 全新、
// v1 快照保持不变、无主键冲突。
// ============================================================

// bug060Child 子测试实体：PKField() 返回 gorm 列名（child_ulid），
// JSON 主键名为 ulid（参照 heims sys_flow_list_field：PKField()="field_ulid"、json:"ulid"）。
type bug060Child struct {
	ChildULID string `gorm:"column:child_ulid;primaryKey;size:26" json:"ulid"`
	ParentID  string `gorm:"size:100;index" json:"parent_id"`
	Name      string `gorm:"size:100" json:"name"`
	IsDeleted int8   `gorm:"column:is_deleted;default:0" json:"-"`
}

func (t bug060Child) SetDefaults()               {}
func (t bug060Child) SetCreatedAt(tm time.Time)  {}
func (t bug060Child) SetCreatedBy(userID string) {}
func (t bug060Child) SetUpdatedAt(tm time.Time)  {}
func (t bug060Child) SetUpdatedBy(userID string) {}
func (t bug060Child) SupportsDraft() bool        { return false }
func (t bug060Child) SetDelete() bool            { return true }
func (t bug060Child) GetULID() string            { return t.ChildULID }
func (t bug060Child) PKField() string            { return "child_ulid" } // gorm 列名 ≠ JSON 名 ulid
func (t bug060Child) SelfFKField() string        { return "" }

// openBug060DB 打开独立 sqlite 内存库（每次 Open 全新库，避免与 BUG-059 测试共享数据）。
func openBug060DB(t *testing.T) *gorm.DB {
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
	if err := db.AutoMigrate(&bug059VersionedParent{}, &bug060Child{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return db
}

// newBug060ChildHandler 子 Handler（非版本化，值类型）。
func newBug060ChildHandler(db *gorm.DB) *GenericHandler[bug060Child] {
	repo := repository.NewCRUDWithDB[bug060Child](db)
	svc := service.NewGenericService[bug060Child](repo, service.Config[bug060Child]{})
	return &GenericHandler[bug060Child]{
		svc:     svc,
		svcName: "bug060_child",
		config:  HandlerConfig[bug060Child]{PathPrefix: "/bug060/child"},
	}
}

// newBug060ParentHandler 版本化父 Handler（级联 bug060_child）。
func newBug060ParentHandler(db *gorm.DB) *GenericHandler[*bug059VersionedParent] {
	repo := repository.NewCRUDWithDB[*bug059VersionedParent](db)
	svc := service.NewGenericService[*bug059VersionedParent](repo, service.Config[*bug059VersionedParent]{
		VersionMode: true,
		VersionFields: &service.VersionFieldMapping{
			ULIDField:    "ParentULID",
			CodeField:    "Code",
			VersionField: "VersionCode",
			CurrentField: "IsCurrent",
			StatusField:  "VersionStatus",
			ParentField:  "ParentVersion",
			RemarkField:  "VersionRemark",
		},
	})
	cascades := []CascadeRelation{
		{HandlerName: "bug060_child", ChildrenField: "children", FKField: "parent_id", OnCreate: true, OnUpdate: true},
	}
	handlerReg := NewHandlerRegistry()
	handlerReg.Register("bug060_child", newBug060ChildHandler(db))
	tc := NewTxCoordinator(db, nil)
	return &GenericHandler[*bug059VersionedParent]{
		svc:        svc,
		svcName:    "bug060_versioned_parent",
		config:     HandlerConfig[*bug059VersionedParent]{PathPrefix: "/bug060/versioned_parent", Cascades: cascades},
		handlerReg: handlerReg,
		txCoord:    tc,
	}
}

// bug060Req 构造 CrudRequest[*bug059VersionedParent]。
func bug060Req(m map[string]any) service.CrudRequest[*bug059VersionedParent] {
	return &MapRequest[*bug059VersionedParent]{data: m}
}

// TestDoUpdate_CascadeVersionedBackfillRemovesJSONPK BUG-060 核心回归：
// 版本化父 create（2 子行）→ update 不带子表 → 回填复制重建，
// v2 子行 ChildULID 必须全新（修复前旧 PK 残留 → 复用 → 主键冲突）。
func TestDoUpdate_CascadeVersionedBackfillRemovesJSONPK(t *testing.T) {
	db := openBug060DB(t)
	h := newBug060ParentHandler(db)

	// 1. create v1：父 + 2 个子记录（子数据不带 PK，框架生成）
	createRaw := map[string]any{
		"code": "flow_060",
		"name": "v1",
		"children": []map[string]any{
			{"name": "c1"},
			{"name": "c2"},
		},
	}
	ctx := context.WithValue(context.Background(), rawCreateMapsKey{}, []map[string]any{createRaw})
	created, err := h._doCreate(ctx, []service.CrudRequest[*bug059VersionedParent]{bug060Req(createRaw)})
	if err != nil {
		t.Fatalf("_doCreate 失败: %v", err)
	}
	v1ULID := (*created[0]).ParentULID
	if v1ULID == "" {
		t.Fatal("v1 ParentULID 不应为空")
	}

	var v1Children []bug060Child
	db.Where("parent_id = ? AND is_deleted = 0", v1ULID).Find(&v1Children)
	if len(v1Children) != 2 {
		t.Fatalf("create 后 v1 子表应有 2 条, 实际 %d", len(v1Children))
	}
	v1ULIDs := make(map[string]bool, len(v1Children))
	for _, c := range v1Children {
		if c.ChildULID == "" {
			t.Fatal("v1 子行 ChildULID 不应为空")
		}
		v1ULIDs[c.ChildULID] = true
	}

	// 2. update v1：不带 children → 版本化回填（BUG-060 场景）
	updateRaw := map[string]any{"id": v1ULID, "name": "v2"}
	uctx := context.WithValue(context.Background(), rawUpdateMapsKey{}, []map[string]any{updateRaw})
	updated, err := h._doUpdate(uctx, []service.CrudRequest[*bug059VersionedParent]{bug060Req(updateRaw)}, false)
	if err != nil {
		t.Fatalf("_doUpdate 失败（修复前：旧 PK 残留 → 主键冲突）: %v", err)
	}
	v2ULID := (*updated[0]).ParentULID
	if v2ULID == "" || v2ULID == v1ULID {
		t.Fatalf("版本化 update 应生成新 ULID: v1=%s v2=%s", v1ULID, v2ULID)
	}

	// 3. v1 子表快照保持完整（ChildULID 不变，未被“搬走”）
	var v1After []bug060Child
	db.Where("parent_id = ? AND is_deleted = 0", v1ULID).Find(&v1After)
	if len(v1After) != 2 {
		t.Fatalf("update 后 v1 子表快照应保持 2 条, 实际 %d", len(v1After))
	}
	for _, c := range v1After {
		if !v1ULIDs[c.ChildULID] {
			t.Fatalf("v1 子行 ChildULID 被改动: %s", c.ChildULID)
		}
	}

	// 4. v2 子表为复制快照，ChildULID 必须全新（BUG-060 核心断言）
	var v2Children []bug060Child
	db.Where("parent_id = ? AND is_deleted = 0", v2ULID).Find(&v2Children)
	if len(v2Children) != 2 {
		t.Fatalf("update 后 v2 子表应有复制快照 2 条, 实际 %d", len(v2Children))
	}
	for _, c := range v2Children {
		if v1ULIDs[c.ChildULID] {
			t.Fatalf("v2 子行复用了 v1 旧 PK（清除失败）: %s", c.ChildULID)
		}
	}
	names := map[string]bool{}
	for _, c := range v2Children {
		names[c.Name] = true
	}
	if !names["c1"] || !names["c2"] {
		t.Fatalf("v2 子表快照内容应与 v1 一致: %+v", v2Children)
	}

	// 5. 父表版本状态：v1 退位、v2 当前
	var v1Row, v2Row bug059VersionedParent
	db.Where("parent_ulid = ?", v1ULID).First(&v1Row)
	db.Where("parent_ulid = ?", v2ULID).First(&v2Row)
	if v1Row.IsCurrent != 0 {
		t.Fatalf("v1 应退位 is_current=0, 实际 %d", v1Row.IsCurrent)
	}
	if v2Row.IsCurrent != 1 {
		t.Fatalf("v2 应为当前版本 is_current=1, 实际 %d", v2Row.IsCurrent)
	}
}

// TestDoUpdate_CascadeVersionedWithChildrenClearsOldULID 版本化父 update 携带子表
// 且子数据回传了旧 ulid（前端回显场景）→ 清除块须删除 JSON 名 ulid，v2 子行全新，
// 不得复用旧 PK（修复前 delete(PKField()) 无效 → 旧 ulid 残留 → 主键冲突）。
func TestDoUpdate_CascadeVersionedWithChildrenClearsOldULID(t *testing.T) {
	db := openBug060DB(t)
	h := newBug060ParentHandler(db)

	createRaw := map[string]any{
		"code": "flow_060b",
		"name": "v1",
		"children": []map[string]any{
			{"name": "c1"},
			{"name": "c2"},
		},
	}
	ctx := context.WithValue(context.Background(), rawCreateMapsKey{}, []map[string]any{createRaw})
	created, err := h._doCreate(ctx, []service.CrudRequest[*bug059VersionedParent]{bug060Req(createRaw)})
	if err != nil {
		t.Fatalf("_doCreate 失败: %v", err)
	}
	v1ULID := (*created[0]).ParentULID

	var v1Children []bug060Child
	db.Where("parent_id = ? AND is_deleted = 0", v1ULID).Find(&v1Children)
	if len(v1Children) != 2 {
		t.Fatalf("create 后 v1 子表应有 2 条, 实际 %d", len(v1Children))
	}

	// update 携带 children（回传旧 ulid，模拟前端回显）→ 版本化重建
	updateRaw := map[string]any{
		"id":   v1ULID,
		"name": "v2",
		"children": []map[string]any{
			{"ulid": v1Children[0].ChildULID, "name": "new_c1"},
		},
	}
	uctx := context.WithValue(context.Background(), rawUpdateMapsKey{}, []map[string]any{updateRaw})
	updated, err := h._doUpdate(uctx, []service.CrudRequest[*bug059VersionedParent]{bug060Req(updateRaw)}, false)
	if err != nil {
		t.Fatalf("_doUpdate 失败（旧 ulid 未被清除 → 主键冲突）: %v", err)
	}
	v2ULID := (*updated[0]).ParentULID
	if v2ULID == v1ULID {
		t.Fatal("版本化 update 应生成新 ULID")
	}

	// v1 快照完整
	var v1After []bug060Child
	db.Where("parent_id = ? AND is_deleted = 0", v1ULID).Find(&v1After)
	if len(v1After) != 2 {
		t.Fatalf("v1 子表快照应保持 2 条, 实际 %d", len(v1After))
	}

	// v2 子行为携带的新数据（1 条），且 ChildULID 全新（不复用旧 ulid）
	var v2Children []bug060Child
	db.Where("parent_id = ? AND is_deleted = 0", v2ULID).Find(&v2Children)
	if len(v2Children) != 1 || v2Children[0].Name != "new_c1" {
		t.Fatalf("v2 子表应为携带的新数据: %+v", v2Children)
	}
	if v2Children[0].ChildULID == v1Children[0].ChildULID {
		t.Fatalf("v2 子行复用了回传的旧 ulid（清除失败）: %s", v2Children[0].ChildULID)
	}
}

// TestDoUpdate_CascadeNonVersionedBackfillKeepsChildULID 回归（BUG-018/020 语义不回归）：
// 非版本化父 update 不带子表 → 回填子数据原地更新（子行 ChildULID 不变，不复制重建）。
// 同时覆盖 BUG-060 的 id 注入修复：id 注入能从 JSON 名 ulid 读到 PK。
func TestDoUpdate_CascadeNonVersionedBackfillKeepsChildULID(t *testing.T) {
	db := openBug060DB(t)

	// 非版本化父 handler（复用 bug059VersionedParent 但不启用 VersionMode）
	repo := repository.NewCRUDWithDB[*bug059VersionedParent](db)
	svc := service.NewGenericService[*bug059VersionedParent](repo, service.Config[*bug059VersionedParent]{})
	cascades := []CascadeRelation{
		{HandlerName: "bug060_child", ChildrenField: "children", FKField: "parent_id", OnCreate: true, OnUpdate: true},
	}
	handlerReg := NewHandlerRegistry()
	handlerReg.Register("bug060_child", newBug060ChildHandler(db))
	tc := NewTxCoordinator(db, nil)
	h := &GenericHandler[*bug059VersionedParent]{
		svc:        svc,
		svcName:    "bug060_nonversioned_parent",
		config:     HandlerConfig[*bug059VersionedParent]{PathPrefix: "/bug060/nv_parent", Cascades: cascades},
		handlerReg: handlerReg,
		txCoord:    tc,
	}

	// create：非版本化父手动指定 PK
	createRaw := map[string]any{
		"parent_ulid": "nv_p060",
		"name":        "nv1",
		"children": []map[string]any{
			{"name": "c1"},
			{"name": "c2"},
		},
	}
	ctx := context.WithValue(context.Background(), rawCreateMapsKey{}, []map[string]any{createRaw})
	created, err := h._doCreate(ctx, []service.CrudRequest[*bug059VersionedParent]{bug060Req(createRaw)})
	if err != nil {
		t.Fatalf("_doCreate 失败: %v", err)
	}
	if (*created[0]).ParentULID != "nv_p060" {
		t.Fatalf("期望 PK=nv_p060, 实际 %s", (*created[0]).ParentULID)
	}

	var createdChildren []bug060Child
	db.Where("parent_id = ? AND is_deleted = 0", "nv_p060").Find(&createdChildren)
	if len(createdChildren) != 2 {
		t.Fatalf("create 后子表应有 2 条, 实际 %d", len(createdChildren))
	}
	createdULIDs := make(map[string]bool, len(createdChildren))
	for _, c := range createdChildren {
		createdULIDs[c.ChildULID] = true
	}

	// update：不带 children → 回填子数据原地更新（不复制重建）
	updateRaw := map[string]any{"id": "nv_p060", "name": "nv2"}
	uctx := context.WithValue(context.Background(), rawUpdateMapsKey{}, []map[string]any{updateRaw})
	updated, err := h._doUpdate(uctx, []service.CrudRequest[*bug059VersionedParent]{bug060Req(updateRaw)}, false)
	if err != nil {
		t.Fatalf("_doUpdate 失败: %v", err)
	}
	if (*updated[0]).ParentULID != "nv_p060" {
		t.Fatalf("非版本化 update 不应生成新 PK: %s", (*updated[0]).ParentULID)
	}

	// 子行保持原 ChildULID（未被删除重建、未复制出新行）
	var children []bug060Child
	db.Where("parent_id = ? AND is_deleted = 0", "nv_p060").Find(&children)
	if len(children) != 2 {
		t.Fatalf("非版本化回填后子行应保持 2 条, 实际 %d", len(children))
	}
	for _, c := range children {
		if !createdULIDs[c.ChildULID] {
			t.Fatalf("非版本化回填不应改动子行 ChildULID: %s", c.ChildULID)
		}
	}
}
