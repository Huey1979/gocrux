package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/Huey1979/gocrux/constants"
	errs "github.com/Huey1979/gocrux/errors"
	"github.com/Huey1979/gocrux/service"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 1. 辅助类型定义
// ============================================================

// GetRequest 查询请求，统一描述按 ID/Code + FollowPublished 三种查询模式。
type GetRequest struct {
	ID              any    // 精确版本查询（?id=xxx）
	Code            string // 按 Code 查询（?code=xxx）
	FollowPublished bool   // 是否解析为已发布版本（?follow_published=true）
}

// ============================================================
// 2. 基础定义：Config / Handler / 构造函数 / 注入
// ============================================================

// HandlerConfig Handler 配置。
type HandlerConfig[M service.Record] struct {
	// PathPrefix 路由前缀，如 "/api/v1/site"。
	// 基础路由（始终注册 5 条）：
	//   POST /{prefix}/create        → Create
	//   GET  /{prefix}/list          → List
	//   GET  /{prefix}/get           → Get
	//   POST /{prefix}/update        → Update
	//   POST /{prefix}/delete        → Delete
	// 版本路由（仅在 Service.VersionMode=true 时额外注册 3 条）：
	//   POST /{prefix}/activate      → Activate（发布/回滚统一入口）
	//   GET  /{prefix}/versions      → ListVersions
	//   POST /{prefix}/edit-version  → EditVersion
	PathPrefix string

	// Cascades 级联关系声明（可选，向下级联：父→子）。
	// 配置后，Create / Delete / Get / List 自动处理子表联动（List 中批量展开子记录）。
	Cascades []CascadeRelation

	// References 向上级联声明（可选，子→父）。
	// 配置后，Get/List 查询时自动解析本实体的逻辑外键字段（如 site_ulid→site），
	// 并将解析结果挂到返回 map 的对应键下。
	References []ReferenceRelation

	// ChildRefs 向下引用声明（可选，父→子 FK 列表）。
	// 配置后，Get/List 查询时自动将 FK 列表（如 tag_ulids: [1,2,3]）批量解析为完整对象列表。
	// 与 CascadeRelation 不同：仅关联已有实体，不参与级联创建/删除/更新。
	ChildRefs []ChildRefRelation

	// ReqFactory 按操作的 Request 构造器（可选）。
	// 设置后，HTTP 请求的 map 数据会被反序列化为具体 Request 类型，
	// 从而调用其 Validate() 方法进行字段级校验。
	// nil 时 fallback 到 MapRequest[M]（无校验，仅类型转换）。
	ReqFactory *RequestFactory[M]

	// Auth 认证钩子（可选）。
	// 设置后，Handler 在需要用户信息的场景（日志、权限检查等）通过
	// Auth.FromContext(c) 获取 UserInfo。
	// 注意：Auth.Middleware() 需要使用者自行在路由注册时应用。
	Auth Authenticator

	// Perm 权限校验钩子（可选）。
	// 设置后，每个操作（Create/Update/Delete/Get/List…）执行前会调用
	// Perm.Check(info, resource, action) 进行权限验证。
	// resource 为当前实体的 svcName（如 "site", "role"），
	// action 为操作名（如 "create", "update", "delete", "get", "list"）。
	Perm Authorizer

	// MaxExpandDepth 最大展开深度（Get/List 时 References/ChildRefs/Cascades 递归展开层数，默认 0 不递归）。
	// 设置 > 0 后，Get/List 会自动递归展开嵌套的引用和级联数据，每展开一层减一，到 0 时停止。
	// 可通过 HTTP query param ?depth=N 临时覆盖（N 不能超过此值）。
	MaxExpandDepth int

	// FieldDepthLimits 单字段深度上限（可选）。
	// key: 当前 Handler 的字段名（References 的 Field、ChildRefs 的 FKListField、Cascades 的 ChildrenField）。
	// value: 该字段展开时传递给子 Handler 的最大递归层数。
	// 例：{"dept_ulid": 1} → 展开 dept 时子 Handler 最多展开 1 层。
	// 可通过 HTTP query param ?fdepth=dept_ulid:1 临时覆盖。
	FieldDepthLimits map[string]int

	// FieldStopRules 字段级截止规则（可选）。
	// key: 当前 Handler 的字段名（同上）。
	// value: 截止规则列表，控制该字段展开到目标子 Handler 后子 Handler 哪些字段被截止。
	// 例：{"dept_ulid": []StopRule{{OnHandler:"department", Field:"manager", Stop:true}}}
	// 格式对照：-department:manager → {OnHandler:"department", Field:"manager", Stop:true}
	//         department:parent_id → {OnHandler:"department", Field:"parent_id", Stop:false}
	// 可通过 HTTP query param ?fstop=dept_ulid=-department:manager,department:parent_id 临时覆盖。
	FieldStopRules map[string][]StopRule

	// ResponseMapper 将展开后的原始 Entity 实例映射为 API 响应对象（可选）。
	// 入参是展开后的原始实体指针（此时 References/Cascades/ChildRefs 已 inject 到 map 中）。
	// 返回 any 将被 JSON 序列化写入 HTTP response，级联数据（References/Cascades/ChildRefs）
	// 会从展开后的 map 中自动合并回 DTO 输出。
	// 为 nil 时直接返回原始 map（向后兼容，零破坏）。
	// 例：ResponseMapper: func(s *entity.SysSite) any { return s.ToDTO() }
	ResponseMapper func(M) any

	// ListSkipFields List 时跳过的字段名列表（可选，优先级高于 ListKeepFields）。
	// 配置后，_doList 返回前会从每条记录中删除这些字段。
	// 常用于跳过较大的 JSON/Text 字段（如 form_config、entity_config）。
	// 不影响 Get 接口（单条查询始终返回全字段）。
	// 例：[]string{"form_config", "entity_config", "flow_config"}
	ListSkipFields []string

	// ListKeepFields List 时保留的字段名列表（可选，仅 ListSkipFields 为空时生效）。
	// 配置后，_doList 返回前每条记录仅保留这些字段（白名单模式）。
	// 不影响 Get 接口。
	// 例：[]string{"form_ulid", "form_code", "form_name", "form_type"}
	ListKeepFields []string
}

