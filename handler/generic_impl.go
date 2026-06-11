package handler

import (
	"context"
	"encoding/json"
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
	if h.hasCascadesOnCreate() && h.txCoord != nil && h.handlerReg != nil {
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
	if h.hasCascadesOnUpdate() && h.txCoord != nil && h.handlerReg != nil {
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
	if h.hasCascadesOnDelete() && h.txCoord != nil && h.handlerReg != nil {
		idList, ok := ids.([]any)
		if !ok || len(idList) == 0 {
			return h.svc.Delete(ctx, ids, codes)
		}

		return h.txCoord.Run(ctx, func(txCtx context.Context) error {
			// 1. 按 FK 级联删除子记录（先子后父，避免 FK 约束冲突）
			for _, rel := range h.config.Cascades {
				if !rel.OnDelete {
					continue
				}
				childHandler := h.handlerReg.Get(rel.HandlerName)
				if childHandler == nil {
					continue
				}
				if err := childHandler.DoDeleteByFK(txCtx, rel.FKField, idList); err != nil {
					return errs.ErrCascadeDelete(rel.HandlerName, err)
				}
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

// -------- Get (统一处理 getByID / getByCode / getByIDToPublished) --------

func (h *GenericHandler[M]) _beforeGet(_ context.Context, req *GetRequest) (*GetRequest, error) {
	// 默认：透传
	return req, nil
}

func (h *GenericHandler[M]) _doGet(ctx context.Context, req *GetRequest) (map[string]any, error) {
	var result *M
	var err error

	switch {
	case req.Code != "":
		result, err = h.svc.GetByCode(ctx, req.Code)
	default:
		result, err = h.svc.Get(ctx, req.ID)
	}
	if err != nil {
		return nil, err
	}

	// FollowPublished：按 ID 取出的记录可能是非 published 版本，
	// 需要解析为同 code 族的正式发布版本。
	if req.FollowPublished {
		result, err = h.svc.ResolveOneToPublished(ctx, result)
		if err != nil {
			return nil, err
		}
	}

	// 将原始实体写入 context holder，供 HTTP 出口处 ResponseMapper 使用。
	// holder 仅由顶层 HTTP Get() handler 创建，级联调用（DoGetByID）不经过此路径。
	if holder, ok := ctx.Value(entityGetCtxKey{}).(**M); ok && holder != nil {
		*holder = result
	}

	// 展开：*M → map + References + Cascades + ChildRefs
	return h.expandGet(ctx, result)
}

func (h *GenericHandler[M]) _afterGet(_ context.Context, result map[string]any) (map[string]any, error) {
	// 默认：透传
	return result, nil
}

// -------- List --------

func (h *GenericHandler[M]) _beforeList(_ context.Context, query any) (any, error) {
	// 默认：透传
	return query, nil
}

func (h *GenericHandler[M]) _doList(ctx context.Context, query any, followPublished bool) ([]map[string]any, int64, error) {
	list, total, err := h.svc.List(ctx, query)
	if err != nil {
		return nil, 0, err
	}

	// FollowPublished：级联场景将 FK 指向的记录解析为正式发布版本。
	if followPublished && len(list) > 0 {
		resolved, err := h.svc.ResolveToPublished(ctx, list)
		if err != nil {
			return nil, 0, errs.ErrParsePublishedVersion(err)
		}
		list = resolved
	}

	// 将原始实体切片写入 context holder，供 HTTP 出口处 ResponseMapper 使用。
	// holder 仅由顶层 HTTP List() handler 创建，级联调用（DoList）不创建 holder。
	if holder, ok := ctx.Value(entityListCtxKey{}).(*[]*M); ok && holder != nil && len(list) > 0 {
		entities := make([]*M, len(list))
		for i := range list {
			entities[i] = &list[i]
		}
		*holder = entities
	}

	// *M → []map[string]any
	result := make([]map[string]any, len(list))
	for i, r := range list {
		data, err := json.Marshal(r)
		if err != nil {
			return nil, 0, errs.ErrMarshalEntity(err)
		}
		if err := json.Unmarshal(data, &result[i]); err != nil {
			return nil, 0, errs.ErrUnmarshalEntity(err)
		}
	}

		// 深度检查 + visited 防护：防止循环展开
		curDepth, hasDepth := getDepth(ctx)
		if isVisited(ctx, h.svcName, "batch") {
			return result, total, nil
		}
		childCtx := addVisited(ctx, h.svcName, "batch")
		if hasDepth {
			childCtx = withDepth(childCtx, curDepth-1)
		}

	// 批量展开 References（向上引用）
	if (!hasDepth || curDepth > 0) && len(h.config.References) > 0 && h.handlerReg != nil {
		for _, ref := range h.config.References {
			resultKey := ref.ResultField
			if resultKey == "" {
				resultKey = deriveRefResultKey(ref.Field)
			}
			// 忽略控制：ignoreAll / ignoreRef / ignore=resultKey
			if shouldIgnoreRef(childCtx) || shouldIgnoreField(childCtx, resultKey) {
				continue
			}
			// 字段级截止：检查父 Handler 注入的 fieldLimitMap
			_, ok := effectiveExpandDepth(ctx, hasDepth, resultKey)
			if !ok {
				continue
			}

			refHandler := h.handlerReg.Get(ref.HandlerName)
			if refHandler == nil {
				continue
			}

			// 收集所有 FK 值（去重）
			fkSet := make(map[string]bool)
			for _, m := range result {
				fkVal, ok := m[ref.Field]
				if !ok || fkVal == nil {
					continue
				}
				s := fmt.Sprint(fkVal)
				if s != "" {
					fkSet[s] = true
				}
			}
			if len(fkSet) == 0 {
				continue
			}

			fkList := make([]any, 0, len(fkSet))
			for k := range fkSet {
				fkList = append(fkList, k)
			}

			// 构造字段级子 context
			refCtx := h.buildFieldCtx(childCtx, ref.Field, ref.HandlerName)

			// 批量查（DoList + slice → OpIn）
			pkField := refHandler.PKField()
			// visited 防自引用：如果引用的目标已在此展开链中，跳过
			if isVisited(childCtx, ref.HandlerName, "batch") {
				continue
			}
			parentRecords, err := refHandler.DoList(refCtx, pkField, fkList, false)
			if err != nil {
				return nil, 0, errs.ErrRefBatchResolve(ref.HandlerName, err)
			}

			// 按 PK 建索引
			parentMap := make(map[string]map[string]any)
			for _, pr := range parentRecords {
				if idVal, ok := pr[pkField]; ok {
					parentMap[fmt.Sprint(idVal)] = pr
				}
			}

			for _, m := range result {
				if parent, ok := parentMap[fmt.Sprint(m[ref.Field])]; ok {
					m[resultKey] = parent
				}
			}
		}
	}

	// 批量展开 ChildRefs（向下引用 FK 列表）
	if (!hasDepth || curDepth > 0) && len(h.config.ChildRefs) > 0 && h.handlerReg != nil {
		for _, cr := range h.config.ChildRefs {
			resultKey := cr.ResultField
			if resultKey == "" {
				resultKey = deriveChildRefResultKey(cr.FKListField)
			}
			// 忽略控制：ignoreAll / ignoreRef / ignore=resultKey
			if shouldIgnoreRef(childCtx) || shouldIgnoreField(childCtx, resultKey) {
				continue
			}
			// 字段级截止
			_, ok := effectiveExpandDepth(ctx, hasDepth, resultKey)
			if !ok {
				continue
			}

			refHandler := h.handlerReg.Get(cr.HandlerName)
			if refHandler == nil {
				continue
			}

			// 收集所有 FK 值（去重）
			fkSet := make(map[string]bool)
			for _, m := range result {
				ids := toAnySlice(m[cr.FKListField])
				for _, id := range ids {
					s := fmt.Sprint(id)
					if s != "" {
						fkSet[s] = true
					}
				}
			}
			if len(fkSet) == 0 {
				continue
			}

			fkList := make([]any, 0, len(fkSet))
			for k := range fkSet {
				fkList = append(fkList, k)
			}

			// 构造字段级子 context
			refCtx := h.buildFieldCtx(childCtx, cr.FKListField, cr.HandlerName)

			pkField := refHandler.PKField()
			childRecords, err := refHandler.DoList(refCtx, pkField, fkList, false)
			if err != nil {
				return nil, 0, errs.ErrChildRefBatchResolve(cr.HandlerName, err)
			}

			childMap := make(map[string]map[string]any)
			for _, child := range childRecords {
				if idVal, ok := child[pkField]; ok {
					childMap[fmt.Sprint(idVal)] = child
				}
			}

			for _, m := range result {
				ids := toAnySlice(m[cr.FKListField])
				resolved := make([]map[string]any, 0, len(ids))
				for _, id := range ids {
					if child, ok := childMap[fmt.Sprint(id)]; ok {
						resolved = append(resolved, child)
					}
				}
				if len(resolved) > 0 {
					m[resultKey] = resolved
				}
			}
		}
	}

	// 批量展开 Cascades（向下级联子记录）
	if (!hasDepth || curDepth > 0) && len(h.config.Cascades) > 0 && h.handlerReg != nil {
		for _, rel := range h.config.Cascades {
			// 忽略控制：ignoreAll / ignoreCascade / ignore=ChildrenField
			if shouldIgnoreCascade(childCtx) || shouldIgnoreField(childCtx, rel.ChildrenField) {
				continue
			}
			// 字段级截止
			_, ok := effectiveExpandDepth(ctx, hasDepth, rel.ChildrenField)
			if !ok {
				continue
			}

			childHandler := h.handlerReg.Get(rel.HandlerName)
			if childHandler == nil {
				continue
			}

			// 收集所有父实体 PK
			pkSet := make(map[string]bool)
			for _, m := range result {
				pk := extractMapID(m)
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

			// 构造字段级子 context
			cascCtx := h.buildFieldCtx(childCtx, rel.ChildrenField, rel.HandlerName)

			// 批量查子记录（DoList + slice → OpIn）
			// visited 防自引用
			if isVisited(childCtx, rel.HandlerName, "batch") {
				continue
			}
			children, err := childHandler.DoList(cascCtx, rel.FKField, pkList, rel.FollowPublished)
			if err != nil {
				return nil, 0, errs.ErrCascadeBatchQuery(rel.HandlerName, err)
			}

			// 按 FKField 分组
			groups := make(map[string][]map[string]any)
			for _, child := range children {
				fkVal := fmt.Sprint(child[rel.FKField])
				groups[fkVal] = append(groups[fkVal], child)
			}

			// 回填到每条父结果
			for _, m := range result {
				pk := extractMapID(m)
				if pk != nil {
					if group, ok := groups[fmt.Sprint(pk)]; ok {
						m[rel.ChildrenField] = group
					}
				}
			}
		}
	}

	// List 字段裁剪：skip 优先于 keep，均未配置时全字段返回。
	// 仅作用于 _doList（Get 接口不受影响），在所有展开逻辑之后执行。
	if len(h.config.ListSkipFields) > 0 {
		skipSet := make(map[string]bool, len(h.config.ListSkipFields))
		for _, f := range h.config.ListSkipFields {
			skipSet[f] = true
		}
		for _, row := range result {
			for f := range skipSet {
				delete(row, f)
			}
		}
	} else if len(h.config.ListKeepFields) > 0 {
		keepSet := make(map[string]bool, len(h.config.ListKeepFields))
		for _, f := range h.config.ListKeepFields {
			keepSet[f] = true
		}
		for _, row := range result {
			for k := range row {
				if !keepSet[k] {
					delete(row, k)
				}
			}
		}
	}

	return result, total, nil
}

func (h *GenericHandler[M]) _afterList(_ context.Context, list []map[string]any, total int64) ([]map[string]any, int64, error) {
	// 默认：透传
	return list, total, nil
}

// -------- Activate --------

func (h *GenericHandler[M]) _beforeActivate(_ context.Context, id any) (any, error) {
	// 默认：透传
	return id, nil
}

func (h *GenericHandler[M]) _doActivate(ctx context.Context, id any) error {
	// 1. Self: activate（仅版本化模式有意义，非版本化直接跳过）
	if h.svc.IsVersionMode() {
		if err := h.svc.Activate(ctx, id); err != nil {
			return err
		}
	}

	// 2. Cascade: 级联激活子记录
	//    父激活 → 查子记录（DoList）→ 逐个调用子 Handler 的 DoActivate
	//    非版本化 Handler 自身空操作，但级联继续向下穿透
	if h.hasCascadesOnActivate() && h.txCoord != nil && h.handlerReg != nil {
		return h.txCoord.Run(ctx, func(txCtx context.Context) error {
			for _, rel := range h.config.Cascades {
				if !rel.OnActivate {
					continue
				}
				childHandler := h.handlerReg.Get(rel.HandlerName)
				if childHandler == nil {
					continue
				}

				children, err := childHandler.DoList(txCtx, rel.FKField, id, false)
				if err != nil {
					return errs.ErrCascadeActivateQuery(rel.HandlerName, err)
				}
				for _, child := range children {
					childPK := extractMapID(child)
					if childPK == nil {
						continue
					}
					if err := childHandler.DoActivate(txCtx, childPK); err != nil {
						return errs.ErrCascadeActivate(rel.HandlerName, err)
					}
				}
			}
			return nil
		})
	}
	return nil
}

func (h *GenericHandler[M]) _afterActivate(_ context.Context) error {
	// 默认：空操作
	return nil
}

// -------- ListVersions --------

func (h *GenericHandler[M]) _beforeListVersions(_ context.Context, id any, code string) (any, string, error) {
	// 默认：透传
	return id, code, nil
}

func (h *GenericHandler[M]) _doListVersions(ctx context.Context, id any, code string) ([]M, error) {
	return h.svc.ListVersions(ctx, id, code)
}

func (h *GenericHandler[M]) _afterListVersions(_ context.Context, result []M) ([]M, error) {
	// 默认：透传
	return result, nil
}

// -------- EditVersion --------

func (h *GenericHandler[M]) _beforeEditVersion(_ context.Context, id any, patches map[string]any) (any, map[string]any, error) {
	// 默认：透传
	return id, patches, nil
}

func (h *GenericHandler[M]) _doEditVersion(ctx context.Context, id any, patches map[string]any) (*M, error) {
	// 1. Self: edit version（仅版本化模式有意义）
	var result *M
	var err error
	if h.svc.IsVersionMode() {
		result, err = h.svc.EditVersion(ctx, id, patches)
		if err != nil {
			return nil, err
		}
	}

	// 2. Cascade: 级联编辑子记录的版本元数据
	if h.hasCascadesOnEditVersion() && h.txCoord != nil && h.handlerReg != nil {
		err = h.txCoord.Run(ctx, func(txCtx context.Context) error {
			for _, rel := range h.config.Cascades {
				if !rel.OnEditVersion {
					continue
				}
				childHandler := h.handlerReg.Get(rel.HandlerName)
				if childHandler == nil {
					continue
				}

				children, err := childHandler.DoList(txCtx, rel.FKField, id, false)
				if err != nil {
					return errs.ErrCascadeEditVerQuery(rel.HandlerName, err)
				}
				for _, child := range children {
					childPK := extractMapID(child)
					if childPK == nil {
						continue
					}
					// DoEditVersion 的返回值被忽略：级联场景下父不关心子返回什么
					if _, err := childHandler.DoEditVersion(txCtx, childPK, patches); err != nil {
						return errs.ErrCascadeEditVer(rel.HandlerName, err)
					}
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	return result, nil
}

func (h *GenericHandler[M]) _afterEditVersion(_ context.Context, result *M) (*M, error) {
	// 默认：透传
	return result, nil
}
