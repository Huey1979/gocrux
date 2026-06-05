# 需求：Entity → DTO 响应映射

## 1. 背景与问题

gocrux 生成的 handler 目前直接将 Entity（DB 实体）序列化后作为 API 响应返回。DB 实体携带了大量**存储层专属字段**，这些字段不应暴露给 API 消费者：

| 不应暴露的典型字段 | 所属实体示例 |
|---|---|
| `is_deleted` | 所有软删除实体 |
| `is_current` / `parent_ulid` | 版本化实体（SysSite、SysMenu 等） |
| `version_status` | 版本化实体 |
| `password` / `password_hash` | 用户/认证表 |
| 审计字段（按需） | `created_by` / `updated_by` |

当前 heims 项目中，`entity.Site` DTO 即为此目的而手写，但 `ToSite()` **从未被调用**，是个未完成的方案。

## 2. 核心需求

**在 gocrux handler 层提供可选的 Entity→Response 映射能力**，使每个 handler 可独立选择返回实体还是 DTO。映射由两部分协作完成：

| 组件 | 职责 | 归属 |
|---|---|---|
| **DTO 结构体 + 转换函数** | 编译期类型安全的数据结构，字段裁剪 | gentity（代码生成） |
| **ResponseMapper 注入点** | handler 管线中执行映射的钩子 | gocrux handler |

## 3. 行为规格

### 3.1 默认行为（100% 向后兼容）

- `ResponseMapper == nil`：行为与当前完全一致，直接返回实体 map
- 现有项目升级 gocrux 后零影响

### 3.2 启用映射后

handler 在 **展开完成之后、写入 HTTP response 之前** 调用 `ResponseMapper`，将实体结果转换为目标类型：

- **Get** 流程：`map[string]any`（由 `_doGet` 产出，含 References/Cascades 展开）→ `ResponseMapper(entity)` → 新的 `map[string]any` 或 struct
- **List** 流程：`[]map[string]any`（由 `_doList` 产出，含批量展开）→ `ResponseMapper(entity)` per item → `[]map[string]any` 或 `[]struct`

### 3.3 映射的输入

映射函数的输入是**展开后的原始 Entity 实例**（此时 References/Cascades 已 inject 到同一个 map 中），不是未展开的裸记录。

### 3.4 映射的输出

映射函数返回 `any`，handler 统一将其序列化为 JSON 写入 response。可以是：
- 一个 struct（如 `*SysSiteDTO`）—— 推荐
- `map[string]any` —— 可用但不推荐（丢失类型安全）

### 3.5 与级联调用（CascadeHandler）的关系

`DoGetByID` / `DoList` 作为级联接口被父 handler 调用时，**不应**执行 ResponseMapper。映射仅在**顶层 HTTP handler** 中生效。

如果存在争议（即级联调用时是否也需要返回 DTO），当前版本保守处理：级联调用永远返回原始 map，仅在顶层 HTTP 入口应用映射。

## 4. gentity 生成规格

### 4.1 DTO struct 生成

gentity 在解析表结构后，为每张表生成一个 `{EntityName}DTO` struct：

**生成规则**：
- 字段名、类型、`json` 标签与 Entity 一致
- 不包含任何 `gorm` 标签
- 不包含 `TableName()` 方法
- 不实现 `Record` 接口
- 字段子集 = Entity 全部字段 **减去全局排除字段**

**全局排除字段**（可配置，以下为默认值）：
```
is_deleted, deleted_at, is_current, parent_ulid
```

**附加排除字段**（从 DSN 内省推断，可选）：
- 字段名为 `password`、`passwd`、`password_hash`、`secret` 等敏感字段
- 当 gentity CLI 提供 `--dto-exclude field1,field2` 时追加

### 4.2 ToDTO() 转换函数

gentity 为每个 Entity 生成：

```go
func (e *EntityName) ToDTO() *EntityNameDTO
```

- 逐字段赋值，无反射
- 不包含已排除的字段
- 级联展开的附加字段（References/Cascades inject 到 map 中的额外 key）**不自动转**——DTO 只映射 Entity 自有字段，级联数据嵌入由 handler 层的 References/ChildRefs 配置的 `ResultField` 负责，它们本就在 map 中且不受 DTO 裁剪影响

### 4.3 gentity CLI 参数

| 参数 | 说明 | 默认值 |
|---|---|---|
| `--dto` | 启用 DTO 生成 | `false`（不生成） |
| `--dto-exclude` | 全局排除字段列表，逗号分隔 | `is_deleted,is_current,parent_ulid` |
| `--dto-pkg` | DTO 输出包名/子目录 | `dto` |

生成物位置：`{output}/dto/{table}_dto.go`

## 5. gocrux handler 改动规格

### 5.1 HandlerConfig 新增字段

```go
type HandlerConfig[M service.Record] struct {
    // ... 现有字段不变 ...

    // ResponseMapper 将单个已展开的 Entity 实例映射为 API 响应对象。
    // 入参是展开后的原始实体指针（含 References/Cascades inject 后的完整 map）。
    // 返回 any 将被 JSON 序列化写入 HTTP response。
    // 为 nil 时直接返回原始 map（向后兼容）。
    ResponseMapper func(M) any
}
```

### 5.2 插入点

映射应在以下位置执行：

**Get**：`getPipeline` → `afterGet` **之后**，`Success(c, gin.H{"data": result})` **之前**。

**List**：`listPipeline` → `afterList` **之后**，`Success(c, gin.H{"items": items, ...})` **之前**。对 `items` 中每一元素调用 `ResponseMapper`。

**CUD**：Create/Update/Delete 流程中，如果启用了 `ReturnCreated` / `ReturnUpdated`（返回操作后的记录），也应执行映射。此行为可后续细化。

### 5.3 签名影响

- `getPipeline` 返回类型 `map[string]any` **不变**，映射在 pipeline 外部执行
- `listPipeline` 返回类型 `([]map[string]any, int64, error)` **不变**，映射在 pipeline 外部执行
- `DoGetByID` / `DoList`（CascadeHandler 接口）**不执行映射**

## 6. 使用方式（heims 示例）

```go
// 场景 1：不映射 — 完全兼容旧行为
gh := handler.NewGenericHandler[*entity.SysDept](svcReg, "sys_dept",
    handler.HandlerConfig[*entity.SysDept]{
        PathPrefix: "/api/v1/sys-dept",
        // ResponseMapper: nil （默认）
    })

// 场景 2：映射为 DTO
gh := handler.NewGenericHandler[*entity.SysSite](svcReg, "sys_site",
    handler.HandlerConfig[*entity.SysSite]{
        PathPrefix: "/api/v1/sys-site",
        ResponseMapper: func(s *entity.SysSite) any {
            return s.ToDTO()
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

## 7. 破坏性变更分析

**零破坏**：
- `ResponseMapper` 为 `nil` 时行为与当前完全一致
- `HandlerConfig` 新增字段带有零值默认行为
- 现有 handler 注册代码无需任何修改

## 8. 实施顺序建议

1. **gentity**：新增 `--dto` flag，实现 DTO struct + `ToDTO()` 生成
2. **gocrux handler**：`HandlerConfig` 加 `ResponseMapper` 字段，在 Get/List HTTP 入口中接入
3. **heims**：删除手写 `entity.Site` DTO，改为使用 gentity 生成的 `SysSiteDTO`，handler 注册时传入 `ResponseMapper`
