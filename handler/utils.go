package handler

import (
	"reflect"
	"strings"
)

// ============================================================
// extractPKFromResult — 从实体提取主键值（供级联返回用）
// ============================================================

// extractPKFromResult 从实体指针中提取主键值。
// 优先尝试 GetULID 接口，否则用反射查找以 ULID 结尾的字段。
func extractPKFromResult(v any) any {
	if v == nil {
		return nil
	}

	// 1. 尝试 GetULID 接口
	type hasULID interface{ GetULID() string }
	if u, ok := v.(hasULID); ok {
		return u.GetULID()
	}

	// 2. 反射查找以 ULID 结尾的字段
	val := reflect.ValueOf(v)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct {
		return nil
	}
	for i := 0; i < val.NumField(); i++ {
		if strings.HasSuffix(val.Type().Field(i).Name, "ULID") {
			return val.Field(i).Interface()
		}
	}
	return nil
}

// extractMapID 从 map 中提取记录 ID（按常见字段名优先级查找）。
func extractMapID(m map[string]any) any {
	if v, ok := m["id"]; ok {
		return v
	}
	if v, ok := m["ID"]; ok {
		return v
	}
	// 查找以 ULID / ulid 结尾的字段
	for k, v := range m {
		if strings.HasSuffix(k, "ulid") || strings.HasSuffix(k, "ULID") {
			return v
		}
	}
	return nil
}

// removeMapID 从 map 中移除所有主键相关字段（id / ID / *_ulid / *_ULID）。
// 用于 DoCreate 前剥离回填旧数据中的 ID，避免主键冲突。
func removeMapID(m map[string]any) {
	delete(m, "id")
	delete(m, "ID")
	for k := range m {
		if strings.HasSuffix(k, "ulid") || strings.HasSuffix(k, "ULID") {
			delete(m, k)
		}
	}
}
