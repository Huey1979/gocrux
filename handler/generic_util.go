package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/Huey1979/gocrux/common"
	"github.com/Huey1979/gocrux/expression"

	errs "github.com/Huey1979/gocrux/errors"
	"github.com/Huey1979/gocrux/constants"
	"github.com/Huey1979/gocrux/service"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 3. map → Request 类型转换
// ============================================================

// newCrudRequest 将原始 map 转换为 CrudRequest[M]。
// 若配置了 ReqFactory，通过 JSON 往返反序列化为具体 Request 类型；
// 否则 fallback 到无 schema 的 MapRequest[M]。
func (h *GenericHandler[M]) newCrudRequest(raw map[string]any) service.CrudRequest[M] {
	if h.config.ReqFactory != nil && h.config.ReqFactory.Create != nil {
		data, err := json.Marshal(raw)
		if err != nil {
			return &MapRequest[M]{data: raw}
		}
		req := h.config.ReqFactory.Create()
		if err := json.Unmarshal(data, req); err != nil {
			return &MapRequest[M]{data: raw}
		}
		return req
	}
	return &MapRequest[M]{data: raw}
}

// newCrudRequestForUpdate 同 newCrudRequest，但使用 Update 工厂。
func (h *GenericHandler[M]) newCrudRequestForUpdate(raw map[string]any) service.CrudRequest[M] {
	factory := h.config.ReqFactory
	if factory != nil && factory.Update != nil {
		data, _ := json.Marshal(raw)
		req := factory.Update()
		if json.Unmarshal(data, req) == nil {
			return req
		}
	}
	// fallback: 尝试用 Create 工厂
	if factory != nil && factory.Create != nil {
		data, _ := json.Marshal(raw)
		req := factory.Create()
		if json.Unmarshal(data, req) == nil {
			return req
		}
	}
	return &MapRequest[M]{data: raw}
}

// newListRequest 将原始 map 转为 List 查询结构体。
// 若未配置 List 工厂，返回原始 map。
func (h *GenericHandler[M]) newListRequest(raw map[string]any) any {
	if h.config.ReqFactory != nil && h.config.ReqFactory.List != nil {
		data, _ := json.Marshal(raw)
		req := h.config.ReqFactory.List()
		if json.Unmarshal(data, req) == nil {
			return req
		}
	}
	return raw
}

// ============================================================
// 4. RegisterRoutes — 路由注册
// ============================================================

// RegisterRoutes 注册标准 CRUD + 版本管理路由到指定 RouterGroup。
// 版本相关的路由（Activate / ListVersions / EditVersion）仅在 Service 启用了 VersionMode 时注册。
func (h *GenericHandler[M]) RegisterRoutes(r gin.IRoutes) {
	p := h.config.PathPrefix
	r.POST(p+"/create", h.Create)
	r.GET(p+"/list", h.List)
	r.GET(p+"/get", h.Get)
	r.POST(p+"/update", h.Update)
	r.POST(p+"/batch-update", h.BatchUpdate)
	r.POST(p+"/delete", h.Delete)
	if h.svc.SupportsVersion() {
		r.POST(p+"/activate", h.Activate)
		r.GET(p+"/versions", h.ListVersions)
		r.POST(p+"/edit-version", h.EditVersion)
		r.GET(p+"/versions-archived", h.ListArchivedVersions)
	}
}

// ============================================================
// 5. 共享 helper
// ============================================================

// userInfo 从 gin.Context 提取已认证的用户信息。
// 未配置 Auth 钩子时返回空 UserInfo + false。
func (h *GenericHandler[M]) userInfo(c *gin.Context) (UserInfo, bool) {
	if h.config.Auth == nil {
		return UserInfo{}, false
	}
	return h.config.Auth.FromContext(c)
}

// checkPerm 执行权限校验（如配置了 Perm 钩子）。
// 校验失败直接通过 c.Abort() 中断请求并返回 403。
// 返回 true 表示通过或未启用权限检查，调用方继续执行。
func (h *GenericHandler[M]) checkPerm(c *gin.Context, action string) bool {
	if h.config.Perm == nil {
		return true
	}
	info, ok := h.userInfo(c)
	if !ok {
		ErrorWithMsg(c, constants.CodeUnauthorized, "未登录")
		return false
	}
	if !h.config.Perm.Check(info, h.svcName, action) {
		ErrorWithMsg(c, constants.CodeForbidden, "无权限")
		return false
	}
	return true
}

