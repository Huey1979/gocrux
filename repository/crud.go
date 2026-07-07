package repository

import (
	"context"
	"errors"
	"reflect"

	"github.com/Huey1979/gocrux/common"
	"github.com/Huey1979/gocrux/internal/database/mysql"

	"gorm.io/gorm"
)

// ============================================================
// CRUDRepository 泛型仓储基类
//
// 所有标准 CRUD 操作的统一实现。
// 用法：
//
//	repo := NewCRUDRepository[entity.SysRole]()
//	role, err := repo.GetByID(ctx, "01Jxxx...")
//
// 事务场景：
//
//	tx := mysql.DB.WithCtx(ctx).Begin()
//	repo.SetDB(tx)
//	// ... 操作 ...
//	tx.Commit()
// ============================================================

// CRUDRepository 泛型仓储
// M = GORM Model 类型（必须为 struct 指针的零值语义型，如 entity.SysRole）
type CRUDRepository[M any] struct {
	db     *gorm.DB // 写库
	readDB *gorm.DB // 读库（nil = 回退到写库）

	// 允许外部配置字段列名，默认自动推导
	pkField string // 主键列名（默认 "id"；若已知模型 PK 列名可在 NewXxx 中覆盖）
}

// DefaultReadDB 全局读库实例。heims 启动时注入，所有 NewCRUDRepository 自动拾取。
var DefaultReadDB *gorm.DB

// SetDefaultReadDB 注入全局读库（由 heims main 调用）。
func SetDefaultReadDB(db *gorm.DB) { DefaultReadDB = db }

// NewCRUDRepository 创建泛型仓储实例。
// 若通过 SetDefaultReadDB 注入了读库，自动配置读写分离。
func NewCRUDRepository[M any]() *CRUDRepository[M] {
	r := &CRUDRepository[M]{
		db:      mysql.DB.InternalDB(),
		readDB:  DefaultReadDB,
		pkField: "id",
	}
	r.detectPK()
	return r
}

// NewCRUDWithDB 使用自定义 DB 实例创建仓储（主要用于测试）。
// 与 NewCRUDRepository 行为一致，但不依赖 mysql.DB 全局实例。
func NewCRUDWithDB[M any](db *gorm.DB) *CRUDRepository[M] {
	r := &CRUDRepository[M]{
		db:      db,
		pkField: "id",
	}
	r.detectPK()
	return r
}

// SetDB 注入写库实例。
func (r *CRUDRepository[M]) SetDB(db *gorm.DB) *CRUDRepository[M] {
	r.db = db
	return r
}

// SetReadDB 注入读库实例（读写分离）。nil 时回退到写库。
func (r *CRUDRepository[M]) SetReadDB(db *gorm.DB) *CRUDRepository[M] {
	r.readDB = db
	return r
}

// SetPKField 显式设置主键列名（当自动推导不可靠时使用）
func (r *CRUDRepository[M]) SetPKField(column string) *CRUDRepository[M] {
	r.pkField = column
	return r
}

// PKField 返回当前主键列名
func (r *CRUDRepository[M]) PKField() string {
	return r.pkField
}

// DB 返回写库 *gorm.DB（已附加请求 context）。
// 事务中始终走写库。
func (r *CRUDRepository[M]) DB(ctx context.Context) *gorm.DB {
	if tx := common.GetTx(ctx); tx != nil {
		return tx.WithContext(ctx)
	}
	return r.db.WithContext(ctx)
}

// ReadDB 返回读库 *gorm.DB。事务中或未配置 readDB 时回退到写库。
func (r *CRUDRepository[M]) ReadDB(ctx context.Context) *gorm.DB {
	if tx := common.GetTx(ctx); tx != nil {
		return tx.WithContext(ctx) // 事务中走写库保证一致性
	}
	if r.readDB != nil {
		return r.readDB.WithContext(ctx)
	}
	return r.db.WithContext(ctx) // 未配读库则回退写库
}

// ============================================================
// 基础 CRUD
// ============================================================

// Insert 插入一条记录
func (r *CRUDRepository[M]) Insert(ctx context.Context, entity *M) error {
	return r.DB(ctx).Create(entity).Error
}

