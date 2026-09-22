package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/Huey1979/gocrux/repository"
	"github.com/Huey1979/gocrux/service"
)

// ============================================================
// 嵌套级联下的引用装配（应用方 §25 / §26 / §27 的迁移门禁）
//
// 树形（精确复刻 heims 表单域）：
//
//	asm_parent（根，HandlerConfig.Target = "root.form"）
//	  ├─ asm_section（中继层，Target = "asm.section"）
//	  │    ├─ asm_field      ← 发布方在**孙批次**（Target = "asm.field"）
//	  │    └─ asm_column     ← 堂兄弟：本节内的列引用**兄弟分支**的字段
//	  ├─ asm_column          ← 叔侄：根的直接子批次引用孙批次发布的字段
//	  ├─ asm_detail          ← 同上（第二个标量消费方）
//	  └─ asm_validation      ← 对象数组消费方（error_on[]）
//
// 修复前（阶段 1/2 只处理直接子批次）：孙批次要到根的阶段 3 才登记，
// 而根的消费方在阶段 2 已装配完 → 目标索引为空 → 空 ULID / Unmatched。
// ============================================================

const (
	asmFieldTarget   = "asm.field"
	asmSectionTarget = "asm.section"
	asmRootTarget    = "root.form"
)

// -------- 测试实体 --------

// asmSection 中继层（模拟 heims write_section / detail_section）。
type asmSection struct {
	ULID        string `gorm:"column:section_ulid;primaryKey;size:26" json:"ulid"`
	ParentID    string `gorm:"column:parent_ulid;size:26" json:"parent_ulid"`
	SectionCode string `gorm:"column:section_code;size:64" json:"section_code"`
	// 引用祖记录自身（§27.3：祖先作为 Target）
	FormCode  string `gorm:"column:form_code;size:64" json:"form_code"`
	FormULID  string `gorm:"column:form_ulid;size:26" json:"form_ulid"`
	IsDeleted int8   `gorm:"column:is_deleted;default:0" json:"-"`
}

func (d *asmSection) SetDefaults()             {}
func (d *asmSection) SetCreatedAt(_ time.Time) {}
func (d *asmSection) SetCreatedBy(string)      {}
func (d *asmSection) SetUpdatedAt(_ time.Time) {}
func (d *asmSection) SetUpdatedBy(string)      {}
func (d *asmSection) SupportsDraft() bool      { return false }
func (d *asmSection) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *asmSection) PKField() string          { return "section_ulid" }
func (d *asmSection) SelfFKField() string      { return "" }

// asmDetail 根的直接子批次标量消费方（模拟 detail_field）。
type asmDetail struct {
	ULID       string `gorm:"column:detail_ulid;primaryKey;size:26" json:"ulid"`
	ParentID   string `gorm:"column:parent_ulid;size:26" json:"parent_ulid"`
	DetailCode string `gorm:"column:detail_code;size:64" json:"detail_code"`
	FieldCode  string `gorm:"column:field_code;size:64" json:"field_code"`
	FieldULID  string `gorm:"column:field_ulid;size:26" json:"field_ulid"`
	IsDeleted  int8   `gorm:"column:is_deleted;default:0" json:"-"`
}

func (d *asmDetail) SetDefaults()             {}
func (d *asmDetail) SetCreatedAt(_ time.Time) {}
func (d *asmDetail) SetCreatedBy(string)      {}
func (d *asmDetail) SetUpdatedAt(_ time.Time) {}
func (d *asmDetail) SetUpdatedBy(string)      {}
func (d *asmDetail) SupportsDraft() bool      { return false }
func (d *asmDetail) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *asmDetail) PKField() string          { return "detail_ulid" }
func (d *asmDetail) SelfFKField() string      { return "" }

