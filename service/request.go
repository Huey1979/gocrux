package service

// ============================================================
// CrudRequest — 创建/更新请求约束
// 组合 Mergeable（数据合并） + Identifiable（主键提取）
// ============================================================

// Mergeable 请求数据合并到实体
type Mergeable[M Record] interface {
	MergeTo(target *M) error
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
