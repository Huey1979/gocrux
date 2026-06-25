package service

import (
	"context"
	"fmt"
	errs "github.com/Huey1979/gocrux/errors"
	"reflect"
	"strings"
)

// keywordSearchKey context key（内部使用）。
type keywordSearchKey struct{}

// KeywordSearch 关键字搜索配置，通过 context 从 Handler 传递到 Service。
// Keyword 为搜索词，Fields 为需要做 LIKE 模糊匹配的 DB 列名列表。
type KeywordSearch struct {
	Keyword string
	Fields  []string
}

// WithKeywordSearch 将关键字搜索配置注入 context。
func WithKeywordSearch(ctx context.Context, ks KeywordSearch) context.Context {
	return context.WithValue(ctx, keywordSearchKey{}, ks)
}

// ============================================================
// 内置 before — 钩子优先，否则 fallback 默认实现
// ============================================================

func (s *GenericService[M]) beforeCreate(ctx context.Context, input []CrudRequest[M]) ([]*M, error) {
	if s.hooks.BeforeCreate != nil {
		return s.hooks.BeforeCreate(ctx, input)
	}
	return s._beforeCreate(ctx, input)
}

func (s *GenericService[M]) beforeUpdate(ctx context.Context, id, data any) (any, any, error) {
	if s.hooks.BeforeUpdate != nil {
		return s.hooks.BeforeUpdate(ctx, id, data)
	}
	return s._beforeUpdate(ctx, id, data)
}

func (s *GenericService[M]) beforeDelete(ctx context.Context, ids, codes any) (any, any, error) {
	if s.hooks.BeforeDelete != nil {
		return s.hooks.BeforeDelete(ctx, ids, codes)
	}
	return s._beforeDelete(ctx, ids, codes)
}

func (s *GenericService[M]) beforeGet(ctx context.Context, id any) (any, error) {
	if s.hooks.BeforeGet != nil {
		return s.hooks.BeforeGet(ctx, id)
	}
	return s._beforeGet(ctx, id)
}

func (s *GenericService[M]) beforeList(ctx context.Context, query any) (any, error) {
	if s.hooks.BeforeList != nil {
		return s.hooks.BeforeList(ctx, query)
	}
	return s._beforeList(ctx, query)
}

func (s *GenericService[M]) beforeActivate(ctx context.Context, id any) (any, error) {
	if s.hooks.BeforeActivate != nil {
		return s.hooks.BeforeActivate(ctx, id)
	}
	return s._beforeActivate(ctx, id)
}

func (s *GenericService[M]) beforeListVersions(ctx context.Context, id any, code string) (any, error) {
	if s.hooks.BeforeListVersions != nil {
		return s.hooks.BeforeListVersions(ctx, id, code)
	}
	return s._beforeListVersions(ctx, id, code)
}

func (s *GenericService[M]) beforeEditVersion(ctx context.Context, id any, patches map[string]any) (any, any, error) {
	if s.hooks.BeforeEditVersion != nil {
		return s.hooks.BeforeEditVersion(ctx, id, patches)
	}
	return s._beforeEditVersion(ctx, id, patches)
}

// ============================================================
// 内置 do — 钩子优先，否则 fallback 默认实现
// ============================================================

func (s *GenericService[M]) doCreate(ctx context.Context, input []*M) ([]*M, error) {
	if s.hooks.DoCreate != nil {
		return s.hooks.DoCreate(ctx, input)
	}
	return s._doCreate(ctx, input)
}

func (s *GenericService[M]) doUpdate(ctx context.Context, id, data any) (*M, error) {
	if s.hooks.DoUpdate != nil {
		return s.hooks.DoUpdate(ctx, id, data)
	}
	return s._doUpdate(ctx, id, data)
}

func (s *GenericService[M]) doDelete(ctx context.Context, id, data any) error {
	if s.hooks.DoDelete != nil {
		return s.hooks.DoDelete(ctx, id, data)
	}
	return s._doDelete(ctx, id, data)
}

