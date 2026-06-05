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
- [Entity → DTO 响应映射](#entity--dto-响应映射)
- [路由注册](#路由注册)
- [钩子系统](#钩子系统)
- [级联机制](#级联机制)
- [版本管理](#版本管理)
- [身份认证与授权](#身份认证与授权)
- [幂等支持](#幂等支持)
- [操作日志与备份](#操作日志与备份)
- [运行时日志（Request/Response/Business）](#运行时日志requestresponsebusiness)
- [列表查询条件](#列表查询条件)
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
}
```

### 软删除

- `SetDelete() bool` 返回 `true`：实体有 `is_deleted` 字段，删除时执行 `UPDATE SET is_deleted=1`
- 返回 `false`：物理删除，先写备份日志再 `DELETE`

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
| `?ignore=ref` | 跳过 References 展开 |
| `?ignore=cascade` | 跳过 Cascades 展开 |
| `?ignore=all` | 跳过所有展开 |
| `?ignoreRef=site_ulid` | 跳过特定 References 字段 |
| `?ignoreCascade=domains` | 跳过特定 Cascades 字段 |

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
GET /api/v1/sites/get?code=S001     # 直接按 code 查 published 版本
```

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
        --field-config tools/gentity/gentity_fields.yml \
        --out generated
```

### 检查模式（`--check`）

检查所有表（排除日志表）是否缺少框架约定字段，若缺失则生成 ALTER TABLE SQL 文件。

```bash
gentity --dsn "user:pass@tcp(localhost:3306)/db?charset=utf8mb4&parseTime=true" \
        --check \
        --field-config tools/gentity/gentity_fields.yml \
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

**配置文件格式**（`tools/gentity/gentity_fields.yml`）：

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
│   ├── generic.go          # GenericHandler 定义 + before/do/after
│   ├── generic_impl.go     # 内置 _before/_do/_after 默认实现
│   ├── hooks.go            # HandlerHooks 钩子类型定义
│   ├── cascade.go          # CascadeHandler 接口 + CascadeRelation/ReferenceRelation/ChildRefRelation
│   ├── registry.go         # HandlerRegistry 注册表
│   ├── txcoordinator.go    # TxCoordinator 事务编排器
│   ├── request.go          # RequestFactory + MapRequest + map→struct 合并
│   ├── request_util.go     # BindJSON/BindQuery/GetPageParams 工具
│   ├── response.go         # Response 结构 + Success/Error/InternalError
│   ├── auth_hooks.go       # UserInfo + Authenticator + Authorizer 接口
│   ├── errors.go           # Service error → HTTP BusinessCode 映射
│   └── utils.go            # extractPK/extractMapID/removeMapID
├── service/                # 业务逻辑层
│   ├── generic.go          # GenericService 定义 + CrudRequest/Record 接口
│   ├── generic_impl.go     # 内置 _before/_do/_after 默认实现
│   ├── hooks.go            # Hooks 钩子类型定义
│   ├── registry.go         # ServiceRegistry 注册表
│   ├── request.go          # CrudRequest/Mergeable/Identifiable/Validatable 接口
│   ├── idempotency.go      # IdempotencyStore 幂等缓存
│   └── tx.go               # WithTx/GetTx 事务透传
├── repository/             # 数据访问层
│   ├── crud.go             # CRUDRepository 泛型仓储 + ListFilters + FilterOp
│   ├── base.go             # BaseRepository + VersionRepository
│   └── dao.go              # BaseDAO（缓存/审计扩展点）
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
├── config.yaml             # 配置文件示例
└── go.mod
```

---

## License

MIT