// asmValidation 对象数组消费方（模拟 form_validation.error_on[]）；
// 同时支持标量引用形态（远层级用例：校验规则直接引用某个字段）。
type asmValidation struct {
	ULID      string `gorm:"column:rule_ulid;primaryKey;size:26" json:"ulid"`
	ParentID  string `gorm:"column:parent_ulid;size:26" json:"parent_ulid"`
	RuleCode  string `gorm:"column:rule_code;size:64" json:"rule_code"`
	ErrorOn   string `gorm:"column:error_on;type:json" json:"error_on"`
	FieldCode string `gorm:"column:field_code;size:64" json:"field_code"`
	FieldULID string `gorm:"column:field_ulid;size:26" json:"field_ulid"`
	IsDeleted int8   `gorm:"column:is_deleted;default:0" json:"-"`
}

func (d *asmValidation) SetDefaults()             {}
func (d *asmValidation) SetCreatedAt(_ time.Time) {}
func (d *asmValidation) SetCreatedBy(string)      {}
func (d *asmValidation) SetUpdatedAt(_ time.Time) {}
func (d *asmValidation) SetUpdatedBy(string)      {}
func (d *asmValidation) SupportsDraft() bool      { return false }
func (d *asmValidation) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *asmValidation) PKField() string          { return "rule_ulid" }
func (d *asmValidation) SelfFKField() string      { return "" }

// -------- 装置 --------

// openNestedAsmDB 在共享内存库上迁移并清理嵌套用例全部表。
func openNestedAsmDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := openAsmDB(t)
	if err := db.AutoMigrate(&asmSection{}, &asmDetail{}, &asmValidation{}); err != nil {
		t.Fatalf("AutoMigrate nested: %v", err)
	}
	for _, m := range []any{&asmSection{}, &asmDetail{}, &asmValidation{}} {
		if err := db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(m).Error; err != nil {
			t.Fatalf("清表失败: %v", err)
		}
	}
	return db
}

// scalarFieldAssembly 标量引用（按 code 匹配字段 ULID）。
func scalarFieldAssembly() ReferenceAssembly {
	return ReferenceAssembly{
		Source: "field_ulid", Target: asmFieldTarget,
		Match:  map[string]string{"field_code": "field_code"},
		Assign: map[string]string{"field_ulid": "ulid"},
	}
}

// nestedSectionCascades 中继层（asm_section）的级联声明。
func nestedSectionCascades() []CascadeRelation {
	return []CascadeRelation{
		{
			// FKField 是**子实体自己的字段名**（asmField.ParentID → parent_ulid），
			// 与父级的主键列名无关 —— 每一层都把父身份写进同一个 parent_ulid 列。
			HandlerName: "asm_field", ChildrenField: "fields", FKField: "parent_ulid",
			OnCreate: true, OnUpdate: true, Target: asmFieldTarget,
			// 批内自引用（§25.4 #3）：字段的 access 子项引用**同批**其它字段
			Assemblies: []ReferenceAssembly{{
				Source: "field_access[]", Target: "",
				Match:  map[string]string{"field_code": "field_code"},
				Assign: map[string]string{"field_ulid": "ulid"},
			}},
		},
		{
			HandlerName: "asm_column", ChildrenField: "columns", FKField: "parent_ulid",
			OnCreate: true, OnUpdate: true,
			// 堂兄弟（§27.4 #2）：本节内的列引用另一兄弟分支的字段
			Assemblies: []ReferenceAssembly{scalarFieldAssembly()},
		},
	}
}

