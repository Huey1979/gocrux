package handler

import (
	"encoding/json"
	"strings"
	"testing"
)

// Bug064Doc 测试实体：gorm:"type:json" 的 string 字段（BUG-064 目标形态，
// 即 heims SysFlowNode.Operations 等同款：实体字段是 string、读写形态不对称）。
type Bug064Doc struct {
	ID      string `gorm:"column:id;primaryKey"`
	Depts   string `gorm:"column:depts;type:json"`
	Enabled bool   `gorm:"column:enabled"`
}

// TestBug064EmptyContainerStillCoerced 验证 BUG-064 核心修复：
// 非必填的空容器（[]any{} / map[string]any{}）不再被 isEmpty 提前放行，
// 继续走 coerceValue 归一化为 JSON 字符串；create/update 两条路径共用
// validateInput → validateField，逐一覆盖。
func TestBug064EmptyContainerStillCoerced(t *testing.T) {
	rules := deriveFieldRules[Bug064Doc]()
	if dr := rules["depts"]; dr == nil || dr.Type != "string" || dr.Format != "json" || dr.Required {
		t.Fatalf("deriveFieldRules depts rule = %+v, want Type=string Format=json Required=false", rules["depts"])
	}

	cases := []struct {
		name string
		in   any
		want string
	}{
		{"空数组", []any{}, "[]"},
		{"空对象", map[string]any{}, "{}"},
		{"非空数组", []any{1, 2}, "[1,2]"},
		{"非空对象", map[string]any{"op": "eq", "value": 1}, `{"op":"eq","value":1}`},
		{"JSON字符串", "[]", "[]"}, // 字符串原样透传，形态不受影响
	}
	for _, endpoint := range []string{"create", "update"} {
		for _, c := range cases {
			t.Run(endpoint+"/"+c.name, func(t *testing.T) {
				data := map[string]any{"depts": c.in}
				if err := validateInput(rules, data, endpoint, false); err != nil {
					t.Fatalf("validateInput(%s) error: %v", endpoint, err)
				}
				got, ok := data["depts"].(string)
				if !ok {
					t.Fatalf("data[depts] = %v (%T), want normalized string", data["depts"], data["depts"])
				}
				if got != c.want {
					t.Errorf("data[depts] = %q, want %q", got, c.want)
				}
			})
		}
	}
}

// TestBug064RequiredEmptyArrayStillReportsMissing 兼容验证：
// required 字段传空数组仍报「必填」——修复只放行非必填容器继续归一化，
// 不能因此让 required 校验失效。
func TestBug064RequiredEmptyArrayStillReportsMissing(t *testing.T) {
	rules := deriveFieldRules[Bug064Doc]()
	rules["depts"].Required = true // 模拟用户配置 required

	// 空数组 → 仍报「必填」（BUG-064 兼容验证点）
	t.Run("空数组", func(t *testing.T) {
		err := validateInput(rules, map[string]any{"depts": []any{}}, "create", false)
		if err == nil || !strings.Contains(err.Error(), "不能为空") {
			t.Errorf("validateInput required depts=[] error = %v, want 不能为空", err)
		}
	})
	// 空字符串 → 仍报「必填」
	t.Run("空字符串", func(t *testing.T) {
		err := validateInput(rules, map[string]any{"depts": ""}, "create", false)
		if err == nil || !strings.Contains(err.Error(), "不能为空") {
			t.Errorf("validateInput required depts=\"\" error = %v, want 不能为空", err)
		}
	})
	// 空对象 → 放行并归一化为 "{}"（isEmpty 对 map 判非空的既有语义，见 TestBug064IsEmptyContainerSemantics）
	t.Run("空对象", func(t *testing.T) {
		data := map[string]any{"depts": map[string]any{}}
		if err := validateInput(rules, data, "create", false); err != nil {
			t.Errorf("validateInput required depts={} error: %v", err)
		}
		if s, ok := data["depts"].(string); !ok || s != "{}" {
			t.Errorf("data[depts] = %v (%T), want \"{}\"", data["depts"], data["depts"])
		}
	})
	// required + 非空容器 → 通过并归一化
	t.Run("非空容器", func(t *testing.T) {
		data := map[string]any{"depts": []any{"x"}}
		if err := validateInput(rules, data, "create", false); err != nil {
			t.Errorf("validateInput required depts=non-empty error: %v", err)
		}
		if s, ok := data["depts"].(string); !ok || s != `["x"]` {
			t.Errorf("data[depts] = %v (%T), want [\"x\"]", data["depts"], data["depts"])
		}
	})
}

// TestBug064ScalarEmptyStillSkipped 回归：非必填标量空值（空字符串）仍被跳过、不报错。
func TestBug064ScalarEmptyStillSkipped(t *testing.T) {
	rules := deriveFieldRules[Bug064Doc]()
	data := map[string]any{"depts": ""}
	if err := validateInput(rules, data, "create", false); err != nil {
		t.Fatalf("validateInput error: %v", err)
	}
	if s, ok := data["depts"].(string); !ok || s != "" {
		t.Errorf("data[depts] = %v (%T), want unchanged empty string", data["depts"], data["depts"])
	}
}

// TestBug064IsScalarEmpty 纯函数验证：容器类型（含空容器）一律 false，
// 只有 nil/空字符串判 true。
func TestBug064IsScalarEmpty(t *testing.T) {
	for _, v := range []any{nil, ""} {
		if !isScalarEmpty(v) {
			t.Errorf("isScalarEmpty(%v) = false, want true", v)
		}
	}
	for _, v := range []any{
		[]any{},
		map[string]any{},
		json.RawMessage{},
		[]any{"x"},
		map[string]any{"a": 1},
		json.RawMessage(`[]`),
		"x",
		0,
		false,
	} {
		if isScalarEmpty(v) {
			t.Errorf("isScalarEmpty(%v) = true, want false", v)
		}
	}
}

// TestBug064IsEmptyContainerSemantics 记录 isEmpty 的既有语义（required 判空用）：
// 空数组判空、空 map 判非空（default 巧合正确）、空 RawMessage 判非空，
// 避免后续修改 isEmpty 时发生语义漂移。
func TestBug064IsEmptyContainerSemantics(t *testing.T) {
	if !isEmpty([]any{}) {
		t.Errorf("isEmpty([]any{}) = false, want true")
	}
	if isEmpty(map[string]any{}) {
		t.Errorf("isEmpty(map[string]any{}) = true, want false")
	}
	if isEmpty(json.RawMessage{}) {
		t.Errorf("isEmpty(json.RawMessage{}) = true, want false")
	}
}
