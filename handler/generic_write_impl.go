package handler

import (
	"context"
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
	if h.hasCascadeFlag(func(r CascadeRelation) bool { return r.OnCreate }) && h.txCoord != nil && h.handlerReg != nil {
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
						childData[j][rel.FKField] = parentPK
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
				if _, txErr = childHandler.DoCreate(txCtx, allChildData); txErr != nil {
					return errs.ErrCascadeCreate(rel.HandlerName, txErr)
				}
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

func (h *GenericHandler[M]) _afterCreate(_ context.Context, result []*M) ([]*M, error) {
	// 默认：透传
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
		rawMaps, _ := ctx.Value(rawUpdateMapsKey{}).([]map[string]any)
		isVersioned := h.svc.IsVersionMode()

		var results []*M
		err := h.txCoord.Run(ctx, func(txCtx context.Context) error {
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

							// ä¸º backfill æ°æ®è¡¥å id é®ï¼ç¡®ä¿ GetID() è½å¹éå°ä¸»é®
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

						// è®¡ç®ä¼ éç»å­ Handler ççæ¬åæ å¿
						passToChild := passParentVersioned
						// è¡¥åå­æ°æ®æ¶ï¼ç°æå­è®°å½åªéæ´æ° FKï¼ä¸å¼ºå¶åå»º
						if !hasChildren && oldPK != nil {
							passToChild = false
						}
						if txErr = childHandler.DoUpdate(txCtx, rel.FKField, newPK, childData, passToChild); txErr != nil {
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
	if forceCreate {
		return h.svc.Create(ctx, reqs)
	}
	// 逐条更新 / 无 ID 时创建
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

func (h *GenericHandler[M]) _afterUpdate(_ context.Context, results []*M, _ bool) ([]*M, error) {
	// 默认：透传
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
		idList, ok := ids.([]any)
		if !ok || len(idList) == 0 {
			return h.svc.Delete(ctx, ids, codes)
		}

		return h.txCoord.Run(ctx, func(txCtx context.Context) error {
			// 1. 按 FK 级联删除子记录（先子后父，避免 FK 约束冲突）
			if err := h.forEachCascade(
				func(r CascadeRelation) bool { return r.OnDelete },
				func(rel CascadeRelation, child CascadeHandler) error {
					return errs.ErrCascadeDelete(rel.HandlerName, child.DoDeleteByFK(txCtx, rel.FKField, idList))
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

func (h *GenericHandler[M]) _afterDelete(_ context.Context) error {
	// 默认：空操作
	return nil
}
