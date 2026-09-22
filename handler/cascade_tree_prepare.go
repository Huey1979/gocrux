package handler

import (
	"context"

	errs "github.com/Huey1979/gocrux/errors"
)

// ============================================================
// 阶段 1（递归）：展开整棵 Cascade 树 + 预分配 ULID + 登记 Target 索引
//
// 本文件是 cascade_tree.go 的「准备」侧实现，create / update 两套语义各自一份
// （update 需要回填、清 PK、身份映射，与 create 差异大，强行合并会让两边都难读）。
//
// 两条铁律（工程前提 A3 + §25.3）：
//  1. **不重新提取**：childData 是请求/回填数据的**同一批句柄**，阶段 3 落库读的
//     就是它（否则 wrapKey 包裹 map、DB 回填新切片会让「分配给 A 的 ULID 用不到 A 上」）。
//  2. **不落库**：阶段 1 只做内存准备（唯一的 DB 交互是回填查询与旧子记录清理，
//     它们是 update 语义本身要求的，非落库）。
// ============================================================

// prepareCreateSubtree 阶段 1（create 语义，递归）。
//
// 对 rawMaps 这批父记录：
//
//	① 规范化钩子（§26.3，浅到深）
//	② 逐关系提取子数据并注入父 FK
//	③ 预分配 ULID（仅 v3 关系）+ 登记 Target 索引
//	④ 递归处理子批次（**孙批次及更深**）—— 这正是 §25 缺陷的修复点：
//	   没有 ④ 时，孙批次要到祖批次阶段 3 才登记，祖批次的消费方在阶段 2 就已装配完。
func (h *GenericHandler[M]) prepareCreateSubtree(
	ctx context.Context, rawMaps []map[string]any, parentPKs []any,
	onFilter func(CascadeRelation) bool,
) (subtreePrepareResult, error) {
	res := subtreePrepareResult{ctx: ctx}
	if len(rawMaps) == 0 {
		return res, nil
	}

	// §26.3：本层 raw map 树在展开 / 预分配 / 装配之前规范化（浅到深，由应用实现）。
	// 收到的就是后续提取与落库的同一批 map 句柄。
	//
	// 顺序很关键：钩子必须在「无注册表 / 无子 Handler」的早退**之前**触发 ——
	// 叶子层（如 list_column）没有下级、接入方也常常不给它注入注册表，
	// 但它自己的记录同样可能带 JSON 文本节点需要规范化。
	if err := h.runBeforeCascadePrepare(ctx, rawMaps); err != nil {
		return res, err
	}
	if h.handlerReg == nil {
		return res, nil
	}

	// 注意：即使没有注册表（未启用装配通道）也要继续 —— 本函数同时承担既有的
	// 「提取子数据 + 注入 FK + 自引用 FK 解析」职责；预分配/登记在无注册表时是空操作。
	var nodes []*cascadeBatchNode
	for _, rel := range h.config.Cascades {
		if onFilter != nil && !onFilter(rel) {
			continue
		}
		childHandler := h.handlerReg.Get(rel.HandlerName)
		if childHandler == nil {
			continue
		}

		// 收集所有父记录的子数据并注入 FK。
		// FK 取自父记录的**权威 PK**（调用方显式传入）：顶层来自 service 落库结果
		// （预分配通道下与 preallocateRoot 写回的值一致），更深层来自上一轮预分配 ——
		// 不能读原始请求 map，那里的主键在未启用预分配时是空的。
		var all []map[string]any
		for i, raw := range rawMaps {
			if raw == nil {
				continue
			}
			var pk any
			if i < len(parentPKs) {
				pk = parentPKs[i]
			}
			cd := extractChildData(raw, rel.ChildrenField, rel.ChildrenWrapKey)
			for j := range cd {
				setByPath(cd[j], rel.FKField, pk)
			}
			all = append(all, cd...)
		}
		if len(all) == 0 {
			continue
		}

		// 预分配 + 登记 Target 索引（仅 v3 关系；v2 关系的语义是「落库时才生成新 ULID」，
		// 提前填值会让 v2 的 prepareRemap 把新值当成旧 PK，映射失效）。
		if useV3Prealloc(rel) {
			res.ctx = preallocateChildBatch(res.ctx, rel, rel.HandlerName, childHandler.PKField(), all)
			// 子 Handler 自身也可声明 Target（应用方 §27.3）；与关系级同名时不重复登记。
			registerBatchOwnTarget(preallocRegistryFrom(res.ctx), childHandler, rel.Target,
				rel.HandlerName, childHandler.PKField(), all)
		}

		// 自引用 FK 代码解析（如 parent_menu_code → parent_item_ulid）：预分配已填好 PK
		// 时它以预分配值为准（幂等）。
		if sfk := childHandler.SelfFKField(); sfk != "" {
			resolveSelfFKCodeRefs(all, childHandler.PKField(), sfk)
		}

		node := &cascadeBatchNode{
			rel:       rel,
			handler:   childHandler,
			childData: all,
			// _temp_ref 索引：阶段 3 落库后据此填充 __ref: 映射表
			// （与既有实现一致 —— 占位符解析需要「前序批次已落库」的 ULID）。
			tempRefs: collectTempRefsOrdered(all, rel.HandlerName),
		}

		// 本批记录是否已持有权威 PK → 决定子树能否在阶段 1 继续递归：
		// 孙批次的 FK 需要本批记录的身份，而 v2 通道 / 未启用预分配时 PK 要到
		// 落库那一刻才生成。此时**不动子树**，留给嵌套 Handler 在落库后自行展开
		// （即既有行为，prepared=false 时也不会把节点下传）。
		childPKField := childHandler.PKField()
		childPKs := make([]any, len(all))
		node.prepared = len(all) > 0
		for j := range all {
			v, ok := readPKValue(all[j], childPKField)
			if !ok || v == nil || scalarToString(v) == "" {
				node.prepared = false
				break
			}
			childPKs[j] = v
		}

		// ④ 递归子树（深度/防环由 childPreparer.subtreeCtx 判定）
		if node.prepared {
			if sub, ok := childHandler.(cascadeTreePreparer); ok {
				if subCtx, allowed := sub.subtreeCtx(res.ctx); allowed {
					subRes, err := sub.prepareCreateSubtree(subCtx, all, childPKs, onFilter)
					if err != nil {
						return res, err
					}
					res.ctx = subRes.ctx
					node.subs = subRes.nodes
				}
			}
		}
		nodes = append(nodes, node)
	}
	res.nodes = nodes
	return res, nil
}

