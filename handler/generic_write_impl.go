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

			// 应用方 §27.3：本 Handler 记录自身也可作为装配目标（祖先作为发布方）。
			// 关系级 Target 只能发布子批次，顶层/祖先记录没有对应关系，故用
			// HandlerConfig.Target 声明。
			//
			// 登记的是**落库后的实体快照**（而不是请求 map）：请求里往往缺少
			// Match 需要的字段（update 尤其如此，如 code 通常不在请求体里），
			// 用请求 map 会让消费方的 Match 因缺字段而落空。目标记录只被读取
			// （Match 与 Assign 的源），故用快照不影响落库内容。
			h.registerRootTargetFromResults(preallocRegistryFrom(txCtx), created)

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

			// ---------- 嵌套调用：本批次已由入口完成阶段 1/2 ----------
			// 入口把「本批次的子树」随 ctx 下传（withBatchNode），嵌套 Handler
			// 据此跳过预分配/登记/装配 —— 重复登记会让同一记录在目标索引里
			// 出现两次（命中多条），重复装配则是无用功。
			if node := batchNodeFrom(ctx); node != nil {
				return h.persistCreateNodes(cascadeCtx, node.subs, refMap)
			}

			// ---------- 阶段 1：全树展开 + 预分配 + 登记（递归） ----------
			// 注意这里是**整棵树**：孙批次（如 form_write_section → form_write_field）
			// 必须在祖批次的阶段 2 之前完成预分配与登记，否则祖批次直接子批次上的
			// 消费方装配时目标索引为空（应用方 §25.2 指出的缺陷）。
			// 顶层父记录的权威 PK 来自 service 落库结果（未启用预分配时请求 map 里没有）
			parentPKs := make([]any, len(rawMaps))
			for i := range rawMaps {
				if i < len(results) {
					parentPKs[i] = extractPKFromResult(results[i])
				}
			}
			prep, perr := h.prepareCreateSubtree(cascadeCtx, rawMaps, parentPKs,
				func(r CascadeRelation) bool { return r.OnCreate })
			if perr != nil {
				return perr
			}
			// 注意：阶段 2/3 继续使用**未下钻**的 cascadeCtx。
			// prep.ctx 是递归下钻过程中逐层构造的 ctx（visited 与 depth 已按更深层级
			// 递减），拿它去落库会让子 Handler 把自己判成"已访问过"而短路级联。
			// 每一层落库时各自 buildCascadeCtx 才是正确口径（与既有实现一致）。

			// ---------- 阶段 2：全树统一装配（递归）+ 装配后编码（后序） ----------
			if reg := preallocRegistryFrom(cascadeCtx); reg != nil {
				owner := ""
				if len(rawMaps) > 0 {
					owner = assemblyOwnerID(rawMaps[0])
				}
				if _, aerr := assembleTree(cascadeCtx, reg, prep.nodes, owner, h.svcName); aerr != nil {
					return aerr
				}
				// §26.3：装配全部完成后才做「深到浅」编码 —— 子层先于父层
				// （外层若先被序列化成文本，内层随后的装配改动就进不去那段文本）。
				if aerr := encodeTree(cascadeCtx, prep.nodes); aerr != nil {
					return aerr
				}
				if aerr := h.runAfterCascadeAssemble(cascadeCtx, rawMaps); aerr != nil {
					return aerr
				}
			}

			// ---------- 阶段 3：全树落库（递归，顺序任意） ----------
			// 每个批次把「本批次子树」随 ctx 交给子 Handler（withBatchNode），
			// 因此嵌套层不会重新展开、也不会重新预分配（A3：同一批句柄）。
			return h.persistCreateNodes(cascadeCtx, prep.nodes, refMap)
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

		// 预分配通道（v3）：同 _doCreate —— 绕过 HTTP 管线直接调用时幂等补齐
		// 注册表（顶层 PK 不在此重分配：update 的主键来自请求的 id，
		// 由 service 的版本化逻辑决定是否派生新行）。
		ctx, _ = h.ensureAssemblyContext(ctx)

		var results []*M
		// 同 _doCreate：按显式声明的策略处理预分配主键冲突。
		err := h.txCoord.RunWithPKRetry(ctx, func(txCtx context.Context) error {
			cascadeCtx := h.buildCascadeCtx(txCtx)

			// ---------- 嵌套调用：本批次已由入口完成阶段 1/2 ----------
			// 入口把「本批次的子树」随 ctx 下传（withBatchNode），嵌套 Handler
			// 据此跳过回填/清 PK/预分配/登记/装配，只做本层记录的落库。
			if node := batchNodeFrom(ctx); node != nil {
				for _, req := range reqs {
					if forceCreate || req.GetID() == nil {
						created, txErr := h.svc.Create(txCtx, []service.CrudRequest[M]{req})
						if txErr != nil {
							return txErr
						}
						results = append(results, created[0])
						continue
					}
					r, txErr := h.svc.Update(txCtx, req.GetID(), req)
					if txErr != nil {
						return txErr
					}
					results = append(results, r)
				}
				return h.persistUpdateNodes(cascadeCtx, node.subs)
			}

			// ---------- 入口：逐条更新/创建父记录，并准备整棵子树 ----------
			// 记录集合 = raw map（句柄）+ 落库前后身份（回填查询 / FK 注入 / 目标登记）。
			var records []updateLevelRecord
			for i, req := range reqs {
				var result *M
				var oldPK any

				shouldCreate := forceCreate || req.GetID() == nil
				if shouldCreate {
					created, txErr := h.svc.Create(txCtx, []service.CrudRequest[M]{req})
					if txErr != nil {
						return txErr
					}
					result = created[0]
				} else {
					oldPK = req.GetID()
					updated, txErr := h.svc.Update(txCtx, oldPK, req)
					if txErr != nil {
						return txErr
					}
					result = updated
				}
				results = append(results, result)

				newPK := extractPKFromResult(result)
				var raw map[string]any
				if rawMaps != nil && i < len(rawMaps) {
					raw = rawMaps[i]
				}
				records = append(records, updateLevelRecord{raw: raw, oldPK: oldPK, newPK: newPK})
				// 应用方 §27.3：祖先记录自身作为 Target。用**落库后的实体快照**
				// （update 请求常常只有 id + 改动的字段，缺少 Match 需要的 code 等）。
				h.registerRootTargetFromResults(preallocRegistryFrom(txCtx),
					[]*M{result})
			}

			// ---------- 阶段 1：全树展开 + 回填 + 预分配 + 登记（递归） ----------
			// 与 _doCreate 同理：必须递归到孙批次及更深，否则祖批次的直接子批次
			// 在阶段 2 装配时看不到孙批次发布的目标（应用方 §25.2）。
			prep, perr := h.prepareUpdateSubtree(cascadeCtx, records, parentVersioned)
			if perr != nil {
				return perr
			}
			// 同 _doCreate：阶段 2/3 用未下钻的 cascadeCtx（prep.ctx 的 visited/depth
			// 已按更深层级递减，会让子 Handler 误判自己已访问而短路级联）。

			// ---------- 阶段 2：全树统一装配（递归）+ 装配后编码（后序） ----------
			if reg := preallocRegistryFrom(cascadeCtx); reg != nil {
				owner := ""
				for _, r := range records {
					if r.raw != nil {
						owner = assemblyOwnerID(r.raw)
						break
					}
				}
				if _, aerr := assembleTree(cascadeCtx, reg, prep.nodes, owner, h.svcName); aerr != nil {
					return aerr
				}
				// §26.3：装配全部完成后才「深到浅」编码（子层先于父层）
				if aerr := encodeTree(cascadeCtx, prep.nodes); aerr != nil {
					return aerr
				}
				for _, r := range records {
					if r.raw == nil {
						continue
					}
					if aerr := h.runAfterCascadeAssemble(cascadeCtx, []map[string]any{r.raw}); aerr != nil {
						return aerr
					}
				}
			}

			// ---------- 阶段 3：全树落库（递归，顺序任意） ----------
			return h.persistUpdateNodes(cascadeCtx, prep.nodes)
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
