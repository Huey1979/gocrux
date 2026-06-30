package service

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	errs "github.com/Huey1979/gocrux/errors"
	"github.com/Huey1979/gocrux/repository"

	"gorm.io/gorm"
)

// -------- Get --------
func (s *GenericService[M]) _beforeGet(ctx context.Context, id any) (any, error) {
	if id == nil {
		return nil, errs.ErrInvalidParam
	}
	return id, nil
}
func (s *GenericService[M]) _doGet(ctx context.Context, id any) (*M, error) {
	result, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.ErrRecordNotFound
		}
		return nil, err
	}
	return result, nil
}
func (s *GenericService[M]) _afterGet(ctx context.Context, result *M) (*M, error) { return result, nil }

// _doGetByCode 按业务编码查当前生效版本。
// 版本化模式：CodeField = code AND CurrentField = 1（is_current=true，不论是否 published）。
// 非版本化模式：退化为 CodeField 字段等值查询（repo.GetByField）。
func (s *GenericService[M]) _doGetByCode(ctx context.Context, code string) (*M, error) {
	if !s.config.VersionMode || s.config.VersionFields == nil {
		// 非版本化模式：使用 VersionFields.CodeField，不再硬编码 "code"
		codeField := "code"
		if s.config.VersionFields != nil && s.config.VersionFields.CodeField != "" {
			codeField = resolveColumn[M](s.config.VersionFields.CodeField)
		}
		result, err := s.repo.GetByField(ctx, codeField, code)
		if err != nil {
			return nil, err
		}
		if result == nil {
			return nil, errs.ErrRecordNotFound
		}
		return result, nil
	}

	vf := s.config.VersionFields
	codeCol := resolveColumn[M](vf.CodeField)
	currentCol := resolveColumn[M](vf.CurrentField)

	// 查当前生效版本（is_current=1），不论是否 published
	results, _, err := s.repo.ListByFilters(ctx, repository.ListFilters{
		Filters: []repository.Filter{
			{Field: codeCol, Op: repository.OpEQ, Value: code},
			{Field: currentCol, Op: repository.OpEQ, Value: int8(1)},
			{Field: "is_deleted", Op: repository.OpEQ, Value: int8(0)},
		},
		Page:     1,
		PageSize: 1,
	})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, errs.ErrRecordNotFound
	}
	return &results[0], nil
}

// -------- List --------
func (s *GenericService[M]) _beforeList(ctx context.Context, query any) (any, error) {
	return query, nil
}
func (s *GenericService[M]) _doList(ctx context.Context, query any) ([]M, int64, error) {
	var f repository.ListFilters

	switch v := query.(type) {
	case repository.ListFilters:
		f = v
	case map[string]any:
		// 分离控制参数与过滤条件
		f.Page, f.PageSize = popIntParam(v, "page"), popIntParam(v, "page_size")
		f.OrderBy, f.OrderDir = popStrParam(v, "order_by"), popStrParam(v, "order_dir")

		for k, val := range v {
			field, op, value := parseFilterKey(k, val)
			if field == "" {
				continue
			}
			f.Filters = append(f.Filters, repository.Filter{Field: field, Op: op, Value: value})
		}

	default:
		// 无过滤条件，不分页返回全部
		all, err := s.repo.ListAll(ctx)
		if err != nil {
			return nil, 0, err
		}
		return all, int64(len(all)), nil
	}

	// keyword 关键字搜索：多字段 OR 匹配（支持模糊/精确）
	if ks, ok := ctx.Value(keywordSearchKey{}).(KeywordSearch); ok && ks.Keyword != "" {
		keywordFilters := make([]repository.Filter, 0, len(ks.Fields))
		for _, kf := range ks.Fields {
			if kf.Exact {
				keywordFilters = append(keywordFilters, repository.Filter{
					Field: kf.Field, Op: repository.OpEQ, Value: ks.Keyword,
				})
			} else {
				keywordFilters = append(keywordFilters, repository.Filter{
					Field: kf.Field, Op: repository.OpLike, Value: "%" + ks.Keyword + "%",
				})
			}
		}
		f.Filters = append(f.Filters, repository.Filter{
			Op: "or_group", Value: keywordFilters,
		})
	}

	// 默认过滤：版本化 → 仅当前版本 + 草稿可见性
	if s.config.VersionMode && s.config.VersionFields != nil {
		vf := s.config.VersionFields
		f.Filters = append(f.Filters, repository.Filter{
			Field: resolveColumn[M](vf.CurrentField), Op: repository.OpEQ, Value: true,
		})
		// 草稿可见性：未登录仅看已发布，登录后看已发布+自己的草稿
		if vf.StatusField != "" {
			userID := GetUserULID(ctx)
			statusCol := resolveColumn[M](vf.StatusField)
			if userID == "" {
				f.Filters = append(f.Filters, repository.Filter{
					Field: statusCol, Op: repository.OpEQ, Value: string(VersionStatusPublished),
				})
			} else {
				// 登录用户：published OR (draft AND created_by=user)
				createdByCol := resolveColumn[M]("CreatedBy")
				f.Filters = append(f.Filters, repository.Filter{
					Op: "or_group",
					Value: []repository.Filter{
						{Field: statusCol, Op: repository.OpEQ, Value: string(VersionStatusPublished)},
						{Op: repository.OpRaw, Value: []any{
							statusCol + " = ? AND " + createdByCol + " = ?",
							string(VersionStatusDraft), userID,
						}},
					},
				})
			}
		}
	} else {
		m := newRecord[M]()
		if m.SetDelete() {
		field := s.config.DeletedField
		if field == "" {
			field = "is_deleted"
		}
		val := s.config.DeletedValue
		if val == nil {
			val = int8(0)
		}
		f.Filters = append(f.Filters, repository.Filter{Field: field, Op: repository.OpEQ, Value: val})
		}
	}

	return s.repo.ListByFilters(ctx, f)
}

