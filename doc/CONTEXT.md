# gocrux 项目上下文

> 模块: `github.com/Huey1979/gocrux` | Go 1.20 | MIT License

---

## 一、项目概述

gocrux 是一个 Go 语言通用 CRUD 后端框架，提供泛型化的 **Handler → Service → Repository** 三层架构，支持级联操作、版本管理和审计日志。

### 核心设计原则

- **泛型化**: `GenericHandler[M]` → `GenericService[M]` → `CRUDRepository[M]` 链条，编译时类型安全
- **管线模式**: 每个操作 (Create/Update/Delete/Get/List/Activate/EditVersion) 遵循 `before → do → after` 三段管线
- **钩子覆盖**: 任意环节钩子可被外部替换，未替换时 fallback 到内置默认实现
- **事务透明**: Handler 层通过 `TxCoordinator` 编排事务，Service 层通过 `common.GetTx(ctx)` 自动感知
- **HTTP 与业务解耦**: 所有业务场景统一返回 HTTP 200，业务结果通过响应体 `code` 字段区分（CodeSuccess/CodeNotFound/CodeParamError/CodeInternalError），绝不使用 HTTP 状态码表达业务语义

---

## 二、项目结构

```
gocrux/
├── cmd/                    # 入口示例
├── handler/                # HTTP 处理层（核心）
│   ├── generic.go          # GenericHandler 定义 + Get/List 入口 + expandGet + 深度/忽略注入
│   ├── generic_impl.go     # 内置 _doXxx 默认实现（含批量展开逻辑）
│   ├── cascade.go          # CascadeHandler 接口 + 关系声明 + 深度/忽略/visited 控制
│   ├── hooks.go            # HandlerHooks 钩子类型定义
│   ├── registry.go         # HandlerRegistry 注册表
│   ├── txcoordinator.go    # TxCoordinator 事务编排器
│   ├── request.go          # RequestFactory + MapRequest
│   ├── response.go         # HTTP 响应封装
│   └── auth_hooks.go       # Authenticator / Authorizer 接口
├── service/                # 业务逻辑层（核心）
│   ├── generic.go          # Record 接口 + GenericService + Config
│   └── generic_impl.go     # Service 默认实现
├── repository/             # 数据访问层
│   ├── crud.go             # CRUDRepository 泛型仓储 + ListFilters + FilterOp
│   └── base.go             # BaseRepository + VersionRepository
├── internal/               # 框架内部
│   ├── config/             # YAML 配置加载
│   ├── database/           # MySQL/MongoDB/Redis 连接
│   ├── logger/             # 日志系统
│   ├── middleware/          # HTTP 中间件
│   └── bootstrap/          # 启动引导
├── common/                 # 通用工具（ULID、反射、事务上下文）
├── constants/              # 业务状态码
├── errors/                 # 哨兵错误
├── tools/gentity/          # MySQL → Go 代码生成器（独立工具）
├── config.yaml             # 配置文件（不提交 git）
└── doc/                    # 项目文档（不提交 git）
```

---

## 三、关键接口与类型

### 3.1 Record 接口 (`service/generic.go`)

```go
type Record interface {
    SetDefaults()
    SetID()
    SetCreatedAt(t time.Time)
    SetCreatedBy(userID string)
    SetUpdatedAt(t time.Time)
    SetUpdatedBy(userID string)
    SupportsDraft() bool          // 是否支持版本管理
    SetDelete() bool              // true=软删除, false=物理删除
    PKField() string              // 主键数据库列名
    SelfFKField() string          // 自关联外键字段名（如 parent_ulid）；非空表示存在自关联，展开时通过深度控制+visited 防环
}
```

### 3.2 HandlerConfig 关系声明

