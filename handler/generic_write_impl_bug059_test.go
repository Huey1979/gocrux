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
// BUG-059 回归测试：版本化父表 Update 未携带的子表应复制重建快照，
// 而不是“原地改 FK”把旧版本子行搬到新版本。
//
// 测试实体（结构参照 heims SysSite 版本化模型，M 均为指针类型——
// 版本化管线 _beforeUpdateVersioned 要求 M 为指针）：
//   *bug059VersionedParent — 版本化父（PK=parent_ulid，含 code/version/current/status）
//   *bug059Child           — 非版本化子（PK=id 自增，FK=parent_id）
//
// 修复前：flow/update 只带部分子表 → 旧子行被 UPDATE 改 FK 到新版本 → 旧版本子表快照丢失。
// 修复后：版本化父回填子表时清除子行 PK 走 CREATE，旧版本子行保持不变、新版本复制重建。
// ============================================================

// bug059VersionedParent 版本化父测试实体。
type bug059VersionedParent struct {
	ParentULID    string    `gorm:"column:parent_ulid;primaryKey;size:26" json:"parent_ulid"`
	Code          string    `gorm:"size:64" json:"code"`
	Name          string    `gorm:"size:100" json:"name"`
	IsCurrent     int8      `gorm:"column:is_current;default:0" json:"is_current"`
	ParentVersion string    `gorm:"column:parent_version;size:26" json:"parent_version"`
	VersionCode   string    `gorm:"size:20" json:"version_code"`
	VersionStatus string    `gorm:"size:20" json:"version_status"`
	VersionRemark string    `gorm:"size:500" json:"version_remark"`
	IsDeleted     int8      `gorm:"column:is_deleted;default:0" json:"-"`
	CreatedAt     time.Time `gorm:"column:created_at" json:"created_at"`
}

func (t *bug059VersionedParent) SetDefaults()               {}
func (t *bug059VersionedParent) SetCreatedAt(tm time.Time)  { t.CreatedAt = tm }
func (t *bug059VersionedParent) SetCreatedBy(userID string) {}
func (t *bug059VersionedParent) SetUpdatedAt(tm time.Time)  {}
func (t *bug059VersionedParent) SetUpdatedBy(userID string) {}
func (t *bug059VersionedParent) SupportsDraft() bool        { return false }
func (t *bug059VersionedParent) SetDelete() bool            { t.IsDeleted = 1; return true }
func (t *bug059VersionedParent) GetULID() string            { return t.ParentULID }
func (t *bug059VersionedParent) PKField() string            { return "parent_ulid" }
func (t *bug059VersionedParent) SelfFKField() string        { return "" }

// bug059Child 子测试实体。
type bug059Child struct {
	ID        uint   `gorm:"primaryKey;autoIncrement" json:"id"`
	ParentID  string `gorm:"size:100;index" json:"parent_id"`
	Name      string `gorm:"size:100" json:"name"`
	IsDeleted int8   `gorm:"column:is_deleted;default:0" json:"-"`
}

func (t bug059Child) SetDefaults()               {}
func (t bug059Child) SetCreatedAt(tm time.Time)  {}
func (t bug059Child) SetCreatedBy(userID string) {}
func (t bug059Child) SetUpdatedAt(tm time.Time)  {}
func (t bug059Child) SetUpdatedBy(userID string) {}
func (t bug059Child) SupportsDraft() bool        { return false }
func (t bug059Child) SetDelete() bool            { return true }
func (t bug059Child) GetULID() string            { return fmt.Sprintf("%d", t.ID) }
func (t bug059Child) PKField() string            { return "id" }
func (t bug059Child) SelfFKField() string        { return "" }

