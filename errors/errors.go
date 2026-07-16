package errs

import (
	"errors"
	"fmt"
)

// ============================================================
// 通用
// ============================================================
var (
	ErrUniqueValidationFailed = errors.New("unique validation failed")
	ErrInvalidParam           = errors.New("参数无效")
	ErrDuplicateCode          = errors.New("编码已存在，请更换 form_code 或使用 Update 更新现有表单")
)

// errFieldValidationSentinel 字段校验哨兵（不导出，通过 IsFieldValidation 检查）。
var errFieldValidationSentinel = errors.New("field validation failed")

// errMissingParamSentinel 缺失参数哨兵（不导出，通过 IsMissingParam 检查）。
var errMissingParamSentinel = errors.New("missing required parameter")

// ============================================================
// 通用服务 (generic) — 框架内部使用
// ============================================================
var (
	ErrUpdateDataNotRequest           = errors.New("Update data 必须实现 CrudRequest")
	ErrDoUpdateTypeMismatch           = errors.New("_doUpdate: data 类型错误")
	ErrVersionFieldsNotSet            = errors.New("版本字段映射未配置")
	ErrVersionNotEnabled              = errors.New("未启用版本管理")
	ErrUpdatePairTypeMismatch         = errors.New("_doUpdate: 版本模式下 data 必须为 updatePair")
	ErrDeleteDataInvalid              = errors.New("_doDelete: data 类型错误")
	ErrRecordNotFound                       = errors.New("记录不存在")
	ErrInvalidVersionStatusTransition       = errors.New("不允许的版本状态迁移")
	ErrBatchUpdateSimpleNotSupportVersion   = errors.New("简单批量更新不支持版本化管理表")
)

// ============================================================
// 格式化错误函数
// ============================================================

// ErrQueryRecordFailed 查询记录失败
func ErrQueryRecordFailed(cause error) error {
	return fmt.Errorf("查询待更新记录失败: %w", cause)
}

// ============================================================
// 序列化/校验 — Handler 层通用
// ============================================================

func ErrMarshalEntity(cause error) error {
	return fmt.Errorf("序列化实体失败: %w", cause)
}
func ErrUnmarshalEntity(cause error) error {
	return fmt.Errorf("反序列化实体失败: %w", cause)
}
func ErrMarshalRecord(cause error) error {
	return fmt.Errorf("序列化记录失败: %w", cause)
}
func ErrUnmarshalRecord(cause error) error {
	return fmt.Errorf("反序列化记录失败: %w", cause)
}
func ErrMarshalVersion(cause error) error {
	return fmt.Errorf("序列化版本记录失败: %w", cause)
}
func ErrUnmarshalVersion(cause error) error {
	return fmt.Errorf("反序列化版本记录失败: %w", cause)
}
func ErrMarshalEditVersion(cause error) error {
	return fmt.Errorf("序列化编辑版本结果失败: %w", cause)
}
func ErrUnmarshalEditVersion(cause error) error {
	return fmt.Errorf("反序列化编辑版本结果失败: %w", cause)
}
func ErrReqValidation(idx int, cause error) error {
	return fmt.Errorf("请求[%d]校验失败: %w", idx, cause)
}
func ErrUpdateReqValidation(idx int, cause error) error {
	return fmt.Errorf("更新请求[%d]校验失败: %w", idx, cause)
}
func ErrFieldValidation(field, reason string) error {
	return fmt.Errorf("字段[%s] %s: %w", field, reason, errFieldValidationSentinel)
}

// ErrMissingParam 创建缺失参数错误。
// 供 Handler/Service 层在必传参数未提供时返回，前端可精确得知缺少哪个参数。
func ErrMissingParam(param string) error {
	return fmt.Errorf("缺少必需参数: %s: %w", param, errMissingParamSentinel)
}

// IsMissingParam 检查是否为缺失参数错误（供 mapServiceError 等使用 errors.Is 匹配）。
func IsMissingParam(err error) bool {
	return errors.Is(err, errMissingParamSentinel)
}

// IsFieldValidation 检查是否为字段校验错误（供 mapServiceError 等使用 errors.Is 匹配）。
func IsFieldValidation(err error) bool {
	return errors.Is(err, errFieldValidationSentinel)
}
func ErrParsePublishedVersion(cause error) error {
	return fmt.Errorf("解析正式发布版本失败: %w", cause)
}

// ============================================================
// 级联操作 — Handler 层
// ============================================================

