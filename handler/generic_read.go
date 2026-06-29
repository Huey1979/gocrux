package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	errs "github.com/Huey1979/gocrux/errors"
	"github.com/sirupsen/logrus"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 10. Get — 管线（HTTP → before → do → after）
// ============================================================

// DoGetByID CascadeHandler 接口实现。
// 按主键查询单条记录，返回 map。
// 用于向上级联：父实体的逻辑外键字段 → 调用引用 Handler 的 DoGetByID。
//
// 若 context 中存在深度控制（depth > 0），自动通过 expandGet 递归展开
// 该记录的 References/ChildRefs/Cascades。
func (h *GenericHandler[M]) DoGetByID(ctx context.Context, id any) (map[string]any, error) {
	result, err := h.svc.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	// depth > 0 且 handlerReg 已注入 → 递归展开
	if d, ok := getDepth(ctx); ok && d > 0 && h.handlerReg != nil {
		return h.expandGet(ctx, result)
	}

	// 无深度控制或 handlerReg 未注入 → 返回原始 map（旧行为）
	data, err := json.Marshal(result)
	if err != nil {
		return nil, errs.ErrMarshalRecord(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, errs.ErrUnmarshalRecord(err)
	}
	return m, nil
}

// Get 获取记录详情（三个子查询的统一入口）。
//
//	GET /{prefix}/get?id=xxx                       → 精确版本查询
//	GET /{prefix}/get?id=xxx&follow_published=true  → 解析为已发布版本
//	GET /{prefix}/get?code=xxx                      → 按 Code 查询已发布版本
//	GET /{prefix}/get?id=xxx&depth=2                → 展开引用/级联最多 2 层
func (h *GenericHandler[M]) Get(c *gin.Context) {
	if !h.checkPerm(c, "get") {
		return
	}
	ctx := c.Request.Context()

	idStr := c.Query("id")
	code := c.Query("code")

	if idStr == "" && code == "" {
		h.handleError(c, errs.ErrInvalidParam)
		return
	}

	req := &GetRequest{
		ID:              idStr,
		Code:            code,
		FollowPublished: idStr != "" && c.Query("follow_published") == "true",
	}

	// 展开深度：query ?depth=N 覆盖配置值
	ctx = h.injectDepth(ctx, c)
	// 忽略控制：query ?ignore / ?ignoreRef / ?ignoreCascade / ?ignoreAll
	ctx = h.injectIgnore(ctx, c)
	// 字段级控制：query ?fdepth / ?fstop
	ctx = h.injectStop(ctx, c)
	// 字段裁剪：query ?fields=a;b:c;d:[e,f]
	ctx = withFields(ctx, c.Query("fields"))

	// 创建 Entity holder：_doGet 会将原始实体写入此指针，供 HTTP 出口处 ResponseMapper 使用。
	// 级联调用（DoGetByID）不经过此路径，因此不会触发映射。
	var entityHolder *M
	ctx = context.WithValue(ctx, entityGetCtxKey{}, &entityHolder)

	result, err := h.getPipeline(ctx, req)
	if err != nil {
		h.handleError(c, err)
		return
	}

	// HTTP 出口处执行 ResponseMapper（仅顶层 HTTP handler，管线/级联不参与）
	result = h.applyResponseMapper(entityHolder, result)
	Success(c, result)
}

// getPipeline 统一管线（HTTP 入口共享）。
func (h *GenericHandler[M]) getPipeline(ctx context.Context, req *GetRequest) (_ map[string]any, err error) {
	start := traceStart(ctx, h.svcName+".get", logrus.Fields{"id": req.ID, "code": req.Code})
	defer func() { traceEnd(ctx, h.svcName+".get", start, err) }()
	preq, err := h.beforeGet(ctx, req)
	if err != nil {
		return nil, err
	}

	result, err := h.doGet(ctx, preq)
	if err != nil {
		return nil, err
	}

	return h.afterGet(ctx, result)
}

func (h *GenericHandler[M]) beforeGet(ctx context.Context, req *GetRequest) (*GetRequest, error) {
	if h.hooks.BeforeGet != nil {
		return h.hooks.BeforeGet(ctx, req)
	}
	return h._beforeGet(ctx, req)
}

func (h *GenericHandler[M]) doGet(ctx context.Context, req *GetRequest) (map[string]any, error) {
	if h.hooks.DoGet != nil {
		return h.hooks.DoGet(ctx, req)
	}
	return h._doGet(ctx, req)
}

func (h *GenericHandler[M]) afterGet(ctx context.Context, result map[string]any) (map[string]any, error) {
	if h.hooks.AfterGet != nil {
		return h.hooks.AfterGet(ctx, result)
	}
	return h._afterGet(ctx, result)
}

// expandGet Get 结果双向级联展开。
// 将 *M 展开为 map[string]any，并执行：
//  1. 向上级联：解析本实体的逻辑外键字段（References 配置）
//  2. 向下引用：解析 FK 列表为完整子对象列表（ChildRefs 配置）
//  3. 向下级联：查询本实体的子记录（Cascades 配置）
//
// 若 context 中设置了深度控制（depth > 0），每层展开后向下传递 depth-1，
// 实现递归展开；depth=0 时停止。未设置深度时保持旧行为（仅展开一层）。
//
// 字段级控制：通过 fieldLimitCtx 接收父 Handler 的截止规则，
// 通过 buildFieldCtx 将本 Handler 的 FieldDepthLimits/FieldStopRules 传递给子 Handler。
//
// 无 handlerReg 时跳过级联，仅做 marshal → map 转换。
// 版本操作（activate / list-versions / edit-version）不应调用此方法。
func (h *GenericHandler[M]) expandGet(ctx context.Context, result *M) (map[string]any, error) {
	// 1. *M → map[string]any
	data, err := json.Marshal(result)
	if err != nil {
		return nil, errs.ErrMarshalEntity(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, errs.ErrUnmarshalEntity(err)
	}

	// -------- visited + depth 统一防护 --------
	pk := extractPKFromResult(result)
	baseChildCtx, ok := canExpandTo(ctx, h.svcName, fmt.Sprint(pk))
	if !ok {
		return out, nil
	}
	baseChildCtx = addVisited(baseChildCtx, h.svcName, "batch")

	// 2. 向上级联：解析本实体的引用字段
	if h.handlerReg != nil && true {
		for _, ref := range h.config.References {
			resultKey := ref.ResultField
			if resultKey == "" {
				resultKey = deriveRefResultKey(ref.Field)
			}
			// 忽略控制：ignoreAll / ignoreRef / ignore=resultKey
			refHandler, ok := h.shouldExpandField(ctx, baseChildCtx, resultKey, ref.HandlerName, true)
			if !ok {
				continue
			}

			fkVal, ok := out[ref.Field]
			if !ok || fkVal == nil || fkVal == "" {
				continue
			}

			// 构造字段级子 context：含字段深度上限 + 截止规则
			refCtx := h.buildFieldCtx(baseChildCtx, ref.Field, ref.HandlerName)
			// visited 防自引用
			if isVisited(refCtx, ref.HandlerName, fmt.Sprint(fkVal)) {
				continue
			}
			parentRecord, err := refHandler.DoGetByID(refCtx, fkVal)
			if err != nil {
				return nil, errs.ErrRefResolve(ref.HandlerName, err)
			}

			out[resultKey] = parentRecord
		}
	}

	// 2.5. 向下引用：将 FK 列表解析为完整子对象列表（ChildRefs）
	if h.handlerReg != nil && true {
		for _, cr := range h.config.ChildRefs {
			resultKey := cr.ResultField
			if resultKey == "" {
				resultKey = deriveChildRefResultKey(cr.FKListField)
			}
			// 忽略控制：ignoreAll / ignoreRef / ignore=resultKey
			refHandler, ok := h.shouldExpandField(ctx, baseChildCtx, resultKey, cr.HandlerName, true)
			if !ok {
				continue
			}

			fkList, ok := out[cr.FKListField]
			if !ok || fkList == nil {
				continue
			}

			ids := toAnySlice(fkList)
			if len(ids) == 0 {
				continue
			}

			strIDs := make([]any, 0, len(ids))
			for _, id := range ids {
				strIDs = append(strIDs, fmt.Sprint(id))
			}

			// 构造字段级子 context
			refCtx := h.buildFieldCtx(baseChildCtx, cr.FKListField, cr.HandlerName)

			pkField := refHandler.PKField()
			if isVisited(refCtx, cr.HandlerName, "batch") {
				continue
			}
			childRecords, err := refHandler.DoList(refCtx, pkField, strIDs, false)
			if err != nil {
				return nil, errs.ErrChildRefResolve(cr.HandlerName, err)
			}

			childMap := make(map[string]map[string]any, len(childRecords))
			for _, child := range childRecords {
				if idVal, ok := child[pkField]; ok {
					childMap[fmt.Sprint(idVal)] = child
				}
			}

			resolved := make([]map[string]any, 0, len(ids))
			for _, id := range ids {
				if child, ok := childMap[fmt.Sprint(id)]; ok {
					resolved = append(resolved, child)
				}
			}

			out[resultKey] = resolved
		}
	}

	// 3. 向下级联：查询子记录
	if h.handlerReg != nil && true {
		for _, rel := range h.config.Cascades {
			// 忽略控制：ignoreAll / ignoreCascade / ignore=ChildrenField
			childHandler, ok := h.shouldExpandField(ctx, baseChildCtx, rel.ChildrenField, rel.HandlerName, false)
			if !ok {
				continue
			}

			pk := extractPKFromResult(result)
			if pk == nil || pk == "" {
				continue
			}

			cascCtx := h.buildFieldCtx(baseChildCtx, rel.ChildrenField, rel.HandlerName)
			if isVisited(cascCtx, rel.HandlerName, "batch") {
				continue
			}

			children, err := childHandler.DoList(cascCtx, rel.FKField, pk, rel.FollowPublished)
			if err != nil {
				return nil, errs.ErrCascadeQuery(rel.HandlerName, err)
			}

			if len(children) > 0 {
				out[rel.ChildrenField] = children
			}
		}
	}

	return out, nil
}

// deriveRefResultKey 从逻辑外键字段名推导结果键名。
// 例：site_ulid → site、dept_ulid → dept、default_menu_ulid → default_menu。
// deriveRefResultKey 从逻辑外键字段名推导展开结果键名。
// 例：entity_id → entity_info、site_ulid → site_info。
// 使用 _info 后缀避免覆盖原始 FK 字段值。
func deriveRefResultKey(field string) string {
	s := field
	if after, ok := strings.CutSuffix(s, "_ulid"); ok {
		s = after
	} else if after, ok := strings.CutSuffix(s, "_id"); ok {
		s = after
	}
	return s + "_info"
}

// deriveChildRefResultKey 从 FK 列表字段名推导展开结果键名。
// 例：tag_ulids → tags、menu_ids → menus、role_ulids → roles。
func deriveChildRefResultKey(fkListField string) string {
	s := fkListField
	// 去掉 _ulids 后缀
	if i := len(s) - 6; i > 0 && s[i:] == "_ulids" {
		return s[:i] + "s"
	}
	// 去掉 _ids 后缀
	if i := len(s) - 4; i > 0 && s[i:] == "_ids" {
		return s[:i] + "s"
	}
	return s
}

// ============================================================
// 11. List — 管线（HTTP → cascade → before → do → after）
// ============================================================

// DoList CascadeHandler 接口实现。
// 按外键查询子记录，返回 map 列表。
func (h *GenericHandler[M]) DoList(ctx context.Context, fkField string, fkValue any, followPublished bool) ([]map[string]any, error) {
	query := map[string]any{fkField: fkValue}
	records, _, err := h.listPipeline(ctx, query, followPublished)
	return records, err
}

// List 列表查询
// GET /{prefix}/list?page=1&page_size=20&keyword=xxx&status=active&depth=2
func (h *GenericHandler[M]) List(c *gin.Context) {
	if !h.checkPerm(c, "list") {
		return
	}
	ctx := c.Request.Context()

	filters := make(map[string]any)
	for key, vals := range c.Request.URL.Query() {
		if len(vals) == 0 {
			continue
		}
		// 处理 key[]= 语法（如 ?id[]=1&id[]=2）→ 去掉 [] 后缀，全量作为切片
		if strings.HasSuffix(key, "[]") {
			cleanKey := key[:len(key)-2]
			anyVals := make([]any, len(vals))
			for i, v := range vals {
				anyVals[i] = v
			}
			filters[cleanKey] = anyVals
		} else if len(vals) == 1 {
			filters[key] = vals[0]
		} else {
			// 多值同键（如 ?status=active&status=draft）→ 全量作为切片
			anyVals := make([]any, len(vals))
			for i, v := range vals {
				anyVals[i] = v
			}
			filters[key] = anyVals
		}
	}

	// 展开深度：query ?depth=N 覆盖配置值
	ctx = h.injectDepth(ctx, c)
	// 忽略控制：query ?ignore / ?ignoreRef / ?ignoreCascade / ?ignoreAll
	ctx = h.injectIgnore(ctx, c)
	// 字段级控制：query ?fdepth / ?fstop
	ctx = h.injectStop(ctx, c)
	// 字段裁剪：query ?fields=a;b:c;d:[e,f]
	ctx = withFields(ctx, c.Query("fields"))
	// 从 filters 中移除控制参数（避免误传到 Service 层）
	delete(filters, "ignore")
	delete(filters, "ignoreRef")
	delete(filters, "ignoreCascade")
	delete(filters, "ignoreAll")
	delete(filters, "depth")
	delete(filters, "fdepth")
	delete(filters, "fstop")
	delete(filters, "expand")
	delete(filters, "expandAll")
	// keyword 交由 BeforeList hook 处理（业务数据用 fields.xxx 前缀，需动态配置）

	// expand 级联展开覆盖：ListSkipCascades 可通过 GET 参数覆写
	expandAll := c.Query("expandAll") == "true"
	expandList := c.Query("expand")
	if expandAll || expandList != "" {
		ctx = context.WithValue(ctx, listExpandKey{}, listExpand{
			all:    expandAll,
			fields: expandList,
		})
	}

	// 框架层校验：分页参数 + 过滤字段类型（自动推导 + 用户配置）
	if err := validateInput(h.validateRules.List, filters, "list"); err != nil {
		h.handleError(c, err)
		return
	}

	// 创建 Entities holder：_doList 会将原始实体切片写入此指针，供 HTTP 出口处 ResponseMapper 使用。
	// 级联调用（DoList）不创建 holder，因此不会触发映射。
	var entitiesHolder []*M
	ctx = context.WithValue(ctx, entityListCtxKey{}, &entitiesHolder)

	// 转换为具体 List Request 类型（若配置了工厂）
	query := h.newListRequest(filters)
	items, total, err := h.listPipeline(ctx, query, false)
	if err != nil {
		h.handleError(c, err)
		return
	}

	// HTTP 出口处执行 ResponseMapper（仅顶层 HTTP handler，管线/级联不参与）
	if h.config.ResponseMapper != nil && len(entitiesHolder) > 0 {
		for i, entity := range entitiesHolder {
			if i < len(items) {
				items[i] = h.applyResponseMapper(entity, items[i])
			}
		}
	}

	page, pageSize := GetPageParams(c)
	Success(c, gin.H{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// listPipeline 统一管线。
func (h *GenericHandler[M]) listPipeline(ctx context.Context, query any, followPublished bool) (_ []map[string]any, _ int64, err error) {
	start := traceStart(ctx, h.svcName+".list", logrus.Fields{"follow_published": followPublished})
	defer func() { traceEnd(ctx, h.svcName+".list", start, err) }()
	pq, err := h.beforeList(ctx, query)
	if err != nil {
		return nil, 0, err
	}

	list, total, err := h.doList(ctx, pq, followPublished)
	if err != nil {
		return nil, 0, err
	}

	return h.afterList(ctx, list, total)
}

func (h *GenericHandler[M]) beforeList(ctx context.Context, query any) (any, error) {
	if h.hooks.BeforeList != nil {
		return h.hooks.BeforeList(ctx, query)
	}
	return h._beforeList(ctx, query)
}

func (h *GenericHandler[M]) doList(ctx context.Context, query any, followPublished bool) ([]map[string]any, int64, error) {
	if h.hooks.DoList != nil {
		return h.hooks.DoList(ctx, query, followPublished)
	}
	return h._doList(ctx, query, followPublished)
}

func (h *GenericHandler[M]) afterList(ctx context.Context, list []map[string]any, total int64) ([]map[string]any, int64, error) {
	if h.hooks.AfterList != nil {
		return h.hooks.AfterList(ctx, list, total)
	}
	return h._afterList(ctx, list, total)
}
