// BUG-075 回归测试：goKindToRuleType 把 struct/slice/map 兜底成 type="string"，
// 在 handler 层原地改写非 scalar 字段的写入契约。
//
// 缺陷链（三跳，全部在 handler/）：
//  1. goKindToRuleType 对 struct/slice/map 落 default → "string"；
//  2. coerceToString 把原生 JSON 对象/数组序列化成字符串；
//  3. mergeByJSON 再 json.Unmarshal 回结构化字段必炸 →
//     `json: cannot unmarshal string into Go struct field ...` → 4001。
// 结果：实体只要没有自定义解析钩子，客户端提交合法嵌套 JSON 就落不了库。
//
// 修复（方案 A）：收窄兜底 —— struct/slice/array/map 返回 "any"（透传，交由
// encoding/json 按目标类型解码），channel/func 等仍保留 "string"。
//
// 本文件覆盖：①~③ 结构化字段可写通；④ BUG-055 行为不回归（Go string + type:json
// 的字段仍被字符串化）；⑤ 长度/枚举等派生规则不受影响；⑥ 反向验证锚点。
package handler

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// bug075Cfg 嵌套配置结构（模拟 heims DocCentral.Permission）。
type bug075Cfg struct {
	AllowMode string `json:"allow_mode"`
	CondULID  string `json:"access_condition_ulid"`
}

// bug075Doc 非 scalar 字段测试实体。
type bug075Doc struct {
	ULID     string            `gorm:"column:ulid;primaryKey;size:26" json:"ulid"`
	Name     string            `gorm:"column:name;size:100" json:"name"`
	Perm     *bug075Cfg        `gorm:"column:permission" json:"permission"`      // struct 指针
	Tags     []string          `gorm:"column:tags" json:"tags"`                  // slice
	Raw      map[string]any    `gorm:"column:raw" json:"raw"`                    // map
	Created  time.Time         `gorm:"column:created_at" json:"created_at"`      // struct（time.Time）
	JSONStr  string            `gorm:"column:json_str;type:json" json:"json_str"` // Go string + type:json（BUG-055 路径）
	RawBytes json.RawMessage   `gorm:"column:raw_bytes" json:"raw_bytes"`        // 显式 format=json 分支
}

