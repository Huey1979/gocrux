# gocrux 项目 CLAUDE.md

> 自研 Go 泛型 CRUD 框架。heims 低代码平台的核心依赖。
> 更新于 2026-07-07

---

## 1. 项目概述

gocrux 是一个基于 Go 泛型的 CRUD 框架，提供统一的数据操作管线：
`Handler → Service → Repository`。

### 1.1 核心能力

- **泛型 CRUD**：`GenericHandler[M]` + `GenericService[M]` + `CRUDRepository[M]`
- **版本管理**：草稿/发布/废弃/归档状态机，支线版本切换
- **级联操作**：父→子 CascadeRelation（Create/Update/Delete），子→父 ReferenceRelation（Get 展开）
- **钩子系统**：Before/Do/After 三层钩子，Handler 层 + Service 层
- **双存储**：MySQL（GORM `CRUDRepository`）+ MongoDB（`MongoCRUDRepository`）
- **事务编排**：`TxCoordinator` 跨 Handler 事务

### 1.2 项目结构

```
gocrux/
├── handler/          # HTTP Handler 层：管线编排、级联、展开、校验、Trace
├── service/          # Service 层：CRUD 业务逻辑、版本管理
├── repository/       # Repository 层：CRUDRepository(MySQL)/MongoCRUDRepository/RawList
├── errors/           # 哨兵错误定义
├── constants/        # 业务状态码
├── common/           # ULID、reflect（SetFieldValue 增强版）、事务上下文
├── expression/       # 表达式求值引擎（JSON 格式运算符）
├── internal/         # 内部工具：bootstrap/config/database/logger/middleware/router
├── doc/              # 项目设计文档（CODE_INDEX.md/INDEX.md/CONTEXT.md 等，不入 git）
└── tools/gentity/    # 代码生成工具
```

---

## 2. 核心架构

### 2.1 管线模式

```
HTTP Request → GenericHandler
  ├── beforeXxx (hook/默认)
  ├── doXxx    (hook/默认)
  └── afterXxx  (hook/默认)
```

Handler 和 Service 各有独立钩子系统。Handler 钩子可注入自定义逻辑（如 heims 的权限校验、公式计算）。

### 2.2 三类关系

| 关系 | 方向 | 生效场景 | FK 注入方式 |
|------|------|----------|-------------|
| CascadeRelation | 父→子 | Create/Update/Delete | `child[FKField] = parentPK` |
| ReferenceRelation | 子→父 | 仅 Get 展开 | `out[ResultField] = resolve(FK)` |
| ChildRefRelation | 父→子(FK列表) | 仅 Get 展开 | 批量解析 FK 列表 |

FKField 支持**点分路径**（`fields.parentUlid`），setByPath/getByPath 自动穿越嵌套 map。

### 2.3 HandlerConfig 完整配置

| 配置 | 说明 |
|------|------|
| `PathPrefix` | 路由前缀 |
| `Cascades` | 级联子表声明（向下） |
| `References` | 向上引用声明 |
| `ChildRefs` | FK 列表引用声明 |
| `ReqFactory` | 请求类型工厂 |
| `Auth` | 认证钩子（需 gin→context bridge） |
| `Perm` | 权限钩子 |
| `MaxExpandDepth` | 展开深度上限 |
| `FieldDepthLimits` | 单字段深度上限 |
| `FieldStopRules` | 字段级截止规则 |
| `ResponseMapper` | Entity→DTO 响应映射 |
| `ListSkipFields` | List 黑名单字段 |
| `ListKeepFields` | List 白名单字段 |
| `ListSkipCascades` | List 默认不展开的级联 |
| `KeywordFields` | 关键字搜索字段 |
| `Validate` | 输入校验规则 |
| `NormalizeFields` | JSON 字段自动规范化 |
| `BatchErrorMode` | 批量错误模式（all_or_nothing/collect） |
| `SkipAutoValidate` | 跳过自动校验（MongoDB 动态 schema） |
| `GlobalStore` | 内存缓存（nil=不启用） |
| `DateTimeFormat` | 日期时间格式化（空=RFC3339） |

