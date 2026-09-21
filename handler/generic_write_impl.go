package handler

import (
	"context"
	"fmt"
	"strings"

	"github.com/Huey1979/gocrux/common"
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

		// 预分配通道（v3）：直接调用 _doCreate（内部调用 / 测试，绕过 HTTP
		// 管线）时，注册表与顶层 ULID 尚未挂载 —— 此处幂等补齐。
		// 经 createPipeline 进入时是空操作（注册表已存在 → entered=false）。
		ctx, entered := h.ensureAssemblyContext(ctx)
		if entered && len(rawMaps) > 0 {
			ctx = preallocateRoot(ctx, preallocRegistryFrom(ctx), h.PKField(), rawMaps)
		}

		// 用 RunWithPKRetry 而非 Run：按 TxCoordinator 显式声明的策略
		// 处理预分配主键冲突（有事务 → 整树回滚重试；无事务 → 直接报错，
		// 由下方 cleanupAfterPKConflict 做标删兜底）。见 conflict.go。
		err := h.txCoord.RunWithPKRetry(ctx, func(txCtx context.Context) error {
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

			// ============================================================
			// 三阶段（设计文档 §11.2.1 / 应用方 §16）
			//
			//	阶段 1：收集全部子批次 + 预分配 ULID + 登记索引
			//	阶段 2：基于完整索引统一装配所有引用
			//	阶段 3：各 Handler 正常落库（顺序任意）
			//
			// **1 与 2 绝不能合并**：若「边展开边装配」，消费分支可能在目标分支
			// 登记前就执行装配 —— 那正是应用方 §16 指出的"实现阶段划分问题"
			// （会造成装配可见性依赖，从而又需要 v2 的顺序校验与 catalog）。
			// ============================================================

			// batch 一个待落库的子批次（阶段 1 的产物）。
			type createBatch struct {
				rel       CascadeRelation
				child     CascadeHandler
				childData []map[string]any
				tempRefs  map[string]tempRefEntry
			}
			var batches []createBatch

			// ---------- 阶段 1：收集 + 预分配 + 登记 ----------
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

				// 预分配本批 ULID 并登记 Target 索引（v3）。
				// 注意：预分配**不能**独立遍历请求树（工程前提 A3）—— 这里用的
				// 正是真实级联展开路径上的 childData，因此「分配给 A 的值必然
				// 用在 A 上」。未配置 Assemblies / 未挂载注册表时本调用是空操作。
				//
				// ⚠ 仅在**纯 v3 批次**（该关系未配 Remaps/RemapKey）时预分配：
				// v2 通道的语义是「落库时才生成新 ULID」（映射表需要「旧 → 新」的
				// 对照），若在此提前填入 ULID，v2 的 prepareRemap 会把「新 ULID」
				// 记成旧 PK 的值，导致映射失效（实测：v2 用例
				// TestRemapUpdateRewritesScalarRefToNewVersion 精确失败）。
				// 两套机制本就互斥（构造期校验），故按关系分流是正确的隔离方式。
				if useV3Prealloc(rel) {
					cascadeCtx = preallocateChildBatch(cascadeCtx, rel, rel.HandlerName,
						childHandler.PKField(), allChildData)
				}

				// 自引用 FK 代码解析（如 parent_menu_code → parent_item_ulid）
				// 在级联创建前，将子数据中的代码字段解析为实际的 ULID 外键值。
				// 预分配已填好 PK 时它以预分配值为准（resolveSelfFKCodeRefs 幂等）。
				if sfk := childHandler.SelfFKField(); sfk != "" {
					resolveSelfFKCodeRefs(allChildData, childHandler.PKField(), sfk)
				}

				// 跨实体引用：解析 allChildData 中的 __ref:handler:temp__ 占位符
				resolveCrossRefs(allChildData, refMap)
				// 收集本批的 _temp_ref 标记
				tempRefs := collectTempRefsOrdered(allChildData, rel.HandlerName)

				batches = append(batches, createBatch{
					rel: rel, child: childHandler,
					childData: allChildData, tempRefs: tempRefs,
				})
			}

			// ---------- 阶段 2：统一装配（基于完整索引） ----------
			// 所有批次的 ULID 与索引都已登记完毕，此刻消费方必然能看到目标。
			if reg := preallocRegistryFrom(txCtx); reg != nil {
				for bi := range batches {
					st, asmErr := runAssemblies(txCtx, reg, h.config.Cascades,
						indexOfCascade(h.config.Cascades, batches[bi].rel),
						batches[bi].childData,
						assemblyOwnerID(rawMaps[0]), h.svcName)
					if asmErr != nil {
						return asmErr
					}
					if st.Unmatched > 0 {
						// B5：匹配不到是 WARN（应用侧发布门禁负责 fail-closed），
						// 日志已在 assemble 内输出，此处仅留汇总便于排查。
						asmWarnf("gocrux: 引用装配存在未匹配项（Handler=%s 批次=%s unmatched=%d）",
							h.svcName, batches[bi].rel.HandlerName, st.Unmatched)
					}
				}
			}

			// ---------- 阶段 3：落库（顺序任意） ----------
			for bi := range batches {
				b := batches[bi]
				// 批次内横向引用重映射（v1/v2 通道）：与 Assemblies 互斥
				// （构造期已校验），故配置了 Assemblies 时不会进入此分支。
				childCtx := cascadeCtx
				if len(b.rel.Remaps) > 0 || b.rel.RemapKey != "" {
					childCtx = stageRemapWithKey(cascadeCtx, b.rel.HandlerName, b.rel.Remaps,
						b.childData, b.child.PKField(), b.rel.RemapKey,
						b.rel.PublishCodeField, resolveRemapPublishers(b.rel, h.config.Cascades))
				}

				// 传递含 visited + depth 的 context，子 Handler 可感知级联链状态
				pks, txErr := b.child.DoCreate(childCtx, b.childData)
				if txErr != nil {
					return errs.ErrCascadeCreate(b.rel.HandlerName, txErr)
				}
				// 登记已落库记录（无事务冲突兜底清理用，设计文档 §7.3）
				for _, pk := range pks {
					markWrittenRecord(txCtx, b.rel.HandlerName, scalarToString(pk))
				}
				// 将本批创建的实体 ULID 加入引用映射，供后续级联批次使用
				updateRefMap(refMap, b.tempRefs, pks)
			}
			return nil
		})
		if err != nil {
			// 主键冲突：有事务部署已在 runWithPKRetry 内整树回滚并重试；
			// 走到这里说明重试用尽或不支持重试（无事务部署）→ 走兜底清理。
			// 清理「尽力而为」：返回给调用方的错误不依赖它成功（§7.3）。
			if isPKConflictError(err) {
				return nil, h.cleanupAfterPKConflict(ctx, err)
			}
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

		// 预分配通道（v3）：同 _doCreate —— 绕过 HTTP 管线直接调用时幂等补齐
		// 注册表（顶层 PK 不在此重分配：update 的主键来自请求的 id，
		// 由 service 的版本化逻辑决定是否派生新行）。
		ctx, _ = h.ensureAssemblyContext(ctx)

		var results []*M
		// 同 _doCreate：按显式声明的策略处理预分配主键冲突。
		err := h.txCoord.RunWithPKRetry(ctx, func(txCtx context.Context) error {
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

				// 级联委托子 Handler 的 DoUpdate（三阶段，同 _doCreate）
				if rawMaps != nil && i < len(rawMaps) {
					raw := rawMaps[i]

					// updateBatch 一个待落库的子批次。
					type updateBatch struct {
						rel         CascadeRelation
						child       CascadeHandler
						childData   []map[string]any
						passToChild bool
						// oldPKs v2 通道所需的旧主键快照（在清 PK 之前留存）。
						// v3 通道不使用（装配基于预分配 ULID，不需要「旧 → 新」对照）。
						oldPKs []string
					}
					var ubatches []updateBatch

					// ---------- 阶段 1：收集 + 预分配 + 登记 ----------
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
						// v2 通道：本批是否走「落库时生成 ULID + 事后重映射」。
						// 与 v3 预分配互斥（构造期已校验），故按关系分流是干净的。
						isV2 := !useV3Prealloc(rel)
						var oldPKs []string

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
							// 使用 childHandler.PKField() 精确定位，避免后缀匹配误取 FK（BUG-035）。
							// childData 的 key 是 JSON 字段名（marshalToMap），PKField() 可能返回
							// gorm 列名（如 field_ulid）而非 JSON 名（ulid，gentity 约定），
							// 两者都查，避免 id 注入失败（BUG-060）。
							pkField := childHandler.PKField()
							for j := range childData {
								if _, ok := childData[j]["id"]; !ok {
									if pkVal, exists := childData[j][pkField]; exists && pkVal != nil && pkVal != "" {
										childData[j]["id"] = pkVal
									} else if pkVal, exists := childData[j]["ulid"]; exists && pkVal != nil && pkVal != "" {
										childData[j]["id"] = pkVal
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
						// 子记录的旧 PK 必须清除，否则 CREATE 时会与旧记录冲突（BUG-020）。
						// 版本化父表回填（未携带子表）同样进入：清除 PK 走 CREATE，
						// 为每个新版本复制重建子表快照，旧版本子行保持不变（BUG-059）。
						if passToChild && (hasChildren || passParentVersioned) {
							if isV2 {
								// v2 通道：**只清 PK，不填预分配值** —— v2 的语义是
								// 「落库时才生成新 ULID，再回头重写引用」，它的映射表需要
								// 「旧 ULID → 新 ULID」的对照，故新 ULID 必须留给 service 生成。
								// 旧 PK 快照在此留存（清之前），供 prepareRemap 构建映射。
								oldPKs = snapshotPKs(childData, childHandler.PKField())
								clearChildPKs(childData, childHandler.PKField())
							} else {
								// v3 通道：清 PK 后**立即填回预分配值**，这样引用可以在
								// 落库前装配（设计文档 §5.2①）。填回前先建立「旧子记录 ↔
								// 预分配项」的身份映射 —— 无法唯一对应则报错，
								// 绝不按位置静默猜测（应用方 §13.4）。
								if _, txErr := rebuildChildPKs(preallocRegistryFrom(txCtx),
									rel.HandlerName, childHandler.PKField(), childData); txErr != nil {
									return txErr
								}
							}
							// 自引用 FK 代码解析（如 parent_menu_code → parent_item_ulid）
							// PK 已清除/已填预分配值，在此解析代码字段，子 Handler 的
							// _beforeCreate → MergeTo 会保留这些值。
							if sfk := childHandler.SelfFKField(); sfk != "" {
								resolveSelfFKCodeRefs(childData, childHandler.PKField(), sfk)
							}
						}
						// 补充子数据时：
						// - 非版本化父表：现有子记录只需更新 FK，不强制创建（原地改 FK 语义保留，BUG-018/020）
						// - 版本化父表：passToChild 保持 true，回填数据已清除 PK，走 CREATE 复制重建（BUG-059）
						if !hasChildren && oldPK != nil && !passParentVersioned {
							passToChild = false
						}

						// 预分配本批 ULID 并登记 Target 索引（v3，仅纯 v3 批次，见 _doCreate 的说明）。
						// 注意顺序：必须在 rebuildChildPKs **之后** —— 重建路径已填好
						// 预分配值（幂等），此处只为「未清 PK 的批次」（如非版本化的
						// 原地更新分支）补分配，并统一完成 Target 索引登记。
						if !isV2 {
							cascadeCtx = preallocateChildBatch(cascadeCtx, rel, rel.HandlerName,
								childHandler.PKField(), childData)
						}

						ubatches = append(ubatches, updateBatch{
							rel: rel, child: childHandler,
							childData: childData, passToChild: passToChild,
							oldPKs: oldPKs,
						})
					}

					// ---------- 阶段 2：统一装配（基于完整索引） ----------
					if reg := preallocRegistryFrom(txCtx); reg != nil {
						for bi := range ubatches {
							st, asmErr := runAssemblies(txCtx, reg, h.config.Cascades,
								indexOfCascade(h.config.Cascades, ubatches[bi].rel),
								ubatches[bi].childData, assemblyOwnerID(raw), h.svcName)
							if asmErr != nil {
								return asmErr
							}
							if st.Unmatched > 0 {
								asmWarnf("gocrux: 引用装配存在未匹配项（Handler=%s 批次=%s unmatched=%d）",
									h.svcName, ubatches[bi].rel.HandlerName, st.Unmatched)
							}
						}
					}

					// ---------- 阶段 3：落库（顺序任意） ----------
					for bi := range ubatches {
						b := ubatches[bi]
						// 级联引用重映射（v1/v2 通道）：旧主键快照在阶段 1 清 PK 之前
						// 已留存（b.oldPKs），此处据此登记重映射任务；真正的重写由子
						// Handler 在落库前执行（service BeforeCreatePersist 钩子）。
						// 与 Assemblies 互斥（构造期已校验）。
						childCtx := cascadeCtx
						if len(b.rel.Remaps) > 0 || b.rel.RemapKey != "" {
							childCtx = stageRemapWithKeyOldPKs(cascadeCtx, b.rel.HandlerName, b.rel.Remaps,
								b.childData, b.oldPKs, b.child.PKField(), b.rel.RemapKey,
								b.rel.PublishCodeField, resolveRemapPublishers(b.rel, h.config.Cascades))
						}

						// 传递含 visited + depth（以及重映射登记项）的 context，
						// 子 Handler 可感知级联链状态并在落库前完成引用重写
						if txErr = b.child.DoUpdate(childCtx, b.rel.FKField, newPK, b.childData, b.passToChild); txErr != nil {
							return errs.ErrCascadeUpdate(b.rel.HandlerName, txErr)
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
			// 用户可能传了 codes 而非 ids，需要先解析 codes→IDs 才能级联
			if codesList, ok2 := codes.([]any); ok2 && len(codesList) > 0 {
				if resolved := h.svc.ResolveCodesToIDs(ctx, codesList); len(resolved) > 0 {
					idList = resolved
				}
			}
		}

		if len(idList) == 0 {
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

// ============================================================
// Restore — 恢复已软删记录（BUG-069）
//
// 与 Update 严格分离：Restore 只把软删字段置回「未删值」，
// 不承载任何业务字段修改；需要改已删记录时先 Restore 再 Update。
// ============================================================

func (h *GenericHandler[M]) _beforeRestore(_ context.Context, ids any) (any, error) {
	// 默认：透传
	return ids, nil
}

func (h *GenericHandler[M]) _doRestore(ctx context.Context, ids any) error {
	idList, ok := ids.([]any)
	if !ok || len(idList) == 0 {
		return errs.ErrMissingParam("ids")
	}
	return h.svc.Restore(ctx, idList)
}

func (h *GenericHandler[M]) _afterRestore(_ context.Context, _ any) error {
	// 默认：空操作
	return nil
}

// resolveSelfFKCodeRefs 解析同一批次子数据中的自引用 FK 代码字段。
//
// 背景：某些实体（如 SysMenuItem）通过 parent_menu_code（代码）表达
// 自引用层级关系，但 DB 实际存储 parent_item_ulid（ULID）。
// 在级联创建/更新中，同一批次的所有子项 ULID 可能尚未生成（或被清除后重新生成），
// 因此需要在此处完成三步操作：
//  1. 为每个子项生成新的主键 ULID
//  2. 构建业务代码（如 menu_code）→ 新 ULID 的映射
//  3. 将代码字段（如 parent_menu_code）解析为 ULID 填入 FK 字段（如 parent_item_ulid）
//
// 检测约定：若子数据中存在形如 "parent_xxx_code" 的虚拟字段，
// 且去除 "parent_" 前缀后的字段（如 "menu_code"）也在子数据中作为业务代码存在，
// 则认为该批数据需要进行自引用 FK 解析。
func resolveSelfFKCodeRefs(childData []map[string]any, pkField, selfFKField string) {
	// 检测自引用 FK 代码模式：查找 parent_xxx_code 格式的虚拟字段
	// 约定：selfFKCodeField = "parent_" + baseCodeField
	//       其中 baseCodeField 是业务代码字段（如 "menu_code"）
	var baseCodeField string
	var selfFKCodeField string

outer:
	for _, item := range childData {
		for key := range item {
			if strings.HasPrefix(key, "parent_") && strings.HasSuffix(key, "_code") {
				baseField := strings.TrimPrefix(key, "parent_")
				// 验证 baseField 确实存在于子数据中（作为业务代码字段）
				for _, item2 := range childData {
					if _, ok := item2[baseField]; ok {
						baseCodeField = baseField
						selfFKCodeField = key
						break outer
					}
				}
			}
		}
	}
	if baseCodeField == "" {
		return // 无自引用 FK 代码模式，无需解析
	}

	// Step 1: 为没有 PK 的子项生成新 ULID
	// childData 的 key 是 JSON 字段名（marshalToMap），PKField() 可能返回 gorm 列名
	// （如 field_ulid）而 JSON 主键名是 "ulid"（gentity 约定，BUG-060）。
	// 读写统一走 JSON 名（写时两个 key 都写，MergeTo 只认 JSON 名）。
	for j := range childData {
		if v, ok := readPKValue(childData[j], pkField); !ok || v == nil || v == "" {
			writePKValue(childData[j], pkField, common.NewULID())
		}
	}

	// Step 2: 构建业务代码 → 新 ULID 映射
	codeToULID := make(map[string]string, len(childData))
	for j := range childData {
		if code, ok := childData[j][baseCodeField].(string); ok && code != "" {
			if pkV, _ := readPKValue(childData[j], pkField); pkV != nil {
				if ulid, ok := pkV.(string); ok && ulid != "" {
					codeToULID[code] = ulid
				}
			}
		}
	}

	// Step 3: 解析自引用 FK 代码 → ULID
	for j := range childData {
		if parentCode, ok := childData[j][selfFKCodeField].(string); ok && parentCode != "" {
			if parentULID, exists := codeToULID[parentCode]; exists {
				childData[j][selfFKField] = parentULID
			}
			// 移除虚拟字段，避免传递给子 Handler 时产生未知字段错误
			delete(childData[j], selfFKCodeField)
		}
	}
}

// clearChildPKs 清除子记录的主键（v2 通道用：新 ULID 交给 service 生成）。
//
// 两种 key 都删 —— "ulid" 是 gentity 统一的 JSON 主键名约定（BUG-060 根因：
// 只删 gorm 列名导致 JSON 名 ulid 残留 → MergeTo 灌入旧 PK →
// _beforeCreate 判定非空复用旧 ULID → MySQL 1062）。
// 不做 *_ulid 后缀匹配（不误删 FK 字段，如 form_ulid / parent_item_ulid）。
func clearChildPKs(childData []map[string]any, pkField string) {
	for j := range childData {
		delete(childData[j], pkField)
		delete(childData[j], "ulid")
		delete(childData[j], "id")
		delete(childData[j], "ID")
	}
}

// readPKValue 从 JSON map 中读取主键值。
// childData 的 key 是 JSON 字段名（marshalToMap），而 PKField() 可能返回 gorm 列名
// （如 field_ulid）或 JSON 名（如 ulid，BUG-035 实体）。两种 key 都尝试（BUG-060）。
func readPKValue(m map[string]any, pkField string) (any, bool) {
	if v, ok := m[pkField]; ok {
		return v, true
	}
	if pkField != "ulid" {
		if v, ok := m["ulid"]; ok {
			return v, true
		}
	}
	return nil, false
}

// writePKValue 向 JSON map 写入主键值。
// 同时写 pkField 与 JSON 名 "ulid"：MergeTo 通过 json 反序列化灌入实体，
// 只认 JSON tag 名（gentity 统一 "ulid"），未匹配的 key（如 gorm 列名）会被忽略，写入无害。
func writePKValue(m map[string]any, pkField, v string) {
	m[pkField] = v
	m["ulid"] = v
}
