package handler

import (
	"context"
	"encoding/json"
	"fmt"

	errs "github.com/Huey1979/gocrux/errors"
	"github.com/Huey1979/gocrux/service"
)

// ============================================================
// 内置 _before / _do / _after 默认实现
//
// _beforeXxx：纯数据管线中的前置处理（校验、转换等，不依赖 gin）
// _doXxx：    调用 Service（后续扩展级联逻辑）
// _afterXxx： 纯数据管线中的后置处理（结果转换等，不依赖 gin）
//
// gin 相关的 I/O 已全部上提至 HTTP 薄壳方法。
// 与 Service 层的 generic_impl.go 完全对等。
// ============================================================

// -------- Get (统一处理 getByID / getByCode / getByIDToPublished) --------

func (h *GenericHandler[M]) _beforeGet(_ context.Context, req *GetRequest) (*GetRequest, error) {
	// 默认：透传
	return req, nil
}

func (h *GenericHandler[M]) _doGet(ctx context.Context, req *GetRequest) (map[string]any, error) {
	var result *M
	var err error

	// GlobalStore 缓存：先查缓存，命中跳过 DB
	if store := h.config.GlobalStore; store != nil {
		if req.ID != nil {
			if v, ok := store.Get(ctx, cacheKeyULID(fmt.Sprint(req.ID))); ok {
				result = v.(*M)
			}
		}
		if result == nil && req.Code != "" {
			if v, ok := store.Get(ctx, cacheKeyCode(req.Code)); ok {
				result = v.(*M)
			}
		}
	}

	if result == nil {
		switch {
		case req.Code != "":
			result, err = h.svc.GetByCode(ctx, req.Code)
		default:
			result, err = h.svc.Get(ctx, req.ID)
		}
		if err != nil {
			return nil, err
		}
		// 写入缓存
		if store := h.config.GlobalStore; store != nil {
			if pk := extractPKFromResult(result); pk != nil {
				store.Set(ctx, cacheKeyULID(fmt.Sprint(pk)), result)
			}
		}
	}

	// FollowPublished：按 ID 取出的记录可能是非 published 版本，
	// 需要解析为同 code 族的正式发布版本。
	if req.FollowPublished {
		result, err = h.svc.ResolveOneToPublished(ctx, result)
		if err != nil {
			return nil, err
		}
	}

	// 将原始实体写入 context holder，供 HTTP 出口处 ResponseMapper 使用。
	// holder 仅由顶层 HTTP Get() handler 创建，级联调用（DoGetByID）不经过此路径。
	if holder, ok := ctx.Value(entityGetCtxKey{}).(**M); ok && holder != nil {
		*holder = result
	}

	// 展开：*M → map + References + Cascades + ChildRefs
	return h.expandGet(ctx, result)
}

func (h *GenericHandler[M]) _afterGet(ctx context.Context, result map[string]any) (map[string]any, error) {
	// 字段裁剪：?fields=a;b:c;d:[e,f]
	if f := getFields(ctx); f != "" {
		result = pruneFields(result, f)
	}
	return result, nil
}

// -------- List --------

func (h *GenericHandler[M]) _beforeList(_ context.Context, query any) (any, error) {
	// 默认：透传
	return query, nil
}