// openBug059DB 打开 sqlite 内存库并迁移测试表。
func openBug059DB(t *testing.T) *gorm.DB {
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
	if err := db.AutoMigrate(&bug059VersionedParent{}, &bug059Child{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return db
}

// newBug059ChildHandler 子 Handler（非版本化，绑定 sqlite DB，值类型与现有 testChild 一致）。
func newBug059ChildHandler(db *gorm.DB) *GenericHandler[bug059Child] {
	repo := repository.NewCRUDWithDB[bug059Child](db)
	svc := service.NewGenericService[bug059Child](repo, service.Config[bug059Child]{})
	return &GenericHandler[bug059Child]{
		svc:     svc,
		svcName: "bug059_child",
		config:  HandlerConfig[bug059Child]{PathPrefix: "/bug059/child"},
	}
}

// newBug059ParentHandler 版本化父 Handler（含级联子表 OnCreate/OnUpdate）。
func newBug059ParentHandler(db *gorm.DB) *GenericHandler[*bug059VersionedParent] {
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
		{HandlerName: "bug059_child", ChildrenField: "children", FKField: "parent_id", OnCreate: true, OnUpdate: true},
	}
	handlerReg := NewHandlerRegistry()
	handlerReg.Register("bug059_child", newBug059ChildHandler(db))
	tc := NewTxCoordinator(db, nil)
	return &GenericHandler[*bug059VersionedParent]{
		svc:       svc,
		svcName:   "bug059_versioned_parent",
		config:    HandlerConfig[*bug059VersionedParent]{PathPrefix: "/bug059/versioned_parent", Cascades: cascades},
		handlerReg: handlerReg,
		txCoord:    tc,
	}
}

// bug059Req 构造 CrudRequest[*bug059VersionedParent]。
func bug059Req(m map[string]any) service.CrudRequest[*bug059VersionedParent] {
	return &MapRequest[*bug059VersionedParent]{data: m}
}

// TestDoUpdate_CascadeVersionedBackfillCreatesSnapshot BUG-059 主回归：
// 版本化父 create（全子表）→ update（不带子表）→
//   - 新版本（v2）子表复制重建（新子行 PK，FK 指向 v2）
//   - 旧版本（v1）子表快照保持完整（FK 仍指向 v1，未被“搬走”）
func TestDoUpdate_CascadeVersionedBackfillCreatesSnapshot(t *testing.T) {
	db := openBug059DB(t)
	h := newBug059ParentHandler(db)

	// 1. create v1：父 + 2 个子记录
	createRaw := map[string]any{
		"code": "flow_059",
		"name": "v1",
		"children": []map[string]any{
			{"name": "c1"},
			{"name": "c2"},
		},
	}
	ctx := context.WithValue(context.Background(), rawCreateMapsKey{}, []map[string]any{createRaw})
	created, err := h._doCreate(ctx, []service.CrudRequest[*bug059VersionedParent]{bug059Req(createRaw)})
	if err != nil {
		t.Fatalf("_doCreate 失败: %v", err)
	}
	v1 := *created[0]
	v1ULID := v1.ParentULID
	if v1ULID == "" {
		t.Fatal("v1 ParentULID 不应为空")
	}

	var v1Children []bug059Child
	db.Where("parent_id = ? AND is_deleted = 0", v1ULID).Find(&v1Children)
	if len(v1Children) != 2 {
		t.Fatalf("create 后 v1 子表应有 2 条, 实际 %d", len(v1Children))
	}

	// 2. update v1：仅带 name，不带 children → 版本化创建 v2
	updateRaw := map[string]any{"id": v1ULID, "name": "v2"}
	uctx := context.WithValue(context.Background(), rawUpdateMapsKey{}, []map[string]any{updateRaw})
	updated, err := h._doUpdate(uctx, []service.CrudRequest[*bug059VersionedParent]{bug059Req(updateRaw)}, false)
	if err != nil {
		t.Fatalf("_doUpdate 失败: %v", err)
	}
	v2 := *updated[0]
	v2ULID := v2.ParentULID
	if v2ULID == "" {
		t.Fatal("v2 ParentULID 不应为空")
	}
	if v2ULID == v1ULID {
		t.Fatal("版本化 update 应生成新 ULID（v2 不应等于 v1）")
	}

	// 3. 旧版本子表快照保持完整（BUG-059 核心断言：未被“原地改 FK”搬走）
	var v1ChildrenAfter []bug059Child
	db.Where("parent_id = ? AND is_deleted = 0", v1ULID).Find(&v1ChildrenAfter)
	if len(v1ChildrenAfter) != 2 {
		t.Fatalf("update 后 v1 子表快照应保持 2 条, 实际 %d（子行被搬走或丢失）", len(v1ChildrenAfter))
	}

	// 4. 新版本子表已复制重建（新子行，FK 指向 v2）
	var v2Children []bug059Child
	db.Where("parent_id = ? AND is_deleted = 0", v2ULID).Find(&v2Children)
	if len(v2Children) != 2 {
		t.Fatalf("update 后 v2 子表应有复制快照 2 条, 实际 %d", len(v2Children))
	}
	if v1ChildrenAfter[0].ID == v2Children[0].ID || v1ChildrenAfter[1].ID == v2Children[1].ID {
		t.Fatalf("v2 子表应为新复制行（新 PK），不得复用旧子行: v1=%+v v2=%+v", v1ChildrenAfter, v2Children)
	}
	if v2Children[0].Name != "c1" || v2Children[1].Name != "c2" {
		t.Fatalf("v2 子表快照内容应与 v1 一致: %+v", v2Children)
	}

	// 5. 父表版本状态：v1 退位（is_current=0）、v2 当前（is_current=1）
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

// TestDoUpdate_CascadeVersionedWithChildrenOnUpdate 版本化父 update 携带子表：
// 仍走原有“请求携带子表”的清除 PK 重建语义，旧版本子表快照保持完整。
func TestDoUpdate_CascadeVersionedWithChildrenOnUpdate(t *testing.T) {
	db := openBug059DB(t)
	h := newBug059ParentHandler(db)

	createRaw := map[string]any{
		"code": "flow_059b",
		"name": "v1",
		"children": []map[string]any{
			{"name": "c1"},
			{"name": "c2"},
		},
	}
	ctx := context.WithValue(context.Background(), rawCreateMapsKey{}, []map[string]any{createRaw})
	created, err := h._doCreate(ctx, []service.CrudRequest[*bug059VersionedParent]{bug059Req(createRaw)})
	if err != nil {
		t.Fatalf("_doCreate 失败: %v", err)
	}
	v1ULID := (*created[0]).ParentULID

	// update 携带 children（新子数据）→ 版本化重建（携带子表本身已走 CREATE 替换语义）
	updateRaw := map[string]any{
		"id":   v1ULID,
		"name": "v2",
		"children": []map[string]any{
			{"name": "new_c1"},
		},
	}
	uctx := context.WithValue(context.Background(), rawUpdateMapsKey{}, []map[string]any{updateRaw})
	updated, err := h._doUpdate(uctx, []service.CrudRequest[*bug059VersionedParent]{bug059Req(updateRaw)}, false)
	if err != nil {
		t.Fatalf("_doUpdate 失败: %v", err)
	}
	v2ULID := (*updated[0]).ParentULID
	if v2ULID == v1ULID {
		t.Fatal("版本化 update 应生成新 ULID")
	}

	// 旧版本子表快照完整（携带子表时旧子行不得被删除/搬走）
	var v1Children []bug059Child
	db.Where("parent_id = ? AND is_deleted = 0", v1ULID).Find(&v1Children)
	if len(v1Children) != 2 {
		t.Fatalf("v1 子表快照应保持 2 条, 实际 %d", len(v1Children))
	}

	// 新版本子表为请求中携带的新数据（1 条）
	var v2Children []bug059Child
	db.Where("parent_id = ? AND is_deleted = 0", v2ULID).Find(&v2Children)
	if len(v2Children) != 1 || v2Children[0].Name != "new_c1" {
		t.Fatalf("v2 子表应为携带的新数据: %+v", v2Children)
	}
}

// TestDoUpdate_CascadeNonVersionedBackfillKeepsIDs 回归（BUG-018/020 语义不回归）：
// 非版本化父 update 不带子表 → 回填子数据仍原地 UPDATE（子行 PK 不变、不删除重建）。
func TestDoUpdate_CascadeNonVersionedBackfillKeepsIDs(t *testing.T) {
	db := openBug059DB(t)

	// 非版本化父 handler（复用 bug059VersionedParent 但不启用 VersionMode）
	repo := repository.NewCRUDWithDB[*bug059VersionedParent](db)
	svc := service.NewGenericService[*bug059VersionedParent](repo, service.Config[*bug059VersionedParent]{})
	cascades := []CascadeRelation{
		{HandlerName: "bug059_child", ChildrenField: "children", FKField: "parent_id", OnCreate: true, OnUpdate: true},
	}
	handlerReg := NewHandlerRegistry()
	handlerReg.Register("bug059_child", newBug059ChildHandler(db))
	tc := NewTxCoordinator(db, nil)
	h := &GenericHandler[*bug059VersionedParent]{
		svc:        svc,
		svcName:    "bug059_nonversioned_parent",
		config:     HandlerConfig[*bug059VersionedParent]{PathPrefix: "/bug059/nv_parent", Cascades: cascades},
		handlerReg: handlerReg,
		txCoord:    tc,
	}

	// create：非版本化父手动指定 PK
	createRaw := map[string]any{
		"parent_ulid": "nv_p1",
		"name":        "nv1",
		"children": []map[string]any{
			{"name": "c1"},
			{"name": "c2"},
		},
	}
	ctx := context.WithValue(context.Background(), rawCreateMapsKey{}, []map[string]any{createRaw})
	created, err := h._doCreate(ctx, []service.CrudRequest[*bug059VersionedParent]{bug059Req(createRaw)})
	if err != nil {
		t.Fatalf("_doCreate 失败: %v", err)
	}
	pk := (*created[0]).ParentULID
	if pk != "nv_p1" {
		t.Fatalf("期望 PK=nv_p1, 实际 %s", pk)
	}

	// update：不带 children → 回填子数据原地更新（FK 语义保留，不复制重建）
	updateRaw := map[string]any{"id": "nv_p1", "name": "nv2"}
	uctx := context.WithValue(context.Background(), rawUpdateMapsKey{}, []map[string]any{updateRaw})
	updated, err := h._doUpdate(uctx, []service.CrudRequest[*bug059VersionedParent]{bug059Req(updateRaw)}, false)
	if err != nil {
		t.Fatalf("_doUpdate 失败: %v", err)
	}
	if (*updated[0]).ParentULID != "nv_p1" {
		t.Fatalf("非版本化 update 不应生成新 PK: %s", (*updated[0]).ParentULID)
	}

	// 子行保持原 ID（未被删除重建，未复制出新行）
	var children []bug059Child
	db.Where("parent_id = ? AND is_deleted = 0", "nv_p1").Find(&children)
	if len(children) != 2 {
		t.Fatalf("非版本化回填后子行应保持 2 条, 实际 %d", len(children))
	}
}
