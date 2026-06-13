package expression

import (
	"encoding/json"
	"sort"
	"strings"
)

// commutative 参数顺序不影响结果的函数。
var commutative = map[string]bool{
	"And": true, "Or": true,
	"Add": true, "Mul": true, "Concat": true,
	"Eq": true, "Neq": true,
	"Overlap": true,
}

// Normalize 将任意格式的表达式 JSON 转为规范形式。
// 相同语义的表达式总是产生相同的输出，用于缓存键和等值比较。
func Normalize(raw string) (string, error) {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return "", err
	}
	normalized := normalizeValue(v)
	b, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// normalizeValue 对任意 JSON 值做规范化。
func normalizeValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		return normalizeExpr(val)
	case []any:
		result := make([]any, len(val))
		for i, item := range val {
			result[i] = normalizeValue(item)
		}
		return result
	default:
		return v
	}
}

// normalizeExpr 规范化一个表达式节点（单函数调用）。
// 表达式格式为 {"FunctionName": [params]}。
// 规范化包括：key 排序、交换律参数排序、Eq 参数定序。
func normalizeExpr(expr map[string]any) map[string]any {
	keys := sortedKeys(expr)
	if len(keys) == 1 {
		funcName := keys[0]
		params := expr[funcName]

		if arr, ok := params.([]any); ok {
			// 递归规范化每个参数
			normParams := make([]any, len(arr))
			for i, p := range arr {
				normParams[i] = normalizeValue(p)
			}

			// 交换律函数：按规范 JSON 排序参数
			if commutative[funcName] {
				sort.Slice(normParams, func(i, j int) bool {
					return jsonString(normParams[i]) < jsonString(normParams[j])
				})
			}

			// Eq / Neq：变量在左，字面量在右
			if (funcName == "Eq" || funcName == "Neq") && len(normParams) == 2 {
				if !isVariable(normParams[0]) && isVariable(normParams[1]) {
					normParams[0], normParams[1] = normParams[1], normParams[0]
				}
			}

			return map[string]any{funcName: normParams}
		}
	}

	// 多 key 的普通 JSON 对象：递归规范化值，按 key 排序输出
	result := make(map[string]any, len(expr))
	for _, k := range sortedKeys(expr) {
		result[k] = normalizeValue(expr[k])
	}
	return result
}

// sortedKeys 返回 map 的 key 按字母序排列。
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// jsonString 返回值的规范 JSON 字符串，用于排序比较。
func jsonString(v any) string {
	n := normalizeValue(v)
	b, _ := json.Marshal(n)
	return string(b)
}

// isVariable 判断值是否为表达式变量（${xxx} 或 $varname 格式）。
func isVariable(v any) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	return strings.HasPrefix(s, "${") || strings.HasPrefix(s, "$")
}

// IsValid 检查表达式 JSON 格式是否合法。
func IsValid(raw string) bool {
	var v any
	return json.Unmarshal([]byte(raw), &v) == nil
}
