package handler

import (
	"context"
	"fmt"
	"github.com/Huey1979/gocrux/common"
	"strings"
)

// ============================================================
// depthCtx — 展开深度控制
//
// 通过 context 传递剩余展开层数，每展开一层减一，<=0 停止。
// ctx 中不存在 depth 值时，保持旧行为（展开一层，不做递归）。
// ============================================================

// hardMaxExpandDepth 全局硬上限，无论 query ?depth=N 或 MaxExpandDepth 设置为何值，
// 实际递归层数不可超过此值。防止无上限递归。
const hardMaxExpandDepth = 10

// defaultExpandDepth 默认展开深度，当 Handler 未显式配置 MaxExpandDepth 时使用。
// 设为 hardMaxExpandDepth 以支持深层嵌套（form→layout→section→field→ref→entity 约7层）
const defaultExpandDepth = hardMaxExpandDepth

type depthCtxKey struct{}

// withDepth 将剩余展开深度写入 context。
func withDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, depthCtxKey{}, depth)
}

// getDepth 返回剩余展开深度，以及是否明确设置了深度。
// 未设置时返回 (0, false) → 保持旧行为（展开一层，不递归）。
func getDepth(ctx context.Context) (int, bool) {
	d, ok := ctx.Value(depthCtxKey{}).(int)
	return d, ok
}

// ============================================================
// ignoreCtx — 忽略展开/级联控制
//
// 通过 context 传递忽略配置，支持以下 query param：
//
//	?ignore=fieldA,fieldB         → 跳过指定 ResultField/ChildrenField 的展开
//	?ignore_ref=true              → 跳过所有 References + ChildRefs 展开
//	?ignore_cascade=true          → 跳过所有 Cascades 展开
//	?ignore_all=true              → 跳过所有展开和级联（仅返回裸数据）
//
// 优先级：ignore_all > ignore_ref/ignore_cascade > ignore
// ============================================================

type ignoreCtxKey struct{}

// IgnoreConfig 描述当前请求中需跳过的展开/级联配置。
type IgnoreConfig struct {
	// Fields 需跳过的具体字段名列表（匹配 ResultField、ChildrenField）。
	Fields []string

	// All 跳过所有展开和级联（仅返回裸 map 数据）。
	All bool

	// Ref 跳过所有 References + ChildRefs 展开。
	Ref bool

	// Cascade 跳过所有 Cascades 展开。
	Cascade bool
}

// withIgnore 将忽略配置写入 context。
func withIgnore(ctx context.Context, cfg *IgnoreConfig) context.Context {
	return context.WithValue(ctx, ignoreCtxKey{}, cfg)
}

// getIgnore 从 context 获取忽略配置，未设置时返回 nil。
func getIgnore(ctx context.Context) *IgnoreConfig {
	cfg, _ := ctx.Value(ignoreCtxKey{}).(*IgnoreConfig)
	return cfg
}

// shouldIgnoreField 判断指定字段名是否应被忽略展开。
// name 为展开结果的键名（References 的 ResultField、Cascades 的 ChildrenField 等）。
func shouldIgnoreField(ctx context.Context, name string) bool {
	ic := getIgnore(ctx)
	if ic == nil {
		return false
	}
	if ic.All {
		return true
	}
	for _, f := range ic.Fields {
		if f == name {
			return true
		}
	}
	return false
}

// shouldIgnoreRef 判断是否应跳过所有 References/ChildRefs 展开。
func shouldIgnoreRef(ctx context.Context) bool {
	ic := getIgnore(ctx)
	return ic != nil && (ic.All || ic.Ref)
}

// shouldIgnoreCascade 判断是否应跳过所有 Cascades 展开。
func shouldIgnoreCascade(ctx context.Context) bool {
	ic := getIgnore(ctx)
	return ic != nil && (ic.All || ic.Cascade)
}

// ============================================================

type fieldsCtxKey struct{}

