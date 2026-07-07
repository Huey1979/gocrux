package common

import (
	"reflect"
	"strings"
	"sync"
)

// ParseInt 字符串转 int，非数字字符截断返回（不报错）。
// 如 "123abc" → 123, "abc" → 0。
func ParseInt(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n, nil
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// ToSnakeCase 驼峰命名 → 下划线命名（snake_case）。
// 如 "SiteCode" → "site_code", "EntityID" → "entity_id"。
func ToSnakeCase(s string) string {
	if s == "" {
		return ""
	}
	result := make([]byte, 0, len(s)+4)
	for i, c := range s {
		if c >= 'A' && c <= 'Z' {
			lc := byte(c + 32)
			if i > 0 {
				prev := s[i-1]
				if prev >= 'a' && prev <= 'z' {
					result = append(result, '_')
				} else if prev >= 'A' && prev <= 'Z' {
					if i+1 < len(s) && s[i+1] >= 'a' && s[i+1] <= 'z' {
						result = append(result, '_')
					}
				}
			}
			result = append(result, lc)
		} else {
			result = append(result, byte(c))
		}
	}
	return string(result)
}

// SplitAndTrim 按分隔符分割字符串，过滤空串并去除每项前后空格。
func SplitAndTrim(s, sep string) []string {
	parts := strings.Split(s, sep)
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// Registry 泛型注册表。线程安全的名称→实例映射。
// T 为存储的实例类型。NewRegistry / Register / Get 提供基础操作。
type Registry[T any] struct {
	mu   sync.RWMutex
	data map[string]T
}

// NewRegistry 创建泛型注册表。
func NewRegistry[T any]() *Registry[T] {
	return &Registry[T]{data: make(map[string]T)}
}

// Register 注册实例，同一 name 覆盖写（幂等）。
func (r *Registry[T]) Register(name string, val T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[name] = val
}

// Get 按 name 获取实例，未注册返回零值。
func (r *Registry[T]) Get(name string) T {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.data[name]
}

// IsSlice 判断值是否为切片/数组类型。
func IsSlice(v any) bool {
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array
}

// ExtractGormColumn 从 gorm struct tag 中提取 column 值。
// 如 `gorm:"column:site_code;type:varchar"` → "site_code"。
// 若未找到 column 标签返回空字符串。
func ExtractGormColumn(tag string) string {
	for _, part := range strings.Split(tag, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "column:") {
			return strings.TrimPrefix(part, "column:")
		}
	}
	return ""
}
