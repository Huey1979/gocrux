package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	errs "github.com/Huey1979/gocrux/errors"
	"github.com/Huey1979/gocrux/service"
)

// ============================================================
// 级联引用重映射的时机与传递
//
// 为什么必须借 context 传递、并把执行点放在「主键已生成、尚未落库」：
//
// 重映射的正确时点由 REQ 明确约定：
//
//	清除旧子记录主键 → 新 ULID 全部生成 → 构建映射 → 重写引用 → 子记录落库
//
// 而新 ULID 是在 service 的 `_beforeCreate` 里生成的（MergeTo 后 PK 为空 → 补 ULID）。
// 父 Handler 调用 childHandler.DoCreate 之前，子数据里根本没有新 ULID，
// 既建不出「旧 → 新」映射，也无法重写引用。
//
// 因此：
//   - 父 Handler 只**登记**（stageRemap）：留存重映射声明 + 旧 PK 快照；
//   - 子 Handler 通过 service 的 `BeforeCreatePersist` 钩子在**落库前**执行
//     （applyStagedRemap）。
//
// 登记项经 context 传递（父子看到同一份 childData 切片），整条链在同一事务内，
// 失败即整体回滚 —— 满足「不能在落库后再修补」的硬约束。
// ============================================================

// remapCtxKey 级联引用重映射登记项的 context key。
type remapCtxKey struct{}

// stagedRemap 一个待执行的重映射登记项。
type stagedRemap struct {
	// handlerName 目标子 Handler 名。仅当与执行方 svcName 相同时才消费，
	// 避免级联链上多层 Handler 误消费对方的登记项。
	handlerName string
	// remaps 重映射声明。
	remaps []ReferenceRemap
	// childData 本批子数据。父子 Handler 拿到的是**同一份切片**，
	// 因此子 Handler 侧生成的新 ULID 在父 Handler 侧同样可见。
	childData []map[string]any
	// pkField 子实体主键列名。
	pkField string
	// oldPKs 旧主键快照（在 PK 被清除前留存）—— 建立「旧 → 新」映射的唯一依据。
	oldPKs []string
	// remapKey 本批作为**发布方**的命名空间（v2，CascadeRelation.RemapKey）。
	// 非空时，本批执行完毕后把映射发布到事务级 catalog。
	remapKey string
	// publishCodeField 发布时用于构建 code → 新 ULID 的字段名
	// （CascadeRelation.PublishCodeField）；留空则只发布旧 ULID → 新 ULID。
	publishCodeFieldName string
	// sourceHandlers 消费命名空间 → 发布方 Handler 名（仅用于 L3 错误文案，
	// 让「发布方是谁没执行」可读）。
	//
	// 按 key 索引而非单个切片：一个消费方关系可能声明多个 SourceRemapKey，
	// 各自的发布方不同（heims §12.2）。
	sourceHandlers map[string][]string

	// processed 已处理过的 childData 索引（按 map 身份去重）。
	//
	// 为什么需要：本批子记录可能被**拆成多次钩子调用**送达 ——
	// service 逐条 Update 时每次只传一个实体（heims §13），
	// 因此不能像早期实现那样用一次性 done 标记「本批已处理完」。
	// 改为按索引累积：每次处理新到的那几条，并记录已处理。
	processed map[int]bool

	// publishedOld / publishedCode 已发布过的键 → 值（增量发布的去重标记）。
	// 每次钩子调用都会把**当前已累积的全部映射**再发布一次（Publish 幂等），
	// 这样无需知道「批次何时结束」也能保证最终完整。
	//
	// 必须记录**值**而非仅键：后续调用可能为同一键解析出更完整的新 ULID，
	// 只按键去重会把这种修正挡掉。
	publishedOld  map[string]string
	publishedCode map[string]string

	// done 是否已完成（仅表示「至少处理过一次」，不再用于跳过后续调用）。
	done bool
}

// stageRemap 在父 Handler 侧登记重映射任务，返回携带登记项的 context。
//
// 旧 PK 快照在此刻留存：PK 尚未被清除、ULID 尚未重建，
// 这是能拿到「旧 ULID」的最后时机。
func stageRemap(
	ctx context.Context,
	handlerName string,
	remaps []ReferenceRemap,
	childData []map[string]any,
	pkField string,
) context.Context {
	return stageRemapWithKey(ctx, handlerName, remaps, childData, pkField, "", "", nil)
}

// stageRemapWithKey 同 stageRemap，额外携带发布键（v2）、发布 code 字段与发布方 Handler 名。
//
// 发布方 Handler 名不在此处解析（父 Handler 的 config 里没有它），
// 由调用方传入用于 L3 错误文案。
func stageRemapWithKey(
	ctx context.Context,
	handlerName string,
	remaps []ReferenceRemap,
	childData []map[string]any,
	pkField string,
	remapKey string,
	publishCodeField string,
	sourceHandlers map[string][]string,
) context.Context {
	// 只有「消费型」remaps（批内）与「发布键」都为空的批次才完全无事可做
	if len(childData) == 0 {
		return ctx
	}
	if len(remaps) == 0 && remapKey == "" {
		return ctx
	}
	item := &stagedRemap{
		handlerName:          handlerName,
		remaps:               remaps,
		childData:            childData,
		pkField:              pkField,
		oldPKs:               snapshotPKs(childData, pkField),
		remapKey:             remapKey,
		publishCodeFieldName: publishCodeField,
		sourceHandlers:       sourceHandlers,
	}
	existing, _ := ctx.Value(remapCtxKey{}).([]*stagedRemap)
	return context.WithValue(ctx, remapCtxKey{}, append(existing, item))
}