// InsertBatch 批量插入记录
func (r *CRUDRepository[M]) InsertBatch(ctx context.Context, entities []*M) error {
	if len(entities) == 0 {
		return nil
	}
	return r.DB(ctx).Create(entities).Error
}

// GetByID 根据主键 ID 获取单条记录
// 通过 detectPK 推导的主键列名来显式构造 WHERE 条件。
func (r *CRUDRepository[M]) GetByID(ctx context.Context, id any) (*M, error) {
	var m M
	err := r.ReadDB(ctx).Where(r.pkField+" = ?", id).First(&m).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, gorm.ErrRecordNotFound
		}
		return nil, err
	}
	return &m, nil
}

// GetByField 根据任意字段值获取第一条记录
// 示例: repo.GetByField(ctx, "site_code", "S001")
func (r *CRUDRepository[M]) GetByField(ctx context.Context, field string, value any) (*M, error) {
	var m M
	err := r.ReadDB(ctx).Where(field+" = ?", value).First(&m).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, gorm.ErrRecordNotFound
		}
		return nil, err
	}
	return &m, nil
}

// GetByCode 根据 code 字段值获取记录（便捷方法）
// 等效于 GetByField(ctx, "code", value)
func (r *CRUDRepository[M]) GetByCode(ctx context.Context, code string) (*M, error) {
	return r.GetByField(ctx, "code", code)
}

// ExistsByField 检查是否存在满足条件的记录
func (r *CRUDRepository[M]) ExistsByField(ctx context.Context, field string, value any) (bool, error) {
	var count int64
	err := r.ReadDB(ctx).Model(new(M)).Where(field+" = ?", value).Count(&count).Error
	return count > 0, err
}

// Save 保存记录（upsert，根据主键判断 create 还是 update）
func (r *CRUDRepository[M]) Save(ctx context.Context, entity *M) error {
	return r.DB(ctx).Save(entity).Error
}

// UpdateByID 按主键更新指定字段
// updates 为 map[string]any，如 map[string]any{"status": "deleted"}
func (r *CRUDRepository[M]) UpdateByID(ctx context.Context, id any, updates map[string]any) error {
	return r.DB(ctx).Model(new(M)).Where(r.pkField+" = ?", id).Updates(updates).Error
}

// Delete 按主键删除记录（硬删除）
// 注意：业务中通常使用软删除（status='deleted' 等），请使用 UpdateByID 或 service 层封装。
// 使用 pkField+" = ?" 以避免 GORM 将非整型主键值错误解析为 SQL 片段。
func (r *CRUDRepository[M]) Delete(ctx context.Context, id any) error {
	return r.DB(ctx).Where(r.pkField+" = ?", id).Delete(new(M)).Error
}

// ============================================================
// 结构化过滤条件
// ============================================================

// FilterOp 过滤操作符
type FilterOp string

const (
	OpEQ    FilterOp = "eq"
	OpNEQ   FilterOp = "neq"
	OpLike  FilterOp = "like"
	OpGT    FilterOp = "gt"
	OpGTE   FilterOp = "gte"
	OpLT    FilterOp = "lt"
	OpLTE   FilterOp = "lte"
	OpIn    FilterOp = "in"
	OpRange FilterOp = "between"
	OpRaw   FilterOp = "raw" // 原生 SQL 条件（Value 为 (string, []any) 或仅 string）
)

// Filter 单个过滤条件
type Filter struct {
	Field string   // DB 列名
	Op    FilterOp // 操作符
	Value any      // 值
}

