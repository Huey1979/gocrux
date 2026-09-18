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

	// ErrDuplicateCode 版本化实体 Create 时业务编码重复（BUG-042 判定逻辑）。
	//
	// 文案刻意保持**中性**：该哨兵被全部版本化实体共用（form / flow /
	// notification_template / container / bi_chart / site / site_menu / role …），
	// 不能写死某个实体的字段名（BUG-074：原文案写死「form_code / 表单」，
	// 在通知模板页弹出会让用户以为串到了表单模块）。
	// 需要实体专属措辞时，用 VersionFieldMapping.DuplicateCodeMsg 覆写。
	ErrDuplicateCode = errors.New("编码已存在，请更换编码，或改用更新接口修改现有记录")
)

// errFieldValidationSentinel 字段校验哨兵（不导出，通过 IsFieldValidation 检查）。
var errFieldValidationSentinel = errors.New("field validation failed")

// errMissingParamSentinel 缺失参数哨兵（不导出，通过 IsMissingParam 检查）。
var errMissingParamSentinel = errors.New("missing required parameter")

// errRefResolveSentinel 引用/级联解析失败哨兵（不导出，通过 IsRefResolveError 检查）。
//
// 存在的意义是**区分报错宾语**：ErrRefResolve / ErrRefBatchResolve /
// ErrChildRefResolve / ErrChildRefBatchResolve 都用 %w 保留原始错误链，
// 若引用目标恰好是 ErrRecordNotFound，整条错误会同时满足
// errors.Is(err, ErrRecordNotFound)，被 handler 映射成 404「本条记录不存在」——
// 而实际上是「本条记录的引用坏了」（BUG-080）。
// mapServiceError 用本哨兵先行拦截，避免引用解析失败被升格为主记录不存在。
var errRefResolveSentinel = errors.New("reference resolve failed")

// IsRefResolveError 检查是否为引用/级联解析失败错误（供 mapServiceError 等使用）。
// 注意：该判定必须在 ErrRecordNotFound 之前，因为此类错误可能同时包装了后者。
func IsRefResolveError(err error) bool {
	return errors.Is(err, errRefResolveSentinel)
}

// ============================================================
// 通用服务 (generic) — 框架内部使用
// ============================================================
var (
	ErrUpdateDataNotRequest               = errors.New("Update data 必须实现 CrudRequest")
	ErrDoUpdateTypeMismatch               = errors.New("_doUpdate: data 类型错误")
	ErrVersionFieldsNotSet                = errors.New("版本字段映射未配置")
	ErrVersionNotEnabled                  = errors.New("未启用版本管理")
	ErrUpdatePairTypeMismatch             = errors.New("_doUpdate: 版本模式下 data 必须为 updatePair")
	ErrDeleteDataInvalid                  = errors.New("_doDelete: data 类型错误")
	ErrRecordNotFound                     = errors.New("记录不存在")
	ErrInvalidVersionStatusTransition     = errors.New("不允许的版本状态迁移")
	ErrBatchUpdateSimpleNotSupportVersion = errors.New("简单批量更新不支持版本化管理表")

	// ErrSoftDeleteNotSupported 实体不支持软删除（SetDelete() 返回 false，
	// 删除走物理删 + 备份日志），因此没有"恢复"语义（BUG-069）。
	ErrSoftDeleteNotSupported = errors.New("实体不支持软删除，无法恢复")

	// ErrUseActivateInstead 版本化实体调用 restore（BUG-069 复核 P2）。
	// 版本化的「删除」= 废弃（只写 is_current=0 / version_status=deprecated，
	// 从不写 is_deleted），因此 restore 对其是静默空操作；
	// 恢复当前版本应走 activate 接口。
	ErrUseActivateInstead = errors.New("版本化实体不支持 restore：删除即废弃，请使用 activate 恢复")

	// ErrUpdateDeprecatedVersion 更新已废弃（is_current=0）的版本行（BUG-069 复核 P3）。
	// 版本化 update 会以该行为底派生 is_current=1 的新行，等于绕过 activate
	// 的钩子与审计直接"复活 + 改写 + 造新版本号"。应先 activate 再编辑。
	ErrUpdateDeprecatedVersion = errors.New("该版本已废弃，请先 activate 激活后再编辑")
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
//
// 这四个构造函数都同时包装 errRefResolveSentinel（BUG-080）：
// 目的是让 mapServiceError 能识别「引用目标解析失败」，即使 cause 链上
// 含 ErrRecordNotFound 也不映射成 404（报错宾语是引用，不是主记录）。
// 用多个 %w 同时保留哨兵与原始 cause，errors.Is 两者皆可命中。
// ============================================================

func ErrRefResolve(handlerName string, cause error) error {
	if cause == nil {
		return nil
	}
	return fmt.Errorf("向上级联解析 %s 失败: %w: %w", handlerName, errRefResolveSentinel, cause)
}
func ErrRefBatchResolve(handlerName string, cause error) error {
	if cause == nil {
		return nil
	}
	return fmt.Errorf("向上级联批量解析 %s 失败: %w: %w", handlerName, errRefResolveSentinel, cause)
}
func ErrChildRefResolve(handlerName string, cause error) error {
	if cause == nil {
		return nil
	}
	return fmt.Errorf("向下引用批量解析 %s 失败: %w: %w", handlerName, errRefResolveSentinel, cause)
}
func ErrChildRefBatchResolve(handlerName string, cause error) error {
	if cause == nil {
		return nil
	}
	return fmt.Errorf("向下引用批量解析 %s 失败: %w: %w", handlerName, errRefResolveSentinel, cause)
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

// ============================================================
// 携带业务码的错误 — 供业务校验/钩子返回自定义业务码（BUG-058）
// ============================================================

// BizError 携带业务码的错误类型。
// 用于业务校验失败需要返回自定义业务码（而非统一映射为 500）的场景，
// 例如 heims 文档中心钩子返回 15001/15002/15003 等业务错误码。
// handler.mapServiceError 通过 errors.As 识别该类型并透传业务码。
type BizError struct {
	Code int
	Msg  string
}

func (e *BizError) Error() string { return e.Msg }

// NewBizError 创建携带业务码的错误。
// 可直接作为 error 返回，也可用 fmt.Errorf("%w", ...) 嵌套包装（errors.As 仍可识别）。
func NewBizError(code int, msg string) *BizError {
	return &BizError{Code: code, Msg: msg}
}
