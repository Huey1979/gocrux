package handler

import (
	"context"

	errs "github.com/Huey1979/gocrux/errors"
)

// ============================================================
// 全树级联编排（阶段 1 / 2 / 3 的**递归**实现）
//
// 应用方 §25 指出的缺陷：三阶段最初只覆盖「发布方与消费方都是同一个父 Handler
// 的直接子批次」，而 heims 表单域的真实树形是：
//
//	form
//	  ├─ form_write_section → form_write_field   ← 发布方在**孙批次**
//	  ├─ form_list_column                        ← 消费方在直接子批次
//	  ├─ form_detail_section → form_detail_field
//	  └─ form_validation
//
// 非递归时：祖批次（form）的阶段 2 装配发生在孙批次被预分配**之前**
// （孙批次要到祖批次的阶段 3、由 form_write_section 自己的 _doCreate 才登记），
// 于是目标索引为空 → 只能 Unmatched（保留空 ULID）。这正是 §16 要求
// 「全树预分配完成后，再统一装配」却未被落实的部分。
//
// 因此：
//
//	阶段 1（递归、纯内存）：展开整棵树 → 每批预分配 ULID → 每个非空 Target 登记索引
//	阶段 2（递归）：对每一批执行本关系的 Assemblies（此刻全树索引已完整）
//	阶段 2'（递归、后序）：本层记录的后处理（如深到浅 JSON 编码，见 §26.3）
//	阶段 3（递归落库）：保持既有 DoCreate / DoUpdate 顺序
//
// 工程前提 A3：递归必须复用**同一批 childData 句柄**，绝不重新提取 ——
// 否则「分配给 A 的 ULID 用不到 A 上」（wrapKey 生成的包裹 map、DB 回填的
// 新切片都会导致句柄不同）。为此阶段 3 把「本批次子树」（cascadeBatchNode）
// 随 ctx 下传：嵌套 Handler 见到节点即跳过阶段 1/2，直接按预置子树落库。
//
// 任意跨级（应用方 §27）不需要新增 API：每个批次只要声明 Target 就能被任意
// 其它批次引用，亲属关系（兄弟/堂兄弟/叔侄/爷孙）只影响测试树形，不影响语义。
// ============================================================

// cascadeBatchNode 一个子批次（阶段 1 的产物，含其子树）。
type cascadeBatchNode struct {
	// rel 本批对应的级联关系（提供 HandlerName / FKField / Remaps / Target / Assemblies）。
	rel CascadeRelation
	// handler 本批的 Handler（阶段 3 落库入口）。
	handler CascadeHandler
	// childData 本批记录（**句柄**：阶段 1 预分配/装配的改动必须与落库读到的是同一份）。
	childData []map[string]any

	// -------- create 语义 --------
	// tempRefs 本批的 _temp_ref 索引（供 __ref: 占位符映射，阶段 3 更新 refMap）。
	tempRefs map[string]tempRefEntry

	// -------- update 语义 --------
	// oldPKs 本批记录「清 PK 之前」的身份键（与 childData 同序）。
	// 用途：① v2 重映射的「旧 ULID → 新 ULID」映射；② 更深层子批次回填查询的父身份。
	oldPKs []string
	// passToChild 本批在子 Handler 侧应走 CREATE（true）还是 UPDATE（false）语义。
	passToChild bool
	// fkValue 本批的父记录身份（update 语义下由 DoUpdate 注入 FK；create 已在阶段 1 注入）。
	fkValue any

	// subs 更深层批次（本批记录的子批次，含再往下的递归结果）。
	subs []*cascadeBatchNode

	// prepared 本批次的**子树**是否已在阶段 1 准备完毕。
	//
	// 判据：本批记录在阶段 1 是否已持有权威 PK（v3 预分配保证；v2 通道与
	// 未启用预分配时，PK 要到落库那一刻才由 service 生成）。
	//
	// 只有 true 才能把子树交给嵌套 Handler（withBatchNode）：否则子批次的 FK
	// 无从注入（父 PK 尚不存在），必须让嵌套 Handler 在落库后按自己的结果注入 ——
	// 也就是既有行为。未准备时也不能携带节点，否则嵌套层会「跳过阶段 1/2 却
	// 又没拿到子树」，其子批次将既不预分配也不落库。
	prepared bool
}

