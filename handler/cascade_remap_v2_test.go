package handler

import (
	"context"
	"encoding/json"
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
// 级联引用重映射 v2 —— 跨级联批次发布/消费（catalog）回归测试
//
// 需求来源：doc/reply_cascade_remap_v2_2026-09-20.md §八 + heims §九共识。
//
// 覆盖范围（对应需求 §6 的 11 条 + §8.5 的 8 条验收）：
//  1. 跨批次标量引用重写
//  2. 跨批次标量「仅 code」（旧 ULID 字段缺失）→ 必须补写
//  3. 跨批次对象数组引用重写
//  4. 点号路径引用重写
//  5. 旧 ULID 命中优先 / 6. code 兜底
//  7. ULID 与 code 冲突报错（含同旧 ULID 冲突、同 code 冲突）
//  8. 发布方未执行 / 映射为空 → 事务回滚 + ErrRemapSourceMissing
//  9. 旧版本子行不变、新版本指向本版本新 ULID
// 10. 未配置新键 → v1 行为不回归（由 cascade_remap_test.go 的 15 例守护）
// 11. 同事务内多个独立发布键互不污染
//
// 另：L1 ValidateRemapKeys 全局存在性 / L2 构造期顺序 panic / 多消费方重复 Resolve。
// ============================================================

// -------- 测试实体（v2 专用：三个并列子表，模拟 form 的级联结构）--------
//
// 结构模拟 heims 表单域：
//
//	remapV2Parent
//	  ├─ write_field   ← 发布方（RemapKey = "v2.write_field"）
//	  ├─ list_column   ┐
//	  └─ validation    ┘ 消费方（SourceRemapKey = "v2.write_field"）

// remapV2Parent 版本化父实体。
type remapV2Parent struct {
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

func (d *remapV2Parent) SetDefaults()             {}
func (d *remapV2Parent) SetCreatedAt(t time.Time) { d.CreatedAt = t }
func (d *remapV2Parent) SetCreatedBy(string)      {}
func (d *remapV2Parent) SetUpdatedAt(t time.Time) { d.UpdatedAt = t }
func (d *remapV2Parent) SetUpdatedBy(string)      {}
func (d *remapV2Parent) SupportsDraft() bool      { return false }
func (d *remapV2Parent) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *remapV2Parent) PKField() string          { return "parent_ulid" }
func (d *remapV2Parent) SelfFKField() string      { return "" }

// remapV2Field 发布方子实体（模拟 write_field）。
type remapV2Field struct {
	ULID      string `gorm:"column:field_ulid;primaryKey;size:26" json:"ulid"`
	ParentID  string `gorm:"column:parent_ulid;size:26" json:"parent_ulid"`
	FieldCode string `gorm:"column:field_code;size:64" json:"field_code"`
	Name      string `gorm:"column:name;size:100" json:"name"`
	IsDeleted int8   `gorm:"column:is_deleted;default:0" json:"-"`
}

func (d *remapV2Field) SetDefaults()             {}
func (d *remapV2Field) SetCreatedAt(_ time.Time) {}
func (d *remapV2Field) SetCreatedBy(string)      {}
func (d *remapV2Field) SetUpdatedAt(_ time.Time) {}
func (d *remapV2Field) SetUpdatedBy(string)      {}
func (d *remapV2Field) SupportsDraft() bool      { return false }
func (d *remapV2Field) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *remapV2Field) PKField() string          { return "field_ulid" }
func (d *remapV2Field) SelfFKField() string      { return "" }

// remapV2Column 消费方子实体（模拟 list_column：标量引用 + 仅 code 场景）。
type remapV2Column struct {
	ULID      string `gorm:"column:column_ulid;primaryKey;size:26" json:"ulid"`
	ParentID  string `gorm:"column:parent_ulid;size:26" json:"parent_ulid"`
	ColCode   string `gorm:"column:col_code;size:64" json:"col_code"`

	// 标量引用：指向 write_field
	FieldULID string `gorm:"column:field_ulid;size:26" json:"field_ulid"`
	FieldCode string `gorm:"column:field_code;size:64" json:"field_code"`

	// 点号路径：嵌套 JSON
	Options string `gorm:"column:options;type:json" json:"options"`

	IsDeleted int8 `gorm:"column:is_deleted;default:0" json:"-"`
}

func (d *remapV2Column) SetDefaults()             {}
func (d *remapV2Column) SetCreatedAt(_ time.Time)      {}
func (d *remapV2Column) SetCreatedBy(string)      {}
func (d *remapV2Column) SetUpdatedAt(_ time.Time)      {}
func (d *remapV2Column) SetUpdatedBy(string)      {}
func (d *remapV2Column) SupportsDraft() bool      { return false }
func (d *remapV2Column) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *remapV2Column) PKField() string          { return "column_ulid" }
func (d *remapV2Column) SelfFKField() string      { return "" }

// remapV2Validation 消费方子实体（模拟 validation：对象数组引用）。
type remapV2Validation struct {
	ULID     string `gorm:"column:validation_ulid;primaryKey;size:26" json:"ulid"`
	ParentID string `gorm:"column:parent_ulid;size:26" json:"parent_ulid"`
	VCode    string `gorm:"column:v_code;size:64" json:"v_code"`

	// 对象数组：[{"field_ulid":"...","field_code":"..."}]
	ErrorOn string `gorm:"column:error_on;type:json" json:"error_on"`

	IsDeleted int8 `gorm:"column:is_deleted;default:0" json:"-"`
}

