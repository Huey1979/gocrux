# gocrux 代码文件索引

> 按包（package）列出每个 `.go` 源文件的路径和用途。测试文件（`*_test.go`）已排除。

---

## handler/ — HTTP 处理层

| 文件 | 用途 |
|------|------|
| `generic.go` | **核心定义**——`GetRequest`、`HandlerConfig`（Handler 配置，含路由前缀/级联关系/认证/权限/展开深度/字段裁剪/校验规则等所有字段）、`GenericHandler[M]` 泛型 Handler 结构体、`NewGenericHandler` / `NewGenericHandlerWithSvc` 构造函数、`initValidation` 校验规则合并、setter 方法 |
| `generic_impl.go` | **内置默认实现**——`expandCascadesBatch`（List 批量展开级联子记录）、`forEachCascade` / `forEachCascadeChild`（级联遍历辅助） |
| `generic_read.go` | **读操作入口**——`DoGetByID` / `Get`（详情查询管线）、`getPipeline` / `beforeGet` / `doGet` / `afterGet`、`expandGet`（Get 展开 References/ChildRefs/Cascades）、`deriveRefResultKey` / `deriveChildRefResultKey`（结果键名推导）、`DoList` / `List`（列表查询管线）、`listPipeline` / `beforeList` / `doList` / `afterList` |
| `generic_read_impl.go` | **读操作默认实现**——`_beforeGet` / `_doGet` / `_afterGet`、`_beforeList` / `_doList`（List 核心逻辑：参数解析→批量展开 References/ChildRefs/Cascades→字段裁剪→ListSkipCascades/expand 控制）、`_afterList` |
| `generic_write.go` | **写操作入口**——`PKField`、`DoCreate` / `Create`（创建管线）、`createPipeline` / `beforeCreate` / `doCreate` / `afterCreate`、`DoUpdate` / `Update`（更新管线）、`updatePipeline` / `beforeUpdate` / `doUpdate` / `afterUpdate`、`DoDelete` / `DoDeleteByFK` / `Delete`（删除管线）、`deletePipeline` / `beforeDelete` / `doDelete` / `afterDelete` |
| `generic_write_impl.go` | **写操作默认实现**——`_beforeCreate` / `_doCreate`（创建：事务包装→父实体创建→级联子实体创建→级联激活）、`_beforeUpdate` / `_doUpdate`（更新：非版本化原地更新↔版本化新旧替换→级联子实体全量替换）、`_beforeDelete` / `_doDelete`（删除：code→ULID 解析→级联先删子→后删父）、`shouldShortCircuitCascade`（级联防环/深度短路检查）、`buildCascadeCtx`（统一构建级联 context） |
| `generic_version.go` | **版本操作入口**——`DoActivate` / `Activate`（激活版本管线）、`DoListVersions` / `ListVersions`（版本历史管线）、`DoEditVersion` / `EditVersion`（编辑版本管线），及各管线对应的 before/do/after 方法 |
| `generic_version_impl.go` | **版本操作默认实现**——`_beforeActivate` / `_doActivate`（退位当前版本→目标版本即位→级联激活子）、`_beforeListVersions` / `_doListVersions`、`_beforeEditVersion` / `_doEditVersion`（状态迁移校验→字段更新） |
| `generic_util.go` | **Handler 工具函数集**——`newCrudRequest` / `newCrudRequestForUpdate` / `newListRequest`（请求构造）、`RegisterRoutes`（路由注册，版本化模式下额外注册 activate/versions/edit-version）、`userInfo` / `checkPerm`（认证权限）、`injectDepth` / `injectIgnore` / `injectStop`（HTTP 参数→context 注入）、`getStopCfg` / `buildFieldCtx`（字段级深度/截止控制）、`effectiveExpandDepth` / `withEffectiveChildCtx`、`extractChildData`（子数据拆解，含 ChildrenWrapKey 包裹）、`applyResponseMapper`（Entity→DTO 映射+级联数据合并）、`shouldExpandCascade`（ListSkipCascades/expand 优先级判定）、`handleError`（统一错误处理）、`normalizeFields`（表达式规范化） |
| `cascade.go` | **展开/级联控制核心**——depth/ignore/visited/fieldLimit 四个 context key 及全套工具函数（`withDepth`/`getDepth`、`withIgnore`/`getIgnore`/`shouldIgnore*`、`addVisited`/`isVisited`、`withFieldLimits`/`getFieldLimits`）、`canExpandTo`（统一前置检查）、`shouldExpandField`（字段级展开判断）、`StopRule` 及解析器（`parseStopRule`/`parseStopRules`）、`splitN`、`CascadeHandler` 接口（子 Handler 通过该接口被父 Handler 调用）、`CascadeRelation` / `ReferenceRelation` / `ChildRefRelation` 三种关系声明 |
| `hooks.go` | **HandlerHooks 钩子类型定义**——每个 CRUD 操作对应 before/do/after 三个钩子字段 |
| `registry.go` | **HandlerRegistry 注册表**——基于 `common.Registry[*GenericHandler]` 泛型注册表，管理所有 Handler 实例，级联时通过名称查找子 Handler |
| `txcoordinator.go` | **TxCoordinator 事务编排器**——编排 MySQL（GORM）和 MongoDB 事务，支持 `Run` / `RunMySQL` / `RunMongo` |
| `request.go` | **请求构造器**——`RequestFactory`（按操作区分 Create/Update/List 请求类型）、`MapRequest[M]`（默认 map 请求实现，含 GetID/GetIdempotencyKey/MergeTo/Validate）、`mergeByJSON` / `mergeMapToStructFlat`（map→struct 映射） |
| `request_util.go` | **请求绑定工具**——`BindJSON`（请求体 JSON 绑定+字段补全）、`BindQuery`（查询参数绑定）、`GetPageParams`（分页参数提取+默认值）、`GetCurrentUserULID` |
| `response.go` | **HTTP 响应封装**——`Response` 结构体、`Success` / `SuccessWithMessage` / `Error` / `ErrorWithMsg` / `ErrorWithCode` / `InternalError` |
| `auth_hooks.go` | **认证授权接口**——`UserInfo` 结构体、`Authenticator` 接口（认证中间件+上下文提取）、`Authorizer` 接口（权限校验） |
| `errors.go` | **错误映射**——`mapServiceError`（Service 层错误→HTTP BusinessCode 映射） |
| `fields.go` | **字段裁剪工具**——`pruneFields` 按自定义规则语法（`key:subs` / `key:[a,b]`）裁剪 map 数据，用于 List 字段白名单裁剪；`splitRules`（委托 `common.SplitAndTrim`）/ `splitRule` / `parseRule` 辅助函数 |
| `path.go` | **点分路径读写**——`setByPath` / `getByPath` 按 `a.b.c` 点分路径读写嵌套 map，自动创建/穿越中间层，支撑 CascadeRelation 中 FKField 点分路径注入 |
| `trace.go` | **管线追踪日志**——`traceNode` 结构化记录管线节点日志（含 request_id），`traceStart` / `traceEnd` 记录入口/出口+耗时（ms），6 个管线自动埋点依赖此模块 |
| `utils.go` | **通用工具**——`extractPKFromResult`（从结果提取主键）、`equalFieldName`（大小写不敏感字段名比较）、`extractMapID` / `removeMapID` |
| `validation.go` | **输入校验核心**——`FieldRule` / `EndpointRules` / `ValidateConfig` 类型定义、`deriveFieldRules`（反射 gorm 标签自动推导规则）、`coerceValue` 系列宽松类型转换函数、`checkFormat` 内置格式校验（datetime/date/time/email/url/phone/ulid）、`validateField`（单字段校验核心，被 `validateInput` / `validateInputCollect` 复用）、`mergeRules` 规则合并 |
| `validation_config.go` | **校验规则 YAML 加载**——`LoadValidationConfig`（从 YAML 文件加载 `map[handlerName]*ValidateConfig`） |

