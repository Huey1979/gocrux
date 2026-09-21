package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	errs "github.com/Huey1979/gocrux/errors"
)

// ============================================================
// 引用装配（Reference Assembly）
//
// 取代 v1/v2 的「事后重映射」：把 ULID 的生成时机提前到请求入口，
// 引用在装配阶段一次性写对，因此不再需要 catalog / 增量发布 / L1-L3 顺序校验。
//
// 三阶段（设计文档 §11.2.1，应用方 §16 最终口径）：
//
//	阶段 1：展开请求树 + 为全树预分配 ULID + 登记索引
//	阶段 2：基于完整索引一次性装配所有引用
//	阶段 3：各 Handler 正常落库（顺序任意）
//
// 关键：阶段 2 **不绑定**在「消费分支第一次被访问的时刻」——
// 那会造成「装配可见性依赖」（消费方执行时目标分支尚未登记）。
// ============================================================

// ReferenceAssembly 挂在 CascadeRelation.Assemblies 上：声明本关系所在批次
// 涉及的引用如何装配。
//
// 示例（heims 表单域）：
//
//	// list_column 标量引用 write_field
//	ReferenceAssembly{
//	    Source: "field_ulid",
//	    Target: "form.write_field",
//	    Match:  map[string]string{"field_code": "field_code"},
//	    Assign: map[string]string{"field_ulid": "field_ulid"},
//	}
type ReferenceAssembly struct {
	// Source 从**本批子数据**出发的路径，指向承载引用信息的容器/元素。
	//
	//	"field_ulid"       → 子记录顶层字段（标量场景）
	//	"error_on[]"       → error_on 数组的每个元素
	//	"options.target"   → 固定嵌套（不支持数组时不写 []）
	//	"$" 或 ""          → 当前子记录本身
	//
	// 约定：
	//   - "$" 可省略，路径默认从子记录根出发；
	//   - "[]" 表示"逐个数组元素"（当前只支持一层，见设计文档 §4.5）；
	//   - **判定依据是「路径/key 是否存在」，不是「叶值是否为空」**
	//     （应用方 §15 要求）：路径不存在（含父对象不存在）→ 视为"该引用不存在"跳过；
	//     路径存在、叶值为 "" / null → **继续执行 Match/Assign 回填**。
	//
	// 因此路径求值返回 (value, exists) 两个结果（见 resolveAssemblySource），
	// 不能复用只返回 value != nil 的读取函数。
	Source string

	// Target 提供 ULID 的来源：
	//   留空 = 本批次自身（批内自引用）
	//   非空 = 另一条级联分支的标识（跨批次），对应 v2 的 RemapKey 命名空间
	//
	// 命名沿用 v2 的 RemapKey 语义，便于 heims 平滑迁移。
	Target string

	// Match 匹配键（source 侧键 → target 侧键），**至少一个**。
	//
	//	{"field_code": "field_code"}
	//
	// 语义：在 target 批次的记录中，找到 Match **全部**命中（AND）的那一条。
	Match map[string]string

	// Assign 写入映射（source 侧键 → target 侧键）。
	//
	//	{"field_ulid": "field_ulid"}
	//
	// 支持多个键（如同时写 ulid + code）。
	Assign map[string]string
}

// sourcePath 归一 Source 路径（"$" 与 "" 都表示当前子记录本身）。
func (a ReferenceAssembly) sourcePath() string {
	s := strings.TrimSpace(a.Source)
	if s == "$" {
		return ""
	}
	return s
}

// hasArrayMarker 判断路径是否带数组标记（尾部 "[]"）。
func hasArrayMarker(path string) bool {
	return strings.HasSuffix(path, "[]")
}

// stripArrayMarker 去掉尾部数组标记。
func stripArrayMarker(path string) string {
	return strings.TrimSuffix(path, "[]")
}

// ============================================================
// 阶段 2：装配执行器
// ============================================================

