# gocrux Bug 报告

> 由 heims 项目发现并记录。heims 不改 vendor 下 gocrux 代码，所有问题到此报告。

---

## 待修

### BUG-001: 版本化 Delete 标记全部同 code 版本

- **发现**：2026-06-25
- **位置**：`service/generic_write_impl.go` `_doDelete`
- **现象**：版本化实体 Delete 时，`BatchDeprecateVersionsByFK` 按 code 标记全部版本为 deprecated，而非仅标记指定 ULID
- **期望**：只标记指定版本
- **影响**：heims 业务数据删除

### BUG-002: MongoDB `isCurrent` bool vs int8 类型不匹配

- **发现**：2026-06-28
- **位置**：`service/generic_read_impl.go` `_doList`
- **现象**：版本过滤 `isCurrent = int8(1)` 对 MongoDB bool 字段不生效，List 查不到记录
- **期望**：版本过滤值类型与实体字段类型一致
- **影响**：heims BizRecord 版本化 List

### BUG-003: ReferenceRelation 不支持点分路径 Field

- **发现**：2026-06-29
- **位置**：`handler/generic_read.go` `expandGet`
- **现象**：`ReferenceRelation.Field = "fields.dept_id"` 时，FK 值在嵌套 map 中，`out[ref.Field]` 无法直接读取
- **期望**：使用 `getByPath` 读取 FK 值
- **影响**：heims BizRecord 外键展开（dept_id → dept 信息）

### BUG-004: `_doDelete` 版本化路径 CRUDRepo nil（同 a9dccfd）

- **发现**：2026-06-29
- **位置**：`service/generic_write_impl.go` `_doDelete`
- **现象**：与 `_doUpdate` 同样问题，版本化 Delete 调用 `cr.Transaction()` 对 MongoDB nil panic
- **期望**：同 `a9dccfd` 修复方式，nil 时走 repo 方法
- **影响**：heims BizRecord 版本化 Delete

---

## 已修

| 编号 | 描述 | commit |
|------|------|--------|
| - | mergeByJSON 空串覆盖 SetDefaults | `b47cf92` |
| - | CRUDRepo nil 空指针 MongoDB Update | `a9dccfd` |
| - | keyword 参数直接当 SQL 列名 | `df5fafd` |
| - | _beforeUpdateVersioned 深拷贝后缺 SetDefaults | `10e1d2a` |
| - | resolveColumn 只认 gorm 不认 bson | `364d386` |
| - | validateInput 拒绝动态 schema 实体字段 | `eaf9715` |
| - | MongoCRUDRepository.List page<=0 → skip 负值 | `eaf9715` |
