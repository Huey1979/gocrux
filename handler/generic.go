package handler

import (
	"github.com/Huey1979/gocrux/repository"
	"github.com/Huey1979/gocrux/service"
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

// KeywordMatch 关键词匹配模式
type KeywordMatch string

const (
	KeywordFuzzy KeywordMatch = "fuzzy" // LIKE %keyword%
	KeywordExact KeywordMatch = "exact" // = keyword
)

// KeywordField 关键词搜索字段配置
type KeywordField struct {
	Field string       // DB 列名
	Match KeywordMatch // 匹配模式，默认 fuzzy
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

	// ListSkipCascades List 时默认不展开的级联子表名（约定：List 不展开级联）。
	// nil = 全部展开（向后兼容）。
	// []string{} = 全部跳过（推荐）。
	// []string{"list_layout"} = 仅跳过 list_layout。
	// GET 参数 ?expand=name1,name2 可临时开启指定级联。
	// GET 参数 ?expand_all=true 强制全部展开。
	ListSkipCascades []string

	// KeywordFields 关键字搜索字段列表。
	// nil = 不支持关键字搜索（向后兼容）。
	// GET 参数 ?keyword=xxx 在这些字段上做 OR 模糊/精确匹配。
	// 例：[]KeywordField{{Field:"form_code",Match:KeywordExact},{Field:"form_name",Match:KeywordFuzzy}}
	KeywordFields []KeywordField

	// Validate 输入校验规则（可选）。
	// 为 nil 时使用自动推导的默认规则（从 entity struct 反射类型信息）。
	// 非 nil 时与自动推导规则合并（用户规则覆盖自动规则的同名字段）。
	// 可通过 YAML 批量加载：handler.LoadValidationConfig("configs/validations.yaml")
	// 例：
	//   Validate: &ValidateConfig{
	//     Create: &EndpointRules{"site_code": &FieldRule{Required: true}},
	//     List:   &EndpointRules{"page_size": &FieldRule{Max: Float64Ptr(200)}},
	//   }
	Validate *ValidateConfig
	// NormalizeFields 需表达式规范化的 JSON 字段名
	NormalizeFields []string

	// BatchErrorMode 批量操作错误处理模式（Create）。
	//   "all_or_nothing"（默认）：第一个错误即返回，不写入任何数据。
	//   "collect"：收集所有校验错误后统一返回，标注每条出错数据的索引和字段。
	BatchErrorMode string

	// SkipAutoValidate 跳过框架自动字段校验（用于动态 schema 实体如 BizRecord）。
	// 为 true 时不从 entity struct tag 反射校验规则，完全交由钩子处理。
	SkipAutoValidate bool

	// RejectUnknownFields 拒绝未知字段（默认 false 静默跳过）。
	// 为 true 时，Create/Update 请求中不在 schema 定义内的字段将返回校验错误。
	// List 接口同样生效：?key=val 中 key 不在已知字段或框架控制参数中时报错。
	RejectUnknownFields bool

	// GlobalStore 内存缓存（可选）。nil 时不启用。
	// 内置默认实现：repository.NewMapStore()。
	GlobalStore repository.GlobalStore

	// DateTimeFormat 日期时间格式化字符串（可选）。
	// 例："2006-01-02 15:04:05"。为空时使用 Go 默认 RFC3339 格式。
	DateTimeFormat string
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

	// 合并后的校验规则（自动推导 + 用户配置）
	validateRules struct {
		Create EndpointRules
		Update EndpointRules
		List   EndpointRules
	}
}

// NewGenericHandler 从 Service 注册表创建泛型 Handler（推荐方式）。
func NewGenericHandler[M service.Record](
	reg *service.ServiceRegistry,
	svcName string,
	cfg HandlerConfig[M],
) *GenericHandler[M] {
	svc := service.GetTyped[M](reg, svcName)
	h := &GenericHandler[M]{
		svc:     svc,
		svcName: svcName,
		config:  cfg,
	}
	h.initValidation()
	return h
}

// NewGenericHandlerWithSvc 直接传入 Service 实例创建 Handler。
func NewGenericHandlerWithSvc[M service.Record](
	svc *service.GenericService[M],
	cfg HandlerConfig[M],
) *GenericHandler[M] {
	h := &GenericHandler[M]{
		svc:     svc,
		svcName: "(direct)",
		config:  cfg,
	}
	h.initValidation()
	return h
}

// initValidation 构建合并后的校验规则。
func (h *GenericHandler[M]) initValidation() {
	if h.config.SkipAutoValidate {
		// 动态 schema 实体：跳过自动字段校验，完全交由钩子处理
		h.validateRules.Create = make(EndpointRules)
		h.validateRules.Update = make(EndpointRules)
		h.validateRules.List = make(EndpointRules)
		return
	}

	auto := deriveFieldRules[M]()
	if auto == nil {
		auto = make(EndpointRules)
	}

	// Create 规则：auto + 用户 Create 覆盖
	h.validateRules.Create = mergeRules(auto, nil)
	if h.config.Validate != nil && h.config.Validate.Create != nil {
		h.validateRules.Create = mergeRules(h.validateRules.Create, *h.config.Validate.Create)
	}

	// Update 规则：auto（不含 Required）+ 用户 Update 覆盖
	updateAuto := cloneEndpointRules(auto)
	for k, r := range updateAuto {
		r.Required = false // Update 不该要求全量字段
		updateAuto[k] = r
	}
	h.validateRules.Update = mergeRules(updateAuto, nil)
	if h.config.Validate != nil && h.config.Validate.Update != nil {
		h.validateRules.Update = mergeRules(h.validateRules.Update, *h.config.Validate.Update)
	}

	// List 规则：框架默认 + auto（仅字段类型） + 用户 List 覆盖
	listRules := defaultListRules()
	for k, r := range auto {
		// 仅保留类型信息
		listRules[k] = &FieldRule{Type: r.Type}
	}
	h.validateRules.List = listRules
	if h.config.Validate != nil && h.config.Validate.List != nil {
		h.validateRules.List = mergeRules(listRules, *h.config.Validate.List)
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
