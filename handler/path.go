package handler

import "strings"

// setByPath 按点分路径在 map 中设置值，自动创建中间 map。
// 如 setByPath(m, "fields.dept_id", "01KW...") → m["fields"]["dept_id"] = "01KW..."
func setByPath(m map[string]any, path string, val any) {
	if m == nil || path == "" {
		return
	}
	parts := strings.Split(path, ".")
	if len(parts) == 1 {
		m[parts[0]] = val
		return
	}
	for i := 0; i < len(parts)-1; i++ {
		sub, ok := m[parts[i]]
		if !ok {
			subMap := make(map[string]any)
			m[parts[i]] = subMap
			m = subMap
		} else if sm, ok := sub.(map[string]any); ok {
			m = sm
		} else {
			return // 路径上有非 map 值，终止
		}
	}
	m[parts[len(parts)-1]] = val
}

// getByPath 按点分路径从 map 中读取值。
// 如 getByPath(m, "fields.dept_id") → m["fields"]["dept_id"]
// 任意一段不存在或非 map 则返回 nil, false。
func getByPath(m map[string]any, path string) (any, bool) {
	if m == nil || path == "" {
		return nil, false
	}
	parts := strings.Split(path, ".")
	if len(parts) == 1 {
		v, ok := m[parts[0]]
		return v, ok
	}
	for i := 0; i < len(parts)-1; i++ {
		sub, ok := m[parts[i]]
		if !ok {
			return nil, false
		}
		if sm, ok := sub.(map[string]any); ok {
			m = sm
		} else {
			return nil, false
		}
	}
	v, ok := m[parts[len(parts)-1]]
	return v, ok
}