// withFields 将字段裁剪规则写入 context。
func withFields(ctx context.Context, fields string) context.Context {
	if fields == "" {
		return ctx
	}
	return context.WithValue(ctx, fieldsCtxKey{}, fields)
}

// getFields 从 context 获取字段裁剪规则。
func getFields(ctx context.Context) string {
	f, _ := ctx.Value(fieldsCtxKey{}).(string)
	return f
}

// ============================================================
// visitedCtx — 展开链条追踪（防跨实体循环引用 A→B→A）
//
// 使用不可变语义的 visitedSet，存入 context。
// 每次 addVisited 创建新的 context 层，上层保持不变。
// 同一层级的多个子 Handler 各自拥有独立的 visited 副本，互不干扰。
// ============================================================

type visitedCtxKey struct{}

// visitedSet 展开链条上已访问的记录集合。
// key = "handlerName:recordID"（如 "dept:01J..."）。
type visitedSet map[string]bool

// addVisited 将 (handlerName, id) 加入 visited set，返回新 ctx。
// 创建新的 map 副本，不影响上层调用者的 visited。
func addVisited(ctx context.Context, handlerName, id string) context.Context {
	var newSet visitedSet
	if existing, ok := ctx.Value(visitedCtxKey{}).(visitedSet); ok && existing != nil {
		newSet = make(visitedSet, len(existing)+1)
		for k, v := range existing {
			newSet[k] = v
		}
	} else {
		newSet = make(visitedSet)
	}
	newSet[handlerName+":"+id] = true
	return context.WithValue(ctx, visitedCtxKey{}, newSet)
}

// isVisited 检查 (handlerName, id) 是否已出现。
func isVisited(ctx context.Context, handlerName, id string) bool {
	vs, ok := ctx.Value(visitedCtxKey{}).(visitedSet)
	if !ok || vs == nil {
		return false
	}
	return vs[fmt.Sprintf("%s:%s", handlerName, id)]
}

// canExpandToCheck 统一的展开前置检查（visited + depth）。
// 在每次级联/引用展开到子 handler 之前调用。
// 返回新 ctx（含 visited 追踪 + 深度递减）和是否允许继续。
func canExpandTo(ctx context.Context, handlerName, id string) (context.Context, bool) {
	// 1. visited 防循环：如果目标已在展开链中出现过，停止
	if isVisited(ctx, handlerName, id) {
		return ctx, false
	}
	// 2. 深度检查：如果深度已耗尽，停止
	if d, ok := getDepth(ctx); ok && d <= 0 {
		return ctx, false
	}
	// 3. 通过：将目标加入 visited，深度递减
	newCtx := addVisited(ctx, handlerName, id)
	// 同时加 batch 键，供 _doList 交叉检测
	newCtx = addVisited(newCtx, handlerName, "batch")
	if d, ok := getDepth(ctx); ok {
		newCtx = withDepth(newCtx, d-1)
	}
	return newCtx, true
}

// shouldExpandField 统一的字段级展开前置检查（ignore + fieldLimit + handler 存在）。
// 消除 expandGet / _doList 中 6 处重复的 References / ChildRefs / Cascades 循环体头部。
func (h *GenericHandler[M]) shouldExpandField(
	ctx context.Context,
	childCtx context.Context,
	fieldName string,
	handlerName string,
	isRef bool,
) (CascadeHandler, bool) {
	if isRef {
		if shouldIgnoreRef(ctx) || shouldIgnoreField(ctx, fieldName) {
			return nil, false
		}
	} else {
		if shouldIgnoreCascade(ctx) || shouldIgnoreField(ctx, fieldName) {
			return nil, false
		}
	}
	_, ok := effectiveExpandDepth(ctx, true, fieldName)
	if !ok {
		return nil, false
	}
	if h.handlerReg == nil {
		return nil, false
	}
	ch := h.handlerReg.Get(handlerName)
	return ch, ch != nil
}

