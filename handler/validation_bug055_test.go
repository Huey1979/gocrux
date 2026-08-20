package handler

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Bug055Doc 测试实体：gorm:"type:json" 的 string 字段 + json.RawMessage 字段。
// 字段必须导出（deriveFieldRules 反射遍历需要）。
type Bug055Doc struct {
	ID      string          `gorm:"column:id;primaryKey"`
	Depts   string          `gorm:"column:depts;type:json"`
	Raw     json.RawMessage `gorm:"column:raw;type:json"`
	Enabled bool            `gorm:"column:enabled"`
}

// TestBug055CoerceToStringJSON 验证 BUG-055 核心修复：
// coerceToString 对 JSON 数组/对象/RawMessage 序列化为 JSON 字符串。
func TestBug055CoerceToStringJSON(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"数组", []any{"01KW_TECH_DEPT"}, `["01KW_TECH_DEPT"]`},
		{"空数组", []any{}, `[]`},
		{"对象", map[string]any{"op": "eq", "value": 1}, `{"op":"eq","value":1}`},
		{"嵌套", []any{map[string]any{"a": []any{1, 2}}}, `[{"a":[1,2]}]`},
		{"RawMessage", json.RawMessage(`{"a":1}`), `{"a":1}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := coerceToString("depts", c.in)
			if err != nil {
				t.Fatalf("coerceToString(%v) error: %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("coerceToString(%v) = %q, want %q", c.in, got, c.want)
			}
			if s, ok := got.(string); !ok || !json.Valid([]byte(s)) {
				t.Errorf("coerceToString(%v) result %v must be valid JSON string", c.in, got)
			}
		})
	}
}

// TestBug055CoerceToStringRawInvalid 验证非法 RawMessage 报错。
func TestBug055CoerceToStringRawInvalid(t *testing.T) {
	if _, err := coerceToString("raw", json.RawMessage(`{invalid`)); err == nil {
		t.Errorf("invalid RawMessage must error")
	}
}

// TestBug055CheckFormatJSON 验证 checkFormat 的 json 分支防御：
// 值仍是未序列化的数组/对象时也能通过校验。
func TestBug055CheckFormatJSON(t *testing.T) {
	// 防御分支：直传原生数组/对象（未经过 coerceToString）
	for _, v := range []any{
		[]any{"01KW_TECH_DEPT"},
		map[string]any{"op": "eq", "value": 1},
		json.RawMessage(`{"a":[1,2]}`),
	} {
		if err := checkFormat("depts", "json", v); err != nil {
			t.Errorf("checkFormat(json, %v) error: %v", v, err)
		}
	}
	// fmt.Sprint 产生的无效 JSON 字符串（[01KW_TECH_DEPT]）必须仍被拒绝
	if err := checkFormat("depts", "json", "[01KW_TECH_DEPT]"); err == nil {
		t.Errorf("checkFormat(json, \"[01KW_TECH_DEPT]\") must error")
	}
}

// TestBug055DeriveAndValidate 端到端：deriveFieldRules 推导 type:json 规则 +
// validateInput 处理原生 JSON 数组，data 值被替换为 JSON 字符串。
func TestBug055DeriveAndValidate(t *testing.T) {
	rules := deriveFieldRules[Bug055Doc]()
	deptRule := rules["depts"]
	if deptRule == nil || deptRule.Type != "string" || deptRule.Format != "json" {
		t.Fatalf("deriveFieldRules depts rule = %+v, want Type=string Format=json", deptRule)
	}
	rawRule := rules["raw"]
	if rawRule == nil || rawRule.Type != "string" || rawRule.Format != "json" {
		t.Fatalf("deriveFieldRules raw rule = %+v, want Type=string Format=json", rawRule)
	}

	data := map[string]any{
		"depts":   []any{"01KW_TECH_DEPT", "02KW_SALES_DEPT"},
		"raw":     json.RawMessage(`{"scope":"dept"}`),
		"enabled": true,
	}
	if err := validateInput(rules, data, "create", false); err != nil {
		t.Fatalf("validateInput error: %v", err)
	}
	if s, ok := data["depts"].(string); !ok || s != `["01KW_TECH_DEPT","02KW_SALES_DEPT"]` {
		t.Errorf("data[depts] = %v (%T), want JSON string", data["depts"], data["depts"])
	}
	if s, ok := data["raw"].(string); !ok || s != `{"scope":"dept"}` {
		t.Errorf("data[raw] = %v (%T), want JSON string", data["raw"], data["raw"])
	}
}

// TestBug055RejectUnknownKeepsJSON 验证 RejectUnknownFields=true 时
// JSON 数组字段不误报（回归：validateInput 对未知字段的遍历不受影响）。
func TestBug055RejectUnknownKeepsJSON(t *testing.T) {
	rules := deriveFieldRules[Bug055Doc]()
	data := map[string]any{"depts": []any{"x"}}
	if err := validateInput(rules, data, "create", true); err != nil {
		t.Fatalf("validateInput error: %v", err)
	}
	if s, ok := data["depts"].(string); !ok || s != `["x"]` {
		t.Errorf("data[depts] = %v (%T), want JSON string", data["depts"], data["depts"])
	}
}

// TestBug055JSONValueValid 验证 jsonValueValid 辅助函数。
func TestBug055JSONValueValid(t *testing.T) {
	valid := []any{
		`{"a":1}`,
		`[1,2]`,
		[]any{1, "x"},
		map[string]any{"a": nil},
		json.RawMessage(`null`),
	}
	for _, v := range valid {
		if !jsonValueValid(v) {
			t.Errorf("jsonValueValid(%v) = false, want true", v)
		}
	}
	invalid := []any{
		`{invalid`,
		`[01KW_TECH_DEPT]`, // fmt.Sprint([]any) 的输出不是合法 JSON
		json.RawMessage(`{bad`),
	}
	for _, v := range invalid {
		if jsonValueValid(v) {
			t.Errorf("jsonValueValid(%v) = true, want false", v)
		}
	}
}

// TestBug055CoerceKeepsScalarTypes 回归：标量类型转换不受影响。
func TestBug055CoerceKeepsScalarTypes(t *testing.T) {
	cases := []struct {
		in   any
		want any
	}{
		{"abc", "abc"},
		{123.0, "123"},
		{123.5, "123.5"},
		{true, "true"},
		{int64(7), "7"},
		{uint64(8), "8"},
	}
	for _, c := range cases {
		got, err := coerceToString("f", c.in)
		if err != nil {
			t.Fatalf("coerceToString(%v) error: %v", c.in, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("coerceToString(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
