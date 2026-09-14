package service

// ============================================================
// CrudRequest — 创建/更新请求约束
// 组合 Mergeable（数据合并） + Identifiable（主键提取）
// ============================================================

// Mergeable 请求数据合并到实体（Create 语义：显式空串不覆盖已有非空值）
type Mergeable[M Record] interface {
	MergeTo(target *M) error
}

// MergeableExisting 可选接口：以「更新已有记录」的语义合并数据（BUG-072）。
//
// 与 MergeTo 的唯一差别：请求中**显式提交的空串会原样写入**，
// 使「把文本字段清空」这一常规操作成为可能（MergeTo 会静默丢弃空串以保护 SetDefaults）。
//
//   - MapRequest 已实现（内部走 mergeByJSON 的 allowClearEmpty=true）；
//   - 业务自定义 Request 类型可按需实现；**未实现时框架自动回退 MergeTo**，
//     行为与修复前完全一致（向后兼容，老接入方零改动）。
//
// service 层在 `_beforeUpdate` / `_beforeUpdateVersioned` 两条更新路径优先调用本方法。
type MergeableExisting[M Record] interface {
	MergeToExisting(target *M) error
}

// mergeRequestTo 按场景选择合并方法：
//   - existing=false（Create）：MergeTo（保护 SetDefaults）
//   - existing=true （Update）：优先 MergeToExisting，未实现则回退 MergeTo（BUG-072 向后兼容）
func mergeRequestTo[M Record](req any, target *M, existing bool) error {
	if existing {
		if r, ok := req.(MergeableExisting[M]); ok {
			return r.MergeToExisting(target)
		}
	}
	r, ok := req.(Mergeable[M])
	if !ok {
		// 既不是 Mergeable 也不是 MergeableExisting：无可合并内容，交由调用方判定
		return nil
	}
	return r.MergeTo(target)
}

// Identifiable 从请求中提取主键
type Identifiable interface {
	GetID() any
}

// Validatable 请求数据自校验（字段非空、格式、枚举值等）
type Validatable interface {
	Validate() error
}

// CrudRequest 创建+编辑请求体必须满足
type CrudRequest[M Record] interface {
	Mergeable[M]
	Identifiable
	Validatable
}

// RequestFields 请求体通过此接口暴露"显式出现的字段"（map 键集合）。
// MapRequest 已实现 Data()；业务自定义 Request 实现后，Create/版本化 Update 可将
// 请求中显式出现的零值字段（0/false/""）真实落库，不被 DB 默认值覆盖（BUG-045）。
type RequestFields interface {
	Data() map[string]any
}

// HasIdempotencyKey 可选接口，请求体通过实现此接口提供幂等键。
// MapRequest 已实现此接口（从 data["idempotency_key"] 提取）。
// 业务侧自定义 Request 类型按需实现，空字符串表示不启用幂等。
type HasIdempotencyKey interface {
	GetIdempotencyKey() string
}