// ListFilters 列表查询条件
//
// 参数语法说明
// ============
//
// 一、字段说明
//
//	Page     int      — 页码，从 1 开始。不填（零值）默认为 1。
//	PageSize int      — 每页条数。<=0 表示不分页，返回全部结果。
//	Filters  []Filter — 过滤条件数组，为空表示无过滤。
//	Logic    string   — 多个 Filter 之间的逻辑连接符：
//	                    "and"（默认）：所有条件 AND 连接。
//	                    "or"：所有条件 OR 连接。
//	OrderBy  string   — 排序字段，必须是 DB 列名（不是 Go 字段名）。
//	OrderDir string   — 排序方向："asc"（默认、升序）、"desc"（降序）。
//
// 二、FilterOp 支持的操作符及 Value 要求
//
//	OpEQ    ("eq")     → field = ?             Value: 单个值
//	OpNEQ   ("neq")    → field != ?            Value: 单个值
//	OpLike  ("like")   → field LIKE ?          Value: 字符串，需自行拼 %（如 "%关键词%"）
//	OpGT    ("gt")     → field > ?             Value: 数字/时间
//	OpGTE   ("gte")    → field >= ?            Value: 数字/时间
//	OpLT    ("lt")     → field < ?             Value: 数字/时间
//	OpLTE   ("lte")    → field <= ?            Value: 数字/时间
//	OpIn    ("in")     → field IN (?,?,...)    Value: 切片（如 []string{"a","b"}）
//	OpRange ("between")→ field BETWEEN ? AND ? Value: 长度为 2 的切片（如 []int{1,100}）
//
// 三、使用示例
//
// 1. 简单等值查询（默认 AND）
//
//	ListFilters{
//	    Filters: []Filter{
//	        {Field: "status", Op: OpEQ,  Value: "active"},
//	        {Field: "deleted_at", Op: OpEQ, Value: nil},
//	    },
//	    OrderBy:  "created_at",
//	    OrderDir: "desc",
//	    Page:     1,
//	    PageSize: 20,
//	}
//	→ SQL: WHERE status = 'active' AND deleted_at IS NULL ORDER BY created_at DESC LIMIT 20 OFFSET 0
//
// 2. LIKE 模糊搜索
//
//	ListFilters{
//	    Filters: []Filter{
//	        {Field: "name", Op: OpLike, Value: "%测试%"},
//	    },
//	}
//	→ SQL: WHERE name LIKE '%测试%'
//
// 3. IN 批量查询
//
//	ListFilters{
//	    Filters: []Filter{
//	        {Field: "site_code", Op: OpIn, Value: []string{"S001", "S002", "S003"}},
//	    },
//	}
//	→ SQL: WHERE site_code IN ('S001','S002','S003')
//
// 4. BETWEEN 范围查询
//
//	ListFilters{
//	    Filters: []Filter{
//	        {Field: "created_at", Op: OpRange, Value: []time.Time{start, end}},
//	    },
//	}
//	→ SQL: WHERE created_at BETWEEN ? AND ?
//
// 5. OR 逻辑（所有 Filter 用 OR 连接）
//
//	ListFilters{
//	    Logic: "or",
//	    Filters: []Filter{
//	        {Field: "name", Op: OpLike, Value: "%张%"},
//	        {Field: "name", Op: OpLike, Value: "%王%"},
//	    },
//	}
//	→ SQL: WHERE name LIKE '%张%' OR name LIKE '%王%'
//
// 6. 多条件复合 + 排序 + 分页
//
//	ListFilters{
//	    Filters: []Filter{
//	        {Field: "status", Op: OpEQ, Value: "active"},
//	        {Field: "level", Op: OpGTE, Value: 3},
//	        {Field: "name", Op: OpLike, Value: "%研发%"},
//	    },
//	    OrderBy:  "updated_at",
//	    OrderDir: "desc",
//	    Page:     2,
//	    PageSize: 15,
//	}
//	→ SQL: WHERE status='active' AND level>=3 AND name LIKE '%研发%'
//	      ORDER BY updated_at DESC LIMIT 15 OFFSET 15
//
// 7. 无过滤全表查询（只分页 + 排序）
//
//	ListFilters{
//	    OrderBy:  "sort_order",
//	    OrderDir: "asc",
//	    Page:     1,
//	    PageSize: 50,
//	}
//	→ SQL: ORDER BY sort_order ASC LIMIT 50 OFFSET 0
//
// 四、注意事项
//
//   - Field 必须填 DB 列名（snake_case），不是 Go 结构体字段名。
//   - Logic 只影响同层 Filters 数组内的连接方式，不支持 AND/OR 嵌套分组
//     （如需嵌套，请在 Service 层覆盖 DoList 钩子 + RawQuery）。
//   - 如需 JOIN / GROUP BY / 子查询，请使用 DoList 钩子 + repo.RawQuery()，
//     不要尝试用 ListFilters 实现。
type ListFilters struct {
	Page     int      // 页码（>=1）
	PageSize int      // 每页条数（<=0 不分页）
	Filters  []Filter // 过滤条件
	Logic    string   // 逻辑连接符："and"（默认）、"or"
	OrderBy  string   // 排序字段（DB 列名）
	OrderDir string   // 排序方向："asc"（默认）、"desc"
}