func ErrCascadeCreate(handlerName string, cause error) error {
	if cause == nil {
		return nil
	}
	return fmt.Errorf("级联创建%s失败: %w", handlerName, cause)
}
func ErrCascadeUpdate(handlerName string, cause error) error {
	if cause == nil {
		return nil
	}
	return fmt.Errorf("级联更新%s失败: %w", handlerName, cause)
}
func ErrCascadeUpdateBackfill(handlerName string, cause error) error {
	if cause == nil {
		return nil
	}
	return fmt.Errorf("级联更新%s失败（回填旧子数据）: %w", handlerName, cause)
}
func ErrCascadeUpdateCleanup(handlerName string, cause error) error {
	if cause == nil {
		return nil
	}
	return fmt.Errorf("级联更新%s失败（清理旧子记录）: %w", handlerName, cause)
}
func ErrCascadeDelete(handlerName string, cause error) error {
	if cause == nil {
		return nil
	}
	return fmt.Errorf("级联删除%s失败: %w", handlerName, cause)
}
func ErrCascadeActivateQuery(handlerName string, cause error) error {
	if cause == nil {
		return nil
	}
	return fmt.Errorf("级联激活查询%s失败: %w", handlerName, cause)
}
func ErrCascadeActivate(handlerName string, cause error) error {
	if cause == nil {
		return nil
	}
	return fmt.Errorf("级联激活%s失败: %w", handlerName, cause)
}
func ErrCascadeEditVerQuery(handlerName string, cause error) error {
	if cause == nil {
		return nil
	}
	return fmt.Errorf("级联编辑版本查询%s失败: %w", handlerName, cause)
}
func ErrCascadeEditVer(handlerName string, cause error) error {
	if cause == nil {
		return nil
	}
	return fmt.Errorf("级联编辑版本%s失败: %w", handlerName, cause)
}

// ============================================================
// 引用/级联展开 — Handler 层
// ============================================================

func ErrRefResolve(handlerName string, cause error) error {
	if cause == nil {
		return nil
	}
	return fmt.Errorf("向上级联解析 %s 失败: %w", handlerName, cause)
}
func ErrRefBatchResolve(handlerName string, cause error) error {
	if cause == nil {
		return nil
	}
	return fmt.Errorf("向上级联批量解析 %s 失败: %w", handlerName, cause)
}
func ErrChildRefResolve(handlerName string, cause error) error {
	if cause == nil {
		return nil
	}
	return fmt.Errorf("向下引用批量解析 %s 失败: %w", handlerName, cause)
}
func ErrChildRefBatchResolve(handlerName string, cause error) error {
	if cause == nil {
		return nil
	}
	return fmt.Errorf("向下引用批量解析 %s 失败: %w", handlerName, cause)
}
func ErrCascadeQuery(handlerName string, cause error) error {
	if cause == nil {
		return nil
	}
	return fmt.Errorf("向下级联查询 %s 失败: %w", handlerName, cause)
}
func ErrCascadeBatchQuery(handlerName string, cause error) error {
	if cause == nil {
		return nil
	}
	return fmt.Errorf("向下级联批量查询 %s 失败: %w", handlerName, cause)
}

// ============================================================
// Repository 层
// ============================================================

func ErrCurrentVersionQuery(cause error) error {
	return fmt.Errorf("当前版本不存在: %w", cause)
}

// ============================================================
// 数据库/基础设施
// ============================================================

func ErrLoggerCreateDir(cause error) error {
	return fmt.Errorf("创建日志目录失败: %w", cause)
}
func ErrRedisConnect(cause error) error {
	return fmt.Errorf("连接Redis失败: %w", cause)
}
func ErrMySQLConnect(cause error) error {
	return fmt.Errorf("连接MySQL失败: %w", cause)
}
func ErrDBInstance(cause error) error {
	return fmt.Errorf("获取数据库实例失败: %w", cause)
}
func ErrDBCharset(cause error) error {
	return fmt.Errorf("设置数据库字符集失败: %w", cause)
}
func ErrDBModifyCharset(cause error) error {
	return fmt.Errorf("修改数据库字符集失败: %w", cause)
}
func ErrAutoMigrate(cause error) error {
	return fmt.Errorf("AutoMigrate 失败: %w", cause)
}

func ErrMongoDBConnect(cause error) error {
	return fmt.Errorf("连接MongoDB失败: %w", cause)
}
func ErrMongoDBPing(cause error) error {
	return fmt.Errorf("MongoDB Ping失败: %w", cause)
}

func ErrConfigRead(cause error) error {
	return fmt.Errorf("读取配置文件失败: %w", cause)
}
func ErrConfigParse(cause error) error {
	return fmt.Errorf("解析配置文件失败: %w", cause)
}

func ErrInitMySQL(cause error) error {
	return fmt.Errorf("初始化MySQL失败: %w", cause)
}
func ErrInitMongoDB(cause error) error {
	return fmt.Errorf("初始化MongoDB失败: %w", cause)
}
func ErrInitRedis(cause error) error {
	return fmt.Errorf("初始化Redis失败: %w", cause)
}
func ErrCloseMySQL(cause error) error {
	return fmt.Errorf("关闭MySQL失败: %w", cause)
}
func ErrCloseMongoDB(cause error) error {
	return fmt.Errorf("关闭MongoDB失败: %w", cause)
}
func ErrCloseRedis(cause error) error {
	return fmt.Errorf("关闭Redis失败: %w", cause)
}