```go
// 向上引用（当前实体 → 父实体，如 site_ulid → site）
type ReferenceRelation struct {
    Field       string // 当前实体 FK 字段
    HandlerName string // 父 Handler 注册名
    ResultField string // 结果键名（空则自动推导）
}

// 向下 FK 列表引用（如 tag_ulids → tags）
type ChildRefRelation struct {
    FKListField string // FK 列表字段名
    HandlerName string // 目标 Handler 注册名
    ResultField string // 结果键名（空则自动推导）
}

// 向下级联（创建/更新/删除时联动子实体）
type CascadeRelation struct {
    HandlerName     string // 子 Handler 注册名
    ChildrenField   string // 请求体字段名 / 结果字段名
    FKField         string // 子表外键字段名
    OnCreate        bool
    OnDelete        bool
    OnUpdate        bool
    OnActivate      bool
    FollowPublished bool   // 是否只返回正式发布版本
}
```

---

## 四、展开/级联控制机制

### 4.1 设计背景

Get/List 请求中，框架自动展开三类关联数据：
- **References**: 向上 FK 引用（逐条或批量查父实体）
- **ChildRefs**: 向下 FK 列表引用（批量查子实体）
- **Cascades**: 向下级联子数据（批量查子表）

问题场景：
1. **自关联深度控制**: 实体指向自身（如 `dept.parent_dept_ulid → dept`），需限制展开层数
2. **跨实体循环引用**: A → B → A 形成循环
3. **选择性跳过**: 调用方希望只查主表，不展开某些关联

### 4.2 自关联展开（2026-06 重构：移除硬阻断）

**背景**: 原设计通过 `isSelfRef()` 硬阻断自关联展开，导致完全无法在 References/ChildRefs/Cascades 中配置指向自身的关联（如部门层级树）。

**重构**: 移除 `selfFKField` 字段和 `isSelfRef()` 方法，自关联现在正常展开。

**Record 接口**（保留，仅作文档）:
```go
SelfFKField() string  // 返回自关联 FK 字段名，如 "parent_ulid"
```

**使用示例**:
```go
// 部门自关联：parent_dept_ulid → dept，最多展开 5 层
HandlerConfig[*entity.SysDept]{
    MaxExpandDepth: 5,
    References: []handler.ReferenceRelation{
        {Field: "parent_dept_ulid", HandlerName: "dept", ResultField: "parent"},
    },
}
```

**循环防护**（替代硬阻断）:
1. **深度控制**: `MaxExpandDepth` + `hardMaxExpandDepth(10)` 限制层数，到 0 自动停止
2. **visited 追踪**: `expandGet` 中记录 `(handlerName, recordID)` 链条，遇重复即终止
3. 即使出现环形数据（A.parent=B, B.parent=A），visited 追踪在第二轮即检测到并停止

### 4.3 跨实体循环防护（Visited 追踪链）

**类型定义** (`handler/cascade.go`):
```go
type visitedSet map[string]bool  // key = "handlerName:recordID"
```

**上下文工具**:
```go
ctxKey visitedCtxKey

// addVisited: 创建新 map（不可变语义），加入 (handlerName, id)
func addVisited(ctx context.Context, handlerName, id string) context.Context

// isVisited: 检查当前记录是否已在展开链中出现过
func isVisited(ctx context.Context, handlerName, id string) bool
```

**应用位置**:

- **Get 场景** (`expandGet`): 展开前检查 `isVisited` → 已访问则终止本线 → 展开子实体前 `addVisited` 构造 childCtx
- **List 场景**: 当前未启用（每条记录的 visited 不同，批处理受益有限）

**不可变语义**: `addVisited` 创建新 map 而非修改原 map，确保并行展开的多条分支互不干扰。

### 4.4 硬上限深度控制

**常量** (`handler/cascade.go`):
```go
const hardMaxExpandDepth = 10  // 全局硬上限，防止无上限递归
```

**上下文工具**:
```go
// withDepth: 将剩余展开深度写入 context
func withDepth(ctx context.Context, depth int) context.Context

// getDepth: 返回剩余深度，未设置返回 (0, false)
func getDepth(ctx context.Context) (int, bool)
```

**注入逻辑** (`injectDepth`):
```go
// Get 和 List 入口调用
// 优先级: query ?depth=N > HandlerConfig.MaxExpandDepth
// 裁剪上限: min(userSet, hardMaxExpandDepth)
// 未指定 depth → 默认展开一层（不递归）
```

