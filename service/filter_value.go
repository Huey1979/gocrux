package service

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/Huey1979/gocrux/repository"
)

// ============================================================
// BUG-073 / BUG-078：List 过滤条件的值类型归一化
//
// 背景：List 的过滤值全部来自 URL 查询参数，**类型恒为 string**。
//   - MySQL 侧看不出问题：`WHERE timeout_seconds > '1'`、
//     `WHERE start_time > '2020-01-01T00:00:00Z'` 由数据库做隐式转换；
//   - Mongo 侧则完全失效：BSON 比较遵循 type bracketing，不同类型之间
//     不存在大小关系，`ISODate(...)` 与 `"2020-01-01T00:00:00Z"` 永远比不出结果，
//     于是时间/数值列的 :gt/:lt/:between/:in 恒为空集**且不报错**。
//
// 修复：在 service 层（构造 repository.Filter 之前）按**目标列的 Go 字段类型**
// 把值归一化，MySQL 与 Mongo 拿到的是同一份已归一的 Value，两条路径不再分叉。
// ============================================================

// 可接受的入站时间格式（按顺序尝试）。
// 覆盖 RFC3339（含带/不带毫秒、带时区偏移）与常见的 "2006-01-02 15:04:05" 形态。
var filterTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
	"2006/01/02 15:04:05",
	"2006/01/02",
}

// normalizeFilterValue 按目标列 col 的 Go 字段类型归一化过滤值（BUG-073）。
//
// 处理范围：
//   - time.Time / *time.Time → time.Time（解析失败**保持原样**，不抛错：
//     该字段若真配了非法时间，MySQL 侧同样匹配不到，行为一致且不破坏既有调用方）
//   - int* / uint*           → int64 / uint64
//   - float*                 → float64
//   - bool                   → bool
//   - 其余（string / struct / map 等）→ 原样返回，不做转换
//
// OpRange（between）与 OpIn 是切片值，逐元素归一化后返回同构切片。
func normalizeFilterValue[M Record](col string, op repository.FilterOp, value any) any {
	switch op {
	case repository.OpRange:
		lo, hi := normalizeRangeBounds[M](col, value)
		if lo == nil && hi == nil {
			// 形态非法（单值/空值）：保留原值交给下层，避免把可诊断信息吞掉
			return value
		}
		return []any{lo, hi}
	case repository.OpIn:
		items, ok := toAnySlice(value)
		if !ok {
			return value
		}
		out := make([]any, len(items))
		for i, it := range items {
			out[i] = normalizeScalarFilterValue[M](col, it)
		}
		return out
	default:
		return normalizeScalarFilterValue[M](col, value)
	}
}

// normalizeRangeBounds 把 OpRange 的值拆成上下界（BUG-073 §6.1 的「拆上下界」）。
//
// 支持形态：
//   - []any{"lo", "hi"} / []string{...} → 两个界都归一化
//   - 逗号分隔字符串（理论上 parseCSVValue 已拆过，这里兜底）
//   - between=a,  → 只给下界（hi 为 nil）→ 上层生成 $gte 单条件
//   - between=,b  → 只给上界（lo 为 nil）→ 上层生成 $lte 单条件
//
// 单值（无法拆成两段）时返回 (nil, nil)，由调用方保留原值。
func normalizeRangeBounds[M Record](col string, value any) (lo, hi any) {
	if items, ok := toAnySlice(value); ok {
		switch len(items) {
		case 0:
			return nil, nil
		case 1:
			// 单值：无法确定是上界还是下界，交给调用方处理
			return nil, nil
		default:
			loRaw, hiRaw := items[0], items[1]
			return normalizeOptionalBound[M](col, loRaw), normalizeOptionalBound[M](col, hiRaw)
		}
	}

	s := strings.TrimSpace(fmt.Sprintf("%v", value))
	if s == "" {
		return nil, nil
	}
	parts := strings.SplitN(s, ",", 2)
	if len(parts) < 2 {
		return nil, nil // 单值
	}
	return normalizeOptionalBound[M](col, parts[0]), normalizeOptionalBound[M](col, parts[1])
}

// normalizeOptionalBound 归一化单个区间端点；空串表示「该侧不限制」→ nil。
func normalizeOptionalBound[M Record](col string, raw any) any {
	if raw == nil {
		return nil
	}
	if s, ok := raw.(string); ok && strings.TrimSpace(s) == "" {
		return nil
	}
	return normalizeScalarFilterValue[M](col, raw)
}