// ============================================================
// 列表查询
// ============================================================

// List 核心列表查询，所有列表方法均委托到此方法。
// query 为可选的 GORM 条件链（可为 nil 表示无过滤条件）。
// page/ pageSize：当 pageSize <= 0 时不分页、返回全部（不走 count）；否则按标准分页。
// 返回：记录列表、总数（不分页时返回 len(results)）、错误
func (r *CRUDRepository[M]) List(ctx context.Context, query func(*gorm.DB) *gorm.DB, page, pageSize int) ([]M, int64, error) {
	db := r.ReadDB(ctx).Model(new(M))
	if query != nil {
		db = query(db)
	}

	// pageSize <= 0：不分页，取全部
	if pageSize <= 0 {
		var results []M
		if err := db.Find(&results).Error; err != nil {
			return nil, 0, err
		}
		return results, int64(len(results)), nil
	}

	// 标准分页
	if page < 1 {
		page = 1
	}

	// 先统计总数
	var total int64
	if err := db.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 分页查询
	var results []M
	offset := (page - 1) * pageSize
	if err := db.Offset(offset).Limit(pageSize).Find(&results).Error; err != nil {
		return nil, 0, err
	}

	return results, total, nil
}

// ListWhere 按简单条件查询列表（AND 拼接）
// conditions: map[string]any{"status": "active", "type": "business"}
// 注意：仅支持等值匹配，不支持 LIKE/范围查询。复杂查询请使用 List() 传入 query 函数。
func (r *CRUDRepository[M]) ListWhere(ctx context.Context, conditions map[string]any, page, pageSize int) ([]M, int64, error) {
	// 分页参数约束由调用方控制
	if pageSize <= 0 {
		return r.List(ctx, r.buildWhereQuery(conditions), page, pageSize)
	}
	if page < 1 {
		page = 1
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return r.List(ctx, r.buildWhereQuery(conditions), page, pageSize)
}

// buildWhereQuery 内部工具：构建等值条件查询函数
func (r *CRUDRepository[M]) buildWhereQuery(conditions map[string]any) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		for k, v := range conditions {
			db = db.Where(k+" = ?", v)
		}
		return db
	}
}

// ListAll 查询所有记录（委托 List，pageSize=0 不分页）
func (r *CRUDRepository[M]) ListAll(ctx context.Context) ([]M, error) {
	results, _, err := r.List(ctx, nil, 0, 0)
	return results, err
}

// ListAllByField 根据字段值查询全部匹配记录（含软删除、无分页）
// 示例: repo.ListAllByField(ctx, "site_code", "S001")
func (r *CRUDRepository[M]) ListAllByField(ctx context.Context, field string, value any) ([]M, error) {
	query := func(db *gorm.DB) *gorm.DB {
		return db.Unscoped().Where(field+" = ?", value)
	}
	results, _, err := r.List(ctx, query, 0, 0)
	return results, err
}

// ListByFilters 按结构化过滤条件查询列表
// 支持 EQ / NEQ / LIKE / GT / GTE / LT / LTE / IN / BETWEEN 等操作符，
// 以及 AND / OR 逻辑连接、排序、分页。
func (r *CRUDRepository[M]) ListByFilters(ctx context.Context, f ListFilters) ([]M, int64, error) {
	query := func(db *gorm.DB) *gorm.DB {
		if len(f.Filters) > 0 {
			for _, filter := range f.Filters {
				db = applyFilter(db, filter, f.Logic)
			}
		}
		if f.OrderBy != "" {
			dir := f.OrderDir
			if dir == "" {
				dir = "asc"
			}
			db = db.Order(f.OrderBy + " " + dir)
		}
		return db
	}
	return r.List(ctx, query, f.Page, f.PageSize)
}