// stageRemapWithKeyOldPKs 同 stageRemapWithKey，但**直接传入旧主键快照**。
//
// 存在的必要性：v3 的三阶段改造后，清 PK 发生在**阶段 1**（收集阶段），
// 而 stageRemap 在阶段 3（落库前）才被调用 —— 此时旧 PK 已被清除，
// stageRemap 内部的 snapshotPKs 只能取到空值，导致 v2 的「旧 ULID → 新 ULID」
// 映射完全构建不出来（引用解析必然失败）。
//
// 因此阶段 1 清 PK 之前先留存快照（b.oldPKs），在此处透传。
// 这与 v1/v2 时期「先 stage 再清 PK」的语义完全等价，只是把留存的时点
// 提前到了三阶段的划分里。
func stageRemapWithKeyOldPKs(
	ctx context.Context,
	handlerName string,
	remaps []ReferenceRemap,
	childData []map[string]any,
	oldPKs []string,
	pkField string,
	remapKey string,
	publishCodeField string,
	sourceHandlers map[string][]string,
) context.Context {
	if len(childData) == 0 {
		return ctx
	}
	if len(remaps) == 0 && remapKey == "" {
		return ctx
	}
	item := &stagedRemap{
		handlerName:          handlerName,
		remaps:               remaps,
		childData:            childData,
		pkField:              pkField,
		oldPKs:               oldPKs,
		remapKey:             remapKey,
		publishCodeFieldName: publishCodeField,
		sourceHandlers:       sourceHandlers,
	}
	existing, _ := ctx.Value(remapCtxKey{}).([]*stagedRemap)
	return context.WithValue(ctx, remapCtxKey{}, append(existing, item))
}

// applyStagedRemap 执行本 Handler 名下登记的重映射（service BeforeCreatePersist 钩子）。
//
// 时机：主键 ULID 已生成、记录尚未 INSERT。
//
// **可多次调用**（heims §13 的关键约束）：同一登记项可能被拆成多次钩子调用送达 ——
// 例如 service 逐条 Update 时每次只传一个实体。因此这里**不能**用一次性 done 标记
// 就跳过后续调用（早期实现的缺陷：只有第一条子记录的新 ULID 进入映射，
// 其余记录以「旧 ULID」发布，消费方找不到目标）。
//
// 改为按记录**累积**：
//   - 每次调用只重写本次送达的那几条（其余尚未生成新 ULID，重写也无意义）；
//   - 累积已处理索引，避免同一条被重复处理；
//   - 每次调用后把「当前已累积的全部映射」重新发布一遍（Publish 对同键同值为幂等），
//     这样**无需知道批次何时结束**也能保证 catalog 最终拿到完整映射。
func (h *GenericHandler[M]) applyStagedRemap(ctx context.Context, entities []*M) error {
	items, _ := ctx.Value(remapCtxKey{}).([]*stagedRemap)
	if len(items) == 0 {
		return nil
	}
	for _, item := range items {
		if item == nil || item.handlerName != h.svcName {
			continue
		}
		item.done = true
		if err := h.runRemap(ctx, item, entities); err != nil {
			return err
		}
	}
	return nil
}

// matchChildIndexes 把本次送达的实体对应到 childData 的下标。
//
// 对应策略（按可靠性排序）：
//  1. 实体主键能在 childData 里找到相同 ULID → 按下标精确匹配
//     （最可靠：新 ULID 已生成且被回填进 childData 同一条）；
//  2. 找不到时退回「第一个未处理且主键为空的 childData 下标」
//     （逐条赠序送达的常见形态：新 ULID 尚未回填）。
//
// 返回的下标集合去除了已处理过的项。
func matchChildIndexes[M service.Record](item *stagedRemap, entities []*M) []int {
	if item == nil || len(item.childData) == 0 || len(entities) == 0 {
		return nil
	}
	if item.processed == nil {
		item.processed = make(map[int]bool)
	}

	// ① 先按「实体 ULID == childData 中现有 ULID」精确匹配
	byULID := make(map[string]int, len(item.childData))
	for j, rec := range item.childData {
		if rec == nil {
			continue
		}
		if v, ok := readPKValue(rec, item.pkField); ok && v != nil {
			if s := scalarToString(v); s != "" {
				byULID[s] = j
			}
		}
	}

	var out []int
	claimed := make(map[int]bool)
	for _, ent := range entities {
		if ent == nil {
			continue
		}
		if pk := extractPKFromResult(ent); pk != nil {
			if j, ok := byULID[scalarToString(pk)]; ok && !item.processed[j] && !claimed[j] {
				out = append(out, j)
				claimed[j] = true
			}
		}
	}
	if len(out) > 0 {
		return out
	}

	// ② 退回顺序匹配：取未处理的下标（按位置对应，与 stageRemap 登记顺序一致）
	need := 0
	for _, ent := range entities {
		if ent != nil {
			need++
		}
	}
	for j := range item.childData {
		if len(out) >= need {
			break
		}
		if item.processed[j] || claimed[j] {
			continue
		}
		out = append(out, j)
		claimed[j] = true
	}
	return out
}