// injectDepth 从 HTTP query ?depth=N 与 HandlerConfig.MaxExpandDepth 综合计算展开深度，
// 写入 context。默认深度为 defaultExpandDepth（5），?depth=0 可禁用展开。
func (h *GenericHandler[M]) injectDepth(ctx context.Context, c *gin.Context) context.Context {
	qdStr := c.Query("depth")
	var qd int
	hasQD := false
	if qdStr != "" {
		if n, err := strconv.Atoi(qdStr); err == nil && n >= 0 {
			qd = n
			hasQD = true
		}
	}

	maxDepth := h.config.MaxExpandDepth
	if maxDepth <= 0 {
		maxDepth = defaultExpandDepth
	}

	depth := maxDepth
	if hasQD {
		depth = qd // query 优先
	}

	// 上限：配置的 MaxExpandDepth 作为天花板
	if depth > maxDepth {
		depth = maxDepth
	}

	// 硬上限：禁止无上限递归（如 ?depth=999 → 裁剪为 hardMaxExpandDepth）
	if depth > hardMaxExpandDepth {
		depth = hardMaxExpandDepth
	}

	return withDepth(ctx, depth)
}

// injectIgnore 从 HTTP query params 解析忽略配置，写入 context。
//
// 支持参数：
//
//	?ignore=fieldA,fieldB          → 跳过指定字段的展开
//	?ignore_ref=true               → 跳过所有 References + ChildRefs
//	?ignore_cascade=true           → 跳过所有 Cascades
//	?ignore_all=true               → 跳过全部展开/级联
//
// 未传入任何参数时 ctx 不变（无额外开销）。
func (h *GenericHandler[M]) injectIgnore(ctx context.Context, c *gin.Context) context.Context {
	ignoreStr := c.Query("ignore")
	ignoreRef := c.Query("ignore_ref") == "true"
	ignoreCascade := c.Query("ignore_cascade") == "true"
	ignoreAll := c.Query("ignore_all") == "true"

	if ignoreStr == "" && !ignoreRef && !ignoreCascade && !ignoreAll {
		return ctx
	}

	cfg := &IgnoreConfig{
		All:     ignoreAll,
		Ref:     ignoreRef,
		Cascade: ignoreCascade,
	}
	if ignoreStr != "" {
		parts := strings.Split(ignoreStr, ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				cfg.Fields = append(cfg.Fields, p)
			}
		}
	}

	return withIgnore(ctx, cfg)
}

// injectStop 从 HTTP query params 解析字段级深度/截止配置，写入 context。
//
// 支持参数：
//
//	?fdepth=dept_ulid:1,site_ulid:2          → 覆盖 FieldDepthLimits
//	?fstop=dept_ulid=-department:manager      → 覆盖 FieldStopRules
//
// 每个 fstop 格式：field=rules（逗号分隔的 [-]handler:field）。
// 多规则用多个 fstop 参数。
func (h *GenericHandler[M]) injectStop(ctx context.Context, c *gin.Context) context.Context {
	fdepthStr := c.Query("fdepth")
	fstopValues := c.QueryArray("fstop")

	if fdepthStr == "" && len(fstopValues) == 0 {
		return ctx
	}

	// 解析 HTTP overrides
	var fdOverride map[string]int
	var fsOverride map[string][]StopRule

	if fdepthStr != "" {
		fdOverride = make(map[string]int)
		for _, pair := range splitCSV(fdepthStr) {
			parts := splitN(pair, ":", 2)
			if len(parts) == 2 && parts[0] != "" {
				if n, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && n >= 0 {
					fdOverride[parts[0]] = n
				}
			}
		}
	}

	for _, fstopVal := range fstopValues {
		eqIdx := strings.Index(fstopVal, "=")
		if eqIdx < 0 {
			continue
		}
		field := fstopVal[:eqIdx]
		rulesStr := fstopVal[eqIdx+1:]
		rules, err := parseStopRules(rulesStr)
		if err != nil || len(rules) == 0 {
			continue
		}
		if fsOverride == nil {
			fsOverride = make(map[string][]StopRule)
		}
		fsOverride[field] = rules
	}

	if fdOverride != nil {
		ctx = context.WithValue(ctx, fdOverrideCtxKey{}, fdOverride)
	}
	if fsOverride != nil {
		ctx = context.WithValue(ctx, fsOverrideCtxKey{}, fsOverride)
	}
	return ctx
}

