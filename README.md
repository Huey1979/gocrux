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
  - [版本重建时的子记录引用重映射](#版本重建时的子记录引用重映射cascaderelationremaps)
  - [携带子表局部字段合并](#携带子表局部字段合并mergechildrenonupdate)
  - [跨级联批次的引用重映射](#跨级联批次的引用重映射remapkey--sourceremapkey)
  - [引用装配（v3：Target + Assemblies）](#引用装配v3target--assemblies)
  - [外部引用解析（引用已存在的实体）](#外部引用解析引用已存在的实体)
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

    // 创建 Handler（第二个参数是资源名，用于权限校验与日志，不能为空）
    h := handler.NewGenericHandlerWithSvc(svc, "site", handler.HandlerConfig[entity.Site]{
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
| `POST` | `/{prefix}/update` | 编辑记录（自动识别单条/批量，支持级联） |
| `POST` | `/{prefix}/batch-update` | 简单批量更新（SQL IN 统一赋值） |
| `POST` | `/{prefix}/delete` | 批量删除（支持 ids / codes 或同时传入） |

当 Service 启用 `VersionMode` 时，额外注册：

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/{prefix}/activate` | 激活版本（发布/回滚） |
| `GET` | `/{prefix}/versions` | 版本历史列表 |
| `POST` | `/{prefix}/edit-version` | 修改版本元数据 |

### 更新（update / batch-update）

框架提供两种更新模式：

#### update（支持单条/批量自动识别）

`POST /{prefix}/update` 自动识别 Body 是单对象还是数组，统一走 `updatePipeline`（校验→before→do→after），支持级联更新和版本管理：

```http
# 单条更新
POST /api/v1/sites/update
Content-Type: application/json

{"id": "01Jxxx1", "site_name": "站点A", "domains": [...]}

# 批量更新（含级联，逐条执行）
POST /api/v1/sites/update
Content-Type: application/json

[
  {"id": "01Jxxx1", "site_name": "站点A", "domains": [...]},
  {"id": "01Jxxx2", "site_name": "站点B"}
]
```

- 自动兼容单对象，批量时每条独立走管线
- 支持 `BatchErrorMode: "collect"` 收集全部校验错误

#### batch-update（SQL IN 统一赋值）

`POST /{prefix}/batch-update` 将相同字段值统一应用到多条记录，**不做级联更新**，**不支持版本化** handler。Body 为单对象（含 `ids` 数组）：

```http
POST /api/v1/sites/batch-update
Content-Type: application/json

{
  "ids": ["01Jxxx1", "01Jxxx2", "01Jxxx3"],
  "status": "active",
  "remark": "批量审核通过"
}
```

等价 SQL：`UPDATE sites SET status='active', remark='批量审核通过' WHERE site_ulid IN ('01Jxxx1','01Jxxx2','01Jxxx3')`

**限制**：
- **仅非版本化** handler 可用（版本化 handler 返回错误）
- 不做级联更新，仅更新主表
- Body 中的 `ids`、`id`、框架控制参数、级联字段会被自动剥离，其余全部作为 DB 列更新
- 自动补充 `updated_at` / `updated_by` 审计字段

**支持钩子**：`BeforeBatchUpdate(ids, updates) → DoBatchUpdate → AfterBatchUpdate(ids, updates)`，可在 before 中修改 ids/updates，在 after 中做缓存清理、事件通知等后续处理。**空 ids 语义（BUG-052）**：请求缺 ids 报 4001（Handler 层拦截）；`BeforeBatchUpdate` 过滤后 ids 为空时**无操作静默成功**（返回 200），不再报「缺少必需参数: ids」——与 SQL `WHERE pk IN ()` 无操作语义一致，供权限过滤等场景使用。

**适用场景**：批量审核、批量状态变更、批量打标等无需级联和逐条独立校验的场景。



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

#### bson tag 规则

**写入路径**：`MongoCRUDRepository` 的写入（`Insert`/`InsertBatch`/`Save`）通过 `toBsonDoc` 反射生成 BSON 文档，规则：

- 仅写入带 `bson` tag 的导出字段；`bson:"-"` 与无 tag 字段跳过；
- tag 带选项时取逗号前段为 key（`bson:"link_url,omitempty"` → key=`link_url`）；
- 匿名嵌入 struct 需显式标记 `bson:",inline"`（或 `bson:",inline,omitempty"`）才会递归展开（与 mongo-driver 语义一致），子字段遵循同样规则——可用于收敛审计字段等公共嵌入；未标记的匿名字段不展开；
- 主键推导（`detectPK`）与主键提取（`extractPKVal`）同样支持匿名嵌入 struct 递归查找。

**读取/过滤路径**：List 过滤的列名解析（`service/generic_impl.go` `resolveColumn`/`resolveColumnByName`/`resolveColumnFromDB`/`knownColumns`，统一经 `parseBsonKey`）同样取 bson tag 逗号前段，保证 `bson:"xxx,omitempty"` 字段可被前端按 `xxx` 过滤（BUG-061）。

### Repo[M] 接口

`repository/repo.go` 定义了统一的仓储接口，`CRUDRepository`（MySQL/GORM）与 `MongoCRUDRepository`（MongoDB）均实现此接口：

```go
type Repo[M any] interface {
    // Insert/InsertBatch 支持可选 explicitCols（存储列名白名单）：
    // 非空时仅显式写入这些列，使请求显式出现的零值字段（0/false/""）真实落库，
    // 不被 GORM 零值忽略 + DB 默认值覆盖（BUG-045）；空 = 默认行为（零值走 DB 默认值）。
    Insert(ctx context.Context, entity *M, explicitCols ...string) error
    InsertBatch(ctx context.Context, entities []*M, explicitCols ...string) error
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
h := handler.NewGenericHandlerWithSvc(svc, "product", handler.HandlerConfig[entity.Product]{
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

> **说明**：`SetID()` 已从接口移除——主键 ULID 由框架在 `_beforeCreate` 中自动生成（依据 `PKField()` 反向解析 Go 字段名），实体无需再实现。

### 软删除

- `SetDelete() bool` 返回 `true`：实体有 `is_deleted` 字段，删除时执行 `UPDATE SET is_deleted=1`
- 返回 `false`：物理删除，先写备份日志再 `DELETE`

`ServiceConfig` 中的 `DeletedField` / `DeletedValue` 控制 List 查询时自动添加的软删除过滤条件：

- `DeletedField`：软删除字段列名（默认 `"is_deleted"`）
- `DeletedValue`：未删除时的字段值（默认 `int8(0)`）

`_doList` 执行前会自动检查 `SetDelete()` 返回值：若为 `true` 则追加 `WHERE {DeletedField} = {DeletedValue}` 过滤器，确保列表查询不返回已软删除的记录。不支持软删的实体（`SetDelete()` 返回 `false`）不添加此过滤。

**按主键的读 / 写：不对称收口（BUG-069）**

仓储层按主键的读写（`GetByID` / `Save` / `UpdateByIDs`）不追加软删条件，且实体用自维护的 `is_deleted` 列而非 `gorm.DeletedAt`，GORM 不会自动过滤。服务层按「**读不管、写收口**」处理：

- **写路径收口**（`update` / `batch-update`）：目标是已软删记录 → `ErrRecordNotFound`（404）。非版本化不会被 `Save` 全行覆盖改写，版本化不会以已删旧行为底派生 `is_current=1` 的新版本行（避免「复活」）。`batch-update` 自动剔除已删 id，全部已删则无操作返回成功（与 BUG-052 空 ids 语义一致）。
- **版本化实体的另一条守卫**：更新 `is_current=0` 的**已废弃版本行**会被拒绝（`ErrUpdateDeprecatedVersion`，400）。该操作会以废弃行为底派生 `is_current=1` 新行，等于「复活 + 改写 + 凭空造新版本号」一步完成，且绕过 `Before/AfterActivate` 钩子与 `activate` 操作日志；正确路径是**先 `/activate` 再编辑**。草稿行 `is_current=1` 不受影响。
- **读路径不过滤**（`get`）：已删记录照常返回（携带 `is_deleted` 标记）。是否允许调用方查看已删数据属**业务权限语义**，由应用端在 `AfterGet` 钩子中用 `IsSoftDeleted()` 自行判定（无权则 403 / 置空字段）；框架不在主流程拦截 —— 否则回收站查看、恢复等场景会被彻底堵死。
- **恢复接口**：`POST /{prefix}/restore`，body `{"ids":[...]}`。**只把软删字段置回「未删值」，不改动任何业务字段**（恢复 ≠ 修改）；需要修改已删记录时**先 `restore` 再 `update`**。
  - 注册条件：**支持软删 + 非版本化**。物理删实体（`SetDelete()` 返回 `false`）无恢复语义，服务层调用返回 `ErrSoftDeleteNotSupported`（400）；版本化实体的删除 = 废弃（只写 `is_current=0` / `version_status=deprecated`，从不写 `is_deleted`），restore 对其恒为空操作，故不注册路由、服务层调用返回 `ErrUseActivateInstead`（400）——恢复当前版本请走 `/activate`。
  - ⚠️ **权限注意**：`Restore` 直连仓储层，**不经过** `BeforeUpdate` / `BeforeBatchUpdate` 钩子链，业务级权限与校验**必须挂在 `BeforeRestore`**（唯一拦截点），挂在 update 钩子上对 restore 无效。
  - `EnableOpLog` 时写 `operation="restore"` 操作日志；`ids` 含不存在主键时不报错（无操作成功，与 BUG-052 语义一致）；对未删记录调用为幂等空操作。
  - 当前**不做级联恢复**，子表如需同步恢复，由应用端在 `DoRestore` 钩子中扩展。
- 不支持软删的实体不判定、不产生额外查询，行为完全不变。

判定由导出方法 `DeletedColumn()`（复用 `DeletedField` / `DeletedValue` 配置）与 `IsSoftDeleted(*M)` 完成，值比较跨类型归一（`int8` / `int` / `bool` / `string`），避免实体字段类型与配置默认 `int8(0)` 不一致时误判。

### SupportsDraft

- 返回 `true` 时实体需提供 `VersionStatus` 字段（通过 `VersionFieldMapping` 映射）
- 版本化模式下，Update 创建新草稿；Activate 发布草稿为正式版本

### 框架级写入兜底（BUG-044 / BUG-045 / BUG-049）

写入管线（Create / Update / 版本化 Update）在 MergeTo 之后、入库之前自动执行两项兜底，无需业务配置：

**type:json 空串归一化（BUG-044）** — string 类型 + gorm tag 含 `type:json` 的字段，若 MergeTo 后值为 `""`，自动改写为 `"null"`。MySQL JSON 列不接受空字符串（Error 3140），实体 `SetDefaults()` 兜底防不了显式传空串（MergeTo 覆盖），由框架层统一归一化。`"null"` 对任意 JSON 目标类型（slice/map/struct）均合法，语义中性（表示"无配置"）。

**type:json 原生 JSON 传参（BUG-055）** — 自动字段校验（`deriveFieldRules`）将 `gorm:"type:json"` 的 string 字段推导为 `Type=string, Format=json`；Create/Update 请求可直接传**原生 JSON 数组/对象**（`["01KW_TECH_DEPT"]` / `{"op":"eq"}`），校验层自动 `json.Marshal` 序列化为 JSON 字符串落库，不再报「无法转为字符串类型」。`json.RawMessage` 字段同样支持（先 `json.Valid` 校验）。兼容既有传 JSON 字符串的调用方（`string` 分支原样通过）。**空容器同样归一化（BUG-064）** — 非必填字段传**空数组 `[]` / 空对象 `{}`** 不再被「空值跳过校验」提前放行，而是继续序列化为 `"[]"` / `"{}"` 落库（`validateField` 的空值跳过仅对标量 nil/空字符串生效）；required 字段传空数组仍报「必填」。

**显式零值字段真实落库（BUG-045 / BUG-046 / BUG-047）** — Create / 版本化 Update 插入时，请求中**显式出现**的零值字段（`0`/`false`/`""`）真实落库，不被 GORM 零值忽略 + DB 列默认值覆盖（如 `is_enabled=0` 不再落库变 `1`）。白名单 = 实体非零字段列 ∪ 请求显式字段列；请求未显式传的零值字段仍走 DB 默认值，行为与旧版一致。实现依赖可选接口 `RequestFields`（`MapRequest` 已内置实现 `Data()`），业务自定义 Request 实现 `Data() map[string]any` 即可生效。

实现细节：白名单存在时，插入走 GORM `Select(白名单).Create`（struct 路径）。**方案 B（default 只允许 0 值/无）** 后实体不再携带非零 `default:` tag（gentity 生成 `default:(-)`，非零 DB 默认值语义由 `SetDefaults()` 在 Go 层承担），GORM create 回调不再用默认值覆盖零值字段，显式 `0` 原样落库；此前为绕过 GORM default 填充而引入的 map 批量插入（`EntityToMapByColumns`，BUG-047 方案 A）已回退移除。列名解析对齐 `resolveColumn`：gorm `column:` → bson tag → snake 约定兜底（BUG-046）。

**裸 Save 审计时间回填（BUG-049）** — `repository.CRUDRepository.Save` 与 `BaseDAO.Update` 写库前统一回填零值的审计时间字段（`CreatedAt`/`UpdatedAt`，`time.Time{}` 或 nil 指针）。裸 `db.Save` 全字段写回，业务调用方（如版本化 `_doActivate`）未设 `CreatedAt` 时零值时间戳写 MySQL 触发 Error 1292 `'0000-00-00'`（BUG-049）。

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

**`EnableOpLog`** — 启用后，Create/Update/Delete/Activate/Restore 完成时自动写入操作日志。

> ⚠️ **开关与写入方必须同时具备**（BUG-077）。`EnableOpLog: true` 但未注入写入方时，
> 构造期会打印一条 `Warn` 告警，且不写任何记录（而不是静默假装成功）。

三种注入口任选其一（均可在模块外编译）：

```go
// ① 落内置 sys_operation_log 表（最省事）
svc.SetOpLogDB(db) // db *gorm.DB

// ② 自定义落库（Mongo / 文件 / MQ），可拿到前后快照
svc.SetOpLogWriter(myWriter) // 实现 service.OpLogWriter

// ③ 兼容旧写法（仅模块内可用，参数类型在 internal/）
svc.SetOpLogRepo(repository.NewCRUDRepository[entity.SysOperationLog]())
```

自定义写入方接口：

```go
type OpLogWriter interface {
    WriteOpLogs(ctx context.Context, records []OpLogRecord) error
}

// OpLogRecord 字段：
//   EntityType / EntityID / Operation / OperatorULID / RequestID / OperatedAt
//   RecordBefore / RecordAfter  json.RawMessage（可选前后快照，update/delete/activate 填充）
```

写入失败只记 `Error` 日志，不会让主业务失败（审计是尽力而为，但**绝不静默**）。

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

#### 空串语义：Create 保护默认值，Update 允许清空（BUG-072）

`MapRequest` 提供两个合并方法，区别只在「请求中显式提交的空串」如何处理：

| 方法 | 使用场景 | 显式传 `""` 的效果 | 未提交的字段 |
|---|---|---|---|
| `MergeTo` | Create（默认） | **不覆盖** target 上的非空值（保护 `SetDefaults()`） | 保持原值 |
| `MergeToExisting` | Update | **原样写入**（可把字符串清空） | 保持原值 |

框架内部已自动分派：`_beforeCreate` 走 `MergeTo`，`_beforeUpdate` / `_beforeUpdateVersioned` 走
`MergeToExisting`。自定义 `Request` 类型若也想支持「清空」，实现可选接口即可：

```go
type MergeableExisting[M Record] interface {
    MergeToExisting(target *M) error
}
```

**未实现该接口时框架自动回退 `MergeTo`**，行为与升级前完全一致（向后兼容，老接入方零改动）。

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

Get 场景：单次查父实体；List 场景：收集所有 FK 值 → 批量展开。

**引用解析语义（BUG-070 / BUG-080）**：引用展开走 `DoResolve`（引用解析模式），**不套用**「当前有效」过滤（软删 `is_deleted=0` / `is_current=1` / 必须 published）——引用的是外部对象，即使目标被软删或是历史版本，也应保留锚点，并保留 `is_deleted`、`version_status` 等状态字段供调用方判断。**单条 `get` 与列表批量展开走同一入口**（`resolveRefs`），语义完全一致。

> **BUG-080**：References 分支此前漏改（4 个引用展开调用点只改了 3 个），单条 `get` 仍用 `DoGetByID` 单查 + 出错即中断，导致「被引用的父记录缺失」被经 `%w` 穿透后统一映射成 **404「本条记录不存在」**——同一主键 `get` 返回 404、`list` 却照常返回该行。现已统一：缺失落 `missing` 占位，坏引用**不再中断同一次展开**的 ChildRefs / Cascades，只有真错误（DB 故障、权限失败等）才上抛。

> **例外：草稿（未发布）不被放开。** 引用解析模式仍拦截 `version_status=draft`：未登录一律不可见，登录后仅创建者本人可见。未发布草稿不因被某条记录引用而对外暴露。历史版本（`deprecated`）与已发布版本不受影响。

引用目标缺失时返回占位对象 `{<主键输出名>: <id>, "missing": true}` —— **输出名即 JSON 字段名**（如 `ulid`，而非数据库列名 `field_ulid`），与同一数组中正常记录的结构一致；只含引用键与 `missing`，不泄露其它字段。输出名取自**引用目标 Handler 自身**的实体（`refOutputKey`，可选接口 `pkOutputKeyer`，第三方 `CascadeHandler` 未实现时回退其 `PKField()`）。

**引用解析失败不映射为 404（BUG-080）**：`ErrRefResolve` / `ErrRefBatchResolve` / `ErrChildRefResolve` / `ErrChildRefBatchResolve` 包装的错误统一映射为 **500**，即使其 cause 链上含 `ErrRecordNotFound`（`mapServiceError` 经 `errs.IsRefResolveError` 先行拦截）。响应消息带 handler 名（如「向上级联解析 notification_channel 失败」），调用方足以区分「我没这条」与「我这条的引用坏了」。普通 not-found 仍是 404。


对比：向下级联 `Cascades`（父表拥有的子集合）仍走 `DoList` 并按当前有效过滤 —— 删掉的订单明细不应再出现在订单详情里，这是期望行为。

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

**引用解析语义（BUG-070）**：与 References 一致 —— 展开走 `DoResolve`（引用解析模式），不做软删 / 当前版本 / 必须 published 的过滤，被软删或历史版本的目标仍作为锚点返回并保留状态字段；未发布草稿仍受可见性保护（见上一节）。目标缺失时以 `{<主键输出名>: <id>, "missing": true}` 占位（输出名 = JSON 字段名，取自目标 Handler 自身，见上一节 `refOutputKey`），**数组长度与原始 ID 列表对齐**，让调用方能区分「引用为空」与「目标已删除/缺失」。

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

**嵌套子表字段裁剪（`key:sub` 语法）**

`ListSkipFields` 和 `ListKeepFields` 均支持 `key:sub` 语法，可穿透到级联/Reference 展开的嵌套子实体进行字段裁剪：

```go
// 跳过 content 字段，同时跳过 notify_content 展开实体中的 body 和 raw_data 字段
ListSkipFields: []string{"content", "notify_content:body", "notify_content:raw_data"}

// 保留 id、title 主字段，同时仅保留 notify_content 展开实体中的 title、sender_name 字段
ListKeepFields: []string{"id", "title", "notify_content:title", "notify_content:sender_name"}
```

**`?fields=` HTTP 参数** 也同样支持嵌套裁剪（用于 Get/List 接口）：

```http
GET /api/v1/notify-delivery/list?fields=id;title;notify_content:title;notify_content:senderName
GET /api/v1/sites/get?id=xxx&fields=site_name;domains:name;domains:status
```

- 规则分隔符：`;`
- 嵌套分隔符：`:`
- 字段缺失时不报错，静默跳过

**优先级**：`ListSkipFields` > `ListKeepFields`。两者同时配置时 hanya 生效 Skip，Keep 被忽略。均未配置时全字段返回（向后兼容）。

**执行时机**：所有级联展开（References/ChildRefs/Cascades）完成之后执行，嵌套裁剪可对展开后的子实体数据进行控量。

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
h := handler.NewGenericHandlerWithSvc(svc, "site", cfg)
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

### 路由级禁用（DisabledRoutes，BUG-071）

`RegisterRoutes` 默认全量注册。若某个实体只是业务流程的**落库载体**、不允许外部直写，用 `DisabledRoutes` 关闭对应入口：

```go
handler.HandlerConfig[*entity.ShareRecord]{
    PathPrefix:     "/api/v1/share-record",
    DisabledRoutes: []string{"POST /create", "POST /delete"},
}
```

语义要点：

- **不注册**命中的路由 —— 请求表现为「路由不存在」（404），**不是 403**。403 的含义是「入口存在但你不被允许」，会暴露端点存在性并误导调用方去查权限配置。
- **只作用于 HTTP 层**：handler 结构体与 `Create` / `Delete` 等方法全部保留，`DoCreate` / `DoList` / `DoGetByID` 等供级联、钩子、内部后处理调用的入口行为完全不变；`Cascades` / `References` / `ChildRefs` 展开与其它路由上的钩子也不受影响。
- 路径可写**短路径**（相对 `PathPrefix`，推荐）或**全路径**，两种都接受：`"POST /create"` ≡ `"POST /api/v1/share-record/create"`。
- 可禁用任意标准路由（含 `/restore`、`/activate`、`/versions` 等）：method 仅支持 `GET` / `POST`。
- **配置写错直接 panic**（fail-fast）：未知 method（`"POSTT /create"`）或未知路径（`"POST /nope"`）会在构造 Handler 时报错，避免「以为禁了其实没禁」。
- 默认 `nil` = 全量注册，向后兼容。

> 不要用 `BeforeCreate` 返回 `403` 来替代：钩子在进入业务函数之后才拦截，请求已走完鉴权、参数解析与 `checkPerm`，探测者能借此确认端点存在与字段名。

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
| **List** | 分页参数（`page`/`pageNum`/`page_num`≥1, `page_size`/`pageSize`∈[1,100], `offset`≥0 & `size`∈[1,100], `order_dir`∈{asc\|desc}）+ 过滤字段 |

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

**业务校验错误码（BUG-058）** — 业务校验/钩子返回的普通错误默认映射为 `code=500`。需要返回自定义业务码时，使用 `errs.NewBizError(code, msg)`：

```go
// Service 层钩子中
if existing {
    return nil, errs.NewBizError(15002, "同目录下已存在同名文件或文件夹")
}
```

`handler.handleError` 通过 `errors.As` 识别 `*errs.BizError`，响应携带对应业务码 + 消息，且不记录 Error 级日志（预期业务失败，避免日志噪音）；支持 `fmt.Errorf("...: %w", err)` 嵌套包装。现有哨兵错误（`ErrRecordNotFound`/`ErrMissingParam`/`ErrDuplicateCode` 等）映射优先级不变。

### BatchErrorMode — 批量错误收集

`HandlerConfig.BatchErrorMode` 控制批量操作（Create / update 数组模式）时的错误处理行为：

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
| 有子数据 + 非版本化 + 已有旧子记录 | 先删旧子记录 → 全量替换为新子数据；配 `MergeChildrenOnUpdate` 时替换内容为「旧子记录 ⊕ 请求字段」 |
| 有子数据 + 版本化 | 创建新版本子记录（保留旧版本关联）；配 `MergeChildrenOnUpdate` 时先与旧子记录逐字段合并，未提交字段不丢（见下节） |
| 无子数据 + 非版本化 + 已有旧子记录 | 回填旧子数据 → 更新 FK 指向新父实体（子行原地更新，PK 不变） |
| 无子数据 + 版本化 + 已有旧子记录 | 回填旧子数据 → **清除子行 PK 走 CREATE 复制重建**为新版本快照（新子行 PK/ULID，旧版本子表保持不变，BUG-059） |
| 无子数据 + 无旧子记录 | 跳过 |

### 版本重建时的子记录引用重映射（`CascadeRelation.Remaps`）

版本化实体更新时框架会为父实体建新版本、并把子表**复制重建**为新 ULID。
若子记录之间存在横向引用（`field_access.field_ulid → write_field.field_ulid`、
`flow_branch.target_node_ulid → flow_node.node_ulid` 等），不重写就会让新版本
指向**旧版本**子记录 —— 发布看起来成功，运行时却关联到旧字段/旧节点/旧分支。

`Remaps` 声明式解决该问题，在「新 ULID 全部生成后、子记录落库前」统一重写：

```go
CascadeRelation{
    HandlerName:   "form_write_field",
    ChildrenField: "write_fields",
    FKField:       "form_ulid",
    OnCreate:      true,
    OnUpdate:      true,
    Remaps: []handler.ReferenceRemap{{
        SourceCodeField: "field_code",     // 本批子记录的业务 code（用于兜底）
        Bindings: []handler.ReferenceBinding{
            // 标量 ULID：{"field_ulid": "01..."}
            {Field: "ref_field_ulid", Mode: handler.RemapModeScalar},
            // ULID 数组：{"source_node_ulids": ["01...", "02..."]}
            {Field: "source_node_ulids", Mode: handler.RemapModeULIDArray},
            // 对象数组：{"error_on": [{"field_ulid": "01...", "field_code": "amount"}]}
            {Field: "error_on", Mode: handler.RemapModeObjectArray,
                ULIDKey: "field_ulid", CodeKey: "field_code"},
            // 嵌套 JSON（点号路径）：{"target": {"field_ulid": "01..."}}
            {Field: "target.field_ulid", Mode: handler.RemapModeScalar},
        },
    }},
}
```

**别忘了在子 Handler 上安装执行钩子**（父侧只是登记声明，子侧负责在落库前执行）：

```go
childH := handler.NewGenericHandlerWithSvc(childSvc, "form_write_field", childCfg)
childH.InstallRemapHook()
```

各子实体引用形态与取值口径：

| 形态 | `Mode` | 说明 |
|:--|:--|:--|
| 标量 ULID | `RemapModeScalar` | 单值字段；支持点号路径定位嵌套 JSON |
| ULID 数组 | `RemapModeULIDArray` | 数组中每个 ULID 都重写 |
| 对象数组 | `RemapModeObjectArray` | 按 `ULIDKey` / `CodeKey` 逐元素重写 |

映射优先级（与 heims「ULID 为权威、code 为辅助」口径一致）：

1. **旧 ULID → 新 ULID**（权威，优先）；
2. 旧 ULID 匹配不上时，**code → 新 ULID** 兜底（老数据只有 code 时用）；
3. ULID 与 code 同时存在且指向**不同**目标 → 报 `ErrRemapInconsistent`，不静默取其一；
4. 引用在本批次找不到任何目标 → 报 `ErrRemapUnresolved`，**让事务失败**，
   绝不静默保留旧 ULID（否则会落库一个「发布成功但引用悬空」的版本）。

> **JSON 列双形态**：`type:json` 的 string 列在实体里是字符串，重写后会按原形态
> 编码回去（不会把 JSON 字符串变成 Go 数组导致落库失败）。
>
> **旧快照不可变**：重写只作用于**本批次新建**的子记录，旧版本子行保持不变
> （测试 `TestRemapUpdateRewritesScalarRefToNewVersion` 含此断言）。
>
> **业务自定义映射**：引用判定规则无法声明式表达时，让子实体实现
> `handler.ReferenceRemapper`（`RemapReferences(ctx, childData) (map[string]string, error)`），
> 返回值会并入 `oldToNew`（同键以业务侧为准）。

### 携带子表局部字段合并（`MergeChildrenOnUpdate`）

父表 update 携带子表时，框架默认把**请求子数据当作子记录的完整内容**：版本化父
清 PK 重建新快照、非版本化父删旧行后全量替换。若调用方按「提交哪些字段就改哪些
字段」的契约只提交了部分字段，未提交字段就会退化为零值/默认值：

```text
旧子记录: {col_code:"c1", title:"列1", unit:"px", width:120, expr:"a+b"}
请求携带: {"ulid":"<旧ULID>", "expr":"a-b"}
默认结果: {col_code:"c1", title:"", unit:"", width:0, expr:"a-b"}   ← title/unit/width 丢失
```

置位后，**两条父路径都**先按「旧记录为基底 + 请求字段覆盖」合并，再走各自既有语义：

```go
CascadeRelation{
    HandlerName:   "form_list_column",
    ChildrenField: "list_columns",
    FKField:       "form_ulid",
    OnCreate:      true,
    OnUpdate:      true,
    MergeChildrenOnUpdate: true,       // ← 携带子表局部更新不丢字段（默认 false）
    PublishCodeField:      "col_code", // ← 业务 code 兜底身份键（可选）
}
```

| 情形 | 行为 |
|:--|:--|
| 字段未传 | 保留旧值（以旧记录为基底） |
| 字段显式置空（`""` / `null`） | 覆盖为空（key 存在即覆盖） |
| 数组里的子记录无对应旧记录 | 视为**新增**，按请求字段创建 |
| 旧子记录不在数组里 | 删除（**数组即子记录集合**） |

身份匹配（「这条请求记录对应哪条旧记录」）优先用请求携带的**旧主键**
（`ulid` / 实体 PK 字段），未命中时回退 `PublishCodeField` 声明的**业务 code**；
两者都命中不了 → 视为新增。业务 code 在旧记录里不唯一时返回
`errs.ErrAssemblyIdentityAmbiguous`，绝不按位置猜测。

合并不改变各路径对主键与旧行的既有处理：

| 父类型 | 开启后 |
|:--|:--|
| 版本化父 | 合并 → 清 PK 重建新版本子快照；旧版本子行**不做任何修改** |
| 非版本化父 | 合并 → 仍删旧子行并全量替换，但替换内容是合并结果（未提交字段不归零） |

生效范围：`OnUpdate` + 请求**携带子表** + 已有旧子记录。未携带子表时仍走 DB 回填
（本就带全字段），无需合并。默认 `false`，即两条路径都保持既有契约不变。

> `MergeChildrenOnVersionedRebuild` 是同一开关的**旧名**（早期语义只覆盖版本化重建），
> 仍可作为兼容别名使用，两者任一为 `true` 即生效；新代码请用 `MergeChildrenOnUpdate`。

### 跨级联批次的引用重映射（`RemapKey` / `SourceRemapKey`）

`Remaps` 的映射表只在**当前这一个级联关系的子数据批次内**构建，因此只能重写同一批
子记录内部的引用。但真实结构里引用常常**跨级联分支**：

```
form
  ├─ write_section → write_field          ← 新 ULID 的生成源
  ├─ list_column.field_ulid               ┐
  ├─ detail_section → detail_field        ├ 引用 write_field
  └─ validation.error_on[].field_ulid     ┘
```

`list_column` 等分支**看不到** `write_field` 的记录（不是它的子数据），
无从自建映射，结果新版本会静默引用旧版本的字段。

`RemapKey` / `SourceRemapKey` 提供**事务内、跨级联批次**的映射发布/消费：

```go
// 发布方：write_field 批次执行完毕后把「旧→新 / code→新」交付给 catalog
CascadeRelation{
    HandlerName:      "form_write_field",
    ChildrenField:    "write_fields",
    FKField:          "form_ulid",
    OnCreate:         true,
    OnUpdate:         true,
    RemapKey:         "form.write_field",  // ← 发布键（命名空间）
    PublishCodeField: "field_code",        // ← 发布 code→新ULID 兜底映射
}

// 消费方：从 catalog 取映射重写自己的引用
CascadeRelation{
    HandlerName:   "form_list_column",
    ChildrenField: "list_columns",
    FKField:       "form_ulid",
    OnCreate:      true,
    OnUpdate:      true,
    Remaps: []handler.ReferenceRemap{{
        SourceRemapKey: "form.write_field",       // ← 消费键（留空 = v1 批内行为）
        Bindings: []handler.ReferenceBinding{
            // 标量 + 同级 code：字段缺失时也能仅凭 code 补写
            {Field: "field_ulid", CodeField: "field_code", Mode: handler.RemapModeScalar},
        },
    }},
}
```

> **向后兼容**：`SourceRemapKey` 留空 = 沿用 v1 批内行为；两个新键都不配置时
> 行为与 v1 完全一致（零改动接入）。

**执行顺序契约（重要）**：发布方必须在消费方**之前**执行。框架提供三层校验：

| 层 | 检查 | 时机 | 失败表现 |
|:--|:--|:--|:--|
| L1 | `SourceRemapKey` 存在发布方 | 启动期 | `ValidateRemapKeys()` 返回 error（聚合报告全部缺失 key） |
| L2 | 同一 `Cascades` 数组内发布方更靠前 | 构造期 | **panic**（fail-fast） |
| L3 | 跨 Handler 子树时消费点能否取到映射 | 运行时 | `ErrRemapSourceMissing`（文案含发布方 Handler 名） |

```go
// L1：在所有 Handler 构造 + SetHandlerReg 完成之后调用一次
if err := formHandler.ValidateRemapKeys(); err != nil {
    log.Fatalf("remap 配置错误: %v", err)
}
```

L2 只校验**同一数组内**的顺序：若发布方在别的 Handler 子树里（如上例的
`write_field` 位于 `write_section` 下），静态无法推断执行序，L2 会**跳过**
交由 L1 做存在性校验、L3 做运行时兜底。因此跨子树时顺序由调用方保证，
**建议在应用侧加一条顺序回归测试**。

其他约定：

- **catalog 生命周期严格绑定事务**（随 `ctx` 存亡，不跨请求/不跨事务），
  由 `TxCoordinator.Run` 入口创建；
- **同命名空间多批发布合并**：同一 old ULID / code 映射到不同新目标 →
  `ErrRemapInconsistent`（不静默取其一）；映射到相同目标 → 幂等忽略；
- **`Publish` 是原子的**：整批先校验、全部通过后才写入。任一条冲突则本次调用
  不留任何痕迹（不会出现「前半批已落、后半批报错」的部分映射）；
- **`Resolve` 是只读快照**：多个消费方可重复读取互不影响，返回的是**调用时刻**的
  合并结果（若消费方之间又有新发布方写入，后者能看到更新内容）；
- **批内与跨批次可混用**：同一 `CascadeRelation` 的 `Remaps` 里既可写批内声明
  （无 `SourceRemapKey`），也可写跨批次声明，框架**合并两类映射**后统一重写；
  合并时同 key 冲突 → `ErrRemapInconsistent`（旧版本曾静默丢弃批内声明）；
- **空 code / 空旧 ULID 不参与映射且不报错**（必填约束属应用侧发布校验）；
- **错误三分**（便于区分配置问题与数据问题）：命名空间无发布方
  `ErrRemapSourceMissing` / 值不在映射中 `ErrRemapUnresolved` /
  ULID 与 code 冲突 `ErrRemapInconsistent`。

> **L3 文案会给出发布方 Handler 名**：发布方声明 `RemapKey` 的位置是
> **父 Handler 的 `Cascades`**（发布方子 Handler 自身配置里没有该键），
> 因此框架由父 Handler 的 `Cascades` 反查 `RemapKey == <缺失的 key>` 的关系，
> 取其 `HandlerName` 写入提示。若该 key 完全未声明则只报 key（无从得知发布方是谁）。
>
> **发布是增量累积的（批量安全）**：同一批次可能被 service 拆成**多次**
> `BeforeCreatePersist` 调用送达（版本化更新逐条 `Update` 时每次只带一个实体）。
> 框架因此不用「一次性完成」语义，而是：每次调用只重写本次送达的记录、
> 累积已处理的索引、并把**当前已累积的全部映射**增量发布。
> 尚未处理完的记录（其 `ulid` 仍等于旧主键）**不会被发布** ——
> 否则会产出 `旧ULID → 旧ULID` 这种指向旧版本的错误映射，比报错更糟。

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

### 引用装配（v3：`Target` + `Assemblies`）

> 设计文档：`doc/design_ulid_preallocation_2026-09-21.md`（三方确认口径）

v1/v2 的「事后重映射」把 ULID 的确定时机留在落库那一刻，因此必须靠映射表、
顺序校验与 catalog 把引用"追回来"。v3 换掉时点：**请求入口预分配 ULID，
引用在落库前一次性写对**，因此不再需要映射传递。

三阶段（这是「无装配顺序依赖」的关键）：

```
阶段 1：展开请求树 + 为整棵树预分配 ULID + 登记目标索引
阶段 2：基于完整索引统一装配所有引用
阶段 3：各 Handler 正常落库（顺序任意）
```

**1 与 2 绝不能合并**：若"边展开边装配"，消费分支可能在目标分支登记前就装配
—— 那正是「装配可见性依赖」，会重新需要 v2 的顺序校验与 catalog。

#### 发布方：`CascadeRelation.Target`

```go
CascadeRelation{
    HandlerName:   "form_write_field",
    ChildrenField: "write_fields",
    FKField:       "form_ulid",
    OnCreate:      true,
    Target:        "form.write_field",   // ← 本批记录作为装配目标（供他处引用）
}
```

#### 消费方：`CascadeRelation.Assemblies`

```go
CascadeRelation{
    HandlerName:   "form_list_column",
    ChildrenField: "list_columns",
    OnCreate:      true,
    Assemblies: []ReferenceAssembly{{
        Source: "field_ulid",                   // 引用字段（路径）
        Target: "form.write_field",             // ULID 来源；留空 = 本批次自身
        Match:  map[string]string{"field_code": "field_code"},
        Assign: map[string]string{"field_ulid": "ulid"},
    }},
}
```

`Source` 路径形态：`field_ulid`（标量）/ `error_on[]`（逐元素）/
`options.target`（固定嵌套）/ `$` 或留空（当前子记录本身）。只支持**一层**数组标记。

#### 存在性判定（最容易踩错的一条）

判定依据是**引用字段（`Assign` 的目标键）的 key 是否存在**，而不是值是否为空：

| 源数据形态 | 处理 |
|:--|:--|
| key 不存在 | code-only 引用 → 跳过，**不生成、不补写** ULID |
| key 存在，值为 `""` / `null` | ULID-bearing 模式 → 按 `Match` 回填本批次新 ULID |
| key 存在，值为旧 ULID | 重写为本批次新 ULID |
| key 存在，值已是本批次新 ULID | 幂等跳过（`Idempotent`） |
| `Match` 匹配不到 | WARN + 保留旧值（应用侧发布门禁负责 fail-closed） |
| `Match` 命中多条 | **报错** `ErrAssemblyAmbiguous`（匹配键唯一性是前提） |

不能拿 `Match` 键（如 `field_code`）判定存在性 —— code-only 引用同样带 `field_code`，
用它判定会把「只传 code」的引用偷偷升级成 ULID。

对象数组（`error_on[]`）按元素**独立**判定：某元素不带 `field_ulid` key → 该元素跳过，
不影响同一数组里的其它元素。批内自引用（`Target` 留空）时，候选集合会排除
正在装配的那条记录自身（否则「记录引用自己」会被判为命中多条）。

#### 凭证闭环（区分「自己生成的」与「前端伪造的」）

预分配时把 ULID 记入**请求级注册表**（挂 ctx、进程内存、随请求释放），
`service._beforeCreate` 落库前校验：

- PK 非空且在注册表中 → 框架生成 → 信任，直接落库（不重新生成）；
- PK 非空但不在注册表中 → 外部传入 → **重新生成**（子表批次一律不登记外部 PK）；
- 未挂载注册表（未迁移的调用方）→ 保持既有「非空即信任」语义，零影响。

> 顶层实体例外：请求显式传入的顶层主键仍按既有语义沿用（向后兼容），
> 因此防伪强度是「子表严于顶层」。

默认实现零配置；`SetTicketVerifier` 可替换为 HMAC / Redis 等实现（语义等价）。

#### 主键冲突（概率 ≈ 1.2e-24）

```go
tc := handler.NewTxCoordinator(db, nil).SetRetryOnPKConflict(true)  // 有事务：整树回滚 + 重试 1 次
h.txCoord.SetRetryOnPKConflict(false)                              // 无事务：不重试
```

冲突错误统一为 `errs.ErrAssemblyPKConflict`（MySQL 1062 / SQLite UNIQUE /
Mongo E11000 均可识别），文案提示**重新发起整个请求**（重放同一请求体会复用旧 ULID）。
无事务部署下走「尽力标删本次已写入记录 + ERROR 日志」，主流程不依赖清理成功。

#### 嵌套级联：阶段 1/2 按**整棵** Cascade 树递归

这是 v3 的关键前提。若阶段 1/2 只处理「本 Handler 的直接子批次」，那么
「发布方在孙批次、消费方在祖批次的直接子批次」的树形（heims 表单域的常态：

```text
form
  ├─ form_write_section → form_write_field    ← 发布方在**孙批次**
  ├─ form_list_column                         ← 消费方在直接子批次
  └─ form_validation         （error_on[]）
```

）会在祖批次的阶段 2 装配时**看不到**孙批次（孙批次要到阶段 3 才被登记）→
只能 Unmatched、保留空 ULID。

因此：

```text
阶段 1（递归）：展开整棵树 → 每批预分配 ULID → 每个非空 Target 立即登记索引
阶段 2（递归）：对每一批执行本关系的 Assemblies（此刻全树索引已完整）
阶段 2'（后序）：每层的装配后处理（见下方规范化钩子），子层先于父层
阶段 3（递归落库）：保持既有 DoCreate / DoUpdate 顺序
```

- 递归复用**同一批 `childData` 句柄**（阶段 3 把「本批次子树」随 ctx 下传给
  嵌套 Handler，嵌套层据此跳过阶段 1/2），因此「分配给 A 的 ULID 一定用在 A 上」；
- 深度与防环沿用既有 `depth` / `visited` 机制，且**只有**在该批记录已持有
  权威 PK 时才继续下钻（v2 通道的 ULID 要落库时才生成，其子树仍由嵌套
  Handler 自行展开 —— 与旧行为一致）。
- 嵌套层**复用外层事务**（`TxCoordinator` 不再另开一个）：否则单连接池下
  三层级联会自锁，多连接下会产生一个与父事务无关的空事务。

#### 任意跨级引用：不需要亲属专用配置

同一请求树内，引用能力不按「亲兄弟 / 堂兄弟 / 叔侄 / 爷孙」分类，
所有批次统一用 `Target + Assemblies`；差异只在**目标准备时机**与声明位置：

| 关系 | 树形示例 | 说明 |
|:--|:--|:--|
| 亲兄弟 | `root.A`、`root.B` | 同一父的直接子批次互引 |
| 堂兄弟 | `root.A.A1`、`root.B.B1` | 两个分支的孙批次互引 |
| 叔侄 | `root.A`、`root.B.B1` | 跨层级、跨分支互引 |
| 爷孙 | `root.A`、`root.A.A1` | 同分支跨层互引 |
| 更远 | 四层及以上 | 只受 `MaxExpandDepth` / 防环机制约束 |

前提只有两条：① 双方在**同一次请求展开的 Cascade 树内**；② 消费方匹配前，
发布方已登记。若发布方是**库里已存在**的实体（不在本次请求树内），
那不是装配，应走外部引用解析。

#### 祖先记录自身作为 Target：`HandlerConfig.Target`

`CascadeRelation.Target` 只能发布**子批次**；顶层/祖先记录没有对应的关系，
若引用方向是「子孙 → 祖记录本身」，用 `HandlerConfig.Target` 声明：

```go
HandlerConfig[*Form]{
    Target:   "root.form",     // ← 本 Handler 的记录可被任意批次引用
    Cascades: []CascadeRelation{...},
}
```

登记的是**落库后的实体快照**（而非请求 map）：update 请求通常只有
`{id, 改动字段}`，缺少 `Match` 需要的 `code` 等字段，用请求 map 会让消费方
匹配落空。主键同时以 PK 列名与约定 JSON 名 `ulid` 写入，`Assign` 取值键两种都能命中。

#### 装配前 / 后处理：`BeforeCascadePrepare` / `AfterCascadeAssemble`

装配器按 **JSON 字段名**在 raw map 上求值，因此要求被装配的节点已是对象/数组；
而这两类数据仍可能是 JSON 文本：

1. 请求体（历史调用方仍传字符串）；
2. 数据库存量数据与「版本化 update 未传子表」时的 **DB 回填数据**。

框架**不猜**哪个字符串是 JSON（普通字符串若恰好长得像 JSON 必须保持原样），
由应用按自己的 schema 在这对钩子里处理：

```go
h.SetHooks(HandlerHooks[*Column]{
    // 本层记录即将被展开/预分配/装配之前：浅到深（外层不解码，内层路径根本不存在）
    BeforeCascadePrepare: func(ctx context.Context, rawMaps []map[string]any) error { ... },
    // 本层（含更深层）装配全部完成之后、落库之前：深到浅（子层先于父层）
    AfterCascadeAssemble: func(ctx context.Context, rawMaps []map[string]any) error { ... },
})
```

契约要点：

- 收到的 `rawMaps` 就是后续提取、预分配与**落库实际读取的同一批 map 句柄**，原地修改即可；
- **逐层触发**（含叶子层）：DB 回填数据在请求体里不存在，只在入口做一次钩不住；
- 仅在**装配通道启用**（该请求挂了预分配注册表）时触发；未启用装配的调用方零影响；
- 编码必须**后序**（子先父后）：外层若先序列化成文本，内层随后的装配改动就进不去那段文本。

#### 与 v2 的关系

| 项 | v2（`Remaps` / `RemapKey`） | v3（`Assemblies` / `Target`） |
|:--|:--|:--|
| ULID 生成时机 | 落库时 | 请求入口预分配 |
| 跨分支可见性 | 靠 catalog 传递映射 | 同一请求树内直接可见 |
| 顺序校验 | L1 / L2 / L3 | 不需要（三阶段） |
| 分流判据 | `Remaps` / `RemapKey` 非空 | 两者皆空 |

- 同一 `CascadeRelation` **禁止同时配置** `Remaps` 与 `Assemblies`（构造期 panic）；
- 启动期可调 `h.ValidateAssemblies()` 做全局 L1 校验，**聚合报告**全部找不到声明方的
  `Target`（若某标识只由 v2 的 `RemapKey` 声明，报告会附迁移提示）；
- heims 迁移写法：`RemapKey: "x"` → `Target: "x"`（设计文档 §10.1）。

### 外部引用解析（引用**已存在**的实体）

> 与「引用装配」的分工：装配处理**同一请求树内本次新建**的引用（ULID 由预分配产出）；
> 本节处理**已经存在**的外部实体（如 `data_select` 选中的表单 / 流程）——
> 它们不在请求树里，无法装配，只能「解析权威版本 → 应用侧校验 → 按策略持久化」。

框架此前对这类引用完全不介入：前端传什么就存什么。于是「只有 ULID」「只有 code」
「ULID + code」三种形态混成同一种隐式行为，「引用了旧版本」只能靠人工发现。

`ResolveExternalRef` 把「选择意图」解析为「权威身份」：

```go
resolved, err := h.ResolveExternalRef(ctx, handler.ExternalRefLookup{
    HandlerName: "form",
    Code:        "F001",                              // 选择锚点
    ULID:        "",                                  // 可选：给了就做陈旧检测
    Mode:        handler.ExternalRefModePublished,    // 留空则自动推导
})
// resolved.ULID / resolved.Code / resolved.VersionStatus / resolved.VersionCode / resolved.Record
```

也可用包级入口（应用侧只有注册表时）：

```go
resolved, err := handler.ResolveExternalRef(ctx, handlerReg, lookup)
```

| 模式 | 语义 |
|------|------|
| `published` | 按 code 取**线上生效版本**（`version_status='published'`） |
| `current` | 按 code 取**当前版本**（`is_current=1`，可能仍是编辑中的草稿） |
| `snapshot` | 按 ULID 取**固定版本**（校验存在、未软删） |

- `Mode` 留空时按以下规则推导（**必须记住这张表**）：

  | 提交 | 推导模式 | 语义 |
  |------|----------|------|
  | 仅 `Code` | 版本化 → `published`；非版本化 → `current` | 按 code 解析 |
  | 仅 `ULID` | `snapshot` | 固定该版本 |
  | `Code` + `ULID` | `published` | 按 code 解析权威版本并**校验一致** |

  需要「固定版本**同时**带 code」时必须**显式**传 `Mode=snapshot`；
- `Code` + `ULID` 的组合会做**陈旧检测**：不一致返回
  `ErrExternalRefStale`（「版本已变化，请重新选择」）—— 绝不静默改成新版本；
- 目标不存在 / 已软删 → `ErrExternalRefNotFound`（404）；未注册 / 未实现解析能力 →
  `ErrExternalRefTargetMissing`（500）；参数不合法 → `ErrExternalRefInvalidParam`（4001）；
  陈旧 → `ErrExternalRefStale`（409）。

⚠ **`GetByCode` 与 `GetPublishedByCode` 的区别**（容易踩）：前者取 `is_current=1`
（**不论是否 published**，编辑中的草稿同样命中），后者取 `version_status='published'`。
存在草稿时二者指向不同行。

**框架不做的部分**（属业务语义，应用侧负责）：权限 / 数据归属 / 业务状态校验，
以及最终持久化形态（只存 `ulid`、只存 `code`、还是存 `{ulid, code}` 与数组包装）——
应用拿到权威身份后自行组装。

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

### 版本化 Create 的 code 冲突检测

版本化实体 Create 时不允许使用已存在的 code（应去 Update 而非 Create）；冲突检测与 `EnableUniqueValidation` 唯一性校验均排除已废弃版本（`is_current=0`，删除版本化记录时置位），删除后同 code 可复用（BUG-042）。

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
- `deprecated` → `published`（**复活已废弃版本 = 一次新发布，会留发布痕迹**）
- `draft` → `abolished`（直接废弃）
- `deprecated` → `abolished`（归档）
- `abolished` → `draft`（恢复为草稿）
- `published` 禁止直接 abolished

### 发布痕迹（publish trace）

**「一个版本变成线上生效版本」共有 4 条路径，全部由框架统一留痕**，下游零改动：

| 路径 | `via` | 说明 |
|:--|:--|:--|
| `create` 带 `version_status=published` | `create` | 保存并发布 |
| `update` 带 `version_status=published` | `update` | 草稿 → 发布 |
| `activate` | `activate` | 草稿 / 已废弃 → 发布、回滚 |
| `edit-version` 改 `published` | `edit-version` | 复活已废弃版本 |

每条路径的统一动作：

1. **基础列**：写 `VersionFields.PublishedAtField` / `PublishedByField`
   （操作人取 ctx 内登录身份，取不到时只写发布时间）；
2. **发布历史**：一条含 `via` 来源标记的记录，默认落 MongoDB `publish_history` 集合；
3. **统一回调**：`Config.OnPublished` 各触发**恰好一次**。

```go
svc := service.NewGenericService(repo, service.Config[entity.SysForm]{
    EntityName: "form",
    VersionMode: true,
    VersionFields: &service.VersionFieldMapping{
        /* ... */
        PublishedAtField: "PublishedAt",
        PublishedByField: "PublishedBy",
    },
    // 可选：四条发布路径各触发恰好一次；替代各模块各写一份 onXxxPublished
    OnPublished: func(ctx context.Context, id any, e *entity.SysForm, via service.PublishVia) error {
        return writePublishLog(ctx, id, via) // via: create/update/activate/edit-version
    },
})
// 可选：换掉内置 MongoDB 发布历史写入方
svc.SetPublishHistoryWriter(myWriter{})
// 仅要基础列、不要发布历史：
// svc.SetPublishHistoryWriter(service.NoopPublishHistoryWriter{})
```

> **口径**：`published → deprecated`（下线）**不算**发布 —— 不写 `published_at`、
> 不产生发布历史、不触发回调。只有「终点是 published 且旧值不是 published」才留痕
> （`statusBecomesPublished`），因此「复活已废弃版本」算，原地不改不算。

> **事务一致性**：activate / 版本化 update 走 MySQL 事务，而事务不跨库到 MongoDB，
> 因此发布历史挂到 gorm 事务的 after-commit 钩子上写 —— 主业务回滚不留
> 「有发布记录但没有发布」的幽灵痕迹。`OnPublished` 是外部副作用、无法随事务撤回，
> 回滚且回调已执行时会记一条 Error 明确提示。

> **配置缺口**：`PublishedAtField` / `PublishedByField` 都没配置时，发布仍会写历史并
> 触发回调（不让配置缺口吞掉业务事实），只打一条 Warn 提示补配置。

查询发布历史（仅内置 MongoDB 集合）：

```go
records, err := service.ListPublishHistories(ctx, service.PublishHistoryFilter{
    EntityType: "form", EntityID: formULID,
})
// records[i].Via / PublishedAt / OperatorULID / VersionCode / EntityCode ...
```

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

### 版本化删除：`is_current` 交还（BUG-076）

版本化实体的「删除」= **废弃**（写 `is_current=0` + `version_status=deprecated`，从不写 `is_deleted`）。
框架中所有「配置态当前版本」读路径（`list` / `get?code=` / `versions`）都以 `is_current=1` 为判据，
因此删除后会做一次**当前版本交还**，避免出现「全族 `is_current` 之和为 0」的幽灵态：

| 删除对象 | 结果 |
|---|---|
| **非 published 的当前版本**（典型：发布后编辑存草稿，再删掉草稿） | 丢弃草稿，并把 `is_current=1` **交还给族内最新的未软删 published 行**（只改 `is_current`，**不动** `version_status`）→ 配置列表恢复可见 |
| **published 的当前版本** | 整族干净下线（族内无其他 published 可交还），行为与修复前一致 |
| 族内从未发布过（只有草稿） | 无 published 可交还，维持废弃结果 |

交还只在「族内已无 `is_current=1`」且「存在 `version_status='published'` 且未软删的行」时发生；
多条 published 时取 `published_at` 最新的一条。该逻辑对 `delete`（按主键）与按 code 删除同样生效，
MySQL 与 MongoDB 一致。

> 排查提示：交还成功会打印一条 `Info` 日志
> （`BUG-076 删除草稿后已将 is_current 交还线上的 published 版本（entity_type=… code=… id=…）`）。

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

#### gin.Context → context.Context 桥接（重要）

`Authenticator.Middleware()` 将用户信息存入 `gin.Context`，但框架的 Service 层通过 `service.GetUserULID(ctx)` 从 `context.Context` 读取。需要在中间件中将用户 ULID 注入 `context.Context`：

```go
func (a *JWTAuth) Middleware() gin.HandlerFunc {
    return func(c *gin.Context) {
        token := c.GetHeader("Authorization")
        claims := parseJWT(token)
        info := handler.UserInfo{ULID: claims.Sub, Name: claims.Name}
        c.Set("user", info)

        // ★ 关键：将 ULID 注入 context.Context，供 Service 层使用
        ctx := context.WithValue(c.Request.Context(), service.CtxKeyUserULID, claims.Sub)
        c.Request = c.Request.WithContext(ctx)

        c.Next()
    }
}
```

> 缺少此桥接时 `GetUserULID(ctx)` 返回空字符串，导致 `created_by`、`updated_by`、草稿可见性过滤等依赖用户 ULID 的功能静默失效。

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
svc.SetOpLogDB(db) // 或 SetOpLogWriter / SetOpLogRepo，见上文「配置项详解」
```

日志字段：`log_ulid`、`entity_type`、`entity_id`、`operation`、`operator_ulid`、`request_id`、`operated_at`。

支持的 `operation` 取值：`create`、`update`、`delete`、`activate`、`updateVersion`、`restore`。

> **注意（BUG-077）**：只设 `EnableOpLog: true` 而不注入写入方等于没开启 —— 构造期会打告警，
> 也不会写任何记录。`gentity` 生成的模板默认 **不再** 打开该开关。

### 备份写入器（与操作日志相互独立）

非版本化 Update 时旧数据会被覆盖丢失，可通过 `BakWriter` 在更新前写备份日志文件：

```go
svc.SetBakWriter(func(ctx context.Context, tableName string, recordID any, operation string, oldData any, requestID string) error {
    // 写入文件或 MongoDB
    return nil
})
```

调用时机：非版本化 `update`（旧值会被覆盖）、物理 `delete`（按主键 / 按字段）、`activate`、`updateVersion`。

> **注意（BUG-077 §8.2）**：备份写入的唯一门控是「是否注册了 `bakWriter`」，
> **与 `EnableOpLog` 无关**。修复前非版本化 update 的备份被误锁在 `EnableOpLog && opLogRepo != nil` 之下，
> 未开审计的下游应用一条备份都写不出来。备份失败只记 `Error` 日志，不阻塞主流程。

### 在钩子中取旧值（BUG-077 §8.3）

`UpdatePair` 已导出，应用可在 `AfterUpdate` 钩子里直接断言取旧值，无需反射：

```go
func (s *XxxSvc) AfterUpdate(ctx context.Context, id any, result *Entity, pdata any) (*Entity, error) {
    if pair, ok := service.AsUpdatePair[*Entity](pdata); ok && pair.Old != nil {
        backup(pair.Old) // 旧值快照（非版本化时为被覆盖前的内容）
    }
    return result, nil
}
```

> 覆盖 `Hooks.AfterUpdate` 会**接管**默认行为（内置 opLog / bakWriter 不再执行）。

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

分页参数支持多组别名（成对使用，offset 模式优先于页码模式）：

| 参数组 | 别名 | 语义 |
|------|------|------|
| 页码 | `page` / `pageNum` / `page_num` | 从 1 开始 |
| 每页数量 | `page_size` / `pageSize` | 默认 20 |
| 起点 & 数量 | `offset` & `size` | offset 从 0 开始（如 `offset=20&size=10` → 第 21~30 条） |

### 结构化过滤（Repository 层）

```go
type ListFilters struct {
    Page     int      // 页码（>=1）
    PageSize int      // 每页条数（<=0 不分页）
    Offset   int      // 起点偏移（>=0，从 0 开始；>0 时优先于 Page）
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
| BETWEEN | `OpRange` | `field BETWEEN ? AND ?` | 长度为 2 的切片（`[]any{lo,hi}`） |
| 原生 SQL | `OpRaw` | 直接拼接 SQL 片段 | `(string, []any)` 或仅 `string` |
| OR 组合 | `or_group` | 子条件 OR 连接，整体 AND 嵌入 | `[]Filter` 切片 |

**双引擎语义一致性（BUG-073）**

- `OpRange` 在 **MySQL 与 Mongo 语义相同**：都是闭区间 `[lo, hi]`，
  只给一侧时退化为单边条件（`$gte` 或 `$lte` / SQL 侧单边比较）。
  入参形态完全无法解释（空值 / 纯单值字符串）时，Mongo 侧返回**恒不匹配**的条件，
  而不是静默退化成「不过滤返回全量」。
- **通过 HTTP List 接口传入的过滤值由框架自动做类型归一**（`service/filter_value.go`）：
  按目标列的 Go 字段类型把 URL 字符串转成 `time.Time` / `int64` / `float64` / `bool`。
  因此时间与数值字段**直接传字符串即可**，无需调用方自己构造 `time.Time`：
  ```
  GET /api/v1/publish-history/list?published_at:between=2026-01-01 00:00:00,2026-12-31 23:59:59
  GET /api/v1/auto-task-log/list?start_time:gt=2020-01-01T00:00:00Z
  GET /api/v1/auto-task-log/list?duration_ms:gt=0
  ```
  支持的时间格式：RFC3339 / RFC3339Nano（含时区偏移）、`2006-01-02 15:04:05`、
  `2006-01-02 15:04`、`2006-01-02`、`2006/01/02`。解析失败时保持原样
  （与 MySQL 侧「匹配不到」一致，不报错）。
- 直接调用 `repo.ListByFilters`（不经 service）时**不会**自动归一，
  此时请自行传入正确类型的 `Value`。
- 比较条件的取值会经 `bsonSafeValue` 归一（无序 `map[string]any` 统一按有序编码，
  避免与库中原有序文档比较时结果不稳定）；标量类型走快路径直接透传。

### List 过滤的可用列（白名单，BUG-078）

HTTP List 接口只接受**属于该实体的列**作为过滤参数，其余 key 一律静默忽略
（如前端附加的 `_t`、`callback`）。白名单的来源：

- 具名字段的 `gorm column` → `bson` tag（取逗号前段）→ `json` tag；
- **匿名嵌入结构体的字段会被递归展开**（限深 3 层防环）。

第二条是 BUG-078 的修复点：审计字段（`AuditFields` / `MongoAuditFields`：`created_by`、
`created_at`、`updated_by`、`updated_at`）一律匿名嵌入，此前**一个都不在白名单里**，
于是 `?created_at:lte=2026-01-01` 这类条件被整条丢弃并返回全量数据、不报错。现在它们可用：

```http
GET /api/v1/xxx/list?created_at:lte=2026-01-01
GET /api/v1/xxx/list?updated_at:between=2026-01-01 00:00:00,2026-12-31 23:59:59
GET /api/v1/xxx/list?created_by=01M2AHQ...
```

> ⚠️ 升级提示：此前「传了也不生效」的审计列过滤现在会**真正生效**。
> 若调用方曾依赖该空操作（把返回全量当成正常结果），升级后会看到结果变少。
>
> 陌生字段的处理**未变**：仍静默忽略并返回全量（不做 400）。

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
| `replica_set` | string | 副本集名称（可选）。非空时连接串附加 `replicaSet=<名称>`：驱动据此按种子节点发现其余成员（`hosts` 只配一个成员也能连通整个副本集），并在选主/故障转移后重定向。单机部署留空，行为与之前一致 |

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

### default 约定（方案 B）

gorm `default:` tag 只允许 **0 值 / 无**，非零 DB 默认值（如 `default:1`、`default:'active'`、`CURRENT_TIMESTAMP`）**一律不生成 `default:xxx`**，改为生成 `default:(-)`（禁止 GORM 默认填充），其语义由实体 `SetDefaults()` 在 Go 层承担。生成时控制台会对非零默认列打印提示。

原因：GORM create 回调对 `default:<非零>` 可解析 tag + 零值字段会无条件用默认值覆盖并写回实体，`Select` 白名单也无法豁免（BUG-047），导致请求显式零值（`is_enabled=0`）落库变默认值（`1`）。`_beforeCreate` 执行顺序 `SetDefaults → SetCreatedAt/At → MergeTo`，请求值最后覆盖，语义等价且显式零值可真实落库。

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
| `SetDefaults()` | 遍历 DEFAULT 值，零值时回填（**方案 B**：非零 DB 默认值的语义由本方法在 Go 层承担，实体不生成非零 `default:` tag） |
| `SetID()` | 主键为 `*_ulid` 生成 ULID；自增主键空操作（**框架层已在 `_beforeCreate` 自动生成 PK，此方法保留兼容、不再被调用**） |
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
│   ├── cascade_remap.go    # 级联引用重映射（Remaps 声明 + 四形态重写）
│   ├── cascade_remap_ctx.go # 重映射时机/传递 + 跨批次发布消费 + L1/L2 校验
│   ├── cascade_remap_catalog.go # 事务级 remap catalog（命名空间 → 映射，随 ctx 存亡）
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
│   ├── generic_impl.go     # 内置 _before/_do/_after 默认实现 + normalizeNotFound/parseBsonKey 收敛点
│   ├── generic_read_impl.go # _doList 读取实现（含软删除/draft 过滤）
│   ├── generic_write_impl.go # _doCreate/_doUpdate/_doDelete 写入实现（含版本化/级联）
│   ├── generic_version_impl.go # 版本操作实现（Activate/ListVersions/EditVersion）
│   ├── publish_trace.go    # 发布痕迹（published_at/by + 发布历史 + OnPublished）
│   ├── oplog.go            # 操作日志（OpLogRecord/OpLogWriter/SetOpLogDB）
│   ├── filter_value.go     # 过滤值按列类型归一（time/int/float/bool）
│   ├── hooks.go            # Hooks 钩子类型定义
│   ├── registry.go         # ServiceRegistry 注册表
│   ├── request.go          # CrudRequest/Mergeable/Identifiable/Validatable 接口
│   ├── idempotency.go      # IdempotencyStore 幂等缓存
│   └── tx.go               # WithTx/GetTx 事务透传
├── repository/             # 数据访问层
│   ├── crud.go             # CRUDRepository 泛型仓储 + ListFilters + FilterOp
│   ├── base.go             # BaseRepository + VersionRepository（遗留非泛型版，新代码用 CRUDRepository）
│   ├── dao.go              # BaseDAO（缓存/审计扩展点）
│   ├── repo.go             # Repo[M] 统一仓储接口
│   └── mongo_repo.go       # MongoCRUDRepository MongoDB 仓储（mapMongoFindError 归一 not-found）
├── internal/               # 框架内部
│   ├── bootstrap/          # 启动引导（Init/InitMySQL/InitOther/Migrate/Close）
│   ├── config/             # 配置加载（Config/Load + 各配置段结构体）
│   ├── database/
│   │   ├── mysql/          # MySQL 连接 + 纯 SQL 迁移（migration.go/schema 系列：表结构比对 → ALTER/DROP/RENAME 修复 + DEFAULT 比对）+ 类型校验
│   │   ├── mongodb/        # MongoDB 连接
│   │   └── redis/          # Redis 连接
│   ├── logger/             # 日志系统（RequestLog/ResponseLog/BusinessLog + 按天滚动）
│   ├── middleware/          # HTTP 中间件（RequestLogger/Cors/Recovery/AuthMiddleware）
│   ├── model/entity/       # 框架内置实体（SysOperationLog）
│   └── router/             # 基础路由注册
├── common/                 # 通用工具
│   ├── ulid.go             # ULID 生成器（并发安全：crypto/rand + 时间戳）
│   ├── reflect.go          # SetFieldValue 反射辅助
│   ├── conv.go             # ToSnakeCase / ExtractGormColumn / ParseBSONKey / Registry[T]
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

## 开发约定：避免跨包重复实现

框架历史上多次因「同一段解析逻辑在各包各写一份」而产生缺陷 —— 修了 A 包漏了 B 包，
最典型的是 struct tag 选项剥离同时引发 **BUG-053**（repository 侧落库键带
`,omitempty`）与 **BUG-061**（service 侧查询条件永不匹配），根因完全相同却修了两轮。

因此约定：**跨包共用的解析/归一逻辑必须有唯一入口，且放在 `common` 或调用方最小公倍数包**。

| 逻辑 | 唯一入口 | 禁止再内联 |
|:--|:--|:--|
| bson / json tag 取逗号前段 | `common.ParseBSONKey` | 禁止 `strings.IndexByte(tag, ',')` / `strings.Cut(tag, ",")` 手写 |
| bson `,inline` 判定 | `common.IsBSONInline` | — |
| gorm tag 取 column | `common.ExtractGormColumn` | — |
| 驱动 not-found → 框架哨兵 | `service.normalizeNotFound`（service 内）/ `repository.mapMongoFindError`（Mongo 仓储内） | 禁止手写 `errors.Is(err, gorm.ErrRecordNotFound)` 后返回哨兵 |
| 分页参数（page/page_size/offset） | `handler.GetPageParams`（HTTP）/ `repository.mongoPageOpts`(Mongo) | 新增别名时须同步全部入口 |

新增实体/仓储调用点时，凡是「错误归一」「tag 解析」「主键读写」这三类，
先查上表是否已有 helper 再动手。

> 已知待收敛项（未做，记录以免遗忘）：`handler/utils.go` 的 `extractPKFromResult`
> 与 `service/generic_write_impl.go` 的 `extractEntityID` 是两份同构的「主键提取」
> 实现；反射解指针样板（`for rv.Kind() == reflect.Ptr`）散落在 handler /
> service / repository 约 10 处。二者语义有细微差异（前者额外支持 `PKField()` 分支），
> 收敛需一并提升到 `common` 并补回归，属独立重构，不在 bug 修复范围内顺手做。

---

## License

MIT