**HandlerConfig 新增字段**:
```go
MaxExpandDepth int  // 最大展开深度，0 表示只展开一层
```

### 4.5 忽略机制

**IgnoreConfig** (`handler/cascade.go`):
```go
type IgnoreConfig struct {
    Fields  []string // 需跳过的具体字段名
    All     bool     // 跳过所有展开和级联
    Ref     bool     // 跳过所有 References + ChildRefs
    Cascade bool     // 跳过所有 Cascades
}
```

**优先级**: `ignoreAll > ignoreRef/ignoreCascade > ignore=fieldList`

**HTTP 入口**:
```
GET /api/v1/sites/get?id=xxx&ignore=parent_site,domains
GET /api/v1/sites/get?id=xxx&ignoreRef=true
GET /api/v1/sites/get?id=xxx&ignoreCascade=true
GET /api/v1/sites/get?id=xxx&ignoreAll=true
GET /api/v1/sites/list?ignore=tags,children&ignoreAll=true
```

**注入逻辑** (`injectIgnore`, Handler 层 Get/List 入口):
- 解析 query params → 构造 `IgnoreConfig` → `withIgnore(ctx, cfg)` 写入 context
- 在三种展开循环中，每条关系展开前检查 `shouldIgnoreRef` / `shouldIgnoreCascade` / `shouldIgnoreField`

**上下文工具**:
```go
func withIgnore(ctx context.Context, cfg *IgnoreConfig) context.Context
func getIgnore(ctx context.Context) *IgnoreConfig
func shouldIgnoreField(ctx context.Context, name string) bool
func shouldIgnoreRef(ctx context.Context) bool
func shouldIgnoreCascade(ctx context.Context) bool
```

### 4.6 展开流程全景

```
HTTP Request (Get/List)
    │
    ├─ injectDepth(ctx, c)         ← ?depth=N 或 MaxExpandDepth 配置
    ├─ injectIgnore(ctx, c)        ← ?ignore= / ?ignoreRef= / ?ignoreCascade= / ?ignoreAll=
    │
    ├─ [Get] expandGet(ctx, result)
    │    ├─ Marshal *M → map
    │    ├─ isVisited check        ← 防跨实体/自关联循环
    │    ├─ getDepth check         ← 深度限制
    │    ├─ childCtx = withDepth(ctx, cur-1) + addVisited(ctx, name, id)
    │    ├─ References loop: ignoreRef/ignoreField? → effectiveExpandDepth? → DoGetByID
    │    ├─ ChildRefs loop:   ignoreRef/ignoreField? → effectiveExpandDepth? → DoList(OpIn)
    │    └─ Cascades loop:    ignoreCascade/ignoreField? → effectiveExpandDepth? → DoList(FK)
    │
    └─ [List] _doList(ctx, query)
         ├─ Marshal []*M → []map
         ├─ getDepth check
         ├─ childCtx = withDepth(ctx, cur-1)
         ├─ References batch:  ignore? → collect FK → DoList(OpIn) → lookup → backfill
         ├─ ChildRefs batch:   ignore? → collect FK list → DoList(OpIn) → lookup → backfill
         └─ Cascades batch:    ignore? → collect PK → DoList(FK, OpIn) → group → backfill
         └─ ListSkipFields / ListKeepFields 裁剪（在展开后执行）
```

---

## 五、关键实现细节

### 5.1 List 批量展开算法

References 批量展开流程:
1. 遍历结果集，收集所有 FK 值去重到 `fkSet`
2. `refHandler.DoList(childCtx, pkField, fkList, false)` → 一次 SQL (`IN` 查询)
3. 按 PK 建 `parentMap[string]map[string]any`
4. 回填: 每条结果 `m[resultKey] = parentMap[fkValue]`

ChildRefs 批量展开流程:
1. 收集所有 FK 列表值（二维去重）
2. `refHandler.DoList(childCtx, pkField, fkList, false)` → 一次 SQL
3. 按 PK 建 `childMap`
4. 每条结果按 FK 列表顺序组装子实体列表回填