func (s *GenericService[M]) doGet(ctx context.Context, id any) (*M, error) {
	if s.hooks.DoGet != nil {
		return s.hooks.DoGet(ctx, id)
	}
	return s._doGet(ctx, id)
}

func (s *GenericService[M]) doList(ctx context.Context, query any) ([]M, int64, error) {
	if s.hooks.DoList != nil {
		return s.hooks.DoList(ctx, query)
	}
	return s._doList(ctx, query)
}

func (s *GenericService[M]) doActivate(ctx context.Context, id any) error {
	if s.hooks.DoActivate != nil {
		return s.hooks.DoActivate(ctx, id)
	}
	return s._doActivate(ctx, id)
}

func (s *GenericService[M]) doListVersions(ctx context.Context, id any) ([]M, error) {
	if s.hooks.DoListVersions != nil {
		return s.hooks.DoListVersions(ctx, id)
	}
	return s._doListVersions(ctx, id)
}

func (s *GenericService[M]) doEditVersion(ctx context.Context, id any, pdata any) (*M, error) {
	if s.hooks.DoEditVersion != nil {
		// hook 层面传原始 patches（向后兼容）
		if eCtx, ok := pdata.(*editVersionCtx[M]); ok {
			return s.hooks.DoEditVersion(ctx, id, eCtx.Patches)
		}
		if patches, ok := pdata.(map[string]any); ok {
			return s.hooks.DoEditVersion(ctx, id, patches)
		}
		return nil, errs.ErrDoUpdateTypeMismatch
	}
	return s._doEditVersion(ctx, id, pdata)
}

// ============================================================
// 内置 after — 钩子优先，否则 fallback 默认实现（可修改返回值）
// ============================================================

func (s *GenericService[M]) afterCreate(ctx context.Context, result []*M) ([]*M, error) {
	if s.hooks.AfterCreate != nil {
		return s.hooks.AfterCreate(ctx, result)
	}
	return s._afterCreate(ctx, result)
}

func (s *GenericService[M]) afterUpdate(ctx context.Context, id any, result *M, pdata any) (*M, error) {
	if s.hooks.AfterUpdate != nil {
		return s.hooks.AfterUpdate(ctx, id, result, pdata)
	}
	return s._afterUpdate(ctx, id, result, pdata)
}

func (s *GenericService[M]) afterDelete(ctx context.Context, id, data any) error {
	if s.hooks.AfterDelete != nil {
		return s.hooks.AfterDelete(ctx, id, data)
	}
	return s._afterDelete(ctx, id, data)
}

func (s *GenericService[M]) afterGet(ctx context.Context, result *M) (*M, error) {
	if s.hooks.AfterGet != nil {
		return s.hooks.AfterGet(ctx, result)
	}
	return s._afterGet(ctx, result)
}

func (s *GenericService[M]) afterList(ctx context.Context, list []M, total int64) ([]M, int64, error) {
	if s.hooks.AfterList != nil {
		return s.hooks.AfterList(ctx, list, total)
	}
	return s._afterList(ctx, list, total)
}

func (s *GenericService[M]) afterActivate(ctx context.Context, id any) error {
	if s.hooks.AfterActivate != nil {
		return s.hooks.AfterActivate(ctx, id)
	}
	return s._afterActivate(ctx, id)
}

func (s *GenericService[M]) afterListVersions(ctx context.Context, result []M) ([]M, error) {
	if s.hooks.AfterListVersions != nil {
		return s.hooks.AfterListVersions(ctx, result)
	}
	return s._afterListVersions(ctx, result)
}

func (s *GenericService[M]) afterEditVersion(ctx context.Context, id any, result *M, pdata any) (*M, error) {
	if s.hooks.AfterEditVersion != nil {
		return s.hooks.AfterEditVersion(ctx, id, result)
	}
	return s._afterEditVersion(ctx, id, result, pdata)
}

// ============================================================
// 工具函数
// ============================================================

