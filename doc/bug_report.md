# gocrux Bug 报告

> 由 heims 项目发现并记录。heims 不改 vendor 下 gocrux 代码，所有问题到此报告。

---

## 待修

（暂无）

---

## BUG-030 详细分析：Update handler `id` 缺失时错误信息过于笼统（已修 ✅）

**发现问题方**：heims 项目，2026-07-16

### 问题描述

`POST /{prefix}/update` 时，当请求体未包含 `"id"` 字段（如客户端误用了实体 PK 字段名 `"site_menu_ulid"` 而非约定的 `"id"`），gocrux 返回：

```json
{"code": 4001, "msg": "参数无效"}
```

调用方无法从错误信息中得知具体哪个参数缺失。

### 根因

`handler/generic_write.go:220-224` — 校验失败时一律返回 `errs.ErrInvalidParam`（通用 "参数无效"），未指明缺失的是 `id` 字段：

```go
rid, ok := raw["id"]
if !ok || rid == nil {
    h.handleError(c, errs.ErrInvalidParam)  // ← 太笼统
    return
}
```

### 修复建议

将错误信息改为包含具体参数名，便于调用方排查：

```go
rid, ok := raw["id"]
if !ok || rid == nil {
    h.handleError(c, errs.ErrFieldValidation("id", "缺少主键参数"))
    return
}
```

或直接在错误消息中指明：

```go
h.handleError(c, fmt.Errorf("缺少必需参数: id"))
```

### 影响范围

- 所有实体的 `POST /{prefix}/update` 接口
- 任何传错 `id` 字段名的客户端都会看到无帮助的 "参数无效"

---

## BUG-022 详细分析：级联展开 / Reference 展开不支持子实体字段过滤

**发现问题方**：heims 项目，2026-07-09

### 问题描述

`ListSkipFields` / `ListKeepFields` / `?fields=` 仅作用于主实体，不穿透到级联子表或 Reference 展开的子实体。当仅需要子实体的部分字段时（如收件箱展开 `NotifyContent` 只需 `title`/`content`/`senderName`），会拖回全量数据。

### 根因

`handler/generic_read_impl.go` 字段裁剪循环仅 `delete(row, f)` 主实体顶层 key，不递归进入 `row["cascade_child"]` 或 `row["reference_entity"]` 的嵌套 map。

`FieldStopRules` / `StopRule` 只能控制展开深度（展开/不展开），不能表达「展开后保留哪些字段」。

### 修复方案

支持 `?fields=title,content,notify_content:title,notify_content:senderName` 或类似语法，由 `pruneFields` 递归处理嵌套 map。

### 影响范围

- heims 收件箱（`/notify-delivery/list`）：每个 delivery 展开 content 时带回 ~15 个字段，实际只需要 ~5 个
- 所有使用 Reference 展开的场景都存在此浪费

---

## BUG-023 详细分析：不支持批量更新（UpdateMany）

**发现问题方**：heims 项目，2026-07-09

### 问题描述

gocrux 只有单条 `UpdateByID(ctx, id, updates)`，没有批量 `UpdateMany(filter, updates)`。heims 的通知「标记已读」场景需要 `UPDATE notify_delivery SET isRead=true WHERE receiverUlid=?`（批量），但只能走自定义 handler 直连 MongoDB。

### 根因

- Handler 层 `update()` 只接受单个 `raw["id"]`，然后逐条 `svc.Update`
- Repository 层 `Repo` 接口无 `UpdateMany` 方法，`MongoCRUDRepository` 内部有 `UpdateMany` 调用但仅用于 `BatchDeprecateVersions` / `BatchSoftDelete`，不暴露

### 修复方案

1. `Repository.Repo` 接口新增 `UpdateMany(ctx, filter, updates map[string]any) (int64, error)`
2. `MongoCRUDRepository` / `GormCRUDRepository` 各自实现
3. Handler 层新增 `POST /{prefix}/batch-update` 路由，接受 `{ids: [...], data: {...}}` 或 `{filter: {...}, data: {...}}`

### 影响范围

- heims 通知 Read / Recall（批量标记已读/撤回）
- 任何需要「按条件批量更新字段」的场景

---

## BUG-020 详细分析：控制参数命名不统一

**发现问题方**：heims 项目，2026-07-09

### 问题描述

`allControlParams` 中 16 个参数，12 个 snake_case（`page_size`、`order_by`、`follow_published`），4 个 camelCase（`expandAll`、`ignoreRef`、`ignoreCascade`、`ignoreAll`），命名风格不一致。

### 背景

- **URL Query String 的 web 生态惯例是 snake_case**（GitHub `per_page`、Google `max_results`、腾讯云 `OrderField`），不是编程语言变量命名
- 16 个参数中已有 75% 是 snake_case，剩下 4 个是早期实现时图方便直接拼写
- gocrux 还在 v0.1.x 早期，现在改影响最小，等上线后再改就会变成永久技术债

### 修复方案

```go
// allControlParams 中 4 个 key 改名
"expandAll"    → "expand_all"
"ignoreRef"    → "ignore_ref"
"ignoreCascade" → "ignore_cascade"
"ignoreAll"    → "ignore_all"
```

### 影响范围

- `handler/validation.go:79-100` → `allControlParams` 4 个 key 改名
- `handler/generic_read.go` → 检查是否有硬编码引用这些 key 的地方
- heims `constants/field_code.go` → 同步 `ReservedFieldCodes`（如果 BUG-021 已修，则无需单独同步）
- 前端 → 如有调用 `expandAll` 等参数，改为 `expand_all`