---

## 3. 关键设计决策

| 决策 | 原因 |
|------|------|
| PKField() 返回 JSON key 非 DB 列名 | DoUpdate 级联从 JSON map 提取 PK，与 DB 列名不一致会 500 |
| Reference 不影响 Create/Update | 仅 GET 展开，Create 时 FK 值存储即可 |
| ULID 格式校验仅对 primaryKey | FK `_ulid` 字段可能存短 ID（如 "01AC001"） |
| Create 时 code 冲突返回 409 | 不静默覆盖，提示用户换 code 或 Update |
| 版本化 Delete 只标记指定版本（2026-07 修正） | 用户传 ids 时不应扩展 code 族 |
| 深拷贝后必须 SetDefaults | `_beforeUpdateVersioned` 拷贝旧值（含 deprecated），新版本需重置 |
| 点分路径 FKField | MongoDB 实体业务字段在 `fields.*` 内，FK 也需要穿透 |
| `resolveColumn` 回退到 bson tag | MongoDB 实体无 gorm tag，否则列名错误 |
| _beforeCreate 中 MergeTo 后置（2026-07 修正） | 钩子/BizRecord 传入的 created_by 等字段不应被框架默认值覆盖 |
| _doList 默认分页+排序（2026-07 新增） | 未传 page/page_size 全量返回导致问题，兜底 page=1/pageSize=20 |
| _doList 过滤非实体字段（2026-07 新增） | 前端防缓存参数（_t 等）不应作为查询条件 |
| 级联全量替换走 CREATE 非 UPDATE（2026-07 修） | 非版本化 OnUpdate 删旧子记录后，新数据应创建而非更新 |
| passToChild=CREATE 时清除旧 PK（2026-07 修） | 避免 MergeTo 用旧 PK 覆盖 SetID 的新 ULID |
| 编辑版本状态转换规则（2026-07 修正） | 4 种双向：deprecated↔published、deprecated↔abolished |
| SetFieldValue 扩展类型转换（2026-07 增） | bool↔int、T→*T、*T→T 不再静默失败 |

### 3.1 需求决策（medium_large_scale.md 反馈）

| 需求 | 决策 | 理由 |
|------|------|------|
| RawList 原生查询 | ✅ 已实现 | Repo 接口加一个方法，极低成本解决 JOIN/子查询需求 |
| 批量错误收集 | ✅ 已实现 | collect 模式收集所有校验错误后统一返回，全部通过才开事务 |
| Trace 管线日志 | ✅ 已实现 | 6 个管线入口/出口自动埋点，写入 BusinessLog |
| 跨实体引用解析 | ✅ 已实现 | 占位符 `__ref:handler:temp__` + `_temp_ref` 标记 |
| 权限深度集成 | ❌ 不做 | 框架提供 Authorizer 钩子，字段级/行级 ACL 是应用层职责 |
| 事件驱动钩子 | ❌ 不做 | 外部定时任务 + 轮询 + 缓存锁完全覆盖需求 |
| 多数据源/分库 | ❌ 不做 | 基础设施层问题，框架已支持读写分离 |
| GlobalStore 内存缓存 | ✅ 已实现 | 内置 NewMapStore()，可选替换为 Redis 等 |
| 日期时间格式化 | ✅ 已实现 | HandlerConfig.DateTimeFormat 配置，Get/List 统一格式化 |

---

## 4. 当前状态（2026-07-07）

### 4.1 2026-07 新功能