func (d *remapV2Validation) SetDefaults()             {}
func (d *remapV2Validation) SetCreatedAt(_ time.Time)      {}
func (d *remapV2Validation) SetCreatedBy(string)      {}
func (d *remapV2Validation) SetUpdatedAt(_ time.Time)      {}
func (d *remapV2Validation) SetUpdatedBy(string)      {}
func (d *remapV2Validation) SupportsDraft() bool      { return false }
func (d *remapV2Validation) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *remapV2Validation) PKField() string          { return "validation_ulid" }
func (d *remapV2Validation) SelfFKField() string      { return "" }

// -------- 测试装置 --------

const v2RemapKey = "v2.write_field"

func openRemapV2DB(t *testing.T) *gorm.DB {
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
	if err := db.AutoMigrate(&remapV2Parent{}, &remapV2Field{},
		&remapV2Column{}, &remapV2Validation{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	// 共享内存库（cache=shared）在同一进程内**跨测试保留数据**，
	// 计数器断言（如「事务应回滚，库里 0 条」）会被其它用例的残留污染。
	// 因此每个用例开头清一次表（用 GORM 自己的表名解析，避免手写复数形式猜错）。
	for _, m := range []any{&remapV2Parent{}, &remapV2Field{}, &remapV2Column{}, &remapV2Validation{}} {
		if err := db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(m).Error; err != nil {
			t.Fatalf("清理表失败: %v", err)
		}
	}
	return db
}

// remapV2Handlers 组装 v2 测试用 Handler 家族。
//
//	publishRemaps   : 发布方（write_field）的 Remaps（可为 nil，仅发布）
//	columnRemaps    : 消费方（list_column）的 Remaps
//	validationRemaps: 消费方（validation）的 Remaps
//	publishKey      : 发布键（默认 v2RemapKey；传 "" 可模拟漏配发布方）
func remapV2Handlers(
	t *testing.T,
	db *gorm.DB,
	publishRemaps []ReferenceRemap,
	columnRemaps, validationRemaps []ReferenceRemap,
	publishKey string,
) (*GenericHandler[*remapV2Parent], *HandlerRegistry) {
	t.Helper()

	// --- 发布方：write_field ---
	fieldRepo := repository.NewCRUDWithDB[*remapV2Field](db)
	fieldSvc := service.NewGenericService[*remapV2Field](fieldRepo, service.Config[*remapV2Field]{
		EntityName: "v2_field",
	})
	fieldH := NewGenericHandlerWithSvc[*remapV2Field](fieldSvc, "v2_field",
		HandlerConfig[*remapV2Field]{PathPrefix: "/v2/field"})
	fieldH.InstallRemapHook()

	// --- 消费方：list_column ---
	colRepo := repository.NewCRUDWithDB[*remapV2Column](db)
	colSvc := service.NewGenericService[*remapV2Column](colRepo, service.Config[*remapV2Column]{
		EntityName: "v2_column",
	})
	colH := NewGenericHandlerWithSvc[*remapV2Column](colSvc, "v2_column",
		HandlerConfig[*remapV2Column]{PathPrefix: "/v2/column"})
	colH.InstallRemapHook()

	// --- 消费方：validation ---
	valRepo := repository.NewCRUDWithDB[*remapV2Validation](db)
	valSvc := service.NewGenericService[*remapV2Validation](valRepo, service.Config[*remapV2Validation]{
		EntityName: "v2_validation",
	})
	valH := NewGenericHandlerWithSvc[*remapV2Validation](valSvc, "v2_validation",
		HandlerConfig[*remapV2Validation]{PathPrefix: "/v2/validation"})
	valH.InstallRemapHook()

	reg := NewHandlerRegistry()
	reg.Register("v2_field", fieldH)
	reg.Register("v2_column", colH)
	reg.Register("v2_validation", valH)

	// --- 父：Cascades 顺序 = write_field → list_column → validation ---
	parentRepo := repository.NewCRUDWithDB[*remapV2Parent](db)
	parentSvc := service.NewGenericService[*remapV2Parent](parentRepo, service.Config[*remapV2Parent]{
		EntityName:  "v2_parent",
		VersionMode: true,
		VersionFields: &service.VersionFieldMapping{
			ULIDField: "ULID", CodeField: "Code", VersionField: "VersionCode",
			CurrentField: "IsCurrent", StatusField: "VersionStatus",
			ParentField: "ParentVersion", RemarkField: "Remark",
		},
	})
	parentH := NewGenericHandlerWithSvc[*remapV2Parent](parentSvc, "v2_parent",
		HandlerConfig[*remapV2Parent]{
			PathPrefix: "/v2/parent",
			Cascades: []CascadeRelation{
				{
					HandlerName: "v2_field", ChildrenField: "fields", FKField: "parent_ulid",
					OnCreate: true, OnUpdate: true,
					RemapKey: publishKey, PublishCodeField: "field_code",
					Remaps: publishRemaps,
				},
				{
					HandlerName: "v2_column", ChildrenField: "columns", FKField: "parent_ulid",
					OnCreate: true, OnUpdate: true, Remaps: columnRemaps,
				},
				{
					HandlerName: "v2_validation", ChildrenField: "validations", FKField: "parent_ulid",
					OnCreate: true, OnUpdate: true, Remaps: validationRemaps,
				},
			},
		})
	parentH.SetHandlerReg(reg)
	parentH.SetTxCoord(NewTxCoordinator(db, nil))
	return parentH, reg
}

// crossScalarRemaps 消费方：标量引用 + 同级 code（heims §9.5-1 场景）。
func crossScalarRemaps() []ReferenceRemap {
	return []ReferenceRemap{{
		SourceRemapKey: v2RemapKey,
		Bindings: []ReferenceBinding{
			{Field: "field_ulid", CodeField: "field_code", Mode: RemapModeScalar},
		},
	}}
}

// crossObjectArrayRemaps 消费方：对象数组引用。
func crossObjectArrayRemaps() []ReferenceRemap {
	return []ReferenceRemap{{
		SourceRemapKey: v2RemapKey,
		Bindings: []ReferenceBinding{
			{Field: "error_on", Mode: RemapModeObjectArray,
				ULIDKey: "field_ulid", CodeKey: "field_code"},
		},
	}}
}

// crossNestedRemaps 消费方：点号路径（嵌套 JSON）。
func crossNestedRemaps() []ReferenceRemap {
	return []ReferenceRemap{{
		SourceRemapKey: v2RemapKey,
		Bindings: []ReferenceBinding{
			{Field: "options.target.field_ulid", CodeField: "options.target.field_code",
				Mode: RemapModeScalar},
		},
	}}
}

func v2Req(m map[string]any) service.CrudRequest[*remapV2Parent] {
	return &MapRequest[*remapV2Parent]{data: m}
}

// v2Create 走一次完整 _doCreate（含 catalog 生命周期）。
func v2Create(t *testing.T, h *GenericHandler[*remapV2Parent], raw map[string]any) string {
	t.Helper()
	ctx := context.WithValue(context.Background(), rawCreateMapsKey{}, []map[string]any{raw})
	created, err := h._doCreate(ctx, []service.CrudRequest[*remapV2Parent]{v2Req(raw)})
	if err != nil {
		t.Fatalf("_doCreate: %v", err)
	}
	return (*created[0]).ULID
}

// v2Update 走一次完整 _doUpdate（版本化重建子表）。
func v2Update(t *testing.T, h *GenericHandler[*remapV2Parent], raw map[string]any) (string, error) {
	ctx := context.WithValue(context.Background(), rawUpdateMapsKey{}, []map[string]any{raw})
	updated, err := h._doUpdate(ctx, []service.CrudRequest[*remapV2Parent]{v2Req(raw)}, false)
	if err != nil {
		return "", err
	}
	if len(updated) == 0 {
		t.Fatal("_doUpdate 未返回结果")
	}
	return (*updated[0]).ULID, nil
}

// ============================================================
// 用例 1：跨批次标量引用重写（发布方 write_field，消费方 list_column）
// ============================================================

func TestV2CrossBatchScalarRewrite(t *testing.T) {
	db := openRemapV2DB(t)
	h, _ := remapV2Handlers(t, db, nil, crossScalarRemaps(), nil, v2RemapKey)

	// create v1：两个字段 + 一个列（列通过 field_code 关联 f1）
	raw := map[string]any{
		"code": "P1", "name": "v1",
		"fields": []map[string]any{
			{"field_code": "amount", "name": "金额"},
			{"field_code": "qty", "name": "数量"},
		},
		"columns": []map[string]any{
			// 跨批次场景：消费方手里只有 code（旧 ULID 是别的分支的，它拿不到）
			{"col_code": "c1", "field_code": "amount"},
		},
	}
	v1 := v2Create(t, h, raw)

	// v1 的列应已补写上 v1 的 amount 字段 ULID
	var colsV1 []remapV2Column
	db.Where("parent_ulid = ?", v1).Find(&colsV1)
	if len(colsV1) != 1 {
		t.Fatalf("v1 应有 1 个列, 实际 %d", len(colsV1))
	}
	var fieldsV1 []remapV2Field
	db.Where("parent_ulid = ?", v1).Order("field_code").Find(&fieldsV1)
	if len(fieldsV1) != 2 {
		t.Fatalf("v1 应有 2 个字段, 实际 %d", len(fieldsV1))
	}
	amountV1 := fieldsV1[0].ULID // field_code = amount
	if colsV1[0].FieldULID != amountV1 {
		t.Fatalf("★ v1 列应指向 v1 的 amount 字段: got=%s want=%s",
			colsV1[0].FieldULID, amountV1)
	}

	// update → v2（版本化重建，字段与列都会拿到全新 ULID）
	updateRaw := map[string]any{"id": v1, "name": "v2"}
	v2, err := v2Update(t, h, updateRaw)
	if err != nil {
		t.Fatalf("_doUpdate: %v", err)
	}
	if v2 == v1 {
		t.Fatal("版本化 update 应产生新版本 ULID")
	}

	var fieldsV2 []remapV2Field
	db.Where("parent_ulid = ?", v2).Order("field_code").Find(&fieldsV2)
	if len(fieldsV2) != 2 {
		t.Fatalf("v2 应有 2 个字段, 实际 %d", len(fieldsV2))
	}
	amountV2 := fieldsV2[0].ULID

	var colsV2 []remapV2Column
	db.Where("parent_ulid = ?", v2).Find(&colsV2)
	if len(colsV2) != 1 {
		t.Fatalf("v2 应有 1 个列, 实际 %d", len(colsV2))
	}

	// ★ 核心断言：v2 的列指向 v2 的 amount 字段（不是 v1 的）
	if colsV2[0].FieldULID != amountV2 {
		t.Fatalf("★ v2 列应指向 v2 的 amount 字段（跨批次重映射）: got=%s want=%s (v1=%s)",
			colsV2[0].FieldULID, amountV2, amountV1)
	}
	if colsV2[0].FieldULID == amountV1 {
		t.Fatal("★ v2 列仍指向 v1 的字段 —— 跨批次重映射未生效（跨版本悬挂引用）")
	}
}

// ============================================================
// 用例 2：旧 ULID 命中优先（消费方同时带 ULID + code）
// ============================================================

func TestV2CrossBatchULIDTakesPrecedence(t *testing.T) {
	db := openRemapV2DB(t)
	h, _ := remapV2Handlers(t, db, nil, crossScalarRemaps(), nil, v2RemapKey)

	raw := map[string]any{
		"code": "P2", "name": "v1",
		"fields": []map[string]any{{"field_code": "amount", "name": "金额"}},
	}
	v1 := v2Create(t, h, raw)

	var fieldsV1 []remapV2Field
	db.Where("parent_ulid = ?", v1).Find(&fieldsV1)
	amountV1 := fieldsV1[0].ULID

	// 手工把列的 ULID 指向 v1 的字段，同时 code 也带上（两者一致）
	if err := db.Model(&remapV2Column{}).Where("parent_ulid = ?", v1).
		Updates(map[string]any{"field_ulid": amountV1, "field_code": "amount"}).Error; err != nil {
		t.Fatalf("建立引用: %v", err)
	}

	// 更新时携带子表（真实前端行为：回传完整子表）
	updateRaw := map[string]any{
		"id": v1, "name": "v2",
		"fields": []map[string]any{
			{"field_code": "amount", "name": "金额"},
		},
		"columns": []map[string]any{
			{"col_code": "c1", "field_ulid": amountV1, "field_code": "amount"},
		},
	}
	v2, err := v2Update(t, h, updateRaw)
	if err != nil {
		t.Fatalf("_doUpdate: %v", err)
	}

	var fieldsV2 []remapV2Field
	db.Where("parent_ulid = ?", v2).Find(&fieldsV2)
	amountV2 := fieldsV2[0].ULID

	var colsV2 []remapV2Column
	db.Where("parent_ulid = ?", v2).Find(&colsV2)
	if len(colsV2) != 1 {
		t.Fatalf("v2 应有 1 个列, 实际 %d", len(colsV2))
	}
	if colsV2[0].FieldULID != amountV2 {
		t.Fatalf("旧 ULID 命中应优先并重写为新 ULID: got=%s want=%s (v1=%s)",
			colsV2[0].FieldULID, amountV2, amountV1)
	}
}

// ============================================================
// 用例 3：跨批次对象数组引用重写（validation.error_on[]）
// ============================================================

func TestV2CrossBatchObjectArrayRewrite(t *testing.T) {
	db := openRemapV2DB(t)
	h, _ := remapV2Handlers(t, db, nil, nil, crossObjectArrayRemaps(), v2RemapKey)

	// 建 v1：一个字段 + 一个校验（error_on 在 update 回填时才有值，
	// 这里直接在建后手工写入，模拟真实数据的对象数组引用）
	raw := map[string]any{
		"code": "P3", "name": "v1",
		"fields":      []map[string]any{{"field_code": "amount", "name": "金额"}},
		"validations": []map[string]any{{"v_code": "v1"}},
	}
	v1 := v2Create(t, h, raw)

	var fieldsV1 []remapV2Field
	db.Where("parent_ulid = ?", v1).Find(&fieldsV1)
	amountV1 := fieldsV1[0].ULID

	// 写入对象数组引用（含旧 ULID + code）
	errJSON, _ := json.Marshal([]map[string]any{
		{"field_ulid": amountV1, "field_code": "amount"},
	})
	if err := db.Model(&remapV2Validation{}).Where("parent_ulid = ?", v1).
		Update("error_on", string(errJSON)).Error; err != nil {
		t.Fatalf("建立对象数组引用: %v", err)
	}

	updateRaw := map[string]any{"id": v1, "name": "v2"}
	v2, err := v2Update(t, h, updateRaw)
	if err != nil {
		t.Fatalf("_doUpdate: %v", err)
	}

	var fieldsV2 []remapV2Field
	db.Where("parent_ulid = ?", v2).Find(&fieldsV2)
	amountV2 := fieldsV2[0].ULID

	var valsV2 []remapV2Validation
	db.Where("parent_ulid = ?", v2).Find(&valsV2)
	if len(valsV2) != 1 {
		t.Fatalf("v2 应有 1 个校验, 实际 %d", len(valsV2))
	}

	var arr []map[string]any
	if err := json.Unmarshal([]byte(valsV2[0].ErrorOn), &arr); err != nil {
		t.Fatalf("解析 error_on: %v (raw=%s)", err, valsV2[0].ErrorOn)
	}
	if len(arr) != 1 {
		t.Fatalf("error_on 应有 1 个元素, 实际 %d", len(arr))
	}
	if got := arr[0]["field_ulid"]; got != amountV2 {
		t.Fatalf("★ 对象数组引用应指向 v2 的字段: got=%v want=%s (v1=%s)",
			got, amountV2, amountV1)
	}
}

// ============================================================
// 用例 4：点号路径引用重写（嵌套 JSON）
// ============================================================

func TestV2CrossBatchNestedPathRewrite(t *testing.T) {
	db := openRemapV2DB(t)
	h, _ := remapV2Handlers(t, db, nil, crossNestedRemaps(), nil, v2RemapKey)

	raw := map[string]any{
		"code": "P4", "name": "v1",
		"fields": []map[string]any{{"field_code": "amount", "name": "金额"}},
	}
	v1 := v2Create(t, h, raw)

	var fieldsV1 []remapV2Field
	db.Where("parent_ulid = ?", v1).Find(&fieldsV1)
	amountV1 := fieldsV1[0].ULID

	// 嵌套 JSON：options.target.field_ulid
	opt, _ := json.Marshal(map[string]any{
		"target": map[string]any{"field_ulid": amountV1, "field_code": "amount"},
	})
	if err := db.Model(&remapV2Column{}).Where("parent_ulid = ?", v1).
		Update("options", string(opt)).Error; err != nil {
		t.Fatalf("建立嵌套引用: %v", err)
	}

	// 更新时携带子表：options 是 type:json 的 **string 列**，
	// 前端按字符串形态回传（真实契约），且刻意**不带旧 ULID**，
	// 验证「点号路径 + code 兜底 + JSON 字符串双形态编码回写」组合。
	optionsJSON, _ := json.Marshal(map[string]any{
		"target": map[string]any{"field_code": "amount"},
	})
	v2, err := v2Update(t, h, map[string]any{
		"id": v1, "name": "v2",
		"fields": []map[string]any{{"field_code": "amount", "name": "金额"}},
		"columns": []map[string]any{
			{"col_code": "c1", "options": string(optionsJSON)},
		},
	})
	if err != nil {
		t.Fatalf("_doUpdate: %v", err)
	}

	var fieldsV2 []remapV2Field
	db.Where("parent_ulid = ?", v2).Find(&fieldsV2)
	amountV2 := fieldsV2[0].ULID

	var colsV2 []remapV2Column
	db.Where("parent_ulid = ?", v2).Find(&colsV2)
	if len(colsV2) != 1 {
		t.Fatalf("v2 应有 1 个列, 实际 %d", len(colsV2))
	}
	if colsV2[0].Options == "" {
		t.Fatalf("options 未落库（空字符串）—— 子数据未传入实体")
	}
	var optOut map[string]any
	if err := json.Unmarshal([]byte(colsV2[0].Options), &optOut); err != nil {
		t.Fatalf("解析 options: %v (raw=%s)", err, colsV2[0].Options)
	}
	got := nestedGet(optOut, "target.field_ulid")
	if got != amountV2 {
		t.Fatalf("★ 点号路径引用应指向 v2 的字段: got=%v want=%s (v1=%s, raw=%s)",
			got, amountV2, amountV1, colsV2[0].Options)
	}
}

// nestedGet 从嵌套 map 按键路径取值（测试辅助）。
func nestedGet(m map[string]any, path string) any {
	cur := any(m)
	for _, seg := range strings.Split(path, ".") {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[seg]
	}
	return cur
}

// ============================================================
// 用例 5：发布方未执行（漏配 RemapKey）→ ErrRemapSourceMissing + 事务回滚
// ============================================================

func TestV2SourceMissingFailsTransaction(t *testing.T) {
	db := openRemapV2DB(t)
	// publishKey = "" → 发布方不存在，消费方必然拿不到映射
	h, _ := remapV2Handlers(t, db, nil, crossScalarRemaps(), nil, "")

	raw := map[string]any{
		"code": "P5", "name": "v1",
		"fields":  []map[string]any{{"field_code": "amount"}},
		"columns": []map[string]any{{"col_code": "c1", "field_code": "amount"}},
	}
	ctx := context.WithValue(context.Background(), rawCreateMapsKey{}, []map[string]any{raw})
	_, err := h._doCreate(ctx, []service.CrudRequest[*remapV2Parent]{v2Req(raw)})
	if err == nil {
		t.Fatal("★ 发布方缺失时应让事务失败，而不是静默保留旧引用")
	}
	if !errors.Is(err, errs.ErrRemapSourceMissing) {
		t.Fatalf("错误应为 ErrRemapSourceMissing, 实际: %v", err)
	}
	// 与「值无法解析」严格区分（heims §7.4-4）
	if errors.Is(err, errs.ErrRemapUnresolved) {
		t.Fatalf("ErrRemapSourceMissing 不应同时满足 ErrRemapUnresolved: %v", err)
	}
	// 文案要求（heims §7.1-3）：含 key
	if !strings.Contains(err.Error(), v2RemapKey) {
		t.Fatalf("错误文案应包含 SourceRemapKey: %v", err)
	}

	// 事务回滚：库里不应有父记录
	var cnt int64
	db.Model(&remapV2Parent{}).Count(&cnt)
	if cnt != 0 {
		t.Fatalf("事务应回滚, 实际落库 %d 条父记录", cnt)
	}
}

// ============================================================
// 用例 6：L2 构造期顺序校验（同数组内消费方在发布方之前 → panic）
// ============================================================

func TestV2L2ConstructionOrderPanics(t *testing.T) {
	db := openRemapV2DB(t)
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("★ 同数组内发布方声明在消费方之后应构造期 panic（L2）")
		}
		msg := ""
		if s, ok := r.(string); ok {
			msg = s
		} else if e, ok := r.(error); ok {
			msg = e.Error()
		}
		if !strings.Contains(msg, v2RemapKey) {
			t.Fatalf("panic 信息应包含 SourceRemapKey, 实际: %s", msg)
		}
	}()

	colRepo := repository.NewCRUDWithDB[*remapV2Column](db)
	colSvc := service.NewGenericService[*remapV2Column](colRepo, service.Config[*remapV2Column]{EntityName: "v2_column"})
	colH := NewGenericHandlerWithSvc[*remapV2Column](colSvc, "v2_column",
		HandlerConfig[*remapV2Column]{PathPrefix: "/v2/column"})

	// 故意把消费方放在发布方**之前**
	_ = NewGenericHandlerWithSvc[*remapV2Parent](nil, "v2_parent_bad",
		HandlerConfig[*remapV2Parent]{
			PathPrefix: "/v2/parent_bad",
			Cascades: []CascadeRelation{
				{
					HandlerName: "v2_column", ChildrenField: "columns", FKField: "parent_ulid",
					OnCreate: true, Remaps: crossScalarRemaps(), // 消费方在前
				},
				{
					HandlerName: "v2_field", ChildrenField: "fields", FKField: "parent_ulid",
					OnCreate: true, RemapKey: v2RemapKey, // 发布方在后
				},
			},
		})
	_ = colH
}

