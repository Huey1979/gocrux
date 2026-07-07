# gocrux Bug 报告

> 由 heims 项目发现并记录。heims 不改 vendor 下 gocrux 代码，所有问题到此报告。

---

## 待修

| 编号 | 描述 | 根因 |
|------|------|------|
| — | （无） | |

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
| BUG-016 | _doList 无默认分页和排序 → 加 Page=1/PageSize=20/OrderBy=created_at DESC 兜底 | 本次 |
| BUG-018 | 非版本化级联更新全量替换：DoDeleteByFK 后 passToChild=true 强制子数据走 CREATE | 本次 |
| BUG-019 | handleError 吞掉错误详情 → 新增 InternalErrorWithDetail，统一透传 err.Error() | 本次 |
| BUG-020 | MySQL JSON 列收到非法字符串 → checkFormat 新增 "json" 格式校验 + deriveFieldRules 自动识别 gorm type:json | 本次 |
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

## BUG-017 详细分析：字段校验错误被吞为 500 ✅ 已修复

### 现状

当前代码中 `ErrFieldValidation` 已通过 `%w` 包裹哨兵错误 `errFieldValidationSentinel`，`mapServiceError` 中 `IsFieldValidation(err)` 已正确匹配并映射为 `CodeParamError`。此问题已不存在。

### 原始分析（供参考）

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

## BUG-019 详细分析：handleError 吞掉详细错误，通用 500 "系统发生错误"

### 问题

当 gocrux 的 CRUD 操作（Create/Update/Delete/Get/List）发生错误时，`GenericHandler.handleError()` 调用 `mapServiceError(err)` 将错误映射为 BusinessCode。若映射结果是 `CodeInternalError`（即错误类型未被 `mapServiceError` 识别），则调用 `InternalError(c, err)`，后者**仅记录日志**，返回给客户端的是通用消息 `"系统发生错误，请联系管理员。错误编号：xxx"`。

**结果**：前端永远看不到真正的错误原因，只能去翻服务端日志。

### 链路

```
handleError(c, err)                          ← handler/generic_util.go:569
  │
  ├─ code = mapServiceError(err)             ← handler/errors.go:11
  │      只匹配: ErrRecordNotFound/ErrInvalidParam/
  │      ErrDuplicateCode/ErrUniqueValidationFailed/
  │      IsFieldValidation/ErrVersionNotEnabled/
  │      ErrVersionFieldsNotSet/ErrInvalidVersionStatusTransition
  │
  │      → 不匹配 → return CodeInternalError
  │
  └─ code == CodeInternalError
       └─ InternalError(c, err)              ← handler/response.go:86
            日志: logrus.Error("内部错误", "error": err.Error())
            返回: {"code":500, "msg":"系统发生错误，请联系管理员"}
            ❌ err.Error() 被丢弃！
```

### 落入 500 的典型错误

以下 gocrux 自身的格式化错误有明确的业务语义，但 `mapServiceError` 未覆盖它们，全部落入 500：

| 错误 | 来源 | 实际消息示例 |
|------|------|-------------|
| `ErrCascadeCreate` | cascade.go | `"级联创建write_field失败: ..."` |
| `ErrCascadeUpdate` | cascade.go | `"级联更新write_field失败: ..."` |
| `ErrCascadeDelete` | cascade.go | `"级联删除write_field失败: ..."` |
| `ErrCascadeQuery` | generic_read.go | `"向下级联查询write_field失败: ..."` |
| `ErrRefResolve` | generic_read.go | `"向上级联解析site失败: ..."` |
| `ErrCascadeActivate` | cascade.go | `"级联激活write_field失败: ..."` |
| `ErrMarshalEntity` | generic_read.go | `"序列化实体失败: ..."` |
| `ErrUnmarshalEntity` | generic_read.go | `"反序列化实体失败: ..."` |
| `ErrQueryRecordFailed` | generic_write_impl.go | `"查询待更新记录失败: ..."` |
| 原始 MySQL/MongoDB 错误 | repository | `"Error 1062: Duplicate entry 'xxx'"` |
| 自定义 Hook 返回的 error | heims service 层 | 各种业务错误消息 |

### 根因

两处不当设计叠加：

**1. `mapServiceError` 覆盖不足**
`handler/errors.go` 只对少数 sentinel 错误做了 `errors.Is` 检查，大量 gocrux 自己生成的格式化错误（如 `ErrCascadeCreate`、`ErrRefResolve`）没有任何映射，直接 fallthrough 到 `CodeInternalError`。

**2. `handleError` 对 500 错误丢弃了消息**
`generic_util.go:572` — 当 code 为 `CodeInternalError` 时调用 `InternalError`，后者**记录日志但不把 `err.Error()` 返回给客户端**。