// applyFilter 将单个 Filter 应用到 GORM query
func applyFilter(db *gorm.DB, f Filter, logic string) *gorm.DB {
	buildWhere := func(condition string, args ...any) *gorm.DB {
		if logic == "or" {
			return db.Or(condition, args...)
		}
		return db.Where(condition, args...)
	}

	switch f.Op {
	case "or_group":
		// OR 组：多个子 filter 之间用 OR 连接，整体作为 AND 条件
		subs, _ := f.Value.([]Filter)
		if len(subs) > 0 {
			subDB := db.Session(&gorm.Session{NewDB: true})
			for _, sub := range subs {
				subDB = applyFilter(subDB, sub, "or")
			}
			return db.Where(subDB)
		}
		return db
	case OpRaw:
		switch v := f.Value.(type) {
		case string:
			return buildWhere(v)
		case []any:
			if len(v) >= 2 {
				if cond, ok := v[0].(string); ok {
					return buildWhere(cond, v[1:]...)
				}
			}
			return db
		default:
			return db
		}
	case OpEQ:
		return buildWhere(f.Field+" = ?", f.Value)
	case OpNEQ:
		return buildWhere(f.Field+" != ?", f.Value)
	case OpLike:
		return buildWhere(f.Field+" LIKE ?", f.Value)
	case OpGT:
		return buildWhere(f.Field+" > ?", f.Value)
	case OpGTE:
		return buildWhere(f.Field+" >= ?", f.Value)
	case OpLT:
		return buildWhere(f.Field+" < ?", f.Value)
	case OpLTE:
		return buildWhere(f.Field+" <= ?", f.Value)
	case OpIn:
		return buildWhere(f.Field+" IN ?", f.Value)
	case OpRange:
		return buildWhere(f.Field+" BETWEEN ? AND ?", f.Value)
	default:
		return buildWhere(f.Field+" = ?", f.Value)
	}
}

// RawQuery 执行原始 SQL 并扫描到 dest。
// dest 为 *[]*M、*[]M 或任意结构体切片指针（如 JOIN 视图结构体）。
// SQL 和 dest 类型均不绑定 M，用法灵活。
// 示例:
//
//	var results []MyJoinView
//	repo.RawQuery(ctx, &results, "SELECT a.*, b.name FROM a LEFT JOIN b ON ...")
func (r *CRUDRepository[M]) RawQuery(ctx context.Context, dest any, sql string, args ...any) error {
	return r.DB(ctx).Raw(sql, args...).Scan(dest).Error
}

// RawList 实现 Repo[M] 接口。委托 RawQuery 执行原生 SQL。
func (r *CRUDRepository[M]) RawList(ctx context.Context, dest any, query any, args ...any) error {
	sql, ok := query.(string)
	if !ok {
		return errors.New("CRUDRepository.RawList: query must be string (SQL)")
	}
	return r.RawQuery(ctx, dest, sql, args...)
}

// ============================================================
// 聚合
// ============================================================

// Count 按条件计数
func (r *CRUDRepository[M]) Count(ctx context.Context, query func(*gorm.DB) *gorm.DB) (int64, error) {
	db := r.ReadDB(ctx).Model(new(M))
	if query != nil {
		db = query(db)
	}
	var count int64
	err := db.Count(&count).Error
	return count, err
}

// ============================================================
// 事务支持
// ============================================================

// Transaction 在事务中执行操作
// 回调函数 fn 接收的 *gorm.DB 是已开启事务的实例。
// 自动处理 commit / rollback。
func (r *CRUDRepository[M]) Transaction(ctx context.Context, fn func(tx *gorm.DB) error) error {
	return r.DB(ctx).Transaction(fn)
}

// ListByField 实现 Repo[M] 接口（委托 ListAllByField）。
func (r *CRUDRepository[M]) ListByField(ctx context.Context, field string, value any) ([]M, error) {
	return r.ListAllByField(ctx, field, value)
}

// RunInTx 实现 Repo[M] 接口。GORM 事务包装。
func (r *CRUDRepository[M]) RunInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return r.DB(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(common.WithTx(ctx, tx))
	})
}

// ============================================================
// Batch — Repo[M] 接口方法（抽象 GORM DB() 调用）
// ============================================================

// BatchSoftDelete 批量软删除（按主键 IN）。
func (r *CRUDRepository[M]) BatchSoftDelete(ctx context.Context, ids []any) error {
	return r.DB(ctx).Model(new(M)).Where(r.pkField+" IN ?", ids).Update("is_deleted", int8(1)).Error
}

