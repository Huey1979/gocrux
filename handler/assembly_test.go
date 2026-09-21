package handler

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	errs "github.com/Huey1979/gocrux/errors"
	"github.com/Huey1979/gocrux/repository"
	"github.com/Huey1979/gocrux/service"
)

// ============================================================
// 引用装配（v3）单元测试 —— 对应设计文档 §9.1
//
// 覆盖：
//  1  路径求值：标量 / 单层数组 / 嵌套对象 / 路径不存在 → 跳过
//  1b 存在性判定（应用方 §15）：key 不存在→跳过；""→回填；null→回填；
//     旧 ULID→重写；已是新 ULID→幂等（5 态全覆盖）
//  2  Match 索引：单键 / 多键 AND / 0 条（WARN）/ 多条（报错）
//  3  Assign：单字段 / 多字段 / 目标字段缺失
//  4  凭证：默认注册表签发 / 校验通过 / 未注册被拒 / 自定义实现
//  5  预分配：唯一性 / 同请求去重
//  6  错误分级：配置错与路径走不通的区分
// ============================================================

// -------- 测试实体 --------

// asmParent 非版本化父实体（装配的载体）。
type asmParent struct {
	ULID      string    `gorm:"column:parent_ulid;primaryKey;size:26" json:"ulid"`
	Code      string    `gorm:"column:code;size:64" json:"code"`
	Name      string    `gorm:"column:name;size:100" json:"name"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updated_at"`
	IsDeleted int8      `gorm:"column:is_deleted;default:0" json:"-"`
}

func (d *asmParent) SetDefaults()             {}
func (d *asmParent) SetCreatedAt(t time.Time) { d.CreatedAt = t }
func (d *asmParent) SetCreatedBy(string)      {}
func (d *asmParent) SetUpdatedAt(t time.Time) { d.UpdatedAt = t }
func (d *asmParent) SetUpdatedBy(string)      {}
func (d *asmParent) SupportsDraft() bool      { return false }
func (d *asmParent) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *asmParent) PKField() string          { return "parent_ulid" }
func (d *asmParent) SelfFKField() string      { return "" }

// asmField 发布方子实体（模拟 write_field）。
type asmField struct {
	ULID      string `gorm:"column:field_ulid;primaryKey;size:26" json:"ulid"`
	ParentID  string `gorm:"column:parent_ulid;size:26" json:"parent_ulid"`
	FieldCode string `gorm:"column:field_code;size:64" json:"field_code"`
	Name      string `gorm:"column:name;size:100" json:"name"`
	IsDeleted int8   `gorm:"column:is_deleted;default:0" json:"-"`
}

func (d *asmField) SetDefaults()             {}
func (d *asmField) SetCreatedAt(_ time.Time) {}
func (d *asmField) SetCreatedBy(string)      {}
func (d *asmField) SetUpdatedAt(_ time.Time) {}
func (d *asmField) SetUpdatedBy(string)      {}
func (d *asmField) SupportsDraft() bool      { return false }
func (d *asmField) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *asmField) PKField() string          { return "field_ulid" }
func (d *asmField) SelfFKField() string      { return "" }

// asmColumn 消费方子实体（模拟 list_column）。
type asmColumn struct {
	ULID      string `gorm:"column:column_ulid;primaryKey;size:26" json:"ulid"`
	ParentID  string `gorm:"column:parent_ulid;size:26" json:"parent_ulid"`
	ColCode   string `gorm:"column:col_code;size:64" json:"col_code"`
	FieldULID string `gorm:"column:field_ulid;size:26" json:"field_ulid"`
	FieldCode string `gorm:"column:field_code;size:64" json:"field_code"`
	Options   string `gorm:"column:options;type:json" json:"options"`
	IsDeleted int8   `gorm:"column:is_deleted;default:0" json:"-"`
}

func (d *asmColumn) SetDefaults()             {}
func (d *asmColumn) SetCreatedAt(_ time.Time) {}
func (d *asmColumn) SetCreatedBy(string)      {}
func (d *asmColumn) SetUpdatedAt(_ time.Time) {}
func (d *asmColumn) SetUpdatedBy(string)      {}
func (d *asmColumn) SupportsDraft() bool      { return false }
func (d *asmColumn) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *asmColumn) PKField() string          { return "column_ulid" }
func (d *asmColumn) SelfFKField() string      { return "" }

// -------- 测试装置 --------

