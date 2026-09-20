package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	errs "github.com/Huey1979/gocrux/errors"
)

// ============================================================
// 版本化级联引用重映射（cascade reference remap）
//
// 背景：版本化实体更新时，gocrux 会为父实体建新版本，并把子表记录**复制重建**为
// 新 ULID（清除旧 PK → 生成新 ULID → 落库）。但子记录之间往往还存在横向引用，例如：
//
//	field_access.field_ulid        → write_field.field_ulid
//	list_column.field_ulid         → write_field.field_ulid
//	validation.error_on[].field_ulid → write_field.field_ulid
//	flow_branch.target_node_ulid   → flow_node.node_ulid
//
// 这些引用在版本重建后必须指向**本版本新生成的子记录**。框架此前只处理：
//  1. 清除子记录旧主键；2. 生成新子记录 ULID；3. 自动设置直接父级 FK；
//  4. 特定的 parent_xxx_code → parent_xxx_ulid 自引用模式（resolveSelfFKCodeRefs）。
//
// 它不会重映射「任意子记录之间的 ULID 引用」——结果新版本里的引用仍指向旧版本子记录，
// 或形成跨版本悬挂引用：发布看起来成功，运行时却关联到旧字段/旧节点/旧分支。
//
// 本文件提供声明式、通用的重映射能力，在**新 ULID 全部生成后、子记录落库前**执行：
//
//	旧 ULID → 新 ULID（权威，优先）
//	code    → 新 ULID（兜底，仅当旧 ULID 匹配不上）
//
// 与 code 的关系（heims 口径）：版本化配置引用以 ULID 为权威，code 只作查看/导入导出/
// 迁移辅助；两者同时存在且指向不一致时报错，不静默取其一。
//
// 无法解析的引用一律**让事务失败**（ErrRemapUnresolved），不静默保留旧 ULID。
// ============================================================

// 引用形态（ReferenceBinding.Mode）。
const (
	// RemapModeScalar 标量 ULID 字段：{"node_ulid": "01..."}
	RemapModeScalar = "scalar"
	// RemapModeULIDArray ULID 数组字段：{"node_ulids": ["01...", "02..."]}
	RemapModeULIDArray = "ulid_array"
	// RemapModeObjectArray 对象数组字段：{"error_on": [{"field_ulid": "01...", "field_code": "amount"}]}
	RemapModeObjectArray = "object_array"
)

// ReferenceBinding 描述「当前子数据上的哪个字段引用了目标子实体」。
type ReferenceBinding struct {
	// Field 被重写的字段名（JSON 字段名，如 "field_ulid" / "error_on"）。
	//
	// 支持点号路径定位到嵌套 JSON 内部（如 "target.field_ulid"），
	// 因为引用可能落在 JSON 列内部：
	//
	//	{"target": {"type": "form", "field_ulid": "02..."}}
	//
	// 路径上的中间层若不存在则跳过（不报错）——嵌套 JSON 里没有该引用属正常形态。
	Field string

	// Mode 引用形态：scalar / ulid_array / object_array（默认 scalar）。
	Mode string

	// ULIDKey object_array 模式下，数组元素内存放 ULID 的键（如 "field_ulid"）。
	// 支持点号路径（如 "ref.field_ulid"）。
	ULIDKey string

	// CodeKey object_array 模式下，数组元素内存放业务 code 的键（如 "field_code"，可选）。
	// 配置后：ULID 命中优先；ULID 未命中时用 code 兜底；二者同时存在且指向
	// 不同目标时**报错**（不静默取其一）。留空表示该形态不参与 code 兜底。
	CodeKey string

	// CodeField 标量模式下与 Field **同级**的 code 字段（如 Field="field_ulid"、
	// CodeField="field_code"，可选）。
	//
	// 解决的是「消费方数据里根本没有旧 ULID」这一跨批次场景：
	//
	//	{"field_code": "amount"}   ← 只有 code，没有 field_ulid
	//
	// 解析顺序：先按 Field 取旧 ULID → 命中用 oldToNew；未命中/字段缺失则用
	// CodeField 取 code → 用 codeToNew 兜底；**解析结果一律写回 Field**，
	// 因此「字段原本缺失、仅凭 code 命中」也必须补写（不是仅在 ULID 存在时校验）。
	//
	// 二者同时存在且指向不同目标时**报错**（与 object_array 的 ULIDKey/CodeKey 同口径）。
	CodeField string
}

