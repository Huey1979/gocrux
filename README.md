# gocrux

**gocrux** 是一个 Go 语言通用 CRUD 后端框架，提供泛型化的 Handler / Service / Repository 三层架构，支持级联操作、版本管理和审计日志。

## 核心特性

- **泛型 CRUD 框架** — `GenericHandler[M]` → `GenericService[M]` → `CRUDRepository[M]` 完整的泛型 CRUD 管线
- **钩子系统** — Handler 和 Service 层均提供 before/do/after 三阶段钩子，可覆盖任意环节
- **级联机制** — 父 Handler 通过 `CascadeHandler` 接口委托子 Handler，`TxCoordinator` 保证事务一致性
- **版本管理** — 内置 Draft/Published 双态版本控制
- **身份识别钩子** — `Authenticator` / `Authorizer` 接口，使用者自由实现认证和授权方式
- **操作日志** — 自动记录 CRUD 操作审计日志
- **幂等支持** — 内置幂等缓存，避免重复创建

## 快速开始

```go
package main

import (
    "github.com/Huey1979/gocrux/handler"
    "github.com/Huey1979/gocrux/service"
    "github.com/Huey1979/gocrux/repository"
    "github.com/gin-gonic/gin"
)

// 1. 定义实体
type MyEntity struct {
    ULID string `gorm:"column:ulid;primaryKey"`
    Name string `gorm:"column:name"`
}

func (e *MyEntity) SetDefaults()        {}
func (e *MyEntity) SetCreatedAt(t time.Time) {}
func (e *MyEntity) SetUpdatedAt(t time.Time) {}
func (e *MyEntity) GetULID() string     { return e.ULID }

// 2. 定义请求
type MyRequest struct {
    Name string `json:"name" binding:"required"`
}
func (r *MyRequest) MergeTo(entity any) { /* 合并逻辑 */ }

// 3. 创建 Handler
func setupRouter(r *gin.Engine) {
    repo := repository.NewCRUDRepository[MyEntity]()
    svc := service.NewGenericService(repo, service.Config[MyEntity]{
        EntityName: "my_entity",
    })
    h := handler.NewGenericHandler(svc, handler.HandlerConfig{
        EntityName: "my_entity",
        RequestFactory: handler.NewRequestFactory(func() any { return &MyRequest{} }),
    })
    h.RegisterRoutes(r.Group("/api/v1/my_entities"))
}
```

## 身份识别

框架提供 `Authenticator` 和 `Authorizer` 两个钩子接口，使用者按需实现：

```go
// 实现认证器
type JWTAuth struct{}
func (a *JWTAuth) Middleware() gin.HandlerFunc {
    return func(c *gin.Context) {
        token := c.GetHeader("Authorization")
        // 验证 token，注入用户信息
        c.Set("user", handler.UserInfo{ULID: "xxx", Name: "张三"})
        c.Next()
    }
}
func (a *JWTAuth) FromContext(c *gin.Context) (handler.UserInfo, bool) {
    v, ok := c.Get("user")
    return v.(handler.UserInfo), ok
}

// 注入认证器
middleware.DefaultAuthenticator = &JWTAuth{}
```

## 项目结构

```
├── cmd/            # 入口
├── handler/        # HTTP 处理层（泛型 Handler + 钩子 + 级联）
├── service/        # 业务逻辑层（泛型 Service + 幂等 + 日志）
├── repository/     # 数据访问层（泛型 CRUD + 版本管理）
├── internal/       # 框架内部
│   ├── bootstrap/  # 启动引导
│   ├── config/     # 配置管理
│   ├── database/   # 数据库连接（MySQL/MongoDB/Redis）
│   ├── logger/     # 日志系统
│   ├── middleware/ # HTTP 中间件
│   ├── model/      # 框架内置实体
│   └── router/     # 路由注册
├── common/         # 通用工具
├── constants/      # 状态码
├── errors/         # 错误定义
└── config.yaml     # 配置文件
```

## 代码生成器

`tools/gentity` 是一个**独立辅助工具**，用于根据 MySQL 表结构自动生成实体定义和注册蓝图代码。

### 安装

```bash
go build -o gentity.exe ./tools/gentity/
```

### 使用

```bash
# 单表生成
gentity --dsn "user:pass@tcp(localhost:3306)/dbname?charset=utf8mb4&parseTime=true" \
        --table users --out generated

# 全库生成
gentity --dsn "user:pass@tcp(localhost:3306)/dbname?charset=utf8mb4&parseTime=true" \
        --all --out generated
```

### 生成物

```
generated/
├── entity/
│   └── users.go              # struct + TableName + Record 接口实现
└── blueprint/
    ├── blueprints.go         # Blueprints 管理器（共享）
    └── users.go              # 注册蓝图（Repository → Service → Handler → Routes）
```

### 集成方式

1. 复制 `generated/entity/*.go` → `internal/model/entity/`
2. 复制 `generated/blueprint/*.go` → 项目中合适的包（如 `internal/generated/`）
3. 在 `cmd/main.go` 中注册：

```go
import (
    bp "yourproject/internal/generated"
    "github.com/Huey1979/gocrux/internal/bootstrap"
)

func main() {
    // ... 初始化 ...

    // 数据库迁移
    bootstrap.Migrate(&entity.User{})

    // 注册蓝图路由
    blues := bp.NewBlueprints(serviceReg, handlerReg)
    blues.RegisterUser(apiGroup)
}
```

### 自动识别

生成器会根据列名模式自动处理框架约定：

| 列名模式 | 处理方式 |
|---------|---------|
| `*_ulid`（主键） | `SetID()` 自动生成 ULID |
| `id`（自增） | `SetID()` 空操作 |
| `created_at/updated_at` | 自动注入时间戳 |
| `created_by/updated_by` | 自动注入操作人 |
| `is_deleted/deleted_at` | 启用软删除 |
| 列注释 | 保留为字段注释 |

## 配置

编辑 `config.yaml` 配置 MySQL、MongoDB、Redis 连接信息，启动服务：

```bash
go run ./cmd/
```

## License

MIT