// runRemap 构建映射计划并重写本批子数据中的引用。
//
// 关键：新 ULID 在 service._beforeCreate 时被写进**实体**（entities），
// 而重写的目标是 childData（map 形态，_doCreate 会经 MergeTo 灌入实体）。
// 因此这里先把实体上的新主键回填进 childData，映射与重写才能对上。
//
// v2 拆成两段（顺序不可颠倒）：
//
//	先 Resolve 消费（用别的批次发布的映射重写本批引用）
//	再 Publish 发布（把本批的「旧→新」交付给后续批次）
//
// 若先发布再消费，「自产自销」会把本批自身的映射混进消费视图，
// 掩盖跨批次缺失问题（消费方即使拿不到发布方也会“看起来成功”）。
func (h *GenericHandler[M]) runRemap(ctx context.Context, item *stagedRemap, entities []*M) error {
	// 0. 定位本次送达的实体在 childData 中的下标，并把新主键回填到那几条
	idxs := matchChildIndexes(item, entities)
	backfillNewPKsAt(item, entities, idxs, h.PKField())
	for _, j := range idxs {
		item.processed[j] = true
	}

	// 1. 消费：重写**本次这几条**的引用。
	//    尚未生成新 ULID 的子记录若一起重写，映射里还没有它们的目标 → 会误报
	//    「无法解析」，因此按子集重写。
	if err := h.consumeRemap(ctx, item, entities, idxs); err != nil {
		return err
	}

	// 2. 发布：把**当前已累积的全部映射**发布出去（Publish 幂等）。
	//    不等待「批次全部结束」也能保证最终完整 —— 因为每次调用都会带上
	//    之前已回填的记录（它们的 map 已被就地改写）。
	return publishRemap(ctx, item, h.svcName)
}

// consumeRemap 用映射（批内自建 或 跨批次 catalog）重写本批子数据中的引用。
func (h *GenericHandler[M]) consumeRemap(ctx context.Context, item *stagedRemap, entities []*M, idxs []int) error {
	subset := item.subset(idxs)
	if len(subset) == 0 {
		return nil
	}
	plan, err := h.buildRemapPlan(ctx, item, subset)
	if err != nil {
		return err
	}
	if plan == nil {
		return nil
	}
	// 业务侧自定义映射（可选接口）并入（同键以业务侧为准）
	if err := callCustomRemapper(ctx, h, plan); err != nil {
		return err
	}
	// 原地重写**本次子集**的引用字段；解析失败 → 事务失败。
	// 注意：此时 _beforeCreate 已把 childData 灌入 entities，
	// 改写 childData 不会被再次读取，故必须同步回写实体（见 mergeRemappedBack）。
	if err := applyRemap(plan, subset); err != nil {
		return err
	}
	mergeRemappedBackSubset(entities, subset)
	return nil
}

// subset 取 childData 的指定下标子集（保持顺序）。idxs 为 nil 时返回全部。
func (item *stagedRemap) subset(idxs []int) []map[string]any {
	if item == nil {
		return nil
	}
	if idxs == nil {
		return item.childData
	}
	out := make([]map[string]any, 0, len(idxs))
	for _, j := range idxs {
		if j >= 0 && j < len(item.childData) {
			out = append(out, item.childData[j])
		}
	}
	return out
}