// assemblyTarget 装配的目标记录集合（按 Target 解析而来）。
type assemblyTarget struct {
	// name Target 标识（错误文案用）。
	name string
	// records 候选记录。
	records []*assembledRecord
	// selfExclusive 批内自引用（Target 留空）：候选集合排除**正在装配的那条
	// 记录自身**。
	//
	// 为什么必须排除：批内目标集合就是本批记录本身，而被装配的记录同样在
	// 其中 —— 若它的 Match 键值恰与另一条记录相同（引用方与被引用方共用同一
	// 键空间），它就会命中「自己 + 真正的目标」两条，被判为
	// ErrAssemblyAmbiguous。而「记录引用自己」在业务上没有意义：
	// 引用字段要写入的正是自己的 ULID，无需装配。
	//
	// 跨批次（Target 非空）不做此排除：那时目标集合来自**别的**级联分支，
	// 消费方记录不在其中。
	selfExclusive bool
}

// AsmStats 一次装配的统计（供日志与测试断言）。
type AsmStats struct {
	// Applied 成功写入的引用数。
	Applied int
	// Skipped 因「路径不存在」跳过的引用数（code-only，合法）。
	Skipped int
	// Unmatched 匹配不到目标的引用数（WARN + 保留旧值，B5）。
	Unmatched int
	// Idempotent 已是本批次新 ULID 而幂等跳过的引用数。
	Idempotent int
}

// asmWarnf 装配阶段的 WARN 日志钩子。
//
// 抽成变量：便于测试捕获 WARN（设计文档 §6.1 #2 要求「必须能定位」——
// 日志要含 Source 路径 / 匹配键值 / Target 标识 / 所属父记录标识）。
var asmWarnf = func(format string, args ...any) {
	asmLogger(format, args...)
}