// ============================================================
// fieldLimitCtx — 字段级展开限制（父 Handler 对子 Handler 的精确控制）
//
// 父 Handler 在展开 Referenece/Cascade/ChildRef 时构建 fieldLimitMap
// 注入子 context，子 Handler 在自身展开时读取此 map 决定每个字段的行为：
//
//	0 → 跳过该字段（对应截止规则 -handler:field）
//	N → 仅展开 N 层（对应截止规则 handler:field，N=1）
//
// 与全局 depthCtx 的关系：fieldLimitMap 对每个字段独立生效，会覆盖该字段的
// 全局深度；未在 map 中的字段按全局 depthCtx 展开。
// ============================================================

type fieldLimitCtxKey struct{}

// fieldLimitMap 当前 Handler 各字段的最大展开层数。
// key 为 resultKey / ChildrenField 等字段名，value 为剩余深度（0=跳过，N=展开N层）。
type fieldLimitMap map[string]int

// withFieldLimits 将字段级深度限制写入 context。
func withFieldLimits(ctx context.Context, fl fieldLimitMap) context.Context {
	return context.WithValue(ctx, fieldLimitCtxKey{}, fl)
}

// getFieldLimits 从 context 获取字段级深度限制，未设置时返回 nil。
func getFieldLimits(ctx context.Context) fieldLimitMap {
	fl, _ := ctx.Value(fieldLimitCtxKey{}).(fieldLimitMap)
	return fl
}

// ============================================================
// StopRule — 截止规则
//
// 配置在父 Handler 的 HandlerConfig.FieldStopRules 中，
// 控制父字段展开到目标子 Handler 后，子 Handler 哪些字段应被截止/限制。
//
// 格式示例：
//
//	-department:manager    → 级联到 department 后，跳过 manager 字段的展开
//	 department:parent_id  → 级联到 department 后，parent_id 展开一层后截止
//
// HTTP compact 格式 (fstop=) 中同样使用此格式。
// ============================================================

// // StopRule 描述对目标 Handler 上某个字段的截止控制。
type StopRule struct {
	OnHandler string // 目标 Handler 注册名（如 "department"）
	Field     string // 目标 Handler 上的字段名（如 "manager", "parent_ulid"）
	Stop      bool   // true=完全不展开该字段，false=展开一层后截止
}

// parseStopRule 解析单条截止规则字符串。
// 格式：[-]handlerName:fieldName（如 "department:parent_id" 或 "-department:manager"）。
func parseStopRule(s string) (StopRule, error) {
	stop := false
	rest := s
	if len(rest) > 0 && rest[0] == '-' {
		stop = true
		rest = rest[1:]
	}
	parts := splitN(rest, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return StopRule{}, fmt.Errorf("invalid stop rule %q: expected [-]handler:field", s)
	}
	return StopRule{OnHandler: parts[0], Field: parts[1], Stop: stop}, nil
}