// cascadeBatchCtxKey 承载「本批次子树」的 ctx key（入口 → 嵌套 Handler 传递）。
type cascadeBatchCtxKey struct{}

// withBatchNode 把本批次的子树节点写入 ctx。
func withBatchNode(ctx context.Context, n *cascadeBatchNode) context.Context {
	if n == nil {
		return ctx
	}
	return context.WithValue(ctx, cascadeBatchCtxKey{}, n)
}

// batchNodeFrom 取出本批次的子树节点（非入口驱动的调用返回 nil）。
//
// 非 nil 表示「整棵树的阶段 1/2 已由入口完成」：嵌套 Handler 不得再自行
// 预分配/登记/装配（重复登记会让同一记录在目标索引里出现两次 → 命中多条）。
func batchNodeFrom(ctx context.Context) *cascadeBatchNode {
	n, _ := ctx.Value(cascadeBatchCtxKey{}).(*cascadeBatchNode)
	return n
}

// cascadeTreePreparer 能递归准备自身子树（阶段 1）的 Handler。
//
// 只有 GenericHandler 实现；自定义 CascadeHandler 不实现时递归在其处停止 ——
// 那部分子树保留旧的「各自局部三阶段」行为（向后兼容，不阻塞第三方实现）。
type cascadeTreePreparer interface {
	// subtreeCtx 为本层的子批次构造级联 ctx（visited + depth），
	// 并报告是否允许继续下钻（深度耗尽 / 出现环 → false，只落库不再展开）。
	subtreeCtx(ctx context.Context) (context.Context, bool)

	// prepareCreateSubtree 阶段 1（create 语义，递归）。
	//
	// parentPKs 与 rawMaps 等长：本层每条父记录的**权威 PK**。
	// create 时父记录的主键可能来自 service 落库结果（顶层）或上一轮预分配
	// （更深层），两者都不在原始请求 map 里，故必须显式传入。
	prepareCreateSubtree(
		ctx context.Context, rawMaps []map[string]any, parentPKs []any,
		onFilter func(CascadeRelation) bool,
	) (subtreePrepareResult, error)

	// prepareUpdateSubtree 阶段 1（update 语义，递归）。
	prepareUpdateSubtree(
		ctx context.Context, records []updateLevelRecord, parentVersioned bool,
	) (subtreePrepareResult, error)
}

// subtreePrepareResult 阶段 1（递归）的结果。
type subtreePrepareResult struct {
	// ctx 携带凭证桥等（逐层下传的新 ctx）。
	ctx context.Context
	// nodes 本层展开出来的全部子批次（每个 = 一条父记录/一批父记录 × 一个关系）。
	nodes []*cascadeBatchNode
}

// updateLevelRecord 更新语义下「一层」里的一条记录。
type updateLevelRecord struct {
	// raw 该记录的请求/回填 map（**句柄**：预分配与落库必须同一份）。
	raw map[string]any
	// oldPK 落库前身份（回填子表时的父身份；新建记录为 nil → 不回溯 DB）。
	oldPK any
	// newPK 落库后身份（注入子批次 FK 用）。
	newPK any
}

// ============================================================
// 阶段 2：全树装配（递归）
// ============================================================