// assemble 执行阶段 2：对给定批次的全部记录应用声明式装配。
//
// 入参：
//
//	reg      请求级注册表（提供 Target 索引）
//	records  本批次的记录（Target 留空时作为目标来源）
//	asm      装配声明
//	ownerID  归属父记录标识（仅用于日志定位）
//
// 错误分级（设计文档 §6.1）：
//
//	路径不存在        → 静默跳过（code-only 引用）
//	匹配不到目标      → WARN + 保留旧值（B5；应用侧发布门禁负责 fail-closed）
//	匹配到多条目标    → **报错** ErrAssemblyAmbiguous（B6：code 唯一性是前提）
//	配置本身非法      → 由构造期校验拦截（见 validateAssemblies）
func assemble(
	reg *PreallocRegistry,
	records []*assembledRecord,
	asm ReferenceAssembly,
	ownerID string,
	sourceHandler string,
) (AsmStats, error) {
	var stats AsmStats
	if len(records) == 0 || len(asm.Match) == 0 || len(asm.Assign) == 0 {
		return stats, nil
	}

	// 目标集合：Target 留空 = 本批次自身
	var target assemblyTarget
	if asm.Target == "" {
		target = assemblyTarget{name: "(本批次)", records: records, selfExclusive: true}
	} else {
		if reg == nil {
			// 无注册表 → 说明调用方未走预分配通道，装配无从进行（配置了却不生效）。
			// 不静默：否则"配了 Assemblies 却没作用"会以「引用保持旧值」的形态
			// 悄悄溜过，正是 v2 反复踩过的坑。
			return stats, fmt.Errorf(
				"%w: 装配声明 Target=%q 需要请求级索引，但当前 context 中没有预分配注册表；"+
					"请确认级联请求经由 Handler 入口（当前 Handler=%s）",
				errs.ErrAssemblyInvalidConfig, asm.Target, sourceHandler)
		}
		target = assemblyTarget{name: asm.Target, records: reg.TargetRecords(asm.Target)}
	}

	path := asm.sourcePath()
	for _, rec := range records {
		if rec == nil || rec.data == nil {
			continue
		}
		if err := applyAssembly(rec, path, target, asm, ownerID, sourceHandler, &stats); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

// applyAssembly 对单条记录应用装配（可能展开数组元素）。
func applyAssembly(
	rec *assembledRecord,
	path string,
	target assemblyTarget,
	asm ReferenceAssembly,
	ownerID string,
	sourceHandler string,
	stats *AsmStats,
) error {
	// 数组形式：逐个元素独立判定
	if hasArrayMarker(path) {
		field := stripArrayMarker(path)
		v, exists := getByPath(rec.data, field)
		if !exists || v == nil {
			// 路径不存在（含父对象不存在）→ 该引用不存在，跳过
			stats.Skipped++
			return nil
		}
		items, ok := decodeArrayContainer(v)
		if !ok {
			// 存在但不是数组（数据形态异常）→ 无可装配元素，跳过
			stats.Skipped++
			return nil
		}
		changed := false
		out := make([]any, 0, len(items))
		for _, item := range items {
			el, ok := item.(map[string]any)
			if !ok {
				out = append(out, item) // 非对象元素原样保留
				continue
			}
			// el 是元素本身，不含 rec 的身份 —— 但批内自引用要排除的是
			// **记录**（rec），故此处传 rec。
			applied, err := assembleOne(el, target, asm, ownerID, sourceHandler, rec, stats)
			if err != nil {
				return err
			}
			if applied {
				changed = true
			}
			out = append(out, el)
		}
		if changed {
			writeContainer(rec, field, out)
		}
		return nil
	}

	// 标量 / 对象：路径不存在 → 跳过（code-only 情形，应用方 §15.1）
	//
	// 容器的确定（关键）：
	//   - Source 留空 / "$" → 当前子记录本身；
	//   - 路径求值为 map → **在对象内部**装配（Match 读该对象的键、Assign 写回该对象）；
	//   - 路径求值为标量 → 在记录顶层装配（Match 键与被引用字段都在顶层）。
	container := rec.data
	if path != "" {
		v, exists := getByPath(rec.data, path)
		if !exists {
			stats.Skipped++
			return nil
		}
		if m, ok := v.(map[string]any); ok {
			container = m
		}
	}
	_, err := assembleOne(container, target, asm, ownerID, sourceHandler, rec, stats)
	return err
}

// assembleOne 对一个容器（记录本身或数组元素）执行一次 Match/Assign。
//
// 返回 applied 表示是否发生了实际写入。
//
// 关键（应用方 §15）：存在性判定看的是**引用字段本身**（Assign 的目标键，
// 如 field_ulid）在容器里是否存在：
//
//	key 不存在            → code-only 引用 → 跳过，且**不得凭空创建**该字段；
//	key 存在（"" / null） → ULID-bearing 模式 → 继续 Match/Assign 回填新 ULID。
//
// 为什么不能看 Match 键（如 field_code）：code-only 引用同样带 field_code，
// 用它判定会把「只传 code」的引用偷偷升级成 ULID（§15.1 明确禁止）。
// 这也正是「新建字段立即引用」（{"field_code":"new_field","field_ulid":""}）
// 能被补上的原因 —— 该 key 存在，只是值为空。
//
// self 为「正在被装配的记录」（批内自引用时从候选里排除，见 assemblyTarget）。
func assembleOne(
	container map[string]any,
	target assemblyTarget,
	asm ReferenceAssembly,
	ownerID string,
	sourceHandler string,
	self *assembledRecord,
	stats *AsmStats,
) (bool, error) {
	// 引用字段（Assign 目标键）必须存在：至少一个 → ULID-bearing。
	refPresent := false
	for _, dstKey := range sortedKeys(asm.Assign) {
		if _, ok := getByPath(container, dstKey); ok {
			refPresent = true
			break
		}
	}
	if !refPresent {
		stats.Skipped++
		return false, nil
	}

	// 在目标集合里按 Match（AND 语义）查找
	skip := self
	if !target.selfExclusive {
		skip = nil
	}
	hits := matchRecords(container, target.records, asm.Match, skip)

	switch len(hits) {
	case 0:
		// B5：WARN + 保留旧值（应用侧发布门禁负责 fail-closed）
		stats.Unmatched++
		asmWarnf("gocrux: 装配匹配不到目标（保留旧值）source=%s target=%s match=%v owner=%s handler=%s",
			asm.Source, target.name, asm.Match, ownerID, sourceHandler)
		return false, nil
	case 1:
		// 命中唯一目标 → 按 Assign 写入
	default:
		// B6：命中多条 = 匹配键在目标里不唯一 = 前提被破坏，不可容忍
		return false, fmt.Errorf(
			"%w: 装配命中 %d 条目标（要求唯一）source=%s target=%s match=%v owner=%s handler=%s",
			errs.ErrAssemblyAmbiguous, len(hits), asm.Source, target.name,
			asm.Match, ownerID, sourceHandler)
	}

	hit := hits[0]
	changed := false
	// Assign 的键排序后遍历，保证行为确定
	for _, dstKey := range sortedKeys(asm.Assign) {
		srcKeyInTarget := asm.Assign[dstKey]
		val, ok := getByPath(hit.data, srcKeyInTarget)
		if !ok {
			// 目标记录里没有该字段 → 配置错（构造期已校验首段，此处兜底跳过）
			continue
		}
		old, hasOld := getByPath(container, dstKey)
		if hasOld && asmSameValue(old, val) {
			stats.Idempotent++
			continue // 已是本批次新 ULID → 幂等跳过
		}
		if err := setAssemblyValue(container, dstKey, val); err != nil {
			return false, err
		}
		changed = true
	}
	if changed {
		stats.Applied++
	}
	return changed, nil
}

// matchRecords 在候选记录中找出 Match 全部命中（AND）的那些。
//
// skip 非空时跳过该记录（批内自引用排除「正在装配的记录自身」，见
// assemblyTarget.selfExclusive）。
func matchRecords(
	container map[string]any,
	candidates []*assembledRecord,
	match map[string]string,
	skip *assembledRecord,
) []*assembledRecord {
	var out []*assembledRecord
	for _, cand := range candidates {
		if cand == nil || cand.data == nil {
			continue
		}
		if skip != nil && cand == skip {
			continue
		}
		if recordMatches(container, cand.data, match) {
			out = append(out, cand)
		}
	}
	return out
}

// recordMatches 判断容器的 Match 键值是否与目标记录一致（多键 AND）。
//
// 语义：每个 Match 键都要命中；容器侧键不存在 → 视为不匹配
// （调用方已保证至少存在一个匹配键）。
func recordMatches(container, candidate map[string]any, match map[string]string) bool {
	for _, srcKey := range match {
		tgtKey := match[srcKey]
		sv, ok := getByPath(container, srcKey)
		if !ok {
			return false
		}
		tv, ok := getByPath(candidate, tgtKey)
		if !ok {
			return false
		}
		if !asmSameValue(sv, tv) {
			return false
		}
	}
	return true
}

// asmSameValue 比较两个值在「装配匹配」语义下是否相等。
//
// 走 JSON 归一比较（与 service/filter_value.go 的取值归一精神一致）：
// 数字类型（int/int8/float64/json.Number）与字符串需能跨类型比较，
// 因为 map 形态的数据可能来自 JSON 反序列化（float64）或直接构造（int）。
func asmSameValue(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if as, ok := a.(string); ok {
		if bs, ok2 := b.(string); ok2 {
			return as == bs
		}
	}
	// 数值：先尝试按数值比较（1 与 1.0 视为相等）
	af, aok := asmToFloat(a)
	bf, bok := asmToFloat(b)
	if aok && bok {
		return af == bf
	}
	if aok != bok {
		return false
	}
	// 其余形态（bool / 容器）用 JSON 编码比较，保证稳定
	ab, aerr := json.Marshal(a)
	bb, berr := json.Marshal(b)
	if aerr != nil || berr != nil {
		return fmt.Sprint(a) == fmt.Sprint(b)
	}
	return string(ab) == string(bb)
}

// asmToFloat 尝试把标量转为 float（非数值返回 false）。
func asmToFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// setAssemblyValue 按 JSON 字段名写回容器（支持点号路径）。
func setAssemblyValue(container map[string]any, key string, val any) error {
	if container == nil || key == "" {
		return nil
	}
	if strings.Contains(key, ".") {
		setByPath(container, key, val)
		return nil
	}
	container[key] = val
	return nil
}

// ============================================================
// JSON 容器的形态归一（数组 / 对象）
// ============================================================

// decodeArrayContainer 把值归一为 []any。
//
// 兼容三种形态：
//   - 已是 []any（结构化）；
//   - JSON 字符串（type:json 列，如 `"[{...}]"`）；
//   - nil / 空 → 返回 ok=false。
func decodeArrayContainer(v any) ([]any, bool) {
	switch t := v.(type) {
	case []any:
		return t, true
	case string:
		trimmed := strings.TrimSpace(t)
		if trimmed == "" || trimmed == "null" || trimmed == "[]" {
			return nil, false
		}
		var out []any
		if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
			return nil, false
		}
		return out, true
	}
	// 其它切片类型（[]map[string]any 等）
	if arr := toAnySlice(v); len(arr) > 0 {
		return arr, true
	}
	return nil, false
}

// writeContainer 把归一后的数组按**原编码形态**写回。
//
// type:json 的 string 列在实体里是字符串，若写成 Go 数组会在落库时失败，
// 因此需与原形态保持一致（与 cascade_remap.go 的 reencode 同精神）。
func writeContainer(rec *assembledRecord, key string, arr []any) {
	if rec == nil || rec.data == nil {
		return
	}
	if _, exists := rec.data[key]; !exists {
		// 路径原本不存在 —— 装配不应凭空创建字段（保持 code-only 语义）
		return
	}
	orig, _ := getByPath(rec.data, key)
	if _, isStr := orig.(string); isStr {
		if b, err := json.Marshal(arr); err == nil {
			setAssemblyValue(rec.data, key, string(b))
			return
		}
	}
	setAssemblyValue(rec.data, key, arr)
}

// ============================================================
// 构造期配置校验（B7：fail-fast）
// ============================================================

// validateAssemblies 校验装配声明本身是否合法（纯配置错，与数据形态无关）。
//
// 区分（设计文档 §6.1 #4 的注）：
//   - 本函数检查的是「**配置里写的字段名**是否可用」：Match 为空、Assign 为空、
//     Source 路径为空但 Match 键缺失等 —— 纯配置错，构造期 fail-fast；
//   - 「数据里没有这个 key」是合法形态，运行时静默跳过，不在此处报错。
//
// siblings 为同一 Cascades 数组内的全部关系，用于校验 Target 是否指向
// 本数组内已知的发布标识（跨 Handler 子树的目标无法静态推断，跳过不报错）。
func validateAssemblies(rel CascadeRelation, siblings []CascadeRelation) error {
	for i, asm := range rel.Assemblies {
		if len(asm.Match) == 0 {
			return fmt.Errorf(
				"HandlerName=%q 的 Assemblies[%d] 缺少 Match（至少一个匹配键）",
				rel.HandlerName, i)
		}
		if len(asm.Assign) == 0 {
			return fmt.Errorf(
				"HandlerName=%q 的 Assemblies[%d] 缺少 Assign（至少一个写入映射）",
				rel.HandlerName, i)
		}
		for srcKey, tgtKey := range asm.Match {
			if strings.TrimSpace(srcKey) == "" || strings.TrimSpace(tgtKey) == "" {
				return fmt.Errorf(
					"HandlerName=%q 的 Assemblies[%d] 的 Match 存在空键（%q → %q）",
					rel.HandlerName, i, srcKey, tgtKey)
			}
		}
		for dstKey, srcKey := range asm.Assign {
			if strings.TrimSpace(dstKey) == "" || strings.TrimSpace(srcKey) == "" {
				return fmt.Errorf(
					"HandlerName=%q 的 Assemblies[%d] 的 Assign 存在空键（%q → %q）",
					rel.HandlerName, i, dstKey, srcKey)
			}
		}
		// 数组表达能力：只支持一层（设计文档 §4.5 / B11）
		if strings.Count(asm.Source, "[]") > 1 {
			return fmt.Errorf(
				"HandlerName=%q 的 Assemblies[%d] 的 Source=%q 含多层数组标记；"+
					"当前只支持一层（如 error_on[]），两层及以上请在业务侧拍平",
				rel.HandlerName, i, asm.Source)
		}
		if idx := strings.Index(asm.Source, "[]"); idx >= 0 && idx != len(asm.Source)-2 {
			return fmt.Errorf(
				"HandlerName=%q 的 Assemblies[%d] 的 Source=%q 的数组标记只能位于末尾",
				rel.HandlerName, i, asm.Source)
		}
		// Target 存在性：留空 = 批内自引用（合法）；非空时若本数组内有声明了
		// 同名 RemapKey 的关系 → 合法；本数组内找不到 → 可能在子 Handler 子树
		// （无法静态推断，按 L1 的做法留给启动期/运行时），不在此报错。
		_ = siblings
	}
	return nil
}

// AssemblyNotFoundHint 生成「Target 未声明」的提示（供运行时诊断）。
func AssemblyNotFoundHint(target string, reg *HandlerRegistry, extra []CascadeRelation) string {
	if target == "" {
		return ""
	}
	defs := CollectAssemblyTargets(reg, extra)
	if v, ok := defs[target]; ok {
		return fmt.Sprintf("Target=%q 已由 %s 声明", target, strings.Join(v, " / "))
	}
	return fmt.Sprintf("Target=%q 未在任何已注册 Handler 的 Cascades 中声明为 RemapKey", target)
}

// CollectAssemblyTargets 收集全部已声明的发布标识 → 声明方 Handler 名。
//
// 与 v2 的 L1（ValidateRemapKeys）同精神：发布标识声明在**父 Handler 的
// Cascades** 上（rel.Target），因此注册表 + 调用方自身共同构成「已知发布方」
// 集合。
func CollectAssemblyTargets(reg *HandlerRegistry, extra []CascadeRelation) map[string][]string {
	declared, _ := collectAssemblyTargets(reg, extra)
	return declared
}

// collectAssemblyTargets 返回（v3 已声明的发布标识, 仅由 v2 RemapKey 声明的标识）。
//
// 第二个返回值只用于诊断提示：迁移期最常见的错误是「发布方忘了把 RemapKey
// 改写成 Target」，此时标识确实存在于配置里，但走的是 v2 通道（落库时才生成
// ULID，不会进 v3 目标索引），消费方仍然装配不到 —— 必须让报错文案说清这一点，
// 否则使用者会盯着一个"明明声明过"的标识无从下手。
func collectAssemblyTargets(
	reg *HandlerRegistry,
	extra []CascadeRelation,
) (declared, v2Declared map[string][]string) {
	declared = make(map[string][]string)
	v2Declared = make(map[string][]string)
	add := func(handlerName string, relations []CascadeRelation) {
		for _, rel := range relations {
			// 发布标识只由 rel.Target 声明。
			//
			// **消费方的 Assemblies[].Target 不算声明** —— 那是「使用」而非
			// 「发布」；若也算作声明，每个消费方都为自己的 Target 背书，
			// L1 存在性校验将永远通过（等于没校验）。
			if rel.Target != "" {
				declared[rel.Target] = appendUnique(declared[rel.Target], handlerName)
			}
			if rel.RemapKey != "" {
				v2Declared[rel.RemapKey] = appendUnique(v2Declared[rel.RemapKey], handlerName)
			}
		}
	}
	if reg != nil {
		reg.Each(func(name string, ch CascadeHandler) {
			if d, ok := ch.(assemblyDescriber); ok {
				add(name, d.AssemblyRelations())
			}
		})
	}
	add("(本 Handler)", extra)
	return declared, v2Declared
}

// assemblyDescriber 能自述「本 Handler 的 Cascades 声明」的 Handler。
type assemblyDescriber interface {
	AssemblyRelations() []CascadeRelation
}

// AssemblyRelations 实现 assemblyDescriber。
func (h *GenericHandler[M]) AssemblyRelations() []CascadeRelation {
	return h.config.Cascades
}

// ValidateAssemblies L1 全局存在性校验：所有 ReferenceAssembly.Target 都必须
// 能找到声明方（即某处 Cascades[].RemapKey 与之一致）。
//
// 返回 error 而不 panic；**聚合报告**全部缺失项（与 v2 的 ValidateRemapKeys
// 同口径，heims §9.1 要求），避免「启动 → 补一个 → 再启动」的反复试错。
// 幂等，可重复调用。
func (h *GenericHandler[M]) ValidateAssemblies() error {
	if h.handlerReg == nil {
		return fmt.Errorf("[gocrux] ValidateAssemblies 需要 HandlerRegistry：" +
			"请在 SetHandlerReg 之后再调用（当前未注入）")
	}
	return ValidateAssembliesIn(h.handlerReg, h)
}

// assemblyNeeds 描述一个待校验的装配目标引用。
type assemblyNeeds struct {
	handlerName string
}

// ValidateAssembliesIn 对注册表 + 调用方自身做一次全局 L1 校验。
//
// extra 必须传入：父 Handler 往往**不在**注册表里（注册的是子 Handler），
// 而装配声明恰恰在父 Handler 的 Cascades 上。
//
// 判定基准：消费方 Assemblies[].Target 必须在**某处**被发布方以
// rel.Target 声明（v3 通道）。仅被 v2 的 RemapKey 声明不算通过 —— 那条通道
// 在落库时才生成 ULID，不建 v3 目标索引，消费方装配不到；此时报告会附带
// 迁移提示（把 RemapKey 改写成 Target）。
func ValidateAssembliesIn(reg *HandlerRegistry, extra assemblyDescriber) error {
	var extraRels []CascadeRelation
	if extra != nil {
		extraRels = extra.AssemblyRelations()
	}
	declared, v2Declared := collectAssemblyTargets(reg, extraRels)

	// 收集全部消费方（Target 非空）并检查是否有声明方
	missing := make(map[string][]string)
	var order []string
	collect := func(name string, rels []CascadeRelation) {
		for _, rel := range rels {
			for _, asm := range rel.Assemblies {
				if asm.Target == "" {
					continue // 批内自引用，无需声明方
				}
				if _, ok := declared[asm.Target]; ok {
					continue
				}
				if _, seen := missing[asm.Target]; !seen {
					order = append(order, asm.Target)
				}
				missing[asm.Target] = appendUnique(missing[asm.Target], name)
			}
		}
	}
	if reg != nil {
		reg.Each(func(name string, ch CascadeHandler) {
			if d, ok := ch.(assemblyDescriber); ok {
				collect(name, d.AssemblyRelations())
			}
		})
	}
	collect("(本 Handler)", extraRels)

	if len(order) == 0 {
		return nil
	}
	sort.Strings(order)

	var b strings.Builder
	fmt.Fprintf(&b, "[gocrux] 以下 ReferenceAssembly.Target 找不到声明方（共 %d 个）:", len(order))
	for _, t := range order {
		fmt.Fprintf(&b, "\n  - %q（消费方 Handler: %s）", t, strings.Join(missing[t], " / "))
		if v2 := v2Declared[t]; len(v2) > 0 {
			// 迁移期最常见的形态：发布方还在用 v2 的 RemapKey。
			fmt.Fprintf(&b, "；注意：%q 目前只由 v2 的 RemapKey 声明（%s），"+
				"该通道不建 v3 目标索引，请把发布方改写为 Target", t, strings.Join(v2, " / "))
		}
	}
	b.WriteString("\n请检查 Cascades 中是否漏配 Target（发布方），或 Target 拼写是否有误。")
	return fmt.Errorf("%s", b.String())
}

// ============================================================
// 装配的接入：把本批记录登记进目标索引
// ============================================================

// registerAssemblyTargets 把本批子数据登记为某个 Target 的候选记录。
//
// 在**预分配之后、装配之前**调用（阶段 1 的登记动作）。target 为 rel.RemapKey
// （v2 命名空间）—— 新方案沿用该标识，使 heims 的配置可以平滑迁移。
func registerAssemblyTargets(
	reg *PreallocRegistry,
	target string,
	handlerName string,
	pkField string,
	childData []map[string]any,
) {
	if reg == nil {
		return
	}
	for _, rec := range childData {
		if rec == nil {
			continue
		}
		reg.AddTarget(target, &assembledRecord{
			entityType: handlerName,
			data:       rec,
			pkField:    pkField,
		})
	}
}

// assemblyOwnerID 取归属父记录标识（仅用于日志定位）。
func assemblyOwnerID(raw map[string]any) string {
	if raw == nil {
		return ""
	}
	for _, k := range []string{"ulid", "id", "code"} {
		if v, ok := raw[k]; ok && v != nil {
			if s := scalarToString(v); s != "" {
				return s
			}
		}
	}
	return ""
}

// runAssemblies 对一个批次应用全部装配声明（阶段 2 的调用入口）。
//
// 返回统计与错误；错误会让事务失败（配置错 / 匹配多条）。
func runAssemblies(
	ctx context.Context,
	reg *PreallocRegistry,
	relations []CascadeRelation,
	relationIdx int,
	childData []map[string]any,
	ownerID string,
	sourceHandler string,
) (AsmStats, error) {
	var total AsmStats
	if reg == nil || relationIdx < 0 || relationIdx >= len(relations) {
		return total, nil
	}
	rel := relations[relationIdx]
	if len(rel.Assemblies) == 0 {
		return total, nil
	}
	records := make([]*assembledRecord, 0, len(childData))
	for _, rec := range childData {
		if rec == nil {
			continue
		}
		records = append(records, &assembledRecord{
			entityType: rel.HandlerName,
			data:       rec,
		})
	}
	for _, asm := range rel.Assemblies {
		st, err := assemble(reg, records, asm, ownerID, sourceHandler)
		total.Applied += st.Applied
		total.Skipped += st.Skipped
		total.Unmatched += st.Unmatched
		total.Idempotent += st.Idempotent
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
