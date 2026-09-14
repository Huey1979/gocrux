// BUG-073 回归测试：Mongo 侧 OpRange 把整个区间数组同时当成上下界，
// 时间范围筛选静默返回空集。
//
// 缺陷：`case OpRange: return bson.M{f.Field: bson.M{"$gte": f.Value, "$lte": f.Value}}`
// —— f.Value 是 []any{lo, hi} 整体，生成「字段 ≥ 数组 且 字段 ≤ 同一数组」。
// 按 BSON type ordering，Date 恒小于 Array ⇒ 条件恒假 ⇒ 永远空集、零错误信号。
//
// 修复：拆上下界；只给一侧时生成单条件；形态非法时返回明确不匹配的条件
// （而不是静默退化成「不过滤」）。
//
// 本文件为纯函数测试（不需要 Mongo 实例）。
package repository

import (
	"reflect"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

// TestBug073SplitRangeValue 覆盖拆分函数的各种入参形态。
func TestBug073SplitRangeValue(t *testing.T) {
	cases := []struct {
		name   string
		in     any
		wantLo any
		wantHi any
	}{
		{"two element []any", []any{"2026-01-01", "2026-12-31"}, "2026-01-01", "2026-12-31"},
		{"two element []string", []string{"a", "b"}, "a", "b"},
		{"comma string", "lo,hi", "lo", "hi"},
		{"lower only", "lo,", "lo", nil},
		{"upper only", ",hi", nil, "hi"},
		{"whitespace-only upper bound treated as absent", "lo,   ", "lo", nil},
		{"single element slice", []any{"only"}, "only", nil},
		{"empty slice", []any{}, nil, nil},
		{"single scalar", "only", nil, nil},
		{"empty string", "", nil, nil},
	}
	for _, c := range cases {
		lo, hi := splitRangeValue(c.in)
		if !reflect.DeepEqual(lo, c.wantLo) || !reflect.DeepEqual(hi, c.wantHi) {
			t.Errorf("BUG-073: splitRangeValue(%s) = (%v, %v), want (%v, %v)",
				c.name, lo, hi, c.wantLo, c.wantHi)
		}
	}
}

// TestBug073FilterToBsonRangeSplitsBounds 核心回归：
// OpRange 必须生成两个**不同的**界，而不是把整个数组塞给两边。
func TestBug073FilterToBsonRangeSplitsBounds(t *testing.T) {
	got := filterToBson(Filter{
		Field: "published_at",
		Op:    OpRange,
		Value: []any{"2026-01-01 00:00:00", "2026-12-31 23:59:59"},
	})

	cond, ok := got["published_at"].(bson.M)
	if !ok {
		t.Fatalf("BUG-073: expected bson.M under field key, got %#v", got)
	}
	if cond["$gte"] != "2026-01-01 00:00:00" {
		t.Errorf("$gte = %#v, want 2026-01-01 00:00:00", cond["$gte"])
	}
	if cond["$lte"] != "2026-12-31 23:59:59" {
		t.Errorf("$lte = %#v, want 2026-12-31 23:59:59", cond["$lte"])
	}
	// 关键断言：不得把整个数组塞进去（修复前 $gte == $lte == []any{lo,hi}）
	if _, isSlice := cond["$gte"].([]any); isSlice {
		t.Error("BUG-073: $gte must not receive the whole range array")
	}
}

// TestBug073FilterToBsonRangeOpenEnded 单侧区间：
// between=a, 只生成 $gte；between=,b 只生成 $lte（报告 §6.3 建议）。
func TestBug073FilterToBsonRangeOpenEnded(t *testing.T) {
	lowerOnly := filterToBson(Filter{Field: "t", Op: OpRange, Value: "2026-01-01,"})
	cond, _ := lowerOnly["t"].(bson.M)
	if cond == nil || cond["$gte"] != "2026-01-01" {
		t.Errorf("lower-only range: got %#v", lowerOnly)
	}
	if _, has := cond["$lte"]; has {
		t.Error("lower-only range must not emit $lte")
	}

	upperOnly := filterToBson(Filter{Field: "t", Op: OpRange, Value: ",2026-12-31"})
	cond2, _ := upperOnly["t"].(bson.M)
	if cond2 == nil || cond2["$lte"] != "2026-12-31" {
		t.Errorf("upper-only range: got %#v", upperOnly)
	}
	if _, has := cond2["$gte"]; has {
		t.Error("upper-only range must not emit $gte")
	}
}

// TestBug073FilterToBsonInvalidRangeDoesNotDegradeSilently 报告 §6.1 关键要求：
// 完全无法解释的入参（空值 / 单值字符串 / 空切片）必须返回**明确不匹配**的条件，
// 而不是退化成「不过滤返回全量」。
func TestBug073FilterToBsonInvalidRangeDoesNotDegradeSilently(t *testing.T) {
	for _, in := range []any{"single-value", "", []any{}} {
		got := filterToBson(Filter{Field: "t", Op: OpRange, Value: in})
		if _, ok := got["t"]; ok {
			t.Errorf("BUG-073: invalid range %#v must not produce a field condition, got %#v", in, got)
		}
		expr, ok := got["$expr"].(bson.M)
		if !ok {
			t.Fatalf("BUG-073: invalid range %#v must yield an explicit non-matching condition, got %#v", in, got)
		}
		eq, ok := expr["$eq"].([]any)
		if !ok || len(eq) != 2 || eq[0] != "$__invalid_range_filter__" {
			t.Errorf("BUG-073: unexpected non-matching condition %#v", expr)
		}
	}
}

// TestBug073SingleElementSliceIsOpenEnded 单元素切片按「只给下界」处理
// （与 between=a, 一致），不是非法入参。
func TestBug073SingleElementSliceIsOpenEnded(t *testing.T) {
	got := filterToBson(Filter{Field: "t", Op: OpRange, Value: []any{"only-lower"}})
	cond, ok := got["t"].(bson.M)
	if !ok || cond["$gte"] != "only-lower" {
		t.Errorf("single-element range must emit $gte, got %#v", got)
	}
	if _, has := cond["$lte"]; has {
		t.Error("single-element range must not emit $lte")
	}
}

// TestBug073OtherOpsUnchanged 不回归：
// 除 OpRange 外的比较运算仍原样透传（类型归一出在 service 层，见 service/filter_value.go）。
func TestBug073OtherOpsUnchanged(t *testing.T) {
	cases := []struct {
		op   FilterOp
		want string
	}{
		{OpGT, "$gt"}, {OpGTE, "$gte"}, {OpLT, "$lt"}, {OpLTE, "$lte"},
	}
	for _, c := range cases {
		got := filterToBson(Filter{Field: "f", Op: c.op, Value: "v"})
		cond, ok := got["f"].(bson.M)
		if !ok || cond[c.want] != "v" {
			t.Errorf("op %s: got %#v, want %s=v", c.op, got, c.want)
		}
	}
}