// nestedAsmRegistry 构造嵌套用例的全部子 Handler（共享于两种根实体）。
func nestedAsmRegistry(t *testing.T, db *gorm.DB, columnHooks HandlerHooks[*asmColumn]) *HandlerRegistry {
	t.Helper()

	fieldH := NewGenericHandlerWithSvc[*asmField](
		service.NewGenericService[*asmField](repository.NewCRUDWithDB[*asmField](db),
			service.Config[*asmField]{EntityName: "asm_field"}),
		"asm_field", HandlerConfig[*asmField]{PathPrefix: "/asm/field"})

	columnH := NewGenericHandlerWithSvc[*asmColumn](
		service.NewGenericService[*asmColumn](repository.NewCRUDWithDB[*asmColumn](db),
			service.Config[*asmColumn]{EntityName: "asm_column"}),
		"asm_column", HandlerConfig[*asmColumn]{
			PathPrefix: "/asm/column",
			// 第四层（§27.4 #5 远层级）：列下面挂校验规则，且它自己也引用
			// 由中继层发布的字段 —— 消费方在 depth 3、发布方在 depth 2。
			Cascades: []CascadeRelation{{
				HandlerName: "asm_validation", ChildrenField: "validations",
				FKField: "parent_ulid", OnCreate: true, OnUpdate: true,
				Assemblies: []ReferenceAssembly{scalarFieldAssembly()},
			}},
		})
	if columnHooks.BeforeCascadePrepare != nil || columnHooks.AfterCascadeAssemble != nil {
		columnH.SetHooks(columnHooks)
	}

	sectionH := NewGenericHandlerWithSvc[*asmSection](
		service.NewGenericService[*asmSection](repository.NewCRUDWithDB[*asmSection](db),
			service.Config[*asmSection]{EntityName: "asm_section"}),
		"asm_section", HandlerConfig[*asmSection]{
			PathPrefix: "/asm/section", Target: asmSectionTarget, Cascades: nestedSectionCascades(),
		})

	detailH := NewGenericHandlerWithSvc[*asmDetail](
		service.NewGenericService[*asmDetail](repository.NewCRUDWithDB[*asmDetail](db),
			service.Config[*asmDetail]{EntityName: "asm_detail"}),
		"asm_detail", HandlerConfig[*asmDetail]{PathPrefix: "/asm/detail"})

	validationH := NewGenericHandlerWithSvc[*asmValidation](
		service.NewGenericService[*asmValidation](repository.NewCRUDWithDB[*asmValidation](db),
			service.Config[*asmValidation]{EntityName: "asm_validation"}),
		"asm_validation", HandlerConfig[*asmValidation]{PathPrefix: "/asm/validation"})

	reg := NewHandlerRegistry()
	reg.Register("asm_field", fieldH)
	reg.Register("asm_column", columnH)
	reg.Register("asm_section", sectionH)
	reg.Register("asm_detail", detailH)
	reg.Register("asm_validation", validationH)

	// 中继层自身也有级联：必须像真实接入一样注入注册表与事务编排器，
	// 否则它的 _doCreate 会退化成「无级联创建」，孙批次永远不会被落库。
	sectionH.SetHandlerReg(reg)
	sectionH.SetTxCoord(NewTxCoordinator(db, nil).SetRetryOnPKConflict(true))
	// 列层同样是中继层（列 → 校验规则）：同一要求。
	columnH.SetHandlerReg(reg)
	columnH.SetTxCoord(NewTxCoordinator(db, nil).SetRetryOnPKConflict(true))
	return reg
}

