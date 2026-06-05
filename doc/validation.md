# 输入校验设计文档

> 适用于任何需要 HTTP 接口参数校验的 Go 框架。  
> 核心思想：**零配置即可用 + 宽松类型转换 + 内置常用格式**。

---

## 1. 设计理念

### 1.1 为什么放在 Handler 层？

| 层级 | 职责 | 示例 |
|------|------|------|
| **Handler** | 入参格式校验（类型/长度/枚举/格式） | `page` 是否为整数、`email` 格式是否正确 |
| **Service** | 业务逻辑校验（唯一性/状态迁移/权限） | `site_code` 是否重复、状态是否允许修改 |

分开的理由：格式校验是通用的、可以自动化的；业务校验是领域相关的、需要人工编写。

### 1.2 宽松类型转换

HTTP 传输中类型信息会丢失（JSON 数字全是 float64、query string 全是字符串），因此框架在类型不匹配时**优先尝试转换**，而非直接报错：

| 输入 | 期望 | 结果 |
|------|------|------|
| `123`（JSON float64） | `string` | → `"123"` |
| `123.0`（JSON float64） | `int` | → `123` |
| `123.5`（JSON float64） | `int` | → ❌ 非整数 |
| `"123"`（字符串） | `int` | → `123` |
| `"abc"`（字符串） | `int` | → ❌ 无法解析 |
| `"1"` / `0` | `bool` | → `true` / `false` |
| `""` | `datetime` | → ❌ 空字符串时间 |

这样做的好处：前端传数字给字符串字段、传字符串给数字字段都能自动兼容，符合 MySQL/GORM 的宽松风格。

### 1.3 内置格式 vs 正则

常见格式如邮箱、手机号、日期——每个项目都要写一遍正则，不如框架内置：

```yaml
# 不用这样
pattern: "^1[3-9]\\d{9}$"

# 只需这样
format: phone
```

**防坑场景**：前端传 `""`（空字符串）给 `datetime` 字段 → MySQL 报错。`format: datetime` 会在 Handler 层拦截，给出清晰的错误信息。

---

## 2. 规则模型

### 2.1 FieldRule — 单字段规则

```go
type FieldRule struct {
    Type      string   // 期望类型: string / int / float / bool / time
    Required  bool     // 必填（Create 从 gorm not null 自动推导）
    Min       *float64 // 数值下限
    Max       *float64 // 数值上限
    MinLength *int     // 字符串最小长度
    MaxLength *int     // 字符串最大长度
    Enum      []string // 枚举值
    Pattern   string   // 正则
    Format    string   // 内置格式: datetime / date / time / email / url / phone / ulid
}
```

### 2.2 ValidateConfig — 按接口拆分

```go
type ValidateConfig struct {
    Create *EndpointRules  // map[字段名]*FieldRule
    Update *EndpointRules
    List   *EndpointRules
}
```

### 2.3 规则优先级

```
用户配置（YAML/代码）> 自动推导（gorm 反射）
```

合并策略：用户配置中**显式设置的属性**覆盖自动推导的同名字段，未覆盖的属性保留自动推导的值。

---

## 3. 自动推导

从 entity struct 反射 gorm 标签，零配置获得基础规则：

```go
type Site struct {
    SiteULID  string `gorm:"column:site_ulid;primaryKey;size:26"`  // → type=string, max_length=26, format=ulid
    SiteCode  string `gorm:"column:site_code;size:64;not null"`    // → type=string, max_length=64, required=true
    SortOrder int    `gorm:"column:sort_order"`                     // → type=int
    IsActive  bool   `gorm:"column:is_active"`                      // → type=bool
    CreatedAt time.Time `gorm:"column:created_at"`                  // → type=time
}
```

| Go 类型 | 规则 |
|---------|------|
| `string` + gorm `size:N` | `Type=string`, `MaxLength=N` |
| `string` + `*_ulid` | `Type=string`, `MaxLength=26`, `Format=ulid` |
| `int/int8/.../int64` | `Type=int` |
| `float32/64` | `Type=float` |
| `bool` | `Type=bool` |
| `time.Time` | `Type=time` |
| gorm `not null` | `Required=true`（仅 Create） |

---

## 4. 内置格式

### 4.1 列表

| Format | 说明 | 示例 |
|--------|------|------|
| `datetime` | 日期时间（支持多种格式） | `2024-01-01 10:00:00`、`2024-01-01T10:00:00Z` |
| `date` | 日期 | `2024-01-01`、`2024/01/01` |
| `time` | 时间 | `10:00:00` |
| `email` | 邮箱 | `user@example.com` |
| `url` | URL（http/https） | `https://example.com/path?q=1` |
| `phone` | 中国大陆手机号 | `13800138000` |
| `ulid` | 26位 Crockford base32 | `01JXXXXX...` |

### 4.2 datetime 多格式兼容