// buildRemapPlan 按发布/消费键决定映射来源，并**合并**两类来源。
//
// 一个 CascadeRelation 可以同时声明两类绑定：
//
//	Remaps: []ReferenceRemap{
//	    {Bindings: ...},                            // 批内自引用（映射由本批 childData 构建）
//	    {SourceRemapKey: "x", Bindings: ...},       // 跨批次消费（映射来自 catalog）
//	}
//
// 早期实现遇到任何 SourceRemapKey 就**只**走 catalog，导致 batchLocal 被静默丢弃
// —— 批内引用不再重写且不报错，属无声漏项（heims §12.3）。现改为合并：
//
//   - 批内映射照常由 prepareRemap 构建（携带旧 PK 快照）；
//   - 跨批次 namespace 照常从 catalog Resolve（缺失 → ErrRemapSourceMissing，L3）；
//   - 两份 oldToNew / codeToNew 并集写入同一计划；**同 key 冲突时报
//     ErrRemapInconsistent**（不静默取其一，与 Publish 的合并规则同口径）。
//
// 合并冲突理论上不该出现（旧 ULID / code 的归属唯一），一旦出现即为数据问题，
// 故显式报错而非覆盖。
// subset 为本次要重写的子数据切片（累积语义下可能只是整批的一部分）。
// 批内映射必须**基于整批**构建（映射需要全部旧→新），但重写只作用于 subset。
func (h *GenericHandler[M]) buildRemapPlan(
	ctx context.Context, item *stagedRemap, subset []map[string]any,
) (*RemapPlan, error) {
	// 拆出批内声明与跨批次声明（两类可共存，见函数注释）
	var crossBatch []ReferenceRemap
	var batchLocal []ReferenceRemap
	for _, r := range item.remaps {
		if r.SourceRemapKey != "" {
			crossBatch = append(crossBatch, r)
			continue
		}
		batchLocal = append(batchLocal, r)
	}

	// 批内映射：沿用 v1 行为（prepareRemap 已处理 bindings 为空 → nil）。
	// 映射基于全量 childData 构建，但计划的目标数据限定为本次 subset。
	batchPlan := prepareRemap(item.handlerName, batchLocal, item.childData, item.oldPKs)
	if batchPlan != nil {
		batchPlan.childData = subset
	}

	if len(crossBatch) == 0 {
		return batchPlan, nil // 纯批内（或全空）
	}

	// 跨批次消费：按 namespace 归组
	byKey := make(map[string][]ReferenceBinding)
	var order []string
	for _, r := range crossBatch {
		if _, seen := byKey[r.SourceRemapKey]; !seen {
			order = append(order, r.SourceRemapKey)
		}
		byKey[r.SourceRemapKey] = append(byKey[r.SourceRemapKey], r.Bindings...)
	}

	catalog := remapCatalogFrom(ctx)
	if catalog == nil {
		return nil, fmt.Errorf(
			"%w: SourceRemapKey=%q 需要从事务级 catalog 消费映射，但当前 context 中没有 catalog；"+
				"请确认级联事务经由 TxCoordinator.Run 触发（消费方 %s）",
			errs.ErrRemapInvalidConfig, order[0], item.handlerName)
	}

	// 以批内计划为基底（若有），再并入各 namespace 的映射
	merged := batchPlan
	if merged == nil {
		merged = &RemapPlan{
			oldToNew:    make(map[string]string),
			codeToNew:   make(map[string]string),
			handlerName: item.handlerName,
			childData:   subset,
		}
	}
	if merged.oldToNew == nil {
		merged.oldToNew = make(map[string]string)
	}
	if merged.codeToNew == nil {
		merged.codeToNew = make(map[string]string)
	}
	merged.sourceKey = strings.Join(order, ",")

	for _, key := range order {
		snap, ok := catalog.Resolve(key)
		if !ok {
			// L3：发布方尚未执行 / key 拼错 —— 与「值无法解析」严格区分。
			// 文案直接写明**该 namespace 的**发布方 Handler，便于从
			// 「表单配置 → 级联顺序 → catalog」三层中定位（heims §7.1-3 / §12.2）。
			return nil, fmt.Errorf(
				"%w: SourceRemapKey=%q 在本事务中尚无发布方；%s请检查 Cascades 声明顺序（消费方 %s）",
				errs.ErrRemapSourceMissing, key,
				sourceHandlerHint(remapPublishersFor(item.sourceHandlers, key)), item.handlerName)
		}
		if err := mergeIntoPlan(merged, snap, key); err != nil {
			return nil, err
		}
		merged.bindings = append(merged.bindings, byKey[key]...)
	}
	if len(merged.bindings) == 0 {
		return nil, nil
	}
	return merged, nil
}

// mergeIntoPlan 把一个 namespace 快照的映射并入计划，同 key 冲突时报
// ErrRemapInconsistent（heims §12.3 的合并要求）。
func mergeIntoPlan(plan *RemapPlan, snap *RemapSnapshot, key string) error {
	for _, old := range sortedKeys(snap.oldToNew) {
		newULID := snap.oldToNew[old]
		if prev, ok := plan.oldToNew[old]; ok && prev != newULID {
			return fmt.Errorf(
				"%w: 命名空间 %q 中旧 ULID=%s 映射为 %s，与本批已构建的映射 %s 冲突",
				errs.ErrRemapInconsistent, key, old, newULID, prev)
		}
		plan.oldToNew[old] = newULID
	}
	for _, code := range sortedKeys(snap.codeToNew) {
		newULID := snap.codeToNew[code]
		if prev, ok := plan.codeToNew[code]; ok && prev != newULID {
			return fmt.Errorf(
				"%w: 命名空间 %q 中 code=%s 映射为 %s，与本批已构建的映射 %s 冲突",
				errs.ErrRemapInconsistent, key, code, newULID, prev)
		}
		plan.codeToNew[code] = newULID
	}
	return nil
}

// sourceHandlerHint 生成 L3 错误里「发布方 Handler=...」的提示。
//
// 命名空间 → 发布方 Handler 的对应关系对**消费方**是不可见的（消费方只写一个字符串
// key），因此由父 Handler 在登记时按 `Cascades[].RemapKey` 反查注册表得到
// （见 resolveRemapPublishers，在 generic_write_impl 的两个 stage 调用点完成）。
// 这里只做格式化，registry 不可用时不输出该提示（不影响判定语义）。
func sourceHandlerHint(publishers []string) string {
	if len(publishers) == 0 {
		return ""
	}
	return fmt.Sprintf("发布方 Handler=%q 可能未执行，", publishers[0])
}