// GenericHandler 泛型 Handler。
//
// 每个实体类型创建一个实例，通过 RegisterRoutes 自动注册标准路由。
//
// 【推荐】使用注册表创建：
//
//	gh := NewGenericHandler[entity.SysRole](reg, "role",
//	    HandlerConfig{PathPrefix: "/api/v1/role"},
//	)
//
// 若需级联支持，额外注入 HandlerRegistry + TxCoordinator：
//
//	gh.SetHandlerReg(handlerReg)
//	gh.SetTxCoord(NewTxCoordinator(db))
//
// 若需自定义钩子：
//
//	gh.SetHooks(customHooks)
//
// 处理流程（以 Create 为例）：
//
//	HTTP 请求 Create(c)
//	│  gin body → []map[string]any          ← HTTP I/O（薄壳）
//	│  ↓
//	│  createPipeline(ctx, rawReqs)         ← 统一管线
//	│  │  beforeCreate → doCreate → afterCreate
//	│  │  （每段 hook!=nil 走钩子，否则 fallback 到 _xxx）
//	│  ↓
//	│  handler.Success(c, ...)              ← HTTP I/O（薄壳）
//
//	级联调用 DoCreate(ctx, rawReqs)
//	│  createPipeline(ctx, rawReqs)         ← 同一管线！
//	│  ↓
//	│  extractPKs → return []any
//
//	两个入口共享同一条管线，钩子在两个场景下都生效。
type GenericHandler[M service.Record] struct {
	svc        *service.GenericService[M]
	svcName    string // 注册表中的名称，仅用于日志/错误信息
	config     HandlerConfig[M]
	hooks      HandlerHooks[M]  // Handler 层钩子（如未设置，fallback 到 _xxx 默认实现）
	handlerReg *HandlerRegistry // 子 Handler 注册表（级联时用于查找子 Handler）
	txCoord    *TxCoordinator   // 事务编排器（级联时用于保证事务一致性）

}

// NewGenericHandler 从 Service 注册表创建泛型 Handler（推荐方式）。
func NewGenericHandler[M service.Record](
	reg *service.ServiceRegistry,
	svcName string,
	cfg HandlerConfig[M],
) *GenericHandler[M] {
	svc := service.GetTyped[M](reg, svcName)
	return &GenericHandler[M]{
		svc:     svc,
		svcName: svcName,
		config:  cfg,
	}
}

// NewGenericHandlerWithSvc 直接传入 Service 实例创建 Handler。
func NewGenericHandlerWithSvc[M service.Record](
	svc *service.GenericService[M],
	cfg HandlerConfig[M],
) *GenericHandler[M] {
	return &GenericHandler[M]{
		svc:     svc,
		svcName: "(direct)",
		config:  cfg,
	}
}

