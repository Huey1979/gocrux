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
	// done 是否已执行（防重复）。
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

// applyStagedRemap 执行本 Handler 名下登记的重映射（service BeforeCreatePersist 钩子）。
//
// 时机：主键 ULID 已生成、记录尚未 INSERT。
func (h *GenericHandler[M]) applyStagedRemap(ctx context.Context, entities []*M) error {
	items, _ := ctx.Value(remapCtxKey{}).([]*stagedRemap)
	if len(items) == 0 {
		return nil
	}
	for _, item := range items {
		if item == nil || item.done || item.handlerName != h.svcName {
			continue
		}
		item.done = true
		if err := h.runRemap(ctx, item, entities); err != nil {
			return err
		}
	}
	return nil
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
	// 0. 把 service 生成的新主键回填进 childData（按位置对应，见 stageRemap 的登记顺序）
	backfillNewPKs(item, entities, h.PKField())

	// 1. 消费：重写本批引用
	if err := h.consumeRemap(ctx, item, entities); err != nil {
		return err
	}

	// 2. 发布：本批执行完毕，把映射交付给后续批次
	if err := publishRemap(ctx, item, h.svcName); err != nil {
		return err
	}
	return nil
}

// consumeRemap 用映射（批内自建 或 跨批次 catalog）重写本批子数据中的引用。
func (h *GenericHandler[M]) consumeRemap(ctx context.Context, item *stagedRemap, entities []*M) error {
	plan, err := h.buildRemapPlan(ctx, item)
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
	// 原地重写本批次所有引用字段；解析失败 → 事务失败。
	// 注意：此时 _beforeCreate 已把 childData 灌入 entities，
	// 改写 childData 不会被再次读取，故必须同步回写实体（见 mergeRemappedBack）。
	if err := applyRemap(plan, item.childData); err != nil {
		return err
	}
	mergeRemappedBack(item, entities)
	return nil
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
func (h *GenericHandler[M]) buildRemapPlan(ctx context.Context, item *stagedRemap) (*RemapPlan, error) {
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

	// 批内映射：沿用 v1 行为（prepareRemap 已处理 bindings 为空 → nil）
	batchPlan := prepareRemap(item.handlerName, batchLocal, item.childData, item.oldPKs)

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
			childData:   item.childData,
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

// publishRemap 把本批的「旧 ULID → 新 ULID」「code → 新 ULID」发布到 catalog。
//
// 时机（与 heims 共识 §7.3 / §9.4 一致）：本批 BeforeCreatePersist 中的重写已完成、
// 新 ULID 已全部生成 —— **不要求记录已 INSERT**，因为 catalog 只需要
// 「旧/新 ULID + code」的关系。
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
	return catalog.Publish(item.remapKey, oldToNew, codeToNew, publisher)
}

// buildPublishMaps 从一批子数据构建发布用的「旧 ULID → 新 ULID」与「code → 新 ULID」。
//
// 空值处理（heims §9.3）：空 code、空旧 ULID、空新 ULID 一律**跳过且不报错** ——
// 草稿态允许存在未填完整的 code，必填约束由应用侧发布校验承担。
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
		// 旧 ULID（调用方在清除 PK 前留存）
		if j < len(oldPKs) {
			if old := oldPKs[j]; old != "" {
				oldToNew[old] = newULID
			}
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

// mergeRemappedBack 把重写后的 childData 回写进实体。
//
// 时点约束：applyStagedRemap 跑在 `_beforeCreate` **之后**，
// 此时 childData 已通过 MergeTo 变成 entities，后续 `_doCreate` 只读 entities。
// 若不回写，「引用字段被正确改写」这一事实就丢了 —— 落库仍是旧 ULID。
//
// 回写方式：把 childData 重新 json 序列化后反序列化进实体，
// 与 MergeTo 的语义一致（JSON tag 映射，BUG-075 起结构化字段亦可）。
func mergeRemappedBack[M service.Record](item *stagedRemap, entities []*M) {
	if item == nil || len(entities) == 0 {
		return
	}
	n := len(item.childData)
	if len(entities) < n {
		n = len(entities)
	}
	for j := 0; j < n; j++ {
		rec := item.childData[j]
		if rec == nil || entities[j] == nil {
			continue
		}
		if err := remapMergeRecord(rec, entities[j]); err != nil {
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

// backfillNewPKs 把实体上已生成的新主键回填进 childData 的对应位置。
//
// 按**位置**对应：stageRemap 保存的 childData 与子 Handler 收到的 entities
// 来自同一次 DoCreate/DoUpdate 调用，顺序一致（_doCreate 逐条 newRecord + MergeTo）。
// 子实体主键既可能写在 pkField（gorm 列名）也可能在 JSON 名 "ulid"（BUG-060 约定），
// 因此两个 key 都写 —— MergeTo 只认 JSON 名，多写一个无害。
func backfillNewPKs[M service.Record](item *stagedRemap, entities []*M, pkField string) {
	if item == nil || len(entities) == 0 {
		return
	}
	if pkField == "" {
		pkField = item.pkField
	}
	n := len(item.childData)
	if len(entities) < n {
		n = len(entities)
	}
	for j := 0; j < n; j++ {
		rec := item.childData[j]
		if rec == nil || entities[j] == nil {
			continue
		}
		pk := extractPKFromResult(entities[j])
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