### 不推荐方案

强行要求业务方的 JSON body / field_code 也用 snake_case 与框架参数对齐。业务数据字段命名是应用层的自由选择，框架层只需要管好自己的 namespace。

---

## BUG-021 详细分析：控制参数判断函数需对外暴露

**发现问题方**：heims 项目，2026-07-09

### 问题描述

heims 需要在 sysop 配置 form field 时校验 `field_code` 是否为框架保留关键字（防止 MongoDB 业务数据字段与控制参数冲突）。目前 heims 自行维护了一份 `ReservedFieldCodes` map 与 gocrux 保持同步，但这是一份重复维护的副本，gocrux 加新参数时 heims 容易漏同步。

### 修复方案

`handler/validation.go` 中将 `isFrameworkMetaParam`（小写，私有）改为公开函数：

```go
// IsFrameworkControlParam 判断 key 是否为框架控制参数。
// 外部（如 heims）可用此函数校验 field_code 不与之冲突。
func IsFrameworkControlParam(key string) bool {
    return allControlParams[key]
}
```

原来私有函数 `isFrameworkMetaParam` 可保留为别名或直接删除，内部调用点统一改用公开函数。

### 影响范围

- gocrux: `handler/validation.go` → 函数改名 + 公开
- heims: `constants/field_code.go` → 删除 `ReservedFieldCodes` map，`ValidateFieldCode` 改为调用 `handler.IsFrameworkControlParam()`

---

## BUG-019 详细分析：List handler 控制参数与字段过滤参数混淆

**发现问题方**：heims 项目，2026-07-09

### 问题描述

gocrux `List()` Handler 将所有 URL Query 参数全量收集为 `filters map[string]any`，然后用三处不同的代码、三张不同的清单来剔除控制参数（`page`/`depth`/`ignore` 等），这三张清单互相不一致，导致部分控制参数会泄漏到 Service 层被当作字段过滤条件，或反之被校验拒绝。

#### 三张清单不一致对照表

| 控制参数 | Handler `delete()` 是否剔除 | `isFrameworkMetaParam()` 是否识别 | `_doList()` `popXxx` 是否提取 | 结论 |
|----------|:---:|:---:|:---:|------|
| `page` | ❌ | ✅ | ✅ (popIntParam) | Handler 未剔除，靠 Service 层 `popIntParam` 补救 |
| `page_size` | ❌ | ✅ | ✅ (popIntParam) | 同上 |
| `order_by` | ❌ | ✅ | ✅ (popStrParam) | 同上 |
| `order_dir` | ❌ | ✅ | ✅ (popStrParam) | 同上 |
| `depth` | ✅ | ✅ | ❌ | 一致（Handler 剔除 + validate 识别） |
| `fdepth` | ✅ | ✅ | ❌ | 一致 |
| `fstop` | ✅ | ✅ | ❌ | 一致 |
| `ignore` | ✅ | ✅ | ❌ | 一致 |
| `ignoreRef` | ✅ | ✅ | ❌ | 一致 |
| `ignoreCascade` | ✅ | ✅ | ❌ | 一致 |
| `ignoreAll` | ✅ | ✅ | ❌ | 一致 |
| `expand` | ✅ | ❌ | ❌ | Handler 剔除了，但 validate 不认识（如 RejectUnknownFields 会报"无效字段"） |
| `expandAll` | ✅ | ❌ | ❌ | 同上 |
| `follow_published` | ❌ | ✅ | ❌ | Handler 未剔除，validate 识别但仅跳过不报错 |
| `keyword` | ❌ | ❌ | ✅ (_doList 特殊处理 delete) | Handler 未剔除，validate 也不认识；若 RejectUnknownFields=true 会报错 |
| `fields` | ❌ | ❌ | ❌ | 三处都不认识！`fields` 由 `withFields(ctx, c.Query("fields"))` 消费后残留 filters 中 |
| `sort` | ❌ | ❌ | ❌ | 完全未识别，会泄漏到过滤条件 |

#### 具体影响

1. **`page`/`page_size`/`order_by`/`order_dir`** 泄漏到 Service 层，靠 `_doList` 的 `popIntParam`/`popStrParam` 补救，但没有被 `delete(map, key)` 从 filters 中移除，理论上**如果 Entity 恰好有同名字段**（例如有 `order_by` 列），会被当作过滤条件。

2. **`keyword`** 在 `_doList` 中 `delete(q, "keyword")` 特殊处理，但在此之前 `validateInput` 已经检查过了。如果 `RejectUnknownFields=true`，`keyword` 会因为不在 Entity 字段表中而被拒绝（`isFrameworkMetaParam` 不认识它）。

3. **`fields`** 参数在 `List()` 中被读取 2 次（行 376、389），但从未从 filters map 中 delete。它会原样传到 Service → `_doList` → `knownColumns[M]()` 过滤。如果 Entity 有名为 `fields` 的字段（不太可能但非零概率），会被误用。

4. **`sort`** 参数完全不被识别。许多 API 使用 `sort=-created_at` 或 `sort=created_at:desc` 格式而非 `order_by`+`order_dir`。当前会直接泄漏为过滤条件（`knownColumns[M]()` 兜底但不可靠）。

#### 根因

**缺乏单一控制参数清单。** 三处代码各自维护一份列表：