// TestBug075GoKindToRuleTypeStructured 用例 ①~③：
// struct / slice / array / map 必须返回 "any"，不再兜底成 "string"。
func TestBug075GoKindToRuleTypeStructured(t *testing.T) {
	cases := []struct {
		name string
		typ  reflect.Type
		want string
	}{
		{"struct", reflect.TypeOf(bug075Cfg{}), "any"},
		{"struct pointer", reflect.TypeOf(&bug075Cfg{}), "any"},
		{"slice", reflect.TypeOf([]string{}), "any"},
		{"array", reflect.TypeOf([3]string{}), "any"},
		{"map", reflect.TypeOf(map[string]any{}), "any"},
		{"time.Time", reflect.TypeOf(time.Time{}), "any"},
		// 既有标量行为不回归
		{"string", reflect.TypeOf(""), "string"},
		{"int", reflect.TypeOf(0), "int"},
		{"float64", reflect.TypeOf(0.0), "float"},
		{"bool", reflect.TypeOf(false), "bool"},
		{"interface{}", reflect.TypeOf((*any)(nil)).Elem(), "any"},
	}
	for _, c := range cases {
		if got := goKindToRuleType(c.typ); got != c.want {
			t.Errorf("BUG-075: goKindToRuleType(%s) = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestBug075CoerceValuePassesStructuredThrough 用例：
// type="any" 时 coerceValue 原样透传，不做任何字符串化。
func TestBug075CoerceValuePassesStructuredThrough(t *testing.T) {
	nested := map[string]any{"read": map[string]any{"allow_mode": "all"}}
	got, err := coerceValue("permission", "any", nested)
	if err != nil {
		t.Fatalf("coerceValue(any): %v", err)
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("BUG-075: structured value must pass through unchanged, got %T", got)
	}
	if _, ok := m["read"].(map[string]any); !ok {
		t.Errorf("nested structure must be preserved, got %#v", m)
	}
}

// TestBug075StructuredFieldsSurviveMergeTo 用例 ①~③（端到端）：
// 客户端提交合法嵌套 JSON → 校验通过 → mergeByJSON 能解码进结构化字段。
func TestBug075StructuredFieldsSurviveMergeTo(t *testing.T) {
	raw := map[string]any{
		"permission": map[string]any{"allow_mode": "all", "access_condition_ulid": ""},
		"tags":       []any{"a", "b"},
		"raw":        map[string]any{"k": "v"},
		"json_str":   map[string]any{"nested": 1}, // Go string + type:json → 仍应被字符串化
	}

	rules := deriveFieldRules[*bug075Doc]()
	if err := validateInput(rules, raw, "create", false); err != nil {
		t.Fatalf("BUG-075: structured payload must pass validation, got %v", err)
	}

	// 合并进实体（修复前此处会因权限字段被字符串化而失败）
	target := &bug075Doc{}
	if err := mergeByJSON(raw, &target, false); err != nil {
		t.Fatalf("BUG-075: mergeByJSON must accept structured fields, got %v", err)
	}

	if target.Perm == nil {
		t.Fatal("BUG-075: permission must decode into *bug075Cfg")
	}
	if target.Perm.AllowMode != "all" {
		t.Errorf("permission.allow_mode = %q, want all", target.Perm.AllowMode)
	}
	if len(target.Tags) != 2 || target.Tags[0] != "a" || target.Tags[1] != "b" {
		t.Errorf("tags = %#v, want [a b]", target.Tags)
	}
	if target.Raw["k"] != "v" {
		t.Errorf("raw = %#v, want map[k:v]", target.Raw)
	}
	// BUG-055 行为不回归：Go string 字段仍拿到 JSON 字符串
	if target.JSONStr != `{"nested":1}` {
		t.Errorf("BUG-055 must not regress: json_str = %q, want {\"nested\":1}", target.JSONStr)
	}
}

// TestBug075EmptyContainersStillNormalized 用例：
// 空容器不被 isScalarEmpty 提前放行，且结构化字段可正常接收（与 BUG-064 联动）。
func TestBug075EmptyContainersStillNormalized(t *testing.T) {
	raw := map[string]any{
		"tags":     []any{},
		"raw":      map[string]any{},
		"json_str": []any{},
	}
	rules := deriveFieldRules[*bug075Doc]()
	if err := validateInput(rules, raw, "create", false); err != nil {
		t.Fatalf("empty containers must pass validation, got %v", err)
	}

	target := &bug075Doc{}
	if err := mergeByJSON(raw, &target, false); err != nil {
		t.Fatalf("BUG-075: empty containers must still merge, got %v", err)
	}
	if target.Tags == nil || len(target.Tags) != 0 {
		t.Errorf("tags = %#v, want empty non-nil slice", target.Tags)
	}
	if target.Raw == nil || len(target.Raw) != 0 {
		t.Errorf("raw = %#v, want empty non-nil map", target.Raw)
	}
	// json_str（Go string + type:json）：空数组仍被归一化为 "[]"（BUG-064 语义）
	if target.JSONStr != "[]" {
		t.Errorf("BUG-064 must not regress: json_str = %q, want []", target.JSONStr)
	}
}

// TestBug075JSONRawMessageBranchUnchanged 用例（报告 §四 附带请教，保持现状）：
// json.RawMessage 走显式分支（Type=string + Format=json），仍做字符串化 ——
// 本次修复只收窄 default 兜底，不改变该显式分支。
func TestBug075JSONRawMessageBranchUnchanged(t *testing.T) {
	rules := deriveFieldRules[*bug075Doc]()
	rule := rules["raw_bytes"]
	if rule == nil {
		t.Skip("raw_bytes rule not derived (column tag missing)")
	}
	if rule.Type != "string" || rule.Format != "json" {
		t.Errorf("json.RawMessage branch must stay intact, got type=%q format=%q", rule.Type, rule.Format)
	}
}

// TestBug075StringContractNotAffected 用例：Go string 字段的类型推导仍是 "string"，
// 长度/枚举等派生规则不受收窄影响。
func TestBug075StringContractNotAffected(t *testing.T) {
	rules := deriveFieldRules[*bug075Doc]()
	name := rules["name"]
	if name == nil {
		t.Fatal("name rule must be derived")
	}
	if name.Type != "string" {
		t.Errorf("string field type = %q, want string", name.Type)
	}
	if name.MaxLength == nil || *name.MaxLength != 100 {
		t.Errorf("string field must keep gorm size→MaxLength, got %v", name.MaxLength)
	}
}