---

## service/ — 业务逻辑层

| 文件 | 用途 |
|------|------|
| `generic.go` | **核心定义**——`Record` 接口（实体约束）、`VersionStatus` 常量（draft/published/deprecated/abolished）、`CtxKeyUserULID` / `CtxKeyRequestID` context key、`GetUserULID` / `GetRequestID`、`VersionFieldMapping` 版本字段映射、`Config[M]` Service 配置（含 VersionMode/DeletedField/DeletedValue/UniqueFields 等）、`GenericService[M]` 泛型服务结构体、`NewGenericService` / `NewGenericServiceWithRepo` 构造函数、全部 setter（SetHooks/SetOpLogRepo/SetBakWriter/SetIdemStore）、全部公开方法（Create/Update/Delete/Get/GetByCode/List/Activate/ListVersions/EditVersion）、`ResolveToPublished` / `ResolveOneToPublished`（批量/单条版本解析） |
| `generic_impl.go` | **内置默认实现**——`KeywordSearch` 结构体、`WithKeywordSearch` context 注入、全部 before 钩子（创建/更新/删除/激活前默认行为：唯一性校验、幂等检查、字段合并、状态校验） |
| `generic_read_impl.go` | **读操作实现**——`_doGet` / `_doGetByCode`、`_doList`（核心：ListFilters/map/默认三种入参兼容→关键字搜索→版本化过滤→草稿可见性→软删除过滤→分页查询） |
| `generic_write_impl.go` | **写操作实现**——`_doCreate`（事务包装→唯一性校验→批量插入→级联创建通知）、`_doUpdate`（非版本化原地更新↔版本化新旧替换+版本号递增+状态判定）、`_beforeUpdateVersioned`（深拷贝旧数据→合并请求字段→生成新 ID/版本号→确定初始状态）、`_doDelete`（版本化批量废弃↔非版本化批量软删除+备份）、`checkUnique`（统一唯一性校验，Create/Update 共用） |
| `generic_version_impl.go` | **版本操作实现**——`_doActivate`（退位→即位→状态更新）、`_doListVersions`、`_doEditVersion`（状态迁移校验+字段更新） |
| `hooks.go` | **ServiceHooks 钩子类型定义**——与 Handler 层对称，每个操作对应 before/do/after 三个钩子 |
| `registry.go` | **ServiceRegistry 注册表**——基于 `common.Registry[GenericService]` 泛型注册表，管理所有 Service 实例，`GetTyped[M]` 按名称+类型获取，用于 Handler 构造时自动关联 |
| `request.go` | **请求接口定义**——`CrudRequest[M]` 接口（GetID/MergeTo/Validate）、`Identifiable` / `Mergeable` / `Validatable` 组合接口 |
| `idempotency.go` | **幂等支持**——`IdempotencyStore[M]` 内存幂等缓存（TTL 过期）、`extractIdemKey` 提取幂等键 |
| `tx.go` | **事务透传**——`GetTx`（从 context 获取 GORM 事务） |