Cascades 批量展开流程:
1. 收集所有父 PK 去重
2. `childHandler.DoList(childCtx, rel.FKField, pkList, rel.FollowPublished)` → 一次 SQL
3. 按 FKField 值分组 → `groups` map
4. 回填: 每条父结果 `m[rel.ChildrenField] = groups[pkValue]`

### 5.2 context 传递策略

| Control | Key Type | Inject Location | Consume Location |
|---------|----------|-----------------|------------------|
| `depth` | `depthCtxKey(struct{})` | Get/List 入口 | expandGet / _doList |
| `ignore` | `ignoreCtxKey(struct{})` | Get/List 入口 | expandGet / _doList |
| `visited` | `visitedCtxKey(struct{})` | expandGet 子展开前 | expandGet 展开前 |

三个控制维度互不影响，均通过 `context.WithValue` 传递。

### 5.3 版本管理

Service 层支持的 `VersionMode`:
- 非原地修改: Update 创建新版本行（旧行 `is_current=0`）
- 状态流: `draft → published → deprecated → abolished`
- Activate: 发布草稿 / 回滚为当前版本
- FollowPublished: 级联查询时可选只返回已发布版本

### 5.4 List 字段裁剪（2026-06 新增）

`HandlerConfig` 新增两个字段，仅影响 List 接口（`_doList` 返回前，所有展开之后执行）：

| 字段 | 类型 | 说明 |
|------|------|------|
| `ListSkipFields` | `[]string` | 黑名单（优先级高），移除指定字段 |
| `ListKeepFields` | `[]string` | 白名单（仅 Skip 为空时生效），仅保留指定字段 |

优先级: `ListSkipFields > ListKeepFields`。均未配置时全字段返回（向后兼容）。

```go
// 跳过较大 JSON 字段
ListSkipFields: []string{"form_config", "entity_config"}

// 仅返回概要字段
ListKeepFields: []string{"form_ulid", "form_code", "form_name"}
```

### 5.5 HTTP 响应原则（2026-06 确立）

**所有业务场景统一返回 HTTP 200**，业务结果通过响应体 `code` 字段区分：

| 场景 | HTTP 状态码 | `code` | `msg` |
|------|------------|--------|-------|
| 操作成功 | 200 | `200` | OK |
| 数据不存在 | 200 | `404` | 资源不存在 |
| 参数校验失败 | 200 | `4002` | 参数错误描述 |
| 服务器内部异常 | 200 | `500` | 内部错误 |

HTTP 状态码仅用于路由不存在（404）或服务器崩溃（500），绝不用于表达业务语义。

---

## 六、代码生成器 gentity

`tools/gentity/` — 独立的 MySQL → Go 代码生成器。

```bash
gentity --dsn "user:pass@tcp(host:port)/db?charset=utf8mb4&parseTime=true" --all --out generated
```

生成物:
- `generated/entity/` — struct + TableName + Record 接口实现
- `generated/blueprint/` — Repository → Service → Handler → Routes 注册蓝图

自动识别: ULID/自增主键、created_at/by、updated_at/by、软删除字段。

---

## 七、路由一览

| Method | Path | 说明 |
|--------|------|------|
| POST | `/{prefix}/create` | 批量创建（支持幂等 `idempotency_key`） |
| GET | `/{prefix}/list` | 列表查询（分页 + 过滤 + 展开/忽略控制） |
| GET | `/{prefix}/get` | 详情查询（展开/忽略控制） |
| POST | `/{prefix}/update` | 更新记录 |
| POST | `/{prefix}/delete` | 批量删除 |
| POST | `/{prefix}/activate` | 激活版本（VersionMode） |
| GET | `/{prefix}/versions` | 版本历史列表 |
| POST | `/{prefix}/edit-version` | 修改版本元数据 |

---

## 八、Git 说明

- **Remote**: `https://github.com/Huey1979/gocrux.git`
- **分支**: `master`（main 已删除）
- **不跟踪**: `config.yaml`、`logs/`、`generated/`、`storage/`、`doc/`、`*.exe`、IDE 文件
- 如需代理推送: Privoxy HTTP (127.0.0.1:8118) → Shadowsocks SOCKS5 (127.0.0.1:1080)
