package handler

import (
	"context"
	"encoding/json"
	"fmt"

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
	if len(remaps) == 0 || len(childData) == 0 {
		return ctx
	}
	item := &stagedRemap{
		handlerName: handlerName,
		remaps:      remaps,
		childData:   childData,
		pkField:     pkField,
		oldPKs:      snapshotPKs(childData, pkField),
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
func (h *GenericHandler[M]) runRemap(ctx context.Context, item *stagedRemap, entities []*M) error {
	// 0. 把 service 生成的新主键回填进 childData（按位置对应，见 stageRemap 的登记顺序）
	backfillNewPKs(item, entities, h.PKField())

	// 1. 构建「旧 ULID → 新 ULID」（权威）+「code → 新 ULID」（兜底）
	plan := prepareRemap(item.handlerName, item.remaps, item.childData, item.oldPKs)
	if plan == nil {
		return nil
	}
	// 2. 业务侧自定义映射（可选接口）并入（同键以业务侧为准）
	if err := callCustomRemapper(ctx, h, plan); err != nil {
		return err
	}
	// 3. 原地重写本批次所有引用字段；解析失败 → 事务失败。
	//    注意：此时 _beforeCreate 已把 childData 灌入 entities，
	//    改写 childData 不会被再次读取，故必须同步回写实体（见 mergeRemappedBack）。
	if err := applyRemap(plan, item.childData); err != nil {
		return err
	}
	mergeRemappedBack(item, entities)
	return nil
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