// ReferenceRemap 声明「一批子数据内部的引用关系如何在版本重建后重映射」。
//
// 挂在 CascadeRelation.Remaps 上：重映射的作用域是**该级联关系的这一批子数据**
// （一次 DoCreate/DoUpdate 的全部子记录），映射表也在此批次内构建。
//
// 示例（表单：field_access 引用 write_field）：
//
//	CascadeRelation{
//	    HandlerName:   "form_write_field",
//	    ChildrenField: "write_fields",
//	    FKField:       "form_ulid",
//	    OnCreate:      true,
//	    Remaps: []ReferenceRemap{{
//	        Bindings: []ReferenceBinding{
//	            {Field: "field_access", Mode: "object_array", ULIDKey: "field_ulid", CodeKey: "field_code"},
//	        },
//	    }},
//	}
//
// 说明：TargetHandler / ULIDField / CodeField 三个字段（REQ 建议 API 中的）在
// **批内自引用**这一主流场景下是冗余的 —— 被引用目标就是本批次自身的子记录，
// 映射表由批次直接构建。REQ 需求边界中的「标量 / ULID 数组 / 对象数组 / 嵌套 JSON」
// 四种形态与「ULID 优先、code 兜底、不一致报错」口径均已覆盖。
type ReferenceRemap struct {
	// SourceCodeField 本批子记录中作为**业务 code** 的字段名（如 "field_code"）。
	//
	// 用于构建 code → 新 ULID 兜底映射。留空时不提供 code 兜底。
	SourceCodeField string

	// SourceULIDField 本批子记录中作为**主键 ULID** 的 JSON 字段名（默认 "ulid"）。
	// 仅在需要写入 JSON 键时使用；通常无需配置。
	SourceULIDField string

	// SourceRemapKey 跨级联批次消费键（v2，可选）。
	//
	// 留空 = 沿用 v1 行为：消费**本批次自身**构建的映射（映射表由本批 childData
	// 与旧 PK 快照直接构建，见 prepareRemap）。
	//
	// 非空 = 消费当前**级联事务内**由该 RemapKey 命名空间发布出来的映射
	// （发布方声明见 CascadeRelation.RemapKey）。适用场景：引用目标位于另一个
	// 级联分支，本批 childData 里根本没有目标记录，无从构建映射。
	//
	// 留空是**向后兼容的关键**：v1 的接入方零改动。
	SourceRemapKey string

	// Bindings 引用字段清单。
	Bindings []ReferenceBinding
}

// RemapPlan 一次级联批次的引用重映射计划（内部结构）。
//
// 由 prepareRemap 在「新 ULID 已全部生成、子记录尚未落库」的时点构建，
// 随后由 applyRemap 对子数据原地重写。
type RemapPlan struct {
	// oldToNew 旧 ULID → 新 ULID（权威映射）。
	oldToNew map[string]string
	// codeToNew 业务 code → 新 ULID（兜底映射）。
	codeToNew map[string]string
	// bindings 该批次生效的绑定清单。
	bindings []ReferenceBinding
	// handlerName 目标子 Handler 名（用于错误信息）。
	handlerName string
	// sourceKey 映射来源命名空间（v2 跨批次消费时非空；批内模式为空）。
	// 仅用于错误文案，让「跨批次映射里找不到该值」与「批内映射里找不到」可区分。
	sourceKey string
	// childData 本批次子数据（供业务侧 ReferenceRemapper 计算自定义映射）。
	childData []map[string]any
}

