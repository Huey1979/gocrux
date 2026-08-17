package mysql

// ============================================================
// Schema 数据结构 — migration 内部使用
// ============================================================

// columnSchema 列定义。
type columnSchema struct {
	Name         string // 列名，如 "flow_ulid"
	DataType     string // 数据类型，如 "VARCHAR"
	Size         int    // 长度，如 26；0 表示无需指定（如 INT、DATETIME）
	Nullable     bool   // 是否允许 NULL
	HasDefault   bool   // 是否有显式默认值
	DefaultVal   string // 默认值 SQL 片段，如 "'active'"、"CURRENT_TIMESTAMP"
	IsPrimaryKey bool   // 是否为主键
}

// indexSchema 索引定义。
type indexSchema struct {
	Name    string   // 索引名，如 "idx_flow_code"
	Columns []string // 索引列名列表
	Unique  bool     // 是否唯一索引
}

// tableSchema 表结构定义。
type tableSchema struct {
	Name    string         // 表名，如 "sys_flow"
	Columns []columnSchema // 列列表（顺序无关，比对时按 Name 匹配）
	Indexes []indexSchema  // 索引列表
}

// ============================================================
// Diff 数据结构
// ============================================================

// tableDiff 单个表的比对结果。
type tableDiff struct {
	Table string // 表名

	// 预期有、实际无
	MissingColumns []columnSchema
	MissingIndexes []indexSchema

	// 预期无、实际有
	ExtraColumns []columnSchema
	ExtraIndexes []indexSchema

	// 两方都有但定义不一致（类型/长度/可空/默认值）
	ColumnConflicts []columnConflict
}

// columnConflict 列定义冲突。
type columnConflict struct {
	Name     string
	Expected columnSchema
	Actual   columnSchema
}