// fdOverrideCtxKey / fsOverrideCtxKey HTTP override context keys。
type fdOverrideCtxKey struct{}
type fsOverrideCtxKey struct{}

// entityGetCtxKey / entityListCtxKey 用于在管线内部向 HTTP handler 透传 Entity 指针。
// HTTP handler 创建 holder（*M 或 *[]*M）存入 context，管线内部 _doGet/_doList 写入 Entity，
// HTTP handler 在管线返回后读取 Entity 并调用 ResponseMapper。
// 级联调用（DoGetByID/DoList）不创建 holder，因此不会触发映射。
type entityGetCtxKey struct{}
type entityListCtxKey struct{}

// getStopCfg 从 context + HandlerConfig 综合获取字段级深度/截止配置。
// context 中的 HTTP overrides 优先于 HandlerConfig 默认值。
func (h *GenericHandler[M]) getStopCfg(ctx context.Context) (map[string]int, map[string][]StopRule) {
	fdLimits := h.config.FieldDepthLimits
	if ov, ok := ctx.Value(fdOverrideCtxKey{}).(map[string]int); ok {
		if fdLimits == nil {
			fdLimits = ov
		} else {
			merged := make(map[string]int, len(fdLimits)+len(ov))
			for k, v := range fdLimits {
				merged[k] = v
			}
			for k, v := range ov {
				merged[k] = v // override
			}
			fdLimits = merged
		}
	}

	fdStops := h.config.FieldStopRules
	if ov, ok := ctx.Value(fsOverrideCtxKey{}).(map[string][]StopRule); ok {
		if fdStops == nil {
			fdStops = ov
		} else {
			merged := make(map[string][]StopRule, len(fdStops)+len(ov))
			for k, v := range fdStops {
				merged[k] = v
			}
			for k, v := range ov {
				merged[k] = v // override
			}
			fdStops = merged
		}
	}

	return fdLimits, fdStops
}

// buildFieldCtx 为展开到子 Handler 构建携带字段级限制的子 context。
// ctx: 当前 handler 的 context（含 depth/visited/fieldLimits）
// parentField: 当前 handler 上触发展开的字段名（References.Field、Cascades.ChildrenField 等）
// targetHandlerName: 目标子 handler 的注册名
func (h *GenericHandler[M]) buildFieldCtx(ctx context.Context, parentField string, targetHandlerName string) context.Context {
	cCtx := ctx

	fdLimits, fdStops := h.getStopCfg(ctx)

	// 1. 字段级深度上限：覆盖传给子 Handler 的全局深度
	if fdLimits != nil {
		if fd, ok := fdLimits[parentField]; ok {
			curDepth, hasDepth := getDepth(cCtx)
			if !hasDepth || fd < curDepth {
				cCtx = withDepth(cCtx, fd)
			}
		}
	}

	// 2. 字段级截止规则 → 转为 fieldLimitMap 注入子 context
	if fdStops != nil {
		if rules, ok := fdStops[parentField]; ok {
			fl := make(fieldLimitMap)
			for _, r := range rules {
				if r.OnHandler == targetHandlerName {
					if r.Stop {
						fl[r.Field] = 0
					} else {
						fl[r.Field] = 1
					}
				}
			}
			if len(fl) > 0 {
				cCtx = withFieldLimits(cCtx, fl)
			}
		}
	}

	return cCtx
}