// remapULIDField 返回批次子记录的主键 JSON 字段名（默认 "ulid"）。
func remapULIDField(r ReferenceRemap) string {
	if r.SourceULIDField != "" {
		return r.SourceULIDField
	}
	return "ulid"
}

// prepareRemap 构建重映射计划。
//
// 必须在**清除旧主键 / 生成新 ULID 之后、子记录落库之前**调用：
//
//	清除旧子记录主键 → 新 ULID 全部生成 → 构建映射 → 重写引用 → 落库
//
// oldPKs 与 childData 一一对应（调用方在清除 PK 前留存旧主键快照），
// 这样即使 PK 字段已被删除也能建立「旧 → 新」映射。
func prepareRemap(
	handlerName string,
	remaps []ReferenceRemap,
	childData []map[string]any,
	oldPKs []string,
) *RemapPlan {
	if len(remaps) == 0 || len(childData) == 0 {
		return nil
	}
	plan := &RemapPlan{
		oldToNew:    make(map[string]string, len(childData)),
		codeToNew:   make(map[string]string, len(childData)),
		handlerName: handlerName,
		childData:   childData,
	}
	for _, r := range remaps {
		plan.bindings = append(plan.bindings, r.Bindings...)

		ulidField := remapULIDField(r)
		for j := range childData {
			// 新 ULID：可能是 pkField 或 JSON 名 "ulid"，两者都查（BUG-060 约定）
			newULID := ""
			if v, ok := readPKValue(childData[j], ulidField); ok {
				newULID = fmt.Sprint(v)
			}
			if newULID == "" || newULID == "<nil>" {
				continue
			}
			// 旧 ULID（调用方在清除 PK 前留存）
			if j < len(oldPKs) && oldPKs[j] != "" {
				plan.oldToNew[oldPKs[j]] = newULID
			}
			// 业务 code → 新 ULID（兜底）
			if r.SourceCodeField != "" {
				if code, ok := childData[j][r.SourceCodeField].(string); ok && code != "" {
					plan.codeToNew[code] = newULID
				}
			}
		}
	}
	if len(plan.bindings) == 0 {
		return nil
	}
	return plan
}

