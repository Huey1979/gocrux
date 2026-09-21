package handler

import (
	"context"
	"fmt"

	"github.com/Huey1979/gocrux/common"
	errs "github.com/Huey1979/gocrux/errors"
)

// ============================================================
// ULID 预分配（阶段 1）
//
// 设计文档 §5.1 / §11.2.2：
//
//	展开请求树的同时为每个实体分配 ULID —— **复用真实级联展开路径**
//	（工程前提 A3：不自己遍历请求树，否则两条遍历路径必然分叉，
//	 导致"分配给 A 的值用不到 A 上"，且偶发）。
//
// 因此本文件不实现「遍历」：分配发生在父 Handler 的 _doCreate/_doUpdate
// 收集到子数据之后（extractChildData 之后），这正是真实展开路径上的位置。
//
// 分配三件事：
//  1. 为本层实体（若 PK 为空）分配 ULID 并写回 childData；
//  2. 把 ULID 登记进请求级注册表（**凭证**：落库时据此判断"是自己生成的"）；
//  3. 把本批记录登记到 Target 索引（供其它分支装配时查找）。
// ============================================================

// preallocate 为本批数据预分配 ULID，并登记凭证与目标索引。
//
// 参数：
//
//	reg         请求级注册表（nil 时不做凭证登记，仅直接分配）
//	handlerName 本批所属 Handler 名（错误定位用）
//	pkField     主键列名（PKField()）
//	childData   本批子数据（**就地**写入 ULID）
//	target      本批作为目标提供方的标识（rel.Target；为空则不入 Target 索引）
//
// 返回新 ctx（携带凭证桥 + 注册表）。
//
// 注意：**已经带 ULID 的记录不重新分配**（幂等）—— 这保证了同一批数据被
// 多次调用（如 service 逐条 Update 形态）时 ULID 稳定不变。
func preallocate(
	ctx context.Context,
	reg *PreallocRegistry,
	handlerName string,
	pkField string,
	childData []map[string]any,
	target string,
) context.Context {
	if len(childData) == 0 {
		return ctx
	}
	// 凭证桥：service 落库时据此判断 PK 是否可信（PowerBy ctx 传递，
	// 避免 service → handler 的反向包依赖）。
	ctx = InstallTicketBridge(ctx)

	// 配置了预分配通道时，先给本批分配（未配置则保持既有"落库时生成"语义）
	assignMissing := reg != nil

	ticket := ticketFrom(ctx)
	issued := make([]string, 0, len(childData))
	for j := range childData {
		rec := childData[j]
		if rec == nil {
			continue
		}
		v, exists := readPKValue(rec, pkField)
		cur := scalarToString(v)
		if cur != "" {
			// 已有 PK：**一律不重新生成，也一律不登记为可信凭证**。
			//
			// 语义（设计文档 §3.2 / §9.2 #15）：注册表里的 ULID 必须全部是
			// 「框架自己生成的」—— 这才使「非空 PK 直接信任」成立。
			// 此处非空 PK 只可能来自两处：
			//   ① 本请求更早阶段（如 rebuildChildPKs）已分配 → 当时已登记，
			//      此处无需重复登记（幂等）；
			//   ② 请求体带来的外部值（前端伪造 / 未迁移调用方）→ **不登记**，
			//      落库时由 service 的凭证校验判为不可信并重新生成。
			// 若在这里登记，就把 ② 洗成了「自己生成的」，
			// 前端可借此固定任意主键（§9.2 #15 用例正是守护这一点）。
			continue
		}
		if !exists || !assignMissing {
			// PK key 不存在 / 未启用预分配 → 保持既有语义（由 service._beforeCreate 生成）
			// 例外：存在但为空 且 已启用预分配 → 正是要分配的场景（下方继续）。
		}
		if !assignMissing {
			continue
		}
		newULID := common.NewULID()
		writePKValue(rec, pkField, newULID)
		reg.Register(newULID)
		issued = append(issued, newULID)
	}

	// 凭证签发（自定义实现时会走其 Issue；默认实现已在上面 Register 完成）
	if len(issued) > 0 {
		_ = issueWithContext(ctx, ticket, issued)
	}

	// 登记为 Target 索引（供其它分支装配时查找）
	if target != "" {
		registerAssemblyTargets(reg, target, handlerName, pkField, childData)
	}
	return ctx
}

