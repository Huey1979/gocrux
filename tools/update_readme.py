"""Update gocrux README with missing sections."""
import os

readme_path = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), 'README.md')
with open(readme_path, 'r', encoding='utf-8') as f:
    content = f.read()

# ========== 1. MongoDB section ==========
mongo_section = '''
## MongoDB 支持

gocrux 通过 `MongoCRUDRepository` 和 `Repo[M]` 接口提供完整的 MongoDB 支持，与 MySQL/GORM 对等。

### 架构

```
GenericHandler[M]  ->  GenericService[M]
                          |
                          v
                     Repo[M]  (接口)
                    /         \\
         CRUDRepository[M]   MongoCRUDRepository[M]
           (MySQL/GORM)         (MongoDB)
```

### MongoCRUDRepository

提供与 `CRUDRepository` 一致的 CRUD 接口，底层使用 MongoDB：

```go
import "github.com/Huey1979/gocrux/repository"

// 创建 MongoDB 仓储（Collection 名称对应 MySQL 表名）
repo := repository.NewMongoCRUDRepository[entity.Product]("products")

// CRUD 操作（与 GORM 版相同）
product, _ := repo.GetByID(ctx, "01Jxxx...")
products, total, _ := repo.ListByFilters(ctx, repository.ListFilters{
    Filters: []repository.Filter{
        {Field: "status", Op: repository.OpEQ, Value: "active"},
    },
    Page: 1, PageSize: 20,
})
```

支持的 `ListByFilters` 操作符：`OpEQ`、`OpNEQ`、`OpLike`（转 `$regex`）、`OpGT`/`OpGTE`/`OpLT`/`OpLTE`、`OpIn`、`OpRange`。

`MongoCRUDRepository` 也提供 `Batch` 系列批量方法：`BatchSoftDelete`、`BatchSoftDeleteByFK`、`BatchFindByPK`、`BatchFindByFK`、`BatchHardDelete`、`BatchHardDeleteByFK`。

### Repo[M] 接口

`repository/repo.go` 定义了统一的仓储接口，`CRUDRepository`（MySQL/GORM）与 `MongoCRUDRepository`（MongoDB）均实现此接口：

```go
type Repo[M any] interface {
    Insert(ctx context.Context, entity *M) error
    InsertBatch(ctx context.Context, entities []*M) error
    GetByID(ctx context.Context, id any) (*M, error)
    GetByField(ctx context.Context, field string, value any) (*M, error)
    Save(ctx context.Context, entity *M) error
    UpdateByID(ctx context.Context, id any, updates map[string]any) error
    Delete(ctx context.Context, id any) error
    DeleteByFK(ctx context.Context, fkField string, fkValues []any) error

    BatchSoftDelete(ctx context.Context, ids []any) error
    BatchSoftDeleteByFK(ctx context.Context, fkField string, fkValues []any) error
    BatchFindByPK(ctx context.Context, ids []any) ([]M, error)
    BatchFindByFK(ctx context.Context, fkField string, fkValues []any) ([]M, error)
    BatchHardDelete(ctx context.Context, ids []any) error
    BatchHardDeleteByFK(ctx context.Context, fkField string, fkValues []any) error

    ListByFilters(ctx context.Context, filters ListFilters) ([]M, int64, error)
    ListAll(ctx context.Context) ([]M, error)
    ListByField(ctx context.Context, field string, value any) ([]M, error)

    RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
    PKField() string
}
```

### 使用 MongoDB 的 GenericService

通过 `NewGenericServiceWithRepo` 注入任意 `Repo[M]` 实现：

```go
repo := repository.NewMongoCRUDRepository[entity.Product]("products")
svc := service.NewGenericServiceWithRepo(repo, service.Config[entity.Product]{
    EntityName: "product",
})
h := handler.NewGenericHandlerWithSvc(svc, handler.HandlerConfig[entity.Product]{
    PathPrefix: "/api/v1/product",
})
```

### TxCoordinator — MySQL + MongoDB 事务编排

```go
tc := handler.NewTxCoordinator(mysqlDB, mongoDB)

// 自动选择：ctx 中有 mongo session -> RunMongo，否则 -> RunMySQL
tc.Run(ctx, func(txCtx context.Context) error {
    // CRUDRepository / MongoCRUDRepository 自动感知 txCtx 中的事务
    return nil
})

// 显式指定
tc.RunMySQL(ctx, func(txCtx context.Context) error { ... })
tc.RunMongo(ctx, func(txCtx context.Context) error { ... })
```

### 事务上下文传递

```go
// common/tx.go — Repository 内部自动检测
ctx = common.WithTx(ctx, gormTx)            // MySQL 事务注入
ctx = common.WithMongoSession(ctx, sess)    // MongoDB Session 注入

tx := common.GetTx(ctx)                     // CRUDRepository 获取事务
sess := common.GetMongoSession(ctx)         // MongoCRUDRepository 获取 Session
```

### 服务组装对比

| 组件 | MySQL | MongoDB |
|------|-------|---------|
| 仓储 | `NewCRUDRepository[M]()` | `NewMongoCRUDRepository[M]("coll_name")` |
| Service | `NewGenericService(repo, cfg)` | `NewGenericServiceWithRepo(repo, cfg)` |
| 底层 | GORM -> `gorm.DB` | mongo-driver -> `mongo.Collection` |
| 事务 | `db.Transaction()` | `sess.WithTransaction()` |

---
'''