// ============================================================
// parseFilterKey — 解析 URL 查询参数键中的操作符后缀
//
// 使用 `:` 分隔（MySQL 列名不含冒号，绝对安全）：
//
//	field           → (field, OpEQ / OpIn, value)     自动：切片=OpIn，否则=OpEQ
//	field:like      → (field, OpLike, value)           LIKE，自动包裹 %value%
//	field:gt        → (field, OpGT, value)
//	field:gte       → (field, OpGTE, value)
//	field:lt        → (field, OpLT, value)
//	field:lte       → (field, OpLTE, value)
//	field:ne        → (field, OpNEQ, value)
//	field:in        → (field, OpIn, value)             逗号分隔字符串 → []any
//	field:between   → (field, OpRange, value)          逗号分隔字符串 → []any{lo,hi}
//
// 注意：field:like 的值会自动在前后追加 %，除非已包含 %。
// ============================================================
func parseFilterKey(key string, rawValue any) (field string, op repository.FilterOp, value any) {
	// 查找 : 分隔的运算符后缀（如 form_code:like=xxx）。
	// 使用 : 而非 _ 作为分隔符，避免与字段名中的下划线冲突。
	// 兼容旧的 __ 后缀（后续移除）
	parseLegacy := func() bool {
		if idx := strings.LastIndex(key, "__"); idx > 0 {
			field = key[:idx]
			switch key[idx+2:] {
			case "like", "gt", "gte", "lt", "lte", "ne", "in", "between":
				return true // fall through to switch below
			}
		}
		return false
	}
	if parseLegacy() {
		// 旧后缀已设置 field，下面 switch 会 set op + value
		suffix := key[strings.LastIndex(key, "__")+2:]
		switch suffix {
		case "like":
			s := fmt.Sprintf("%v", rawValue)
			if !strings.Contains(s, "%") {
				s = "%" + s + "%"
			}
			return field, repository.OpLike, s
		case "gt":
			return field, repository.OpGT, rawValue
		case "gte":
			return field, repository.OpGTE, rawValue
		case "lt":
			return field, repository.OpLT, rawValue
		case "lte":
			return field, repository.OpLTE, rawValue
		case "ne":
			return field, repository.OpNEQ, rawValue
		case "in":
			return field, repository.OpIn, parseCSVValue(rawValue)
		case "between":
			return field, repository.OpRange, parseCSVValue(rawValue)
		}
	}

	if idx := strings.LastIndex(key, ":"); idx > 0 {
		suffix := key[idx+1:]
		field = key[:idx]
		switch suffix {
		case "eq":
			return field, repository.OpEQ, rawValue
		case "like":
			s := fmt.Sprintf("%v", rawValue)
			if !strings.Contains(s, "%") {
				s = "%" + s + "%"
			}
			return field, repository.OpLike, s
		case "gt":
			return field, repository.OpGT, rawValue
		case "gte":
			return field, repository.OpGTE, rawValue
		case "lt":
			return field, repository.OpLT, rawValue
		case "lte":
			return field, repository.OpLTE, rawValue
		case "ne":
			return field, repository.OpNEQ, rawValue
		case "in":
			return field, repository.OpIn, parseCSVValue(rawValue)
		case "between":
			return field, repository.OpRange, parseCSVValue(rawValue)
		}
		// 非已知运算符 → 整个 key 当作字段名
	}

	// 2. 无后缀：自动推断 Op
	field = key
	if isSlice(rawValue) {
		return field, repository.OpIn, rawValue
	}
	return field, repository.OpEQ, rawValue
}

// parseCSVValue 将逗号分隔的字符串值转为 []any（用于 OpIn / OpRange）。
// 若 rawValue 已是切片则直接返回；否则按 fmt.Sprintf + strings.Split 解析。
func parseCSVValue(rawValue any) []any {
	// 已是切片 → 直接转 []any
	if rv := reflect.ValueOf(rawValue); rv.Kind() == reflect.Slice {
		result := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			result[i] = rv.Index(i).Interface()
		}
		return result
	}

	s := strings.TrimSpace(fmt.Sprintf("%v", rawValue))
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	result := make([]any, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
func (s *GenericService[M]) _afterList(ctx context.Context, list []M, total int64) ([]M, int64, error) {
	return list, total, nil
}