// publishRemap 把本批**当前已累积**的「旧 ULID → 新 ULID」「code → 新 ULID」
// 发布到 catalog。
//
// 时机（与 heims 共识 §7.3 / §9.4 一致）：本批 BeforeCreatePersist 中的重写已完成、
// 新 ULID 已生成 —— **不要求记录已 INSERT**，因为 catalog 只需要
// 「旧/新 ULID + code」的关系。
//
// **增量发布**（heims §13）：本批可能被拆成多次钩子调用，因此每次都把
// 「全部已回填新 ULID 的记录」重新发布一遍。Publish 对同键同值是幂等的，
// 重发无害；这样无需知道批次何时结束也能保证 catalog 最终完整。
//
// buildPublishMaps 会跳过新 ULID 仍为空的记录 —— 那些是尚未送达钩子的
// 子记录，等它们各自的调用到来时自然会被补发。
func publishRemap(ctx context.Context, item *stagedRemap, publisher string) error {
	if item == nil || item.remapKey == "" {
		return nil
	}
	catalog := remapCatalogFrom(ctx)
	if catalog == nil {
		// 未挂载 catalog 说明调用方没有走 TxCoordinator 入口。
		// 不静默：否则消费方会误报「发布方未执行」，把真实原因（缺少 catalog）掩盖掉。
		return fmt.Errorf("%w: 命名空间 %q 需要发布映射，但当前 context 中没有 remap catalog；"+
			"请确认级联事务经由 TxCoordinator.Run 触发（发布方 %s）",
			errs.ErrRemapInvalidConfig, item.remapKey, publisher)
	}

	// 注意：这里**不能**复用 prepareRemap —— 它要求至少有一个 Binding
	// （Bindings 为空时返回 nil），而发布方只关心「旧→新」映射，可能一个
	// Binding 都不声明（映射的消费方在别的批次）。故单独构建。
	oldToNew, codeToNew := buildPublishMaps(item.childData, item.oldPKs, item.publishCodeField())
	if len(oldToNew) == 0 && len(codeToNew) == 0 {
		return nil
	}
	// 去重：只发布与上次**不同的值**（同键同值才跳过）。
	//
	// 必须按「键 + 值」判重而非只按键：同一 old ULID / code 在本次调用里
	// 可能被解析出与上次不同的新 ULID（上一次它还没被回填，值不完整）——
	// 只按键去重会把「修正」也挡掉（heims §13 的隐患之一）。
	deltaOld := make(map[string]string)
	for k, v := range oldToNew {
		if prev, ok := item.publishedOld[k]; !ok || prev != v {
			deltaOld[k] = v
		}
	}
	deltaCode := make(map[string]string)
	for k, v := range codeToNew {
		if prev, ok := item.publishedCode[k]; !ok || prev != v {
			deltaCode[k] = v
		}
	}
	if len(deltaOld) == 0 && len(deltaCode) == 0 {
		return nil
	}
	if err := catalog.Publish(item.remapKey, deltaOld, deltaCode, publisher); err != nil {
		return err
	}
	if item.publishedOld == nil {
		item.publishedOld = make(map[string]string)
	}
	if item.publishedCode == nil {
		item.publishedCode = make(map[string]string)
	}
	for k, v := range deltaOld {
		item.publishedOld[k] = v
	}
	for k, v := range deltaCode {
		item.publishedCode[k] = v
	}
	return nil
}

// buildPublishMaps 从一批子数据构建发布用的「旧 ULID → 新 ULID」与「code → 新 ULID」。
//
// 空值处理（heims §9.3）：空 code、空旧 ULID、空新 ULID 一律**跳过且不报错** ——
// 草稿态允许存在未填完整的 code，必填约束由应用侧发布校验承担。
//
// **未处理的记录必须跳过**（heims §13 的关键）：本批可能被拆成多次钩子调用，
// 尚未送达的子记录其 `ulid` 仍等于**旧主键**（新 ULID 还没生成/回填）。
// 若此时把它们也发布出去，会产出 `old2 → old2` 这样的**错误映射**，
// 消费方据此「成功」解析到旧记录 —— 比直接报错更糟（静默指向旧版本）。
//
// 因此判据是：`newULID == oldPK` 或 `newULID` 为空 → 尚未处理，跳过。
// 真正的「旧值等于新值」不可能发生（版本重建必然换新 ULID）。
func buildPublishMaps(
	childData []map[string]any,
	oldPKs []string,
	codeField string,
) (map[string]string, map[string]string) {
	oldToNew := make(map[string]string, len(childData))
	codeToNew := make(map[string]string, len(childData))
	for j := range childData {
		// 新 ULID：pkField 与 JSON 名 "ulid" 两者都查（BUG-060 约定）
		newULID := ""
		if v, ok := readPKValue(childData[j], "ulid"); ok {
			newULID = scalarToString(v)
		}
		if newULID == "" {
			continue
		}
		old := ""
		if j < len(oldPKs) {
			old = oldPKs[j]
		}
		// 尚未处理：新 ULID 还没被回填（仍等于旧主键）→ 跳过，等它的调用到来
		if old != "" && newULID == old {
			continue
		}
		if old != "" {
			oldToNew[old] = newULID
		}
		// 业务 code（空值跳过，不报错）
		if codeField != "" {
			if code, ok := childData[j][codeField].(string); ok && code != "" {
				codeToNew[code] = newULID
			}
		}
	}
	return oldToNew, codeToNew
}

// publishCodeField 取本批发布时使用的 code 字段名。
//
// 优先级：
//  1. CascadeRelation.PublishCodeField —— 发布方的**专用**声明（推荐）：
//     发布方常常只有 RemapKey 而没有 Remaps（自己并不消费），此时只能靠它声明；
//  2. 回退到 ReferenceRemap.SourceCodeField —— 兼容「发布方同时也在消费」
//     （如 write_field 的 field_access 批内自引用）的写法，取第一个非空值。
func (item *stagedRemap) publishCodeField() string {
	if item == nil {
		return ""
	}
	if item.publishCodeFieldName != "" {
		return item.publishCodeFieldName
	}
	for _, r := range item.remaps {
		if r.SourceCodeField != "" {
			return r.SourceCodeField
		}
	}
	return ""
}

