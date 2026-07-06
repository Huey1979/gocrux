# gocrux Bug 报告

> 由 heims 项目发现并记录。heims 不改 vendor 下 gocrux 代码，所有问题到此报告。

---

## 待修

（空——所有已知 Bug 已修复）

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