| 位置 | 文件 | 用途 |
|------|------|------|
| `List()` 的 `delete()` 调用 | `handler/generic_read.go:377-386` | 从 filters 中剔除 |
| `isFrameworkMetaParam()` | `handler/validation.go:77-86` | validateInput 时跳过 |
| `_doList()` 的 `popIntParam`/`popStrParam` | `service/generic_read_impl.go:90-91` | 提取分页/排序参数 |

三张列表不一致，且目前依赖 `knownColumns[M]()` 做兜底过滤——但这依赖于 Entity 每个字段都有正确的 gorm/bson/json tag，不可靠。

#### 修复建议

**方案（推荐）：统一控制参数清单 + Handler 层集中剔除**

在 `handler/` 包中定义**唯一的**框架控制参数集合：

```go
// allControlParams 框架层面所有非字段过滤的 URL Query 参数。
// Handler List() 用它来一次性剔除；validateInput 用它来判断是否跳过。
var allControlParams = map[string]bool{
    // 分页 & 排序
    "page":      true,
    "page_size": true,
    "order_by":  true,
    "order_dir": true,
    // 展开 & 深度
    "depth":        true,
    "fdepth":       true,
    "fstop":        true,
    "expand":       true,
    "expandAll":    true,
    // 忽略
    "ignore":       true,
    "ignoreRef":    true,
    "ignoreCascade": true,
    "ignoreAll":    true,
    // 其他
    "follow_published": true,
    "keyword":      true,
    "fields":       true,
}
```

**Handler `List()`**：收集所有 query params 后，**一次性遍历剔除**：

```go
for key := range filters {
    if allControlParams[key] {
        delete(filters, key)
    }
}
```

**`isFrameworkMetaParam()`** 改为引用 `allControlParams`（或合并）。

**Service `_doList()`**：移除 `popIntParam("page")` / `popStrParam("order_by")` 等调用（Handler 已剔除），直接从 `page`/`page_size` 等 HTTP query 或 ListRequest 结构体获取。`knownColumns[M]()` 降级为 defence-in-depth。

#### 额外建议：`sort` 参数支持

如有需求，可考虑支持 `sort=field:desc` 格式（单字段），或 `sort=-created_at,+name` 格式（REST 惯例）。目前 gocrux 使用 `order_by` + `order_dir` 两个参数，与许多前端框架（如 Element Plus `sort-change` 事件传 `{prop, order}`）一致，是否改名请决策方决定。

#### 关于字段参数前缀约定（`f:`）

heims 讨论过是否引入 `f:status=active` → 字段过滤，`page=1` → 控制参数这样的前缀约定。

**结论：暂不需要。** gocrux 只要将控制参数全部剔除，剩余参数即为字段过滤参数，不需要前缀。存在极端情况——如果某个 Entity 的字段恰好与某个控制参数重名（如 `page`），目前无解，但这属于极低概率场景，可通过在 Entity 字段上使用不同 DB 列名规避。如需彻底解决，可考虑引入前缀，但当前优先级不高。

---

## 二次修复

| 编号 | 描述 | 根因 | commit |
|------|------|------|--------|
| BUG-005-R | Activate status 不更新 | BatchDeprecateVersionsByFK 覆盖目标行 + UpdateByID 静默失败 | 本次（改为分目标/非目标两步处理，用 Save 持久化） |
| BUG-006-R | Draft 不出现在 List | filterToBson 缺 or_group 处理，整个 OR 组静默丢弃 | 本次（新增 or_group→$or 转换） |
| BUG-010-R | edit-version 仍 4001 | patches key 可能是 GORM/bson/JSON 三种列名，双向查找不够 | 本次（遍历所有 patches key，通过 resolveColumn 匹配） |
| BUG-012 | Update 返回 item→items | BUG-007 从 data 改 item，但与 Create 的 items 数组不一致 | 本次（改为 items 数组包装） |

---

## 已修