// preallocateRoot 为**顶层实体**预分配 ULID（请求入口）。
//
// 顶层实体的 ULID 由 service._beforeCreate 生成（而非 Handler 层构造），
// 因此这里只在「需要跨分支装配」时才介入：预分配必须在**任何子分支处理之前**
// 完成，否则消费方装配时看不到父记录。当前装配只针对子表之间的引用
// （Source 从子记录出发），父记录的 ULID 由落库时生成即可 —— 但为了
// 让「同一请求内父子 ULID 都已确定」（设计文档 §11.2.1 的落库顺序无关性），
// 仍在此处显式分配。
func preallocateRoot(
	ctx context.Context,
	reg *PreallocRegistry,
	pkField string,
	rawMaps []map[string]any,
) context.Context {
	if reg == nil || len(rawMaps) == 0 {
		return ctx
	}
	ctx = InstallTicketBridge(ctx)
	ticket := ticketFrom(ctx)
	issued := make([]string, 0, len(rawMaps))
	for _, raw := range rawMaps {
		if raw == nil {
			continue
		}
		v, _ := readPKValue(raw, pkField)
		if cur := scalarToString(v); cur != "" {
			// 顶层 PK 保持「请求未显式指定才分配」语义，并**登记为可信**：
			// 这是既有的向后兼容行为（调用方显式传主键创建时必须被沿用，
			// 否则所有未迁移的调用方会突然拿不到自己指定的主键）。
			//
			// 代价是顶层仍保留「显式传入即信任」，因此子表的防伪更严格
			// （见 preallocate：子批外部 PK 只落库不登记）。若将来要收紧顶层，
			// 应作为独立的 breaking change 处理。
			reg.Register(cur)
			continue
		}
		newULID := common.NewULID()
		writePKValue(raw, pkField, newULID)
		reg.Register(newULID)
		issued = append(issued, newULID)
	}
	if len(issued) > 0 {
		_ = issueWithContext(ctx, ticket, issued)
	}
	return ctx
}

// ============================================================
// 版本化重建：清旧 PK → 填预分配值（含身份映射，§5.2①）
// ============================================================