// normalizeScalarFilterValue 按列类型归一化单个标量值。
func normalizeScalarFilterValue[M Record](col string, value any) any {
	if value == nil {
		return nil
	}
	kind := columnKindOf[M](col)
	switch kind {
	case reflect.Struct:
		// time.Time（唯一在实体里当标量用的 struct 类型）
		if t, ok := parseFilterTime(value); ok {
			return t
		}
		return value
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if n, ok := parseFilterInt(value); ok {
			return n
		}
		return value
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if n, ok := parseFilterInt(value); ok && n >= 0 {
			return uint64(n)
		}
		return value
	case reflect.Float32, reflect.Float64:
		if f, ok := parseFilterFloat(value); ok {
			return f
		}
		return value
	case reflect.Bool:
		if b, ok := parseFilterBool(value); ok {
			return b
		}
		return value
	default:
		// string / slice / map / pointer-to-struct 等：原样返回
		return value
	}
}

// columnKindOf 返回实体 M 中列 col 对应 Go 字段的 reflect.Kind。
// 解指针（*time.Time → Struct）；取不到时返回 reflect.Invalid（调用方按「原样返回」处理）。
func columnKindOf[M Record](col string) reflect.Kind {
	if col == "" {
		return reflect.Invalid
	}
	goField := resolveColumnFromDB[M](col)
	if goField == "" {
		return reflect.Invalid
	}
	var m M
	t := reflect.TypeOf(m)
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return reflect.Invalid
	}
	sf, ok := t.FieldByName(goField)
	if !ok {
		return reflect.Invalid
	}
	ft := sf.Type
	for ft.Kind() == reflect.Ptr {
		ft = ft.Elem()
	}
	return ft.Kind()
}

// toAnySlice 把任意切片形态转成 []any；非切片返回 ok=false。
func toAnySlice(v any) ([]any, bool) {
	switch s := v.(type) {
	case []any:
		return s, true
	case []string:
		out := make([]any, len(s))
		for i := range s {
			out[i] = s[i]
		}
		return out, true
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, false
	}
	out := make([]any, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		out[i] = rv.Index(i).Interface()
	}
	return out, true
}

// parseFilterTime 尝试把值解析为 time.Time（已是 time.Time 则原样返回）。
func parseFilterTime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, true
	case *time.Time:
		if t == nil {
			return time.Time{}, false
		}
		return *t, true
	}
	s := strings.TrimSpace(fmt.Sprintf("%v", v))
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range filterTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// parseFilterInt 尝试把值解析为 int64（float64 整数形态也接受，JSON 数字走这条）。
func parseFilterInt(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int8:
		return int64(n), true
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint:
		return int64(n), true
	case uint8:
		return int64(n), true
	case uint16:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		return int64(n), true
	case float64:
		if n == float64(int64(n)) {
			return int64(n), true
		}
		return 0, false
	case float32:
		f := float64(n)
		if f == float64(int64(f)) {
			return int64(f), true
		}
		return 0, false
	case bool:
		if n {
			return 1, true
		}
		return 0, true
	}
	if i, err := strconv.ParseInt(strings.TrimSpace(fmt.Sprintf("%v", v)), 10, 64); err == nil {
		return i, true
	}
	return 0, false
}

// parseFilterFloat 尝试把值解析为 float64。
func parseFilterFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		if i, ok := parseFilterInt(v); ok {
			return float64(i), true
		}
		return 0, false
	}
	if f, err := strconv.ParseFloat(strings.TrimSpace(fmt.Sprintf("%v", v)), 64); err == nil {
		return f, true
	}
	return 0, false
}

// parseFilterBool 尝试把值解析为 bool（兼容 "1"/"0"/"true"/"false"）。
func parseFilterBool(v any) (bool, bool) {
	switch b := v.(type) {
	case bool:
		return b, true
	case string:
		switch strings.ToLower(strings.TrimSpace(b)) {
		case "true", "1", "yes":
			return true, true
		case "false", "0", "no", "":
			return false, true
		}
		return false, false
	}
	if i, ok := parseFilterInt(v); ok {
		return i != 0, true
	}
	return false, false
}