// effectiveExpandDepth 计算某字段的有效展开深度。
// ctx: 当前 handler 的 context
// hasDepth: 是否设置了全局深度
// fieldName: 当前字段名（resultKey / ChildrenField）
// 优先使用 fieldLimitMap（父 Handler 注入的截止规则），其次全局深度。
func effectiveExpandDepth(ctx context.Context, hasDepth bool, fieldName string) (depth int, shouldExpand bool) {
	fieldLimits := getFieldLimits(ctx)
	if fieldLimits != nil {
		if lim, ok := fieldLimits[fieldName]; ok {
			if lim <= 0 {
				return 0, false // 截止：完全跳过
			}
			return lim, true
		}
	}
	if !hasDepth {
		return 0, true // 未设置深度 → 展开一层，不递归
	}
	curDepth, _ := getDepth(ctx)
	if curDepth <= 0 {
		return 0, false
	}
	return curDepth, true
}

// withEffectiveChildCtx 基于字段有效深度构造子 context（深度减一）。
// ctx: 当前 handler 的 base childCtx
// fieldName: 字段名
func withEffectiveChildCtx(ctx context.Context, fieldName string) context.Context {
	fieldLimits := getFieldLimits(ctx)
	if fieldLimits != nil {
		if lim, ok := fieldLimits[fieldName]; ok && lim > 0 {
			return withDepth(ctx, lim-1)
		}
	}
	return ctx
}

// extractChildData 从原始请求 map 中提取指定字段的子数据数组。
// 例如：raw["domains"] → []map[string]any，每个 map 对应对条子记录。
// 兼容三种数据来源：
//   - HTTP JSON 反序列化：[]any{map[string]any, ...}
//   - Go 代码直构：[]map[string]any{...}
//   - 标量数组 + wrapKey：如 raw["tags"]=[1,2,3] + wrapKey="tag_id" → [{"tag_id":1},{"tag_id":2},{"tag_id":3}]
func extractChildData(raw map[string]any, field string, wrapKey string) []map[string]any {
	v, ok := raw[field]
	if !ok || v == nil {
		return nil
	}
	// 兼容单对象：{...} 自动包裹为 [{...}]
	if m, ok := v.(map[string]any); ok {
		return []map[string]any{m}
	}
	// 反射兜底：兼容 []any / []map[string]any / 标量切片 等任意切片类型
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice {
		return nil
	}
	result := make([]map[string]any, 0, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		item := rv.Index(i).Interface()
		if m, ok := item.(map[string]any); ok {
			result = append(result, m)
		} else if wrapKey != "" {
			// 标量值 → 自动包裹为单字段 map（如 1 → {"tag_id": 1}）
			result = append(result, map[string]any{wrapKey: item})
		}
	}
	return result
}

// marshalToMap 将任意结构体通过 JSON 往返转换为 map[string]any。
func marshalToMap(v any) (map[string]any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, errs.ErrMarshalEntity(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, errs.ErrUnmarshalEntity(err)
	}
	return m, nil
}

// toAnySlice 将任意切片类型（[]any、[]string、[]float64 等）统一转换为 []any。
// 用于 ChildRefs 中 FK 列表的类型兼容：JSON 反序列化后数字列表可能为 []float64 或 []any。
func toAnySlice(v any) []any { return common.ToAnySlice(v) }

// ============================================================
// 6. 级联检查 helper
// ============================================================

// hasCascadeFlag 检查是否存在满足 check 条件的级联关系。
func (h *GenericHandler[M]) hasCascadeFlag(check func(CascadeRelation) bool) bool {
	for _, rel := range h.config.Cascades {
		if check(rel) {
			return true
		}
	}
	return false
}

// cascadeKnownFields 收集所有级联子表占位 key（CascadeRelation.ChildrenField）。
// 这些 key 用于在请求 body 中传递子表数据（如 "domains"），不对应数据库列，
// 需在 RejectUnknownFields 模式下豁免。
func (h *GenericHandler[M]) cascadeKnownFields() []string {
	if len(h.config.Cascades) == 0 {
		return nil
	}
	fields := make([]string, 0, len(h.config.Cascades))
	for _, rel := range h.config.Cascades {
		if rel.ChildrenField != "" {
			fields = append(fields, rel.ChildrenField)
		}
	}
	return fields
}