// prepareUpdateSubtree 阶段 1（update 语义，递归）。
//
// 逐记录、逐关系：
//
//	① 规范化钩子（§26.3）——含「版本化 update 未传子表」时的 DB 回填数据
//	② 取子数据：请求携带 → 直接用；未携带 → 按父**旧身份**从 DB 回填
//	③ 非版本化父 + 携带子表 → 先删旧子记录（全量替换语义）
//	④ 需要走 CREATE 语义时：v2 快照旧 PK 后清空；v3 清空后**填回预分配值**
//	   （身份映射：旧 PK 作身份键，无法唯一对应则报错）
//	⑤ 预分配 + 登记 Target
//	⑥ 递归子树（子层记录带「清 PK 前身份」与「当前身份」，供更深层回填/注入 FK）
func (h *GenericHandler[M]) prepareUpdateSubtree(
	ctx context.Context, records []updateLevelRecord, parentVersioned bool,
) (subtreePrepareResult, error) {
	res := subtreePrepareResult{ctx: ctx}
	if len(records) == 0 {
		return res, nil
	}

	// 本层父记录是否按版本化处理（子批次的 CREATE/UPDATE 语义由此决定）
	passParentVersioned := h.svc.IsVersionMode() || parentVersioned

	// §26.3：本层 raw map 树规范化（含 DB 回填数据 —— 它们在请求体里不存在，
	// 因此规范化必须逐层触发，而不能只在入口做一次）。
	//
	// 顺序与 create 侧同理：钩子必须在「无注册表 / 无子 Handler」的早退**之前**
	// 触发 —— 叶子层（如 list_column、form_validation）没有下级、接入方也常常
	// 不给它注入注册表，但它自己的记录同样可能带 JSON 文本节点需要规范化。
	rawMaps := make([]map[string]any, 0, len(records))
	for _, r := range records {
		if r.raw != nil {
			rawMaps = append(rawMaps, r.raw)
		}
	}
	if err := h.runBeforeCascadePrepare(ctx, rawMaps); err != nil {
		return res, err
	}
	if h.handlerReg == nil {
		return res, nil
	}

	reg := preallocRegistryFrom(ctx)
	var nodes []*cascadeBatchNode

	for _, rel := range h.config.Cascades {
		if !rel.OnUpdate {
			continue
		}
		childHandler := h.handlerReg.Get(rel.HandlerName)
		if childHandler == nil {
			continue
		}
		// v2 通道：落库时生成 ULID + 事后重映射（与 v3 预分配互斥，构造期已校验）
		isV2 := !useV3Prealloc(rel)
		pkField := childHandler.PKField()

		for _, rec := range records {
			raw := rec.raw
			if raw == nil {
				continue
			}

			childData := extractChildData(raw, rel.ChildrenField, rel.ChildrenWrapKey)
			_, hasChildren := raw[rel.ChildrenField]

			if !hasChildren {
				if rec.oldPK == nil {
					continue // 新建记录无子数据 → 跳过后代
				}
				oldChildren, txErr := childHandler.DoList(ctx, rel.FKField, rec.oldPK, false)
				if txErr != nil {
					return res, errs.ErrCascadeUpdateBackfill(rel.HandlerName, txErr)
				}
				childData = oldChildren
				// 为回填数据补 id 键，确保 GetID() 能匹配到主键。
				// PKField() 可能是 gorm 列名（如 field_ulid）而 JSON 名是 ulid
				// （gentity 约定），两者都查（BUG-060）。
				for j := range childData {
					if _, ok := childData[j]["id"]; !ok {
						if pkVal, exists := childData[j][pkField]; exists && pkVal != nil && pkVal != "" {
							childData[j]["id"] = pkVal
						} else if pkVal, exists := childData[j]["ulid"]; exists && pkVal != nil && pkVal != "" {
							childData[j]["id"] = pkVal
						}
					}
				}
			} else if !passParentVersioned && rec.oldPK != nil {
				// 父非版本化且携带请求子数据 → 先清理旧子记录（全量替换）
				if txErr := childHandler.DoDeleteByFK(ctx, rel.FKField, []any{rec.oldPK}); txErr != nil {
					return res, errs.ErrCascadeUpdateCleanup(rel.HandlerName, txErr)
				}
			}

			// 传递给子 Handler 的版本化标志
			passToChild := passParentVersioned
			if !passParentVersioned && hasChildren && rec.oldPK != nil {
				passToChild = true // 非版本化全量替换：旧子记录已删，子数据应走 CREATE（BUG-018）
			}

			// 清 PK 之前先留存旧身份快照（回填查询 / v2 重映射 / 子层身份都用它）
			oldSnap := snapshotPKs(childData, pkField)

			if passToChild && (hasChildren || passParentVersioned) {
				if isV2 {
					// v2：只清 PK，不填预分配值 —— 新 ULID 必须留给 service 生成，
					// 否则 v2 的「旧 ULID → 新 ULID」映射拿不到对照。
					clearChildPKs(childData, pkField)
				} else {
					// v3：清 PK 后**立即填回预分配值**（可在落库前装配）。
					// 身份映射无法唯一对应时报错，绝不按位置猜测（应用方 §13.4）。
					if _, txErr := rebuildChildPKs(reg, rel.HandlerName, pkField, childData); txErr != nil {
						return res, txErr
					}
				}
				if sfk := childHandler.SelfFKField(); sfk != "" {
					resolveSelfFKCodeRefs(childData, pkField, sfk)
				}
			}
			// 无子数据 + 非版本化父：保留「原地改 FK」语义（BUG-018/020），
			// 但版本化父的 passToChild 保持 true（回填数据已清 PK，走 CREATE 复制重建，BUG-059）
			if !hasChildren && rec.oldPK != nil && !passParentVersioned {
				passToChild = false
			}

			// 预分配本批 ULID 并登记 Target 索引（必须在 rebuildChildPKs 之后：
			// 重建路径已填好预分配值（幂等），此处为「未清 PK 的批次」补分配并统一登记）
			if !isV2 {
				res.ctx = preallocateChildBatch(res.ctx, rel, rel.HandlerName, pkField, childData)
				// 子 Handler 自身也可声明 Target（应用方 §27.3）；同名时不重复登记。
				registerBatchOwnTarget(preallocRegistryFrom(res.ctx), childHandler, rel.Target,
					rel.HandlerName, pkField, childData)
			}
			if len(childData) == 0 {
				continue
			}

			node := &cascadeBatchNode{
				rel:         rel,
				handler:     childHandler,
				childData:   childData,
				oldPKs:      oldSnap,
				passToChild: passToChild,
				fkValue:     rec.newPK,
			}

			// ⑥ 递归子树：子层每条记录同时携带「清 PK 前身份」（更深层回填查询用）
			// 与「当前身份」（注入孙批次 FK 用）。
			//
			// 前提：本批记录在阶段 1 已持有权威 PK。v2 通道在清 PK 后要等 service
			// 生成，此时无法为孙批次注入 FK —— 子树留给嵌套 Handler 落库后自行展开
			// （既有行为；prepared=false 时也不会把节点下传）。
			childPKs := make([]any, len(childData))
			node.prepared = len(childData) > 0
			for j := range childData {
				v, ok := readPKValue(childData[j], pkField)
				if !ok || v == nil || scalarToString(v) == "" {
					node.prepared = false
					break
				}
				childPKs[j] = v
			}
			if node.prepared {
				if sub, ok := childHandler.(cascadeTreePreparer); ok {
					if subCtx, allowed := sub.subtreeCtx(res.ctx); allowed {
						subRecords := make([]updateLevelRecord, len(childData))
						for j := range childData {
							subRecords[j] = updateLevelRecord{
								raw:   childData[j],
								oldPK: levelOldPK(oldSnap, childData[j], j),
								newPK: childPKs[j],
							}
						}
						subRes, err := sub.prepareUpdateSubtree(subCtx, subRecords, passToChild)
						if err != nil {
							return res, err
						}
						res.ctx = subRes.ctx
						node.subs = subRes.nodes
					}
				}
			}
			nodes = append(nodes, node)
		}
	}
	res.nodes = nodes
	return res, nil
}

// levelOldPK 取「清 PK 前身份」：优先用快照，快照为空时回退 map 上的 id/ulid
// （回填数据在被清 PK 前，其 id 键已由调用方注入）。
func levelOldPK(snap []string, raw map[string]any, idx int) any {
	if idx < len(snap) && snap[idx] != "" {
		return snap[idx]
	}
	if raw != nil {
		for _, k := range []string{"id", "ulid"} {
			if v, ok := raw[k]; ok && v != nil && v != "" {
				return v
			}
		}
	}
	return nil
}

// levelNewPK 取记录当前（落库时）的身份：预分配/重建后写在 map 上。
func levelNewPK(raw map[string]any, pkField string) any {
	if raw == nil {
		return nil
	}
	if v, ok := readPKValue(raw, pkField); ok && v != nil && v != "" {
		return v
	}
	return nil
}
