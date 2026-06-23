package handler

import (
	"context"
	"fmt"
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

// expandCascadesBatch 批量展开级联子记录并回填到父结果。
// 用于 _doList 的 Cascades 展开，封装收集 PK → 查子记录 → 分组 → 回填 的完整流程。
func (h *GenericHandler[M]) expandCascadesBatch(ctx context.Context, childCtx context.Context, result []map[string]any) {
	if len(h.config.Cascades) == 0 || h.handlerReg == nil {
		return
	}
	for _, rel := range h.config.Cascades {
		// ListSkipCascades 约定：List 时默认不展开级联，可通过 ?expand 覆写
		if !h.shouldExpandCascade(ctx, rel.ChildrenField) {
			continue
		}
		childHandler, ok := h.shouldExpandField(ctx, childCtx, rel.ChildrenField, rel.HandlerName, false)
		if !ok {
			continue
		}

		// 收集所有父实体 PK
		pkField := h.PKField()
		pkSet := make(map[string]bool)
		for _, m := range result {
			pk := m[pkField]
			if pk != nil {
				s := fmt.Sprint(pk)
				if s != "" && s != "<nil>" {
					pkSet[s] = true
				}
			}
		}
		if len(pkSet) == 0 {
			continue
		}

		pkList := make([]any, 0, len(pkSet))
		for k := range pkSet {
			pkList = append(pkList, k)
		}

		cascCtx := h.buildFieldCtx(childCtx, rel.ChildrenField, rel.HandlerName)
		if isVisited(cascCtx, rel.HandlerName, "batch") {
			continue
		}

		var fkVal any = pkList
		if len(pkList) == 1 {
			fkVal = pkList[0]
		}
		children, err := childHandler.DoList(cascCtx, rel.FKField, fkVal, rel.FollowPublished)
		if err != nil {
			// 级联展开失败静默跳过（不中断整个列表查询）
			continue
		}

		// 按 FKField 分组
		groups := make(map[string][]map[string]any)
		for _, child := range children {
			fkVal := fmt.Sprint(child[rel.FKField])
			groups[fkVal] = append(groups[fkVal], child)
		}

		// 回填到每条父结果
		for _, m := range result {
			pk := m[pkField]
			if pk != nil {
				if group, ok := groups[fmt.Sprint(pk)]; ok {
					m[rel.ChildrenField] = group
				}
			}
		}
	}
}

// ============================================================
// forEachCascade — 级联迭代器
//
// 遍历 CascadeRelation 列表，按 onFlag 过滤 → 查 childHandler → 执行回调。
// 消除 _doCreate/_doUpdate/_doDelete/_doActivate/_doEditVersion 中的重复循环。
// ============================================================

// forEachCascade 迭代匹配 onFlag 的级联关系，查 handler 后回调 fn。遇错即返回。
func (h *GenericHandler[M]) forEachCascade(
	onFlag func(CascadeRelation) bool,
	fn func(rel CascadeRelation, child CascadeHandler) error,
) error {
	if h.handlerReg == nil {
		return nil
	}
	for _, rel := range h.config.Cascades {
		if !onFlag(rel) {
			continue
		}
		child := h.handlerReg.Get(rel.HandlerName)
		if child == nil {
			continue
		}
		if err := fn(rel, child); err != nil {
			return err
		}
	}
	return nil
}

// forEachCascadeChild 查询级联子记录并逐个执行回调。
// 封装 DoList → iterate → 提取 PK → 回调 的通用流程。
func forEachCascadeChild(
	ctx context.Context,
	childHandler CascadeHandler,
	fkField string,
	fkValue any,
	fn func(ctx context.Context, child map[string]any, childPK any) error,
) error {
	children, err := childHandler.DoList(ctx, fkField, fkValue, false)
	if err != nil {
		return err
	}
	pkField := childHandler.PKField()
	for _, child := range children {
		childPK := child[pkField]
		if childPK == nil {
			continue
		}
		if err := fn(ctx, child, childPK); err != nil {
			return err
		}
	}
	return nil
}