// newRecord 创建一个新的零值 Record 实例。
// M 必须是 *Struct 指针类型（gocrux 约定），通过反射分配底层 struct。
// 替代直接 var m M（当 M 为指针类型时会产生 nil 指针，导致方法调用 panic）。
func newRecord[M Record]() M {
	var z M
	t := reflect.TypeOf(z)
	if t.Kind() == reflect.Ptr {
		return reflect.New(t.Elem()).Interface().(M)
	}
	return z
}

// extractIdemKey 从批量请求中提取首个有效幂等键。
// 返回空字符串表示未启用幂等。
func extractIdemKey[M Record](input []CrudRequest[M]) string {
	if len(input) == 0 {
		return ""
	}
	if idem, ok := input[0].(HasIdempotencyKey); ok {
		return idem.GetIdempotencyKey()
	}
	return ""
}

// nextVersionCode 计算下一个版本号: v1.0 → v1.1, v1 → v2
func nextVersionCode(currentCode string) string {
	if currentCode == "" {
		return "v1.0"
	}
	code := currentCode
	if len(code) > 0 && (code[0] == 'v' || code[0] == 'V') {
		code = code[1:]
	}
	parts := strings.Split(code, ".")
	if len(parts) == 0 {
		return "v1.0"
	}
	if len(parts) == 1 {
		n, err := parseInt(parts[0])
		if err != nil {
			return "v1.0"
		}
		return "v" + itoa(n+1)
	}
	major := parts[0]
	minor, err := parseInt(parts[len(parts)-1])
	if err != nil {
		minor = 0
	}
	return "v" + major + "." + itoa(minor+1)
}

// parseInt 字符串转 int，失败返回 0
func parseInt(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n, nil
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// itoa int → string
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	r := ""
	for n > 0 {
		r = string(rune('0'+n%10)) + r
		n /= 10
	}
	return r
}

// getStrField 反射读取实体指定字段的 string 值
func getStrField(_entity any, fieldName string) string {
	v := reflect.ValueOf(_entity)
	for v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return ""
	}
	f := v.FieldByName(fieldName)
	if !f.IsValid() {
		return ""
	}
	return f.String()
}

// getFieldVal 反射读取实体指定字段的原始值（保持类型，用于 DB 精确匹配）
func getFieldVal(_entity any, fieldName string) any {
	v := reflect.ValueOf(_entity)
	for v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}
	f := v.FieldByName(fieldName)
	if !f.IsValid() {
		return nil
	}
	return f.Interface()
}

// resolveColumn 根据 Go 结构体字段名 → GORM column 名称
// 遍历 M 的字段，匹配 fieldName，从 gorm tag 提取 column。
func resolveColumn[M Record](fieldName string) string {
	var m M
	t := reflect.TypeOf(m)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	f, ok := t.FieldByName(fieldName)
	if !ok {
		return toCamelSnake(fieldName)
	}
	gormTag := f.Tag.Get("gorm")
	if col := extractGormColumn(gormTag); col != "" {
		return col
	}
	return toCamelSnake(fieldName)
}

// extractGormColumn 从 gorm tag 中提取 column 值
func extractGormColumn(tag string) string {
	for _, part := range strings.Split(tag, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "column:") {
			return strings.TrimPrefix(part, "column:")
		}
	}
	return ""
}

// toCamelSnake 驼峰转下划线 fallback（如 "SiteCode" → "site_code"）
func toCamelSnake(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r + 32)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// popIntParam 从 map 中取出并删除指定的键，将值转为 int。
// 若键不存在或无法解析则返回 0。
func popIntParam(m map[string]any, key string) int {
	v, ok := m[key]
	if !ok {
		return 0
	}
	delete(m, key)

	s := fmt.Sprintf("%v", v)
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
	}
	n, _ := parseInt(s)
	return n
}

// popStrParam 从 map 中取出并删除指定的键，将值转为 string。
// 若键不存在返回空字符串。
func popStrParam(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	delete(m, key)
	return fmt.Sprintf("%v", v)
}

// isSlice 判断值是否为切片/数组，用于自动从 OpEQ 切换为 OpIn。
func isSlice(v any) bool {
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array
}