// ============================================================
// 用例 7：L2 不越权 —— 发布方不在本数组时完全不校验（heims §9.5-2）
// ============================================================

func TestV2L2SkipsWhenPublisherElsewhere(t *testing.T) {
	// 消费方所在数组内没有该 key 的发布方（发布方在子 Handler 子树）→ 不 panic
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("发布方不在本数组时 L2 应完全跳过，实际 panic: %v", r)
		}
	}()
	_ = NewGenericHandlerWithSvc[*remapV2Parent](nil, "v2_parent_ok",
		HandlerConfig[*remapV2Parent]{
			PathPrefix: "/v2/parent_ok",
			Cascades: []CascadeRelation{
				{
					HandlerName: "v2_column", ChildrenField: "columns", FKField: "parent_ulid",
					OnCreate: true, Remaps: crossScalarRemaps(),
				},
			},
		})
}

// ============================================================
// 用例 8：L1 ValidateRemapKeys —— 聚合报告全部缺失 key
// ============================================================

func TestV2L1ValidateRemapKeysAggregates(t *testing.T) {
	db := openRemapV2DB(t)

	// 两个消费方 key 都没有发布方
	missingA, missingB := "v2.none_a", "v2.none_b"
	colRemaps := []ReferenceRemap{
		{SourceRemapKey: missingA, Bindings: []ReferenceBinding{{Field: "field_ulid", Mode: RemapModeScalar}}},
		{SourceRemapKey: missingB, Bindings: []ReferenceBinding{{Field: "field_ulid", Mode: RemapModeScalar}}},
	}
	// 注意：发布键/消费键都声明在**父 Handler** 的 Cascades 上，而父 Handler
	// 通常不在注册表里（注册的是它的子 Handler）。ValidateRemapKeys 由父 Handler
	// 自身发起，故必须连同自身一起校验。
	h, _ := remapV2Handlers(t, db, nil, colRemaps, nil, v2RemapKey)

	err := h.ValidateRemapKeys()
	if err == nil {
		t.Fatal("★ 有消费键找不到发布方时 ValidateRemapKeys 应返回 error")
	}
	msg := err.Error()
	// 聚合：两个 key 都要出现在同一条错误里（heims §9.1）
	if !strings.Contains(msg, missingA) || !strings.Contains(msg, missingB) {
		t.Fatalf("应聚合报告全部缺失 key，实际: %s", msg)
	}
	if !strings.Contains(msg, "2") {
		t.Fatalf("应报告缺失 key 数量，实际: %s", msg)
	}
	// 消费方 Handler 名应出现，便于定位（父 Handler 自身，名为 v2_parent）
	if !strings.Contains(msg, "v2_parent") {
		t.Fatalf("应列出消费方 Handler，实际: %s", msg)
	}

	// 幂等：重复调用结果一致
	if err2 := h.ValidateRemapKeys(); err2 == nil || err2.Error() != msg {
		t.Fatal("ValidateRemapKeys 应幂等且结果稳定")
	}

	// 正向：发布方存在时通过（crossScalarRemaps 消费的正是 v2.write_field）
	h2, _ := remapV2Handlers(t, db, nil, crossScalarRemaps(), nil, v2RemapKey)
	if err := h2.ValidateRemapKeys(); err != nil {
		t.Fatalf("发布方存在时 ValidateRemapKeys 不应报错, 实际: %v", err)
	}
}