// applyRemap 按计划原地重写本批次子数据中的所有引用字段。
//
// 无法解析的引用返回 errs.ErrRemapUnresolved（让事务失败），
// 绝不静默保留旧 ULID —— 否则会落库一个「看起来发布成功、运行时指向旧记录」的版本。
func applyRemap(plan *RemapPlan, childData []map[string]any) error {
	if plan == nil {
		return nil
	}
	for j := range childData {
		for _, b := range plan.bindings {
			if err := plan.applyBinding(childData[j], b); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyBinding 对单条子记录应用一个绑定。
func (p *RemapPlan) applyBinding(rec map[string]any, b ReferenceBinding) error {
	// 点号路径：嵌套 JSON 内部
	if strings.Contains(b.Field, ".") {
		return p.applyNestedBinding(rec, b)
	}
	v, ok := rec[b.Field]
	if !ok || v == nil {
		// 引用字段本身缺失：v1 直接跳过（视作「该记录没有此引用」）。
		// v2 例外：标量 + CodeField 已配置时，仅凭 code 也要能补写 Field
		// （heims §9.5-1：{"field_code":"amount"} 必须解析出 field_ulid）。
		if modeOf(b) == RemapModeScalar && b.CodeField != "" {
			return p.applyValue(rec, b.Field, nil, b)
		}
		return nil
	}
	return p.applyValue(rec, b.Field, v, b)
}

// applyValue 按形态重写一个值，并把结果按**原编码形态**写回。
//
// JSON 列的双形态问题：`type:json` 的 string 列在实体里是字符串，
// 但 DoList → marshalToMap 后仍可能是字符串（"[]" / "{...}"）或已是结构化值
// （取决于实体字段类型与 GORM 映射）。因此解析前先归一为结构化，重写后
// 再按原形态编码回去 —— 否则会把 JSON 字符串字段写成 Go 数组，落库失败。
func (p *RemapPlan) applyValue(rec map[string]any, field string, v any, b ReferenceBinding) error {
	// 形态先校验：未知 mode 直接报配置错误，不静默忽略（安全网）
	switch modeOf(b) {
	case RemapModeScalar, RemapModeULIDArray, RemapModeObjectArray:
	default:
		return fmt.Errorf("%w: 未知的引用形态 mode=%q（field=%s）",
			errs.ErrRemapInvalidConfig, b.Mode, b.Field)
	}

	origIsString := false
	if s, ok := v.(string); ok {
		origIsString = true
		decoded, ok2 := decodeJSONContainer(s)
		if !ok2 {
			// 非 JSON 容器的字符串：只有 scalar 形态才有意义
			if modeOf(b) != RemapModeScalar {
				return nil
			}
			nv, err := p.resolveScalar(rec, v, b)
			if err != nil {
				return err
			}
			if nv != nil {
				rec[field] = nv
			}
			return nil
		}
		v = decoded
	}

	switch modeOf(b) {
	case RemapModeScalar:
		nv, err := p.resolveScalar(rec, v, b)
		if err != nil {
			return err
		}
		if nv != nil {
			rec[field] = reencode(nv, origIsString)
		}
	case RemapModeULIDArray:
		arr, err := p.resolveULIDArray(v, b)
		if err != nil {
			return err
		}
		if arr != nil {
			rec[field] = reencode(arr, origIsString)
		}
	case RemapModeObjectArray:
		arr, err := p.resolveObjectArray(v, b)
		if err != nil {
			return err
		}
		if arr != nil {
			rec[field] = reencode(arr, origIsString)
		}
	default:
		return fmt.Errorf("%w: 未知的引用形态 mode=%q（field=%s）",
			errs.ErrRemapInvalidConfig, b.Mode, b.Field)
	}
	return nil
}

// modeOf 归一形态名（空 = scalar）。
func modeOf(b ReferenceBinding) string {
	m := strings.ToLower(strings.TrimSpace(b.Mode))
	if m == "" {
		return RemapModeScalar
	}
	return m
}

// decodeJSONContainer 尝试把 JSON 字符串解码为容器（数组/对象）。
// 非容器（标量、非 JSON）返回 ok=false。
func decodeJSONContainer(s string) (any, bool) {
	t := strings.TrimSpace(s)
	if t == "" || t == "null" || t == "{}" || t == "[]" {
		return nil, false
	}
	if !strings.HasPrefix(t, "[") && !strings.HasPrefix(t, "{") {
		return nil, false
	}
	var out any
	if err := json.Unmarshal([]byte(t), &out); err != nil {
		return nil, false
	}
	switch out.(type) {
	case []any, map[string]any:
		return out, true
	}
	return nil, false
}

// reencode 按原编码形态写回：原本是 JSON 字符串的列仍写回字符串。
func reencode(v any, asString bool) any {
	if !asString {
		return v
	}
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	return string(b)
}

// applyNestedBinding 处理带点号路径的绑定（引用位于嵌套 JSON 内部）。
//
// 路径上的中间层不存在时跳过（嵌套 JSON 里没有该引用属正常形态）；
// 中间层存在但类型不符（非 map / 非数组）时同样跳过，不误报。
func (p *RemapPlan) applyNestedBinding(rec map[string]any, b ReferenceBinding) error {
	// 拆出容器路径与叶字段：a.b.field_ulid → container="a.b", leaf="field_ulid"
	idx := strings.LastIndex(b.Field, ".")
	containerPath, leaf := b.Field[:idx], b.Field[idx+1:]
	leafBinding := b
	leafBinding.Field = leaf
	// CodeField 也可能写成完整路径（与 Field 同级书写，如 "a.b.field_code"）：
	// 进入容器后执行的是**叶层**绑定，键名必须是相对叶层的，因此把容器前缀剥掉。
	// 若 CodeField 本就是相对名（如 "field_code"），这里保持原样。
	if b.CodeField != "" {
		prefix := containerPath + "."
		if strings.HasPrefix(b.CodeField, prefix) {
			leafBinding.CodeField = b.CodeField[len(prefix):]
		} else if strings.Contains(b.CodeField, ".") {
			// 与容器不同源的点号路径无法在叶层使用 —— 显式报配置错误，不静默失效
			return fmt.Errorf(
				"%w: 嵌套绑定 %q 的 CodeField=%q 必须以容器路径 %q 为前缀（或写成相对名）",
				errs.ErrRemapInvalidConfig, b.Field, b.CodeField, containerPath)
		}
	}

	// 首层容器可能本身是 JSON 字符串（type:json 列）→ 先解码再下钻
	root := firstSegment(containerPath)
	raw, ok := rec[root]
	if !ok || raw == nil {
		return nil
	}
	rootIsString := false
	if s, isStr := raw.(string); isStr {
		if decoded, ok2 := decodeJSONContainer(s); ok2 {
			rootIsString = true
			rec[root] = decoded
			raw = decoded
		} else {
			return nil // 非 JSON 容器（或空），无嵌套引用可重写
		}
	}

	rest := root
	if root != containerPath {
		rest = containerPath[len(root)+1:]
	} else {
		rest = ""
	}

	container := raw
	if rest != "" {
		c, ok2 := getByPath(map[string]any{root: raw}, containerPath)
		if !ok2 || c == nil {
			return nil
		}
		container = c
	}

	if err := p.walkNestedContainer(container, leafBinding); err != nil {
		return err
	}
	// 若首层原本是字符串，把改写后的结构编码回字符串
	if rootIsString {
		rec[root] = reencode(rec[root], true)
	}
	return nil
}

// walkNestedContainer 在容器（map / 数组）内逐元素应用叶绑定。
func (p *RemapPlan) walkNestedContainer(container any, leafBinding ReferenceBinding) error {
	switch c := container.(type) {
	case map[string]any:
		// 叶字段可能就在这一层
		if v, ok := c[leafBinding.Field]; ok && v != nil {
			if err := p.applyValue(c, leafBinding.Field, v, leafBinding); err != nil {
				return err
			}
			return nil
		}
		// 叶 ULID 字段缺失但配了同级 CodeField → 仅凭 code 也要补写
		// （heims §9.5-1 的嵌套形态：{"target":{"field_code":"amount"}}）
		if leafBinding.CodeField != "" {
			if _, hasCode := c[leafBinding.CodeField]; hasCode {
				if err := p.applyValue(c, leafBinding.Field, nil, leafBinding); err != nil {
					return err
				}
				return nil
			}
		}
		// 否则继续下钻（多层嵌套 JSON）
		for _, v := range c {
			if err := p.walkNestedContainer(v, leafBinding); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range c {
			if err := p.walkNestedContainer(item, leafBinding); err != nil {
				return err
			}
		}
	}
	return nil
}

// firstSegment 取点号路径的首段（"a.b.c" → "a"）。
func firstSegment(path string) string {
	if i := strings.Index(path, "."); i >= 0 {
		return path[:i]
	}
	return path
}

// resolveScalar 解析标量 ULID 引用（含 code 兜底与不一致检测）。
// 返回 nil 表示无需修改。
//
// 与 v1 的差别（v2 标量 CodeField）：v1 只读 `Field`，因此 `{"field_code":"amount"}`
// 这种「没有旧 ULID」的数据会直接被跳过，引用永远补不上。v2 允许配置
// `CodeField` 指向同级的 code 字段：旧 ULID 缺失时用 code 兜底，
// **并把解析出的新 ULID 补写进 `Field`**。
func (p *RemapPlan) resolveScalar(rec map[string]any, v any, b ReferenceBinding) (any, error) {
	oldULID := scalarToString(v)

	byULID := ""
	if oldULID != "" && !p.isNewULID(oldULID) {
		byULID = p.oldToNew[oldULID]
	}

	// code 兜底：数据里可能没有旧 ULID（字段缺失），也可能旧 ULID 未命中本批次映射
	oldCode := ""
	byCode := ""
	if b.CodeField != "" {
		if c, ok := p.readRecKey(rec, b.CodeField); ok {
			oldCode = c
			byCode = p.codeToNew[c]
		}
	}

	switch {
	case byULID != "" && byCode != "" && byULID != byCode:
		// ULID 与 code 都命中但指向不同目标 → 报错，不静默取其一
		return nil, p.inconsistentErr(b, oldULID, oldCode)
	case byULID != "":
		return byULID, nil
	case byCode != "":
		// 关键：旧 ULID 缺失时也补写 —— 这正是 heims §9.5-1 要求的形态
		return byCode, nil
	case oldULID != "" && p.isNewULID(oldULID):
		return nil, nil // 已是本批次新 ULID → 幂等
	}

	if oldULID == "" && oldCode == "" {
		return nil, nil // 该记录没有此引用字段，正常
	}
	return nil, p.unresolvedErr(b.Field, oldULID, oldCode)
}

// scalarToString 把标量值转为字符串（空值返回空串）。
func scalarToString(v any) string {
	if v == nil {
		return ""
	}
	s := fmt.Sprint(v)
	if s == "" || s == "<nil>" {
		return ""
	}
	return s
}

// resolveULIDArray 解析 ULID 数组引用。
func (p *RemapPlan) resolveULIDArray(v any, b ReferenceBinding) (any, error) {
	items := toAnySlice(v)
	if len(items) == 0 {
		return nil, nil
	}
	out := make([]any, 0, len(items))
	changed := false
	for _, item := range items {
		oldULID := fmt.Sprint(item)
		if oldULID == "" || oldULID == "<nil>" || p.isNewULID(oldULID) {
			out = append(out, item)
			continue
		}
		if newULID, ok := p.oldToNew[oldULID]; ok {
			out = append(out, newULID)
			changed = true
			continue
		}
		return nil, p.unresolvedErr(b.Field, oldULID, "")
	}
	if !changed {
		return nil, nil
	}
	return out, nil
}

// resolveObjectArray 解析对象数组引用（元素内 ULIDKey / CodeKey）。
func (p *RemapPlan) resolveObjectArray(v any, b ReferenceBinding) (any, error) {
	items := toAnySlice(v)
	if len(items) == 0 {
		return nil, nil
	}
	if b.ULIDKey == "" && b.CodeKey == "" {
		return nil, fmt.Errorf("%w: object_array 形态必须配置 ULIDKey 或 CodeKey（field=%s）",
			errs.ErrRemapInvalidConfig, b.Field)
	}
	out := make([]any, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			out = append(out, item) // 非对象元素原样保留
			continue
		}
		if err := p.remapObjectItem(m, b); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// remapObjectItem 重写对象元素内的 ULID（可能带 code）。
//
// 优先级（REQ §与 code 的关系）：
//  1. 旧 ULID → 新 ULID（权威）；
//  2. 旧 ULID 匹配不上时，用 code → 新 ULID 兜底；
//  3. ULID 与 code 同时存在且指向不同目标 → 报错。
func (p *RemapPlan) remapObjectItem(m map[string]any, b ReferenceBinding) error {
	// 从 ULIDKey / CodeKey 取旧值（支持点号路径）
	oldULID, hasULID := p.readKeyPath(m, b.ULIDKey)
	oldCode, hasCode := p.readKeyPath(m, b.CodeKey)

	byULID := ""
	if hasULID && oldULID != "" && !p.isNewULID(oldULID) {
		byULID = p.oldToNew[oldULID]
	}
	byCode := ""
	if hasCode && oldCode != "" {
		byCode = p.codeToNew[oldCode]
	}

	switch {
	case byULID != "" && byCode != "" && byULID != byCode:
		// ULID 与 code 都命中但指向不同目标 → 报错，不静默取其一
		return p.inconsistentErr(b, oldULID, oldCode)
	case byULID != "":
		p.writeKeyPath(m, b.ULIDKey, byULID)
		return nil
	case byCode != "":
		p.writeKeyPath(m, b.ULIDKey, byCode)
		return nil
	case hasULID && oldULID != "" && p.isNewULID(oldULID):
		return nil // 已是本批次新 ULID → 幂等
	case !hasULID && !hasCode:
		return nil // 该元素没有引用信息
	}
	// 落此：有引用信息但两边都解析不出来
	if byULID == "" && byCode == "" && (hasULID || hasCode) {
		if hasULID && oldULID != "" && !p.isNewULID(oldULID) {
			return p.unresolvedErr(b.Field, oldULID, oldCode)
		}
		if !hasULID && hasCode {
			return p.unresolvedErr(b.Field, "", oldCode)
		}
	}
	return nil
}

// readKeyPath 按键路径从 map 读取字符串值（支持 "a.b" 形式的嵌套键）。
func (p *RemapPlan) readKeyPath(m map[string]any, keyPath string) (string, bool) {
	if keyPath == "" {
		return "", false
	}
	v, ok := getByPath(m, keyPath)
	if !ok || v == nil {
		return "", false
	}
	s := fmt.Sprint(v)
	if s == "" || s == "<nil>" {
		return "", false
	}
	return s, true
}

// readRecKey 从记录顶层读一个字符串值（支持点号路径，供标量 CodeField 使用）。
func (p *RemapPlan) readRecKey(rec map[string]any, key string) (string, bool) {
	return p.readKeyPath(rec, key)
}

// writeKeyPath 按键路径写入值（支持 "a.b" 形式的嵌套键）。
func (p *RemapPlan) writeKeyPath(m map[string]any, keyPath, v string) {
	if keyPath == "" {
		return
	}
	if strings.Contains(keyPath, ".") {
		setByPath(m, keyPath, v)
		return
	}
	m[keyPath] = v
}

// isNewULID 判断该值是否已是本批次生成的新 ULID（幂等保护）。
func (p *RemapPlan) isNewULID(v string) bool {
	for _, nv := range p.oldToNew {
		if nv == v {
			return true
		}
	}
	for _, nv := range p.codeToNew {
		if nv == v {
			return true
		}
	}
	return false
}

// unresolvedErr 构造「引用无法解析」错误（带子实体名与字段，便于定位）。
//
// v2 区分来源：跨批次消费（sourceKey 非空）时错误里带上命名空间，
// 便于区分「映射本身拿不到」与「映射拿到了但该值不在映射里」。
func (p *RemapPlan) unresolvedErr(field, ulid, code string) error {
	detail := ""
	if ulid != "" {
		detail = fmt.Sprintf("引用 ULID=%s", ulid)
	}
	if code != "" {
		if detail != "" {
			detail += "、"
		}
		detail += fmt.Sprintf("code=%s", code)
	}
	// 说明映射范围：合并计划既有批内映射也有跨批次映射，
	// 因此文案不写死单一来源（否则批内引用失败时会误导排查方向）。
	scope := "本批次中"
	if p.sourceKey != "" {
		scope = fmt.Sprintf("本批次与命名空间 %q 的合并映射中", p.sourceKey)
	}
	return fmt.Errorf("%w: 子实体 %s 的字段 %s 的%s 在%s找不到对应目标",
		errs.ErrRemapUnresolved, p.handlerName, field, detail, scope)
}

// inconsistentErr 构造「ULID 与 code 指向不一致」错误。
func (p *RemapPlan) inconsistentErr(b ReferenceBinding, ulid, code string) error {
	return fmt.Errorf("%w: 子实体 %s 的字段 %s 的 ULID=%s 与 code=%s 指向不同目标",
		errs.ErrRemapInconsistent, p.handlerName, b.Field, ulid, code)
}

// ============================================================
// 旧主键快照（在清除 PK 前调用）
// ============================================================

// snapshotPKs 在清除子记录旧主键**之前**留存旧主键快照。
//
// 顺序约束（REQ §重映射时机）：
//
//	清除旧子记录主键（本函数先取快照）
//	  → 新 ULID 全部生成
//	  → 构建 code/旧 ULID → 新 ULID 映射
//	  → 重写所有引用字段
//	  → 子记录落库
//
// 主键可能存在于 pkField（gorm 列名）或 JSON 名 "ulid"（BUG-060 约定），两者都查。
func snapshotPKs(childData []map[string]any, pkField string) []string {
	if len(childData) == 0 {
		return nil
	}
	out := make([]string, len(childData))
	for j := range childData {
		if v, ok := readPKValue(childData[j], pkField); ok && v != nil {
			s := fmt.Sprint(v)
			if s != "" && s != "<nil>" {
				out[j] = s
			}
		}
	}
	return out
}

// ============================================================
// 业务侧扩展接口（REQ §「更理想的方式」）
// ============================================================

// ReferenceRemapper 业务实体可实现的扩展接口：由业务层返回「旧 ULID → 新 ULID」
// 的自定义映射，框架在级联事务中统一调用并重写。
//
// 适用场景：引用关系的判定规则无法用 ReferenceRemap 声明式表达
// （如需要跨批次/跨实体查询、或含特殊业务规则）。
//
// 使用方式：把实现了本接口的 Handler 注册名为 ReferenceRemap 的 TargetHandler，
// 或经由 CascadeRelation 的 Remaps 里配置后由框架调用 —— 返回值会并入
// oldToNew 映射表（声明式配置仍生效，两者结果一致即可）。
type ReferenceRemapper interface {
	// RemapReferences 返回本批次的「旧 ULID → 新 ULID」映射。
	// childData 为本批次落库前的子数据（新 ULID 已生成），可据此计算映射。
	RemapReferences(ctx context.Context, childData []map[string]any) (map[string]string, error)
}

// mergeCustomRemap 把业务侧自定义映射并入计划（同键以业务侧为准，业务更懂语义）。
// 返回的 map 会写回 plan.oldToNew。
func mergeCustomRemap(plan *RemapPlan, custom map[string]string) {
	if plan == nil || len(custom) == 0 {
		return
	}
	for k, v := range custom {
		if k == "" || v == "" {
			continue
		}
		plan.oldToNew[k] = v
	}
}

// callCustomRemapper 若子 Handler 实现了 ReferenceRemapper，调用它并并入计划。
// 失败（含返回 error）直接上抛，让事务失败 —— 与声明式路径同一口径：不静默落库。
func callCustomRemapper(ctx context.Context, ch CascadeHandler, plan *RemapPlan) error {
	r, ok := ch.(ReferenceRemapper)
	if !ok || plan == nil {
		return nil
	}
	custom, err := r.RemapReferences(ctx, plan.childData)
	if err != nil {
		return err
	}
	mergeCustomRemap(plan, custom)
	return nil
}

// RemapPlanDebugJSON 输出重映射计划的 JSON 快照（排查用；失败返回空串）。
//
// 导出以便 heims 侧在钩子里打印诊断信息，定位「新版本引用为何指向旧记录」。
func RemapPlanDebugJSON(plan *RemapPlan) string {
	if plan == nil {
		return ""
	}
	b, err := json.Marshal(map[string]any{
		"handler":     plan.handlerName,
		"old_to_new":  plan.oldToNew,
		"code_to_new": plan.codeToNew,
		"bindings":    plan.bindings,
	})
	if err != nil {
		return ""
	}
	return string(b)
}