func (h *GenericHandler[M]) _doList(ctx context.Context, query any, followPublished bool) ([]map[string]any, int64, error) {
	// 内置 keyword 处理：提取 ?keyword=xxx → context 注入 OR 查询条件
	if q, ok := query.(map[string]any); ok && len(h.config.KeywordFields) > 0 {
		if kw, hasKW := q["keyword"]; hasKW {
			kwStr := fmt.Sprintf("%v", kw)
			delete(q, "keyword")
			if kwStr != "" {
				kfs := make([]service.KwField, len(h.config.KeywordFields))
				for i, kf := range h.config.KeywordFields {
					kfs[i] = service.KwField{Field: kf.Field, Exact: kf.Match == KeywordExact}
				}
				ctx = service.WithKeywordSearch(ctx, service.KeywordSearch{Keyword: kwStr, Fields: kfs})
			}
		}
	}

	list, total, err := h.svc.List(ctx, query)
	if err != nil {
		return nil, 0, err
	}

	// FollowPublished：级联场景将 FK 指向的记录解析为正式发布版本。
	if followPublished && len(list) > 0 {
		resolved, err := h.svc.ResolveToPublished(ctx, list)
		if err != nil {
			return nil, 0, errs.ErrParsePublishedVersion(err)
		}
		list = resolved
	}

	// 将原始实体切片写入 context holder，供 HTTP 出口处 ResponseMapper 使用。
	// holder 仅由顶层 HTTP List() handler 创建，级联调用（DoList）不创建 holder。
	if holder, ok := ctx.Value(entityListCtxKey{}).(*[]*M); ok && holder != nil && len(list) > 0 {
		entities := make([]*M, len(list))
		for i := range list {
			entities[i] = &list[i]
		}
		*holder = entities
	}

	// *M → []map[string]any
	result := make([]map[string]any, len(list))
	for i, r := range list {
		data, err := json.Marshal(r)
		if err != nil {
			return nil, 0, errs.ErrMarshalEntity(err)
		}
		if err := json.Unmarshal(data, &result[i]); err != nil {
			return nil, 0, errs.ErrUnmarshalEntity(err)
		}
	}

	// 深度检查 + visited 防护：防止循环展开
	// -------- visited + depth 统一防护 --------
	childCtx, ok := canExpandTo(ctx, h.svcName, "batch")
	if !ok {
		return result, total, nil
	}

	// 批量展开 References（向上引用）
	if len(h.config.References) > 0 && h.handlerReg != nil {
		for _, ref := range h.config.References {
			resultKey := ref.ResultField
			if resultKey == "" {
				resultKey = deriveRefResultKey(ref.Field)
			}
			// 忽略控制：ignoreAll / ignoreRef / ignore=resultKey
			refHandler, ok := h.shouldExpandField(ctx, childCtx, resultKey, ref.HandlerName, true)
			if !ok {
				continue
			}

			// 收集所有 FK 值（去重）
			fkSet := make(map[string]bool)
			for _, m := range result {
				fkVal, ok := getByPath(m, ref.Field)
				if !ok || fkVal == nil {
					continue
				}
				s := fmt.Sprint(fkVal)
				if s != "" {
					fkSet[s] = true
				}
			}
			if len(fkSet) == 0 {
				continue
			}

			fkList := make([]any, 0, len(fkSet))
			for k := range fkSet {
				fkList = append(fkList, k)
			}

			// 构造字段级子 context
			refCtx := h.buildFieldCtx(childCtx, ref.Field, ref.HandlerName)
			if isVisited(refCtx, ref.HandlerName, "batch") {
				continue
			}

			// 批量查（DoList + slice → OpIn）
			pkField := refHandler.PKField()
			parentRecords, err := refHandler.DoList(refCtx, pkField, fkList, false)
			if err != nil {
				return nil, 0, errs.ErrRefBatchResolve(ref.HandlerName, err)
			}

			// 按 PK 建索引
			parentMap := make(map[string]map[string]any)
			for _, pr := range parentRecords {
				if idVal, ok := pr[pkField]; ok {
					parentMap[fmt.Sprint(idVal)] = pr
				}
			}

			for _, m := range result {
				if fkVal, fkOk := getByPath(m, ref.Field); fkOk && fkVal != nil {
					if parent, ok := parentMap[fmt.Sprint(fkVal)]; ok {
					m[resultKey] = parent
					}
				}
			}
		}
	}

	// 批量展开 ChildRefs（向下引用 FK 列表）
	if len(h.config.ChildRefs) > 0 && h.handlerReg != nil {
		for _, cr := range h.config.ChildRefs {
			resultKey := cr.ResultField
			if resultKey == "" {
				resultKey = deriveChildRefResultKey(cr.FKListField)
			}
			// 忽略控制：ignoreAll / ignoreRef / ignore=resultKey
			refHandler, ok := h.shouldExpandField(ctx, childCtx, resultKey, cr.HandlerName, true)
			if !ok {
				continue
			}

			// 收集所有 FK 值（去重）
			fkSet := make(map[string]bool)
			for _, m := range result {
				ids := toAnySlice(m[cr.FKListField])
				for _, id := range ids {
					s := fmt.Sprint(id)
					if s != "" {
						fkSet[s] = true
					}
				}
			}
			if len(fkSet) == 0 {
				continue
			}

			fkList := make([]any, 0, len(fkSet))
			for k := range fkSet {
				fkList = append(fkList, k)
			}

			// 构造字段级子 context
			refCtx := h.buildFieldCtx(childCtx, cr.FKListField, cr.HandlerName)
			if isVisited(refCtx, cr.HandlerName, "batch") {
				continue
			}

			pkField := refHandler.PKField()
			childRecords, err := refHandler.DoList(refCtx, pkField, fkList, false)
			if err != nil {
				return nil, 0, errs.ErrChildRefBatchResolve(cr.HandlerName, err)
			}

			childMap := make(map[string]map[string]any)
			for _, child := range childRecords {
				if idVal, ok := child[pkField]; ok {
					childMap[fmt.Sprint(idVal)] = child
				}
			}

			for _, m := range result {
				ids := toAnySlice(m[cr.FKListField])
				resolved := make([]map[string]any, 0, len(ids))
				for _, id := range ids {
					if child, ok := childMap[fmt.Sprint(id)]; ok {
						resolved = append(resolved, child)
					}
				}
				if len(resolved) > 0 {
					m[resultKey] = resolved
				}
			}
		}
	}

	// 批量展开 Cascades → 委托 expandCascadesBatch
	h.expandCascadesBatch(ctx, childCtx, result)

	// List 字段裁剪：skip 优先于 keep，均未配置时全字段返回。
	// 仅作用于 _doList（Get 接口不受影响），在所有展开逻辑之后执行。
	if len(h.config.ListSkipFields) > 0 {
		skipSet := make(map[string]bool, len(h.config.ListSkipFields))
		for _, f := range h.config.ListSkipFields {
			skipSet[f] = true
		}
		for _, row := range result {
			for f := range skipSet {
				delete(row, f)
			}
		}
	} else if len(h.config.ListKeepFields) > 0 {
		keepSet := make(map[string]bool, len(h.config.ListKeepFields))
		for _, f := range h.config.ListKeepFields {
			keepSet[f] = true
		}
		for _, row := range result {
			for k := range row {
				if !keepSet[k] {
					delete(row, k)
				}
			}
		}
	}

	return result, total, nil
}

func (h *GenericHandler[M]) _afterList(ctx context.Context, list []map[string]any, total int64) ([]map[string]any, int64, error) {
	// 字段裁剪：?fields=a;b:c;d:[e,f]
	if f := getFields(ctx); f != "" {
		for i, item := range list {
			list[i] = pruneFields(item, f)
		}
	}
	return list, total, nil
}
