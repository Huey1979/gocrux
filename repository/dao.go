package repository

import (
	"context"

	"github.com/Huey1979/gocrux/internal/database/mysql"
	"github.com/Huey1979/gocrux/internal/database/redis"

	"github.com/sirupsen/logrus"
)

// ========== 通用 DAO（数据访问对象）==========

// DAO 通用数据访问接口
// 封装所有基础数据库操作，便于统一扩展（如缓存、审计等）
type DAO interface {
	Create(ctx context.Context, entity interface{}) error
	GetByULID(ctx context.Context, ulid string) (interface{}, error)
	GetByCode(ctx context.Context, code string) (interface{}, error)
	Update(ctx context.Context, entity interface{}) error
	Delete(ctx context.Context, ulid string) error
	List(ctx context.Context) ([]interface{}, error)
}

// BaseDAO 通用数据访问实现
type BaseDAO struct {
	TableName    string
	ULIDField    string
	CodeField    string
	EntityType   interface{} // 实体类型指针
	CacheEnabled bool        // 是否启用缓存
}

// NewBaseDAO 创建通用DAO
func NewBaseDAO(tableName, ulidField, codeField string, entityType interface{}) *BaseDAO {
	return &BaseDAO{
		TableName:  tableName,
		ULIDField:  ulidField,
		CodeField:  codeField,
		EntityType: entityType,
	}
}

// Create 创建记录
// 扩展点：可在此处添加缓存、审计日志等
func (d *BaseDAO) Create(ctx context.Context, entity interface{}) error {
	// 1. 数据库创建
	if err := mysql.DB.WithCtx(ctx).Create(entity).Error; err != nil {
		return err
	}

	// 2. 扩展：缓存处理（如需要）
	d.onAfterCreate(entity)

	// 3. 扩展：审计日志（如需要）
	logrus.Debugf("[DAO] Created record in %s", d.TableName)

	return nil
}

// onAfterCreate 创建后扩展点
func (d *BaseDAO) onAfterCreate(entity interface{}) {
	if d.CacheEnabled {
		// TODO: 可在此处添加缓存逻辑
		// 例如：cache.UserCache.Set(...)
	}
}

// GetByULID 根据ULID获取记录
// 扩展点：可先查缓存，缓存未命中再查数据库
func (d *BaseDAO) GetByULID(ctx context.Context, ulid string) (interface{}, error) {
	// 1. 扩展：先尝试从缓存获取
	if d.CacheEnabled {
		if cached := d.getFromCache(ulid); cached != nil {
			return cached, nil
		}
	}

	// 2. 数据库查询
	entity := d.EntityType
	err := mysql.DB.WithCtx(ctx).Table(d.TableName).
		Where(d.ULIDField+" = ? AND is_deleted = 0", ulid).
		First(entity).Error
	if err != nil {
		return nil, err
	}

	// 3. 扩展：存入缓存
	if d.CacheEnabled {
		d.setToCache(ulid, entity)
	}

	return entity, nil
}

// GetByCode 根据编码获取记录（当前版本）
func (d *BaseDAO) GetByCode(ctx context.Context, code string) (interface{}, error) {
	// 1. 扩展：先尝试从缓存获取
	if d.CacheEnabled {
		if cached := d.getFromCacheByCode(code); cached != nil {
			return cached, nil
		}
	}

	// 2. 数据库查询
	entity := d.EntityType
	err := mysql.DB.WithCtx(ctx).Table(d.TableName).
		Where(d.CodeField+" = ? AND is_current = 1 AND is_deleted = 0", code).
		First(entity).Error
	if err != nil {
		return nil, err
	}

	// 3. 扩展：存入缓存
	if d.CacheEnabled {
		d.setToCacheByCode(code, entity)
	}

	return entity, nil
}

// Update 更新记录
// 扩展点：可在此处处理缓存失效、版本管理等
func (d *BaseDAO) Update(ctx context.Context, entity interface{}) error {
	// 1. 扩展：更新前处理（如版本管理）
	d.onBeforeUpdate(entity)

	// 2. 数据库更新
	if err := mysql.DB.WithCtx(ctx).Save(entity).Error; err != nil {
		return err
	}

	// 3. 扩展：更新后处理（如缓存失效）
	d.onAfterUpdate(entity)

	return nil
}