// mergeRemappedBackSubset 把重写后的 childData 回写进对应的实体。
//
// 时点约束：applyStagedRemap 跑在 `_beforeCreate` **之后**，
// 此时 childData 已通过 MergeTo 变成 entities，后续 `_doCreate` 只读 entities。
// 若不回写，「引用字段被正确改写」这一事实就丢了 —— 落库仍是旧 ULID。
//
// subset 与 entities 一一对应（都是本次钩子调用的那几条）。
// 回写方式：把 map 重新 json 序列化后反序列化进实体，
// 与 MergeTo 的语义一致（JSON tag 映射，BUG-075 起结构化字段亦可）。
func mergeRemappedBackSubset[M service.Record](entities []*M, subset []map[string]any) {
	n := len(subset)
	if len(entities) < n {
		n = len(entities)
	}
	for i := 0; i < n; i++ {
		rec := subset[i]
		if rec == nil || entities[i] == nil {
			continue
		}
		if err := remapMergeRecord(rec, entities[i]); err != nil {
			// 回写失败不静默：交由调用方链路上的错误传播（此处记录并继续，
			// 因为字段级失败通常意味着实体不含该列，属正常形态）
			continue
		}
	}
}

// remapMergeRecord 把 map 合并进记录（JSON 往返，与 MapRequest.MergeTo 同语义）。
func remapMergeRecord(rec map[string]any, target any) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, target)
}

// backfillNewPKsAt 把实体上已生成的新主键回填进 childData 的**指定下标**。
//
// idxs 与 entities 一一对应（由 matchChildIndexes 解析）。
// 子实体主键既可能写在 pkField（gorm 列名）也可能在 JSON 名 "ulid"（BUG-060 约定），
// 因此两个 key 都写 —— MergeTo 只认 JSON 名，多写一个无害。
//
// **必须逐条回填**（heims §13）：不能只处理第一批 —— 未回填的记录会以旧 ULID
// 发布，消费方找不到目标而让事务失败。
func backfillNewPKsAt[M service.Record](
	item *stagedRemap, entities []*M, idxs []int, pkField string,
) {
	if item == nil || len(idxs) == 0 {
		return
	}
	if pkField == "" {
		pkField = item.pkField
	}
	for i, j := range idxs {
		if i >= len(entities) || entities[i] == nil {
			continue
		}
		if j < 0 || j >= len(item.childData) {
			continue
		}
		rec := item.childData[j]
		if rec == nil {
			continue
		}
		pk := extractPKFromResult(entities[i])
		if pk == nil {
			continue
		}
		s := fmt.Sprint(pk)
		if s == "" || s == "<nil>" {
			continue
		}
		writePKValue(rec, pkField, s)
	}
}

// resolveRemapPublishers 找出本 Handler 的**各个消费命名空间**对应的发布方 Handler 名。
//
// 为什么需要：消费方配置里只写 `SourceRemapKey`（字符串 key），看不到发布方在哪个
// Handler 上。L3 的错误文案要求写明「发布方 Handler=... 可能未执行」，否则排查者
// 必须跨「表单配置 → 级联声明顺序 → remap catalog」三层才能定位（heims §7.1-3）。
//
// 注意作用对象是**消费键**而非 rel.RemapKey：消费方关系（form_list_column /
// form_detail_field / form_validation）自身没有 RemapKey，发布方 key 在
// `rel.Remaps[].SourceRemapKey` 里。早期实现误读 rel.RemapKey，导致消费侧
// 登记的发布方恒为 nil、L3 文案缺提示（heims §12.2）。
//
// **解析依据只能是「父 Handler 的 Cascades 声明」**：
//
//	发布方声明 `RemapKey` 的位置是**父 Handler 的 Cascades**，而不是发布方子
//	Handler 自身（v2_field 是个叶子 Handler，它的 config 里没有 RemapKey）。
//	因此遍历 HandlerRegistry 是无效的 —— 注册表里只有子 Handler，它们无法自述
//	「我作为某个父关系的子分支发布了什么」。故由调用方把**父 Handler 的全部
//	Cascades** 传进来，按 key 反查声明了该 RemapKey 的关系的 HandlerName。
//
// 找不到时 map 中不出现该 key（不影响判定语义，仅少一句提示）。
func resolveRemapPublishers(rel CascadeRelation, siblings []CascadeRelation) map[string][]string {
	// 先收集本关系声明过的全部消费键
	keys := make([]string, 0, len(rel.Remaps))
	seen := make(map[string]bool)
	for _, r := range rel.Remaps {
		if r.SourceRemapKey != "" && !seen[r.SourceRemapKey] {
			seen[r.SourceRemapKey] = true
			keys = append(keys, r.SourceRemapKey)
		}
	}
	if len(keys) == 0 {
		return nil
	}

	out := make(map[string][]string, len(keys))
	for _, key := range keys {
		// 在同一 Cascades 数组内找声明了 RemapKey == key 的关系，
		// 其 HandlerName 即发布方 Handler（与 L2 的顺序校验同源）。
		for _, sib := range siblings {
			if sib.RemapKey == key && sib.HandlerName != "" {
				out[key] = appendUnique(out[key], sib.HandlerName)
			}
		}
	}
	return out
}

// remapPublishersFor 返回某个命名空间的发布方 Handler 名（可能多个）。
func remapPublishersFor(publishers map[string][]string, key string) []string {
	if len(publishers) == 0 {
		return nil
	}
	return publishers[key]
}