| 编号 | 描述 | commit |
|------|------|--------|
| BUG-030 | 全面排查并修复15处笼统的"参数无效"错误，替换为具体的 ErrMissingParam("参数名") | 本次 |
| BUG-029 | ListVersions/ListArchivedVersions `items`→`versions`（前端兼容） | 本次 |
| BUG-028 | 版本化 Create 未设 isCurrent/versionStatus + _doList bool→int8 → LIST 双重死链 | 本次 |
| BUG-027 | ListByFilters 无排序时传 nil sortDoc → List 加 nil 校验 + ListByFilters 按需传参 | 本次 |
| BUG-026 | _doList 版本化分支缺 isDeleted 过滤 → 补上与版本化对齐的软删除过滤 | 本次 |
| BUG-025 | _doGetByCode 硬编码 "is_deleted" → 改为 s.config.DeletedField | 本次 |
| BUG-024 | MongoCRUDRepository.List 忽略排序 → ListByFilters 构建 bson.D 排序传入 | 本次（⚠ 引入 BUG-027 回归） |
| BUG-023 | 批量更新：新增 batch-update-simple 接口 + UpdateByIDs（Repository/Service/Handler） | 本次 |
| BUG-022 | ListSkipFields/ListKeepFields 支持 key:sub 嵌套子表字段裁剪 | 本次 |
| BUG-020 | 控制参数命名统一：4 个 camelCase→snake_case（expand_all/ignore_ref/ignore_cascade/ignore_all） | 本次 |
| BUG-021 | isFrameworkMetaParam→IsFrameworkControlParam 公开，供外部校验 field_code | 本次 |
| BUG-019 | List handler 控制参数三张清单不一致 → 统一 allControlParams + keyword 提升到 handler 层 | 本次 |
| BUG-016 | _doList 无默认分页和排序 → 加 Page=1/PageSize=20/OrderBy=created_at DESC 兜底 | 本次 |
| BUG-018 | 非版本化级联更新全量替换：DoDeleteByFK 后 passToChild=true 强制子数据走 CREATE | 本次 |
| BUG-014 | README 补 gin.Context→context.Context 桥接说明 + _doActivate 类型修正 + SetFieldValue 扩展 | 本次 |
| BUG-015 | SetFieldValue bool→int(X)/T→*T/*T→T 静默失败 → 新增 4 种转换 + _doActivate 类型修正 | 本次 |
| BUG-005 | Activate nil panic + status 不更新 → 二次修复：分目标/非目标两步 + Save 持久化 | 本次 |
| BUG-006 | Draft 可见性 → 二次修复：filterToBson 新增 or_group→$or 转换 | 本次 |
| BUG-007 | Update data.data 双层嵌套 → 最终改为 items 数组 | 本次 |
| BUG-008 | version_remark 默认值覆盖 → 检查非空 | ✅ 回归验证通过 |
| BUG-009 | versions 缺 total/items → 已补齐 | ✅ 回归验证通过 |
| BUG-010 | edit-version patches key 不匹配 → 二次修复：遍历所有 key 通过 resolveColumn 匹配 | 本次 |
| BUG-011 | /versions-archived 404 → 新增路由 | ✅ 回归验证通过 |
| BUG-001 | 版本化 Delete 标记全部同 code 版本 | 本次修复 |
| BUG-002 | MongoDB isCurrent bool vs int8 类型不匹配 | 本次修复 |
| BUG-003 | ReferenceRelation 不支持点分路径 Field | 本次修复 |
| BUG-004 | _doDelete 版本化路径 CRUDRepo nil | 已确认无此问题（_doDelete 全走 Repo 接口，无 CRUDRepo 调用） |
| - | mergeByJSON 空串覆盖 SetDefaults | `b47cf92` |
| - | CRUDRepo nil 空指针 MongoDB Update | `a9dccfd` |
| - | keyword 参数直接当 SQL 列名 | `df5fafd` |
| - | _beforeUpdateVersioned 深拷贝后缺 SetDefaults | `10e1d2a` |
| - | resolveColumn 只认 gorm 不认 bson | `364d386` |
| - | validateInput 拒绝动态 schema 实体字段 | `eaf9715` |
| - | MongoCRUDRepository.List page<=0 → skip 负值 | `eaf9715` |

---

## BUG-027 详细分析：ListByFilters 无排序时传 nil sortDoc → SetSort(nil) → BSON 崩溃（P0）

**发现问题方**：heims 项目，2026-07-13

### 问题描述

heims 业务数据 `POST /biz/{form_code}/create` 返回 500 错误：

```
MongoDB查询失败: cannot marshal type primitive.D to a BSON Document:
WriteNull can only write while positioned on a Element or Value but is positioned on a TopLevel
```

### 触发链路（完整 trace）

```
1. heims 修复 → _beforeCreate 中 DataCode 为空时自动生成 ULID
   修复前：code="" → _doCreate 行 158 skip ListByFilters
   修复后：code="01Jxxx..." → 进入 ListByFilters 校验

2. _doCreate (generic_write_impl.go:161)
   → s.repo.ListByFilters(ctx, ListFilters{
       Filters: [{Field:"dataCode", Op:OpEQ, Value:"01Jxxx..."}],
       Page:1, PageSize:1,
       // ← OrderBy 为空，无需排序
     })

3. ListByFilters (mongo_repo.go:313-324)  ← 关键！
   var sortDoc bson.D                    // nil
   if filters.OrderBy != "" { ... }      // OrderBy 为空，跳过
   return r.List(ctx, f, page, pageSize, sortDoc)  // 始终传 sortDoc！

4. List (mongo_repo.go:229, variadic ...bson.D):
   → sortDoc 接收为 []bson.D{nil}   // 长度=1，但元素是零值 nil
   → len(sortDoc) > 0              // TRUE！
   → opts.SetSort(sortDoc[0])      // SetSort(nil) ← BSON 崩溃！
```

### 根因（回归）

**BUG-024 的修复引入了此回归。** 修复前 `ListByFilters`：

```go
// 原始代码（未排序）
func (r *MongoCRUDRepository[M]) ListByFilters(...) {
    f := toBsonFilter(filters)
    return r.List(ctx, f, filters.Page, filters.PageSize)  // ← 不传 sortDoc
    // 此时 List 中 sortDoc 为空切片 []bson.D{}，len=0 → 不调 SetSort ✅
}
```

BUG-024 修复后：
```go
// 修复后（始终传 sortDoc）
func (r *MongoCRUDRepository[M]) ListByFilters(...) {
    f := toBsonFilter(filters)
    var sortDoc bson.D                          // nil
    if filters.OrderBy != "" {
        sortDoc = bson.D{{Key: ..., Value: ...}}
    }
    return r.List(ctx, f, filters.Page, filters.PageSize, sortDoc)  // ← 无排序时传 nil
    // 此时 List 中 sortDoc 为 []bson.D{nil}，len=1 → 调 SetSort(nil) ❌
}
```

### 修复方案（二选一）

**方案 A（推荐，defence-in-depth）— `List` 方法加 nil 校验：**

```go
// mongo_repo.go:245-247
if len(sortDoc) > 0 && sortDoc[0] != nil {
    opts.SetSort(sortDoc[0])
}
```

**方案 B — `ListByFilters` 按需传参：**

```go
// mongo_repo.go:323
if filters.OrderBy != "" {
    return r.List(ctx, f, filters.Page, filters.PageSize, sortDoc)
}
return r.List(ctx, f, filters.Page, filters.PageSize)
```

建议**两个都改**：`List` 加 nil 校验防止类似问题，`ListByFilters` 按需传参避免无意义的参数传递。

### 影响范围

- 🔴 **所有通过 `ListByFilters` 且无排序的 MongoDB 查询全部崩溃**
- heims 所有版本化表单（`is_versioned=true`）的 CREATE 操作（`/biz/*/create`）→ 崩溃
- 任何调用 `ListByFilters` 且 `OrderBy=""` 的业务 → 崩溃

### 触发条件

1. 使用 `MongoCRUDRepository`
2. 调用 `ListByFilters` 且 `filters.OrderBy == ""`
3. 仅在 `_doCreate` 的 code 查重（code != ""）或 `checkUniqueDB`（code != "" 分支）中被触发

在 heims 场景中，`checkUniqueDB` 的 error 被 `validateUnique` 吞掉（返回 `false` 而非 error），所以**该 bug 只在 `_doCreate` 的 code 查重路径中暴露**。

### 验证方法

```go
// 最小复现
repo := NewMongoCRUDRepository[BizRecord]("test_coll")
// 无排序 → 应触发崩溃
_, _, err := repo.ListByFilters(ctx, ListFilters{
    Filters: []Filter{{Field: "dataCode", Op: OpEQ, Value: "01Jxxx"}},
    Page: 1, PageSize: 1,
})
// 预期：err != nil，信息含 "cannot marshal type primitive.D"
```

---

## BUG-017 详细分析：字段校验错误被吞为 500

### 问题

`handler/errors.go:mapServiceError` 未覆盖 `errs.ErrFieldValidation`。当 List 参数校验失败时（如 `page_size=1000` 超过 `defaultListRules` 中 Max=100），`validateInput` 返回 `ErrFieldValidation("page_size", "不能大于 100")`，但 `mapServiceError` 中没有 `errors.Is(err, errs.ErrFieldValidation)` 分支，走到最后的 `return constants.CodeInternalError`。

随后 `handleError` 因 code==CodeInternalError 调用 `InternalError(c, err)`，返回 HTTP 500 "系统发生错误，请联系管理员"，客户端无法获知具体错误。

### 根因

`ErrFieldValidation` 是纯 `fmt.Errorf`，不是哨兵错误，`errors.Is` 无法匹配：

```go
// errors/errors.go
func ErrFieldValidation(field, reason string) error {
    return fmt.Errorf("字段[%s] %s", field, reason)
}
```

### 修复建议

**方案 A（推荐）**：定义哨兵 + Wrap

```go
var errFieldValidation = errors.New("field validation")