---

## repository/ — 数据访问层

| 文件 | 用途 |
|------|------|
| `crud.go` | **泛型 GORM 仓储核心**——`CRUDRepository[M]` 泛型仓储（读写分离/主键推导/基础 CRUD/批量操作/事务/分页列表）、`FilterOp` 操作符（eq/neq/like/gt/gte/lt/lte/in/between/raw/or_group）、`Filter` / `ListFilters` 结构化过滤、`ListByFilters` / `ListWhere` / `ListAll` / `ListAllByField`、`Count` / `RawQuery`、`Transaction` / `RunInTx`、全部 `Batch*` 方法（SoftDelete/FindByPK/FindByFK/HardDelete/DeprecateVersions）、`detectPK` 主键自动推导（反射 gorm 标签，使用 `common.ToSnakeCase` 驼峰转下划线） |
| `repo.go` | **Repo[M] 统一仓储接口**——定义 CRUDRepository（MySQL）与 MongoCRUDRepository（MongoDB）共同实现的接口，GenericService 依赖此接口而非具体实现 |
| `mongo_repo.go` | **MongoDB 泛型仓储**——`MongoCRUDRepository[M]` 提供与 CRUDRepository 一致的 CRUD 接口，支持读写分离（`DefaultReadCollProvider`）、结构化过滤转 bson（`toBsonFilter`/`filterToBson`）、事务（`RunInTx` 基于 MongoDB Session）、`detectPK` 主键推导（使用 `common.ExtractGormColumn`） |
| `base.go` | **非泛型仓储（旧版兼容）**——`Repository` 接口、`BaseRepository`、`VersionRepository`（原生 MySQL 版本管理，含 CreateVersion/PublishVersion/RollbackVersion）、`VersionConfig`、`VersionError`、哨兵错误 |
| `dao.go` | **DAO 扩展层**——`DAO` / `BaseDAO`（在基础 CRUD 之上预留缓存、审计日志扩展点）、`DAOError` |
| `store.go` | **GlobalStore 内存缓存**——`GlobalStore` 接口定义（Get/Set/Del）、`MapStore` 基于 `sync.Map` 的内置实现、`NewMapStore()` 构造函数，框架在 CRUD 管线中自动维护缓存 |

---

## common/ — 通用工具

| 文件 | 用途 |
|------|------|
| `ulid.go` | **ULID 生成器**——`NewULID()` 生成 26 位时间排序的唯一 ID |
| `reflect.go` | **反射辅助**——`SetFieldValue`（通过反射设置结构体字段值）、`SetReflectField`（通过 reflect.Value 设置字段值，支持更多类型转换） |
| `conv.go` | **跨包公共转换函数**——`ParseInt`（字符串→int）、`ToSnakeCase`（驼峰→下划线）、`ExtractGormColumn`（gorm 标签中提取列名）、`SplitAndTrim`（通用分割+去空）、`IsSlice`（反射判断是否为切片类型）、`Registry[T]`（泛型注册表，统一 Handler/Service 注册器） |
| `tx.go` | **事务 context 传递**——`WithTx` / `GetTx`（GORM 事务向 context 注入/提取）、`WithMongoSession` / `GetMongoSession`（MongoDB Session inject/extract） |

---

## constants/ — 业务状态码