### 修复建议

#### 方案 A（推荐）：handleError 统一透传错误消息

修改 `handler/generic_util.go` 的 `handleError`，不再对 `CodeInternalError` 区别对待：

```go
// handler/generic_util.go
func (h *GenericHandler[M]) handleError(c *gin.Context, err error) {
    code := mapServiceError(err)
    // 统一透传错误消息给前端，不再吞掉
    InternalErrorWithDetail(c, code, err)
}
```

同时修改 `handler/response.go`，新增：

```go
// InternalErrorWithDetail 内部错误响应（带错误详情）
// 将原始 err 记录到日志，返回业务码 + err.Error()。
func InternalErrorWithDetail(c *gin.Context, code constants.BusinessCode, err error) {
    requestID := getRequestID(c)
    _, file, line, _ := runtime.Caller(1)
    logrus.WithFields(logrus.Fields{
        "log_id": requestID,
        "error":  err.Error(),
        "caller": fmt.Sprintf("%s:%d", file, line),
    }).Error("内部错误")

    c.JSON(200, Response{
        Code:      int(code),
        Msg:       err.Error(),
        RequestID: requestID,
    })
}
```

**优点**：所有错误消息一次性透传，零遗漏。
**注意**：需要确认 err.Error() 中没有敏感信息（数据库密码、连接串等）。gocrux 的格式化函数只包装业务信息，不暴露连接凭据。

#### 方案 B（保守）：扩展 mapServiceError 覆盖格式化错误包装

用 `errors.Is` 匹配更多 gocrux 内置错误类型。但这要求所有格式化函数都用 `%w` 包装哨兵错误，目前 `errors.go` 中这些函数确实用了 `%w`：

```go
func ErrCascadeCreate(handlerName string, cause error) error {
    return fmt.Errorf("级联创建%s失败: %w", handlerName, cause)
}
```

因此可以在 `mapServiceError` 中遍历 gocrux 的格式化包装函数链，但嵌套的 error 类型未知，仍会有遗漏。

**优点**：改动最小。
**缺点**：覆盖不彻底，新错误类型仍需持续补充映射。

#### 方案 C：在 mapServiceError 中增加 fallback 兜底

```go
func mapServiceError(err error) constants.BusinessCode {
    // ... 现有 sentinel 匹配 ...
    
    // 兜底：gocrux 格式化错误默认映射为 400（参数/操作错误）
    // 真正的系统异常（如 panic）由 Recovery 中间件处理
    return constants.CodeBadRequest
}
```

**优点**：改动最小，不暴露原始 DB 错误。
**缺点**：所有未识别错误变成 400，语义不精确。

### 推荐

**方案 A**，理由：
1. gocrux 的格式化错误函数本身就是面向用户的，直接用 `err.Error()` 返回给前端即可
2. `Recovery` 中间件已处理真实 panic（进程级崩溃），不会走 `handleError`
3. 前端能获得完整的错误信息链，大幅提升可调试性
4. 安全性：DB 凭据/连接串不出现在 gocrux 格式化错误中，原始 MySQL/MongoDB 错误虽然暴露表名/列名，但对已有权限的后台用户是可接受的

### 影响范围

- 所有 gocrux CRUD 操作的错误返回（Create/Update/Delete/Get/List 及其级联子操作）
- 涉及文件：`handler/generic_util.go`（改 handleError）、`handler/response.go`（增 InternalErrorWithDetail）

---

## BUG-020 详细分析：MySQL JSON 列收到非法字符串触发 Error 3140

### 问题

heims `sys_form` 表 `list_page_size_options` 列（MySQL JSON 类型）收到非 JSON 字符串（如 CSV `"10,20,50"`），GORM 写入时 MySQL 报错：

```
Error 3140 (22032): Invalid JSON text: "The document root must not be followed by other values."
at position 2 in value for column 'sys_form.list_page_size_options'.
```

gocrux 校验层未对 JSON 列做格式检查，非法值直接透传到数据库。

### 根因

1. `validateInput` → `checkFormat` 没有 `"json"` 内置格式
2. `deriveFieldRules` 未自动识别 gorm `type:json` 列，未标记 `format: "json"`

### 修复

1. `checkFormat` 新增 `case "json": json.Valid([]byte(s))` 校验
2. `deriveFieldRules` 新增两处自动检测：
   - gorm 标签含 `type:json` → 自动 `Format = "json"`, `Type = "string"`
   - Go 字段类型为 `json.RawMessage` → 同上

### 影响范围

- 所有 gorm `type:json` / `type:jsonb` 列的 Create/Update 操作
- 涉及文件：`handler/validation.go`