// onBeforeUpdate 更新前扩展点
func (d *BaseDAO) onBeforeUpdate(entity interface{}) {
	// TODO: 可在此处添加版本检查等逻辑
}

// onAfterUpdate 更新后扩展点
func (d *BaseDAO) onAfterUpdate(entity interface{}) {
	if d.CacheEnabled {
		// 失效缓存
		d.invalidateCache(entity)
	}
}

// Delete 删除记录（软删除）
// 扩展点：处理缓存失效、版本历史等
func (d *BaseDAO) Delete(ctx context.Context, ulid string) error {
	// 1. 扩展：删除前处理
	d.onBeforeDelete(ulid)

	// 2. 数据库软删除
	err := mysql.DB.WithCtx(ctx).Table(d.TableName).
		Where(d.ULIDField+" = ?", ulid).
		Update("is_deleted", 1).Error
	if err != nil {
		return err
	}

	// 3. 扩展：删除后处理
	d.onAfterDelete(ulid)

	return nil
}

// onBeforeDelete 删除前扩展点
func (d *BaseDAO) onBeforeDelete(ulid string) {
	// TODO: 可在此处添加删除校验（如是否有关联数据）
}

// onAfterDelete 删除后扩展点
func (d *BaseDAO) onAfterDelete(ulid string) {
	if d.CacheEnabled {
		d.invalidateCacheByULID(ulid)
	}
}

// List 获取列表
func (d *BaseDAO) List(ctx context.Context) ([]interface{}, error) {
	results, err := d.listWithCondition(ctx, "is_current = 1 AND is_deleted = 0")
	if err != nil {
		return nil, err
	}

	entities := make([]interface{}, len(results))
	for i, r := range results {
		entities[i] = r
	}
	return entities, nil
}

// listWithCondition 条件查询
func (d *BaseDAO) listWithCondition(ctx context.Context, where string, args ...interface{}) ([]interface{}, error) {
	rows, err := mysql.DB.WithCtx(ctx).Table(d.TableName).Where(where, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []interface{}
	for rows.Next() {
		entity := d.EntityType
		if err := mysql.DB.ScanRows(rows, entity); err != nil {
			return nil, err
		}
		results = append(results, entity)
	}

	return results, nil
}

// ========== 缓存扩展方法 ==========

// getFromCache 从缓存获取（placeholder）
func (d *BaseDAO) getFromCache(ulid string) interface{} {
	// TODO: 实现缓存逻辑
	return nil
}

// setToCache 设置缓存（placeholder）
func (d *BaseDAO) setToCache(ulid string, entity interface{}) {
	// TODO: 实现缓存逻辑
	_ = redis.Client
}

// getFromCacheByCode 根据编码从缓存获取（placeholder）
func (d *BaseDAO) getFromCacheByCode(code string) interface{} {
	return nil
}

// setToCacheByCode 根据编码设置缓存（placeholder）
func (d *BaseDAO) setToCacheByCode(code string, entity interface{}) {
	// TODO: 实现缓存逻辑
}

// invalidateCache 失效缓存
func (d *BaseDAO) invalidateCache(entity interface{}) {
	// TODO: 实现缓存失效逻辑
}

// invalidateCacheByULID 根据ULID失效缓存
func (d *BaseDAO) invalidateCacheByULID(ulid string) {
	// TODO: 实现缓存失效逻辑
}

// DAO 操作哨兵错误。
var (
	ErrRecordNotFound = &DAOError{Code: "E10001", Message: "记录不存在"}
	ErrRecordExists   = &DAOError{Code: "E10002", Message: "记录已存在"}
)

// DAOError DAO 层错误类型，包含业务错误码。
type DAOError struct {
	Code    string // 业务错误码
	Message string // 错误描述
}

// Error 实现 error 接口。
func (e *DAOError) Error() string {
	return e.Message
}

// NewDAOError 创建DAO错误
func NewDAOError(code, message string) *DAOError {
	return &DAOError{Code: code, Message: message}
}
