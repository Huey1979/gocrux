package handler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	errs "github.com/Huey1979/gocrux/errors"
	"github.com/Huey1979/gocrux/repository"
	"github.com/Huey1979/gocrux/service"
)

// ============================================================
// 回归测试：更新父记录**携带子表**时，子表的**局部字段**更新不得丢失未提交字段
// （CascadeRelation.MergeChildrenOnUpdate）
//
// 场景（下游 BUG 报告 cascade_partial_child）：
//
//	父 A、子 B 非版本化（OnUpdate 挂载）。旧子记录 A=1,B=2,C=3,D=4,E=5,F=6；
//	更新父时只提交 B 的 E/F/G → 期望新快照 = 旧 A/B/C/D + 新 E/F/G；
//	修复前的新快照只有 E/F/G，A~D 退化为零值/默认值。
//
// 同一开关**覆盖两条父路径**（应用方要求）：
//
//	版本化父  ：合并后清 PK 重建新子快照
//	非版本化父：合并后仍走该路径既有的「删旧子行 → 全量替换」
//
// 测试实体：
//
//	bug059VersionedParent（复用 BUG-059 的父实体，含 code/version/current/status）
//	mergeChild          子实体（PK=child_ulid，JSON 名 ulid；FK=parent_id）
//
// 覆盖（BUG 报告 §六 的四种边界 + 身份匹配 + 两条路径）：
//
//	① 未提交字段保留（旧记录为基底）② 显式置空 ③ 子记录新增
//	④ 子记录删除（数组即子记录集合）⑤ 按业务 code 兜底匹配 ⑥ code 歧义报错
//	⑦ 关闭开关时两条路径都保持既有语义（向后兼容）⑧ 非版本化父路径同样合并
//	⑨ 旧字段名（兼容别名）仍然生效
// ============================================================

// mergeChild 非版本化子实体（模拟 heims 的表单子表：字段多、支持局部提交）。
type mergeChild struct {
	ULID      string `gorm:"column:child_ulid;primaryKey;size:26" json:"ulid"`
	ParentID  string `gorm:"column:parent_id;size:26;index" json:"parent_id"`
	ColCode   string `gorm:"column:col_code;size:64" json:"col_code"`
	Title     string `gorm:"column:title;size:100" json:"title"`
	Unit      string `gorm:"column:unit;size:32" json:"unit"`
	Width     int    `gorm:"column:width;default:0" json:"width"`
	Enabled   int8   `gorm:"column:enabled;default:0" json:"enabled"`
	Expr      string `gorm:"column:expr;size:200" json:"expr"`
	Remark    string `gorm:"column:remark;size:300" json:"remark"`
	IsDeleted int8   `gorm:"column:is_deleted;default:0" json:"-"`
}

func (c *mergeChild) TableName() string        { return "merge_children" }
func (c *mergeChild) SetDefaults()             {}
func (c *mergeChild) SetCreatedAt(_ time.Time) {}
func (c *mergeChild) SetCreatedBy(string)      {}
func (c *mergeChild) SetUpdatedAt(_ time.Time) {}
func (c *mergeChild) SetUpdatedBy(string)      {}
func (c *mergeChild) SupportsDraft() bool      { return false }
func (c *mergeChild) SetDelete() bool          { c.IsDeleted = 1; return true }
func (c *mergeChild) GetULID() string          { return c.ULID }
func (c *mergeChild) PKField() string          { return "child_ulid" }
func (c *mergeChild) SelfFKField() string      { return "" }

// -------- 装置 --------