// buildNestedAsmHandler 非版本化根 + 嵌套树。
//
// columnAsm 为根的直接子批次 columns 的装配声明（传零值则用标量默认值）。
func buildNestedAsmHandler(
	t *testing.T, db *gorm.DB, columnAsm ReferenceAssembly, columnHooks HandlerHooks[*asmColumn],
) *GenericHandler[*asmParent] {
	t.Helper()
	reg := nestedAsmRegistry(t, db, columnHooks)
	if columnAsm.Source == "" {
		columnAsm = scalarFieldAssembly()
	}

	parentSvc := service.NewGenericService[*asmParent](
		repository.NewCRUDWithDB[*asmParent](db),
		service.Config[*asmParent]{EntityName: "asm_parent"})

	parentH := NewGenericHandlerWithSvc[*asmParent](parentSvc, "asm_parent", HandlerConfig[*asmParent]{
		PathPrefix: "/asm/parent",
		Target:     asmRootTarget, // §27.3：本 Handler 记录自身可被子孙引用
		Cascades: []CascadeRelation{
			{
				HandlerName: "asm_section", ChildrenField: "sections", FKField: "parent_ulid",
				OnCreate: true, OnUpdate: true, Target: asmSectionTarget,
				// 爷孙（向上）：中继层引用**祖记录自身**
				Assemblies: []ReferenceAssembly{{
					Source: "form_ulid", Target: asmRootTarget,
					Match:  map[string]string{"form_code": "code"},
					Assign: map[string]string{"form_ulid": "ulid"},
				}},
			},
			{
				HandlerName: "asm_column", ChildrenField: "columns", FKField: "parent_ulid",
				OnCreate: true, OnUpdate: true,
				// 叔侄：根的直接子批次引用**孙批次**发布的字段
				Assemblies: []ReferenceAssembly{columnAsm},
			},
			{
				HandlerName: "asm_detail", ChildrenField: "details", FKField: "parent_ulid",
				OnCreate: true, OnUpdate: true,
				Assemblies: []ReferenceAssembly{scalarFieldAssembly()},
			},
			{
				HandlerName: "asm_validation", ChildrenField: "validations", FKField: "parent_ulid",
				OnCreate: true, OnUpdate: true,
				Assemblies: []ReferenceAssembly{{
					Source: "error_on[]", Target: asmFieldTarget,
					Match:  map[string]string{"field_code": "field_code"},
					Assign: map[string]string{"field_ulid": "ulid"},
				}},
			},
		},
	})
	parentH.SetHandlerReg(reg)
	parentH.SetTxCoord(NewTxCoordinator(db, nil).SetRetryOnPKConflict(true))
	return parentH
}

// -------- §25.4 #1/#2 + §27.4 #4：嵌套树一次性覆盖三类消费方 + 祖先作为 Target --------

func TestAsmNestedTreeVisibleAcrossLevels(t *testing.T) {
	db := openNestedAsmDB(t)
	h := buildNestedAsmHandler(t, db, ReferenceAssembly{}, HandlerHooks[*asmColumn]{})

	raw := map[string]any{
		"code": "NF1", "name": "表单",
		"sections": []map[string]any{{
			"section_code": "s1", "form_code": "NF1", "form_ulid": "",
			"fields": []map[string]any{{"field_code": "qty", "name": "数量"}},
		}},
		"columns":     []map[string]any{{"col_code": "c1", "field_code": "qty", "field_ulid": ""}},
		"details":     []map[string]any{{"detail_code": "d1", "field_code": "qty", "field_ulid": ""}},
		"validations": []map[string]any{{"rule_code": "v1", "error_on": `[{"field_code":"qty","field_ulid":""}]`}},
	}
	pid := asmCreate(t, h, raw)

	// 发布方：孙批次的字段（form → section → field）
	var field asmField
	if err := db.Where("parent_ulid = ? AND is_deleted = 0", sectionID(t, db, pid)).First(&field).Error; err != nil {
		t.Fatalf("查询孙批次字段: %v", err)
	}
	if field.ULID == "" {
		t.Fatal("孙批次字段应有 ULID")
	}

	// ① 祖先作为 Target（§27.3）：中继层引用根记录自身
	var section asmSection
	if err := db.Where("parent_ulid = ?", pid).First(&section).Error; err != nil {
		t.Fatalf("查询中继层: %v", err)
	}
	if section.FormULID != pid {
		t.Fatalf("★ 中继层应装配到根记录 ULID：got=%s want=%s", section.FormULID, pid)
	}

	// ② 叔侄：根的直接子批次（depth 1）引用孙批次（depth 2）发布的字段
	var col asmColumn
	if err := db.Where("parent_ulid = ? AND is_deleted = 0", pid).First(&col).Error; err != nil {
		t.Fatalf("查询列: %v", err)
	}
	if col.FieldULID != field.ULID {
		t.Fatalf("★ 根的直接子批次必须能引用孙批次字段：got=%s want=%s", col.FieldULID, field.ULID)
	}

	// ③ 第二个标量消费方
	var detail asmDetail
	if err := db.Where("parent_ulid = ? AND is_deleted = 0", pid).First(&detail).Error; err != nil {
		t.Fatalf("查询 detail: %v", err)
	}
	if detail.FieldULID != field.ULID {
		t.Fatalf("★ detail 应装配到孙批次字段：got=%s want=%s", detail.FieldULID, field.ULID)
	}

	// ④ 对象数组消费方（error_on[]）
	var rule asmValidation
	if err := db.Where("parent_ulid = ? AND is_deleted = 0", pid).First(&rule).Error; err != nil {
		t.Fatalf("查询 validation: %v", err)
	}
	if !strings.Contains(rule.ErrorOn, field.ULID) {
		t.Fatalf("★ error_on[] 元素应装配到孙批次字段：got=%s want 含 %s", rule.ErrorOn, field.ULID)
	}
}