```go
// 依次尝试以下格式：
"2006-01-02 15:04:05"      // 最常用
"2006-01-02T15:04:05Z"     // ISO8601
"2006-01-02T15:04:05.000Z" // 带毫秒
"2006-01-02T15:04:05+08:00" // 带时区
"2006/01/02 15:04:05"      // 斜杠分隔
```

### 4.3 典型使用

```yaml
# 操作日志的查询参数 — 防空字符串
sys_operation_log:
  list:
    created_at_start:
      format: datetime
    created_at_end:
      format: datetime

# 用户注册 — 邮箱手机号格式
sys_user:
  create:
    email:
      format: email
    phone:
      format: phone
```

---

## 5. 执行流程

```
HTTP Request
  │
  ├─ Gin 绑定 JSON / Query → map[string]any
  │
  ├─ validateInput(rules, data)
  │   │
  │   ├─ 必填检查（Required + 非空）
  │   ├─ 类型转换（coerceValue: string←int, int←string, bool←"1"...）
  │   │   └─ 失败 → ErrFieldValidation("字段[xxx] 应为整数")
  │   ├─ 格式校验（Format: datetime/email/phone/...）
  │   │   └─ 失败 → ErrFieldValidation("字段[xxx] 邮箱格式不正确")
  │   ├─ 数值范围（Min/Max）
  │   ├─ 字符串长度（MinLength/MaxLength）
  │   ├─ 枚举（Enum）
  │   └─ 正则（Pattern）
  │
  ├─ CrudRequest.Validate() ← 业务校验（可选）
  │
  └─ Service hooks ← 唯一性 / 状态迁移
```

---

## 6. YAML 配置格式

### 6.1 完整示例

```yaml
# configs/validations.yaml
validations:
  sys_site:
    create:
      site_code:
        required: true
        max_length: 64
      site_type:
        enum: ["web", "app", "miniapp"]
      domain:
        format: url
    list:
      page_size:
        max: 200
      order_by:
        enum: ["site_code", "site_name", "created_at"]

  sys_user:
    create:
      email:
        format: email
      phone:
        format: phone
```

### 6.2 加载

```go
vcMap, err := handler.LoadValidationConfig("configs/validations.yaml")

handler.NewGenericHandler[*entity.SysSite](svcReg, "sys_site",
    handler.HandlerConfig[*entity.SysSite]{
        PathPrefix: "/api/v1/sys-site",
        Validate: vcMap["sys_site"],
    })
```

---

## 7. 移植指南

如果要在其他框架中复用，需要以下组件：

### 7.1 核心文件

| 文件 | 职责 |
|------|------|
| `FieldRule` / `ValidateConfig` | 规则类型定义 |
| `deriveFieldRules[M]` | 反射 gorm 标签 → 自动规则 |
| `coerceValue` | 宽松类型转换 |
| `checkFormat` | 内置格式校验 |
| `validateInput` | 规则执行引擎 |
| `mergeRules` | 用户规则覆盖自动规则 |
| `LoadValidationConfig` | YAML 加载 |

### 7.2 依赖

- Go 标准库：`reflect`、`regexp`、`strconv`、`time`、`fmt`
- 外部依赖：`gopkg.in/yaml.v3`（仅 YAML 加载，可从项目已有依赖中获取）

### 7.3 集成步骤

```go
// 1. 定义 entity（带 gorm 标签）
type User struct {
    ID    int64  `gorm:"column:id;primaryKey"`
    Email string `gorm:"column:email;size:128;not null"`
}

// 2. 构造 Handler 时初始化校验规则
h := &MyHandler{
    rules: deriveFieldRules[User](),
}

// 3. 在 Create/Update/List 入口处调用 validateInput
func (h *MyHandler) Create(ctx *gin.Context) {
    var raw map[string]any
    ctx.ShouldBindJSON(&raw)

    if err := validateInput(h.rules, raw, "create"); err != nil {
        ctx.JSON(400, gin.H{"error": err.Error()})
        return
    }
    // raw 中的值已自动转换类型，直接使用
    // ...
}
```

---

## 8. 常见问题

### Q: 类型转换会不会把 null 转成 "null"？

不会。`nil` 值在必填检查之后直接跳过，不会进入类型转换。

### Q: `Required` 对 bool 字段怎么处理？

`Required=true` 且值为 `false` 时**不会报错**（`false` 不是空值）。只有 `nil` 或不存在才报错。

### Q: `format: date` 和 `type: time` 有什么区别？

- `type: time` 只检查值是否为非空字符串（最宽松）
- `format: date` 进一步校验字符串必须符合 `2006-01-02` 格式
- 两者可同时使用：`{Type: "time", Format: "date"}`

### Q: 原有 crudRequest.Validate() 还能用吗？

能。框架校验在 `Validate()` 之前执行，两者互不干扰：

```
validateInput（框架） → Validate()（业务） → Service hooks（服务）
```

### Q: Update 接口为什么不做 Required 检查？

Update 是部分更新——用户只传要修改的字段，不传的保持原样。框架只校验已传入字段的类型和格式。