func openAsmDB(t *testing.T) *gorm.DB {
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
	// bug059VersionedParent 仅供版本化装配用例使用（复用其 VersionFields 配置）。
	if err := db.AutoMigrate(&asmParent{}, &asmField{}, &asmColumn{}, &bug059VersionedParent{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	for _, m := range []any{&asmParent{}, &asmField{}, &asmColumn{}, &bug059VersionedParent{}} {
		if err := db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(m).Error; err != nil {
			t.Fatalf("清理表失败: %v", err)
		}
	}
	return db
}

// asmRec 构造一条装配记录（data 即 JSON 形态）。
func asmRec(data map[string]any) *assembledRecord {
	return &assembledRecord{entityType: "t", data: data}
}

// asmTargetRec 构造一条目标候选记录。
func asmTargetRec(data map[string]any) *assembledRecord {
	return &assembledRecord{entityType: "target", data: data}
}

// captureAsmWarn 捕获装配 WARN 日志，返回（获取文本, 还原函数）。
func captureAsmWarn() (func() []string, func()) {
	var mu sync.Mutex
	var msgs []string
	prev := asmLogger
	asmLogger = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		msgs = append(msgs, fmt.Sprintf(format, args...))
	}
	return func() []string {
			mu.Lock()
			defer mu.Unlock()
			out := make([]string, len(msgs))
			copy(out, msgs)
			return out
		}, func() { asmLogger = prev }
}

// ============================================================
// §9.1 #1  路径求值
// ============================================================

func TestAsmPathScalar(t *testing.T) {
	src := asmRec(map[string]any{"field_ulid": "", "field_code": "amount"})
	tgt := []*assembledRecord{asmTargetRec(map[string]any{"ulid": "NEW1", "field_code": "amount"})}

	asm := ReferenceAssembly{
		Source: "field_ulid",
		Target: "ns",
		Match:  map[string]string{"field_code": "field_code"},
		Assign: map[string]string{"field_ulid": "ulid"},
	}
	st, err := assemble(regWithTarget("ns", tgt), []*assembledRecord{src}, asm, "P1", "t")
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if got := src.data["field_ulid"]; got != "NEW1" {
		t.Fatalf("标量路径应回填: got=%v want=NEW1", got)
	}
	if st.Applied != 1 {
		t.Fatalf("Applied 应为 1, 实际 %d", st.Applied)
	}
}

// 批内自引用（Target 留空）：目标即本批记录本身。
func TestAsmBatchLocalTarget(t *testing.T) {
	self := asmRec(map[string]any{"ulid": "SELF1", "field_code": "me"})
	other := asmRec(map[string]any{"ulid": "OTHR1", "field_code": "other"})
	ref := asmRec(map[string]any{"field_code": "other", "field_ulid": ""})
	records := []*assembledRecord{self, other, ref}

	asm := ReferenceAssembly{
		Source: "field_ulid",
		// Target 留空 = 批内自引用：目标即本批记录本身，无需注册表
		Match:  map[string]string{"field_code": "field_code"},
		Assign: map[string]string{"field_ulid": "ulid"},
	}
	if _, err := assemble(nil, records, asm, "P", "t"); err != nil {
		t.Fatalf("批内自引用不应需要注册表: %v", err)
	}
	if got := ref.data["field_ulid"]; got != "OTHR1" {
		t.Fatalf("批内自引用应解析到 OTHR1: got=%v", got)
	}
}

func TestAsmPathArray(t *testing.T) {
	src := asmRec(map[string]any{
		"error_on": []any{
			map[string]any{"field_code": "amount", "field_ulid": ""},
			map[string]any{"field_code": "qty", "field_ulid": ""},
		},
	})
	tgt := []*assembledRecord{
		asmTargetRec(map[string]any{"ulid": "A1", "field_code": "amount"}),
		asmTargetRec(map[string]any{"ulid": "Q1", "field_code": "qty"}),
	}
	asm := ReferenceAssembly{
		Source: "error_on[]",
		Target: "ns",
		Match:  map[string]string{"field_code": "field_code"},
		Assign: map[string]string{"field_ulid": "ulid"},
	}
	st, err := assemble(regWithTarget("ns", tgt), []*assembledRecord{src}, asm, "P1", "t")
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	arr := src.data["error_on"].([]any)
	if got := arr[0].(map[string]any)["field_ulid"]; got != "A1" {
		t.Fatalf("数组元素 0 应回填 A1: got=%v", got)
	}
	if got := arr[1].(map[string]any)["field_ulid"]; got != "Q1" {
		t.Fatalf("数组元素 1 应回填 Q1: got=%v", got)
	}
	if st.Applied != 2 {
		t.Fatalf("Applied 应为 2, 实际 %d", st.Applied)
	}
}

func TestAsmPathNestedObject(t *testing.T) {
	src := asmRec(map[string]any{
		"options": map[string]any{
			"target": map[string]any{"field_code": "amount", "field_ulid": ""},
		},
	})
	tgt := []*assembledRecord{asmTargetRec(map[string]any{"ulid": "NEW1", "field_code": "amount"})}
	asm := ReferenceAssembly{
		Source: "options.target",
		Target: "ns",
		Match:  map[string]string{"field_code": "field_code"},
		Assign: map[string]string{"field_ulid": "ulid"},
	}
	if _, err := assemble(regWithTarget("ns", tgt), []*assembledRecord{src}, asm, "P1", "t"); err != nil {
		t.Fatalf("assemble: %v", err)
	}
	nested := src.data["options"].(map[string]any)["target"].(map[string]any)
	if got := nested["field_ulid"]; got != "NEW1" {
		t.Fatalf("嵌套路径应回填: got=%v", got)
	}
}

// §9.1 #1 + ① 路径不存在 → 静默跳过。
func TestAsmPathMissingSkipped(t *testing.T) {
	src := asmRec(map[string]any{"other": "x"})
	asm := ReferenceAssembly{
		Source: "field_ulid",
		Target: "ns",
		Match:  map[string]string{"field_code": "field_code"},
		Assign: map[string]string{"field_ulid": "ulid"},
	}
	st, err := assemble(NewPreallocRegistry(), []*assembledRecord{src}, asm, "P1", "t")
	if err != nil {
		t.Fatalf("路径不存在不应报错: %v", err)
	}
	if st.Skipped != 1 {
		t.Fatalf("应记为 Skipped, 实际 %+v", st)
	}
	if _, exists := src.data["field_ulid"]; exists {
		t.Fatal("路径不存在时不得凭空创建字段（保持 code-only 语义）")
	}
}

// ============================================================
// §9.1 #1b  存在性判定 5 态（应用方 §15，最关键的一组）
// ============================================================

func TestAsmExistenceFiveStates(t *testing.T) {
	asm := ReferenceAssembly{
		Source: "field_ulid",
		Target: "ns",
		Match:  map[string]string{"field_code": "field_code"},
		Assign: map[string]string{"field_ulid": "ulid"},
	}
	newULID := "NEW_ULID"

	cases := []struct {
		name      string
		data      map[string]any
		wantValue any // nil = 期望 key 不存在
		wantSkip  bool
		wantApply bool
	}{
		{
			name:     "① key 不存在 → code-only 跳过",
			data:     map[string]any{"field_code": "amount"},
			wantSkip: true,
		},
		{
			name:      "② key 存在值为空串 → 回填新 ULID",
			data:      map[string]any{"field_code": "amount", "field_ulid": ""},
			wantValue: newULID,
			wantApply: true,
		},
		{
			name:      "③ key 存在值为 null → 回填新 ULID",
			data:      map[string]any{"field_code": "amount", "field_ulid": nil},
			wantValue: newULID,
			wantApply: true,
		},
		{
			name:      "④ key 存在值为旧 ULID → 重写为新 ULID",
			data:      map[string]any{"field_code": "amount", "field_ulid": "OLD_ULID"},
			wantValue: newULID,
			wantApply: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := asmRec(tc.data)
			tgt := []*assembledRecord{asmTargetRec(map[string]any{"ulid": newULID, "field_code": "amount"})}
			st, err := assemble(regWithTarget("ns", tgt), []*assembledRecord{src}, asm, "P", "t")
			if err != nil {
				t.Fatalf("assemble: %v", err)
			}
			if tc.wantSkip && st.Skipped != 1 {
				t.Fatalf("应跳过: %+v", st)
			}
			if tc.wantApply && st.Applied != 1 {
				t.Fatalf("应回填: %+v data=%v", st, src.data)
			}
			if tc.wantValue != nil {
				if got := src.data["field_ulid"]; got != tc.wantValue {
					t.Fatalf("field_ulid: got=%v want=%v", got, tc.wantValue)
				}
			}
			if tc.wantSkip {
				if _, exists := src.data["field_ulid"]; exists {
					t.Fatal("跳过时不得创建字段")
				}
			}
		})
	}
}

// ⑤ 已是本批次新 ULID → 幂等跳过。
func TestAsmExistenceIdempotent(t *testing.T) {
	src := asmRec(map[string]any{"field_code": "amount", "field_ulid": "NEW1"})
	tgt := []*assembledRecord{asmTargetRec(map[string]any{"ulid": "NEW1", "field_code": "amount"})}
	asm := ReferenceAssembly{
		Source: "field_ulid",
		Target: "ns",
		Match:  map[string]string{"field_code": "field_code"},
		Assign: map[string]string{"field_ulid": "ulid"},
	}
	st, err := assemble(regWithTarget("ns", tgt), []*assembledRecord{src}, asm, "P", "t")
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if st.Applied != 0 {
		t.Fatalf("已是新 ULID 应幂等跳过, 实际 %+v", st)
	}
	if st.Idempotent != 1 {
		t.Fatalf("应记 Idempotent=1, 实际 %+v", st)
	}
}

// 数组元素独立判定（§15.2）：一个元素缺 key、一个元素 key 存在但空。
func TestAsmArrayElementIndependentJudgement(t *testing.T) {
	src := asmRec(map[string]any{
		"error_on": []any{
			map[string]any{"field_code": "amount"},               // key 缺失 → 跳过
			map[string]any{"field_code": "qty", "field_ulid": ""}, // key 存在 → 回填
		},
	})
	tgt := []*assembledRecord{
		asmTargetRec(map[string]any{"ulid": "A1", "field_code": "amount"}),
		asmTargetRec(map[string]any{"ulid": "Q1", "field_code": "qty"}),
	}
	asm := ReferenceAssembly{
		Source: "error_on[]",
		Target: "ns",
		Match:  map[string]string{"field_code": "field_code"},
		Assign: map[string]string{"field_ulid": "ulid"},
	}
	if _, err := assemble(regWithTarget("ns", tgt), []*assembledRecord{src}, asm, "P", "t"); err != nil {
		t.Fatalf("assemble: %v", err)
	}
	arr := src.data["error_on"].([]any)
	if _, exists := arr[0].(map[string]any)["field_ulid"]; exists {
		t.Fatal("元素 0 缺 key → 不得创建 field_ulid")
	}
	if got := arr[1].(map[string]any)["field_ulid"]; got != "Q1" {
		t.Fatalf("元素 1 应回填 Q1: got=%v", got)
	}
}