// TestV2L1DetectsCrossHandlerPublisher 验证 L1 能识别「发布方在另一个 Handler」的情形。
func TestV2L1DetectsCrossHandlerPublisher(t *testing.T) {
	db := openRemapV2DB(t)

	// 父 Handler A：只有消费键（发布方在父 Handler B 里）
	consumerOnly := []ReferenceRemap{{
		SourceRemapKey: "other.write_field",
		Bindings:       []ReferenceBinding{{Field: "field_ulid", Mode: RemapModeScalar}},
	}}
	hA, _ := remapV2Handlers(t, db, nil, consumerOnly, nil, "")

	// A 自己没发布 other.write_field，注册表里的子 Handler 也没有 → 应报缺失
	if err := hA.ValidateRemapKeys(); err == nil {
		t.Fatal("★ 发布方在别的 Handler 且未注册时应报缺失")
	} else if !strings.Contains(err.Error(), "other.write_field") {
		t.Fatalf("错误应包含缺失的 key, 实际: %v", err)
	}
}

// ============================================================
// 用例 9：多消费方重复 Resolve（非破坏性）+ 同事务多发布键互不污染
// ============================================================

func TestV2CatalogResolveIsNonDestructive(t *testing.T) {
	c := NewRemapCatalog()
	if err := c.Publish("ns.a", map[string]string{"o1": "n1"}, map[string]string{"c1": "n1"}, "pubA"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := c.Publish("ns.b", map[string]string{"o2": "n2"}, nil, "pubB"); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// 重复 Resolve 同一 key：内容一致且不消失（非破坏性）
	for i := 0; i < 3; i++ {
		snap, ok := c.Resolve("ns.a")
		if !ok {
			t.Fatalf("第 %d 次 Resolve 失败（被破坏性取出？）", i+1)
		}
		if snap.oldToNew["o1"] != "n1" {
			t.Fatalf("第 %d 次 Resolve 内容错误: %v", i+1, snap.oldToNew)
		}
	}

	// 命名空间互不污染
	snapA, _ := c.Resolve("ns.a")
	snapB, _ := c.Resolve("ns.b")
	if _, bad := snapA.oldToNew["o2"]; bad {
		t.Fatal("ns.a 不应含 ns.b 的映射（命名空间污染）")
	}
	if _, bad := snapB.oldToNew["o1"]; bad {
		t.Fatal("ns.b 不应含 ns.a 的映射（命名空间污染）")
	}

	// 未发布的 key
	if _, ok := c.Resolve("ns.absent"); ok {
		t.Fatal("未发布的 key 应返回 false")
	}

	// Keys 排序稳定
	keys := c.Keys()
	if len(keys) != 2 || keys[0] != "ns.a" || keys[1] != "ns.b" {
		t.Fatalf("Keys 应排序返回, 实际: %v", keys)
	}
}

// ============================================================
// 用例 10：合并规则 —— 幂等 / 同 code 冲突 / 同旧 ULID 冲突
// ============================================================

func TestV2CatalogMergeRules(t *testing.T) {
	// ① 多批发布合并（不同旧 ULID）
	c := NewRemapCatalog()
	if err := c.Publish("ns", map[string]string{"o1": "n1"}, map[string]string{"c1": "n1"}, "p1"); err != nil {
		t.Fatalf("第一批发布: %v", err)
	}
	if err := c.Publish("ns", map[string]string{"o2": "n2"}, map[string]string{"c2": "n2"}, "p2"); err != nil {
		t.Fatalf("第二批发布（合并）: %v", err)
	}
	snap, _ := c.Resolve("ns")
	if snap.oldToNew["o1"] != "n1" || snap.oldToNew["o2"] != "n2" {
		t.Fatalf("多批发布应合并, 实际: %v", snap.oldToNew)
	}
	if snap.codeToNew["c1"] != "n1" || snap.codeToNew["c2"] != "n2" {
		t.Fatalf("code 映射应合并, 实际: %v", snap.codeToNew)
	}

	// ② 幂等：同 code / 同旧 ULID 映射到相同新 ULID
	if err := c.Publish("ns", map[string]string{"o1": "n1"}, map[string]string{"c1": "n1"}, "p3"); err != nil {
		t.Fatalf("幂等发布不应报错: %v", err)
	}

	// ③ 同 code → 不同新 ULID：报错
	err := c.Publish("ns", nil, map[string]string{"c1": "nX"}, "p4")
	if !errors.Is(err, errs.ErrRemapInconsistent) {
		t.Fatalf("同 code 指向不同目标应报 ErrRemapInconsistent, 实际: %v", err)
	}

	// ④ 同旧 ULID → 不同新 ULID：报错（heims §7.3 追加要求）
	err = c.Publish("ns", map[string]string{"o1": "nX"}, nil, "p5")
	if !errors.Is(err, errs.ErrRemapInconsistent) {
		t.Fatalf("同旧 ULID 指向不同新 ULID 应报 ErrRemapInconsistent, 实际: %v", err)
	}

	// ⑤ 报错不污染已有映射（保持原值）
	snap, _ = c.Resolve("ns")
	if snap.oldToNew["o1"] != "n1" || snap.codeToNew["c1"] != "n1" {
		t.Fatalf("冲突报错后原有映射不应被改写, 实际: %v / %v", snap.oldToNew, snap.codeToNew)
	}
}

// ============================================================
// 用例 11：标量 code 兜底 —— 旧 ULID 缺失时用 code 补写（heims §9.5-1）
// ============================================================

func TestV2ScalarCodeOnlyBackfill(t *testing.T) {
	// 直接测 applyRemap 层：消费数据只有 code，Field 字段缺失
	plan := &RemapPlan{
		oldToNew:    map[string]string{"oldULID": "newULID"},
		codeToNew:   map[string]string{"amount": "newULID"},
		handlerName: "v2_column",
		bindings: []ReferenceBinding{
			{Field: "field_ulid", CodeField: "field_code", Mode: RemapModeScalar},
		},
	}
	// 场景 A：只有 code，field_ulid 字段完全不存在 → 必须补写
	recA := map[string]any{"field_code": "amount"}
	if err := applyRemap(plan, []map[string]any{recA}); err != nil {
		t.Fatalf("applyRemap(A): %v", err)
	}
	if recA["field_ulid"] != "newULID" {
		t.Fatalf("★ 仅凭 code 也应补写 field_ulid: got=%v want=newULID（heims §9.5-1）",
			recA["field_ulid"])
	}

	// 场景 B：ULID 存在但未命中，code 命中 → 用 code 兜底并改写
	recB := map[string]any{"field_ulid": "staleULID", "field_code": "amount"}
	if err := applyRemap(plan, []map[string]any{recB}); err != nil {
		t.Fatalf("applyRemap(B): %v", err)
	}
	if recB["field_ulid"] != "newULID" {
		t.Fatalf("旧 ULID 未命中时应用 code 兜底: got=%v want=newULID", recB["field_ulid"])
	}

	// 场景 C：ULID 与 code 都命中但指向不同目标 → 报错
	planC := &RemapPlan{
		oldToNew:    map[string]string{"oldA": "newA"},
		codeToNew:   map[string]string{"amount": "newB"},
		handlerName: "v2_column",
		bindings: []ReferenceBinding{
			{Field: "field_ulid", CodeField: "field_code", Mode: RemapModeScalar},
		},
	}
	recC := map[string]any{"field_ulid": "oldA", "field_code": "amount"}
	err := applyRemap(planC, []map[string]any{recC})
	if !errors.Is(err, errs.ErrRemapInconsistent) {
		t.Fatalf("ULID 与 code 指向不同目标应报错, 实际: %v", err)
	}

	// 场景 D：两边都解析不出 → ErrRemapUnresolved（与 SourceMissing 区分）
	recD := map[string]any{"field_ulid": "unknown", "field_code": "unknown"}
	err = applyRemap(plan, []map[string]any{recD})
	if !errors.Is(err, errs.ErrRemapUnresolved) {
		t.Fatalf("值无法解析应报 ErrRemapUnresolved, 实际: %v", err)
	}
}

// ============================================================
// 用例 12：空 code / 空旧 ULID 不参与映射、不报错（heims §9.3）
// ============================================================

func TestV2EmptyCodeAndULIDSkipped(t *testing.T) {
	// 空 code 的行不产生 code 映射，也不报错
	c := NewRemapCatalog()
	if err := c.Publish("ns",
		map[string]string{"": "n1", "o2": ""},
		map[string]string{"": "n1", "c2": ""}, "p"); err != nil {
		t.Fatalf("空值发布不应报错: %v", err)
	}
	snap, ok := c.Resolve("ns")
	if !ok {
		t.Fatal("发布后应能 Resolve")
	}
	if len(snap.oldToNew) != 0 || len(snap.codeToNew) != 0 {
		t.Fatalf("空 key/空 value 不应进映射, 实际: %v / %v", snap.oldToNew, snap.codeToNew)
	}

	// buildPublishMaps（发布侧）：空 code / 空旧 ULID / 空新 ULID 一律跳过且不报错
	childData := []map[string]any{
		{"ulid": "n1", "field_code": "amount"}, // 正常
		{"ulid": "n2", "field_code": ""},       // 空 code
		{"ulid": "", "field_code": "qty"},      // 空新 ULID
	}
	// 旧 PK 快照：o1 正常 / o2 对应「空 code 行」/ 第三个为空
	oldToNew, codeToNew := buildPublishMaps(childData,
		[]string{"o1", "o2", ""}, "field_code")

	// code 映射：只有 amount 一条（空 code 与空新 ULID 都被跳过）
	if len(codeToNew) != 1 || codeToNew["amount"] != "n1" {
		t.Fatalf("应只有 1 条 code 映射（amount→n1）, 实际: %v", codeToNew)
	}
	if _, bad := codeToNew[""]; bad {
		t.Fatal("空 code 不应入映射")
	}
	if _, bad := codeToNew["qty"]; bad {
		t.Fatal("新 ULID 为空的行不应入映射（避免 code 指向空目标）")
	}

	// 旧→新映射：o1→n1、o2→n2 都合法（空 code 不影响 ULID 映射）；
	// 第三个旧值为空 → 不入映射
	if len(oldToNew) != 2 ||
		oldToNew["o1"] != "n1" || oldToNew["o2"] != "n2" {
		t.Fatalf("应有 2 条旧→新映射（o1→n1, o2→n2）, 实际: %v", oldToNew)
	}
	if _, bad := oldToNew[""]; bad {
		t.Fatal("空旧 ULID 不应入映射")
	}
}
