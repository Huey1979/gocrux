# 级联展开深度控制需求与方案

## 背景

gocrux 的 Handler 层支持三种级联/引用展开：

| 类型 | 配置 | 方向 | 说明 |
|------|------|------|------|
| References | `[]ReferenceRelation` | 子→父 | 当前实体的 FK 关联到父实体 |
| ChildRefs | `[]ChildRefRelation` | 父→子 | 通过 FK 列表（如 `tag_ulids`）反向引用子实体 |
| Cascades | `[]CascadeRelation` | 父→子 | 级联创建/更新/删除子实体 |

Get/List 接口返回时会自动展开这些关联数据。此前框架仅支持**全局上限**（是否递归、递归几层），缺乏针对单个字段的精细化控制。

---

## 需求一：单字段深度上限

### 场景

一个 User 实体通过 `dept_ulid` 引用 Department，Department 又通过 `parent_ulid` 自引用。

- 默认展开 Department 时，可能一直递归到顶层部门，数据量大且不必要。
- 用户希望：**对 `dept_ulid` 字段只展开 1 层**（即拿到直属部门信息即可，不再递归展开部门的 parent）。

### 方案

在 `HandlerConfig` 中增加 `FieldDepthLimits` 字段：

```go
FieldDepthLimits map[string]int
// key: 当前 Handler 的字段名（References 的 Field、ChildRefs 的 FKListField、Cascades 的 ChildrenField）
// value: 该字段展开时传递给子 Handler 的最大递归层数
```

**示例：**

```go
HandlerConfig[entity.User]{
    FieldDepthLimits: map[string]int{
        "dept_ulid": 1, // 展开 dept 只向下 1 层
    },
}
```

**工作原理：**

1. `buildFieldCtx(ctx, fieldName)` 被调用时，检查 `FieldDepthLimits[fieldName]`。
2. 若存在，将当前字段的深度限制注入子 context，子 Handler 的 `effectiveExpandDepth()` 会优先使用此值。
3. 若不存在，回退到全局 `MaxExpandDepth`。

**HTTP 覆盖：**

```
GET /api/v1/users/get?id=xxx&fdepth=dept_ulid:1
```

- 格式：`字段:深度`，多字段逗号分隔。
- HTTP 的 `fdepth` 只能**降级**（<= 服务端配置值），不能放大。

---

## 需求二：字段级截止规则

### 场景

一个 User 实体通过 `dept_ulid` 引用 Department。Department 本身有多个级联引用：

- `manager`（References，指向另一个 User）
- `parent_id`（References，自引用父部门）

当展开 `dept_ulid` 拿到 Department 后，用户希望：
- **跳过** `manager` 的展开（不需要经理信息）
- `parent_id` **展开一层后截止**（拿到父部门名称即可，不要继续递归）

### 方案

在 `HandlerConfig` 中增加 `FieldStopRules` 字段：

```go
FieldStopRules map[string][]StopRule
// key: 当前 Handler 的字段名
// value: 截止规则列表
```

**StopRule 结构：**

```go
type StopRule struct {
    OnHandler string // 目标子 Handler 注册名（如 "department"）
    Field     string // 要截止的字段名（如 "manager"）
    Stop      bool   // true=完全跳过，false=展开一层后截止
}
```

**格式规则：**

```
-handler:field   → Stop=true  → 完全跳过该字段的展开
handler:field    → Stop=false → 展开一层后截止，不再递归
```

**示例：**

```go
HandlerConfig[entity.User]{
    FieldStopRules: map[string][]handler.StopRule{
        "dept_ulid": {
            {OnHandler: "department", Field: "manager", Stop: true},  // 不查 manager
            {OnHandler: "department", Field: "parent_id", Stop: false}, // parent 只展开一层
        },
    },
}
```

**HTTP compact 格式：**

```
GET /api/v1/users/get?fstop=dept_ulid=-department:manager,department:parent_id
```

- 每个 `fstop` 参数格式：`field=规则列表`（逗号分隔）。
- 多个 `fstop` 参数可传多条规则。