func ErrFieldValidation(field, reason string) error {
    return fmt.Errorf("字段[%s] %s: %w", field, reason, errFieldValidation)
}
```

然后在 `mapServiceError` 中增加：

```go
if errors.Is(err, errs.ErrFieldValidation) {
    return constants.CodeParamError
}
```

**方案 B**：用自定义错误类型（struct），`errors.As` 匹配。

**不可选方案**：字符串匹配 `strings.Contains`（脆弱，不推荐）。

### 影响范围

所有 List/Create/Update 的字段校验失败都返回 500 而非具体错误信息。

---

## BUG-024 详细分析：MongoCRUDRepository.List 完全忽略排序参数

**发现问题方**：heims 项目，2026-07-13

### 问题描述

`MongoCRUDRepository.List` 方法完全不处理 `ListFilters.OrderBy` / `ListFilters.OrderDir`。即使 Service 层 `_doList` 设置了默认排序 `order_by=created_at&order_dir=desc`，MongoDB 查询的 `Find()` 调用也不带 `SetSort()`，结果无序。

### 根因

**1. `ListByFilters` 只转换 filter，丢弃排序信息**

`repository/mongo_repo.go:310-313`：

```go
func (r *MongoCRUDRepository[M]) ListByFilters(ctx context.Context, filters ListFilters) ([]M, int64, error) {
    f := toBsonFilter(filters)   // 只转 filter，丢弃 OrderBy/OrderDir
    return r.List(ctx, f, filters.Page, filters.PageSize)
}
```

**2. `List` 方法不设置排序**

`repository/mongo_repo.go:229-263` — `Find()` 的 options 仅设置 `Skip` 和 `Limit`，无 `SetSort`：

```go
opts := options.Find().SetSkip(skip).SetLimit(int64(pageSize))
cursor, err := r.ReadColl(ctx).Find(ctx, filter, opts)
```

**3. 默认排序字段名是 MySQL 风格**

`service/generic_read_impl.go:100-103`：

```go
if f.OrderBy == "" {
    f.OrderBy = "created_at"   // snake_case，仅适合 MySQL
    f.OrderDir = "desc"
}
```

MongoDB 实体通常使用 camelCase（如 `createdAt`）。

### 修复方案

`MongoCRUDRepository.List` 或 `ListByFilters` 需支持排序：

```go
func (r *MongoCRUDRepository[M]) List(ctx context.Context, filter bson.M, page, pageSize int, sortFields ...string) ([]M, int64, error) {
    // ...
    opts := options.Find().SetSkip(skip).SetLimit(int64(pageSize))
    if len(sortFields) > 0 {
        sortDoc := bson.D{}
        for _, sf := range sortFields {
            dir := 1  // asc
            field := sf
            if strings.HasPrefix(sf, "-") {
                dir = -1
                field = sf[1:]
            }
            sortDoc = append(sortDoc, bson.E{Key: field, Value: dir})
        }
        opts.SetSort(sortDoc)
    }
    // ...
}
```

或者从 `ListFilters.OrderBy + OrderDir` 构建 `bson.D` 排序文档。

**建议**：默认排序字段 `created_at` 也应支持 `resolveColumn` 转换（BSON/GORM column 解析），使其同时兼容 MySQL 和 MongoDB。

### 影响范围

- heims 业务数据 `GET /biz/{form_code}/list`：MongoDB 返回结果无序
- 所有使用 `MongoCRUDRepository` 的 List 查询均无排序

---

## BUG-025 详细分析：_doGetByCode 硬编码 "is_deleted" 字段名

**发现问题方**：heims 项目，2026-07-13

### 问题描述

`_doGetByCode` (service/generic_read_impl.go:64) 中 `is_deleted` 字段名是硬编码字符串，而非通过 `s.config.DeletedField` 获取。对于 MongoDB 实体（如 heims 的 BizRecord，bson tag 为 `isDeleted`），这个硬编码字段名会导致软删除过滤逻辑完全失效。

### 代码位置

`service/generic_read_impl.go:59-68`：

```go
results, _, err := s.repo.ListByFilters(ctx, repository.ListFilters{
    Filters: []repository.Filter{
        {Field: codeCol, Op: repository.OpEQ, Value: code},
        {Field: currentCol, Op: repository.OpEQ, Value: int8(1)},
        {Field: "is_deleted", Op: repository.OpEQ, Value: int8(0)},  // ← 硬编码 snake_case
    },
    Page:     1,
    PageSize: 1,
})
```

对比 `_doList` 中正确的做法（使用 `s.config.DeletedField`）：

```go
field := s.config.DeletedField
if field == "" {
    field = "is_deleted"
}
```

### 修复方案

将硬编码 `"is_deleted"` 改为使用 `s.config.DeletedField`（fallback 到 `"is_deleted"`）：

```go
delField := s.config.DeletedField
if delField == "" {
    delField = "is_deleted"
}
// ...
{Field: delField, Op: repository.OpEQ, Value: int8(0)},
```

### 影响范围

- 所有使用 `_doGetByCode` 的版本化 MongoDB 实体（Code 查询路径）
- 当前 heims 暂不受直接影响（BizRecord 的版本化路径很少走 `_doGetByCode`），但任何未来使用 `GetByCode` 的 MongoDB 实体都会受影响

---

## BUG-026 详细分析：_doList 版本化分支缺少 isDeleted 过滤

**发现问题方**：heims 项目，2026-07-13

### 问题描述

`_doList` (service/generic_read_impl.go:147-174) 的版本化分支只添加了 `isCurrent = 1` 过滤和草稿可见性过滤，**没有添加 `isDeleted = 0` 过滤**。而非版本化分支（第 175-188 行）正确地添加了软删除过滤。

这导致版本化模式下，已软删除（`isDeleted = 1`）的记录会出现在列表查询结果中。

### 代码对比

**`_doGetByCode`（正确 — 同时加 is_deleted）**：
```go
Filters: []repository.Filter{
    {Field: codeCol, Op: repository.OpEQ, Value: code},
    {Field: currentCol, Op: repository.OpEQ, Value: int8(1)},
    {Field: "is_deleted", Op: repository.OpEQ, Value: int8(0)},  // ✅ 有软删除过滤
},
```

**`_doList` 版本化分支（缺失）**：
```go
if s.config.VersionMode && s.config.VersionFields != nil {
    vf := s.config.VersionFields
    f.Filters = append(f.Filters, repository.Filter{
        Field: resolveColumn[M](vf.CurrentField), Op: repository.OpEQ, Value: int8(1),
    })
    // ... 草稿可见性逻辑 ...
    // ❌ 缺少 isDeleted 过滤！
}
```

### 修复方案

在版本化分支末尾添加软删除过滤（与非版本化分支保持一致）：

```go
if s.config.VersionMode && s.config.VersionFields != nil {
    // ... 现有 isCurrent + 草稿逻辑 ...

    // 添加软删除过滤（与非版本化分支对齐）
    m := newRecord[M]()
    if m.SetDelete() {
        field := s.config.DeletedField
        if field == "" {
            field = "is_deleted"
        }
        val := s.config.DeletedValue
        if val == nil {
            val = int8(0)
        }
        f.Filters = append(f.Filters, repository.Filter{Field: field, Op: repository.OpEQ, Value: val})
    }
}
```

### 影响范围

- 所有版本化模式的 List 查询：已软删除的记录会出现在列表中
- heims 有 `is_versioned` 属性的表单会在 List 时泄漏软删除记录

---

## BUG-028 详细分析：版本化 Create 未设 isCurrent/versionStatus + _doList 版本化过滤 bool vs int8 类型不匹配（P0）

**发现问题方**：heims 项目，2026-07-14

### 问题描述

heims 版本化表单（`is_versioned=true`）`GET /biz/{form_code}/list` 始终返回 `total=0`，即使用户已通过同一服务成功创建多条记录。

### 根因分析

**双重死亡**——两处独立 bug 叠加，导致 LIST 永返回空。

#### Bug A：`_beforeCreate` 从未设置 `isCurrent` 和 `versionStatus`

`service/generic_write_impl.go:56-61`：

```go
if s.config.VersionMode && s.config.VersionFields != nil {
    vf := s.config.VersionFields
    if getStrField(&m, vf.VersionField) == "" {
        common.SetFieldValue(&m, vf.VersionField, "v1.0")  // ✅ 只设了 versionCode
    }
    // ❌ 没有设 vf.CurrentField = 1
    // ❌ 没有设 vf.StatusField = "published"
}
```

对比 Update 流程（同文件第 360 行）**正确设置了 `isCurrent`**：

```go
common.SetFieldValue(&newEntity, vf.CurrentField, int8(1))  // ✅ Update 时设了
```

**结果**：Create 的记录 `isCurrent` 永远为 Go 零值 `int8(0)`，`versionStatus` 永远为空字符串。

#### Bug B：`_doList` 版本化过滤用 `bool true` 匹配 `int8` 字段

`service/generic_read_impl.go:157-159`：

```go
f.Filters = append(f.Filters, repository.Filter{
    Field: resolveColumn[M](vf.CurrentField), Op: repository.OpEQ, Value: true,
})
```

`Value: true` 是 Go `bool` → 编码为 BSON `{"isCurrent": true}`（**布尔类型**）。

但实体（如 heims `BizRecord`）的 `IsCurrent` 字段是 `int8` → MongoDB 中存储为 BSON **int32** 类型的 `0` 或 `1`。

在 MongoDB 严格类型匹配中，**`true` ≠ `int32(1)`**，所以即使修了 Bug A，LIST 也匹配不到任何记录。

### 完整死链

```
Create → isCurrent=0, versionStatus=""（Bug A）
  → BSON: {"isCurrent": 0 (int32)}
  → LIST filter: {"isCurrent": true (bool)}  ← Bug B，true ≠ 0=不匹配
  → 即使手动改 MongoDB 为 isCurrent=1
  → LIST filter: {"isCurrent": true (bool)}  ← Bug B，true ≠ 1 仍不匹配
  → total=0