// remapDescriber 能自述「本 Handler 发布了哪些 remap 命名空间」的 Handler。
//
// 仅 GenericHandler 实现（配置驱动）；自定义 CascadeHandler 无法自述，
// 因此 L1 的存在性校验对它们天然是「未知」而不是「缺失」——
// 见 ValidateRemapKeys 的实现注释。
type remapDescriber interface {
	// RemapPublishKeys 返回本 Handler 的 Cascades 中声明过的全部 RemapKey（去重）。
	RemapPublishKeys() []string
}

// RemapPublishKeys 实现 remapDescriber。
func (h *GenericHandler[M]) RemapPublishKeys() []string {
	var out []string
	seen := make(map[string]bool)
	for _, rel := range h.config.Cascades {
		if rel.RemapKey == "" || seen[rel.RemapKey] {
			continue
		}
		seen[rel.RemapKey] = true
		out = append(out, rel.RemapKey)
	}
	return out
}

// ============================================================
// 三层顺序校验（与 heims 共识 §7.1 / §9.1 / §9.5-2 一致）
//
//	L1 全局存在性   —— ValidateRemapKeys()：SourceRemapKey 必须能找到发布方
//	L2 同父级顺序   —— 构造期 panic：同一 Cascades 数组内发布方必须更靠前
//	L3 跨 Handler   —— 运行时 ErrRemapSourceMissing：由 catalog 消费点触发
//
// L1 之所以不在构造期做：GenericHandler 构造时 handlerReg 尚未注入
// （SetHandlerReg 是独立调用），那时看不到任何其它 Handler。
// 故 L1 由调用方在所有 SetHandlerReg 完成后显式调用一次（幂等）。
// ============================================================

// validateCascadeAssemblies 构造期校验装配声明（v3，fail-fast）。
//
// 两条规则：
//
//  1. **同一关系不得同时配置 Remaps 与 Assemblies**（设计文档 §8.1 B3）：
//     两套机制并存会让行为难以预期（一个在落库前重写、一个在落库前装配，
//     谁先谁后、映射从哪来都不清晰）。应用方已确认按关系逐个迁移，
//     不需要同一关系半旧半新 —— 故直接报错而非"取其一 + WARN"。
//
//  2. 装配声明本身合法（Match/Assign 非空、键非空、Source 数组标记合规）。
func (h *GenericHandler[M]) validateCascadeAssemblies() error {
	for i, rel := range h.config.Cascades {
		if len(rel.Assemblies) > 0 && len(rel.Remaps) > 0 {
			return fmt.Errorf(
				"Cascades[%d]（HandlerName=%q）同时配置了 Remaps 与 Assemblies；"+
					"两套机制互斥（一个在落库时生成 ULID 再回写引用，一个在请求入口预分配），"+
					"请按关系逐个迁移：新关系用 Assemblies，未迁移的保持 Remaps",
				i, rel.HandlerName)
		}
		if err := validateAssemblies(rel, h.config.Cascades); err != nil {
			return err
		}
	}
	return nil
}

// validateRemapKeyOrder 构造期 L2 校验：同一 Cascades 数组内的顺序。
//
// 只看**本 Handler 自己的** Cascades：跨 Handler 子树的执行序无法静态推断
// （发布方可能在子 Handler 里，见 heims 的表单结构），越权推断会误报。
func (h *GenericHandler[M]) validateRemapKeyOrder() error {
	// 先收集本数组内各发布键首次出现的下标
	publishIdx := make(map[string]int)
	for i, rel := range h.config.Cascades {
		if rel.RemapKey == "" {
			continue
		}
		if _, ok := publishIdx[rel.RemapKey]; !ok {
			publishIdx[rel.RemapKey] = i
		}
	}
	// 再检查消费：同数组内**存在**该 key 的发布方时，发布方必须更靠前
	for i, rel := range h.config.Cascades {
		for _, r := range rel.Remaps {
			key := r.SourceRemapKey
			if key == "" {
				continue
			}
			pi, ok := publishIdx[key]
			if !ok {
				// 发布方不在本数组（可能在子 Handler 子树）→ L2 完全跳过，不报错。
				// 这是合法且预期的主流形态（表单的 write_field 正是如此）。
				continue
			}
			if pi >= i {
				return fmt.Errorf(
					"SourceRemapKey=%q 的发布方声明在 Cascades[%d]，"+
						"而消费方是 Cascades[%d]（HandlerName=%q）—— 同数组内发布方必须更靠前",
					key, pi, i, rel.HandlerName)
			}
		}
	}
	return nil
}

// ValidateRemapKeys L1 全局存在性校验：所有 SourceRemapKey 都必须能找到发布方。
//
// 幂等、可重复调用（不缓存结果：配置修正后再次调用即可通过）。
// **聚合报告**全部缺失的 key（而不是只报第一个），避免「启动 → 补一个 key → 再启动」
// 的反复试错（heims §9.1）。
//
// 返回 error 而不 panic：由调用方决定是记录 fatal 还是终止启动。
//
// 注意校验范围：发布键与消费键都声明在**父 Handler 的 Cascades** 上
// （发布方是某个 CascadeRelation.RemapKey，消费方是另一个的 Remaps[].SourceRemapKey），
// 因此本 Handler 自身的发布键 + 注册表内其它 Handler 的发布键共同构成「已知发布方」集合，
// 而消费键则来自**所有能找到的 Handler**，既包括注册表内的，也包括本 Handler 自己。
//
// 典型用法：在应用把所有父 Handler 都 SetHandlerReg 之后，对其中一个调用一次即可
// （它会连带扫描注册表内的兄弟 Handler）。
func (h *GenericHandler[M]) ValidateRemapKeys() error {
	if h.handlerReg == nil {
		return fmt.Errorf(
			"[gocrux] ValidateRemapKeys 需要 HandlerRegistry：" +
				"请在 SetHandlerReg 之后再调用（当前未注入）")
	}
	return ValidateRemapKeysIn(h.handlerReg, h)
}

