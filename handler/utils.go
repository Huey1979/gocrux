package handler

import (
	"reflect"
	"strings"
)

// ============================================================
// extractPKFromResult — 从实体提取主键值（供级联返回用）
// ============================================================

// extractPKFromResult 从实体指针中提取主键值。
// 优先级：GetULID() > PKField() 反射匹配 > ULID 后缀字段 > 回退字段。
func extractPKFromResult(v any) any {
	if v == nil {
		return nil
	}

	// 解引用至非 nil 底层（处理 **T / ***T 等情况），保留最内层非指针值用于接口检测
	rv := reflect.ValueOf(v)
	var ptrForIface any // 最内层指针（用于接口断言）
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return nil
		}
		ptrForIface = rv.Interface()
		rv = rv.Elem()
	}

	// 1. 尝试 GetULID 接口
	type hasULID interface{ GetULID() string }
	if u, ok := ptrForIface.(hasULID); ok {
		return u.GetULID()
	}

	// 反正射拿到 struct
	val := rv
	if val.Kind() != reflect.Struct {
		return nil
	}
	t := val.Type()

	// 2. 尝试 PKField() 接口 → 反射匹配字段名
	type hasPK interface{ PKField() string }
	if pk, ok := ptrForIface.(hasPK); ok {
		pkName := pk.PKField()
		for i := 0; i < val.NumField(); i++ {
			if equalFieldName(t.Field(i), pkName) {
				return val.Field(i).Interface()
			}
		}
	}

	// 3. 反射查找以 ULID 结尾的字段
	for i := 0; i < val.NumField(); i++ {
		if strings.HasSuffix(t.Field(i).Name, "ULID") {
			return val.Field(i).Interface()
		}
	}

	// 4. 回退：常见 PK 字段
	for i := 0; i < val.NumField(); i++ {
		name := t.Field(i).Name
		if name == "ID" || strings.HasSuffix(name, "ID") || strings.HasSuffix(name, "Id") {
			return val.Field(i).Interface()
		}
	}
	return nil
}

// equalFieldName 比较 Go 字段名是否匹配 JSON/gorm column 名。
// 支持 "entity_id" 匹配 "EntityID"、"form_ulid" 匹配 "FormULID"、"layout_ulid" 匹配 "LayoutULID"。
func equalFieldName(f reflect.StructField, target string) bool {
	if strings.EqualFold(f.Name, target) {
		return true
	}
	// gorm column tag
	if col, ok := f.Tag.Lookup("gorm"); ok {
		for _, seg := range strings.Split(col, ";") {
			if strings.HasPrefix(strings.TrimSpace(seg), "column:") {
				if strings.TrimSpace(seg[7:]) == target {
					return true
				}
			}
		}
	}
	// json tag
	if js, ok := f.Tag.Lookup("json"); ok {
		if name := strings.Split(js, ",")[0]; strings.TrimSpace(name) == target {
			return true
		}
	}
	return false
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
