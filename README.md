# gocrux

**gocrux** 是一个 Go 语言通用 CRUD 后端框架，提供泛型化的 **Handler → Service → Repository** 三层架构，支持级联操作、版本管理和审计日志。

```
go get github.com/Huey1979/gocrux
```

## 目录

- [架构概述](#架构概述)
- [快速开始](#快速开始)
- [实体定义](#实体定义)
- [Service 层配置](#service-层配置)
- [Handler 层配置](#handler-层配置)
- [展开深度控制](#展开深度控制)
- [忽略控制](#忽略控制)
- [自关联展开](#自关联展开)
- [List 字段裁剪](#list-字段裁剪)
- [List 级联展开控制](#list-级联展开控制listskipcascades--expand)
- [Entity → DTO 响应映射](#entity--dto-响应映射)
- [路由注册](#路由注册)
- [钩子系统](#钩子系统)
- [输入校验](#输入校验)
  - [BatchErrorMode](#batcherrormode--批量错误收集)
- [GlobalStore — 内存缓存](#globalstore--内存缓存)
- [DateTimeFormat — 日期时间格式化](#datetimeformat--日期时间格式化)
- [级联机制](#级联机制)
  - [级联创建跨实体引用](#级联创建跨实体引用)
- [版本管理](#版本管理)
  - [草稿可见性过滤](#草稿可见性过滤)
- [身份认证与授权](#身份认证与授权)
- [幂等支持](#幂等支持)
- [操作日志与备份](#操作日志与备份)
- [运行时日志（Request/Response/Business）](#运行时日志requestresponsebusiness)
  - [管线 Trace 日志](#管线-trace-日志)
- [列表查询条件](#列表查询条件)
  - [RawList — 原生查询](#rawlist--原生查询)
- [配置文件](#配置文件)
- [代码生成器 gentity](#代码生成器-gentity)
- [项目结构](#项目结构)

---

## 架构概述

```
 HTTP 请求
    │
    ▼
 GenericHandler[M]          ← HandlerConfig（路由前缀、级联关系、认证、权限）
    │  before → do → after  ← HandlerHooks（可覆盖任意环节）
    ▼
 GenericService[M]           ← Config（版本模式、唯一性校验、操作日志）
    │  before → do → after  ← Hooks（可覆盖任意环节）
    ▼
 CRUDRepository[M]           ← 泛型 GORM 仓储（自动推导主键）
    │
    ▼
  MySQL / MongoDB / Redis
```

核心设计原则：
- **泛型化**：每种实体类型 = 一个 `GenericHandler[M]` → `GenericService[M]` → `CRUDRepository[M]` 链条，编译时类型安全
- **管线模式**：每个操作（Create/Update/Delete/Get/List/Activate/EditVersion）都遵循 before → do → after 三段管线
- **钩子覆盖**：任意环节的钩子函数均可被外部替换，未替换时 fallback 到内置默认实现
- **事务透明**：Handler 层通过 `TxCoordinator` 编排事务，Service 层通过 `common.GetTx(ctx)` 自动感知事务上下文
- **HTTP 与业务解耦**：所有业务场景统一返回 HTTP 200，业务结果（成功/参数错误/数据不存在/内部错误）通过响应体中的 `code` 字段区分，绝不使用 HTTP 状态码表达业务语义

---

## 快速开始

### 1. 定义实体

```go
package entity

import "time"

type Site struct {
    SiteULID    string    `gorm:"column:site_ulid;primaryKey;size:26" json:"site_ulid"`
    SiteCode    string    `gorm:"column:site_code;size:64" json:"site_code"`
    SiteName    string    `gorm:"column:site_name;size:128" json:"site_name"`
    CreatedAt   time.Time `gorm:"column:created_at" json:"created_at"`
    UpdatedAt   time.Time `gorm:"column:updated_at" json:"updated_at"`
    IsDeleted   int8      `gorm:"column:is_deleted" json:"is_deleted"`
}

// 必须实现 service.Record 接口
func (s *Site) SetDefaults()                {}
func (s *Site) SetID()                      { s.SiteULID = common.NewULID() }
func (s *Site) SetCreatedAt(t time.Time)    { s.CreatedAt = t }
func (s *Site) SetCreatedBy(userID string)  {}
func (s *Site) SetUpdatedAt(t time.Time)    { s.UpdatedAt = t }
func (s *Site) SetUpdatedBy(userID string)  {}
func (s *Site) SupportsDraft() bool         { return false }
func (s *Site) SetDelete() bool             { s.IsDeleted = 1; return true }
func (s *Site) PKField() string             { return "site_ulid" }
func (s *Site) SelfFKField() string         { return "" }
```

### 2. 定义请求类型

```go
type CreateSiteRequest struct {
    SiteCode string `json:"site_code"`
    SiteName string `json:"site_name"`
}

func (r *CreateSiteRequest) GetID() any         { return nil }
func (r *CreateSiteRequest) Validate() error     { return nil }
func (r *CreateSiteRequest) MergeTo(target any) error {
    s := target.(*entity.Site)
    s.SiteCode = r.SiteCode
    s.SiteName = r.SiteName
    return nil
}
```

### 3. 组装并注册路由

```go
package main

import (
    "github.com/Huey1979/gocrux/handler"
    "github.com/Huey1979/gocrux/service"
    "github.com/Huey1979/gocrux/repository"
    "github.com/gin-gonic/gin"
)

func main() {
    // 创建仓储
    repo := repository.NewCRUDRepository[entity.Site]()

    // 创建 Service
    svc := service.NewGenericService(repo, service.Config[entity.Site]{
        EntityName:              "site",
        EnableOpLog:             true,
        EnableUniqueValidation:  true,
        UniqueFields:            [][]string{{"site_code"}},
    })

    // 创建 Handler
    h := handler.NewGenericHandlerWithSvc(svc, handler.HandlerConfig[entity.Site]{
        PathPrefix: "/api/v1/sites",
    })

    // 注册路由
    r := gin.Default()
    h.RegisterRoutes(r.Group("/api/v1/sites"))
    r.Run(":8080")
}
```

注册后将自动创建以下路由：

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/{prefix}/create` | 批量创建 |
| `GET` | `/{prefix}/list` | 列表查询（分页+过滤） |
| `GET` | `/{prefix}/get` | 详情查询（按 ID/Code） |
| `POST` | `/{prefix}/update` | 更新记录 |
| `POST` | `/{prefix}/delete` | 批量删除 |

当 Service 启用 `VersionMode` 时，额外注册：

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/{prefix}/activate` | 激活版本（发布/回滚） |
| `GET` | `/{prefix}/versions` | 版本历史列表 |
| `POST` | `/{prefix}/edit-version` | 修改版本元数据 |


## MongoDB 支持

gocrux 通过 `MongoCRUDRepository` 和 `Repo[M]` 接口提供完整的 MongoDB 支持，与 MySQL/GORM 对等。

### 架构

```
GenericHandler[M]  ->  GenericService[M]
                          |
                          v
                     Repo[M]  (接口)
                    /         \
         CRUDRepository[M]   MongoCRUDRepository[M]
           (MySQL/GORM)         (MongoDB)
```

### MongoCRUDRepository

提供与 `CRUDRepository` 一致的 CRUD 接口，底层使用 MongoDB：

```go
import "github.com/Huey1979/gocrux/repository"

// 创建 MongoDB 仓储（Collection 名称对应 MySQL 表名）
repo := repository.NewMongoCRUDRepository[entity.Product]("products")

// CRUD 操作（与 GORM 版相同）
product, _ := repo.GetByID(ctx, "01Jxxx...")
products, total, _ := repo.ListByFilters(ctx, repository.ListFilters{
    Filters: []repository.Filter{
        {Field: "status", Op: repository.OpEQ, Value: "active"},
    },
    Page: 1, PageSize: 20,
})
```

支持的 `ListByFilters` 操作符：`OpEQ`、`OpNEQ`、`OpLike`（转 `$regex`）、`OpGT`/`OpGTE`/`OpLT`/`OpLTE`、`OpIn`、`OpRange`。

`MongoCRUDRepository` 也提供 `Batch` 系列批量方法：`BatchSoftDelete`、`BatchSoftDeleteByFK`、`BatchFindByPK`、`BatchFindByFK`、`BatchHardDelete`、`BatchHardDeleteByFK`。

### Repo[M] 接口

`repository/repo.go` 定义了统一的仓储接口，`CRUDRepository`（MySQL/GORM）与 `MongoCRUDRepository`（MongoDB）均实现此接口：

```go
type Repo[M any] interface {
    Insert(ctx context.Context, entity *M) error
    InsertBatch(ctx context.Context, entities []*M) error
    GetByID(ctx context.Context, id any) (*M, error)
    GetByField(ctx context.Context, field string, value any) (*M, error)
    Save(ctx context.Context, entity *M) error
    UpdateByID(ctx context.Context, id any, updates map[string]any) error
    Delete(ctx context.Context, id any) error
    DeleteByFK(ctx context.Context, fkField string, fkValues []any) error

    BatchSoftDelete(ctx context.Context, ids []any) error
    BatchSoftDeleteByFK(ctx context.Context, fkField string, fkValues []any) error
    BatchFindByPK(ctx context.Context, ids []any) ([]M, error)
    BatchFindByFK(ctx context.Context, fkField string, fkValues []any) ([]M, error)
    BatchHardDelete(ctx context.Context, ids []any) error
    BatchHardDeleteByFK(ctx context.Context, fkField string, fkValues []any) error
    BatchDeprecateVersions(ctx context.Context, ids []any) error
    BatchDeprecateVersionsByFK(ctx context.Context, fkField string, fkValues []any) error

    ListByFilters(ctx context.Context, filters ListFilters) ([]M, int64, error)
    ListAll(ctx context.Context) ([]M, error)
    ListByField(ctx context.Context, field string, value any) ([]M, error)

    RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
    PKField() string
}
```

### 使用 MongoDB 的 GenericService

通过 `NewGenericServiceWithRepo` 注入任意 `Repo[M]` 实现：

```go
repo := repository.NewMongoCRUDRepository[entity.Product]("products")
svc := service.NewGenericServiceWithRepo(repo, service.Config[entity.Product]{
    EntityName: "product",
})
h := handler.NewGenericHandlerWithSvc(svc, handler.HandlerConfig[entity.Product]{
    PathPrefix: "/api/v1/product",
})
```

### TxCoordinator — MySQL + MongoDB 事务编排

```go
tc := handler.NewTxCoordinator(mysqlDB, mongoDB)

// 自动选择：ctx 中有 mongo session -> RunMongo，否则 -> RunMySQL
tc.Run(ctx, func(txCtx context.Context) error {
    // CRUDRepository / MongoCRUDRepository 自动感知 txCtx 中的事务
    return nil
})

// 显式指定
tc.RunMySQL(ctx, func(txCtx context.Context) error { ... })
tc.RunMongo(ctx, func(txCtx context.Context) error { ... })
```

### 事务上下文传递

```go
// common/tx.go — Repository 内部自动检测
ctx = common.WithTx(ctx, gormTx)            // MySQL 事务注入
ctx = common.WithMongoSession(ctx, sess)    // MongoDB Session 注入

tx := common.GetTx(ctx)                     // CRUDRepository 获取事务
sess := common.GetMongoSession(ctx)         // MongoCRUDRepository 获取 Session
```

### 服务组装对比

| 组件 | MySQL | MongoDB |
|------|-------|---------|
| 仓储 | `NewCRUDRepository[M]()` | `NewMongoCRUDRepository[M]("coll_name")` |
| Service | `NewGenericService(repo, cfg)` | `NewGenericServiceWithRepo(repo, cfg)` |
| 底层 | GORM -> `gorm.DB` | mongo-driver -> `mongo.Collection` |
| 事务 | `db.Transaction()` | `sess.WithTransaction()` |

---
---

## 实体定义

### Record 接口

所有实体必须实现 `service.Record` 接口：

```go
type Record interface {
    SetDefaults()                     // 设置默认值
    SetID()                           // 自动生成主键（ULID/自增）
    SetCreatedAt(t time.Time)         // 设置创建时间
    SetCreatedBy(userID string)       // 设置创建人
    SetUpdatedAt(t time.Time)         // 设置更新时间
    SetUpdatedBy(userID string)       // 设置更新人
    SupportsDraft() bool              // 是否支持草稿箱（版本管理）
    SetDelete() bool                  // 软删除标记（返回 true=软删, false=物理删）
    PKField() string                  // 主键数据库列名
    SelfFKField() string              // 自关联外键字段名（如 "parent_ulid"）；空字符串=无自关联
}
```

### 软删除

- `SetDelete() bool` 返回 `true`：实体有 `is_deleted` 字段，删除时执行 `UPDATE SET is_deleted=1`
- 返回 `false`：物理删除，先写备份日志再 `DELETE`

`ServiceConfig` 中的 `DeletedField` / `DeletedValue` 控制 List 查询时自动添加的软删除过滤条件：

- `DeletedField`：软删除字段列名（默认 `"is_deleted"`）
- `DeletedValue`：未删除时的字段值（默认 `int8(0)`）

`_doList` 执行前会自动检查 `SetDelete()` 返回值：若为 `true` 则追加 `WHERE {DeletedField} = {DeletedValue}` 过滤器，确保列表查询不返回已软删除的记录。不支持软删的实体（`SetDelete()` 返回 `false`）不添加此过滤。

### SupportsDraft

- 返回 `true` 时实体需提供 `VersionStatus` 字段（通过 `VersionFieldMapping` 映射）
- 版本化模式下，Update 创建新草稿；Activate 发布草稿为正式版本

---

## Service 层配置

### `service.Config[M]`

```go
type Config[M Record] struct {
    EnableUniqueValidation bool          // 启用唯一性校验
    EnableOpLog            bool          // 自动记录操作日志
    EntityName             string        // 实体中文名（用于日志）
    VersionMode            bool          // 启用版本管理模式
    VersionFields          *VersionFieldMapping // 版本字段映射
    UniqueFields           [][]string    // 唯一字段组
    DeletedField           string        // 软删除字段列名（默认 "is_deleted"）
    DeletedValue           any           // 软删除标记值（默认 1）
}
```

#### 配置项详解

**`EnableUniqueValidation`** — 启用后，Create 和 Update 时自动校验 `UniqueFields` 中声明的字段组是否已有重复记录。支持联合唯一索引。

```go
UniqueFields: [][]string{
    {"Mobile"},                               // mobile 单独唯一
    {"DeptID", "RoleID"},                     // dept_id + role_id 联合唯一
}
```

**`EnableOpLog`** — 启用后，Create/Update/Delete/Activate 完成时自动向 `sys_operation_log` 表写入操作日志。需注入 `opLogRepo`：

```go
svc.SetOpLogRepo(repository.NewCRUDRepository[entity.SysOperationLog]())
```

**`EntityName`** — 日志中 `EntityType` 字段的值，建议使用英文表名（如 `"site"`, `"role"`）。

#### 版本管理模式

**`VersionMode`** — 启用后 Update 不原地修改，而是：旧行 `is_current=0` → 插入新行（`is_current=1`）

**`VersionFieldMapping`** — 启用 `VersionMode` 时必须配置：

```go
type VersionFieldMapping struct {
    ULIDField        string // ULID 字段，如 "SiteULID"
    CodeField        string // 业务编码字段，如 "SiteCode"
    VersionField     string // 版本号字段，如 "VersionCode"
    CurrentField     string // 当前标记字段，如 "IsCurrent"
    StatusField      string // 版本状态字段，如 "VersionStatus"
    ParentField      string // 父版本字段，如 "ParentULID"
    RemarkField      string // 版本说明字段，如 "VersionRemark"
    PublishedAtField string // 发布时间字段，如 "PublishedAt"
    PublishedByField string // 发布人字段，如 "PublishedBy"
}
```

版本状态流转：

```
draft ──Activate──→ published ──(新版本发布)──→ deprecated
  │                                                  │
  └──EditVersion──→ abolished ←──EditVersion─────────┘
       (直接废弃)        │         (废弃版本恢复为草稿)
                        └──EditVersion──→ draft
```

---

## Handler 层配置

### `handler.HandlerConfig[M]`

```go
type HandlerConfig[M service.Record] struct {
    PathPrefix       string               // 路由前缀
    Cascades         []CascadeRelation    // 向下级联（父→子）
    References       []ReferenceRelation  // 向上引用（子→父）
    ChildRefs        []ChildRefRelation   // 向下 FK 列表引用
    ReqFactory       *RequestFactory[M]   // 请求构造器
    Auth             Authenticator        // 认证钩子
    Perm             Authorizer           // 权限钩子
    MaxExpandDepth   int                  // 全局最大递归展开深度（>0 启用，默认 0=不递归）
    FieldDepthLimits map[string]int       // 单字段深度上限（如 {"dept_ulid": 1}）
    FieldStopRules   map[string][]StopRule // 字段级截止规则（如 dept_ulid→-department:manager）
    ResponseMapper   func(M) any          // Entity→DTO 响应映射（可选，仅 HTTP 出口生效）
    ListSkipFields   []string             // List 黑名单字段（优先级高于 ListKeepFields）
    ListKeepFields   []string             // List 白名单字段（仅 Skip 为空时生效）
    ListSkipCascades []string             // List 默认不展开的级联子表名（nil=全部展开，[]string{}=全部跳过）
    KeywordFields    []string             // 关键字搜索字段列表（?keyword=xxx OR LIKE）
    Validate         *ValidateConfig      // 输入校验规则（nil=自动推导）
    NormalizeFields  []string             // 需表达式规范化的 JSON 字段名
    BatchErrorMode   string               // 批量错误处理："all_or_nothing"（默认）/"collect"
    SkipAutoValidate bool                 // 跳过自动字段校验（用于动态 schema 实体）
    GlobalStore      repository.GlobalStore // 内存缓存（nil=不启用）
    DateTimeFormat   string               // 日期时间格式，如 "2006-01-02 15:04:05"（空=保留 RFC3339）
}
```

### PathPrefix

路由前缀，如 `/api/v1/sites`。框架自动注册标准 CRUD 路由。

### RequestFactory

为 Create/Update/List 操作分别指定请求类型构造器。配置后 Handler 会将 HTTP body 反序列化为具体类型并调用其 `Validate()` 方法进行字段级校验。

```go
ReqFactory: &handler.RequestFactory[entity.Site]{
    Create: func() service.CrudRequest[entity.Site] { return &CreateSiteRequest{} },
    Update: func() service.CrudRequest[entity.Site] { return &UpdateSiteRequest{} },
    List:   func() any { return &ListSiteRequest{} },
},
```

未配置时 fallback 到内置 `MapRequest`，无 schema 校验但兼容任意 JSON。

### MapRequest 默认行为

若未配置 `ReqFactory`，HTTP body 会被绑定为 `map[string]any`，自动适配：
- `GetID()` 按优先级查找 `id` → `ulid` → `ID` → `ULID`
- `GetIdempotencyKey()` 从 `idempotency_key` 字段提取幂等键
- `MergeTo()` 通过 JSON 序列化/反序列化完成 map→struct 映射
- `Validate()` 始终通过（无 schema 校验）

### References（向上引用）

配置当前实体中指向父实体的逻辑外键字段，Get/List 时自动解析。

```go
References: []handler.ReferenceRelation{
    {
        Field:       "site_ulid",   // 当前实体的 FK 字段
        HandlerName: "site",        // 父 Handler 的注册名
        ResultField: "site",        // 结果键名（空则自动推导：去掉 _ulid）
    },
}
```

Get 场景：单次查父实体；List 场景：收集所有 FK 值 → 批量 `DoList` 展开。

### ChildRefs（向下 FK 列表引用）

配置当前实体通过 FK 列表（如 `tag_ulids: [1,2,3]`）引用的子实体，Get/List 时批量展开。

```go
ChildRefs: []handler.ChildRefRelation{
    {
        FKListField: "tag_ulids",  // FK 列表字段名
        HandlerName: "tag",        // 目标 Handler 注册名
        ResultField: "tags",       // 结果键名（空则自动推导：去掉 _ulids 加 s）
    },
}
```

**注意**：ChildRefs 仅关联已有实体，不参与级联创建/删除/更新。

### 展开深度控制

当配置了 References、ChildRefs 或 Cascades 后，Get/List 会自动展开关联数据。框架提供三层深度控制：

**1. 全局最大深度 (`MaxExpandDepth`)**

```go
MaxExpandDepth: 3, // References/ChildRefs/Cascades 递归展开最多 3 层
```

设置为 0 时只展开一层（不递归）。HTTP 可临时降级：

```http
GET /api/v1/sites/get?id=xxx&depth=2   # 上限不可超过 MaxExpandDepth
```

**2. 单字段深度上限 (`FieldDepthLimits`)**

对特定字段单独限制展开深度：

```go
FieldDepthLimits: map[string]int{
    "dept_ulid": 1,  // dept 字段只展开 1 层（即平铺后不递归）
    "site_ulid": 2,  // site 字段最多展开 2 层
},
```

HTTP 参数（逗号分隔 `字段:深度` 对）：

```http
GET /api/v1/users/get?id=xxx&fdepth=dept_ulid:1,site_ulid:2
```

**3. 字段级截止规则 (`FieldStopRules`)**

控制某个字段展开到目标子 Handler 后，子 Handler 的哪些字段被截止（跳过不展开）：

```go
FieldStopRules: map[string][]handler.StopRule{
    "dept_ulid": {
        {OnHandler: "department", Field: "manager",  Stop: true},  // -department:manager → 跳过
        {OnHandler: "department", Field: "parent_id", Stop: false}, // department:parent_id → 展开一层后截止
    },
},
```

HTTP compact 格式：

```http
GET /api/v1/users/get?fstop=dept_ulid=-department:manager,department:parent_id
```

**格式规则**：前缀 `-` 表示 `Stop:true`（完全跳过），不带前缀表示 `Stop:false`（展开一层后截止）。多规则用逗号分隔，多字段使用多个 `fstop` 参数。

> **设计说明**：`MaxExpandDepth`、`FieldDepthLimits`、`FieldStopRules` 是服务端默认配置，HTTP 参数 `depth`/`fdepth`/`fstop` 作为限缩性覆盖（只能降级不能放大），避免 URL 过长问题。

### 忽略控制

通过 HTTP query 按需跳过特定展开环节（不覆盖配置，仅做减法）：

| 参数 | 作用 |
|------|------|
| `?ignore=fieldA,fieldB` | 跳过指定字段的展开（逗号分隔，匹配 ResultField/ChildrenField） |
| `?ignoreRef=true` | 跳过所有 References + ChildRefs 展开 |
| `?ignoreCascade=true` | 跳过所有 Cascades 展开 |
| `?ignoreAll=true` | 跳过所有展开（仅返回裸数据） |

优先级：`ignoreAll > ignoreRef/ignoreCascade > ignore`。未传入任何参数时无额外开销。

### 自关联展开

当实体需要引用自身时（如部门表 `parent_dept_ulid` 指向同表的父部门），可通过配置 References 实现自关联展开。

**示例**：

```go
// 1. 实体实现 SelfFKField()，声明自关联外键字段
func (d *SysDept) SelfFKField() string { return "parent_dept_ulid" }

// 2. 配置 References 指向自身
HandlerConfig[*entity.SysDept]{
    PathPrefix: "/api/v1/dept",
    MaxExpandDepth: 5, // 最多展开 5 层
    References: []handler.ReferenceRelation{
        {
            Field:       "parent_dept_ulid",
            HandlerName: "dept",  // 指向自身
            ResultField: "parent",
        },
    },
}
```

Get 请求 `/api/v1/dept/get?id=xxx` 会递归展开父部门链，形成层级树：
```json
{
    "dept_name": "研发三组",
    "parent": {
        "dept_name": "研发部",
        "parent": {
            "dept_name": "技术中心",
            "parent": null
        }
    }
}
```

**循环防护**（无需手动阻止自关联）：
1. **深度控制**：`MaxExpandDepth` 限制最大递归层数，全局硬上限 `hardMaxExpandDepth=10`，到 0 时自动停止
2. **visited 追踪**（Get 场景）：记录每条展开线上的 `(HandlerName, RecordID)`，遇到已访问的记录立即终止该条展开线，防止 A→B→A 跨实体环或 A→A→A 自环

> **注意**：级联写操作（OnCreate/OnDelete/OnUpdate）的 `SelfFKField()` 仅用于读展开的循环防护，不影响写行为。

### List 字段裁剪

**注意**：以下配置**仅影响 List 接口**（`_doList` 返回前执行），Get 接口始终返回全字段。

**`ListSkipFields`** — 黑名单模式（优先级高于 Keep）

从 List 响应中移除指定字段，常用于跳过较大的 JSON/Text 字段（如 `form_config`、`entity_config`），减少网络传输量。

```go
HandlerConfig[entity.SysForm]{
    PathPrefix:     "/api/v1/sys-form",
    ListSkipFields: []string{"form_config", "entity_config", "flow_config"},
}
```

**`ListKeepFields`** — 白名单模式（仅 Skip 为空时生效）

仅保留指定字段，所有未声明的字段从 List 响应中移除。

```go
HandlerConfig[entity.SysForm]{
    PathPrefix:     "/api/v1/sys-form",
    ListKeepFields: []string{"form_ulid", "form_code", "form_name", "form_type"},
}
```

**优先级**：`ListSkipFields` > `ListKeepFields`。两者同时配置时 hanya 生效 Skip，Keep 被忽略。均未配置时全字段返回（向后兼容）。

**执行时机**：所有级联展开（References/ChildRefs/Cascades）完成之后执行，级联数据不受裁剪影响。

### List 级联展开控制（`ListSkipCascades` + `?expand`）

**约定**：List 接口默认不展开 Cascades 级联数据（与 Get 行为不同），通过 `ListSkipCascades` 配置和 `?expand` 参数精确控制。

**`ListSkipCascades`** 配置：

```go
HandlerConfig[entity.SysForm]{
    ListSkipCascades: []string{},              // 空切片 = 全部跳过（推荐）
    // ListSkipCascades: nil,                  // nil = 全部展开（向后兼容）
    // ListSkipCascades: []string{"list_layout"}, // 仅跳过 list_layout
}
```

**HTTP 覆盖**：

| 参数 | 作用 |
|------|------|
| `?expand=name1,name2` | 仅展开指定的级联（逗号分隔） |
| `?expandAll=true` | 强制全部展开（覆盖 `ListSkipCascades`） |

优先级：`?expandAll=true > ?expand=list > ListSkipCascades 配置 > 默认不展开`。

### Entity → DTO 响应映射

DB 实体通常携带存储层专属字段（`is_deleted`、`password`、`is_current`、`parent_ulid` 等），不应直接暴露给 API 消费者。`ResponseMapper` 在 HTTP 出口处将 Entity 转换为 DTO，裁剪敏感/冗余字段。

**设计约束**：

- **仅在 HTTP handler 出口执行**（Get/List），管道（pipeline）和级联调用（DoGetByID/DoList）不执行映射
- `ResponseMapper == nil` 时零开销，完全向后兼容
- 展开后的级联数据（References/ChildRefs/Cascades）自动从原始 map 合并回 DTO 输出

**使用方式**：

```go
// 场景 1：不映射 — 完全兼容旧行为（默认）
gh := handler.NewGenericHandler[*entity.SysDept](svcReg, "sys_dept",
    handler.HandlerConfig[*entity.SysDept]{
        PathPrefix: "/api/v1/sys-dept",
        // ResponseMapper: nil （默认）
    })

// 场景 2：映射为 DTO（字段裁剪，如去掉 is_deleted、password 等）
gh := handler.NewGenericHandler[*entity.SysSite](svcReg, "sys_site",
    handler.HandlerConfig[*entity.SysSite]{
        PathPrefix: "/api/v1/sys-site",
        ResponseMapper: func(s *entity.SysSite) any {
            return s.ToDTO() // gentity 自动生成的结构体方法
        },
    })

// 场景 3：自定义映射（如 List 只返回概要字段）
gh := handler.NewGenericHandler[*entity.SysSite](svcReg, "sys_site",
    handler.HandlerConfig[*entity.SysSite]{
        PathPrefix: "/api/v1/sys-site",
        ResponseMapper: func(s *entity.SysSite) any {
            return &BriefSite{
                Code: s.SiteCode,
                Name: s.SiteName,
            }
        },
    })
```

**Get 流程**：

```
Get() → injectDepth/injectIgnore/injectStop → create entityHolder(ctx)
     → getPipeline → _doGet(entity→holder) → expandGet
     → applyResponseMapper(entityHolder, expandedMap)  ← 这里是映射点
     → Success()
```

**List 流程**：

```
List() → inject… → create entitiesHolder(ctx) → listPipeline
      → _doList(entities→holder) → 批量展开
      → for each entity: applyResponseMapper(entity, item)  ← 这里是映射点
      → Success()
```

**DTO 结构体生成**（gentity）：

```
gentity --dto --dto-exclude is_deleted,is_current,parent_ulid --all --out generated
```

参数说明：

| 参数 | 说明 | 默认值 |
|---|---|---|
| `--dto` | 启用 DTO 生成 | `false` |
| `--dto-exclude` | 全局排除字段列表，逗号分隔 | `is_deleted,is_current,parent_ulid` |
| `--dto-pkg` | DTO 输出包名/子目录 | `dto` |

**注意**：`ResponseMapper` 仅裁剪 Entity 自有字段，级联展开数据（References/ChildRefs/Cascades 的 `ResultField`/`ChildrenField`）不受 DTO 裁剪影响——它们由框架在映射后自动合并。

### Cascades（向下级联）

详见 [级联机制](#级联机制) 章节。

### Auth（认证）

详见 [身份认证与授权](#身份认证与授权) 章节。

### Perm（权限）

详见 [身份认证与授权](#身份认证与授权) 章节。
### NormalizeFields — 表达式规范化

配置 `NormalizeFields` 后，Create/Update 请求中指定字段的 JSON 表达式在管线执行前自动规范化：

```go
HandlerConfig[entity.SysFormField]{
    NormalizeFields: []string{"display_formula", "filter_config"},
}
```

规范化规则（`expression/normalizer.go`）：
- `expression` 类型：统一为 `{"type":"expression","expression":{...}}` 结构
- 旧格式 `{"op":"Add","left":...}` 自动升级为新格式

### GlobalStore — 内存缓存

注入 `GlobalStore` 后，Get/Create/Update/Delete 管线自动维护内存缓存：

- **Get**：优先查缓存，命中跳过 DB；未命中走 DB 后写回缓存
- **Create / Update**：写入缓存
- **Delete**：清理缓存

```go
import "github.com/Huey1979/gocrux/repository"

HandlerConfig[entity.SysForm]{
    GlobalStore: repository.NewMapStore(), // 基于 sync.Map 的内置实现，一行搞定
}
```

**自定义后端**（Redis 等）：

```go
type RedisStore struct { client *redis.Client }

func (s *RedisStore) Get(ctx context.Context, key string) (any, bool) { /* ... */ }
func (s *RedisStore) Set(ctx context.Context, key string, entity any)  { /* ... */ }
func (s *RedisStore) Del(ctx context.Context, key string)              { /* ... */ }

HandlerConfig[entity.SysForm]{
    GlobalStore: &RedisStore{client: rdb},
}
```

> 注意：缓存 key 由框架内部生成（如 `ulid:01Jxxx`、`code:S001`），使用者透明。`nil` 时不启用缓存（默认）。

### DateTimeFormat — 日期时间格式化

配置 `DateTimeFormat` 后，Get/List 返回数据中所有 `time.Time` 字段统一按指定格式输出：

```go
HandlerConfig[entity.SysForm]{
    DateTimeFormat: "2006-01-02 15:04:05",
}
```

响应中的 `created_at`、`updated_at`、`published_at` 及级联子数据中的时间字段均自动格式化。为空时使用 Go 默认 RFC3339 格式（向后兼容）。

---

## 路由注册

### 直接注册

```go
h := handler.NewGenericHandlerWithSvc(svc, cfg)
h.RegisterRoutes(router.Group("/api/v1/sites"))
```

### 通过注册表注册（推荐，支持级联）

```go
// 1. 创建注册表
svcReg := service.NewServiceRegistry()
handlerReg := handler.NewHandlerRegistry()
txCoord := handler.NewTxCoordinator(db) // *gorm.DB

// 2. 创建 Service 并注册
siteSvc := service.NewGenericService(siteRepo, siteCfg)
svcReg.Register("site", siteSvc)

// 3. 创建 Handler 并注册
siteHandler := handler.NewGenericHandler[entity.Site](svcReg, "site", handlerCfg)
siteHandler.SetHandlerReg(handlerReg)
siteHandler.SetTxCoord(txCoord)
handlerReg.Register("site", siteHandler)

// 4. 注册路由
siteHandler.RegisterRoutes(api.Group("/api/v1/sites"))
```

---

## 钩子系统

### Handler 层钩子 (`HandlerHooks[M]`)

每个 CRUD 操作对应 before / do / after 三个钩子，覆盖后完全接管对应环节。

```go
h.SetHooks(handler.HandlerHooks[entity.Site]{
    BeforeCreate: func(ctx context.Context, input []service.CrudRequest[entity.Site]) ([]service.CrudRequest[entity.Site], error) {
        // 前置处理：校验、转换、补充字段等
        return input, nil
    },
    DoCreate: func(ctx context.Context, input []service.CrudRequest[entity.Site]) ([]*entity.Site, error) {
        // 自定义创建逻辑（替代默认的 svc.Create + 级联编排）
        return nil, nil
    },
    AfterCreate: func(ctx context.Context, result []*entity.Site) ([]*entity.Site, error) {
        // 后置处理：发送通知、更新缓存等
        return result, nil
    },
    // 同样支持：BeforeUpdate/DoUpdate/AfterUpdate、
    // BeforeDelete/DoDelete/AfterDelete、
    // BeforeGet/DoGet/AfterGet、
    // BeforeList/DoList/AfterList、
    // BeforeActivate/DoActivate/AfterActivate、
    // BeforeListVersions/DoListVersions/AfterListVersions、
    // BeforeEditVersion/DoEditVersion/AfterEditVersion
})
```

**重要**：Handler 层的 before/after 钩子不依赖 `*gin.Context`，无论走 HTTP 入口还是级联入口，钩子都能正常工作。

### Service 层钩子 (`Hooks[M]`)

与 Handler 层对称，同样支持 before / do / after 三段钩子。

```go
svc.SetHooks(service.Hooks[entity.Site]{
    BeforeCreate: func(ctx context.Context, input []service.CrudRequest[entity.Site]) ([]*entity.Site, error) {
        // 数据组装、默认值、唯一性校验等
        return entities, nil
    },
    // ... 其他钩子
})
```

### 钩子覆盖策略

```
handler.hooks.XxxBefore != nil ? → 使用自定义钩子
                               : → fallback handler._beforeXxx()
                                                       │
                                         handler._beforeXxx() 调用 svc.beforeXxx()
                                                       │
                                         svc.hooks.BeforeXxx != nil ? → 使用自定义钩子
                                                                     : → fallback svc._beforeXxx()
```

---

## 输入校验

框架在 **Handler 层**对 Create / Update / List 接口提供内置输入校验，无需显式配置即可获得基础的类型和长度保护。

### 核心特性

| 特性 | 说明 |
|------|------|
| **零配置** | 从 entity struct 的 gorm 标签自动推导规则 |
| **宽松类型** | 类型不匹配时优先尝试转换，而非直接报错 |
| **内置格式** | `datetime` / `date` / `email` / `phone` / `url` / `ulid` 等开箱即用 |
| **双层模型** | 框架校验（字段级）→ 业务校验（跨字段 / DB 唯一性） |

### 自动推导（零配置）

Handler 构造时自动从 entity struct 反射出字段类型规则：

| entity 字段类型 | 自动规则 |
|---------------|---------|
| `string`（gorm `size:N`） | `type=string`, `max_length=N` |
| `string`（`*_ulid` 后缀） | `type=string`, `max_length=26`, `format=ulid` |
| `int/int8/.../int64` | `type=int` |
| `float32/float64` | `type=float` |
| `bool` | `type=bool` |
| `time.Time` | `type=time` |
| gorm `not null` | `required=true`（仅 Create） |

### 宽松类型转换

框架在类型不匹配时**优先尝试转换**，能转就不报错：

| 输入值 | 期望类型 | 结果 |
|--------|---------|------|
| `123`（JSON 数字） | `string` | ✅ → `"123"` |
| `"123"`（字符串） | `int` | ✅ → `123` |
| `"1"` / `1` | `bool` | ✅ → `true` |
| `0` | `bool` | ✅ → `false` |
| `"abc"` | `int` | ❌ 无法转换才报错 |
| `""` | `datetime` | ❌ 空字符串时间 → 报错 |

### 校验范围

| 接口 | 校验内容 |
|------|---------|
| **Create** | body 中每个字段的**类型转换 + 格式 + 必填 + 长度** |
| **Update** | body 中每个字段的**类型转换 + 格式 + 长度**（不强制必填） |
| **List** | 分页参数（`page`≥1, `page_size`∈[1,100], `order_dir`∈{asc\|desc}）+ 过滤字段 |

### 示例：无配置时的行为

```go
type Site struct {
    SiteULID  string `gorm:"column:site_ulid;primaryKey;size:26" json:"site_ulid"`
    SiteCode  string `gorm:"column:site_code;size:64;not null" json:"site_code"`
    SortOrder int    `gorm:"column:sort_order" json:"sort_order"`
}
```

**Create 时**（JSON body）：
```json
{"site_code": 123, "sort_order": "5"}
```
→ 框架自动转换：`site_code` → `"123"`，`sort_order` → `5`，正常入库

**List 时**：
```
GET /api/v1/sites/list?page=a
```
→ 框架拦截：`字段[page] 应为整数`（`"a"` 无法转为数字）

### 内置格式校验（`format` 字段）

无需手写正则的常见格式：

| 格式 | 说明 | 示例 |
|------|------|------|
| `datetime` | 日期时间（支持多种格式） | `2024-01-01 10:00:00` |
| `date` | 日期 | `2024-01-01` |
| `time` | 时间 | `10:00:00` |
| `email` | 邮箱 | `user@example.com` |
| `url` | URL | `https://example.com` |
| `phone` | 手机号（中国大陆） | `13800138000` |
| `ulid` | 26位 Crockford base32 | `01JXXXXX...`（自动开启） |

**使用场景**：

```yaml
# 防止前端传空字符串 "" 导致 MySQL datetime 插入失败
created_at:
  format: datetime
```

```go
// 代码方式
"email": {Format: "email"}
"phone": {Format: "phone"}
```

### 自定义规则（YAML 覆写）

通过 `Validate` 配置字段覆盖或增强自动推导规则。

**代码方式**：

```go
handler.HandlerConfig[entity.SysSite]{
    PathPrefix: "/api/v1/sys-site",
    Validate: &handler.ValidateConfig{
        Create: &handler.EndpointRules{
            "site_code": {Required: true, MaxLength: handler.IntPtr(64)},
            "site_type": {Enum: []string{"web", "app", "miniapp"}},
            "domain":    {Format: "url"},
        },
        List: &handler.EndpointRules{
            "page_size": {Max: handler.Float64Ptr(200)},
        },
    },
}
```

**YAML 文件方式**（`configs/validations.example.yaml`）：

```yaml
validations:
  sys_site:
    create:
      site_code:
        required: true
        max_length: 64
      site_type:
        enum: ["web", "app", "miniapp"]
      domain:
        format: url
    list:
      page_size:
        max: 200
  sys_user:
    create:
      email:
        format: email
      phone:
        format: phone
```

加载：

```go
vcMap, _ := handler.LoadValidationConfig("configs/validations.yaml")

siteHandler := handler.NewGenericHandler[*entity.SysSite](svcReg, "sys_site",
    handler.HandlerConfig[*entity.SysSite]{
        PathPrefix: "/api/v1/sys-site",
        Validate: vcMap["sys_site"],
    })
```

### 支持的校验规则

| 属性 | 类型 | 说明 | 适用操作 |
|------|------|------|---------|
| `type` | `string` | 期望类型：`string`/`int`/`float`/`bool`/`time` | 全部 |
| `required` | `bool` | 是否必填（Create 由 gorm `not null` 自动推导） | Create |
| `min` / `max` | `float64` | 数值范围 | 全部 |
| `min_length` / `max_length` | `int` | 字符串长度 | 全部 |
| `enum` | `[]string` | 枚举值白名单 | 全部 |
| `pattern` | `string` | 正则表达式 | 全部 |
| `format` | `string` | 内置格式：`datetime`/`date`/`time`/`email`/`url`/`phone`/`ulid` | 全部 |

### 双层校验模型

框架校验与业务校验协同工作：

```
HTTP Body
  │
  ├─ validateInput(raw, rules)  ← ★ 框架校验（类型转换 + 格式 + 长度，自动+配置）
  │   └─ 失败 → ErrReqValidation（Create）/ ErrUpdateReqValidation（Update）
  │
  ├─ req.Validate()             ← ★ 业务校验（ReqFactory 注入，可选）
  │   └─ 失败 → 同上
  │
  └─ Service before hooks       ← Service 层校验（唯一性等）
```

两层的分工：
- **框架校验**：字段级别的类型、长度、范围、枚举 — **零配置即可用**
- **业务校验**：跨字段逻辑、数据库唯一性、状态迁移 — 按需覆盖

### BatchErrorMode — 批量错误收集

`HandlerConfig.BatchErrorMode` 控制批量 Create 时的错误处理行为：

```go
HandlerConfig[entity.SysSite]{
    BatchErrorMode: "collect", // 默认 "all_or_nothing"
}
```

| 模式 | 行为 |
|------|------|
| `"all_or_nothing"`（默认） | 第一个校验错误即返回，不写入任何数据。向后兼容 |
| `"collect"` | 逐条收集所有校验错误，标注每条出错数据的**索引**和**字段名**，统一返回。全部通过才开事务 |

错误返回示例（`collect` 模式）：

```
共 3 条数据校验失败:
  [1] 第2条 site_code: 不能为空
  [2] 第3条 domain: 格式不正确，期望 URL
  [3] 第7条 sort_order: 应为整数
```

> 注意：`collect` 模式仅影响**错误报告方式**——仍然全部通过才写入，不产生部分成功/部分失败的情况。用户修复所有报错后重新全量提交即可。

---

## 级联机制

级联是 gocrux 核心特性之一，通过 `CascadeRelation` 声明父子关系，框架自动在事务内编排父实体与子实体的联动操作。

### CascadeRelation 配置

```go
type CascadeRelation struct {
    HandlerName     string // 子 Handler 在 HandlerRegistry 中的注册名称
    ChildrenField   string // 请求体中子数据的字段名（如 "domains"）
    FKField         string // 子表中的外键字段名（如 "site_ulid"）
    OnCreate        bool   // 创建父时级联创建子
    OnDelete        bool   // 删除父前级联删除子
    OnUpdate        bool   // 更新父时级联更新子
    OnActivate      bool   // 激活父版本时级联激活子
    OnEditVersion   bool   // 编辑父版本时级联编辑子
    FollowPublished bool   // 级联检索时是否返回正式发布版本
    ChildrenWrapKey string // 子数据为标量数组时的包裹键名
}
```

### 级联创建示例

```go
Cascades: []handler.CascadeRelation{
    {
        HandlerName:   "domain",
        ChildrenField: "domains",      // HTTP body 中: {"domains": [{...}, {...}]}
        FKField:       "site_ulid",    // 自动注入到每条子数据
        OnCreate:      true,
        OnDelete:      true,
    },
}
```

HTTP 请求体：
```json
[{
    "site_code": "S001",
    "site_name": "主站",
    "domains": [
        {"domain_name": "example.com"},
        {"domain_name": "test.com"}
    ]
}]
```

框架自动：创建 Site → 将 site_ulid 注入 domains 中的每条数据 → 调用 domainHandler.DoCreate → 同一事务内完成。

### 级联更新策略

Update 时级联行为与子数据存在性相关：

| 场景 | 行为 |
|------|------|
| 有子数据 + 非版本化 + 已有旧子记录 | 先删旧子记录 → 全量替换为新子数据 |
| 有子数据 + 版本化 | 创建新版本子记录（保留旧版本关联） |
| 无子数据 + 已有旧子记录 | 回填旧子数据 → 更新 FK 指向新父实体 |
| 无子数据 + 无旧子记录 | 跳过 |

### ChildrenWrapKey

当子数据不是完整对象而是标量数组时使用：

```go
{
    HandlerName:     "tag",
    ChildrenField:   "tags",
    FKField:         "user_ulid",
    ChildrenWrapKey: "tag_id",   // [1,2,3] → [{"tag_id":1},{"tag_id":2},{"tag_id":3}]
    OnCreate:        true,
}
```

前端可直接传 `"tags": [1, 2, 3]`，框架自动包裹。

### TxCoordinator

事务编排器，Handler 层不直接接触 gorm.DB。

```go
tc := handler.NewTxCoordinator(db) // *gorm.DB
// 注入到每个需要级联的 Handler
handler.SetTxCoord(tc)

// 内部通过 tc.Run(ctx, func(txCtx) {...}) 在事务中执行
// Service 通过 service.GetTx(ctx) / common.GetTx(ctx) 自动感知事务
```

### HandlerRegistry

管理所有 Handler 实例的注册表，级联时通过名称查找子 Handler。

```go
handlerReg := handler.NewHandlerRegistry()
handlerReg.Register("site",   siteHandler)
handlerReg.Register("domain", domainHandler)
handlerReg.Register("tag",    tagHandler)

// 注入到需要级联的 Handler
siteHandler.SetHandlerReg(handlerReg)
```

### 级联创建跨实体引用

级联 Create 时，若后续批次的子实体 FK 需要引用**同请求中前面批次刚创建的子实体**（ULID 尚未分配），可使用占位符机制：

**1. 在源数据中标记 `_temp_ref`**：

```json
{
  "form_code": "F001",
  "form_fields": [
    {"_temp_ref": "ff1", "field_code": "name"},
    {"_temp_ref": "ff2", "field_code": "age"}
  ],
  "write_fields": [
    {"form_field_ulid": "__ref:form_field:ff1__"}
  ]
}
```

**2. 在引用处使用占位符** `__ref:<handler_name>:<temp_ref>__`，框架在级联创建过程中自动替换为真实 ULID。

**工作原理**：
1. 创建 `form_fields` 前，框架收集 `_temp_ref` → 临时标记映射
2. `form_fields` 创建完成，`ff1` → `01JXXXX...`，`ff2` → `01JYYYY...`
3. 创建 `write_fields` 前，框架将 `__ref:form_field:ff1__` 替换为 `01JXXXX...`

> `_temp_ref` 字段不会写入数据库，仅用于级联期间的临时引用。

---

## 版本管理

### 启用条件

```go
Config[entity.Site]{
    VersionMode: true,
    VersionFields: &service.VersionFieldMapping{
        ULIDField:        "SiteULID",
        CodeField:        "SiteCode",
        VersionField:     "VersionCode",
        CurrentField:     "IsCurrent",
        StatusField:      "VersionStatus",
        ParentField:      "ParentULID",
        RemarkField:      "VersionRemark",
        PublishedAtField: "PublishedAt",
        PublishedByField: "PublishedBy",
    },
}
```

### 版本化 Update 流程

非原地修改，而是：
1. 取出现有记录
2. 深拷贝 → 合并请求字段 → 生成新 ULID + 新 VersionCode
3. 事务中：旧行 `is_current=0` → 插入新行 `is_current=1`，新版本默认为 `draft`

### Activate（发布/回滚）

```http
POST /api/v1/sites/activate
{"id": "01JXXX..."}
```

- draft/deprecated → published：发布/回滚
- 同 code 所有行退位 → 目标行 `is_current=1`
- 级联激活子记录（按 `OnActivate` 配置）

### ListVersions

```http
GET /api/v1/sites/versions?code=S001
```

按 code 查所有版本，按版本号降序排列。

### EditVersion

```http
POST /api/v1/sites/edit-version
{"id": "01JXXX...", "patches": {"VersionStatus": "abolished"}}
```

状态迁移限制：
- `draft` → `abolished`（直接废弃）
- `deprecated` → `abolished`（归档）
- `abolished` → `draft`（恢复为草稿）
- `published` 禁止直接 abolished

### FollowPublished 机制

Get/List/Cascade 查询时可能 FK 指向旧版本，`followPublished` 控制是否解析为正式发布版：

- `followPublished=false`：返回 FK 精确指向的版本（如订单快照）
- `followPublished=true`：找到同 code 的 `version_status='published'` 版本

HTTP 入口：
```http
GET /api/v1/sites/get?id=xxx&follow_published=true
GET /api/v1/sites/get?code=S001     # 按 code 查当前生效版本（is_current=1, is_deleted=0）
```

### 草稿可见性过滤

版本化模式下 `_doList` 自动在 SQL 层添加草稿可见性过滤：

| 场景 | 可见范围 |
|------|---------|
| 未登录用户 | 仅 `version_status = 'published'` |
| 登录用户 | `published` OR (`draft` AND `created_by = 当前用户`) |

这确保前端列表接口默认不暴露他人的草稿数据。Service 层通过 `GetUserULID(ctx)` 获取当前用户，若 ctx 中无用户信息则按未登录处理。过滤在 SQL 层执行（而非 `_afterList` 内存过滤），保证分页准确性。

---

## 身份认证与授权

### Authenticator（认证）

```go
type Authenticator interface {
    Middleware() gin.HandlerFunc
    FromContext(c *gin.Context) (UserInfo, bool)
}
```

实现示例（JWT）：

```go
type JWTAuth struct{}

func (a *JWTAuth) Middleware() gin.HandlerFunc {
    return func(c *gin.Context) {
        token := c.GetHeader("Authorization")
        claims := parseJWT(token) // 使用者自定义
        c.Set("user", handler.UserInfo{
            ULID: claims.Sub,
            Name: claims.Name,
        })
        c.Next()
    }
}

func (a *JWTAuth) FromContext(c *gin.Context) (handler.UserInfo, bool) {
    v, ok := c.Get("user")
    if !ok { return handler.UserInfo{}, false }
    return v.(handler.UserInfo), true
}
```

注入方式一：全局中间件

```go
middleware.DefaultAuthenticator = &JWTAuth{}
router.Use(middleware.AuthMiddleware())
```

注入方式二：在 HandlerConfig 中指定

```go
HandlerConfig[entity.Site]{
    Auth: &JWTAuth{},
}
```

### Authorizer（授权）

```go
type Authorizer interface {
    Check(info UserInfo, resource string, action string) bool
}
```

实现示例（RBAC）：

```go
type RBACAuthorizer struct{}

func (a *RBACAuthorizer) Check(info handler.UserInfo, resource, action string) bool {
    roles := info.Extra["roles"].([]string)
    return hasPermission(roles, resource, action)
}
```

注入后，每个操作执行前自动调用 `Check`：

| resource | action |
|----------|--------|
| `site` | `create`, `update`, `delete`, `get`, `list` |
| `role` | `create`, `update`, `delete`, `get`, `list` |

---

## 幂等支持

通过 `IdempotencyStore` 缓存创建结果，相同幂等键的重复请求直接返回缓存。

```go
store := service.NewIdempotencyStore[entity.Site](time.Hour) // 1小时 TTL
svc.SetIdemStore(store)
```

HTTP 请求中传入 `idempotency_key` 字段即可：

```json
{
    "idempotency_key": "order-2024-001",
    "site_code": "S001",
    "site_name": "主站"
}
```

> 注意：内存缓存，服务重启后丢失。生产环境可替换为 Redis 实现。

---

## 操作日志与备份

### 操作日志表

启用 `EnableOpLog` 后自动写入 `sys_operation_log` 表：

```go
svc.SetOpLogRepo(repository.NewCRUDRepository[entity.SysOperationLog]())
```

日志字段：`log_ulid`、`entity_type`、`entity_id`、`operation`、`operator_ulid`、`request_id`、`operated_at`。

### 备份写入器

非版本化 Update 时旧数据会被覆盖丢失，可通过 `BakWriter` 在更新前写备份日志文件：

```go
svc.SetBakWriter(func(ctx context.Context, tableName string, recordID any, operation string, oldData any, requestID string) error {
    // 写入文件或 MongoDB
    return nil
})
```

---

## 运行时日志（Request/Response/Business）

框架内置一套独立的**运行时追踪日志**（`internal/logger`），与 `config.yaml` 中的 `log` 配置段**是两套不同的日志系统**：

| 系统 | 初始化方式 | 输出 | 用途 |
|------|-----------|------|------|
| **全局 logrus** | `config.yaml` → `log` 段 | stdout / file（由配置决定） | 应用级日志（启动、配置加载等） |
| **运行时追踪日志** | `logger.Init(logDir)` | 按天滚动文件 | 每个 HTTP 请求的完整链路追踪 |

三个独立实例，每次请求生成唯一 `request_id` 串联：

| 实例 | 文件 | 内容 |
|------|------|------|
| `RequestLog` | `logs/request_YYYY-MM-DD.log` | URL、GET 参数、POST body |
| `ResponseLog` | `logs/response_YYYY-MM-DD.log` | HTTP 状态码、返回体 |
| `BusinessLog` | `logs/business_YYYY-MM-DD.log` | 业务节点（如 `internal_error`） |

**初始化**：

```go
import "github.com/Huey1979/gocrux/internal/logger"

logger.Init("./logs") // 可选，默认为 ./logs
```

**安全特性**：即使不调用 `logger.Init()`，日志实例也已内置默认值（输出到 stderr），**不会因未初始化而 nil panic**。调用 `Init()` 后切换为按天滚动文件模式。

### InternalError 双重日志说明

当发生内部错误时，`handler.InternalError` 会**同时写入两套日志**：

```go
// ① 全局 logrus（受 config.yaml log 段控制，即时可见）
logrus.WithFields(...).Error("内部错误")

// ② 运行时 BusinessLog（按天滚动，带 request_id 串联请求链路）
logger.LogBusiness(c, "internal_error", ...)
```

这不是重复记录，而是**双通道保障**：
- **logrus 通道**：遵循配置的格式和输出（stdout/JSON/text），便于运维实时监控和日志采集
- **BusinessLog 通道**：按天独立文件 + `request_id` 串联完整请求链路，便于事后排查

### 管线 Trace 日志

框架在 **6 个管线**的入口和出口自动记录结构化 trace 日志，写入 `BusinessLog`：

| 管线 | 节点名 | 记录内容 |
|------|--------|---------|
| Create | `{svcName}.create.start` / `.end` | `count`、`elapsed_ms`、`error` |
| Update | `{svcName}.update.start` / `.end` | 同上 |
| Delete | `{svcName}.delete.start` / `.end` | `ids`、`elapsed_ms`、`error` |
| Get | `{svcName}.get.start` / `.end` | `id`、`code`、`elapsed_ms` |
| List | `{svcName}.list.start` / `.end` | `follow_published`、`elapsed_ms` |
| Activate | `{svcName}.activate.start` / `.end` | `id`、`elapsed_ms` |
| EditVersion | `{svcName}.edit_version.start` / `.end` | `id`、`elapsed_ms` |

每条日志自动携带 `log_id`（request_id），可串联同一请求的所有管线节点和级联子调用。日志在 `logs/business_YYYY-MM-DD.log` 中按 `TRACE` 级别输出。

**无需配置**——框架自动埋点，零侵入。

---

### KeywordFields — 关键字搜索

配置 `KeywordFields` 后，List 接口的 `?keyword=xxx` 参数自动对这些字段做 OR LIKE 搜索：

```go
HandlerConfig[entity.SysForm]{
    KeywordFields: []string{"form_code", "form_name"},
}
```

```http
GET /api/v1/form/list?keyword=员工&page=1&page_size=20
```

等价于 `WHERE form_code LIKE '%员工%' OR form_name LIKE '%员工%'`，与其它过滤器 AND 组合。

---

## 列表查询条件

### HTTP 查询方式

```http
GET /api/v1/sites/list?page=1&page_size=20&site_code=S001&keyword=xxx&order_by=created_at&order_dir=desc
```

URL query 参数自动转为 `map[string]any` 过滤条件。当值为切片时自动使用 `OpIn`。

### 结构化过滤（Repository 层）

```go
type ListFilters struct {
    Page     int      // 页码（>=1）
    PageSize int      // 每页条数（<=0 不分页）
    Filters  []Filter // 过滤条件
    Logic    string   // "and"（默认）或 "or"
    OrderBy  string   // 排序字段（DB 列名）
    OrderDir string   // "asc"（默认）或 "desc"
}

type Filter struct {
    Field string   // DB 列名
    Op    FilterOp // 操作符
    Value any      // 值
}
```

### 支持的操作符

| 操作符 | 常量 | SQL | Value 要求 |
|--------|------|-----|-----------|
| 等于 | `OpEQ` | `field = ?` | 单个值 |
| 不等于 | `OpNEQ` | `field != ?` | 单个值 |
| 模糊匹配 | `OpLike` | `field LIKE ?` | 字符串（需自行拼 `%`） |
| 大于 | `OpGT` | `field > ?` | 数字/时间 |
| 大于等于 | `OpGTE` | `field >= ?` | 数字/时间 |
| 小于 | `OpLT` | `field < ?` | 数字/时间 |
| 小于等于 | `OpLTE` | `field <= ?` | 数字/时间 |
| IN | `OpIn` | `field IN (?,?)` | 切片 |
| BETWEEN | `OpRange` | `field BETWEEN ? AND ?` | 长度为 2 的切片 |
| 原生 SQL | `OpRaw` | 直接拼接 SQL 片段 | `(string, []any)` 或仅 `string` |
| OR 组合 | `or_group` | 子条件 OR 连接，整体 AND 嵌入 | `[]Filter` 切片 |

### 使用示例

```go
records, total, err := repo.ListByFilters(ctx, repository.ListFilters{
    Filters: []repository.Filter{
        {Field: "status", Op: repository.OpEQ, Value: "active"},
        {Field: "name", Op: repository.OpLike, Value: "%测试%"},
        {Field: "level", Op: repository.OpGTE, Value: 3},
    },
    OrderBy:  "created_at",
    OrderDir: "desc",
    Page:     1,
    PageSize: 20,
})
```

### RawList — 原生查询

当 `ListByFilters` 无法表达复杂查询（JOIN、子查询、聚合等）时，可通过 `RawList` 直接执行原生 SQL / MQL：

```go
// MySQL: 直接执行 SQL
var results []MyJoinView
err := repo.RawList(ctx, &results,
    "SELECT a.*, b.name FROM form a LEFT JOIN form_field b ON a.form_ulid = b.form_ulid",
)

// MongoDB: 传入 bson.M 过滤器
var docs []entity.Product
err := repo.RawList(ctx, &docs, bson.M{"status": "active"})
```

`RawList` 是 `Repo[M]` 接口方法，MySQL（GORM）和 MongoDB 均支持。

---

## 配置文件

`config.yaml` 完整配置项说明：

### `app` — 应用配置

| 字段 | 类型 | 说明 |
|------|------|------|
| `name` | string | 应用名称 |
| `mode` | string | 运行模式：`debug` / `release` |
| `host` | string | 监听地址，如 `0.0.0.0` |
| `port` | int | 监听端口 |

### `mysql` — MySQL 配置

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `host` | string | | 数据库地址 |
| `port` | int | 3306 | 端口 |
| `user` | string | | 用户名 |
| `password` | string | | 密码 |
| `database` | string | | 数据库名 |
| `charset` | string | utf8mb4 | 字符集 |
| `max_open_conns` | int | 100 | 最大打开连接数 |
| `max_idle_conns` | int | 10 | 最大空闲连接数 |
| `max_life_time` | int | 3600 | 连接最大生命周期（秒） |

### `mongodb` — MongoDB 配置（业务数据）

| 字段 | 类型 | 说明 |
|------|------|------|
| `hosts` | []string | 地址列表 |
| `database` | string | 数据库名 |
| `username` | string | 用户名 |
| `password` | string | 密码 |
| `min_pool_size` | int | 最小连接池 |
| `max_pool_size` | int | 最大连接池 |

### `redis` — Redis 配置

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `host` | string | | 地址 |
| `port` | int | 6379 | 端口 |
| `password` | string | | 密码 |
| `db` | int | 0 | 数据库编号 |
| `pool_size` | int | 10 | 连接池大小 |

### `log` — 日志配置

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `level` | string | debug | 日志级别：`debug`/`info`/`warn`/`error` |
| `format` | string | json | 格式：`json`/`text` |
| `output` | string | stdout | 输出：`stdout`/`file` |
| `file.path` | string | | 文件路径 |
| `file.max_size` | int | 100 | 文件最大 MB |
| `file.max_backups` | int | 7 | 保留备份数 |
| `file.max_age` | int | 30 | 保留天数 |
| `file.compress` | bool | true | 是否压缩 |

### `security` — 安全配置

| 字段 | 类型 | 说明 |
|------|------|------|
| `jwt_secret` | string | JWT 签名密钥 |
| `jwt_expire` | int | JWT 过期时间（秒） |
| `salt` | string | 密码盐值 |

### `storage` — 存储配置

| 字段 | 类型 | 说明 |
|------|------|------|
| `type` | string | 存储类型：`local`/`oss`/`s3` |
| `local.base_path` | string | 本地存储路径 |
| `local.base_url` | string | 本地访问 URL |

### 加载配置

```go
import "github.com/Huey1979/gocrux/internal/config"

cfg, err := config.Load("config.yaml")
// 全局可通过 config.Cfg 访问
```

---

## 代码生成器 gentity

`tools/gentity` 是一个独立的 MySQL→Go 代码生成器，根据表结构自动生成实体定义和注册蓝图。同时支持**字段存在性检查**，可自动发现表中缺少的框架约定字段（`is_deleted`、`created_at/by`、`updated_at/by`）并生成 ALTER TABLE SQL。

### 安装

```bash
go build -o gentity.exe ./tools/gentity/
```

### 正常生成模式

```bash
# 单表生成
gentity --dsn "user:pass@tcp(localhost:3306)/db?charset=utf8mb4&parseTime=true" \
        --table users --out generated

# 全库生成
gentity --dsn "user:pass@tcp(localhost:3306)/db?charset=utf8mb4&parseTime=true" \
        --all --out generated

# 全库生成 + 字段映射 + 排除日志表
gentity --dsn "user:pass@tcp(localhost:3306)/db?charset=utf8mb4&parseTime=true" \
        --all \
        --field-config configs/gentity_fields.yaml \
        --out generated
```

### 检查模式（`--check`）

检查所有表（排除日志表）是否缺少框架约定字段，若缺失则生成 ALTER TABLE SQL 文件。

```bash
gentity --dsn "user:pass@tcp(localhost:3306)/db?charset=utf8mb4&parseTime=true" \
        --check \
        --field-config configs/gentity_fields.yaml \
        --out migration
# 输出: migration/migration_add_fields.sql
```

默认检查的 5 个必填字段：

| 框架字段 | MySQL 类型 | 默认值 |
|---------|-----------|--------|
| `is_deleted` | `tinyint(1)` | `0` |
| `created_at` | `datetime` | `CURRENT_TIMESTAMP` |
| `created_by` | `varchar(26)` | `''` |
| `updated_at` | `datetime` | `CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP` |
| `updated_by` | `varchar(26)` | `''` |

### 字段映射配置（`--field-config`）

通过 YAML 文件自定义框架字段在实际表中的列名。

**配置文件格式**（`configs/gentity_fields.example.yaml`）：

```yaml
field_mapping:
  is_deleted: del_flag      # 用 del_flag 替代 is_deleted
  created_by: creator       # 用 creator 替代 created_by
  updated_by: updater       # 用 updater 替代 updated_by
  deleted_at: deleted_time  # 用 deleted_time 替代 deleted_at
  created_at: gmt_create    # 用 gmt_create 替代 created_at
  updated_at: gmt_modified  # 用 gmt_modified 替代 updated_at

exclude_tables:             # 排除表（不检查也不生成）
  - sys_operation_log
  - sys_publish_history
```

- `field_mapping`：按需覆盖，未覆盖的字段使用默认列名
- `exclude_tables`：日志表等无需框架字段的特殊表，检查/生成时均跳过
- 检查模式下：若配置 `is_deleted: del_flag`，则检查表中是否有 `del_flag` 列

### 生成物

```
generated/
├── entity/
│   └── users.go            # struct + TableName + Record 接口实现
└── blueprint/
    ├── blueprints.go       # Blueprints 管理器
    └── users.go            # 注册蓝图（Repository→Service→Handler→Routes）
```

### Record 接口实现

生成的 entity 文件自动实现 `service.Record` 接口的全部方法：

| 方法 | 行为 |
|------|------|
| `SetDefaults()` | 遍历 DEFAULT 值，零值时回填 |
| `SetID()` | 主键为 `*_ulid` 生成 ULID；自增主键空操作 |
| `SetCreatedAt(t)` | 若表存在 `created_at`（或映射列）则赋值 |
| `SetCreatedBy(uid)` | 若表存在 `created_by`（或映射列）则赋值 |
| `SetUpdatedAt(t)` | 若表存在 `updated_at`（或映射列）则赋值 |
| `SetUpdatedBy(uid)` | 若表存在 `updated_by`（或映射列）则赋值 |
| `SupportsDraft()` | 检测 `version_status` 或 `is_draft` 列 |
| `SetDelete()` | `is_deleted` 列 → 赋值为 `1`（int8）；`deleted_at` 列 → 赋值为 `time.Now()`；否则返回 `false` |
| `PKField()` | 返回主键数据库列名 |
| `SelfFKField()` | 检测 `parent_ulid` 或 `parent_id` 列 |

### 自动类型映射

| MySQL 类型 | Go 类型 | 说明 |
|-----------|---------|------|
| `varchar`/`char`/`text`/`json` | `string` | |
| `int`/`int unsigned` | `int`/`uint` | |
| `bigint` | `int64` | |
| `decimal` | `float64` | |
| `datetime`/`timestamp` | `time.Time` | |
| `tinyint(1)` | `int8` | `is_deleted` 专用，与 heims 约定对齐 |

### 集成方式

1. 复制 `generated/entity/*.go` → 项目实体目录
2. 复制 `generated/blueprint/*.go` → 项目蓝图目录
3. 在主程序中注册

```go
blues := bp.NewBlueprints(svcReg, handlerReg)
blues.RegisterUser(apiGroup)
```

---

## 项目结构

```
gocrux/
├── cmd/                    # 入口示例
├── handler/                # HTTP 处理层
│   ├── generic.go          # GenericHandler 定义 + HandlerConfig
│   ├── generic_impl.go     # 内置 _before/_do/_after 默认实现 + expandCascadesBatch
│   ├── generic_read.go     # Get/List 入口 + expandGet + depth/ignore 注入
│   ├── generic_read_impl.go # _doList 批量展开 + ListSkipCascades + expand 控制
│   ├── generic_write.go    # Create/Update/Delete 入口 + createPipeline
│   ├── generic_write_impl.go # _doCreate/_doUpdate/_doDelete 级联编排
│   ├── generic_version.go  # Activate/ListVersions/EditVersion 入口
│   ├── generic_version_impl.go # 版本操作默认实现
│   ├── generic_util.go     # injectDepth/injectIgnore/injectStop + ResponseMapper + aux
│   ├── cascade.go          # depthCtx/ignoreCtx/visitedCtx/fieldLimitCtx + CascadeRelation/StopRule
│   ├── hooks.go            # HandlerHooks 钩子类型定义
│   ├── registry.go         # HandlerRegistry 注册表
│   ├── txcoordinator.go    # TxCoordinator 事务编排器
│   ├── request.go          # RequestFactory + MapRequest + map→struct 合并
│   ├── request_util.go     # BindJSON/BindQuery/GetPageParams 工具
│   ├── response.go         # Response 结构 + Success/Error/InternalError
│   ├── auth_hooks.go       # UserInfo + Authenticator + Authorizer 接口
│   ├── errors.go           # Service error → HTTP BusinessCode 映射
│   ├── validation.go       # 输入校验核心（类型转换 + 格式校验）
│   ├── validation_config.go # 校验规则 YAML 加载
│   └── utils.go            # extractPK/extractMapID/removeMapID
├── service/                # 业务逻辑层
│   ├── generic.go          # GenericService 定义 + Record/CrudRequest 接口 + Config
│   ├── generic_impl.go     # 内置 _before/_do/_after 默认实现
│   ├── generic_read_impl.go # _doList 读取实现（含软删除/draft 过滤）
│   ├── generic_write_impl.go # _doCreate/_doUpdate/_doDelete 写入实现（含版本化/级联）
│   ├── generic_version_impl.go # 版本操作实现（Activate/ListVersions/EditVersion）
│   ├── hooks.go            # Hooks 钩子类型定义
│   ├── registry.go         # ServiceRegistry 注册表
│   ├── request.go          # CrudRequest/Mergeable/Identifiable/Validatable 接口
│   ├── idempotency.go      # IdempotencyStore 幂等缓存
│   └── tx.go               # WithTx/GetTx 事务透传
├── repository/             # 数据访问层
│   ├── crud.go             # CRUDRepository 泛型仓储 + ListFilters + FilterOp
│   ├── base.go             # BaseRepository + VersionRepository
│   ├── dao.go              # BaseDAO（缓存/审计扩展点）
│   ├── repo.go             # Repo[M] 统一仓储接口
│   └── mongo_repo.go       # MongoCRUDRepository MongoDB 仓储
├── internal/               # 框架内部
│   ├── bootstrap/          # 启动引导（Init/InitMySQL/InitOther/Migrate/Close）
│   ├── config/             # 配置加载（Config/Load + 各配置段结构体）
│   ├── database/
│   │   ├── mysql/          # MySQL 连接 + AutoMigrate + 类型校验
│   │   ├── mongodb/        # MongoDB 连接
│   │   └── redis/          # Redis 连接
│   ├── logger/             # 日志系统（RequestLog/ResponseLog/BusinessLog + 按天滚动）
│   ├── middleware/          # HTTP 中间件（RequestLogger/Cors/Recovery/AuthMiddleware）
│   ├── model/entity/       # 框架内置实体（SysOperationLog）
│   └── router/             # 基础路由注册
├── common/                 # 通用工具
│   ├── ulid.go             # ULID 生成器
│   ├── reflect.go          # SetFieldValue 反射辅助
│   └── tx.go               # WithTx/GetTx context 事务传递
├── constants/              # 业务状态码（BusinessCode + 消息映射）
├── errors/                 # 哨兵错误 + 格式化错误函数
├── tools/gentity/          # 代码生成器（独立工具）
├── configs/                # YAML 配置样例
│   ├── gentity_fields.example.yaml   # gentity 字段映射
│   ├── validations.example.yaml      # 输入校验规则
│   └── menu.example.yaml             # 默认菜单配置
├── config.yaml             # 应用主配置（脱敏，不提交）
└── go.mod
```

---

## License

MIT