// Service 暴露底层 Service（供自定义 Handler 扩展用）。
func (h *GenericHandler[M]) Service() *service.GenericService[M] {
	return h.svc
}

// SetHooks 注入 Handler 层钩子。
func (h *GenericHandler[M]) SetHooks(hooks HandlerHooks[M]) {
	h.hooks = hooks
}

// SetHandlerReg 注入 Handler 注册表（级联支持）。
func (h *GenericHandler[M]) SetHandlerReg(reg *HandlerRegistry) {
	h.handlerReg = reg
}

// SetTxCoord 注入事务编排器（级联支持）。
func (h *GenericHandler[M]) SetTxCoord(tc *TxCoordinator) {
	h.txCoord = tc
}

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
	r.POST(p+"/delete", h.Delete)
	if h.svc.SupportsVersion() {
		r.POST(p+"/activate", h.Activate)
		r.GET(p+"/versions", h.ListVersions)
		r.POST(p+"/edit-version", h.EditVersion)
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
// 写入 context。若两者均未设置则不注入（保持旧行为：展开一层不递归）。
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

	// 仅当配置或 query 明确设置了 depth 时才注入 context
	if maxDepth <= 0 && !hasQD {
		return ctx // 未设置 → 旧行为
	}

	depth := 0
	if hasQD {
		depth = qd // query 优先
	} else if maxDepth > 0 {
		depth = maxDepth // fallback 到配置
	}

	// 上限：配置的 MaxExpandDepth 作为天花板
	if maxDepth > 0 && depth > maxDepth {
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
//	?ignoreRef=true                → 跳过所有 References + ChildRefs
//	?ignoreCascade=true            → 跳过所有 Cascades
//	?ignoreAll=true                → 跳过全部展开/级联
//
// 未传入任何参数时 ctx 不变（无额外开销）。
func (h *GenericHandler[M]) injectIgnore(ctx context.Context, c *gin.Context) context.Context {
	ignoreStr := c.Query("ignore")
	ignoreRef := c.Query("ignoreRef") == "true"
	ignoreCascade := c.Query("ignoreCascade") == "true"
	ignoreAll := c.Query("ignoreAll") == "true"

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

// toAnySlice 将任意切片类型（[]any、[]string、[]float64 等）统一转换为 []any。
// 用于 ChildRefs 中 FK 列表的类型兼容：JSON 反序列化后数字列表可能为 []float64 或 []any。
func toAnySlice(v any) []any {
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

// ============================================================
// 6. 级联检查 helper（hasCascadesOnXxx）
// ============================================================

func (h *GenericHandler[M]) hasCascadesOnCreate() bool {
	for _, rel := range h.config.Cascades {
		if rel.OnCreate {
			return true
		}
	}
	return false
}

func (h *GenericHandler[M]) hasCascadesOnDelete() bool {
	for _, rel := range h.config.Cascades {
		if rel.OnDelete {
			return true
		}
	}
	return false
}

func (h *GenericHandler[M]) hasCascadesOnUpdate() bool {
	for _, rel := range h.config.Cascades {
		if rel.OnUpdate {
			return true
		}
	}
	return false
}

func (h *GenericHandler[M]) hasCascadesOnActivate() bool {
	for _, rel := range h.config.Cascades {
		if rel.OnActivate {
			return true
		}
	}
	return false
}

func (h *GenericHandler[M]) hasCascadesOnEditVersion() bool {
	for _, rel := range h.config.Cascades {
		if rel.OnEditVersion {
			return true
		}
	}
	return false
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

// PKField CascadeHandler 接口实现，返回实体 M 的主键数据库列名。
func (h *GenericHandler[M]) PKField() string {
	var zero M
	return zero.PKField()
}

// Create 创建记录
// POST /{prefix}/create
func (h *GenericHandler[M]) Create(c *gin.Context) {
	if !h.checkPerm(c, "create") {
		return
	}
	ctx := c.Request.Context()

	var rawReqs []map[string]any
	if err := c.ShouldBindJSON(&rawReqs); err != nil {
		h.handleError(c, err)
		return
	}
	if len(rawReqs) == 0 {
		h.handleError(c, errs.ErrInvalidParam)
		return
	}

	result, err := h.createPipeline(ctx, rawReqs)
	if err != nil {
		h.handleError(c, err)
		return
	}
	Success(c, gin.H{"items": result})
}

// createPipeline 统一管线：HTTP 和级联共享。
func (h *GenericHandler[M]) createPipeline(ctx context.Context, rawReqs []map[string]any) ([]*M, error) {
	reqs := make([]service.CrudRequest[M], len(rawReqs))
	for i, raw := range rawReqs {
		reqs[i] = h.newCrudRequest(raw)
	}

	// 字段级校验（具体 Request 的 Validate()，MapRequest 为 no-op）
	for i, r := range reqs {
		if err := r.Validate(); err != nil {
			return nil, errs.ErrReqValidation(i, err)
		}
	}

	// 将原始 map 注入 ctx，供 _doCreate 级联创建时提取子数据
	ctx = context.WithValue(ctx, rawCreateMapsKey{}, rawReqs)

	processed, err := h.beforeCreate(ctx, reqs)
	if err != nil {
		return nil, err
	}

	created, err := h.doCreate(ctx, processed)
	if err != nil {
		return nil, err
	}

	return h.afterCreate(ctx, created)
}

func (h *GenericHandler[M]) beforeCreate(ctx context.Context, input []service.CrudRequest[M]) ([]service.CrudRequest[M], error) {
	if h.hooks.BeforeCreate != nil {
		return h.hooks.BeforeCreate(ctx, input)
	}
	return h._beforeCreate(ctx, input)
}

func (h *GenericHandler[M]) doCreate(ctx context.Context, input []service.CrudRequest[M]) ([]*M, error) {
	if h.hooks.DoCreate != nil {
		return h.hooks.DoCreate(ctx, input)
	}
	return h._doCreate(ctx, input)
}

func (h *GenericHandler[M]) afterCreate(ctx context.Context, result []*M) ([]*M, error) {
	if h.hooks.AfterCreate != nil {
		return h.hooks.AfterCreate(ctx, result)
	}
	return h._afterCreate(ctx, result)
}

// ============================================================
// 8. Update — 管线（HTTP → cascade → before → do → after）
// ============================================================

// rawUpdateMapsKey 用于在 updatePipeline 中将原始请求 map 切片透传给 _doUpdate，
// 以便级联更新时从父请求中提取子表数据。
type rawUpdateMapsKey struct{}

// DoUpdate CascadeHandler 接口实现。
// 注入 FK 后统一走 updatePipeline，享受完整的 before→do→after 管线。
func (h *GenericHandler[M]) DoUpdate(ctx context.Context, fkField string, fkValue any, childrenData []map[string]any, parentVersioned bool) error {
	// 注入 FK 到每条子数据
	for i := range childrenData {
		childrenData[i][fkField] = fkValue
	}
	_, err := h.updatePipeline(ctx, childrenData, parentVersioned)
	return err
}

// Update 编辑记录
// POST /{prefix}/update
func (h *GenericHandler[M]) Update(c *gin.Context) {
	if !h.checkPerm(c, "update") {
		return
	}
	ctx := c.Request.Context()

	var raw map[string]any
	if err := c.ShouldBindJSON(&raw); err != nil {
		h.handleError(c, err)
		return
	}

	rid, ok := raw["id"]
	if !ok || rid == nil {
		h.handleError(c, errs.ErrInvalidParam)
		return
	}
	_ = rid

	results, err := h.updatePipeline(ctx, []map[string]any{raw}, false)
	if err != nil {
		h.handleError(c, err)
		return
	}
	Success(c, gin.H{"data": results[0]})
}

// updatePipeline 统一管线（HTTP 入口 + 级联入口共享）。
// rawReqs 为待处理的原始请求 map 列表；parentVersioned 表示父链是否已出现版本化节点。
func (h *GenericHandler[M]) updatePipeline(ctx context.Context, rawReqs []map[string]any, parentVersioned bool) ([]*M, error) {
	reqs := make([]service.CrudRequest[M], len(rawReqs))
	for i, raw := range rawReqs {
		reqs[i] = h.newCrudRequestForUpdate(raw)
	}

	for i, r := range reqs {
		if err := r.Validate(); err != nil {
			return nil, errs.ErrUpdateReqValidation(i, err)
		}
	}

	// 若配置了级联更新，将原始 maps 注入 ctx，供 _doUpdate 提取子数据
	if h.hasCascadesOnUpdate() {
		ctx = context.WithValue(ctx, rawUpdateMapsKey{}, rawReqs)
	}

	processed, err := h.beforeUpdate(ctx, reqs, parentVersioned)
	if err != nil {
		return nil, err
	}

	results, err := h.doUpdate(ctx, processed, parentVersioned)
	if err != nil {
		return nil, err
	}

	return h.afterUpdate(ctx, results, parentVersioned)
}

func (h *GenericHandler[M]) beforeUpdate(ctx context.Context, reqs []service.CrudRequest[M], parentVersioned bool) ([]service.CrudRequest[M], error) {
	if h.hooks.BeforeUpdate != nil {
		return h.hooks.BeforeUpdate(ctx, reqs, parentVersioned)
	}
	return h._beforeUpdate(ctx, reqs, parentVersioned)
}

func (h *GenericHandler[M]) doUpdate(ctx context.Context, reqs []service.CrudRequest[M], parentVersioned bool) ([]*M, error) {
	if h.hooks.DoUpdate != nil {
		return h.hooks.DoUpdate(ctx, reqs, parentVersioned)
	}
	return h._doUpdate(ctx, reqs, parentVersioned)
}

func (h *GenericHandler[M]) afterUpdate(ctx context.Context, results []*M, parentVersioned bool) ([]*M, error) {
	if h.hooks.AfterUpdate != nil {
		return h.hooks.AfterUpdate(ctx, results, parentVersioned)
	}
	return h._afterUpdate(ctx, results, parentVersioned)
}

// ============================================================
// 9. Delete — 管线（HTTP → cascade → before → do → after）
// ============================================================

// DoDelete CascadeHandler 接口实现。
func (h *GenericHandler[M]) DoDelete(ctx context.Context, ids []any) error {
	return h.deletePipeline(ctx, ids, nil)
}

// DoDeleteByFK CascadeHandler 接口实现。
// 按外键字段批量软删除子记录（用于级联删除）。
// fkField 为数据库列名（与 JSON 字段名一致），fkValues 为父记录主键列表。
func (h *GenericHandler[M]) DoDeleteByFK(ctx context.Context, fkField string, fkValues []any) error {
	if len(fkValues) == 0 {
		return nil
	}
	return h.svc.DeleteByFK(ctx, fkField, fkValues)
}

// Delete 删除记录（逻辑删除）
// POST /{prefix}/delete
func (h *GenericHandler[M]) Delete(c *gin.Context) {
	if !h.checkPerm(c, "delete") {
		return
	}
	ctx := c.Request.Context()

	var raw struct {
		IDs   []any `json:"ids"`
		Codes []any `json:"codes"`
	}
	if err := c.ShouldBindJSON(&raw); err != nil {
		h.handleError(c, err)
		return
	}
	if len(raw.IDs) == 0 {
		h.handleError(c, errs.ErrInvalidParam)
		return
	}

	if err := h.deletePipeline(ctx, raw.IDs, raw.Codes); err != nil {
		h.handleError(c, err)
		return
	}
	SuccessWithMessage(c, "删除成功", nil)
}

// deletePipeline 统一管线。
func (h *GenericHandler[M]) deletePipeline(ctx context.Context, ids, codes any) error {
	pid, pdata, err := h.beforeDelete(ctx, ids, codes)
	if err != nil {
		return err
	}

	if err := h.doDelete(ctx, pid, pdata); err != nil {
		return err
	}

	return h.afterDelete(ctx)
}

func (h *GenericHandler[M]) beforeDelete(ctx context.Context, ids, codes any) (any, any, error) {
	if h.hooks.BeforeDelete != nil {
		return h.hooks.BeforeDelete(ctx, ids, codes)
	}
	return h._beforeDelete(ctx, ids, codes)
}

func (h *GenericHandler[M]) doDelete(ctx context.Context, ids, codes any) error {
	if h.hooks.DoDelete != nil {
		return h.hooks.DoDelete(ctx, ids, codes)
	}
	return h._doDelete(ctx, ids, codes)
}

func (h *GenericHandler[M]) afterDelete(ctx context.Context) error {
	if h.hooks.AfterDelete != nil {
		return h.hooks.AfterDelete(ctx)
	}
	return h._afterDelete(ctx)
}

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
	Success(c, gin.H{"data": result})
}

// getPipeline 统一管线（HTTP 入口共享）。
func (h *GenericHandler[M]) getPipeline(ctx context.Context, req *GetRequest) (map[string]any, error) {
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

	// visited 防循环：若当前记录已在此链条中出现过（如 site→dept→site），停止展开
	pk := extractPKFromResult(result)
	if isVisited(ctx, h.svcName, fmt.Sprint(pk)) {
		return out, nil
	}

	// 深度检查：仅在未设置 depth 或 depth > 0 时展开
	curDepth, hasDepth := getDepth(ctx)

	// 构造基础子 context：visited 追踪（不含深度，由各字段单独控制）
	baseChildCtx := ctx
	if pk != nil && pk != "" {
		baseChildCtx = addVisited(baseChildCtx, h.svcName, fmt.Sprint(pk))
	}
	// 基础深度：全局 depth-1
	if hasDepth {
		baseChildCtx = withDepth(baseChildCtx, curDepth-1)
	}

	// 2. 向上级联：解析本实体的引用字段
	if h.handlerReg != nil && (!hasDepth || curDepth > 0) {
		for _, ref := range h.config.References {
			resultKey := ref.ResultField
			if resultKey == "" {
				resultKey = deriveRefResultKey(ref.Field)
			}
			// 忽略控制：ignoreAll / ignoreRef / ignore=resultKey
			if shouldIgnoreRef(ctx) || shouldIgnoreField(ctx, resultKey) {
				continue
			}
			// 字段级截止：检查父 Handler 注入的 fieldLimitMap
			effDepth, ok := effectiveExpandDepth(ctx, hasDepth, resultKey)
			if !ok {
				continue
			}

			refHandler := h.handlerReg.Get(ref.HandlerName)
			if refHandler == nil {
				continue
			}

			fkVal, ok := out[ref.Field]
			if !ok || fkVal == nil || fkVal == "" {
				continue
			}

			// 构造字段级子 context：含字段深度上限 + 截止规则
			refCtx := h.buildFieldCtx(baseChildCtx, ref.Field, ref.HandlerName)
			// 若有字段级限深，覆盖全局深度
			if fieldLimits := getFieldLimits(ctx); fieldLimits != nil {
				if _, hasFieldLimit := fieldLimits[resultKey]; hasFieldLimit || effDepth != curDepth {
					refCtx = withDepth(refCtx, effDepth-1)
				}
			}

			parentRecord, err := refHandler.DoGetByID(refCtx, fkVal)
			if err != nil {
				return nil, errs.ErrRefResolve(ref.HandlerName, err)
			}

			out[resultKey] = parentRecord
		}
	}

	// 2.5. 向下引用：将 FK 列表解析为完整子对象列表（ChildRefs）
	if h.handlerReg != nil && (!hasDepth || curDepth > 0) {
		for _, cr := range h.config.ChildRefs {
			resultKey := cr.ResultField
			if resultKey == "" {
				resultKey = deriveChildRefResultKey(cr.FKListField)
			}
			// 忽略控制：ignoreAll / ignoreRef / ignore=resultKey
			if shouldIgnoreRef(ctx) || shouldIgnoreField(ctx, resultKey) {
				continue
			}
			// 字段级截止
			_, ok := effectiveExpandDepth(ctx, hasDepth, resultKey)
			if !ok {
				continue
			}

			refHandler := h.handlerReg.Get(cr.HandlerName)
			if refHandler == nil {
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
	if h.handlerReg != nil && (!hasDepth || curDepth > 0) {
		for _, rel := range h.config.Cascades {
			// 忽略控制：ignoreAll / ignoreCascade / ignore=ChildrenField
			if shouldIgnoreCascade(ctx) || shouldIgnoreField(ctx, rel.ChildrenField) {
				continue
			}
			// 字段级截止
			_, ok := effectiveExpandDepth(ctx, hasDepth, rel.ChildrenField)
			if !ok {
				continue
			}

			childHandler := h.handlerReg.Get(rel.HandlerName)
			if childHandler == nil {
				continue
			}

			pk := extractPKFromResult(result)
			if pk == nil || pk == "" {
				continue
			}

			// 构造字段级子 context
			cascCtx := h.buildFieldCtx(baseChildCtx, rel.ChildrenField, rel.HandlerName)

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
func deriveRefResultKey(field string) string {
	s := field
	if i := len(s) - 5; i > 0 && s[i:] == "_ulid" {
		s = s[:i]
	}
	return s
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
		if len(vals) > 0 {
			filters[key] = vals[0]
		}
	}

	// 展开深度：query ?depth=N 覆盖配置值
	ctx = h.injectDepth(ctx, c)
	// 忽略控制：query ?ignore / ?ignoreRef / ?ignoreCascade / ?ignoreAll
	ctx = h.injectIgnore(ctx, c)
	// 字段级控制：query ?fdepth / ?fstop
	ctx = h.injectStop(ctx, c)
	// 从 filters 中移除控制参数（避免误传到 Service 层）
	delete(filters, "ignore")
	delete(filters, "ignoreRef")
	delete(filters, "ignoreCascade")
	delete(filters, "ignoreAll")
	delete(filters, "depth")
	delete(filters, "fdepth")
	delete(filters, "fstop")

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
func (h *GenericHandler[M]) listPipeline(ctx context.Context, query any, followPublished bool) ([]map[string]any, int64, error) {
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

// ============================================================
// 12. Activate — 管线（HTTP → cascade → before → do → after）
// ============================================================

// DoActivate CascadeHandler 接口实现。
// 激活 / 发布版本并级联到子 Handler。
// - 版本化自身：调用 svc.Activate，然后级联
// - 非版本化自身：空操作（svc 无 Activate 语义），仅级联
func (h *GenericHandler[M]) DoActivate(ctx context.Context, id any) error {
	return h.activatePipeline(ctx, id)
}

// Activate 激活版本（发布 / 回滚统一入口）
// POST /{prefix}/activate
func (h *GenericHandler[M]) Activate(c *gin.Context) {
	if !h.checkPerm(c, "activate") {
		return
	}
	ctx := c.Request.Context()

	var raw struct {
		ID any `json:"id"`
	}
	if err := c.ShouldBindJSON(&raw); err != nil {
		h.handleError(c, err)
		return
	}
	if raw.ID == nil {
		h.handleError(c, errs.ErrInvalidParam)
		return
	}

	if err := h.activatePipeline(ctx, raw.ID); err != nil {
		h.handleError(c, err)
		return
	}
	SuccessWithMessage(c, "操作成功", nil)
}

// activatePipeline 统一管线。
func (h *GenericHandler[M]) activatePipeline(ctx context.Context, id any) error {
	pid, err := h.beforeActivate(ctx, id)
	if err != nil {
		return err
	}

	if err := h.doActivate(ctx, pid); err != nil {
		return err
	}

	return h.afterActivate(ctx)
}

func (h *GenericHandler[M]) beforeActivate(ctx context.Context, id any) (any, error) {
	if h.hooks.BeforeActivate != nil {
		return h.hooks.BeforeActivate(ctx, id)
	}
	return h._beforeActivate(ctx, id)
}

func (h *GenericHandler[M]) doActivate(ctx context.Context, id any) error {
	if h.hooks.DoActivate != nil {
		return h.hooks.DoActivate(ctx, id)
	}
	return h._doActivate(ctx, id)
}

func (h *GenericHandler[M]) afterActivate(ctx context.Context) error {
	if h.hooks.AfterActivate != nil {
		return h.hooks.AfterActivate(ctx)
	}
	return h._afterActivate(ctx)
}

// ============================================================
// 13. ListVersions — 管线（HTTP → cascade → before → do → after）
// ============================================================

// DoListVersions CascadeHandler 接口实现。
// 版本化：调用 svc.ListVersions，marshal 为 map 列表返回。
// 非版本化：返回空列表。
func (h *GenericHandler[M]) DoListVersions(ctx context.Context, id any, code string) ([]map[string]any, error) {
	records, err := h.listVersionsPipeline(ctx, id, code)
	if err != nil {
		return nil, err
	}

	result := make([]map[string]any, len(records))
	for i, r := range records {
		data, err := json.Marshal(r)
		if err != nil {
			return nil, errs.ErrMarshalVersion(err)
		}
		if err := json.Unmarshal(data, &result[i]); err != nil {
			return nil, errs.ErrUnmarshalVersion(err)
		}
	}
	return result, nil
}

// ListVersions 获取版本列表
// GET /{prefix}/versions?code=xxx 或 ?id=xxx
func (h *GenericHandler[M]) ListVersions(c *gin.Context) {
	if !h.checkPerm(c, "versions") {
		return
	}
	ctx := c.Request.Context()

	rid := c.Query("id")
	rcode := c.Query("code")
	if rid == "" && rcode == "" {
		h.handleError(c, errs.ErrInvalidParam)
		return
	}

	versions, err := h.listVersionsPipeline(ctx, rid, rcode)
	if err != nil {
		h.handleError(c, err)
		return
	}
	Success(c, gin.H{"versions": versions})
}

// listVersionsPipeline 统一管线。
func (h *GenericHandler[M]) listVersionsPipeline(ctx context.Context, id any, code string) ([]M, error) {
	pid, pcode, err := h.beforeListVersions(ctx, id, code)
	if err != nil {
		return nil, err
	}

	results, err := h.doListVersions(ctx, pid, pcode)
	if err != nil {
		return nil, err
	}

	return h.afterListVersions(ctx, results)
}

func (h *GenericHandler[M]) beforeListVersions(ctx context.Context, id any, code string) (any, string, error) {
	if h.hooks.BeforeListVersions != nil {
		return h.hooks.BeforeListVersions(ctx, id, code)
	}
	return h._beforeListVersions(ctx, id, code)
}

func (h *GenericHandler[M]) doListVersions(ctx context.Context, id any, code string) ([]M, error) {
	if h.hooks.DoListVersions != nil {
		return h.hooks.DoListVersions(ctx, id, code)
	}
	return h._doListVersions(ctx, id, code)
}

func (h *GenericHandler[M]) afterListVersions(ctx context.Context, result []M) ([]M, error) {
	if h.hooks.AfterListVersions != nil {
		return h.hooks.AfterListVersions(ctx, result)
	}
	return h._afterListVersions(ctx, result)
}

// ============================================================
// 14. EditVersion — 管线（HTTP → cascade → before → do → after）
// ============================================================

// DoEditVersion CascadeHandler 接口实现。
// 修改版本元数据并级联到子 Handler。
// - 版本化自身：调用 svc.EditVersion，然后级联
// - 非版本化自身：空操作（svc 无 EditVersion 语义），仅级联
func (h *GenericHandler[M]) DoEditVersion(ctx context.Context, id any, patches map[string]any) (map[string]any, error) {
	result, err := h.editVersionPipeline(ctx, id, patches)
	if err != nil {
		return nil, err
	}

	// 非版本化 Handler 返回 nil（自身空操作），级联已由 _doEditVersion 完成
	if result == nil {
		return map[string]any{}, nil
	}

	data, err := json.Marshal(result)
	if err != nil {
		return nil, errs.ErrMarshalEditVersion(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, errs.ErrUnmarshalEditVersion(err)
	}
	return m, nil
}

// EditVersion 修改版本元数据（状态、备注）
// POST /{prefix}/edit-version
func (h *GenericHandler[M]) EditVersion(c *gin.Context) {
	if !h.checkPerm(c, "edit-version") {
		return
	}
	ctx := c.Request.Context()

	var raw struct {
		ID      any            `json:"id"`
		Patches map[string]any `json:"patches"`
	}
	if err := c.ShouldBindJSON(&raw); err != nil {
		h.handleError(c, err)
		return
	}
	if raw.ID == nil || len(raw.Patches) == 0 {
		h.handleError(c, errs.ErrInvalidParam)
		return
	}

	result, err := h.editVersionPipeline(ctx, raw.ID, raw.Patches)
	if err != nil {
		h.handleError(c, err)
		return
	}
	Success(c, gin.H{"data": result})
}

// editVersionPipeline 统一管线。
func (h *GenericHandler[M]) editVersionPipeline(ctx context.Context, id any, patches map[string]any) (*M, error) {
	pid, ppatches, err := h.beforeEditVersion(ctx, id, patches)
	if err != nil {
		return nil, err
	}

	result, err := h.doEditVersion(ctx, pid, ppatches)
	if err != nil {
		return nil, err
	}

	return h.afterEditVersion(ctx, result)
}

func (h *GenericHandler[M]) beforeEditVersion(ctx context.Context, id any, patches map[string]any) (any, map[string]any, error) {
	if h.hooks.BeforeEditVersion != nil {
		return h.hooks.BeforeEditVersion(ctx, id, patches)
	}
	return h._beforeEditVersion(ctx, id, patches)
}

func (h *GenericHandler[M]) doEditVersion(ctx context.Context, id any, patches map[string]any) (*M, error) {
	if h.hooks.DoEditVersion != nil {
		return h.hooks.DoEditVersion(ctx, id, patches)
	}
	return h._doEditVersion(ctx, id, patches)
}

func (h *GenericHandler[M]) afterEditVersion(ctx context.Context, result *M) (*M, error) {
	if h.hooks.AfterEditVersion != nil {
		return h.hooks.AfterEditVersion(ctx, result)
	}
	return h._afterEditVersion(ctx, result)
}

// ============================================================
// 15. handleError — 统一错误映射（基于 errors.Is，不使用字符串匹配）
// ============================================================

// handleError 将 Service 层 error 映射为 HTTP 响应。
// 使用 errors.Is 与 errs 包中的哨兵错误精确匹配。
// 未匹配到的错误归为 InternalError（日志记录 + 友好提示）。
func (h *GenericHandler[M]) handleError(c *gin.Context, err error) {
	code := mapServiceError(err)
	if code == constants.CodeInternalError {
		InternalError(c, err)
		return
	}
	ErrorWithMsg(c, code, err.Error())
}