// sectionID 取根记录下中继层的 ULID。
func sectionID(t *testing.T, db *gorm.DB, parentID string) string {
	t.Helper()
	var s asmSection
	if err := db.Where("parent_ulid = ?", parentID).First(&s).Error; err != nil {
		t.Fatalf("查询中继层: %v", err)
	}
	return s.ULID
}

// -------- §27.4 #2：堂兄弟（两个兄弟分支的孙批次互相引用） --------

func TestAsmNestedCousinsCrossBranch(t *testing.T) {
	db := openNestedAsmDB(t)
	h := buildNestedAsmHandler(t, db, ReferenceAssembly{}, HandlerHooks[*asmColumn]{})

	raw := map[string]any{
		"code": "NF2",
		"sections": []map[string]any{
			{
				"section_code": "s1",
				"fields":       []map[string]any{{"field_code": "qty"}},
				// 堂兄弟：s1 的列引用 **s2** 的字段
				"columns": []map[string]any{{"col_code": "c1", "field_code": "price", "field_ulid": ""}},
			},
			{
				"section_code": "s2",
				"fields":       []map[string]any{{"field_code": "price"}},
			},
		},
	}
	pid := asmCreate(t, h, raw)

	var sections []asmSection
	db.Where("parent_ulid = ?", pid).Order("section_code").Find(&sections)
	if len(sections) != 2 {
		t.Fatalf("应有 2 个中继层, 实际 %d", len(sections))
	}
	var s2Field asmField
	if err := db.Where("parent_ulid = ? AND field_code = ?", sections[1].ULID, "price").
		First(&s2Field).Error; err != nil {
		t.Fatalf("查询 s2 字段: %v", err)
	}
	var s1Col asmColumn
	if err := db.Where("parent_ulid = ?", sections[0].ULID).First(&s1Col).Error; err != nil {
		t.Fatalf("查询 s1 列: %v", err)
	}
	if s1Col.FieldULID != s2Field.ULID {
		t.Fatalf("★ 堂兄弟（跨分支同层）应能互相引用：got=%s want=%s", s1Col.FieldULID, s2Field.ULID)
	}
}

// -------- §27.4 #5：远层级（四层树，任意两层之间引用） --------

