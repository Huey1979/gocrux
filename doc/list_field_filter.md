# List 接口字段过滤需求

## 背景

heims 项目中部分 entity 单条记录包含较大的 JSON/Text 字段（如 `form_config`、`entity_config`、`flow_config`、`data_config` 等，参见 `internal/model/entity/` 下 `type:json` 或 `type:text` 字段），单条可达数 KB 到数十 KB。

在 List 接口中，通常不需要展示这些大字段的全部内容（例如表单列表只需展示表单名称、类型等基本信息），但当前实现会将这些字段完整返回，浪费带宽和序列化开销。

**需要一种在 List 时按需裁剪字段的机制，同时保证 Get 接口不受影响。**

## 方案对比

| 方案 | 实现复杂度 | 维护成本 | 适用场景 |
|------|-----------|---------|---------|
| 字段裁剪（本需求） | 低，在 Handler 层过滤 map key | 零（按 handler 配置） | 配置管理系统，List 请求量不大 |
| 分表 | 高（拆表、迁移、同步、双 handler） | 高（改表结构需同步两张表） | 单条 > 1MB / 十万行级高并发 |

**结论**：当前场景应选择字段裁剪方案。分表方案仅在单条 JSON > 1MB 或十万行级高并发下才有意义。

## 需求描述

在 `HandlerConfig` 中新增两个可选字段，控制 List 接口的字段过滤行为。优先级如下：

```
ListSkipFields（非空） > ListKeepFields（非空） > 全字段返回
```

### 字段定义

```go
// ListSkipFields List 时跳过的字段名列表（可选，优先级高于 ListKeepFields）。
// 配置后，_doList 返回前会从每条记录中删除这些字段。
// 例：[]string{"form_config", "entity_config", "flow_config"}
ListSkipFields []string

// ListKeepFields List 时保留的字段名列表（可选，仅 ListSkipFields 为空时生效）。
// 配置后，_doList 返回前每条记录仅保留这些字段。
// 例：[]string{"form_ulid", "form_code", "form_name", "form_type"}
ListKeepFields []string
```

### 行为规范

1. **Get 接口不受影响**：字段过滤仅作用于 `_doList` 返回结果，`_doGet` 等单条查询接口始终返回全字段。
2. **不影响级联数据**：过滤逻辑应在所有展开逻辑（References / ChildRefs / Cascades）执行之后、最终 `return` 之前。级联展开后的额外字段不受影响。
3. **向后兼容**：两个字段默认为 nil/空，不配置时行为与原来完全一致（全字段返回）。

### 实现位置

**`handler/generic.go`** — `HandlerConfig` 结构体，新增上述两个字段。

**`handler/generic_impl.go`** — `_doList` 方法末尾（在 `return result, total, nil` 之前），新增字段过滤逻辑：

```go
// List 字段裁剪：skip 优先于 keep，均未配置时全字段返回。
if len(h.config.ListSkipFields) > 0 {
    skipSet := make(map[string]bool, len(h.config.ListSkipFields))
    for _, f := range h.config.ListSkipFields {
        skipSet[f] = true
    }
    for _, row := range result {
        for f := range skipSet {
            delete(row, f)
        }
    }
} else if len(h.config.ListKeepFields) > 0 {
    keepSet := make(map[string]bool, len(h.config.ListKeepFields))
    for _, f := range h.config.ListKeepFields {
        keepSet[f] = true
    }
    for _, row := range result {
        for k := range row {
            if !keepSet[k] {
                delete(row, k)
            }
        }
    }
}
```

### 配置示例

```go
// 黑名单模式：跳过指定大字段（推荐，改动最小）
handler.NewGenericHandler[*entity.SysForm](svcReg, "form",
    handler.HandlerConfig[*entity.SysForm]{
        PathPrefix:     "/form",
        ListSkipFields: []string{"form_config"},
    })

// 白名单模式：仅保留核心字段
handler.NewGenericHandler[*entity.SysForm](svcReg, "form",
    handler.HandlerConfig[*entity.SysForm]{
        PathPrefix:     "/form",
        ListKeepFields: []string{"form_ulid", "form_code", "form_name", "form_type"},
    })
```

### 效果

- `GET /form/list` → 不含 `form_config`，每条数据瘦身到数百字节
- `GET /form/get?id=xxx` → 正常返回完整 `form_config`

## 现状

heims 已在本地 `vendor/` 中完成试用性实现并通过编译验证，该改动位于：
- `vendor/github.com/Huey1979/gocrux/handler/generic.go`
- `vendor/github.com/Huey1979/gocrux/handler/generic_impl.go`

`vendor/` 不在 git 追踪范围内，需由 gocrux 开发者在上游仓库正式合入此特性。
注：vendor目录的物理路径为 F:\labvoyage\go_project\heims\vendor