// rebuildChildPKs 在版本化重建时清除旧 PK 并填回预分配值。
//
// 现有语义（BUG-020/059/060）：清 PK 后由 service._beforeCreate 生成新 ULID。
// 新方案改为：清 PK 后**立即填回预分配值** —— 既保留"旧版本子行不变、
// 新版本复制重建"语义，又让新 ULID 来自预分配（因此引用可以提前装配）。
//
// ⚠ 身份映射要求（应用方 §13.4，设计文档 §5.2①）：
//
//	回填路径（update 未传子表 → DoList 拉回旧数据）下，childData 来自数据库，
//	其 PK 就是**旧记录的主键**。清 PK 前必须先把「旧子记录 ↔ 预分配项」的
//	对应关系固定下来，否则顺序/软删行变化会让旧 A 对应到新 B（静默错位）。
//
// 本函数用**旧 PK 本身**作为身份键：oldPKs 与 childData 一一对应（快照在清除前取），
// 因此"这条旧记录 → 这个新 ULID"的对应是确定的。若 childData 中存在**重复的旧 PK**
// （同一记录出现两次），说明数据本身有歧义 → 报错，绝不按位置猜测。
//
// 返回：写入的 ULID 列表（与 childData 同序，未分配的为空串）。
func rebuildChildPKs(
	reg *PreallocRegistry,
	handlerName string,
	pkField string,
	childData []map[string]any,
) ([]string, error) {
	if len(childData) == 0 {
		return nil, nil
	}

	// 1. 清除 PK **之前**留存旧主键快照（身份键）
	oldPKs := snapshotPKs(childData, pkField)

	// 2. 身份映射健全性检查：旧 PK 必须唯一（非空的那些）
	seen := make(map[string]int, len(oldPKs))
	for j, old := range oldPKs {
		if old == "" {
			continue
		}
		if prev, dup := seen[old]; dup {
			return nil, fmt.Errorf(
				"%w: Handler %s 的回填数据中主键 %s 出现两次（下标 %d 与 %d）；"+
					"无法确定「旧记录 ↔ 预分配项」的对应关系，拒绝按位置猜测"+
					"（请检查子表是否缺少主键或存在脏数据）",
				errs.ErrAssemblyIdentityAmbiguous, handlerName, old, prev, j)
		}
		seen[old] = j
	}

	// 3. 清旧 PK（与既有实现同口径：JSON 主键名 + gorm 列名都清）
	for j := range childData {
		delete(childData[j], pkField)
		delete(childData[j], "ulid")
		delete(childData[j], "id")
		delete(childData[j], "ID")
	}

	// 4. 填回预分配值（未启用预分配时留空，由 service._beforeCreate 生成）
	out := make([]string, len(childData))
	if reg == nil {
		return out, nil
	}
	for j := range childData {
		newULID := common.NewULID()
		writePKValue(childData[j], pkField, newULID)
		reg.Register(newULID)
		out[j] = newULID
	}
	return out, nil
}

// ============================================================
// 预分配通道的判定与挂载
// ============================================================

// usesAssembly 判断该配置是否启用了装配通道（任一关系配了 Assemblies）。
//
// 启用时：
//   - 请求入口挂载注册表 + ticket（凭证闭环）；
//   - 各批次的子数据在展开时预分配 ULID（而非落库时生成）。
func (h *GenericHandler[M]) usesAssembly() bool {
	for _, rel := range h.config.Cascades {
		if len(rel.Assemblies) > 0 {
			return true
		}
	}
	return false
}

// ensureAssemblyContext 在需要时挂载预分配上下文（幂等）。
//
// 顶层 Handler 入口调用（createPipeline / updatePipeline）。级联子调用
// 会看到同一份注册表（随 ctx 下传），因此**不需要**重复挂载。
//
// 返回的 entered 表示「**本次调用**创建了注册表」，即当前 Handler 是本次
// 请求的入口（顶层）。调用方据此决定是否要做顶层预分配 —— 这是必须区分的：
//
//	入口（entered=true）  ：顶层主键沿用「请求显式传入即信任」的既有语义
//	                        （否则未迁移的调用方会拿不到自己指定的主键），
//	                        故 preallocateRoot 会把它登记为可信；
//	级联子（entered=false）：本批主键的归属由**父级阶段 1** 决定
//	                        （preallocate / rebuildChildPKs），子级的
//	                        createPipeline 绝不能再把请求体里的值登记为可信 ——
//	                        否则前端伪造的子记录主键会被洗白（§3.2 / §9.2 #15）。
func (h *GenericHandler[M]) ensureAssemblyContext(ctx context.Context) (context.Context, bool) {
	if preallocRegistryFrom(ctx) != nil {
		// 父级已挂载 → 本次是级联子调用，不走顶层预分配。
		return ctx, false
	}
	if !h.usesAssembly() {
		return ctx, false
	}
	ctx, _ = EnsurePreallocRegistry(ctx)
	return ctx, true
}

// preallocEnabled 判断当前 ctx 是否已启用预分配通道。
func preallocEnabled(ctx context.Context) bool {
	return preallocRegistryFrom(ctx) != nil
}