// 树形（四层）：
//
//	asm_parent(1) → asm_section(2)
//	                    ├─ asm_field(3)      发布方
//	                    └─ asm_column(3) → asm_validation(4)   消费方
//
// 校验规则在 depth 3（相对根），引用的字段在 depth 2 —— 消费方比发布方更深，
// 且两者相隔两层。递归阶段 1 必须把 depth 2 的字段批次也登记进索引。
func TestAsmNestedDeepLevelCrossReference(t *testing.T) {
	db := openNestedAsmDB(t)
	h := buildNestedAsmHandler(t, db, ReferenceAssembly{}, HandlerHooks[*asmColumn]{})

	raw := map[string]any{
		"code": "NF5",
		"sections": []map[string]any{{
			"section_code": "s1",
			"fields":       []map[string]any{{"field_code": "qty"}},
			"columns": []map[string]any{{
				"col_code": "c1",
				"validations": []map[string]any{
					{"rule_code": "v1", "field_code": "qty", "field_ulid": ""},
				},
			}},
		}},
	}
	pid := asmCreate(t, h, raw)
	secID := sectionID(t, db, pid)

	var field asmField
	if err := db.Where("parent_ulid = ? AND field_code = ?", secID, "qty").First(&field).Error; err != nil {
		t.Fatalf("查询字段: %v", err)
	}
	var col asmColumn
	if err := db.Where("parent_ulid = ?", secID).First(&col).Error; err != nil {
		t.Fatalf("查询列: %v", err)
	}
	var rule asmValidation
	if err := db.Where("parent_ulid = ?", col.ULID).First(&rule).Error; err != nil {
		t.Fatalf("查询校验规则（第四层）: %v", err)
	}
	if rule.FieldULID != field.ULID {
		t.Fatalf("★ 四层树中相隔两层的引用必须装配：got=%s want=%s", rule.FieldULID, field.ULID)
	}
}

// -------- §25.4 #3：批内自引用（Target 留空） --------

func TestAsmNestedIntraBatchSelfReference(t *testing.T) {
	db := openNestedAsmDB(t)
	h := buildNestedAsmHandler(t, db, ReferenceAssembly{}, HandlerHooks[*asmColumn]{})

	raw := map[string]any{
		"code": "NF3",
		"sections": []map[string]any{{
			"section_code": "s1",
			"fields": []map[string]any{
				{"field_code": "a", "name": "A"},
				{"field_code": "b", "name": "B", "field_access": `[{"field_code":"a","field_ulid":""}]`},
			},
		}},
	}
	pid := asmCreate(t, h, raw)
	secID := sectionID(t, db, pid)

	var fieldA, fieldB asmField
	if err := db.Where("parent_ulid = ? AND field_code = ?", secID, "a").First(&fieldA).Error; err != nil {
		t.Fatalf("查询字段 a: %v", err)
	}
	if err := db.Where("parent_ulid = ? AND field_code = ?", secID, "b").First(&fieldB).Error; err != nil {
		t.Fatalf("查询字段 b: %v", err)
	}
	if fieldA.ULID == "" {
		t.Fatal("字段 a 应有 ULID")
	}
	if !strings.Contains(fieldB.FieldAccess, fieldA.ULID) {
		t.Fatalf("★ 批内自引用应装配到同批字段 ULID：got=%s want 含 %s",
			fieldB.FieldAccess, fieldA.ULID)
	}
}

// -------- §25.4 #4 + §26.4 #5/#6/#7：版本化嵌套回填 + JSON 文本规范化契约 --------