// parseStopRules 解析逗号分隔的截止规则字符串。
func parseStopRules(s string) ([]StopRule, error) {
	var rules []StopRule
	for _, part := range splitCSV(s) {
		if part == "" {
			continue
		}
		r, err := parseStopRule(part)
		if err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, nil
}

// splitCSV 按逗号分割，过滤空串。内部委托到 common.SplitAndTrim。
func splitCSV(s string) []string {
	return common.SplitAndTrim(s, ",")
}

// splitN 按 sep 分割 n 段（最后一段包含剩余全部内容）。
func splitN(s, sep string, n int) []string {
	parts := make([]string, 0, n)
	for i := 0; i < n-1; i++ {
		idx := strings.Index(s, sep)
		if idx < 0 {
			parts = append(parts, s)
			return parts
		}
		parts = append(parts, s[:idx])
		s = s[idx+len(sep):]
	}
	parts = append(parts, s)
	return parts
}

// ============================================================
// CascadeHandler — 子 Handler 的统一入口
//
// GenericHandler[M] 实现此接口，父 Handler 通过此接口委托子 Handler：
//
//	父 Handler 拆出子表数据 → 注 FK → childHandler.DoCreate(txCtx, childData)
//
// 父 Handler 不知道子实体的类型、字段、持久化方式，
// 只知道「把 FK 填上，交给子 Handler」。
// 同一事务由 TxCoordinator 保证。
// ============================================================

type CascadeHandler interface {
	// DoCreate 创建子记录。
	// requests 为已注入 FK 的 map 数据，每个 map 直接对应一条新记录。
	// 返回创建后的主键列表（[]any，如 ULID 列表）。
	DoCreate(ctx context.Context, requests []map[string]any) ([]any, error)

	// DoDelete 按主键删除子记录。
	// ids 为子记录的主键列表。
	DoDelete(ctx context.Context, ids []any) error

	// DoDeleteByFK 按外键批量删除子记录。
	// fkField 为 FK 字段名（Go 结构体字段名，如 "SiteULID"）。
	// fkValues 为父记录的主键值列表。
	DoDeleteByFK(ctx context.Context, fkField string, fkValues []any) error

	// DoUpdate 级联更新子记录。
	// fkField 为 FK 字段名，fkValue 为要注入到子记录的外键值。
	// childrenData 为子表数据（可能来自请求体或回填的旧数据）。
	// parentVersioned 表示父链上是否已出现版本化节点：
	//   - false: 按自身实际模式处理（版本化则创建新版本，非版本化则原地更新）
	//   - true + 子版本化: 创建新版本（Service 自动处理）
	//   - true + 子非版本化: 强制创建新记录（旧记录保留关联旧父版本）
	DoUpdate(ctx context.Context, fkField string, fkValue any, childrenData []map[string]any, parentVersioned bool) error

	// DoList 按外键查询子记录。
	// fkField 为 FK 字段名，fkValue 为父记录的主键值。
	// followPublished 控制是否返回正式发布版本：
	//   - false: 返回 FK 精确指向的记录版本（回填补数据 / 订单快照用）
	//   - true:  若为版本化模式，按 code 找到 version_status='published' 的版本
	// 返回子记录的 map 列表。
	DoList(ctx context.Context, fkField string, fkValue any, followPublished bool) ([]map[string]any, error)

	// DoGetByID 按主键获取单条记录（返回 map）。
	// 用于向上级联：当前实体的逻辑外键字段（如 site_ulid）→ 查引用的父实体。
	DoGetByID(ctx context.Context, id any) (map[string]any, error)

	// -------- 版本管理级联接口 --------
	//
	// 非版本化 Handler 也需要实现这些接口：自身空操作，仅级联到子 Handler。
	// 这样父 Handler（版本化）激活版本时，可通过非版本化子 Handler 的 DoActivate
	// 继续向下穿透，直到最底层的版本化孙子 Handler。

	// DoActivate 激活 / 发布版本。
	// 版本化：调用自身 svc.Activate，然后级联到所有子 Handler。
	// 非版本化：自身空操作，仅级联到子 Handler。
	DoActivate(ctx context.Context, id any) error

	// DoListVersions 查询版本列表（返回 map 列表）。
	// 版本化：调用自身 svc.ListVersions 并 marshal。
	// 非版本化：返回空列表。
	DoListVersions(ctx context.Context, id any, code string) ([]map[string]any, error)

	// DoEditVersion 修改版本元数据（状态、备注等）。
	// 版本化：调用自身 svc.EditVersion，然后级联到所有子 Handler。
	// 非版本化：自身空操作，仅级联到子 Handler。
	DoEditVersion(ctx context.Context, id any, patches map[string]any) (map[string]any, error)

	// PKField 返回当前 Handler 对应实体的主键数据库列名（如 "site_ulid"、"dept_ulid"）。
	// 用于批量展开时生成 WHERE 条件及构建结果 lookup map。
	PKField() string

	// SelfFKField 返回当前 Handler 对应实体的自关联外键字段名。
	// 返回值非空字符串 → 该实体存在自关联（如 "parent_item_ulid"）。
	// 返回值空字符串 → 无自关联。
	// 用于级联写入时解析同批次内自引用外键的代码→ULID 映射（如 parent_menu_code → parent_item_ulid）。
	SelfFKField() string
}

// ============================================================
// CascadeRelation — 级联关系声明
//
// 在 HandlerConfig.Cascades 中配置，GenericHandler 自动处理。
// 示例：
//
//	CascadeRelation{
//	    HandlerName:     "domain",
//	    ChildrenField:   "domains",
//	    FKField:         "SiteULID",
//	    OnCreate:        true,
//	    OnDelete:        true,
//	    FollowPublished: false,   // 级联检索时取当时版本（如订单快照）
//	}
//
// FollowPublished 控制级联检索（Get/List 展开子数据）时是否返回正式发布版本：
//   - false（默认）: 返回 FK 精确指向的记录版本（当时版本，如订单快照）
//   - true:          若子 Handler 为版本化模式，按 code 找到 version_status='published' 的版本
//
// 注意：_doUpdate 中的回填 DoList 始终使用当时版本（不受此配置影响）。
// ============================================================

type CascadeRelation struct {
	// HandlerName 子 Handler 在 HandlerRegistry 中的注册名称。
	HandlerName string

	// ChildrenField 请求体中子表数据的字段名（如 "domains"）。
	// Create 时，父 Handler 从此字段拆出子数据。
	ChildrenField string

	// FKField 子表中指向父实体的外键字段名（JSON 字段名，如 "site_ulid"）。
	// Create 时，父 Handler 自动将父 PK 注入子请求 map 的此字段。
	FKField string

	// OnCreate 是否在创建父记录时级联创建子记录。
	OnCreate bool

	// OnDelete 是否在删除父记录前先级联删除子记录。
	OnDelete bool

	// OnUpdate 是否在更新父记录时级联更新子记录。
	OnUpdate bool

	// OnActivate 是否在激活父版本时级联激活子记录。
	// 版本化父 Handler 执行 Activate 时，先激活自身，再按此标志级联到子 Handler。
	// 非版本化父 Handler 收到 DoActivate 时自身空操作，但受此标志控制是否级联。
	OnActivate bool

	// OnEditVersion 是否在编辑父版本元数据时级联编辑子记录。
	OnEditVersion bool

	// FollowPublished 级联检索（Get/List 展开子数据）时，是否返回子实体的正式发布版本
	// （version_status='published'），而非 FK 精确指向的当时版本。
	//   - false（默认）: 返回 FK 指向的精确版本（如订单快照需当时产品信息）
	//   - true:          返回子实体族的正式发布版本
	// 仅子 Handler 为版本化模式时生效；_doUpdate 的回填 DoList 不受此配置影响。
	FollowPublished bool

	// ChildrenWrapKey 子数据为标量数组（如 [1,2,3]）时，用此字段名自动包裹为 map。
	// 配置后，前端可直接传 "tags": [1, 2, 3]，系统自动转为 [{"tag_id": 1}, {"tag_id": 2}, {"tag_id": 3}]。
	// 不配置（空字符串）时保持原逻辑不变。
	ChildrenWrapKey string

	// Remaps 本批子数据内部的引用重映射声明（版本化级联重建场景，可选）。
	//
	// 版本化更新会为父实体建新版本并把子表复制重建为新 ULID，但子记录之间的
	// 横向引用（field_access.field_ulid → write_field.field_ulid、
	// flow_branch.target_node_ulid → flow_node.node_ulid 等）若不重写，
	// 新版本就会指向旧版本子记录，形成跨版本悬挂引用。
	//
	// 配置后，框架在新 ULID 全部生成、子记录落库**之前**建立
	// 旧 ULID → 新 ULID（权威）与 code → 新 ULID（兜底）映射并重写引用；
	// 无法解析的引用让事务失败，绝不静默保留旧 ULID。详见 cascade_remap.go。
	Remaps []ReferenceRemap

	// RemapKey 跨级联批次**发布**键（v2，可选）。
	//
	// 非空时，本关系产生的「旧 ULID → 新 ULID」「code → 新 ULID」映射会发布到
	// 当前**级联事务**的该命名空间，供配置了 ReferenceRemap.SourceRemapKey 的
	// 其它级联分支消费（同一事务内、可跨 Handler 边界）。
	//
	// 为什么需要：Remaps 的映射表只在**本批 childData** 内构建，而表单域的引用
	// 天然跨分支 —— write_field 与 list_column / detail_field / validation 是
	// form 下并列的级联分支，消费方看不到发布方的记录，无从自建映射。
	//
	//	form
	//	  ├─ write_section → write_field      RemapKey: "form.write_field"  ← 发布方
	//	  ├─ list_column                      SourceRemapKey: "form.write_field"
	//	  ├─ detail_section → detail_field    SourceRemapKey: "form.write_field"
	//	  └─ validation                       SourceRemapKey: "form.write_field"
	//
	// 顺序契约（重要）：
	//   - 同一 Cascades 数组内，发布方必须声明在消费方**之前** —— 违反时构造期 panic；
	//   - 跨 Handler 子树（发布方在子 Handler 内）无法静态推断，顺序由调用方保证，
	//     违反时运行时返回 errs.ErrRemapSourceMissing（文案含发布方 Handler 名）；
	//   - 可用 ValidateRemapKeys() 在启动期做全局存在性校验（L1）。
	//
	// 同一 RemapKey 可被多个 CascadeRelation 声明、可被同一批次多次发布：
	// 映射合并；同 code / 同旧 ULID 映射到不同新 ULID 时返回 errs.ErrRemapInconsistent。
	//
	// 映射生命周期严格绑定级联事务（随 context 存亡），不跨请求、不跨事务。
	RemapKey string

	// PublishCodeField 发布时用于构建「code → 新 ULID」兜底映射的**本批子记录字段名**
	// （如 "field_code"）。可选；留空则只发布「旧 ULID → 新 ULID」。
	//
	// 为什么需要独立字段：发布方常常只负责产出映射、自己并不消费（消费方在另外的
	// 级联分支），因此它的 Remaps 往往是空的 —— 那时没有任何地方能声明 code 字段。
	// 若强行要求发布方写一个空 Binding 只为携带 SourceCodeField，语义很别扭。
	//
	// 只依赖旧 ULID 映射也能工作（前提：消费方手里有旧 ULID）。跨版本场景下
	// 消费方通常拿不到旧 ULID（那是别的分支的记录），此时 code 兜底是必须的，
	// 故表单域这类结构应配置本字段。
	PublishCodeField string

	// Target 本关系的**发布标识**（v3 装配用，可选）。
	//
	// 非空表示「本批记录作为装配目标，供其它级联分支引用」，其值即
	// ReferenceAssembly.Target 的取值（如 "form.write_field"）。装配阶段
	// 据此把本批记录登记进请求级索引，消费方按 Match 在索引里查找。
	//
	//	form
	//	  ├─ write_section → write_field      Target: "form.write_field"  ← 发布方
	//	  ├─ list_column                      Assemblies[].Target: "form.write_field"
	//	  └─ validation                       Assemblies[].Target: "form.write_field"
	//
	// 与 v2 的 RemapKey 语义相同（都是「跨级联分支的发布命名空间」），
	// 区别在**时点**（设计文档 §10.1 的迁移对照）：
	//   - v2（RemapKey）：落库时才生成新 ULID，再把「旧 → 新」映射发布出去；
	//   - v3（Target）  ：请求入口预分配 ULID，登记记录本身，无需映射传递。
	// 因此**不得同时配置**：配了 Remaps/RemapKey 走 v2 通道（不预分配），
	// 配了 Target 走 v3 通道（预分配 + 装配）。heims 按关系逐个迁移即可。
	//
	// 与 Assemblies 的分工：Assemblies 声明**消费**（本批要装配哪些引用），
	// Target 声明**发布**（本批供谁引用）。同一关系可只发布、只消费或两者兼有。
	Target string

	// Assemblies 本批子数据的引用装配声明（v3，可选）。
	//
	// 与 Remaps（v1/v2 的事后重映射）**互斥**：同一关系不允许同时配置两者
	// （构造期报错，见 §8.1 B3）—— 两套机制并存会让行为难以预期。
	// heims 按关系逐个迁移，不需要同一关系半旧半新。
	//
	// 装配与重映射的本质区别在**时点**：
	//
	//	v1/v2：落库时才生成新 ULID → 事后回头把引用改对（需要映射表/顺序校验/catalog）
	//	v3  ：请求入口预分配 ULID → 引用在落库前一次性写对（不需要任何映射传递）
	//
	// 因此配置了 Assemblies 的批次：
	//   - 在阶段 1（展开请求树）为本批记录预分配 ULID 并登记索引；
	//   - 在阶段 2（全树预分配完成后）统一装配；
	//   - 阶段 3 落库时 PK 已存在，service 不再生成、引用不再修改。
	//
	// 配置示例见 handler/assembly.go 的 ReferenceAssembly 文档。
	Assemblies []ReferenceAssembly
}

// ============================================================
// ReferenceRelation — 向上级联声明
//
// 当前实体中某字段是指向另一实体的逻辑外键时，Get 查询会自动解析该引用。
// 示例（SysDomain 中的 site_ulid → SysSite）：
//
//	ReferenceRelation{
//	    Field:       "site_ulid",   // 当前实体中的 JSON 字段名
//	    HandlerName: "site",        // 引用的 Handler 在注册表中的名称
//	    ResultField: "site",        // 解析后的父实体在返回结果中的键名
//	}
//
// ResultField 为空时，自动从 Field 派生（去掉 _ulid 后缀，如 site_ulid→site）。
// ============================================================

type ReferenceRelation struct {
	// Field 当前实体中作为逻辑外键的 JSON 字段名（如 "site_ulid"）。
	Field string

	// HandlerName 引用的 Handler 在 HandlerRegistry 中的注册名称。
	HandlerName string

	// ResultField 解析后父实体在返回结果 map 中的键名。
	// 空字符串时自动推导：去掉 Field 中的 "_ulid" 后缀（如 site_ulid → site）。
	ResultField string
}

// ============================================================
// ChildRefRelation — 向下引用声明
//
// 当前实体通过 FK 列表引用一组已有子实体（仅关联，不控制生命周期）。
// Get/List 查询时自动将 FK 列表批量解析为子实体的完整对象列表。
//
// 示例（User 的 tag_ulids → 展开为 tags 对象列表）：
//
//	ChildRefRelation{
//	    FKListField: "tag_ulids",     // 请求/响应中的 FK 列表字段名
//	    HandlerName: "tag",           // 目标 Handler 在注册表中的名称
//	    ResultField: "tags",          // 解析后的子实体列表键名
//	}
//
// ResultField 为空时，自动从 FKListField 派生（去掉 _ulids/_ids 后缀并加 s，
// 如 tag_ulids→tags、menu_ids→menus）。
//
// 输入时（Create/Update）：FK 列表字段透传，实际关联由 Service 层处理。
// 输出时（Get/List）：通过 HandlerRegistry 批量查询（DoList + OpIn）展开 FK 列表。
// ============================================================

type ChildRefRelation struct {
	// FKListField 当前实体中作为逻辑外键列表的 JSON 字段名（如 "tag_ulids"）。
	FKListField string

	// HandlerName 目标 Handler 在 HandlerRegistry 中的注册名称。
	HandlerName string

	// ResultField 解析后子实体列表在返回结果 map 中的键名（如 "tags"）。
	// 空字符串时自动推导：去掉 FKListField 中的 "_ulids" 或 "_ids" 后缀（如 tag_ulids→tags）。
	ResultField string
}