```

### 修复方案

**Bug A** — `_beforeCreate` 补设 `isCurrent` + `versionStatus`（`service/generic_write_impl.go:56-61`）：

```go
if s.config.VersionMode && s.config.VersionFields != nil {
    vf := s.config.VersionFields
    common.SetFieldValue(&m, vf.CurrentField, int8(1))  // ← 新增
    if vf.StatusField != "" {
        common.SetFieldValue(&m, vf.StatusField, string(VersionStatusPublished))  // ← 新增
    }
    if getStrField(&m, vf.VersionField) == "" {
        common.SetFieldValue(&m, vf.VersionField, "v1.0")
    }
}
```

**Bug B** — `_doList` 版本化过滤把 `true` 改成 `int8(1)`（`service/generic_read_impl.go:157`）：

```go
f.Filters = append(f.Filters, repository.Filter{
    Field: resolveColumn[M](vf.CurrentField), Op: repository.OpEQ, Value: int8(1),  // ← bool true → int8(1)
})
```

### 影响范围

- 🔴 **所有版本化实体的 LIST 接口全部返回空列表**
- heims 受影响：`control_test_0713` 及所有 `is_versioned=true` 表单的 `GET /biz/*/list`
- 所有通过 `_doList` 版本化分支查询的场景：`total` 永远为 0

### 验证方法

1. 通过 gocrux Create 一条版本化记录
2. 查 MongoDB：`db.collection.findOne({})` → 确认 `isCurrent` 为 `0`（Bug A）
3. 调用 List → 确认 `total=0`（Bug A + Bug B 共同作用）
4. 手动 MongoDB update 设 `isCurrent=1`
5. 再次 List → 确认仍 `total=0`（Bug B 单独作用：`bool true` ≠ `int32 1`）

### 与 BUG-002 的关系

`BUG-002`（已修列表第 277 行）标题为"MongoDB isCurrent bool vs int8 类型不匹配"，描述与本 BUG-028 的 Bug B 相同问题。可能 BUG-002 的修复不完整（如只修了 `_doGetByCode` 没修 `_doList`），或修复后在其他位置回归。建议 gocrux 侧一并复查所有 `isCurrent` 过滤的位置。

---

---
## BUG-031 详细分析：版本化级联更新时自引用 FK 代码解析失效（P0）

**发现问题方**：heims 项目，2026-07-17

### 问题描述

`SysSiteMenu` 版本化 UPDATE 时，子表 `SysMenuItem` 的自引用 FK（`parent_item_ulid`）映射失败。前端发送 `parent_menu_code`（代码），期望后端解析为 `parent_item_ulid`（ULID），但 UPDATE 后所有子菜单项的层级关系丢失（全平铺）。

### 根因

**heims 中间件 `menu_self_fk.go` 在请求进入前预处理 `parent_menu_code → parent_item_ulid`，但 gocrux `_doUpdate` 第 246-250 行无条件删除子数据主键，导致中间件预设的 ULID 被丢弃**：

```go
// handler/generic_write_impl.go:246-250
if passToChild && hasChildren {
    for j := range childData {
        delete(childData[j], childHandler.PKField())  // 删除 menu_item_ulid
        delete(childData[j], "id")
    }
}
```

**流程分析**：

| 步骤 | CREATE（成功 ✅） | UPDATE（失败 ❌） |
|:---|:---|:---|
| 1. 中间件 | 生成新 ULID + 解析 `parent_menu_code` | 生成新 ULID + 解析 ✅ |
| 2. gocrux `_doUpdate` | N/A | **删除 `menu_item_ulid`** |
| 3. `_beforeCreate` → `SetID()` | 复用中间件 ULID | **重新生成新 ULID** |
| 4. `_beforeCreate` → `MergeTo` | `parent_item_ulid` = 中间件值 | `parent_item_ulid` = 中间件值（→旧 ULID） |
| 5. 写入 DB | ✅ 正确 | ❌ `parent_item_ulid` 指向不存在的旧记录 |

**结论**：外部中间件预处理方式对版本化级联更新无效——gocrux 必须为每个新版本子记录生成**全新** ULID，中间件无法预知这些新 ULID。解决方案必须放在 gocrux `_doUpdate` 内部，在 PK 清除 → 重新生成 → MergeTo 之前完成 code→ULID 解析。

### 修复方案

在 gocrux `handler/generic_write_impl.go` 的 `_doCreate` 和 `_doUpdate` 中新增 `resolveSelfFKCodeRefs` 函数：

**检测约定**：若子数据中存在 `parent_xxx_code` 格式的虚拟字段，且去前缀后的字段（如 `menu_code`）也存在于子数据中，则触发自引用 FK 解析。

**解析流程**：
1. 为每个子项生成新的 PK ULID（填充已清除的 PK 字段）
2. 构建业务代码（如 `menu_code`）→ 新 ULID 映射
3. 将代码字段（如 `parent_menu_code`）解析为 ULID 填入 FK 字段（如 `parent_item_ulid`）
4. 移除虚拟代码字段

**涉及文件**：
- `handler/cascade.go`：`CascadeHandler` 接口新增 `SelfFKField() string`
- `handler/generic_write.go`：`GenericHandler` 新增 `SelfFKField()` 方法实现
- `handler/generic_write_impl.go`：新增 `resolveSelfFKCodeRefs()` + 在 `_doCreate`/`_doUpdate` 中调用

**heims 侧配套变更**：
- `internal/middleware/menu_self_fk.go`：UPDATE / edit-version 路径跳过自 FK 解析（透传给 gocrux 内部处理），CREATE 路径保持现有逻辑

### 影响范围
- gocrux: 所有存在自引用 FK（`SelfFKField() != ""`）且使用代码字段传递层级关系的版本化实体
- heims: `SysSiteMenu` 级联更新 `SysMenuItem`（`parent_menu_code → parent_item_ulid`）修复
- 当前仅 `SysMenuItem` 实现了 `SelfFKField()`，其他实体不受影响

### 验证方法
1. 通过前端菜单编辑器修改 `default` 菜单（添加/调整子菜单项）
2. 调用 `GET /api/v1/site-menu/list?code=default` 确认 `parent_menu_code` 字段正确
3. 调用 `GET /api/v1/menu/list` 确认侧边栏菜单树层级正确

---
## BUG-029 详细分析：ListVersions / ListArchivedVersions 返回 `items` 而非 `versions`（P1）

**发现问题方**：heims 项目，2026-07-15

### 问题描述

三个版本化实体（site、site-menu、role）的 `/versions` 接口返回 `data.items` + `data.total`，但前端 `VersionHistory.vue` 期望 `data.versions`，导致版本历史面板始终显示空列表。

### 复现

```
GET /api/v1/site/versions?code=labvoyage
GET /api/v1/site-menu/versions?code=01KXJDG7GNTDXDYNAA9FQ1HJBB
GET /api/v1/role/versions?id=xxx
```

**后端返回**：

```json
{ "code": 200, "data": { "items": [...], "total": 6 } }
```

**前端期望**：

```json
{ "code": 200, "data": { "versions": [...], "total": 6 } }
```

### 根因

`handler/generic_version.go` 两处硬编码 `"items"` 作为响应 key：

**第 135 行** — `ListVersions`：
```go
Success(c, gin.H{"items": versions, "total": len(versions)})
```

**第 303 行** — `ListArchivedVersions`：
```go
Success(c, gin.H{"items": archived, "total": len(archived)})
```

作为对比，所有标准 List 接口返回 `items` 是合理的（前端列表组件读取 `data.items`），但 `/versions` 是独立的版本管理接口，前端有专门的 `VersionHistory.vue` 组件读取 `data.versions`，`items` 语义不匹配。

### 修复方案

将两处 `"items"` 改为 `"versions"`：

```go
// ListVersions (line 135)
Success(c, gin.H{"versions": versions, "total": len(versions)})

// ListArchivedVersions (line 303)
Success(c, gin.H{"versions": archived, "total": len(archived)})
```

### 影响范围

- 所有使用 gocrux 版本化实体的 `/versions` 和 `/versions-archived` 接口
- heims 受影响：site、site-menu、role 三个版本化实体的版本历史面板

### heims 侧临时修复

已直接在 heims vendor 副本中修改 `vendor/github.com/Huey1979/gocrux/handler/generic_version.go`，gocrux 源修复后再通过 `go get` 同步。


---