// remapKeyDescriber 同时能自述发布键与消费键的 Handler（GenericHandler 实现）。
type remapKeyDescriber interface {
	remapDescriber
	remapConsumerDescriber
}

// ValidateRemapKeysIn 对注册表 + 调用方自身做一次全局 L1 校验。
//
// 「调用方自身」（extra）必须传入：父 Handler 往往**不在**注册表里
// （注册的是它的子 Handler，供级联时 Get 用），而发布键/消费键恰恰声明在
// 父 Handler 的 Cascades 上 —— 只扫注册表会漏掉绝大多数声明。
//
// 返回的 error 聚合全部「无发布方」的 key，每个 key 附带消费方 Handler 列表。
func ValidateRemapKeysIn(reg *HandlerRegistry, extra remapKeyDescriber) error {
	// 1. 收集全部已声明的发布键（注册表内 + 调用方自身）
	published := make(map[string]bool)
	collectPublish := func(d remapDescriber) {
		for _, k := range d.RemapPublishKeys() {
			published[k] = true
		}
	}
	if reg != nil {
		reg.Each(func(_ string, ch CascadeHandler) {
			if d, ok := ch.(remapDescriber); ok {
				collectPublish(d)
			}
		})
	}
	if extra != nil {
		collectPublish(extra)
	}

	// 2. 收集全部消费键（key → 消费方 Handler 名列表，用于聚合报告）
	missing := make(map[string][]string)
	var order []string
	collectConsume := func(name string, d remapConsumerDescriber) {
		for _, k := range d.RemapConsumeKeys() {
			if published[k] {
				continue
			}
			if _, seen := missing[k]; !seen {
				order = append(order, k)
			}
			missing[k] = appendUnique(missing[k], name)
		}
	}
	if reg != nil {
		reg.Each(func(name string, ch CascadeHandler) {
			if d, ok := ch.(remapConsumerDescriber); ok {
				collectConsume(name, d)
			}
		})
	}
	if extra != nil {
		collectConsume(remapDescriberName(extra), extra)
	}

	if len(order) == 0 {
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[gocrux] 以下 SourceRemapKey 找不到发布方（共 %d 个）:", len(order))
	for _, k := range order {
		fmt.Fprintf(&b, "\n  - %q（消费方 Handler: %s）",
			k, strings.Join(missing[k], " / "))
	}
	b.WriteString("\n请检查 Cascades 中是否漏配 RemapKey，或 key 拼写是否有误。")
	return errors.New(b.String())
}

// remapDescriberName 返回 Handler 的展示名（用于聚合报告）。
func remapDescriberName(d any) string {
	if n, ok := d.(interface{ Name() string }); ok {
		return n.Name()
	}
	return "本 Handler"
}

// remapConsumerDescriber 能自述「本 Handler 消费了哪些 remap 命名空间」的 Handler。
type remapConsumerDescriber interface {
	// RemapConsumeKeys 返回本 Handler 的 Cascades 中声明过的全部 SourceRemapKey（去重）。
	RemapConsumeKeys() []string
}

// RemapConsumeKeys 实现 remapConsumerDescriber。
func (h *GenericHandler[M]) RemapConsumeKeys() []string {
	var out []string
	seen := make(map[string]bool)
	for _, rel := range h.config.Cascades {
		for _, r := range rel.Remaps {
			k := r.SourceRemapKey
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// Name 返回本 Handler 的资源名（svcName），供校验报告展示。
func (h *GenericHandler[M]) Name() string { return h.svcName }

// appendUnique 追加去重（保持顺序）。
func appendUnique(list []string, v string) []string {
	for _, s := range list {
		if s == v {
			return list
		}
	}
	return append(list, v)
}

// InstallRemapHook 把「落库前执行登记重映射」注册为 service 的 BeforeCreatePersist 钩子。
//
// 幂等，可重复调用。应用已自定义该钩子时包一层：先跑应用逻辑（可能补字段），
// 再做重映射（重映射需要看到最新值）。
//
// 未配置任何 Remaps 的 Handler 调用它是无害的：无登记项时直接返回，运行时零成本。
// 但**建议所有可能作为级联目标的 Handler 都调用**，否则父 Handler 侧的 Remaps 声明
// 不会生效（登记了但没人执行）。
func (h *GenericHandler[M]) InstallRemapHook() {
	if h.remapHookInstalled {
		return
	}
	h.remapHookInstalled = true

	svcHooks := h.svc.HooksSnapshot()
	if svcHooks.BeforeCreatePersist == nil {
		svcHooks.BeforeCreatePersist = h.applyStagedRemap
	} else {
		prev := svcHooks.BeforeCreatePersist
		svcHooks.BeforeCreatePersist = func(ctx context.Context, entities []*M) error {
			if err := prev(ctx, entities); err != nil {
				return err
			}
			return h.applyStagedRemap(ctx, entities)
		}
	}
	h.svc.SetHooks(svcHooks)
}
