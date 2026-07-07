package handler

import (
	"context"
	"fmt"
	"strings"

	errs "github.com/Huey1979/gocrux/errors"
	"github.com/Huey1979/gocrux/service"
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

// -------- Create --------

func (h *GenericHandler[M]) _beforeCreate(_ context.Context, input []service.CrudRequest[M]) ([]service.CrudRequest[M], error) {
	// 默认：透传（字段校验已在 createPipeline 中统一完成，此处仅做额外业务预处理）
	return input, nil
}

func (h *GenericHandler[M]) _doCreate(ctx context.Context, input []service.CrudRequest[M]) ([]*M, error) {
	// 若配置了级联创建且已注入 TxCoordinator + HandlerRegistry，
	// 在事务内编排父实体创建 + 子实体级联创建。
	//
	// visited + depth 防环/防无限深度（与 Get/List 管线中 canExpandTo 一致）：
	// - 若当前 Handler 已在级联链中出现过（A→B→A）→ 退化为简单创建
	// - 若级联深度已耗尽 → 退化为简单创建
	if h.hasCascadeFlag(func(r CascadeRelation) bool { return r.OnCreate }) && h.txCoord != nil && h.handlerReg != nil {
		if isVisited(ctx, h.svcName, "batch") {
			return h.svc.Create(ctx, input)
		}
		if d, ok := getDepth(ctx); ok && d <= 0 {
			return h.svc.Create(ctx, input)
		}

		var results []*M
		rawMaps, _ := ctx.Value(rawCreateMapsKey{}).([]map[string]any)

		err := h.txCoord.Run(ctx, func(txCtx context.Context) error {
			// 1. 创建父实体
			created, txErr := h.svc.Create(txCtx, input)
			if txErr != nil {
				return txErr
			}
			results = created

			// 2. 级联创建子实体（按级联关系归拢，每个关系只调一次 DoCreate → 一次 InsertBatch）
			if rawMaps == nil {
				return nil
			}

			// 将当前 Handler 加入 visited set，传给所有子 Handler。
			// 复用 cascade.go 的不可变 visitedSet（addVisited 创建新 map 副本），
			// 同层多个子 Handler 各自独立，互不干扰。
			cascadeCtx := addVisited(txCtx, h.svcName, "batch")

			// 深度控制：首层默认 hardMaxExpandDepth（10）层，每级联一层 -1。
			// 到 0 时子 Handler 的 getDepth 检查会阻止继续展开。
			if d, ok := getDepth(txCtx); ok {
				cascadeCtx = withDepth(cascadeCtx, d-1)
			} else {
				cascadeCtx = withDepth(cascadeCtx, hardMaxExpandDepth-1)
			}

			// 跨实体引用映射：级联创建过程中，后续子实体可通过 __ref:handler:temp__ 占位符
			// 引用前面已创建子实体的 ULID。每次 DoCreate 后更新此映射。
			refMap := make(cascadeRefMap)

			for _, rel := range h.config.Cascades {
				if !rel.OnCreate {
					continue
				}
				// 先收集所有父实体的子数据并注入 FK
				var allChildData []map[string]any
				for i, parent := range created {
					parentPK := extractPKFromResult(parent)
					childData := extractChildData(rawMaps[i], rel.ChildrenField, rel.ChildrenWrapKey)
					for j := range childData {
						setByPath(childData[j], rel.FKField, parentPK)
					}
					allChildData = append(allChildData, childData...)
				}
				if len(allChildData) == 0 {
					continue
				}

				childHandler := h.handlerReg.Get(rel.HandlerName)
				if childHandler == nil {
					continue
				}

				// 跨实体引用：解析 allChildData 中的 __ref:handler:temp__ 占位符
				resolveCrossRefs(allChildData, refMap)
				// 收集本批的 _temp_ref 标记
				tempRefs := collectTempRefsOrdered(allChildData, rel.HandlerName)

				// 传递含 visited + depth 的 context，子 Handler 可感知级联链状态
				pks, txErr := childHandler.DoCreate(cascadeCtx, allChildData)
				if txErr != nil {
					return errs.ErrCascadeCreate(rel.HandlerName, txErr)
				}
				// 将本批创建的实体 ULID 加入引用映射，供后续级联批次使用
				updateRefMap(refMap, tempRefs, pks)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		return results, nil
	}

	// 无级联：直接创建
	return h.svc.Create(ctx, input)
}

func (h *GenericHandler[M]) _afterCreate(ctx context.Context, result []*M) ([]*M, error) {
	// GlobalStore：写入缓存
	for _, r := range result {
		h.cacheSet(ctx, r)
	}
	return result, nil
}

// -------- Update --------

func (h *GenericHandler[M]) _beforeUpdate(_ context.Context, reqs []service.CrudRequest[M], _ bool) ([]service.CrudRequest[M], error) {
	// 默认：透传（字段校验已在 updatePipeline 中统一完成）
	return reqs, nil
}

func (h *GenericHandler[M]) _doUpdate(ctx context.Context, reqs []service.CrudRequest[M], parentVersioned bool) ([]*M, error) {
	forceCreate := parentVersioned && !h.svc.IsVersionMode()

	// 若配置了级联更新且已注入 TxCoordinator + HandlerRegistry，
	// 在事务内：逐条更新/创建父实体 → 委托子 Handler 的 DoUpdate 处理子记录。
	if h.hasCascadeFlag(func(r CascadeRelation) bool { return r.OnUpdate }) && h.txCoord != nil && h.handlerReg != nil {
		// visited + depth 防环/防无限深度：退化为逐条更新/创建（不展开子表级联）
		if isVisited(ctx, h.svcName, "batch") {
			return h.updateOrCreate(ctx, reqs, forceCreate)
		}
		if d, ok := getDepth(ctx); ok && d <= 0 {
			return h.updateOrCreate(ctx, reqs, forceCreate)
		}

		rawMaps, _ := ctx.Value(rawUpdateMapsKey{}).([]map[string]any)
		isVersioned := h.svc.IsVersionMode()

		var results []*M
		err := h.txCoord.Run(ctx, func(txCtx context.Context) error {
			// 在事务 context 上叠加 visited + depth 标记，传给子 Handler
			cascadeCtx := addVisited(txCtx, h.svcName, "batch")
			if d, ok := getDepth(txCtx); ok {
				cascadeCtx = withDepth(cascadeCtx, d-1)
			} else {
				cascadeCtx = withDepth(cascadeCtx, hardMaxExpandDepth-1)
			}

			for i, req := range reqs {
				var result *M
				var oldPK any
				var txErr error

				shouldCreate := forceCreate || req.GetID() == nil

				if shouldCreate {
					created, txErr := h.svc.Create(txCtx, []service.CrudRequest[M]{req})
					if txErr != nil {
						return txErr
					}
					result = created[0]
				} else {
					oldPK = req.GetID()
					result, txErr = h.svc.Update(txCtx, oldPK, req)
					if txErr != nil {
						return txErr
					}
				}

				newPK := extractPKFromResult(result)
				// 版本化向下传播：一旦本节点或父节点是版本化的，子节点必须按版本化处理
				passParentVersioned := isVersioned || parentVersioned

				// 级联委托子 Handler 的 DoUpdate
				if rawMaps != nil && i < len(rawMaps) {
					raw := rawMaps[i]
					for _, rel := range h.config.Cascades {
						if !rel.OnUpdate {
							continue
						}
						childHandler := h.handlerReg.Get(rel.HandlerName)
						if childHandler == nil {
							continue
						}

						childData := extractChildData(raw, rel.ChildrenField, rel.ChildrenWrapKey)
						_, hasChildren := raw[rel.ChildrenField]

						if !hasChildren {
							if oldPK == nil {
								continue // 新建记录无子数据 → 跳过后代
							}
							oldChildren, txErr := childHandler.DoList(txCtx, rel.FKField, oldPK, false)
							if txErr != nil {
								return errs.ErrCascadeUpdateBackfill(rel.HandlerName, txErr)
							}
							childData = oldChildren

							// 为 backfill 数据补充 id 键，确保 GetID() 能匹配到主键
							for j := range childData {
								if _, ok := childData[j]["id"]; !ok {
									for k, v := range childData[j] {
										if v != nil && v != "" && (strings.HasSuffix(strings.ToLower(k), "_ulid") || strings.HasSuffix(strings.ToLower(k), "_id")) {
											childData[j]["id"] = v
											break
										}
									}
								}
							}
						} else if !passParentVersioned && oldPK != nil {
							// 父非版本化且有请求子数据 → 先清理旧子记录（全量替换）
							if txErr = childHandler.DoDeleteByFK(txCtx, rel.FKField, []any{oldPK}); txErr != nil {
								return errs.ErrCascadeUpdateCleanup(rel.HandlerName, txErr)
							}
						}

						// 计算传递给子 Handler 的版本化标志
						passToChild := passParentVersioned
						// 非版本化全量替换：旧子记录已删，子数据应走 CREATE 而非 UPDATE（BUG-018 修复）
						if !passParentVersioned && hasChildren && oldPK != nil {
							passToChild = true
						}
						// 补充子数据时，现有子记录只需更新 FK，不强制创建
						if !hasChildren && oldPK != nil {
							passToChild = false
						}
						// 传递含 visited + depth 的 context，子 Handler 可感知级联链状态
						if txErr = childHandler.DoUpdate(cascadeCtx, rel.FKField, newPK, childData, passToChild); txErr != nil {
							return errs.ErrCascadeUpdate(rel.HandlerName, txErr)
						}
					}
				}
				results = append(results, result)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		return results, nil
	}

	// 无级联
	return h.updateOrCreate(ctx, reqs, forceCreate)
}

// updateOrCreate 逐条更新或创建（无级联），供 visited/depth 耗尽时回退使用。
func (h *GenericHandler[M]) updateOrCreate(ctx context.Context, reqs []service.CrudRequest[M], forceCreate bool) ([]*M, error) {
	if forceCreate {
		return h.svc.Create(ctx, reqs)
	}
	var results []*M
	for _, req := range reqs {
		id := req.GetID()
		if id == nil {
			created, err := h.svc.Create(ctx, []service.CrudRequest[M]{req})
			if err != nil {
				return nil, err
			}
			results = append(results, created[0])
		} else {
			r, err := h.svc.Update(ctx, id, req)
			if err != nil {
				return nil, err
			}
			results = append(results, r)
		}
	}
	return results, nil
}

func (h *GenericHandler[M]) _afterUpdate(ctx context.Context, results []*M, _ bool) ([]*M, error) {
	// GlobalStore：更新缓存
	for _, r := range results {
		h.cacheSet(ctx, r)
	}
	return results, nil
}

// -------- Delete --------

func (h *GenericHandler[M]) _beforeDelete(_ context.Context, ids, codes any) (any, any, error) {
	// 默认：透传
	return ids, codes, nil
}

func (h *GenericHandler[M]) _doDelete(ctx context.Context, ids, codes any) error {
	// 若配置了级联删除且已注入 TxCoordinator + HandlerRegistry，
	// 在事务内先删子记录再删父实体。
	if h.hasCascadeFlag(func(r CascadeRelation) bool { return r.OnDelete }) && h.txCoord != nil && h.handlerReg != nil {
		// visited + depth 防环/防无限深度：退化为简单删除（不级联子表）
		if isVisited(ctx, h.svcName, "batch") {
			return h.svc.Delete(ctx, ids, codes)
		}
		if d, ok := getDepth(ctx); ok && d <= 0 {
			return h.svc.Delete(ctx, ids, codes)
		}

		idList, ok := ids.([]any)
		if !ok || len(idList) == 0 {
			return h.svc.Delete(ctx, ids, codes)
		}

		return h.txCoord.Run(ctx, func(txCtx context.Context) error {
			// 加入 visited set，传递给子 Handler（防跨级联回路）
			cascadeCtx := addVisited(txCtx, h.svcName, "batch")
			if d, ok := getDepth(txCtx); ok {
				cascadeCtx = withDepth(cascadeCtx, d-1)
			} else {
				cascadeCtx = withDepth(cascadeCtx, hardMaxExpandDepth-1)
			}

			// 1. 按 FK 级联删除子记录（先子后父，避免 FK 约束冲突）
			if err := h.forEachCascade(
				func(r CascadeRelation) bool { return r.OnDelete },
				func(rel CascadeRelation, child CascadeHandler) error {
					return errs.ErrCascadeDelete(rel.HandlerName, child.DoDeleteByFK(cascadeCtx, rel.FKField, idList))
				},
			); err != nil {
				return err
			}
			// 2. 删除父实体
			return h.svc.Delete(txCtx, ids, codes)
		})
	}

	// 无级联：直接删除
	return h.svc.Delete(ctx, ids, codes)
}

func (h *GenericHandler[M]) _afterDelete(ctx context.Context) error {
	// GlobalStore：清理缓存（ids 从 ctx 获取）
	if store := h.config.GlobalStore; store != nil {
		if ids, ok := ctx.Value(deleteCacheIDsKey{}).([]any); ok {
			for _, id := range ids {
				store.Del(ctx, cacheKeyULID(fmt.Sprint(id)))
			}
		}
	}
	return nil
}
