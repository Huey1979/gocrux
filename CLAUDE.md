# gocrux 项目 CLAUDE.md

> 自研 Go 泛型 CRUD 框架。heims 低代码平台的核心依赖。
> 更新于 2026-06-29

---

## 1. 项目概述

gocrux 是一个基于 Go 泛型的 CRUD 框架，提供统一的数据操作管线：
`Handler → Service → Repository`。

### 1.1 核心能力

- **泛型 CRUD**：`GenericHandler[M]` + `GenericService[M]` + `CRUDRepository[M]`
- **版本管理**：草稿/发布/废弃/归档状态机，支线版本切换
- **级联操作**：父→子 CascadeRelation（Create/Update/Delete），子→父 ReferenceRelation（Get 展开）
- **钩子系统**：Before/Do/After 三层钩子，Handler 层 + Service 层
- **双存储**：MySQL（GORM `CRUDRepository`）+ MongoDB（`MongoCRUDRepository`）
- **事务编排**：`TxCoordinator` 跨 Handler 事务

### 1.2 项目结构

```
gocrux/
├── handler/          # HTTP Handler 层：管线编排、级联、展开、校验
├── service/          # Service 层：CRUD 业务逻辑、版本管理
├── repository/       # Repository 层：CRUDRepository(MySQL) / MongoCRUDRepository
├── errors/           # 哨兵错误定义
├── constants/        # 业务状态码
├── common/           # ULID、reflect、事务上下文
├── expression/       # 表达式求值引擎（JSON 格式运算符）
├── internal/         # 内部工具：bootstrap/config/database/logger/middleware/router
└── tools/gentity/    # 代码生成工具
```

---

## 2. 核心架构

### 2.1 管线模式

```
HTTP Request → GenericHandler
  ├── beforeXxx (hook/默认)
  ├── doXxx    (hook/默认)
  └── afterXxx  (hook/默认)
```

Handler 和 Service 各有独立钩子系统。Handler 钩子可注入自定义逻辑（如 heims 的权限校验、公式计算）。

### 2.2 三类关系

| 关系 | 方向 | 生效场景 | FK 注入方式 |
|------|------|----------|-------------|
| CascadeRelation | 父→子 | Create/Update/Delete | `child[FKField] = parentPK` |
| ReferenceRelation | 子→父 | 仅 Get 展开 | `out[ResultField] = resolve(FK)` |
| ChildRefRelation | 父→子(FK列表) | 仅 Get 展开 | 批量解析 FK 列表 |

FKField 支持**点分路径**（`fields.parentUlid`），setByPath/getByPath 自动穿越嵌套 map。

### 2.3 HandlerConfig 关键配置

| 配置 | 说明 |
|------|------|
| `Cascades` | 级联子表声明 |
| `References` | 向上级联声明 |
| `KeywordFields` | 关键字搜索（支持精确/模糊） |
| `SkipAutoValidate` | 动态 schema 实体跳过自动字段校验 |
| `MaxExpandDepth` | GET 展开深度上限（默认 10） |
| `ListSkipFields` | List 时不返回的字段 |
| `NormalizeFields` | JSON 字段自动初始化 |

---

## 3. 关键设计决策

| 决策 | 原因 |
|------|------|
| PKField() 返回 JSON key 非 DB 列名 | DoUpdate 级联从 JSON map 提取 PK，与 DB 列名不一致会 500 |
| Reference 不影响 Create/Update | 仅 GET 展开，Create 时 FK 值存储即可 |
| ULID 格式校验仅对 primaryKey | FK `_ulid` 字段可能存短 ID（如 "01AC001"） |
| Create 时 code 冲突返回 409 | 不静默覆盖，提示用户换 code 或 Update |
| 版本化 Delete 标记全部同 code | 业务上同一 code 族应全部不可见（已知待改） |
| 深拷贝后必须 SetDefaults | `_beforeUpdateVersioned` 拷贝旧值（含 deprecated），新版本需重置 |
| 点分路径 FKField | MongoDB 实体业务字段在 `fields.*` 内，FK 也需要穿透 |
| `resolveColumn` 回退到 bson tag | MongoDB 实体无 gorm tag，否则列名错误 |

---

## 4. 当前状态（2026-06-29）

### 4.1 已完成的修改

| commit | 内容 |
|--------|------|
| `6c485d6` | ULID 格式校验只对 primaryKey 生效 |
| `c5f07bb` | Create 时 code 冲突返回 409 + ErrDuplicateCode |
| `d197c63` | deprecateByCode helper |
| `6345dbb` | Create 拒绝重复 code（不覆盖） |
| `df5fafd` | `_doList` 内置 keyword 搜索（精确/模糊） |
| `b47cf92` | `mergeByJSON` 空串不覆盖 SetDefaults 值 |
| `eaf9715` | `SkipAutoValidate` + Mongo page 边界防护 |
| `364d386` | `resolveColumn` bson tag 回退 |
| `a9dccfd` | `_doUpdate` MongoDB 空指针修复（CRUDRepo nil） |
| `10e1d2a` | `_beforeUpdateVersioned` 深拷贝后 SetDefaults |
| `6ff83fd` | `?fields=` 参数响应裁剪 |
| `472dc84` | `setByPath/getByPath` 点分路径工具函数 |

### 4.2 已知问题

（2026-06-30 全部已修复——见 `doc/bug_report.md` 已修列表）

---

## 5. 开发约定

- **未获许可不动代码**
- **commit 后报 hash 给用户 push，不要自己推**（GitHub 网络不稳定时）
- **MongoDB 实体配置**：`SkipAutoValidate: true`，FKField 用 `fields.xxx` 点分路径
- **PKField() 返回值 = JSON key，不是 DB 列名**
- **ListSkipFields 影响级联展开**：`_doList` 走 ListPipeline，配置了 Cascades 的子表不可配 ListSkipFields
- **不要用业务错误码接管 HTTP 状态码**：API 成功返回 200，错误在 JSON body 业务码表达

---

## 6. 待办事项

- [x] ReferenceRelation 支持点分路径 Field（2026-06-30 已修复）
- [x] 版本化 Delete 只标记指定版本（2026-06-30 已修复）
- [x] MongoDB `isCurrent` bool vs int8 类型匹配（2026-06-30 已修复）
- [x] `_doDelete` 版本化路径 MongoDB 适配（确认无此问题，_doDelete 全走 Repo 接口）
- [ ] `trigger_conditions` 表达式评估（目前空实现）
- [ ] `execFunction` 函数注册表
- [ ] `shouldExpandField` 中 fieldLimit 与 depth 优先级确认
- [ ] handler/generic_util.go 超 500 行，需拆分

---

## 7. 常见坑

1. **ListSkipFields + Cascades**：级联展开走 `_doList`，被跳过的字段在子数据中也会缺失
2. **PKField vs DB 列名**：`DoUpdate` 用 PKField 从 JSON map 取 PK，必须匹配 JSON key
3. **mergeByJSON 覆盖**：请求中的空字符串会覆盖 SetDefaults 生成的值（已修）
4. **vendor 与 go.mod 不同步**：heims 使用 vendor 模式，改 gocrux 后需同步 vendor
5. **伪版本时间戳**：commit 后 push 到 GitHub 的时间戳才是 canonical
6. **点分路径在 bson.M 中**：`bson.M{"fields.parentUlid": val}` 是 MongoDB 原生语法，无需特殊处理