// ============================================================
// §9.1 #2  Match 索引
// ============================================================

func TestAsmMatchMultiKeyAND(t *testing.T) {
	src := asmRec(map[string]any{"a": "1", "b": "2", "dst": ""})
	tgt := []*assembledRecord{
		asmTargetRec(map[string]any{"a": "1", "b": "X", "ulid": "WRONG"}),
		asmTargetRec(map[string]any{"a": "1", "b": "2", "ulid": "RIGHT"}),
	}
	asm := ReferenceAssembly{
		Source: "dst",
		Target: "ns",
		Match:  map[string]string{"a": "a", "b": "b"},
		Assign: map[string]string{"dst": "ulid"},
	}
	if _, err := assemble(regWithTarget("ns", tgt), []*assembledRecord{src}, asm, "P", "t"); err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if got := src.data["dst"]; got != "RIGHT" {
		t.Fatalf("多键 AND 应命中 RIGHT: got=%v", got)
	}
}

func TestAsmMatchNotFoundWarns(t *testing.T) {
	getWarns, restore := captureAsmWarn()
	defer restore()

	src := asmRec(map[string]any{"field_code": "missing", "field_ulid": "OLD"})
	tgt := []*assembledRecord{asmTargetRec(map[string]any{"ulid": "X", "field_code": "other"})}
	asm := ReferenceAssembly{
		Source: "field_ulid",
		Target: "ns",
		Match:  map[string]string{"field_code": "field_code"},
		Assign: map[string]string{"field_ulid": "ulid"},
	}
	st, err := assemble(regWithTarget("ns", tgt), []*assembledRecord{src}, asm, "P1", "col")
	if err != nil {
		t.Fatalf("匹配不到不应报错（B5: WARN + 保留旧值）: %v", err)
	}
	if st.Unmatched != 1 {
		t.Fatalf("应记 Unmatched=1, 实际 %+v", st)
	}
	if got := src.data["field_ulid"]; got != "OLD" {
		t.Fatalf("匹配不到应保留旧值: got=%v", got)
	}
	warns := getWarns()
	if len(warns) == 0 {
		t.Fatal("匹配不到必须打 WARN 日志")
	}
	// 日志必须能定位（§6.1 #2 要求）：含 Source 路径 / 匹配键 / Target / 父记录
	joined := strings.Join(warns, "\n")
	for _, want := range []string{"field_ulid", "field_code", "P1", "col"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("WARN 日志应含 %q 便于定位: %s", want, joined)
		}
	}
}

func TestAsmMatchAmbiguousErrors(t *testing.T) {
	src := asmRec(map[string]any{"field_code": "dup", "field_ulid": ""})
	tgt := []*assembledRecord{
		asmTargetRec(map[string]any{"ulid": "A", "field_code": "dup"}),
		asmTargetRec(map[string]any{"ulid": "B", "field_code": "dup"}),
	}
	asm := ReferenceAssembly{
		Source: "field_ulid",
		Target: "ns",
		Match:  map[string]string{"field_code": "field_code"},
		Assign: map[string]string{"field_ulid": "ulid"},
	}
	_, err := assemble(regWithTarget("ns", tgt), []*assembledRecord{src}, asm, "P", "t")
	if err == nil {
		t.Fatal("命中多条必须报错（B6）")
	}
	if !isErr(err, errs.ErrAssemblyAmbiguous) {
		t.Fatalf("应为 ErrAssemblyAmbiguous: %v", err)
	}
}

// ============================================================
// §9.1 #3  Assign
// ============================================================

func TestAsmAssignMultiField(t *testing.T) {
	src := asmRec(map[string]any{"field_code": "amount", "field_ulid": ""})
	tgt := []*assembledRecord{asmTargetRec(map[string]any{
		"ulid": "NEW1", "field_code": "amount", "field_name": "金额",
	})}
	asm := ReferenceAssembly{
		Source: "field_ulid",
		Target: "ns",
		Match:  map[string]string{"field_code": "field_code"},
		Assign: map[string]string{"field_ulid": "ulid", "col_label": "field_name"},
	}
	if _, err := assemble(regWithTarget("ns", tgt), []*assembledRecord{src}, asm, "P", "t"); err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if got := src.data["field_ulid"]; got != "NEW1" {
		t.Fatalf("field_ulid: got=%v", got)
	}
	if got := src.data["col_label"]; got != "金额" {
		t.Fatalf("多字段 Assign: col_label got=%v want=金额", got)
	}
}

// 目标字段缺失 → 跳过该键（构造期已校验首段，此处是数据侧兜底）。
func TestAsmAssignTargetFieldMissing(t *testing.T) {
	src := asmRec(map[string]any{"field_code": "amount", "field_ulid": "", "col_label": "keep"})
	tgt := []*assembledRecord{asmTargetRec(map[string]any{"ulid": "NEW1", "field_code": "amount"})}
	asm := ReferenceAssembly{
		Source: "field_ulid",
		Target: "ns",
		Match:  map[string]string{"field_code": "field_code"},
		Assign: map[string]string{"field_ulid": "ulid", "col_label": "nonexistent"},
	}
	if _, err := assemble(regWithTarget("ns", tgt), []*assembledRecord{src}, asm, "P", "t"); err != nil {
		t.Fatalf("目标字段缺失不应报错: %v", err)
	}
	if got := src.data["field_ulid"]; got != "NEW1" {
		t.Fatalf("存在的目标字段仍应写入: got=%v", got)
	}
	if got := src.data["col_label"]; got != "keep" {
		t.Fatalf("目标字段缺失时不得改写源值: got=%v", got)
	}
}

// ============================================================
// §9.1 #4  凭证闭环
// ============================================================

func TestTicketRegistryIssueVerify(t *testing.T) {
	ctx, reg := EnsurePreallocRegistry(context.Background())
	if ticketFrom(ctx) == "" {
		t.Fatal("EnsurePreallocRegistry 应生成 ticket")
	}
	reg.Register("ULID_A")
	if !reg.Has("ULID_A") {
		t.Fatal("已登记 ULID 应校验通过")
	}
	if reg.Has("ULID_B") {
		t.Fatal("未登记 ULID 不应通过")
	}
}

func TestTicketVerifyViaContext(t *testing.T) {
	ctx, reg := EnsurePreallocRegistry(context.Background())
	ctx = InstallTicketBridge(ctx)
	reg.Register("MINE")

	if !VerifyPreallocatedPK(ctx, "MINE") {
		t.Fatal("注册表内的 ULID 应可信")
	}
	if VerifyPreallocatedPK(ctx, "FORGED") {
		t.Fatal("未注册（前端伪造）的 ULID 不应可信")
	}
	if VerifyPreallocatedPK(ctx, "") {
		t.Fatal("空 ULID 不应可信")
	}
}

// 未挂载注册表 → 保持既有「非空即信任」语义（向后兼容的关键）。
func TestTicketBackwardCompatibleNoRegistry(t *testing.T) {
	ctx := context.Background()
	if !VerifyPreallocatedPK(ctx, "ANY") {
		t.Fatal("未挂载注册表时应保持既有语义（视为可信），否则会破坏未迁移的调用方")
	}
}

