# gocrux Bug 报告

> 由 heims 项目发现并记录。heims 不改 vendor 下 gocrux 代码，所有问题到此报告。

---

## 待修

（空——所有已知 Bug 已修复）

---

## 已修

| 编号 | 描述 | commit |
|------|------|--------|
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