// openMergeDB 在共享内存库上迁移并列清空本组用例的表。
// （与 openAsmDB 同写法：SetMaxOpenConns(1) 避免 sqlite 内存库多连接各自为库。）
func openMergeDB(t *testing.T) *gorm.DB {
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
	if err := db.AutoMigrate(&bug059VersionedParent{}, &mergeChild{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	for _, m := range []any{&bug059VersionedParent{}, &mergeChild{}} {
		if err := db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(m).Error; err != nil {
			t.Fatalf("清表失败: %v", err)
		}
	}
	return db
}

// newMergeChildHandler 子 Handler（非版本化）。
func newMergeChildHandler(db *gorm.DB) *GenericHandler[*mergeChild] {
	svc := service.NewGenericService[*mergeChild](repository.NewCRUDWithDB[*mergeChild](db),
		service.Config[*mergeChild]{})
	return &GenericHandler[*mergeChild]{
		svc:     svc,
		svcName: "merge_child",
		config:  HandlerConfig[*mergeChild]{PathPrefix: "/merge/child"},
	}
}

// newMergeParentHandler 版本化父 Handler；merge 控制是否开启子记录字段合并。
func newMergeParentHandler(db *gorm.DB, merge bool) *GenericHandler[*bug059VersionedParent] {
	svc := service.NewGenericService[*bug059VersionedParent](
		repository.NewCRUDWithDB[*bug059VersionedParent](db),
		service.Config[*bug059VersionedParent]{
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
	cascades := []CascadeRelation{{
		HandlerName: "merge_child", ChildrenField: "children", FKField: "parent_id",
		OnCreate: true, OnUpdate: true,
		MergeChildrenOnUpdate: merge,
		// 业务 code 作为身份兜底键（前端不带旧 ULID 时按它配对旧子记录）
		PublishCodeField: "col_code",
	}}
	reg := NewHandlerRegistry()
	reg.Register("merge_child", newMergeChildHandler(db))
	return &GenericHandler[*bug059VersionedParent]{
		svc:        svc,
		svcName:    "merge_parent",
		config:     HandlerConfig[*bug059VersionedParent]{PathPrefix: "/merge/parent", Cascades: cascades},
		handlerReg: reg,
		txCoord:    NewTxCoordinator(db, nil),
	}
}

// newMergeNonVersionedParentHandler **非版本化**父 Handler（同一开关必须覆盖此路径）。
func newMergeNonVersionedParentHandler(db *gorm.DB, merge bool) *GenericHandler[*bug059VersionedParent] {
	svc := service.NewGenericService[*bug059VersionedParent](
		repository.NewCRUDWithDB[*bug059VersionedParent](db),
		service.Config[*bug059VersionedParent]{})
	cascades := []CascadeRelation{{
		HandlerName: "merge_child", ChildrenField: "children", FKField: "parent_id",
		OnCreate: true, OnUpdate: true,
		MergeChildrenOnUpdate: merge,
		PublishCodeField:      "col_code",
	}}
	reg := NewHandlerRegistry()
	reg.Register("merge_child", newMergeChildHandler(db))
	return &GenericHandler[*bug059VersionedParent]{
		svc:        svc,
		svcName:    "merge_nv_parent",
		config:     HandlerConfig[*bug059VersionedParent]{PathPrefix: "/merge/nv_parent", Cascades: cascades},
		handlerReg: reg,
		txCoord:    NewTxCoordinator(db, nil),
	}
}

// mergeCreate 走一次完整 _doCreate，返回父主键。
func mergeCreate(t *testing.T, h *GenericHandler[*bug059VersionedParent], raw map[string]any) string {
	t.Helper()
	ctx := context.WithValue(context.Background(), rawCreateMapsKey{}, []map[string]any{raw})
	created, err := h._doCreate(ctx, []service.CrudRequest[*bug059VersionedParent]{bug059Req(raw)})
	if err != nil {
		t.Fatalf("_doCreate: %v", err)
	}
	if len(created) == 0 {
		t.Fatal("_doCreate 未返回结果")
	}
	return (*created[0]).ParentULID
}

// mergeUpdate 走一次完整 _doUpdate（版本化父 → 派生新版本），返回新版本父主键。
func mergeUpdate(t *testing.T, h *GenericHandler[*bug059VersionedParent], raw map[string]any) string {
	t.Helper()
	ctx := context.WithValue(context.Background(), rawUpdateMapsKey{}, []map[string]any{raw})
	updated, err := h._doUpdate(ctx, []service.CrudRequest[*bug059VersionedParent]{bug059Req(raw)}, false)
	if err != nil {
		t.Fatalf("_doUpdate: %v", err)
	}
	if len(updated) == 0 {
		t.Fatal("_doUpdate 未返回结果")
	}
	return (*updated[0]).ParentULID
}

// mergeChildrenOf 查询某父版本下的有效子记录。
func mergeChildrenOf(t *testing.T, db *gorm.DB, parentULID string) []mergeChild {
	t.Helper()
	var rows []mergeChild
	db.Where("parent_id = ? AND is_deleted = 0", parentULID).Find(&rows)
	return rows
}

// mergePick 从子记录里按 col_code 取一条（缺失即失败）。
func mergePick(t *testing.T, rows []mergeChild, code string) mergeChild {
	t.Helper()
	for _, r := range rows {
		if r.ColCode == code {
			return r
		}
	}
	t.Fatalf("未找到 col_code=%q 的子记录（共 %d 条：%+v）", code, len(rows), rows)
	return mergeChild{}
}

// mergeFullChild 构造一条字段完整的子记录（旧快照）。
func mergeFullChild(code, title, unit, expr string, width int) map[string]any {
	return map[string]any{
		"col_code": code, "title": title, "unit": unit,
		"width": width, "enabled": 1, "expr": expr, "remark": "旧备注-" + code,
	}
}

// -------- ① 局部字段更新：未提交字段必须保留 --------

func TestCascadeMergeChildrenPartialUpdate(t *testing.T) {
	db := openMergeDB(t)
	h := newMergeParentHandler(db, true)

	v1 := mergeCreate(t, h, map[string]any{
		"code": "M1", "name": "v1",
		"children": []map[string]any{
			mergeFullChild("c1", "列1", "px", "a+b", 120),
			mergeFullChild("c2", "列2", "%", "c+d", 60),
		},
	})
	oldRows := mergeChildrenOf(t, db, v1)
	if len(oldRows) != 2 {
		t.Fatalf("create 后 v1 应有 2 个子记录, 实际 %d", len(oldRows))
	}
	oldC1 := mergePick(t, oldRows, "c1")

	// 局部更新：c1 只提交 expr（带旧 ULID 身份）；c3 为新增；c2 不出现在数组里（删除）
	v2 := mergeUpdate(t, h, map[string]any{
		"id": v1, "name": "v2",
		"children": []map[string]any{
			{"ulid": oldC1.ULID, "expr": "a-b"},
			{"col_code": "c3", "title": "列3", "expr": "e+f"},
		},
	})
	if v2 == v1 {
		t.Fatal("版本化 update 应生成新版本 ULID")
	}

	newRows := mergeChildrenOf(t, db, v2)
	if len(newRows) != 2 {
		t.Fatalf("v2 应有 2 个子记录（c1 合并 + c3 新增）, 实际 %d：%+v", len(newRows), newRows)
	}

	// ① 未提交字段保留 + 提交字段覆盖
	got := mergePick(t, newRows, "c1")
	if got.Expr != "a-b" {
		t.Fatalf("提交字段应被覆盖: expr=%q want \"a-b\"", got.Expr)
	}
	if got.Title != "列1" || got.Unit != "px" || got.Width != 120 || got.Enabled != 1 {
		t.Fatalf("★ 未提交字段应保留旧值: title=%q unit=%q width=%d enabled=%d（want 列1/px/120/1）",
			got.Title, got.Unit, got.Width, got.Enabled)
	}
	if got.Remark != "旧备注-c1" {
		t.Fatalf("未提交字段应保留旧值: remark=%q", got.Remark)
	}

	// ③ 新子记录使用新的子主键（清 PK 重建）
	if got.ULID == oldC1.ULID {
		t.Fatalf("新版本子记录应使用新主键, 仍是 %s", oldC1.ULID)
	}
	if got.ULID == "" {
		t.Fatal("新版本子记录主键不应为空")
	}

	// 新增记录：按提交字段创建（未提交字段为零值）
	added := mergePick(t, newRows, "c3")
	if added.Title != "列3" || added.Expr != "e+f" {
		t.Fatalf("新增子记录内容错误: %+v", added)
	}
	if added.Unit != "" || added.Width != 0 {
		t.Fatalf("新增子记录未提交字段应为零值: unit=%q width=%d", added.Unit, added.Width)
	}

	// ④ 未出现在数组里的旧子记录不进入新版本
	for _, r := range newRows {
		if r.ColCode == "c2" {
			t.Fatalf("c2 未出现在请求数组里 → 不应进入新版本: %+v", r)
		}
	}

	// ② 旧版本子行原样保留（未被修改/搬走）
	stillOld := mergeChildrenOf(t, db, v1)
	if len(stillOld) != 2 {
		t.Fatalf("v1 子表快照应保持 2 条, 实际 %d", len(stillOld))
	}
	keep := mergePick(t, stillOld, "c1")
	if keep.ULID != oldC1.ULID || keep.Expr != "a+b" || keep.Title != "列1" {
		t.Fatalf("旧版本子行不得被修改: %+v（原 %+v）", keep, oldC1)
	}
}

// -------- ② 显式置空 vs 字段未传 --------

func TestCascadeMergeChildrenExplicitClear(t *testing.T) {
	db := openMergeDB(t)
	h := newMergeParentHandler(db, true)

	v1 := mergeCreate(t, h, map[string]any{
		"code": "M2", "name": "v1",
		"children": []map[string]any{mergeFullChild("c1", "列1", "px", "a+b", 120)},
	})
	oldC1 := mergePick(t, mergeChildrenOf(t, db, v1), "c1")

	// unit 显式置空（key 存在、值为 ""）→ 覆盖；title 未传 → 保留
	v2 := mergeUpdate(t, h, map[string]any{
		"id": v1, "name": "v2",
		"children": []map[string]any{
			{"ulid": oldC1.ULID, "unit": ""},
		},
	})

	got := mergePick(t, mergeChildrenOf(t, db, v2), "c1")
	if got.Unit != "" {
		t.Fatalf("显式置空应被写入（key 存在即覆盖）: unit=%q", got.Unit)
	}
	if got.Title != "列1" || got.Expr != "a+b" || got.Width != 120 {
		t.Fatalf("未传字段应保留旧值: %+v", got)
	}
}

// -------- ⑤ 业务 code 兜底匹配（请求不带旧 ULID） --------

func TestCascadeMergeChildrenMatchByBusinessCode(t *testing.T) {
	db := openMergeDB(t)
	h := newMergeParentHandler(db, true)

	v1 := mergeCreate(t, h, map[string]any{
		"code": "M3", "name": "v1",
		"children": []map[string]any{mergeFullChild("c1", "列1", "px", "a+b", 120)},
	})

	// 请求不带 ulid，只带业务 code → 应命中旧子记录并合并（不得当成新增而重复建行）
	v2 := mergeUpdate(t, h, map[string]any{
		"id": v1, "name": "v2",
		"children": []map[string]any{
			{"col_code": "c1", "expr": "a-b"},
		},
	})

	rows := mergeChildrenOf(t, db, v2)
	if len(rows) != 1 {
		t.Fatalf("按 code 命中旧记录应合并为 1 条（而非新增重复行）, 实际 %d：%+v", len(rows), rows)
	}
	if rows[0].Title != "列1" || rows[0].Unit != "px" || rows[0].Expr != "a-b" {
		t.Fatalf("按 code 匹配后应合并旧字段: %+v", rows[0])
	}
}

// -------- ⑥ 身份歧义：同一 code 对应多条旧记录 → 报错，不按位置猜测 --------

func TestCascadeMergeChildrenAmbiguousCode(t *testing.T) {
	db := openMergeDB(t)
	h := newMergeParentHandler(db, true)

	v1 := mergeCreate(t, h, map[string]any{
		"code": "M4", "name": "v1",
		"children": []map[string]any{mergeFullChild("dup", "列1", "px", "a+b", 120)},
	})
	// 人为制造重复 code 的旧数据（框架不会产生，但历史脏数据可能存在）
	if err := db.Create(&mergeChild{
		ULID: "dup_second_row_ulid_000001", ParentID: v1, ColCode: "dup", Title: "列1-重复",
	}).Error; err != nil {
		t.Fatalf("造脏数据失败: %v", err)
	}

	raw := map[string]any{
		"id": v1, "name": "v2",
		"children": []map[string]any{
			{"col_code": "dup", "expr": "a-b"},
		},
	}
	ctx := context.WithValue(context.Background(), rawUpdateMapsKey{}, []map[string]any{raw})
	_, err := h._doUpdate(ctx, []service.CrudRequest[*bug059VersionedParent]{bug059Req(raw)}, false)
	if err == nil {
		t.Fatal("旧记录中 code 不唯一时应报错，不得按位置猜测")
	}
	if !errors.Is(err, errs.ErrAssemblyIdentityAmbiguous) {
		t.Fatalf("应返回 ErrAssemblyIdentityAmbiguous，实际: %v", err)
	}
	if !strings.Contains(err.Error(), "dup") {
		t.Fatalf("错误文案应含歧义 code: %v", err)
	}
}

// -------- ⑦ 未开启开关：保持既有「请求即完整快照」语义 --------

func TestCascadeMergeChildrenDisabledKeepsSnapshotSemantics(t *testing.T) {
	db := openMergeDB(t)
	h := newMergeParentHandler(db, false)

	v1 := mergeCreate(t, h, map[string]any{
		"code": "M5", "name": "v1",
		"children": []map[string]any{mergeFullChild("c1", "列1", "px", "a+b", 120)},
	})
	oldC1 := mergePick(t, mergeChildrenOf(t, db, v1), "c1")

	v2 := mergeUpdate(t, h, map[string]any{
		"id": v1, "name": "v2",
		"children": []map[string]any{
			{"ulid": oldC1.ULID, "expr": "a-b"},
		},
	})

	rows := mergeChildrenOf(t, db, v2)
	if len(rows) != 1 {
		t.Fatalf("v2 应有 1 个子记录, 实际 %d", len(rows))
	}
	got := rows[0]
	if got.Expr != "a-b" {
		t.Fatalf("提交字段应照常写入: expr=%q", got.Expr)
	}
	// 既有语义：请求即新版本的**完整**内容 —— 请求没带的字段（含业务 code）一律为空
	if got.Title != "" || got.Unit != "" || got.Width != 0 || got.ColCode != "" {
		t.Fatalf("未开启开关时应保持既有语义（请求即完整快照，未提交字段为零值）: %+v", got)
	}
}

// -------- ⑧ 非版本化父 + 携带子表：同一开关必须覆盖这条路径 --------

func TestCascadeMergeChildrenNonVersionedParent(t *testing.T) {
	db := openMergeDB(t)
	h := newMergeNonVersionedParentHandler(db, true)

	// 非版本化父（PK 显式指定）
	v1 := mergeCreate(t, h, map[string]any{
		"parent_ulid": "nv_merge_1", "code": "M6", "name": "v1",
		"children": []map[string]any{
			mergeFullChild("c1", "列1", "px", "a+b", 120),
			mergeFullChild("c2", "列2", "%", "c+d", 60),
		},
	})
	if v1 != "nv_merge_1" {
		t.Fatalf("非版本化父 PK 应沿用显式传入值, 实际 %s", v1)
	}
	oldC1 := mergePick(t, mergeChildrenOf(t, db, v1), "c1")

	// update 携带子表：该路径既有语义是「删旧子行 + 全量替换」，合并后替换内容
	// 变为「旧记录 ⊕ 请求字段」，因此未提交字段同样不得归零。
	pk := mergeUpdate(t, h, map[string]any{
		"id": v1, "name": "v2",
		"children": []map[string]any{
			{"ulid": oldC1.ULID, "expr": "a-b"},              // 局部字段 → 合并
			{"col_code": "c3", "title": "列3", "expr": "e+f"}, // 新增
			// c2 从数组消失 → 删除
		},
	})
	if pk != v1 {
		t.Fatalf("非版本化 update 不应更换父主键: %s → %s", v1, pk)
	}

	rows := mergeChildrenOf(t, db, pk)
	if len(rows) != 2 {
		t.Fatalf("非版本化父合并后应有 2 个子记录（c1 合并 + c3 新增）, 实际 %d：%+v", len(rows), rows)
	}

	got := mergePick(t, rows, "c1")
	if got.Expr != "a-b" {
		t.Fatalf("提交字段应被覆盖: expr=%q", got.Expr)
	}
	if got.Title != "列1" || got.Unit != "px" || got.Width != 120 || got.Remark != "旧备注-c1" {
		t.Fatalf("★ 非版本化父路径同样不得丢未提交字段: title=%q unit=%q width=%d remark=%q",
			got.Title, got.Unit, got.Width, got.Remark)
	}
	// 该路径既有语义：旧子行被删除、子记录重建 → 子主键为新建
	if got.ULID == oldC1.ULID {
		t.Fatalf("该路径既有语义为「删旧行 + 新建」，子主键应为新建, 仍是 %s", oldC1.ULID)
	}

	added := mergePick(t, rows, "c3")
	if added.Title != "列3" || added.Expr != "e+f" {
		t.Fatalf("新增子记录内容错误: %+v", added)
	}

	// 消失的 c2 已被删除（软删）
	var deletedC2 int64
	db.Model(&mergeChild{}).
		Where("parent_id = ? AND col_code = ? AND is_deleted = 1", pk, "c2").
		Count(&deletedC2)
	if deletedC2 != 1 {
		t.Fatalf("c2 从数组消失 → 应被删除（软删）, 实际软删行数 %d", deletedC2)
	}
}

// 关闭开关时，非版本化父保持既有「全量替换（请求即完整内容）」语义。
func TestCascadeMergeChildrenNonVersionedDisabledKeepsReplacementSemantics(t *testing.T) {
	db := openMergeDB(t)
	h := newMergeNonVersionedParentHandler(db, false)

	v1 := mergeCreate(t, h, map[string]any{
		"parent_ulid": "nv_merge_2", "code": "M7", "name": "v1",
		"children": []map[string]any{mergeFullChild("c1", "列1", "px", "a+b", 120)},
	})
	oldC1 := mergePick(t, mergeChildrenOf(t, db, v1), "c1")

	pk := mergeUpdate(t, h, map[string]any{
		"id": v1, "name": "v2",
		"children": []map[string]any{
			{"ulid": oldC1.ULID, "expr": "a-b"},
		},
	})

	rows := mergeChildrenOf(t, db, pk)
	if len(rows) != 1 {
		t.Fatalf("应有 1 个子记录, 实际 %d", len(rows))
	}
	got := rows[0]
	if got.Expr != "a-b" {
		t.Fatalf("提交字段应照常写入: expr=%q", got.Expr)
	}
	if got.Title != "" || got.Unit != "" || got.Width != 0 || got.ColCode != "" {
		t.Fatalf("未开启开关时非版本化路径应保持既有全量替换语义（未提交字段为零值）: %+v", got)
	}
}

// -------- ⑨ 兼容别名：旧字段名 MergeChildrenOnVersionedRebuild 仍然生效 --------

func TestCascadeMergeChildrenDeprecatedAliasStillWorks(t *testing.T) {
	db := openMergeDB(t)
	svc := service.NewGenericService[*bug059VersionedParent](
		repository.NewCRUDWithDB[*bug059VersionedParent](db),
		service.Config[*bug059VersionedParent]{})
	reg := NewHandlerRegistry()
	reg.Register("merge_child", newMergeChildHandler(db))
	h := &GenericHandler[*bug059VersionedParent]{
		svc:     svc,
		svcName: "merge_alias_parent",
		config: HandlerConfig[*bug059VersionedParent]{
			PathPrefix: "/merge/alias",
			Cascades: []CascadeRelation{{
				HandlerName: "merge_child", ChildrenField: "children", FKField: "parent_id",
				OnCreate: true, OnUpdate: true,
				// Deprecated 别名（早期采用者写法）：语义等同 MergeChildrenOnUpdate
				MergeChildrenOnVersionedRebuild: true,
				PublishCodeField:                "col_code",
			}},
		},
		handlerReg: reg,
		txCoord:    NewTxCoordinator(db, nil),
	}

	v1 := mergeCreate(t, h, map[string]any{
		"parent_ulid": "alias_p1", "code": "M8", "name": "v1",
		"children": []map[string]any{mergeFullChild("c1", "列1", "px", "a+b", 120)},
	})
	oldC1 := mergePick(t, mergeChildrenOf(t, db, v1), "c1")

	pk := mergeUpdate(t, h, map[string]any{
		"id": v1, "name": "v2",
		"children": []map[string]any{
			{"ulid": oldC1.ULID, "expr": "a-b"},
		},
	})

	got := mergePick(t, mergeChildrenOf(t, db, pk), "c1")
	if got.Title != "列1" || got.Unit != "px" || got.Expr != "a-b" {
		t.Fatalf("旧字段名（兼容别名）应同样生效: %+v", got)
	}
}