// 自定义 TicketVerifier 替换默认实现后仍工作。
func TestTicketCustomVerifier(t *testing.T) {
	defer SetTicketVerifier(nil)
	SetTicketVerifier(fakeVerifier{valid: map[string]bool{"SIGNED": true}})

	ctx := InstallTicketBridge(context.Background()) // 无注册表，但自定义实现生效
	if !VerifyPreallocatedPK(ctx, "SIGNED") {
		t.Fatal("自定义实现的合法凭证应通过")
	}
	if VerifyPreallocatedPK(ctx, "OTHER") {
		t.Fatal("自定义实现应拒绝未签发的 ULID")
	}
}

// fakeVerifier 测试用自定义凭证实现。
type fakeVerifier struct{ valid map[string]bool }

func (f fakeVerifier) Issue(string, []string) error { return nil }
func (f fakeVerifier) Verify(_, ulid string) bool   { return f.valid[ulid] }

// ============================================================
// §9.1 #5  预分配
// ============================================================

func TestPreallocateAssignsUniqueULIDs(t *testing.T) {
	_, reg := EnsurePreallocRegistry(context.Background())
	childData := []map[string]any{
		{"field_code": "a"},
		{"field_code": "b"},
		{"field_code": "c"},
	}
	preallocate(context.Background(), reg, "t", "field_ulid", childData, "ns")

	seen := map[string]bool{}
	for i, rec := range childData {
		ulid := scalarToString(rec["ulid"])
		if ulid == "" {
			t.Fatalf("第 %d 条应已分配 ULID", i)
		}
		if len(ulid) != 26 {
			t.Fatalf("ULID 长度应为 26: %q", ulid)
		}
		if seen[ulid] {
			t.Fatalf("ULID 重复: %s", ulid)
		}
		seen[ulid] = true
		if !reg.Has(ulid) {
			t.Fatalf("分配的 ULID 应登记为可信凭证: %s", ulid)
		}
	}
	if len(reg.TargetRecords("ns")) != 3 {
		t.Fatalf("应登记 3 条到 Target 索引, 实际 %d", len(reg.TargetRecords("ns")))
	}
}

// 已有 PK 不重复分配（幂等：同一批被多次送达时 ULID 必须稳定）。
func TestPreallocateIdempotentOnExistingPK(t *testing.T) {
	_, reg := EnsurePreallocRegistry(context.Background())
	childData := []map[string]any{{"ulid": "KEEP_ME"}}
	preallocate(context.Background(), reg, "t", "field_ulid", childData, "")
	if got := childData[0]["ulid"]; got != "KEEP_ME" {
		t.Fatalf("已有 PK 不应被重新分配: got=%v", got)
	}
}

// 高并发下预分配唯一性（NewULID 已改 crypto/rand，此处回归业务侧）。
func TestPreallocateConcurrentUniqueness(t *testing.T) {
	const goroutines = 32
	const perG = 200

	var mu sync.Mutex
	seen := make(map[string]bool, goroutines*perG)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, reg := EnsurePreallocRegistry(context.Background())
			data := make([]map[string]any, perG)
			for i := range data {
				data[i] = map[string]any{}
			}
			preallocate(context.Background(), reg, "t", "field_ulid", data, "")
			mu.Lock()
			defer mu.Unlock()
			for _, rec := range data {
				u := scalarToString(rec["ulid"])
				if seen[u] {
					t.Errorf("并发预分配出现重复 ULID: %s", u)
					return
				}
				seen[u] = true
			}
		}()
	}
	wg.Wait()
}

// ============================================================
// §9.1 #6  错误分级：配置错 vs 路径走不通
// ============================================================

func TestAsmConfigValidation(t *testing.T) {
	cases := []struct {
		name    string
		rel     CascadeRelation
		wantErr string
	}{
		{
			name:    "缺 Match",
			rel:     CascadeRelation{HandlerName: "h", Assemblies: []ReferenceAssembly{{Assign: map[string]string{"a": "b"}}}},
			wantErr: "缺少 Match",
		},
		{
			name:    "缺 Assign",
			rel:     CascadeRelation{HandlerName: "h", Assemblies: []ReferenceAssembly{{Match: map[string]string{"a": "b"}}}},
			wantErr: "缺少 Assign",
		},
		{
			name: "Match 空键",
			rel: CascadeRelation{HandlerName: "h", Assemblies: []ReferenceAssembly{{
				Match: map[string]string{"": "b"}, Assign: map[string]string{"a": "b"},
			}}},
			wantErr: "空键",
		},
		{
			name: "多层数组不支持",
			rel: CascadeRelation{HandlerName: "h", Assemblies: []ReferenceAssembly{{
				Source: "matrix[][]", Match: map[string]string{"a": "b"}, Assign: map[string]string{"a": "b"},
			}}},
			wantErr: "多层数组",
		},
		{
			name: "数组标记不在末尾",
			rel: CascadeRelation{HandlerName: "h", Assemblies: []ReferenceAssembly{{
				Source: "a[].b", Match: map[string]string{"a": "b"}, Assign: map[string]string{"a": "b"},
			}}},
			wantErr: "末尾",
		},
		{
			name: "合法配置",
			rel: CascadeRelation{HandlerName: "h", Assemblies: []ReferenceAssembly{{
				Source: "error_on[]", Match: map[string]string{"field_code": "field_code"},
				Assign: map[string]string{"field_ulid": "ulid"},
			}}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAssemblies(tc.rel, nil)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("合法配置不应报错: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("应报配置错误（含 %q）", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("错误文案应含 %q, 实际: %v", tc.wantErr, err)
			}
		})
	}
}

// 互斥：同一关系不得同时配 Remaps 与 Assemblies（B3）→ 构造期 panic。
func TestAsmRemapsAndAssembliesMutuallyExclusive(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("同一关系同时配 Remaps 与 Assemblies 应构造期 panic（B3）")
		}
		if !strings.Contains(fmt.Sprint(r), "互斥") {
			t.Fatalf("panic 文案应说明互斥: %v", r)
		}
	}()
	db := openAsmDB(t)
	buildAsmParentHandler(t, db, CascadeRelation{
		HandlerName: "asm_field", ChildrenField: "fields", FKField: "parent_ulid",
		OnCreate: true, OnUpdate: true,
		Remaps: []ReferenceRemap{{Bindings: []ReferenceBinding{{Field: "f"}}}},
		Assemblies: []ReferenceAssembly{{
			Match: map[string]string{"a": "b"}, Assign: map[string]string{"a": "b"},
		}},
	})
}

// Target 非空但 ctx 无注册表 → 配置没错，但调用路径不具备前置条件 → 报错不静默。
func TestAsmTargetWithoutRegistryErrors(t *testing.T) {
	src := asmRec(map[string]any{"field_code": "a", "field_ulid": ""})
	asm := ReferenceAssembly{
		Source: "field_ulid", Target: "ns",
		Match:  map[string]string{"field_code": "field_code"},
		Assign: map[string]string{"field_ulid": "ulid"},
	}
	_, err := assemble(nil, []*assembledRecord{src}, asm, "P", "t")
	if err == nil {
		t.Fatal("Target 非空但无注册表时应报错，不得静默跳过")
	}
	if !isErr(err, errs.ErrAssemblyInvalidConfig) {
		t.Fatalf("应为 ErrAssemblyInvalidConfig: %v", err)
	}
}

// ============================================================
// 辅助
// ============================================================

// regWithTarget 构造带 Target 索引的注册表。
func regWithTarget(target string, recs []*assembledRecord) *PreallocRegistry {
	reg := NewPreallocRegistry()
	for _, r := range recs {
		reg.AddTarget(target, r)
	}
	return reg
}