// applyResponseMapper 将原始 Entity 通过 ResponseMapper 转换为 DTO map，并合并级联展开数据。
// entity: 原始实体指针（*M）。
// expanded: 展开后的完整 map（含 References/Cascades/ChildRefs 注入的额外 key）。
// 返回：DTO 字段 + 级联数据合并后的 map。
// ResponseMapper 为 nil 时直接返回 expanded（零开销）。
func (h *GenericHandler[M]) applyResponseMapper(entity *M, expanded map[string]any) map[string]any {
	if h.config.ResponseMapper == nil {
		return expanded
	}

	dto := h.config.ResponseMapper(*entity)
	dtoData, err := json.Marshal(dto)
	if err != nil {
		return expanded // fallback：映射失败时返回原始数据
	}
	var dtoMap map[string]any
	if err := json.Unmarshal(dtoData, &dtoMap); err != nil {
		return expanded
	}

	// 合并级联展开数据：References / ChildRefs / Cascades 的 result key
	// 这些 key 由 expandGet / _doList 注入到 expanded map 中，不在 Entity 自有字段内
	for _, ref := range h.config.References {
		k := ref.ResultField
		if k == "" {
			k = deriveRefResultKey(ref.Field)
		}
		if v, ok := expanded[k]; ok {
			dtoMap[k] = v
		}
	}
	for _, cr := range h.config.ChildRefs {
		k := cr.ResultField
		if k == "" {
			k = deriveChildRefResultKey(cr.FKListField)
		}
		if v, ok := expanded[k]; ok {
			dtoMap[k] = v
		}
	}
	for _, rel := range h.config.Cascades {
		if v, ok := expanded[rel.ChildrenField]; ok {
			dtoMap[rel.ChildrenField] = v
		}
	}

	return dtoMap
}

// ============================================================
// 7. Create — 管线（HTTP → cascade → before → do → after）
// ============================================================

// listExpandKey 用于在 List handler 中传递级联展开覆写参数。
type listExpandKey struct{}

type listExpand struct {
	all    bool   // expand_all=true → 全部展开
	fields string // expand=name1,name2 → 仅展开指定级联
}

// shouldExpandCascade 判断 List 时是否应展开指定级联。
// 优先级：?expand_all=true > ?expand=list > ListSkipCascades 配置 > 默认不展开。
func (h *GenericHandler[M]) shouldExpandCascade(ctx context.Context, childrenField string) bool {
	// 1. GET 参数 expand_all=true → 全部展开
	if v, ok := ctx.Value(listExpandKey{}).(listExpand); ok && v.all {
		return true
	}
	// 2. GET 参数 expand=name1,name2 → 指定展开
	if v, ok := ctx.Value(listExpandKey{}).(listExpand); ok && v.fields != "" {
		for _, f := range strings.Split(v.fields, ",") {
			if strings.TrimSpace(f) == childrenField {
				return true
			}
		}
		return false
	}
	// 3. ListSkipCascades 配置：nil = 全部展开（向后兼容），[]string{} = 全部跳过
	if h.config.ListSkipCascades == nil {
		return true // 向后兼容：未配置 = 全部展开
	}
	if len(h.config.ListSkipCascades) == 0 {
		return false // 空切片 = 全部跳过
	}
	// 4. 部分跳过：在列表中的跳过
	for _, skip := range h.config.ListSkipCascades {
		if skip == childrenField {
			return false
		}
	}
	return true
}

// rawCreateMapsKey 用于在 createPipeline 中将原始请求 map 透传给 _doCreate，
// 以便级联创建时从父请求中提取子表数据。
type rawCreateMapsKey struct{}

// DoCreate CascadeHandler 接口实现。
// 父 Handler 调用此方法委托子 Handler 创建数据（已在同一事务内）。
func (h *GenericHandler[M]) DoCreate(ctx context.Context, requests []map[string]any) ([]any, error) {
	results, err := h.createPipeline(ctx, requests)
	if err != nil {
		return nil, err
	}
	ids := make([]any, len(results))
	for i, r := range results {
		ids[i] = extractPKFromResult(r)
	}
	return ids, nil
}

// ============================================================
// 15. handleError — 统一错误映射（基于 errors.Is，不使用字符串匹配）
// ============================================================

// handleError 将 Service 层 error 映射为 HTTP 响应。
// 使用 errors.Is 与 errs 包中的哨兵错误精确匹配。
// 错误消息统一透传返回前端，不再对未匹配错误吞掉详情。
func (h *GenericHandler[M]) handleError(c *gin.Context, err error) {
	code := mapServiceError(err)
	InternalErrorWithDetail(c, code, err)
}

