# GlobalStore 内存缓存功能需求

> 日期：2026-07-01 | 需求方：heims | 优先级：P0

## 背景

heims 的配置类实体（form、flow、dept、role、personnel、view、container 等）读取频繁但写入少，每次走 MySQL 造成不必要的延迟。希望将已发布的配置数据缓存在内存中，Get/List 优先命中缓存。

## 需求

在 `HandlerConfig` 中增加两个可选配置项：

```go
type HandlerConfig[M service.Record] struct {
    // ... 现有字段 ...

    // GlobalStore 全局内存缓存。nil=不使用。
    GlobalStore GlobalStore

    // GlobalKey 从实体提取缓存键。未配置时推导 PKField 的值。
    GlobalKey func(M) string
}

// GlobalStore 全局内存缓存接口（heims 侧提供实现，如 sync.Map 包装）。
type GlobalStore interface {
    Get(key string) (val any, ok bool)
    Set(key string, val any)
    Del(key string)
}
```

## 管线行为

| 操作 | GlobalStore 行为 |
|------|-----------------|
| `_doGet(id)` | 先 `Get(id)` → 命中直接返回；未命中走 DB → `Set(code, result)` |
| `_doGet(code)` | 同 id |
| `_beforeList` | 不缓存（List 条件多变，缓存命中率低） |
| `_afterCreate` | `Set(code, result)` |
| `_afterUpdate` | `Set(code, result)`（新版本覆盖旧 key） |
| `_afterDelete` | `Del(code)` |

**Code 优先**：GlobalKey 有值时用 GlobalKey 提取的 key；无 GlobalKey 时用 PKField 的值作为 key。

**非阻塞**：Set/Del 失败不中断主流程，仅打 warn 日志。

## 示例

```go
// heims 侧：
var formCache = &sync.Map{}

type MapStore struct{ m *sync.Map }
func (s *MapStore) Get(key string) (any, bool) { return s.m.Load(key) }
func (s *MapStore) Set(key string, val any)    { s.m.Store(key, val) }
func (s *MapStore) Del(key string)              { s.m.Delete(key) }

// 配置：
HandlerConfig[*entity.SysForm]{
    GlobalStore: &MapStore{formCache},
    GlobalKey: func(f *entity.SysForm) string { return f.FormCode },
    // ...
}
```

## 实施位置

`handler/generic_read_impl.go`：
- `_doGet`：在 `svc.GetByID` 前查 GlobalStore

`handler/generic_write_impl.go`：
- `_afterCreate` / `_afterUpdate` / `_afterDelete`：操作 GlobalStore

## 边界

- GlobalStore 非 nil 但 GlobalKey 为 nil：用 PKField 值做 key
- 同一个 key 多次 Set 覆盖旧值（版本更新场景）
- 不做过期淘汰（heims 侧自行决定用 sync.Map 或带 TTL 的实现）
