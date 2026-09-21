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
// 发布痕迹 — Service 层（REQ edit-version publish trace）
// ============================================================

// ErrPublishTraceNotSupported 实体声明了发布痕迹（VersionFields.PublishedAtField /
// PublishedByField）但两者都未配置，无法留痕。
//
// 出现即配置错误（fail-fast），仅在发布痕迹写入口触发，不阻塞主流程：
// 调用方按「尽力而为」记 Error 日志并继续。
var ErrPublishTraceNotSupported = errors.New("实体未配置发布痕迹字段（published_at / published_by）")

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
// 版本化级联引用重映射 — Handler 层（cascade reference remap）
// ============================================================

var (
	// ErrRemapUnresolved 版本重建后子记录的引用无法解析到本批次目标。
	//
	// 刻意让事务失败而非静默保留旧 ULID：静默会把「跨版本悬挂引用」落库，
	// 表现为发布成功、运行时却关联旧字段/旧节点/旧分支。
	ErrRemapUnresolved = errors.New("级联引用重映射失败：引用目标不在本批次内")

	// ErrRemapInconsistent 引用的 ULID 与 code 同时存在但指向不同目标。
	// 不静默取其一 —— 二者不一致本身说明数据有问题。
	ErrRemapInconsistent = errors.New("级联引用重映射失败：ULID 与 code 指向不一致")

	// ErrRemapInvalidConfig 重映射声明配置非法（未知形态、缺必填键等）。
	ErrRemapInvalidConfig = errors.New("级联引用重映射配置错误")

	// ErrRemapSourceMissing 消费方请求的命名空间在本事务中**尚无发布方**（v2，L3）。
	//
	// 与 ErrRemapUnresolved 严格区分：
	//   - ErrRemapSourceMissing：映射本身拿不到（发布方 Handler 未执行 / 声明顺序错 / key 拼错）；
	//   - ErrRemapUnresolved：映射拿到了，但当前这个 ULID/code 不在映射中（值的问题）。
	//
	// 两者在 errors.Is 层可精确区分，便于把「配置/时序错误」与「数据错误」分开排查。
	ErrRemapSourceMissing = errors.New("级联引用重映射失败：请求的命名空间尚无发布方")
)

// ============================================================
// 引用装配 — Handler 层（reference assembly，v3）
//
// 与上面的 v2 重映射错误并列而非替换：两套机制在迁移期共存
// （阶段 1 新增装配通道、旧机制保留，见设计文档 §8.1）。
// ============================================================
var (
	// ErrAssemblyAmbiguous 装配命中多条目标记录。
	//
	// 与 v2 的 ErrRemapInconsistent 同哲学但语义不同：
	//   - ErrRemapInconsistent：映射表里同一 key 被映射到不同目标（发布侧冲突）；
	//   - ErrAssemblyAmbiguous：**查询**时命中多条（匹配键在目标集合里不唯一）。
	//
	// 不可容忍（设计文档 §6.2）：匹配不到是「数据形态」（可 WARN 后保留旧值），
	// 命中多条则说明「匹配键唯一」这一前提被破坏，结果不确定。
	// heims 已确认同一 form 版本内 field_code 唯一（§13.2 B6），故报错正确。
	ErrAssemblyAmbiguous = errors.New("引用装配失败：匹配到多条目标记录")

	// ErrAssemblyInvalidConfig 装配声明配置非法（缺 Match/Assign、多层数组等）。
	//
	// 构造期经 validateAssemblies 拦截并 panic（fail-fast，B7 与 BUG-071 同哲学）；
	// 运行时此哨兵用于「Target 需要请求级索引但 context 中没有注册表」等
	// 「配置本身没错、但调用路径不具备前置条件」的场景。
	ErrAssemblyInvalidConfig = errors.New("引用装配配置错误")

	// ErrAssemblyIdentityAmbiguous 版本化回填时「旧子记录 ↔ 预分配项」无法唯一对应。
	//
	// 设计文档 §5.2①（应用方 §13.4）：回填后清 PK、填预分配 ULID 时，
	// 旧 A 必须对应新 A。若仅按**位置**对应，会在「子表顺序变化 / 存在软删行」
	// 时把旧 A 对应到新 B —— 且静默错位。故无法唯一对应时**报错，不猜测**。
	ErrAssemblyIdentityAmbiguous = errors.New("版本化回填失败：旧子记录与预分配项无法唯一对应")

	// ErrAssemblyPKConflict 预分配的主键与库中已有记录冲突（概率 ≈ 1.2e-24）。
	//
	// 处理策略（设计文档 §7.2/§7.3）：
	//   - 有事务部署：整树回滚 + 重试 1 次（由 TxCoordinator 封装）；
	//   - 无事务部署：不重试，报错 + 尽力而为标记本次写入的记录 is_deleted=1 + ERROR 日志。
	//
	// 错误文案明确提示「请重新发起整个请求」——重放同一请求体是不行的
	// （会复用旧 ticket 与旧 ULID，见 §7.4）。
	ErrAssemblyPKConflict = errors.New("主键冲突：预分配的 ULID 与已有记录冲突，请重新发起整个请求")
)

// PKConflictError 包装底层主键冲突错误，保留原始错误链并统一为框架哨兵。
//
// 为什么用包装而非直接返回哨兵：排查时需要看到底层是 MySQL 1062 还是
// Mongo E11000、冲突的是哪个索引 —— 只报「主键冲突」会丢失这些线索。
// errors.Is(err, ErrAssemblyPKConflict) 仍可命中。
func PKConflictError(cause error) error {
	if cause == nil {
		return ErrAssemblyPKConflict
	}
	return fmt.Errorf("%w: %v", ErrAssemblyPKConflict, cause)
}

// ============================================================
// 外部引用解析 — Handler/Service 层（external reference，应用方 §19.4 / §20.5）
//
// 与 v3 装配（请求树**内部**引用）的分工：
//
//	装配（Assemblies）：引用的目标在同一请求树里「本次新建」，ULID 由预分配产出；
//	外部引用（本节）  ：引用的目标**已经存在**（如 data_select 选中的表单/流程），
//	                    不做装配，而是「按 code 解析权威版本 → 校验 → 按策略持久化」。
//
// 因此二者机制不同、互不替代（应用方 §20.5 的判断正确）。
// ============================================================
var (
	// ErrExternalRefNotFound 外部引用目标不存在（或已软删，不可作为引用目标）。
	ErrExternalRefNotFound = errors.New("外部引用目标不存在")

	// ErrExternalRefStale 调用方提交的 ULID 与「按 code 解析出的权威版本」不一致。
	//
	// 语义（应用方 §20.3 第 3 条）：前端提交的是快照意图（ulid + code），
	// 但服务端按 code 查到的权威 ULID 已经变了 —— 说明引用的是旧版本。
	// 必须让调用方重新选择，绝不静默改成新版本（那会悄悄改变业务语义）。
	ErrExternalRefStale = errors.New("外部引用目标版本已变化，请重新选择")

	// ErrExternalRefTargetMissing 目标 Handler 未注册（或未实现外部引用解析能力）。
	ErrExternalRefTargetMissing = errors.New("外部引用目标 Handler 未注册")

	// ErrExternalRefInvalidParam 解析参数不合法（未给 code/ULID、模式未知、缺注册表等）。
	ErrExternalRefInvalidParam = errors.New("外部引用解析参数不合法")
)

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