// ============================================================
// 8. 跨实体引用解析（级联 create 时子实体引用同批次其他子实体的 ULID）
// ============================================================

// cascadeRefMap 级联创建过程中的临时引用→真实 ULID 映射表。
// key 为 "handlerName:tempRef"，value 为创建后分配的真实 ULID。
// 级联创建多批子实体时共享此映射，后续批次可引用前面已创建实体的 ULID。
type cascadeRefMap map[string]string

// crossRefMarker 占位符前缀。子数据中 FK 值以此前缀开头时，表示需要跨实体引用解析。
// 格式: __ref:handler_name:temp_ref_id__
const crossRefMarker = "__ref:"

// resolveCrossRefs 扫描 data 中所有 map 和 string 值，将 __ref:handler:temp__ 占位符
// 替换为 refMap 中对应的真实 ULID。若占位符引用的目标尚未创建（不在 refMap 中），保持不变。
func resolveCrossRefs(data []map[string]any, refMap cascadeRefMap) {
	if len(refMap) == 0 {
		return
	}
	for _, d := range data {
		resolveCrossRefsInMap(d, refMap)
	}
}

// resolveCrossRefsInMap 递归解析单个 map 中的跨实体引用占位符。
func resolveCrossRefsInMap(m map[string]any, refMap cascadeRefMap) {
	for k, v := range m {
		switch val := v.(type) {
		case string:
			if strings.HasPrefix(val, crossRefMarker) {
				if resolved, ok := refMap[val]; ok {
					m[k] = resolved
				}
			}
		case map[string]any:
			resolveCrossRefsInMap(val, refMap)
		case []any:
			for i, item := range val {
				if sm, ok := item.(map[string]any); ok {
					resolveCrossRefsInMap(sm, refMap)
				} else if s, ok := item.(string); ok && strings.HasPrefix(s, crossRefMarker) {
					if resolved, ok := refMap[s]; ok {
						val[i] = resolved
					}
				}
			}
		}
	}
}

// collectTempRefs 从子数据中收集 _temp_ref → handlerName 的映射。
// 返回 map[_temp_ref]handlerName，供 DoCreate 返回后用真实 ULID 替换。
func collectTempRefs(data []map[string]any, handlerName string) map[string]string {
	out := make(map[string]string)
	for _, d := range data {
		if ref, ok := d["_temp_ref"].(string); ok && ref != "" {
			out[ref] = handlerName
			delete(d, "_temp_ref") // 不写入数据库
		}
	}
	return out
}

// updateRefMap 根据 DoCreate 返回的 PK 列表建立引用映射。
// 假设 data[i] 对应 pks[i]（DoCreate 按顺序返回 PK），
// 但 _temp_ref 已被 collectTempRefs 删除，因此需要额外的 tempRefOrder 来记录顺序。
// 简化方案：在 collectTempRefs 中同时记录 tempRef → index 的对应关系。
type tempRefEntry struct {
	handlerName string
	index       int // 在 childData 切片中的位置
}

// collectTempRefsOrdered 收集 _temp_ref → (handlerName, index) 的映射。
func collectTempRefsOrdered(data []map[string]any, handlerName string) map[string]tempRefEntry {
	out := make(map[string]tempRefEntry)
	for i, d := range data {
		if ref, ok := d["_temp_ref"].(string); ok && ref != "" {
			out[ref] = tempRefEntry{handlerName: handlerName, index: i}
			delete(d, "_temp_ref") // 不写入数据库
		}
	}
	return out
}

// updateRefMap 根据 DoCreate 返回的 PK 列表，将 _temp_ref → PK 写入 refMap。
// tempRefs 为 collectTempRefsOrdered 的返回值，pks 为 DoCreate 按序返回的主键列表。
func updateRefMap(refMap cascadeRefMap, tempRefs map[string]tempRefEntry, pks []any) {
	for ref, entry := range tempRefs {
		if entry.index < len(pks) {
			fullKey := crossRefMarker + entry.handlerName + ":" + ref + "__"
			refMap[fullKey] = fmt.Sprint(pks[entry.index])
		}
	}
}