| commit | 内容 |
|--------|------|
| 系列提交 | RawList — Repo 接口新增原生 SQL/MQL 方法 |
| 系列提交 | BatchErrorMode — collect 模式批量收集校验错误（标注索引+字段名） |
| 系列提交 | Pipeline Trace — 6 个管线自动埋点（trace.go） |
| 系列提交 | 跨实体引用 — `__ref:handler:temp__` + `_temp_ref` 级联创建时解析 |
| `f4c6df8` | GlobalStore — HandlerConfig 内存缓存（sync.Map 默认实现） |
| `8edf0b5` | DateTimeFormat — Get/List 返回数据时间格式化 |

### 4.2 Bug 修复清单（2026-07，共 19 个）

| 编号 | 描述 |
|------|------|
| BUG-001 | 版本化 Delete 标记全部同 code → 仅标记指定 ULID |
| BUG-002 | MongoDB `isCurrent` bool vs int8 → 改用 true |
| BUG-003 | ReferenceRelation 点分路径 FK → getByPath |
| BUG-004 | _doDelete CRUDRepo nil → 确认无问题（全走 Repo 接口） |
| BUG-005 | Activate status 不更新 → 重写 _doActivate（分目标/非目标+Save） |
| BUG-006 | Draft 可见性 MongoDB → filterToBson 加 or_group |
| BUG-007 | Update data.data 双层嵌套 → items 数组 |
| BUG-008 | version_remark 被默认值覆盖 → 检查非空保留 |
| BUG-009 | versions 缺 total/items → 补齐 |
| BUG-010 | edit-version patches key 不匹配 → 遍历所有 key 双向匹配 |
| BUG-011 | /versions-archived 404 → 新增路由+handler |
| BUG-012 | Update item→items 数组对齐 |
| BUG-014 | Authenticator 缺 gin→context bridge 文档 |
| BUG-015 | SetFieldValue 类型不匹配静默失败 → 4 种新转换 |
| BUG-016 | _doList 缺默认分页+排序 → page=1/pageSize=20/created_at DESC |
| BUG-017 | ErrFieldValidation 非哨兵→改为哨兵+errors.Is 匹配 |
| BUG-018 | 非版本化级联更新全量替换 → passToChild=CREATE |
| BUG-019 | passToChild=CREATE 时旧 PK 致 Duplicate → 清除旧 PK+id |
| BUG-020 | MySQL JSON 列非法字符串 Error 3140 → checkFormat + deriveFieldRules 自动检测 |
| BUG-021 | 版本化更新级联子表 PK 冲突：passToChild=true 但旧 PK 未清除 → 条件从 `!passParentVersioned` 扩展为 `passToChild && hasChildren` |

---

## 5. 开发约定

- **未获许可不动代码**
- **commit 后报 hash 给用户 push，不要自己推**（GitHub 网络不稳定时）
- **文件引用必须带项目相对路径**：输出任何文件名时必须附带以项目根目录为基础的相对路径（如 `handler/generic.go:42`），禁止仅输出裸文件名（根目录文件除外）。引用 heims 项目文件时同理（如 `heims/internal/service/setup.go:58`）
- **MongoDB 实体配置**：`SkipAutoValidate: true`，FKField 用 `fields.xxx` 点分路径
- **PKField() 返回值 = JSON key，不是 DB 列名**
- **ListSkipFields 影响级联展开**：`_doList` 走 ListPipeline，配置了 Cascades 的子表不可配 ListSkipFields
- **不要用业务错误码接管 HTTP 状态码**：API 成功返回 200，错误在 JSON body 业务码表达

---

## 6. 待办事项

- [x] ReferenceRelation 支持点分路径 Field
- [x] 版本化 Delete 只标记指定版本
- [x] MongoDB `isCurrent` bool vs int8 类型匹配
- [x] `_doDelete` 版本化路径 MongoDB 适配
- [x] RawList 原生查询
- [x] 批量错误收集 BatchErrorMode
- [x] 管线 Trace 日志
- [x] 跨实体引用解析
- [x] GlobalStore 内存缓存
- [x] DateTimeFormat 日期时间格式化
- [ ] `trigger_conditions` 表达式评估（目前空实现）
- [ ] `execFunction` 函数注册表
- [ ] `shouldExpandField` 中 fieldLimit 与 depth 优先级确认
- [ ] handler/generic_util.go 超 500 行，需拆分