// assembleTree 对整棵树执行装配（阶段 2），并返回统计。
//
// 顺序：本层各批次 → 递归子树。装配只读目标索引（此时阶段 1 已完成，
// 全树索引完整），因此本层与子层的先后不影响结果。
func assembleTree(
	ctx context.Context,
	reg *PreallocRegistry,
	nodes []*cascadeBatchNode,
	ownerID string,
	sourceHandler string,
) (AsmStats, error) {
	var total AsmStats
	for _, n := range nodes {
		st, err := runAssemblies(ctx, reg, []CascadeRelation{n.rel}, 0, n.childData, ownerID, sourceHandler)
		total.Applied += st.Applied
		total.Skipped += st.Skipped
		total.Unmatched += st.Unmatched
		total.Idempotent += st.Idempotent
		if err != nil {
			return total, err
		}
		if st.Unmatched > 0 {
			// B5：匹配不到是 WARN（应用侧发布门禁负责 fail-closed）。
			asmWarnf("gocrux: 引用装配存在未匹配项（Handler=%s 批次=%s unmatched=%d）",
				sourceHandler, n.rel.HandlerName, st.Unmatched)
		}
		sub, err := assembleTree(ctx, reg, n.subs, ownerID, sourceHandler)
		total.Applied += sub.Applied
		total.Skipped += sub.Skipped
		total.Unmatched += sub.Unmatched
		total.Idempotent += sub.Idempotent
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// encodeTree 阶段 2 的**后序**收尾：对每一层的记录做装配后处理（如深到浅 JSON 编码）。
//
// 为什么必须后序：外层若先被序列化成 JSON 文本，内层随后发生的装配改动就
// 只落在对象上、不会进入那段文本 —— 深到浅（子先父后）才自洽。
//
// 每一层的钩子取「该层记录所属 Handler」的 AfterCascadeAssemble（§26.3）。
func encodeTree(ctx context.Context, nodes []*cascadeBatchNode) error {
	for _, n := range nodes {
		if err := encodeTree(ctx, n.subs); err != nil {
			return err
		}
		if err := runAfterCascadeAssemble(ctx, n.handler, n.childData); err != nil {
			return err
		}
	}
	return nil
}

// ============================================================
// 阶段 3：落库（递归）
// ============================================================

// persistCreateNodes 阶段 3（create）：按批次落库，并把子树节点交给嵌套 Handler。
//
// 与旧实现的差别：不再由嵌套 Handler 重新展开子树，而是把**已准备的同一批
// childData 句柄**连同子树一起传下去（withBatchNode）—— 这同时保证了
// 「分配给 A 的 ULID 一定用在 A 上」（工程前提 A3）。
func (h *GenericHandler[M]) persistCreateNodes(
	ctx context.Context, nodes []*cascadeBatchNode, refMap cascadeRefMap,
) error {
	for _, n := range nodes {
		childCtx := ctx
		// 批次内横向引用重映射（v1/v2 通道）：与 Assemblies 互斥（构造期校验），
		// 故配置了 Assemblies 的关系不会进入此分支。
		if len(n.rel.Remaps) > 0 || n.rel.RemapKey != "" {
			childCtx = stageRemapWithKey(childCtx, n.rel.HandlerName, n.rel.Remaps,
				n.childData, n.handler.PKField(), n.rel.RemapKey,
				n.rel.PublishCodeField, resolveRemapPublishers(n.rel, h.config.Cascades))
		}
		// 只有子树已在阶段 1 准备完毕时才把节点交给子 Handler（见 prepared 的说明）
		if n.prepared {
			childCtx = withBatchNode(childCtx, n)
		}

		pks, err := n.handler.DoCreate(childCtx, n.childData)
		if err != nil {
			return errs.ErrCascadeCreate(n.rel.HandlerName, err)
		}
		// 登记已落库记录（无事务冲突兜底清理用，设计文档 §7.3）
		for _, pk := range pks {
			markWrittenRecord(ctx, n.rel.HandlerName, scalarToString(pk))
		}
		updateRefMap(refMap, n.tempRefs, pks)
	}
	return nil
}

// persistUpdateNodes 阶段 3（update）：按批次落库，并把子树节点交给嵌套 Handler。
func (h *GenericHandler[M]) persistUpdateNodes(ctx context.Context, nodes []*cascadeBatchNode) error {
	for _, n := range nodes {
		childCtx := ctx
		// v2 通道：旧 PK 快照在阶段 1 清 PK 之前已留存（n.oldPKs），此处透传，
		// 因为清 PK 后 stageRemap 内部的 snapshotPKs 只会取到空值。
		if len(n.rel.Remaps) > 0 || n.rel.RemapKey != "" {
			childCtx = stageRemapWithKeyOldPKs(childCtx, n.rel.HandlerName, n.rel.Remaps,
				n.childData, n.oldPKs, n.handler.PKField(), n.rel.RemapKey,
				n.rel.PublishCodeField, resolveRemapPublishers(n.rel, h.config.Cascades))
		}
		// 只有子树已在阶段 1 准备完毕时才把节点交给子 Handler（见 prepared 的说明）
		if n.prepared {
			childCtx = withBatchNode(childCtx, n)
		}

		if err := n.handler.DoUpdate(childCtx, n.rel.FKField, n.fkValue, n.childData, n.passToChild); err != nil {
			return errs.ErrCascadeUpdate(n.rel.HandlerName, err)
		}
	}
	return nil
}

// ============================================================
// 阶段 1 的公共件
// ============================================================

// subtreeCtx 实现 cascadeTreePreparer：为子批次构造级联 ctx。
//
// 与既有 DoCreate/DoUpdate 的语义一致：把本 Handler 加入 visited、深度减一；
// 已访问（成环）或深度耗尽 → allowed=false，调用方只落库、不再展开子层。
func (h *GenericHandler[M]) subtreeCtx(ctx context.Context) (context.Context, bool) {
	if h.shouldShortCircuitCascade(ctx) {
		return ctx, false
	}
	return h.buildCascadeCtx(ctx), true
}

// cascadeRootTargeter 能自述「本 Handler 记录自身的发布标识」的 Handler。
//
// 用途：非根批次也能用 HandlerConfig.Target 发布自身记录（与父级关系的
// rel.Target 等价，只是声明位置不同）。二者同名时只登记一次，避免同一记录
// 在目标索引里出现两次（命中多条）。
type cascadeRootTargeter interface {
	cascadeRootTarget() string
}

// cascadeRootTarget 实现 cascadeRootTargeter。
func (h *GenericHandler[M]) cascadeRootTarget() string { return h.config.Target }

// registerBatchOwnTarget 登记「本批次 Handler 自身声明」的发布标识（若与关系级不同）。
func registerBatchOwnTarget(reg *PreallocRegistry, holder CascadeHandler, relTarget, handlerName, pkField string, childData []map[string]any) {
	t, ok := holder.(cascadeRootTargeter)
	if !ok {
		return
	}
	own := t.cascadeRootTarget()
	if own == "" || own == relTarget {
		return
	}
	registerAssemblyTargets(reg, own, handlerName, pkField, childData)
}

// registerRootTargetFromResults 登记「本 Handler 记录自身」为装配目标（§27.3）。
//
// 与直接拿 rawMaps 登记的区别：这里用**落库后的实体快照**。
// 原因是 Match 要在目标记录上读取匹配键，而请求体往往缺少这些字段 ——
// 版本化 update 的请求通常只有 {id, 改动字段}，code / 名称都不在其中，
// 用请求 map 会让消费方的 Match 因缺 key 而落空（实测：v2 中继层引用
// 祖记录时装配不到，最终保留 DB 回填的 v1 旧值）。
//
// 目标记录只被**读取**（Match 与 Assign 的源），不会被写回，
// 因此用快照不影响任何落库内容。
func (h *GenericHandler[M]) registerRootTargetFromResults(reg *PreallocRegistry, results []*M) {
	if reg == nil || h.config.Target == "" || len(results) == 0 {
		return
	}
	maps := make([]map[string]any, 0, len(results))
	for _, r := range results {
		if r == nil {
			continue
		}
		m, err := marshalToMap(r)
		if err != nil {
			asmWarnf("gocrux: 顶层 Target=%s 登记失败（实体快照序列化出错）：%v", h.config.Target, err)
			continue
		}
		// 主键同时以「PKField 列名」与约定 JSON 名 "ulid" 写入（与子批次登记的
		// 口径一致）：不同实体的 JSON tag 各不相同（如版本化父实体用 parent_ulid），
		// Assign 的取值键必须两种都能命中，否则装配会静默写空。
		if pk := scalarToString(extractPKFromResult(r)); pk != "" {
			writePKValue(m, h.PKField(), pk)
		}
		maps = append(maps, m)
	}
	registerRootTarget(reg, h.config.Target, h.svcName, h.PKField(), maps)
}

// registerRootTarget 把「本 Handler 记录自身」登记为目标（应用方 §27.3）。
//
// 语义：Target 原先只能声明在 CascadeRelation 上（即只有**子批次**能发布），
// 顶层/祖先记录没有对应的关系，无法声明 —— 于是「爷孙」中「祖先是发布方」
// 这一方向不成立。HandlerConfig.Target 补上这一点：
//
//	HandlerConfig{ Target: "root.form" }   → 本 Handler 的记录可被任意批次引用
//
// rawMaps 必须是**落库后**持有权威 PK 的同一批 map（create: preallocateRoot 已写；
// update: 调用方需先写回服务返回的新 PK）。
func registerRootTarget(reg *PreallocRegistry, target, handlerName, pkField string, rawMaps []map[string]any) {
	if reg == nil || target == "" || len(rawMaps) == 0 {
		return
	}
	registerAssemblyTargets(reg, target, handlerName, pkField, rawMaps)
}

// runBeforeCascadePrepare 触发本 Handler 的「装配前 raw 树规范化」钩子（§26.3）。
func (h *GenericHandler[M]) runBeforeCascadePrepare(ctx context.Context, rawMaps []map[string]any) error {
	if h.hooks.BeforeCascadePrepare == nil || len(rawMaps) == 0 {
		return nil
	}
	return h.hooks.BeforeCascadePrepare(ctx, rawMaps)
}

// runBeforeCascadePrepareOn 在任意 Handler 上触发规范化钩子（非泛型分发）。
func runBeforeCascadePrepareOn(ctx context.Context, handler CascadeHandler, rawMaps []map[string]any) error {
	if p, ok := handler.(cascadeTreePreparer); ok {
		if hooker, ok2 := p.(cascadePrepareHooker); ok2 {
			return hooker.runBeforeCascadePrepareHook(ctx, rawMaps)
		}
	}
	return nil
}

// runAfterCascadeAssemble 触发本 Handler 的「装配后 raw 树编码」钩子（§26.3）。
func (h *GenericHandler[M]) runAfterCascadeAssemble(ctx context.Context, rawMaps []map[string]any) error {
	if h.hooks.AfterCascadeAssemble == nil || len(rawMaps) == 0 {
		return nil
	}
	return h.hooks.AfterCascadeAssemble(ctx, rawMaps)
}

// runAfterCascadeAssemble 在任意 Handler 上触发编码钩子（非泛型分发）。
func runAfterCascadeAssemble(ctx context.Context, handler CascadeHandler, rawMaps []map[string]any) error {
	if hooker, ok := handler.(cascadeAssembleHooker); ok {
		return hooker.runAfterCascadeAssembleHook(ctx, rawMaps)
	}
	return nil
}

// cascadePrepareHooker / cascadeAssembleHooker 供非泛型分发调用钩子。
type cascadePrepareHooker interface {
	runBeforeCascadePrepareHook(ctx context.Context, rawMaps []map[string]any) error
}

type cascadeAssembleHooker interface {
	runAfterCascadeAssembleHook(ctx context.Context, rawMaps []map[string]any) error
}

// runBeforeCascadePrepareHook 实现 cascadePrepareHooker。
func (h *GenericHandler[M]) runBeforeCascadePrepareHook(ctx context.Context, rawMaps []map[string]any) error {
	return h.runBeforeCascadePrepare(ctx, rawMaps)
}

// runAfterCascadeAssembleHook 实现 cascadeAssembleHooker。
func (h *GenericHandler[M]) runAfterCascadeAssembleHook(ctx context.Context, rawMaps []map[string]any) error {
	return h.runAfterCascadeAssemble(ctx, rawMaps)
}
