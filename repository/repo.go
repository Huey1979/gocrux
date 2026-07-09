package repository

import "context"

// Repo 通用仓储接口（多态替换 MySQL / MongoDB）。
//
// CRUDRepository（MySQL/GORM）与 MongoCRUDRepository（MongoDB）
// 均实现此接口。GenericService 依赖此接口而非具体实现。
type Repo[M any] interface {
	// ===== 基础 CRUD =====
	Insert(ctx context.Context, entity *M) error
	InsertBatch(ctx context.Context, entities []*M) error
	GetByID(ctx context.Context, id any) (*M, error)
	GetByField(ctx context.Context, field string, value any) (*M, error)
	Save(ctx context.Context, entity *M) error
	UpdateByID(ctx context.Context, id any, updates map[string]any) error
	UpdateByIDs(ctx context.Context, ids []any, updates map[string]any) error
	Delete(ctx context.Context, id any) error
	DeleteByFK(ctx context.Context, fkField string, fkValues []any) error

	// ===== 批量操作（抽象 GORM DB() 调用的替代） =====
	BatchSoftDelete(ctx context.Context, ids []any) error
	BatchSoftDeleteByFK(ctx context.Context, fkField string, fkValues []any) error
	BatchFindByPK(ctx context.Context, ids []any) ([]M, error)
	BatchFindByFK(ctx context.Context, fkField string, fkValues []any) ([]M, error)
	BatchHardDelete(ctx context.Context, ids []any) error
	BatchHardDeleteByFK(ctx context.Context, fkField string, fkValues []any) error
	BatchDeprecateVersions(ctx context.Context, ids []any) error
	BatchDeprecateVersionsByFK(ctx context.Context, fkField string, fkValues []any) error

	// ===== 列表查询 =====
	ListByFilters(ctx context.Context, filters ListFilters) ([]M, int64, error)
	ListAll(ctx context.Context) ([]M, error)
	ListByField(ctx context.Context, field string, value any) ([]M, error)

	// RawList 执行原生查询并将结果扫描到 dest（必须为 *[]M 类型）。
	// MySQL:  query 为 SQL 语句（支持 ? 占位符），args 为参数值。
	// MongoDB: query 为 bson.M 过滤器，args 忽略。
	// 用于 ListByFilters 无法表达的复杂查询（JOIN、子查询、聚合等）。
	RawList(ctx context.Context, dest any, query any, args ...any) error

	// ===== 事务 =====
	// RunInTx 在事务内执行 fn。具体事务类型由实现决定（MySQL=GORM Tx, MongoDB=Session）。
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error

	// ===== 元数据 =====
	PKField() string
}