### 已判定不做的需求

| 需求 | 理由 |
|------|------|
| 权限深度集成（字段级/行级 ACL） | 应用层通过 Authorizer 钩子实现，不属于 CRUD 框架职责 |
| 事件驱动钩子（异步事件发布） | 外部定时任务+轮询+缓存锁替代，更可靠且零框架侵入 |
| 多数据源/分库 | 基础设施层问题，框架已有读写分离足够 |

---

## 7. 常见坑

1. **ListSkipFields + Cascades**：级联展开走 `_doList`，被跳过的字段在子数据中也会缺失
2. **PKField vs DB 列名**：`DoUpdate` 用 PKField 从 JSON map 取 PK，必须匹配 JSON key
3. **mergeByJSON 覆盖**：请求中的空字符串会覆盖 SetDefaults 生成的值（已修）
4. **vendor 与 go.mod 不同步**：heims 使用 vendor 模式，改 gocrux 后需同步 vendor
5. **伪版本时间戳**：commit 后 push 到 GitHub 的时间戳才是 canonical
6. **点分路径在 bson.M 中**：`bson.M{"fields.parentUlid": val}` 是 MongoDB 原生语法，无需特殊处理
7. **Authenticator 缺少 gin→context bridge**：`Middleware()` 设置 `gin.Context`，但 `GetUserULID(ctx)` 从 `context.Context` 读取。必须用 `context.WithValue(c.Request.Context(), service.CtxKeyUserULID, ulid)` 桥接
8. **_beforeCreate 执行顺序**：`SetID → SetCreatedBy → SetCreatedAt → SetUpdatedAt → MergeTo`，MergeTo 最后执行确保钩子写入的字段不被框架覆盖
9. **级联全量替换**：非版本化 `OnUpdate` 先 `DoDeleteByFK` 删旧子，再 `passToChild=true` 走 CREATE，且清除子数据旧 PK
10. **edit-version 状态转换规则**：`deprecated↔published`、`deprecated↔abolished`（4 种双向）。`draft→published` 必须用 Activate API

---

## 8. 长期记忆

### 8.1 项目定位与作者

- 作者：Huey1979，GitHub: `https://github.com/Huey1979/gocrux.git`
- 主分支：`master`
- 配套项目：heims 低代码平台（`F:\labvoyage\go_project\heims`）
- 配套工具：`tools/gentity/` MySQL→Go 代码生成器

### 8.2 技术栈

Go 1.20、Gin、GORM、MongoDB driver、Redis、Logrus、Validator

### 8.3 HTTP 约定

所有业务场景统一返回 HTTP 200，业务结果通过 `code` 字段区分：
- 成功：200
- 数据不存在：404
- 参数校验失败：4002
- 服务器内部异常：500

### 8.4 展开/级联控制

五维控制：depth（深度）/ ignore（忽略）/ visited（防环）/ fieldLimits（字段级深度）/ stopRules（截止规则）。均通过 context 传递。

### 8.5 日志系统

双通道：全局 logrus（config.yaml 控制）+ 运行时追踪日志（按天滚动，RequestLog/ResponseLog/BusinessLog）。

### 8.6 文档索引

- `doc/CONTEXT.md` — 项目上下文总览（新人第一份文档）
- `doc/CODE_INDEX.md` — 每个 .go 文件的用途
- `doc/INDEX.md` — 设计文档目录索引
- `doc/FEEDBACK.md` — 对 heims 用户需求的反馈
- `doc/bug_report.md` — Bug 跟踪（待修/已修）
- `doc/medium_large_scale.md` — 中大型项目需求原始文档
- `doc/feature_global_store.md` / `doc/feature_global_store_response.md` — GlobalStore 需求与回应