**工作原理：**

1. `injectStop()` 在 HTTP 入口解析 `fdepth`/`fstop` 参数 → 注入 context。
2. `buildFieldCtx(ctx, fieldName)` 将 `FieldDepthLimits[fieldName]` 和 `FieldStopRules[fieldName]` 封装为 `fieldLimitMap` → `context.WithValue` 注入子 context。
3. 子 Handler 的 `effectiveExpandDepth()` 检查 `fieldLimitMap`：
   - 若当前字段在 map 中且有 `Stop:true` → 返回 `(0, false)` → 跳过展开。
   - 若当前字段在 map 中且有 `Stop:false` → 返回 `(1, true)` → 展开一层后截止。
   - 否则回退到字段级深度上限或全局 `MaxExpandDepth`。

---

## 补充问题：HTTP 参数长度

### 问题

`fstop` 参数可能很长（多字段、多规则），会导致 URL 超长。是否需要改用 POST？

### 分析

- **`HandlerConfig` 是主配置源**：`MaxExpandDepth`、`FieldDepthLimits`、`FieldStopRules` 在代码中设定，覆盖绝大多数场景。
- **HTTP 参数是例外覆盖**：仅当前端需要降级时使用（如管理后台点开某条记录看完整链路，前端临时传 `depth=5` / `fdepth=xxx` 等）。
- 实际上 `fdepth`/`fstop` 在日常请求中极少使用，参数长度不是实际问题。如果极少数场景确实超长，后续可考虑在 body 中增加 `_expand` 扩展字段，不改变接口语义。

---

## 涉及文件

| 文件 | 变更 |
|------|------|
| `handler/generic.go` | `HandlerConfig` 新增 `MaxExpandDepth`、`FieldDepthLimits`、`FieldStopRules`；`injectDepth`/`injectStop` HTTP 参数解析；`buildFieldCtx`/`effectiveExpandDepth`/`getStopCfg` 字段级上下文构建 |
| `handler/cascade.go` | `fieldLimitCtxKey`/`fieldLimitMap` context 类型；`StopRule` 结构体；`parseStopRule`/`parseStopRules` 解析器；`splitCSV`/`splitN` 工具函数 |
| `handler/generic_impl.go` | `_doList` 三处批量展开块使用 `buildFieldCtx` + `effectiveExpandDepth`；`expandGet` 重构 |
| `handler/cascade_test.go` | 单元测试：`TestParseStopRule`、`TestParseStopRules`、`TestFieldLimitMap`、`TestEffectiveExpandDepth`、`TestSplitCSV` |
| `handler/generic_test.go` | 添加 `//go:build dbtest` 标签（DB 依赖测试隔离） |

---

## 上下文传递流程

```
HTTP 请求
  │
  ├─ ?depth=N ──────────────→ injectDepth()  → depthCtxKey
  ├─ ?fdepth=field:depth ──→ injectStop()   → fdOverrideCtxKey
  └─ ?fstop=field=rules ───→ injectStop()   → fsOverrideCtxKey
         │
         ▼
  expandGet / _doList
         │
         ├─ getStopCfg() 合并 HandlerConfig + HTTP override
         │
         ├─ buildFieldCtx(ctx, fieldName)
         │      │
         │      ├─ FieldDepthLimits[fieldName] → 覆盖 depth
         │      └─ FieldStopRules[fieldName]   → fieldLimitMap → context
         │
         └─ 子 Handler
                │
                ├─ effectiveExpandDepth(ctx, hasDepth, field)
                │      ├─ fieldLimitMap 中有该 field → 按 Stop 规则返回
                │      └─ 否则 → depthCtxKey 中的值
                │
                └─ 递归展开或截止
```

## 优先级

1. `fieldLimitMap`（父 Handler 注入）→ 最先检查
2. 字段级 `FieldDepthLimits`（`buildFieldCtx` 注入的 depth）
3. 全局 `MaxExpandDepth`
4. 未设置任何深度控制 → 展开一层不递归（默认行为）