func TestAsmNestedVersionedBackfillWithJSONNormalize(t *testing.T) {
	db := openNestedAsmDB(t)

	// 消费方（根的直接子批次 columns）用**固定嵌套对象**作为 Source：
	// 该对象在 DB 回填时是 JSON 文本 —— 只有应用侧的规范化钩子能解开它，
	// 框架内置解码只处理 `[]` 数组路径，不会去猜普通字符串（§26.2/#7）。
	columnAsm := ReferenceAssembly{
		Source: "options.target", Target: asmFieldTarget,
		Match:  map[string]string{"field_code": "field_code"},
		Assign: map[string]string{"field_ulid": "ulid"},
	}
	columnHooks := HandlerHooks[*asmColumn]{
		// 浅到深：把 schema 标记为 JSON 文本的节点解成对象
		BeforeCascadePrepare: func(_ context.Context, rawMaps []map[string]any) error {
			for _, m := range rawMaps {
				if s, ok := m["options"].(string); ok && s != "" {
					var obj map[string]any
					if err := json.Unmarshal([]byte(s), &obj); err == nil {
						m["options"] = obj
					}
				}
			}
			return nil
		},
		// 深到浅：装配完成后写回原形态
		AfterCascadeAssemble: func(_ context.Context, rawMaps []map[string]any) error {
			for _, m := range rawMaps {
				if obj, ok := m["options"].(map[string]any); ok {
					if b, err := json.Marshal(obj); err == nil {
						m["options"] = string(b)
					}
				}
			}
			return nil
		},
	}

	// 版本化根（复用 bug059 的版本化父实体）+ 嵌套树
	reg := nestedAsmRegistry(t, db, columnHooks)
	parentSvc := service.NewGenericService[*bug059VersionedParent](
		repository.NewCRUDWithDB[*bug059VersionedParent](db),
		service.Config[*bug059VersionedParent]{
			EntityName:  "asm_versioned_parent",
			VersionMode: true,
			VersionFields: &service.VersionFieldMapping{
				ULIDField: "ParentULID", CodeField: "Code", VersionField: "VersionCode",
				CurrentField: "IsCurrent", StatusField: "VersionStatus",
				ParentField: "ParentVersion", RemarkField: "VersionRemark",
			},
		})
	parentH := NewGenericHandlerWithSvc[*bug059VersionedParent](parentSvc, "asm_versioned_parent",
		HandlerConfig[*bug059VersionedParent]{
			PathPrefix: "/asm/vparent", Target: asmRootTarget,
			Cascades: []CascadeRelation{
				{
					HandlerName: "asm_section", ChildrenField: "sections", FKField: "parent_ulid",
					OnCreate: true, OnUpdate: true, Target: asmSectionTarget,
					Assemblies: []ReferenceAssembly{{
						Source: "form_ulid", Target: asmRootTarget,
						Match:  map[string]string{"form_code": "code"},
						Assign: map[string]string{"form_ulid": "ulid"},
					}},
				},
				{
					HandlerName: "asm_column", ChildrenField: "columns", FKField: "parent_ulid",
					OnCreate: true, OnUpdate: true,
					Assemblies: []ReferenceAssembly{columnAsm},
				},
			},
		})
	parentH.SetHandlerReg(reg)
	parentH.SetTxCoord(NewTxCoordinator(db, nil).SetRetryOnPKConflict(true))

	// v1：中继层 + 孙批次字段 + 根级列（options 用 JSON 文本，模拟前端旧形态）
	raw := map[string]any{
		"code": "NV1",
		"name": "v1",
		"sections": []map[string]any{{
			"section_code": "s1", "form_code": "NV1", "form_ulid": "",
			"fields": []map[string]any{{"field_code": "qty"}},
		}},
		"columns": []map[string]any{{
			"col_code": "c1",
			"options":  `{"target":{"field_code":"qty","field_ulid":""}}`,
			// 普通字符串：恰好长得像 JSON，但不是 schema 标记的 JSON 节点 → 必须原样保留
			"field_code": `{"looks":"like json"}`,
		}},
	}
	v1 := asmCreateVersioned(t, parentH, raw)

	// 断言 v1：JSON 文本被钩子解开 → 装配 → 再写回文本；普通字符串未被触碰
	var v1Col asmColumn
	if err := db.Where("parent_ulid = ? AND is_deleted = 0", v1).First(&v1Col).Error; err != nil {
		t.Fatalf("查询 v1 列: %v", err)
	}
	var v1Field asmField
	if err := db.Where("parent_ulid = ? AND field_code = ?", sectionID(t, db, v1), "qty").
		First(&v1Field).Error; err != nil {
		t.Fatalf("查询 v1 字段: %v", err)
	}
	if !strings.Contains(v1Col.Options, v1Field.ULID) {
		t.Fatalf("★ 装配前应按 schema 解码 JSON 文本、装配后写回：got=%s want 含 %s",
			v1Col.Options, v1Field.ULID)
	}
	if v1Col.FieldCode != `{"looks":"like json"}` {
		t.Fatalf("★ 未标记 JSON 的普通字符串必须原样保留：got=%q", v1Col.FieldCode)
	}

	// update（**不带子表**）→ 版本化回填（含孙批次）→ 递归装配
	uraw := map[string]any{"id": v1, "name": "v2"}
	uctx := context.WithValue(context.Background(), rawUpdateMapsKey{}, []map[string]any{uraw})
	updated, err := parentH._doUpdate(uctx,
		[]service.CrudRequest[*bug059VersionedParent]{bug059Req(uraw)}, false)
	if err != nil {
		t.Fatalf("_doUpdate: %v", err)
	}
	v2 := (*updated[0]).ParentULID
	if v2 == "" || v2 == v1 {
		t.Fatalf("版本化 update 应产生新版本: v1=%s v2=%s", v1, v2)
	}

	// ① v2 的孙批次字段是**新 ULID**（递归回填 + 重建）
	secV2 := sectionID(t, db, v2)
	var v2Field asmField
	if err := db.Where("parent_ulid = ? AND field_code = ?", secV2, "qty").
		First(&v2Field).Error; err != nil {
		t.Fatalf("查询 v2 字段: %v", err)
	}
	if v2Field.ULID == v1Field.ULID {
		t.Fatal("v2 的孙批次字段应是新 ULID（复制重建）")
	}

	// ② 递归回填后的新孙批次 ULID 被同次请求的消费者引用（§25.4 #4 核心）
	var v2Col asmColumn
	if err := db.Where("parent_ulid = ? AND is_deleted = 0", v2).First(&v2Col).Error; err != nil {
		t.Fatalf("查询 v2 列: %v", err)
	}
	if !strings.Contains(v2Col.Options, v2Field.ULID) {
		t.Fatalf("★ v2 的引用必须指向 v2 的孙批次字段：got=%s want 含 %s（v1 的是 %s）",
			v2Col.Options, v2Field.ULID, v1Field.ULID)
	}
	if strings.Contains(v2Col.Options, v1Field.ULID) {
		t.Fatalf("★ v2 不得仍指向 v1 的字段（跨版本悬挂引用）：%s", v2Col.Options)
	}

	// ③ 旧版本快照不被改写（含 JSON 文本形态）
	var v1ColReload asmColumn
	if err := db.First(&v1ColReload, "column_ulid = ?", v1Col.ULID).Error; err != nil {
		t.Fatalf("读回 v1 列: %v", err)
	}
	if !strings.Contains(v1ColReload.Options, v1Field.ULID) {
		t.Fatalf("v1 快照不应被改写：%s", v1ColReload.Options)
	}

	// ④ 中继层的祖先引用：v2 的中继层指向 v2
	var v2Section asmSection
	if err := db.Where("parent_ulid = ?", v2).First(&v2Section).Error; err != nil {
		t.Fatalf("查询 v2 中继层: %v", err)
	}
	if v2Section.FormULID != v2 {
		t.Fatalf("v2 中继层应指向 v2 根记录：got=%s want=%s", v2Section.FormULID, v2)
	}
}

// TestAsmNestedSQLiteMigrateGuard 防止测试库遗漏新表（sqlite 共享内存库）。
func TestAsmNestedSQLiteMigrateGuard(t *testing.T) {
	db := openNestedAsmDB(t)
	if !db.Migrator().HasTable(&asmSection{}) ||
		!db.Migrator().HasTable(&asmDetail{}) ||
		!db.Migrator().HasTable(&asmValidation{}) {
		t.Fatal("嵌套用例的表必须已迁移（共享内存库）")
	}
}
