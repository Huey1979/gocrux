package handler

import "strings"

// pruneFields 按 fields 规则裁剪 map 数据。
// 规则语法：
//   key           → 保留下级全部
//   key:sub       → 保留下级的部分（单个 key）
//   key:[a,b]     → 保留下级的部分（多个 key）
// 子规则用 ; 分隔。如: "form_code;form_name;write_section:section_name"
func pruneFields(data map[string]any, fields string) map[string]any {
	if fields == "" {
		return data
	}
	rules := splitRules(fields)
	if len(rules) == 0 {
		return data
	}
	out := make(map[string]any, len(rules))
	for _, r := range rules {
		key, subs := parseRule(r)
		val, ok := data[key]
		if !ok {
			continue
		}

		if len(subs) == 0 {
			// key → 保留下级全部
			out[key] = val
		} else if arr, ok := val.([]any); ok {
			// key:subs → 数组每元素裁剪
			if len(arr) == 0 {
				continue
			}
			filtered := make([]any, 0, len(arr))
			for _, item := range arr {
				if m, ok := item.(map[string]any); ok {
					sub := keepKeys(m, subs)
					if len(sub) > 0 {
						filtered = append(filtered, sub)
					}
				}
			}
			if len(filtered) > 0 {
				out[key] = filtered
			}
		} else if m, ok := val.(map[string]any); ok {
			// key:subs → 对象裁剪
			sub := keepKeys(m, subs)
			if len(sub) > 0 {
				out[key] = sub
			}
		}
	}
	return out
}

// splitRules 按 ; 分割规则，过滤空串。
func splitRules(fields string) []string {
	parts := strings.Split(fields, ";")
	rules := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			rules = append(rules, p)
		}
	}
	return rules
}

// parseRule 解析单条规则 "key:subs" 或 "key:[a,b]" 或 "key"。
// 返回 key 和下级的子 key 列表（nil=全部保留）。
func parseRule(rule string) (string, []string) {
	idx := strings.IndexByte(rule, ':')
	if idx < 0 {
		return rule, nil
	}
	key := strings.TrimSpace(rule[:idx])
	subStr := strings.TrimSpace(rule[idx+1:])

	// key:[a,b] 格式
	if len(subStr) > 2 && subStr[0] == '[' && subStr[len(subStr)-1] == ']' {
		inner := subStr[1 : len(subStr)-1]
		subs := strings.Split(inner, ",")
		for i := range subs {
			subs[i] = strings.TrimSpace(subs[i])
		}
		return key, subs
	}

	// key:sub 单个 key
	return key, []string{subStr}
}

// keepKeys 从 map 中仅保留指定 key。
func keepKeys(m map[string]any, keys []string) map[string]any {
	out := make(map[string]any, len(keys))
	for _, k := range keys {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}