// isErr 判断错误链是否含目标哨兵。
func isErr(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// ============================================================
// §9.2 集成测试：跨分支装配 / 批内自引用 / 预分配落库
//
// 结构模拟 heims 表单域：
//
//	asm_parent
//	  ├─ asm_field  (发布方, Target = "asm.write_field")
//	  └─ asm_column (消费方, Assemblies.Target = "asm.write_field")
//
// 注意发布标识用 v3 的 Target（不是 v2 的 RemapKey）：RemapKey 非空即代表
// v2 通道（落库时才生成 ULID、不建 v3 目标索引），见 useV3Prealloc。
// ============================================================

const asmKey = "asm.write_field"

// buildAsmParentHandler 构造「父 + 发布方子 + 消费方子」的 Handler 家族。
func buildAsmParentHandler(t *testing.T, db *gorm.DB, relations ...CascadeRelation) *GenericHandler[*asmParent] {
	t.Helper()

	fieldRepo := repository.NewCRUDWithDB[*asmField](db)
	fieldSvc := service.NewGenericService[*asmField](fieldRepo, service.Config[*asmField]{EntityName: "asm_field"})
	fieldH := NewGenericHandlerWithSvc[*asmField](fieldSvc, "asm_field",
		HandlerConfig[*asmField]{PathPrefix: "/asm/field"})

	colRepo := repository.NewCRUDWithDB[*asmColumn](db)
	colSvc := service.NewGenericService[*asmColumn](colRepo, service.Config[*asmColumn]{EntityName: "asm_column"})
	colH := NewGenericHandlerWithSvc[*asmColumn](colSvc, "asm_column",
		HandlerConfig[*asmColumn]{PathPrefix: "/asm/column"})

	reg := NewHandlerRegistry()
	reg.Register("asm_field", fieldH)
	reg.Register("asm_column", colH)

	parentRepo := repository.NewCRUDWithDB[*asmParent](db)
	parentSvc := service.NewGenericService[*asmParent](parentRepo, service.Config[*asmParent]{EntityName: "asm_parent"})

	// 默认关系（未显式传入时）
	if len(relations) == 0 {
		relations = []CascadeRelation{
			{
				HandlerName: "asm_field", ChildrenField: "fields", FKField: "parent_ulid",
				OnCreate: true, OnUpdate: true,
				Target: asmKey, // v3 发布标识（供 asm_column 引用）
			},
			{
				HandlerName: "asm_column", ChildrenField: "columns", FKField: "parent_ulid",
				OnCreate: true, OnUpdate: true,
				Assemblies: []ReferenceAssembly{{
					Source: "field_ulid",
					Target: asmKey,
					Match:  map[string]string{"field_code": "field_code"},
					Assign: map[string]string{"field_ulid": "ulid"},
				}},
			},
		}
	}

	parentH := NewGenericHandlerWithSvc[*asmParent](parentSvc, "asm_parent",
		HandlerConfig[*asmParent]{PathPrefix: "/asm/parent", Cascades: relations})
	parentH.SetHandlerReg(reg)
	parentH.SetTxCoord(NewTxCoordinator(db, nil).SetRetryOnPKConflict(true))
	return parentH
}

// asmReq 构造 CRUD 请求。
func asmReq(m map[string]any) service.CrudRequest[*asmParent] {
	return &MapRequest[*asmParent]{data: m}
}

// asmCreate 走一次完整 _doCreate。
func asmCreate(t *testing.T, h *GenericHandler[*asmParent], raw map[string]any) string {
	t.Helper()
	ctx := context.WithValue(context.Background(), rawCreateMapsKey{}, []map[string]any{raw})
	created, err := h._doCreate(ctx, []service.CrudRequest[*asmParent]{asmReq(raw)})
	if err != nil {
		t.Fatalf("_doCreate: %v", err)
	}
	if len(created) == 0 {
		t.Fatal("_doCreate 未返回结果")
	}
	return (*created[0]).ULID
}

// §9.2 #7 跨分支装配：消费方引用发布方本批次的记录（核心 E2E）。
func TestAsmCrossBranchEndToEnd(t *testing.T) {
	db := openAsmDB(t)
	h := buildAsmParentHandler(t, db)

	raw := map[string]any{
		"code": "F1", "name": "表单",
		"fields": []map[string]any{
			{"field_code": "amount", "name": "金额"},
			{"field_code": "qty", "name": "数量"},
			{"field_code": "note", "name": "备注"},
		},
		"columns": []map[string]any{
			// ★ 关键：消费方手里**只有 code**（跨分支，看不到 write_field 的 ULID），
			// 但按 §15.1 属于「field_ulid key 存在、值为空」的 ULID-bearing 模式
			// → 必须被回填本批次的 ULID（若完全不传该 key 则是 code-only，跳过）。
			{"col_code": "c1", "field_code": "qty", "field_ulid": ""},
		},
	}
	pid := asmCreate(t, h, raw)

	// 断言：消费方被装配为**本批次** qty 字段的 ULID
	var fields []asmField
	db.Where("parent_ulid = ?", pid).Order("field_code").Find(&fields)
	byCode := map[string]string{}
	for _, f := range fields {
		byCode[f.FieldCode] = f.ULID
	}
	if len(fields) != 3 {
		t.Fatalf("应有 3 个字段, 实际 %d", len(fields))
	}

	var cols []asmColumn
	db.Where("parent_ulid = ?", pid).Find(&cols)
	if len(cols) != 1 {
		t.Fatalf("应有 1 个列, 实际 %d", len(cols))
	}
	if cols[0].FieldULID != byCode["qty"] {
		t.Fatalf("★ 跨分支装配失败：列应指向本批 qty 字段\ngot=%s want=%s",
			cols[0].FieldULID, byCode["qty"])
	}
	if cols[0].FieldULID == "" {
		t.Fatal("列未补写 ULID（装配未生效）")
	}
}

// §9.2 #8 批内自引用 + §9.1 存在性 5 态的端到端形态。
func TestAsmNewFieldImmediatelyReferenced(t *testing.T) {
	db := openAsmDB(t)
	// 批内自引用：column 引用同批的 field（Target 留空）
	h := buildAsmParentHandler(t, db,
		CascadeRelation{
			HandlerName: "asm_field", ChildrenField: "fields", FKField: "parent_ulid",
			OnCreate: true, OnUpdate: true, Target: asmKey,
		},
		CascadeRelation{
			HandlerName: "asm_column", ChildrenField: "columns", FKField: "parent_ulid",
			OnCreate: true, OnUpdate: true,
			Assemblies: []ReferenceAssembly{{
				Source: "field_ulid",
				Target: asmKey,
				Match:  map[string]string{"field_code": "field_code"},
				Assign: map[string]string{"field_ulid": "ulid"},
			}},
		},
	)

	raw := map[string]any{
		"code": "F2",
		// §9.2 #10c：新建字段立即引用 —— key 存在但值为空串，必须被回填
		"fields": []map[string]any{{"field_code": "new_field", "name": "新建字段"}},
		"columns": []map[string]any{
			{"col_code": "c1", "field_code": "new_field", "field_ulid": ""},
		},
	}
	pid := asmCreate(t, h, raw)

	var f asmField
	if err := db.Where("parent_ulid = ? AND field_code = ?", pid, "new_field").First(&f).Error; err != nil {
		t.Fatalf("查询新字段: %v", err)
	}
	var c asmColumn
	if err := db.Where("parent_ulid = ?", pid).First(&c).Error; err != nil {
		t.Fatalf("查询列: %v", err)
	}
	if c.FieldULID != f.ULID {
		t.Fatalf("★ 新建字段的引用应被回填：got=%s want=%s", c.FieldULID, f.ULID)
	}
}

// §9.2 #10c 反向：完全不传 field_ulid（key 不存在）→ 保持 code-only，不生成 ULID。
func TestAsmCodeOnlyNotUpgraded(t *testing.T) {
	db := openAsmDB(t)
	h := buildAsmParentHandler(t, db)

	raw := map[string]any{
		"code":   "F3",
		"fields": []map[string]any{{"field_code": "amount", "name": "金额"}},
		"columns": []map[string]any{
			// 完全不带 field_ulid 键 → code-only，框架不应生成 ULID
			{"col_code": "c1", "field_code": "amount"},
		},
	}
	pid := asmCreate(t, h, raw)

	var c asmColumn
	if err := db.Where("parent_ulid = ?", pid).First(&c).Error; err != nil {
		t.Fatalf("查询列: %v", err)
	}
	if c.FieldULID != "" {
		t.Fatalf("★ code-only 引用不得被自动升级为 ULID：got=%q", c.FieldULID)
	}
	if c.FieldCode != "amount" {
		t.Fatalf("code 应保留: got=%q", c.FieldCode)
	}
}

// §9.2 #16 三阶段验证：装配发生在全树预分配之后、落库之前。
//
// 验证方式：把 Cascades 的顺序**颠倒**（消费方声明在发布方之前）——
// 若实现是「边展开边装配」，消费方会在发布方登记前执行装配而失败；
// 三阶段划分下结果与顺序无关。
func TestAsmOrderIndependent(t *testing.T) {
	db := openAsmDB(t)
	// 故意把消费方（asm_column）放在发布方（asm_field）**之前**
	h := buildAsmParentHandler(t, db,
		CascadeRelation{
			HandlerName: "asm_column", ChildrenField: "columns", FKField: "parent_ulid",
			OnCreate: true, OnUpdate: true,
			Assemblies: []ReferenceAssembly{{
				Source: "field_ulid", Target: asmKey,
				Match:  map[string]string{"field_code": "field_code"},
				Assign: map[string]string{"field_ulid": "ulid"},
			}},
		},
		CascadeRelation{
			HandlerName: "asm_field", ChildrenField: "fields", FKField: "parent_ulid",
			OnCreate: true, OnUpdate: true, Target: asmKey,
		},
	)

	raw := map[string]any{
		"code":    "F4",
		"fields":  []map[string]any{{"field_code": "amount", "name": "金额"}},
		"columns": []map[string]any{{"col_code": "c1", "field_code": "amount", "field_ulid": ""}},
	}
	pid := asmCreate(t, h, raw)

	var f asmField
	if err := db.Where("parent_ulid = ? AND field_code = ?", pid, "amount").First(&f).Error; err != nil {
		t.Fatalf("查询字段: %v", err)
	}
	var c asmColumn
	if err := db.Where("parent_ulid = ?", pid).First(&c).Error; err != nil {
		t.Fatalf("查询列: %v", err)
	}
	if c.FieldULID != f.ULID {
		t.Fatalf("★ 装配应与 Cascades 声明顺序无关（三阶段）：got=%s want=%s",
			c.FieldULID, f.ULID)
	}
}

// §9.2 #9/#10（update 侧）：更新时子表被清 PK → 填预分配值（重建为新 ULID），
// 且消费方的引用必须装配到**本次重建后**的 ULID（而不是留在旧值上）。
//
// 这是「引用装配」相对 v2 的关键收益：v2 需要「旧 ULID → 新 ULID」映射表才能
// 把引用追回来，v3 在装配阶段引用本来就是新值，不存在"事后修正"这一步。
func TestAsmUpdateRebuildsAndAssembles(t *testing.T) {
	db := openAsmDB(t)
	h := buildAsmParentHandler(t, db)

	pid := asmCreate(t, h, map[string]any{
		"code":    "F8",
		"fields":  []map[string]any{{"field_code": "qty", "name": "数量v1"}},
		"columns": []map[string]any{{"col_code": "c1", "field_code": "qty", "field_ulid": ""}},
	})

	var firstFields []asmField
	db.Where("parent_ulid = ? AND is_deleted = 0", pid).Find(&firstFields)
	if len(firstFields) != 1 {
		t.Fatalf("create 后应有 1 个字段, 实际 %d", len(firstFields))
	}
	firstFieldULID := firstFields[0].ULID

	// update：携带子表 → 非版本化父表的「全量替换」语义（删旧子记录 → 子树重建）。
	raw := map[string]any{
		"id":      pid,
		"fields":  []map[string]any{{"field_code": "qty", "name": "数量v2"}},
		"columns": []map[string]any{{"col_code": "c1", "field_code": "qty", "field_ulid": ""}},
	}
	ctx := context.WithValue(context.Background(), rawUpdateMapsKey{}, []map[string]any{raw})
	if _, err := h._doUpdate(ctx, []service.CrudRequest[*asmParent]{asmReq(raw)}, false); err != nil {
		t.Fatalf("_doUpdate: %v", err)
	}

	var fields []asmField
	db.Where("parent_ulid = ? AND is_deleted = 0", pid).Find(&fields)
	if len(fields) != 1 {
		t.Fatalf("update 后应有 1 个字段, 实际 %d", len(fields))
	}
	if fields[0].ULID == firstFieldULID {
		t.Fatalf("子表应被重建为新 ULID（清 PK → 填预分配值）: 仍是 %s", firstFieldULID)
	}

	var cols []asmColumn
	db.Where("parent_ulid = ? AND is_deleted = 0", pid).Find(&cols)
	if len(cols) != 1 {
		t.Fatalf("update 后应有 1 个列, 实际 %d", len(cols))
	}
	if cols[0].FieldULID != fields[0].ULID {
		t.Fatalf("★ 引用应装配到本次重建后的字段 ULID：got=%s want=%s（旧值=%s）",
			cols[0].FieldULID, fields[0].ULID, firstFieldULID)
	}
}

// §9.2 #15 凭证拒绝：service 落库时对未登记的 PK 重新生成（不采信外部值）。
func TestAsmServiceRejectsForgedPK(t *testing.T) {
	db := openAsmDB(t)
	h := buildAsmParentHandler(t, db)

	// 模拟：请求体携带一个伪造的 ULID（不在本请求注册表中）
	forged := "01FORGED0000000000000000AA"
	raw := map[string]any{
		"code": "F5",
		"fields": []map[string]any{
			{"ulid": forged, "field_code": "amount", "name": "金额"},
		},
	}
	pid := asmCreate(t, h, raw)

	var f asmField
	if err := db.Where("parent_ulid = ?", pid).First(&f).Error; err != nil {
		t.Fatalf("查询字段: %v", err)
	}
	if f.ULID == forged {
		t.Fatal("★ 伪造的 ULID 不应被采信（应重新生成）")
	}
	if f.ULID == "" {
		t.Fatal("应生成新的 ULID")
	}
}

// §9.2 #14 无事务兜底：冲突时不重试，返回主键冲突错误。
func TestAsmPKConflictNoRetryWithoutExplicitOptIn(t *testing.T) {
	db := openAsmDB(t)
	h := buildAsmParentHandler(t, db)
	// 关闭重试（模拟无事务部署）
	h.txCoord.SetRetryOnPKConflict(false)

	// 先占位一条顶层记录，再用它的 ULID 作为冲突源
	//
	// 通过「显式传顶层 ULID」模拟冲突：顶层 PK 会在 preallocateRoot 中
	// 被登记为可信，因此 service 会沿用该值 → INSERT 撞主键。
	occupied := "01OCCUPIED000000000000AA1"
	if err := db.Create(&asmParent{ULID: occupied, Code: "X"}).Error; err != nil {
		t.Fatalf("占位: %v", err)
	}

	raw := map[string]any{"ulid": occupied, "code": "F6"}
	ctx := context.WithValue(context.Background(), rawCreateMapsKey{}, []map[string]any{raw})
	_, err := h._doCreate(ctx, []service.CrudRequest[*asmParent]{asmReq(raw)})
	if err == nil {
		t.Fatal("主键冲突应返回错误")
	}
	if !isErr(err, errs.ErrAssemblyPKConflict) {
		t.Fatalf("应为 ErrAssemblyPKConflict: %v", err)
	}
	if !strings.Contains(err.Error(), "重新发起整个请求") {
		t.Fatalf("错误文案应提示重新发起整个请求: %v", err)
	}
}

// §9.2 #13 有事务时冲突整树重试（重试耗尽后仍报错，但走的是重试路径）。
func TestAsmPKConflictRetryPath(t *testing.T) {
	db := openAsmDB(t)
	h := buildAsmParentHandler(t, db)
	h.txCoord.SetRetryOnPKConflict(true).SetMaxPKConflictRetries(1)

	occupied := "01OCCUPIED100000000000AA2"
	if err := db.Create(&asmParent{ULID: occupied, Code: "Y"}).Error; err != nil {
		t.Fatalf("占位: %v", err)
	}

	raw := map[string]any{"ulid": occupied, "code": "F7"}
	ctx := context.WithValue(context.Background(), rawCreateMapsKey{}, []map[string]any{raw})
	_, err := h._doCreate(ctx, []service.CrudRequest[*asmParent]{asmReq(raw)})
	if err == nil {
		t.Fatal("重试耗尽后仍应报错")
	}
	if !isErr(err, errs.ErrAssemblyPKConflict) {
		t.Fatalf("应为 ErrAssemblyPKConflict: %v", err)
	}
}

// §21.4 #2（应用方 §23.1 要求补齐）：无事务部署的冲突兜底必须**确实标删**
// 本次已写入的记录 —— 此前只验到「不重试 + 打 ERROR 日志」。
//
// 说明：真实冲突是 10^-24 级随机事件，用预分配出来的随机 ULID 无法稳定复现，
// 因此本用例直接驱动兜底入口 `cleanupAfterPKConflict`（它承载「尽力而为清理」
// 的全部逻辑：收集已写记录 → 按实体批量标删 → 汇总失败 → 返回哨兵错误）。
func TestAsmPKConflictCleanupMarksWrittenRecords(t *testing.T) {
	db := openAsmDB(t)
	h := buildAsmParentHandler(t, db)
	h.txCoord.SetRetryOnPKConflict(false) // 模拟无事务部署（不重试）

	// 冲突发生前已经写入的子记录（清理范围必须覆盖它，不能只清顶级）
	if err := db.Create(&asmField{ULID: "WRITTEN1", ParentID: "P1", FieldCode: "qty"}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	ctx, _ := EnsurePreallocRegistry(context.Background())
	markWrittenRecord(ctx, "asm_field", "WRITTEN1")

	err := h.cleanupAfterPKConflict(ctx, fmt.Errorf("UNIQUE constraint failed: asm_fields.field_ulid (1555)"))
	if !isErr(err, errs.ErrAssemblyPKConflict) {
		t.Fatalf("兜底应返回 ErrAssemblyPKConflict: %v", err)
	}
	if !strings.Contains(err.Error(), "重新发起整个请求") {
		t.Fatalf("错误文案应提示重新发起整个请求: %v", err)
	}

	var got asmField
	if e := db.Where("field_ulid = ?", "WRITTEN1").First(&got).Error; e != nil {
		t.Fatalf("查回已写记录: %v", e)
	}
	if got.IsDeleted != 1 {
		t.Fatalf("★ 无事务兜底必须把本次已写入的记录标删: is_deleted=%d", got.IsDeleted)
	}
}

// §9.2 #10b 回填身份映射边界：旧子记录 PK 重复 → 报错，不按位置猜测。
func TestRebuildChildPKsRejectsDuplicateOldPK(t *testing.T) {
	_, reg := EnsurePreallocRegistry(context.Background())
	childData := []map[string]any{
		{"ulid": "DUP", "field_code": "a"},
		{"ulid": "DUP", "field_code": "b"},
	}
	_, err := rebuildChildPKs(reg, "h", "field_ulid", childData)
	if err == nil {
		t.Fatal("旧 PK 重复应报错（无法唯一对应）")
	}
	if !isErr(err, errs.ErrAssemblyIdentityAmbiguous) {
		t.Fatalf("应为 ErrAssemblyIdentityAmbiguous: %v", err)
	}
}

// §9.2 #10b 正常路径：旧 PK 唯一 → 映射确定，填回预分配值。
func TestRebuildChildPKsFillsPreallocated(t *testing.T) {
	_, reg := EnsurePreallocRegistry(context.Background())
	childData := []map[string]any{
		{"ulid": "OLD_A", "field_code": "a"},
		{"ulid": "OLD_B", "field_code": "b"},
	}
	got, err := rebuildChildPKs(reg, "h", "field_ulid", childData)
	if err != nil {
		t.Fatalf("rebuildChildPKs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("应返回 2 个新 ULID, 实际 %d", len(got))
	}
	for i := range childData {
		if childData[i]["ulid"] != got[i] {
			t.Fatalf("第 %d 条应填入预分配值 %s: got=%v", i, got[i], childData[i]["ulid"])
		}
		if strings.HasPrefix(got[i], "OLD_") {
			t.Fatalf("不得保留旧 PK: %s", got[i])
		}
	}
	if got[0] == got[1] {
		t.Fatal("两条应得到不同的新 ULID")
	}
}

// §9.2 #12 清 PK 兼容：v2 通道（Remaps）既有用例由 cascade_remap_test.go
// 与 cascade_remap_v2_test.go 守护；此处额外确认 v3 与 v2 的分流判据。
func TestUseV3PreallocSplitsChannels(t *testing.T) {
	if !useV3Prealloc(CascadeRelation{}) {
		t.Fatal("无 Remaps/RemapKey 的关系应走 v3 预分配")
	}
	if !useV3Prealloc(CascadeRelation{Target: "k"}) {
		t.Fatal("v3 发布方（Target）必须走 v3 预分配 —— " +
			"否则本批记录不进目标索引，消费方装配不到")
	}
	if useV3Prealloc(CascadeRelation{RemapKey: "k"}) {
		t.Fatal("配了 RemapKey 的关系应走 v2 通道（不预分配）")
	}
	if useV3Prealloc(CascadeRelation{Remaps: []ReferenceRemap{{}}}) {
		t.Fatal("配了 Remaps 的关系应走 v2 通道（不预分配）")
	}
}

// §9.2 #9/#10b（版本化门禁，应用方 §23.1）：**版本化父表** update 未携带子表 →
// 回填旧子数据 → 建立身份映射 → 清 PK 填预分配值 → 装配。
//
// 这是 heims 迁移的前置门禁：form / flow 都是版本化实体，子表重建 + 跨版本引用
// 是正式使用路径。断言三件事：
//
//	① v2 的子表是**新 ULID**（复制重建，不是原地改 FK）；
//	② v2 的消费方引用指向 **v2 的**发布方 ULID（否则就是跨版本悬挂引用）；
//	③ v1 的子表快照**保持原样**（旧版本引用不被改写）。
func TestAsmVersionedUpdateBackfillAssembles(t *testing.T) {
	db := openAsmDB(t)
	h := buildAsmVersionedParentHandler(t, db)

	// ---- v1：create 带全子表 ----
	raw := map[string]any{
		"code":    "VF1",
		"name":    "v1",
		"fields":  []map[string]any{{"field_code": "qty", "name": "数量"}},
		"columns": []map[string]any{{"col_code": "c1", "field_code": "qty", "field_ulid": ""}},
	}
	v1 := asmCreateVersioned(t, h, raw)

	var v1Fields []asmField
	db.Where("parent_ulid = ? AND is_deleted = 0", v1).Find(&v1Fields)
	if len(v1Fields) != 1 {
		t.Fatalf("v1 应有 1 个字段, 实际 %d", len(v1Fields))
	}
	var v1Cols []asmColumn
	db.Where("parent_ulid = ? AND is_deleted = 0", v1).Find(&v1Cols)
	if len(v1Cols) != 1 || v1Cols[0].FieldULID != v1Fields[0].ULID {
		t.Fatalf("v1 的引用装配应指向 v1 字段: %+v vs %s", v1Cols, v1Fields[0].ULID)
	}

	// ---- update：**不带子表** → 版本化回填 + 重建 + 装配 ----
	uraw := map[string]any{"id": v1, "name": "v2"}
	uctx := context.WithValue(context.Background(), rawUpdateMapsKey{}, []map[string]any{uraw})
	updated, err := h._doUpdate(uctx, []service.CrudRequest[*bug059VersionedParent]{bug059Req(uraw)}, false)
	if err != nil {
		t.Fatalf("_doUpdate: %v", err)
	}
	if len(updated) == 0 {
		t.Fatal("_doUpdate 未返回结果")
	}
	v2 := (*updated[0]).ParentULID
	if v2 == "" || v2 == v1 {
		t.Fatalf("版本化 update 应产生新版本 ULID: v1=%s v2=%s", v1, v2)
	}

	// ① v2 子表复制重建（新 ULID）
	var v2Fields []asmField
	db.Where("parent_ulid = ? AND is_deleted = 0", v2).Find(&v2Fields)
	if len(v2Fields) != 1 {
		t.Fatalf("v2 应有 1 个字段（回填后重建）, 实际 %d", len(v2Fields))
	}
	if v2Fields[0].ULID == v1Fields[0].ULID {
		t.Fatal("v2 的子行必须是新 ULID（复制重建），不得原地复用 v1 的行")
	}

	// ② v2 的引用指向 v2 的发布方（跨版本悬挂引用的守卫）
	var v2Cols []asmColumn
	db.Where("parent_ulid = ? AND is_deleted = 0", v2).Find(&v2Cols)
	if len(v2Cols) != 1 {
		t.Fatalf("v2 应有 1 个列, 实际 %d", len(v2Cols))
	}
	if v2Cols[0].FieldULID != v2Fields[0].ULID {
		t.Fatalf("★ v2 的引用必须指向 v2 的新字段 ULID：got=%s want=%s（v1 的字段=%s）",
			v2Cols[0].FieldULID, v2Fields[0].ULID, v1Fields[0].ULID)
	}

	// ③ v1 快照保持原样（旧版本的引用不被改写 —— 快照语义）
	var v1FieldsAfter []asmField
	db.Where("parent_ulid = ?", v1).Find(&v1FieldsAfter)
	if len(v1FieldsAfter) != 1 || v1FieldsAfter[0].ULID != v1Fields[0].ULID {
		t.Fatalf("v1 子表快照不应被改写: %+v", v1FieldsAfter)
	}
	var v1ColsAfter []asmColumn
	db.Where("parent_ulid = ?", v1).Find(&v1ColsAfter)
	if len(v1ColsAfter) != 1 || v1ColsAfter[0].FieldULID != v1Fields[0].ULID {
		t.Fatalf("v1 的引用应保持指向 v1 字段: %+v", v1ColsAfter)
	}
}

// buildAsmVersionedParentHandler 版本化父 + v3 装配（发布方 asm_field / 消费方 asm_column）。
//
// 复用 bug059 的版本化父实体（其 VersionFields 已覆盖 code/version/current/status）。
func buildAsmVersionedParentHandler(t *testing.T, db *gorm.DB) *GenericHandler[*bug059VersionedParent] {
	t.Helper()

	fieldRepo := repository.NewCRUDWithDB[*asmField](db)
	fieldH := NewGenericHandlerWithSvc[*asmField](
		service.NewGenericService[*asmField](fieldRepo, service.Config[*asmField]{EntityName: "asm_field"}),
		"asm_field", HandlerConfig[*asmField]{PathPrefix: "/asm/field"})
	colRepo := repository.NewCRUDWithDB[*asmColumn](db)
	colH := NewGenericHandlerWithSvc[*asmColumn](
		service.NewGenericService[*asmColumn](colRepo, service.Config[*asmColumn]{EntityName: "asm_column"}),
		"asm_column", HandlerConfig[*asmColumn]{PathPrefix: "/asm/column"})

	reg := NewHandlerRegistry()
	reg.Register("asm_field", fieldH)
	reg.Register("asm_column", colH)

	parentSvc := service.NewGenericService[*bug059VersionedParent](
		repository.NewCRUDWithDB[*bug059VersionedParent](db),
		service.Config[*bug059VersionedParent]{
			EntityName:  "asm_versioned_parent",
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

	rel := func() []CascadeRelation {
		return []CascadeRelation{
			{
				HandlerName: "asm_field", ChildrenField: "fields", FKField: "parent_ulid",
				OnCreate: true, OnUpdate: true, Target: asmKey,
			},
			{
				HandlerName: "asm_column", ChildrenField: "columns", FKField: "parent_ulid",
				OnCreate: true, OnUpdate: true,
				Assemblies: []ReferenceAssembly{{
					Source: "field_ulid", Target: asmKey,
					Match:  map[string]string{"field_code": "field_code"},
					Assign: map[string]string{"field_ulid": "ulid"},
				}},
			},
		}
	}()

	parentH := NewGenericHandlerWithSvc[*bug059VersionedParent](parentSvc, "asm_versioned_parent",
		HandlerConfig[*bug059VersionedParent]{PathPrefix: "/asm/vparent", Cascades: rel})
	parentH.SetHandlerReg(reg)
	parentH.SetTxCoord(NewTxCoordinator(db, nil).SetRetryOnPKConflict(true))
	return parentH
}

// asmCreateVersioned 走一次完整 _doCreate（版本化父），返回新版本 ULID。
func asmCreateVersioned(t *testing.T, h *GenericHandler[*bug059VersionedParent], raw map[string]any) string {
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

// §9.2 L1 校验：Target 找不到声明方 → 聚合报告。
func TestValidateAssembliesInReportsMissingTarget(t *testing.T) {
	db := openAsmDB(t)
	// 消费方声明 Target，但没有对应的 RemapKey 声明方
	h := buildAsmParentHandler(t, db,
		CascadeRelation{
			HandlerName: "asm_column", ChildrenField: "columns", FKField: "parent_ulid",
			OnCreate: true, OnUpdate: true,
			Assemblies: []ReferenceAssembly{{
				Source: "field_ulid", Target: "nowhere.declared",
				Match:  map[string]string{"field_code": "field_code"},
				Assign: map[string]string{"field_ulid": "ulid"},
			}},
		},
	)
	err := h.ValidateAssemblies()
	if err == nil {
		t.Fatal("Target 无声明方应报错")
	}
	if !strings.Contains(err.Error(), "nowhere.declared") {
		t.Fatalf("报告应含缺失的 Target: %v", err)
	}
}

// L1 校验：声明与消费齐备 → 通过。
func TestValidateAssembliesInPasses(t *testing.T) {
	db := openAsmDB(t)
	h := buildAsmParentHandler(t, db)
	if err := h.ValidateAssemblies(); err != nil {
		t.Fatalf("齐备的声明应通过校验: %v", err)
	}
}

// 确保未使用的导入被引用。
var (
	_ = repository.NewCRUDWithDB[*asmParent]
	_ = service.Config[*asmParent]{}
)
