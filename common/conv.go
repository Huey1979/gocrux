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

// Each 遍历注册表中全部实例（fn 内**禁止**调用 Register/Get，会死锁）。
//
// 遍历顺序不确定（map 语义）：调用方若需要稳定输出，须自行排序后使用。
// 覆盖写语义同 Register：同一 name 只出现一次。
func (r *Registry[T]) Each(fn func(name string, val T)) {
	if r == nil || fn == nil {
		return
	}
	// 先快照再遍历：避免回调持锁期间调用方误触 Register 造成死锁
	r.mu.RLock()
	snapshot := make(map[string]T, len(r.data))
	for k, v := range r.data {
		snapshot[k] = v
	}
	r.mu.RUnlock()

	for k, v := range snapshot {
		fn(k, v)
	}
}

// Names 返回注册表中全部名称（顺序不确定）。
func (r *Registry[T]) Names() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.data))
	for k := range r.data {
		out = append(out, k)
	}
	return out
}

// IsSlice 判断值是否为切片/数组类型。
func IsSlice(v any) bool {
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array
}

// ToAnySlice 将任意切片类型（[]any、[]string、[]float64 等）统一转换为 []any。
func ToAnySlice(v any) []any {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice {
		return nil
	}
	result := make([]any, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		result[i] = rv.Index(i).Interface()
	}
	return result
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

// ParseBSONKey 从 bson / json struct tag 中取出真正的字段名（**逗号前段**）。
//
//	bson:"link_url,omitempty"         → "link_url"
//	bson:"shared_fields,omitempty"    → "shared_fields"
//	json:"field_ulid,omitempty"       → "field_ulid"
//	bson:",inline"                    → ""
//	""                                → ""
//
// 这是全框架**唯一**的 tag 选项剥离入口。历史上该逻辑被手写在至少 4 处，
// 直接后果是两个真实缺陷：
//   - BUG-053：`toBsonDoc` 原样用整个 tag 当 Mongo key，落库键变成
//     "link_url,omitempty"（带逗号），读取按 link_url 读不到 → create 后字段全空；
//   - BUG-061：`resolveColumn` 等把 tag 原样当列名，拼接的 Mongo 查询条件
//     永远匹配不到（List 过滤 total=0）。
//
// 之所以单独成函数而非各处内联：上述两处 bug 的根因完全相同，
// 且分散实现导致「修了一处漏一处」（BUG-053 修完 repository 又发现 service 同名问题）。
func ParseBSONKey(tag string) string {
	if idx := strings.IndexByte(tag, ','); idx >= 0 {
		return tag[:idx]
	}
	return tag
}

// IsBSONInline 判断 bson tag 是否为 inline 选项（mongo-driver 语义）。
//
// 首段（key）必须为空、且后续选项段含 "inline"：
//
//	bson:",inline"            → true
//	bson:",inline,omitempty"  → true
//	bson:"inline"             → false（无逗号，首段是字段名）
//	bson:"link_url,omitempty" → false
func IsBSONInline(tag string) bool {
	parts := strings.Split(tag, ",")
	if parts[0] != "" {
		return false
	}
	for _, opt := range parts[1:] {
		if opt == "inline" {
			return true
		}
	}
	return false
}
