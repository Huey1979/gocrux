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
// ============================================================

// shouldShortCircuitCascade 检查 visited + depth 是否阻止继续级联展开。
// 与 cascade.go 中 canExpandTo 语义一致：已访问或深度耗尽时返回 true。
func (h *GenericHandler[M]) shouldShortCircuitCascade(ctx context.Context) bool {
	if isVisited(ctx, h.svcName, "batch") {
		return true
	}
	if d, ok := getDepth(ctx); ok && d <= 0 {
		return true
	}
	return false
}

// buildCascadeCtx 构造级联子调用的 context：加入 visited set，深度递减。
// 首层默认为 hardMaxExpandDepth（10）层。
func (h *GenericHandler[M]) buildCascadeCtx(ctx context.Context) context.Context {
	cascadeCtx := addVisited(ctx, h.svcName, "batch")
	if d, ok := getDepth(ctx); ok {
		cascadeCtx = withDepth(cascadeCtx, d-1)
	} else {
		cascadeCtx = withDepth(cascadeCtx, hardMaxExpandDepth-1)
	}
	return cascadeCtx
}

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
		if h.shouldShortCircuitCascade(ctx) {
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

			cascadeCtx := h.buildCascadeCtx(txCtx)

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
		if h.shouldShortCircuitCascade(ctx) {
			return h.updateOrCreate(ctx, reqs, forceCreate)
		}

		rawMaps, _ := ctx.Value(rawUpdateMapsKey{}).([]map[string]any)
		isVersioned := h.svc.IsVersionMode()

		var results []*M
		err := h.txCoord.Run(ctx, func(txCtx context.Context) error {
			cascadeCtx := h.buildCascadeCtx(txCtx)

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
						// 当 passToChild=true 时（版本化 or 非版本化全量替换），
						// 子记录的旧 PK 必须清除，否则 CREATE 时会与旧记录冲突（BUG-020）
						if passToChild && hasChildren {
							for j := range childData {
								delete(childData[j], childHandler.PKField())
								delete(childData[j], "id")
							}
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

// -------- BatchUpdate（SQL IN 统一赋值） --------

func (h *GenericHandler[M]) _beforeBatchUpdate(_ context.Context, ids []any, updates map[string]any) ([]any, map[string]any, error) {
	return ids, updates, nil
}

func (h *GenericHandler[M]) _doBatchUpdate(ctx context.Context, ids []any, updates map[string]any) error {
	return h.svc.BatchUpdateByIDs(ctx, ids, updates)
}

func (h *GenericHandler[M]) _afterBatchUpdate(ctx context.Context, _ []any, _ map[string]any) error {
	// 清理缓存
	ids, _ := ctx.Value(deleteCacheIDsKey{}).([]any)
	if ids != nil {
		for _, id := range ids {
			h.cacheDelByID(ctx, id)
		}
	}
	return nil
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
		if h.shouldShortCircuitCascade(ctx) {
			return h.svc.Delete(ctx, ids, codes)
		}

		idList, ok := ids.([]any)
		if !ok || len(idList) == 0 {
			return h.svc.Delete(ctx, ids, codes)
		}

		return h.txCoord.Run(ctx, func(txCtx context.Context) error {
			cascadeCtx := h.buildCascadeCtx(txCtx)

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