// BatchSoftDeleteByFK 批量软删除（按外键 IN）。
func (r *CRUDRepository[M]) BatchSoftDeleteByFK(ctx context.Context, fkField string, fkValues []any) error {
	return r.DB(ctx).Model(new(M)).Where(fkField+" IN ?", fkValues).Update("is_deleted", int8(1)).Error
}

// BatchFindByPK 批量按主键查询。
func (r *CRUDRepository[M]) BatchFindByPK(ctx context.Context, ids []any) ([]M, error) {
	var records []M
	if err := r.ReadDB(ctx).Model(new(M)).Where(r.pkField+" IN ?", ids).Find(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}

// BatchFindByFK 批量按外键查询。
func (r *CRUDRepository[M]) BatchFindByFK(ctx context.Context, fkField string, fkValues []any) ([]M, error) {
	var records []M
	if err := r.ReadDB(ctx).Model(new(M)).Where(fkField+" IN ?", fkValues).Find(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}

// BatchHardDelete 批量硬删除（按主键 IN）。
func (r *CRUDRepository[M]) BatchHardDelete(ctx context.Context, ids []any) error {
	return r.DB(ctx).Unscoped().Where(r.pkField+" IN ?", ids).Delete(new(M)).Error
}

// BatchHardDeleteByFK 批量硬删除（按外键 IN）。
func (r *CRUDRepository[M]) BatchHardDeleteByFK(ctx context.Context, fkField string, fkValues []any) error {
	return r.DB(ctx).Unscoped().Where(fkField+" IN ?", fkValues).Delete(new(M)).Error
}

// DeleteByFK 按外键批量删除（Repo 接口方法别名）。
func (r *CRUDRepository[M]) DeleteByFK(ctx context.Context, fkField string, fkValues []any) error {
	return r.BatchHardDeleteByFK(ctx, fkField, fkValues)
}

// ============================================================
// 内部工具
// ============================================================

// detectPK 从 M 的 GORM 标签自动推导主键列名
func (r *CRUDRepository[M]) detectPK() {
	var m M
	t := reflect.TypeOf(m)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		gormTag := field.Tag.Get("gorm")
		if gormTag == "" {
			continue
		}
		// 检查是否包含 primaryKey
		if matchPKTag(gormTag) {
			// 提取 column 名称，如果没有 column 子标签则用字段名
			col := extractColumn(gormTag, common.ToSnakeCase(field.Name))
			if col != "" {
				r.pkField = col
			}
			return
		}
	}
}

// BatchDeprecateVersions 版本化批量废弃（GORM）。
func (r *CRUDRepository[M]) BatchDeprecateVersions(ctx context.Context, ids []any) error {
	return r.DB(ctx).Model(new(M)).Where(r.pkField+" IN ?", ids).
		Updates(map[string]any{"is_current": 0, "version_status": "deprecated"}).Error
}

// BatchDeprecateVersionsByFK 版本化按外键批量废弃（GORM）。
func (r *CRUDRepository[M]) BatchDeprecateVersionsByFK(ctx context.Context, fkField string, fkValues []any) error {
	return r.DB(ctx).Model(new(M)).Where(fkField+" IN ?", fkValues).
		Updates(map[string]any{"is_current": 0, "version_status": "deprecated"}).Error
}

// matchPKTag 检查 GORM 标签是否包含 primaryKey
func matchPKTag(tag string) bool {
	// 简化分析：遍历 ; 分隔的片段
	start := 0
	for i := 0; i <= len(tag); i++ {
		if i == len(tag) || tag[i] == ';' {
			part := tag[start:i]
			if part == "primaryKey" || part == "primarykey" {
				return true
			}
			start = i + 1
		}
	}
	return false
}

// extractColumn 从 GORM 标签中提取 column 名称
func extractColumn(tag string, defaultVal string) string {
	// 格式:  "column:site_ulid;primaryKey;size:26"
	start := 0
	for i := 0; i <= len(tag); i++ {
		if i == len(tag) || tag[i] == ';' {
			part := tag[start:i]
			if len(part) > 7 && part[:7] == "column:" {
				return part[7:]
			}
			start = i + 1
		}
	}
	return defaultVal
}

// ============================================================