// normalizeFields 对请求 map 中配置的 JSON 字段做表达式规范化。
// 在 createPipeline / updatePipeline 验证前调用。
func (h *GenericHandler[M]) normalizeFields(rawReqs []map[string]any) {
	if len(h.config.NormalizeFields) == 0 {
		return
	}
	for _, raw := range rawReqs {
		for _, field := range h.config.NormalizeFields {
			if v, ok := raw[field]; ok {
				if s, ok := v.(string); ok && s != "" && s != "{}" {
					if n, err := expression.Normalize(s); err == nil {
						raw[field] = n
					}
				}
			}
		}
	}
}

// pruneFields 按 fields 规则裁剪 map，返回新 map（不修改原数据）。
// 规则格式: key;key:sub;key:[sub1,sub2]
//   "a"       → 保留顶层 a
//   "b:c"     → b 下只留 c
//   "b:[c,d]" → b 下只留 c,d
//   "e:f"     → 数组 e 每个元素只留 f

func splitRule(rule string) (string, []string) {
	idx := strings.Index(rule, ":")
	if idx < 0 {
		return rule, nil
	}
	key := strings.TrimSpace(rule[:idx])
	sub := strings.TrimSpace(rule[idx+1:])
	if strings.HasPrefix(sub, "[") && strings.HasSuffix(sub, "]") {
		inner := sub[1 : len(sub)-1]
		parts := strings.Split(inner, ",")
		cleaned := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				cleaned = append(cleaned, p)
			}
		}
		return key, cleaned
	}
	return key, []string{sub}
}

// ============================================================
// 9. GlobalStore 内存缓存辅助
// ============================================================

// cacheKeyULID 生成 ulid 前缀的缓存 key。
func cacheKeyULID(ulid string) string { return "ulid:" + ulid }

// cacheKeyCode 生成 code 前缀的缓存 key。
func cacheKeyCode(code string) string { return "code:" + code }

// cacheGet 从 GlobalStore 查缓存，提取 entity 的 ulid 和 code 用于多 key 索引。
func (h *GenericHandler[M]) cacheSet(ctx context.Context, entity *M) {
	if h.config.GlobalStore == nil {
		return
	}
	// 尝试提取主键
	if pk := extractPKFromResult(entity); pk != nil {
		h.config.GlobalStore.Set(ctx, cacheKeyULID(fmt.Sprint(pk)), entity)
	}
}

// cacheDelByID 按 ulid 和 code 删除缓存。
func (h *GenericHandler[M]) cacheDelByID(ctx context.Context, id any) {
	if h.config.GlobalStore == nil {
		return
	}
	h.config.GlobalStore.Del(ctx, cacheKeyULID(fmt.Sprint(id)))
}

// deleteCacheIDsKey 用于在 deletePipeline 中将 ids 传递给 _afterDelete 的缓存清理。
type deleteCacheIDsKey struct{}

// ============================================================
// 10. 日期时间格式化
// ============================================================

// formatDateTimes 递归遍历 map，将 RFC3339 格式的时间字符串按 DateTimeFormat 重新格式化。
func formatDateTimes(m map[string]any, layout string) {
	if layout == "" {
		return
	}
	formatDateTimesRecursive(m, layout)
}

// formatDateTimesRecursive 递归处理 map、slice、string 中的时间字符串。
func formatDateTimesRecursive(v any, layout string) {
	switch val := v.(type) {
	case map[string]any:
		for k, vv := range val {
			if s, ok := vv.(string); ok {
				if t, err := parseTimeString(s); err == nil {
					val[k] = t.Format(layout)
				}
			} else {
				formatDateTimesRecursive(vv, layout)
			}
		}
	case []any:
		for i, item := range val {
			if m, ok := item.(map[string]any); ok {
				formatDateTimesRecursive(m, layout)
			} else if s, ok := item.(string); ok {
				if t, err := parseTimeString(s); err == nil {
					val[i] = t.Format(layout)
				}
			}
		}
	}
}

// parseTimeString 尝试解析常见时间格式（RFC3339 / JSON 默认时间格式）。
func parseTimeString(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339Nano, // "2006-01-02T15:04:05.999999999Z07:00"
		time.RFC3339,     // "2006-01-02T15:04:05Z07:00"
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05.000Z",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("not a time string: %s", s)
}