# Insert after the version routes table (end of Quick Start)
marker = '| `POST` | `/{prefix}/edit-version` | 修改版本元数据 |\n\n---\n\n## 实体定义'
pos = content.find(marker)
if pos < 0:
    # Try alternative ending
    marker = '修改版本元数据 |\n\n---\n\n## 实体定义'
    pos = content.find(marker)
if pos > 0:
    end_of_table = pos + len('| `POST` | `/{prefix}/edit-version` | 修改版本元数据 |\n\n')
    content = content[:end_of_table] + mongo_section + content[end_of_table:]
    print('1. MongoDB section INSERTED')
else:
    print('1. MongoDB marker NOT FOUND at pos', pos)

# ========== 2. KeywordFields section (insert before 列表查询条件) ==========
keyword_section = '''### KeywordFields — 关键字搜索

配置 `KeywordFields` 后，List 接口的 `?keyword=xxx` 参数自动对这些字段做 OR LIKE 搜索：

```go
HandlerConfig[entity.SysForm]{
    KeywordFields: []string{"form_code", "form_name"},
}
```

```http
GET /api/v1/form/list?keyword=员工&page=1&page_size=20
```

等价于 `WHERE form_code LIKE '%员工%' OR form_name LIKE '%员工%'`，与其它过滤器 AND 组合。

---

'''

marker2 = '\n## 列表查询条件'
pos2 = content.find(marker2)
if pos2 > 0:
    content = content[:pos2] + '\n' + keyword_section + content[pos2+1:]
    print('2. KeywordFields section INSERTED')
else:
    print('2. 列表查询条件 marker NOT FOUND')

# ========== 3. Update ?code= documentation ==========
old_code = 'GET /api/v1/sites/get?code=S001     # 直接按 code 查 published 版本'
new_code = 'GET /api/v1/sites/get?code=S001     # 按 code 查当前生效版本（is_current=1, is_deleted=0）'
if old_code in content:
    content = content.replace(old_code, new_code)
    print('3. ?code= doc UPDATED')
else:
    print('3. ?code= old doc NOT FOUND')

# ========== 4. NormalizeFields section (after MapRequest) ==========
normalize_section = '''### NormalizeFields — 表达式规范化

配置 `NormalizeFields` 后，Create/Update 请求中指定字段的 JSON 表达式在管线执行前自动规范化：

```go
HandlerConfig[entity.SysFormField]{
    NormalizeFields: []string{"display_formula", "filter_config"},
}
```

规范化规则（`expression/normalizer.go`）：
- `expression` 类型：统一为 `{"type":"expression","expression":{...}}` 结构
- 旧格式 `{"op":"Add","left":...}` 自动升级为新格式

---

'''

marker4 = '### MapRequest 默认行为'
pos4 = content.find(marker4)
if pos4 > 0:
    # Find the end of MapRequest section (next '##' or '---')
    next_major = content.find('\n## ', pos4 + 50)
    if next_major < 0:
        next_major = content.find('\n---\n', pos4 + 50)
    if next_major > 0:
        # Find the '---' before the next section
        dash = content.rfind('\n---\n', pos4, next_major + 10)
        if dash > pos4 and dash < next_major + 10:
            content = content[:dash] + normalize_section + content[dash:]
            print('4. NormalizeFields section INSERTED')
        else:
            print('4. Dash marker not found between', pos4, 'and', next_major)
    else:
        print('4. Next section not found after MapRequest')
else:
    print('4. MapRequest marker NOT FOUND')

with open(readme_path, 'w', encoding='utf-8') as f:
    f.write(content)

print('\nFinal README size:', len(content), 'bytes')