// preallocateChildBatch 对本批子数据执行预分配并返回新 ctx（阶段 1 的统一入口）。
//
// rel 为本批对应的级联关系（提供发布标识 rel.Target，即 v2 RemapKey 的
// v3 对应物）。无论是否启用 Assemblies 都会 **登记 Target 索引**：某个批次
// 可能自己不做装配（只当发布方），但它的记录是别的分支的装配目标。
func preallocateChildBatch(
	ctx context.Context,
	rel CascadeRelation,
	handlerName string,
	pkField string,
	childData []map[string]any,
) context.Context {
	reg := preallocRegistryFrom(ctx)
	if reg == nil {
		return ctx
	}
	return preallocate(ctx, reg, handlerName, pkField, childData, rel.Target)
}

// ============================================================
// 落库凭证校验（service 侧经桥调用）
// ============================================================

// useV3Prealloc 判断该关系是否应走 v3 预分配通道。
//
// 判据：**未配置 v1/v2 重映射**（Remaps / RemapKey 都为空）。
//
// 注意 v3 的发布方声明在 `Target`（不是 RemapKey），因此
// 「发布方关系」同样返回 true —— 否则它的记录不会被预分配，
// 消费方在阶段 2 的索引里就找不到目标（这正是 Target 与 RemapKey
// 必须分开的原因：RemapKey 一旦非空即代表 v2 通道）。
//
// 为什么必须分流：v2 通道的语义是「落库时才生成新 ULID，再回头重写引用」，
// 它的映射表需要「旧 ULID → 新 ULID」的对照。若在展开阶段就填入预分配值，
// v2 的 prepareRemap 会把「新 ULID」当成旧的（childData 里的值已被改写），
// 映射失效（实测会让 v2 用例精确失败）。两套机制本就互斥（构造期校验），
// 故按关系分流是干净的隔离方式。
//
// 未配置任何装配声明的普通级联也会返回 true：那时 preallocate 只会登记
// Target 索引（供其它分支装配查找），并在启用注册表时给本批分配 ULID ——
// 后者是「落库顺序无关」的前提，且凭证桥保证 service 不会重新生成。
func useV3Prealloc(rel CascadeRelation) bool {
	return len(rel.Remaps) == 0 && rel.RemapKey == ""
}

// indexOfCascade 找出关系在 Cascades 数组中的下标（按 HandlerName + ChildrenField 定位）。
//
// 存在的意义：阶段 1 的批次是**按需收集**的（只含 OnCreate 且非空的关系），
// 而阶段 2 需要按关系取它的 Assemblies —— 故需要从关系回到下标。
// 找不到返回 -1（调用方会跳过装配，不报错：说明该关系不在配置里）。
func indexOfCascade(relations []CascadeRelation, target CascadeRelation) int {
	for i, rel := range relations {
		if rel.HandlerName == target.HandlerName && rel.ChildrenField == target.ChildrenField {
			return i
		}
	}
	return -1
}

// VerifyPreallocatedPK 校验一个非空 PK 是否可作为"框架生成"信任。
//
// 语义（设计文档 §3.2）：
//
//	PK 非空 且 存在于本请求 ctx 的预分配注册表 → 可信，直接落库（不重新生成）
//	否则 → 前端传来的，按原语义处理（重新生成 / 拒绝）
//
// 向后兼容：**未挂载注册表时返回 true** —— 那意味着调用方没走预分配通道
// （老代码、直接调 service），保持既有"非空即信任"语义，避免破坏未迁移的调用方。
func VerifyPreallocatedPK(ctx context.Context, ulid string) bool {
	return verifyWithContext(ctx, ticketFrom(ctx), ulid)
}

// markWrittenRecord 记录一条已落库记录（供无事务冲突兜底清理）。
func markWrittenRecord(ctx context.Context, entityType, ulid string) {
	if reg := preallocRegistryFrom(ctx); reg != nil {
		reg.MarkWritten(entityType, ulid)
	}
}