| 文件 | 用途 |
|------|------|
| `code.go` | **HTTP 响应码**——`BusinessCode` 类型定义及全部业务状态码常量（CodeSuccess/CodeNotFound/CodeParamError/CodeInternalError 等），以及对应的消息映射 |

---

## errors/ — 错误定义

| 文件 | 用途 |
|------|------|
| `errors.go` | **哨兵错误+格式化错误函数**——`ErrRecordNotFound`、`ErrInvalidParam`、`ErrVersionFieldsNotSet` 等基础错误；`ErrQueryRecordFailed`、`ErrCascadeCreate`、`ErrCascadeDelete`、`ErrCascadeUpdate` 等级联错误包装函数（支持错误链 nil cause 保护） |

---

## expression/ — 表达式规范化

| 文件 | 用途 |
|------|------|
| `normalizer.go` | **JSON 表达式规范化**——将旧格式操作符表达式（`{"op":"Add",...}`）自动升级为新格式（`{"type":"expression","expression":{...}}`），统一规范化的表达式中操作数排序（commutative 函数保证参数顺序无关） |

---

## internal/ — 框架内部

### internal/bootstrap/

| 文件 | 用途 |
|------|------|
| `bootstrap.go` | **启动引导**——`Init`（完整初始化：config→log→mysql→mongo→redis→migrate）、`InitMySQL` / `InitOther` / `Migrate` / `Close` 分步初始化函数 |

### internal/config/

| 文件 | 用途 |
|------|------|
| `config.go` | **YAML 配置加载**——`Config` 结构体（app/mysql/mongodb/redis/log/security/storage 各段）、`Load` 函数 |

### internal/database/

| 文件 | 用途 |
|------|------|
| `mysql/mysql.go` | **MySQL 连接管理**——`DB` 全局 GORM 实例、`Init` 初始化连接池、`AutoMigrate` 自动建表、连接池参数配置 |
| `mysql/migration.go` | **数据库迁移**——DDL 变更、类型校验等迁移逻辑 |
| `mongodb/mongodb.go` | **MongoDB 连接管理**——`Database` 全局实例、`Init` 初始化连接、连接池配置 |
| `redis/redis.go` | **Redis 连接管理**——`Client` 全局实例、`Init` 初始化、连接池配置 |

### internal/logger/

| 文件 | 用途 |
|------|------|
| `logger.go` | **运行时追踪日志**——`RequestLog` / `ResponseLog` / `BusinessLog` 三个按天滚动日志实例、`Init` 初始化（从 stderr 切换为按天文件）、`LogRequest` / `LogResponse` / `LogBusiness` 日志函数（含 nil guard 安全加固） |
| `gorm.go` | **GORM 日志适配**——`GormLogger` 将 GORM SQL 日志写入 BusinessLog，支持慢查询阈值告警 |

### internal/middleware/

| 文件 | 用途 |
|------|------|
| `middleware.go` | **HTTP 中间件**——`RequestLogger`（请求日志+log_id 注入+request body 捕获+响应体捕获）、`Cors`（跨域中间件）、`Recovery`（panic 恢复） |
| `auth.go` | **认证中间件**——`DefaultAuthenticator` 全局认证器、`AuthMiddleware`（调用 Authenticator.Middleware 注入用户信息到 context） |

### internal/model/entity/

| 文件 | 用途 |
|------|------|
| `sys_operation_log.go` | **框架内置实体**——`SysOperationLog` 操作日志表模型，用于 `EnableOpLog` 时自动写入操作日志 |

### internal/router/

| 文件 | 用途 |
|------|------|
| `router.go` | **基础路由注册**——启动时注册全局中间件（Cors/Recovery/RequestLogger）和健康检查路由 |

---

## cmd/ — 入口示例

| 文件 | 用途 |
|------|------|
| `main.go` | **框架使用示例**——演示完整的启动流程：配置加载→数据库初始化→Handler 创建→路由注册→服务启动 |

---

## tools/gentity/ — 代码生成器

| 文件 | 用途 |
|------|------|
| `main.go` | **CLI 入口**——解析命令行参数（`--dsn`/`--table`/`--all`/`--check`/`--dto`/`--out` 等），启动扫描→生成流程 |
| `scanner.go` | **MySQL 表结构扫描**——连接数据库，读取 `information_schema`，解析表结构（列名/类型/主键/注释），支持 `--check` 模式检查框架约定字段缺失 |
| `generator.go` | **Go 代码生成引擎**——根据扫描结果生成 entity struct（含 gorm 标签+Record 接口实现）、blueprint（Repository→Service→Handler 注册蓝图）、DTO struct+ToDTO 方法 |
| `field_config.go` | **字段映射配置**——加载 `gentity_fields.yaml`，支持 `field_mapping`（自定义框架字段对应表列名）和 `exclude_tables`（排除表列表） |
