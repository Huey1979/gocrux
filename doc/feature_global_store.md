# GlobalStore 内存缓存 — heims 需求

> 日期：2026-07-01 | 仅描述 heims 使用需求，不限制实现方式

## 需求概述

heims 的配置类实体（form、flow、dept、role 等）读频繁写少，希望指定一个内存中的全局变量作为中间垫，handler 的增删查改自动读写该变量，减少 MySQL 查询。

## 使用方式

配置 `HandlerConfig` 时传入一个 `GlobalStore`：

```go
var formCache = &sync.Map{}

type MapStore struct{ m *sync.Map }
func (s *MapStore) Get(key string) (any, bool) { return s.m.Load(key) }
func (s *MapStore) Set(key string, val any)    { s.m.Store(key, val) }
func (s *MapStore) Del(key string)             { s.m.Delete(key) }

HandlerConfig[*entity.SysForm]{
    GlobalStore: &MapStore{formCache},
    // ...
}
```

## 期望行为

| 操作 | 期望 |
|------|------|
| `GET ?code=staff_form` | 先 `Get("staff_form")` → 命中返回；未命中走 DB → `Set("staff_form", result)` |
| `GET ?id=01KW...` | 先 `Get("01KW...")` → 命中返回；未命中走 DB → `Set("form_code", result)` |
| `Create` | DB 写入成功后 `Set(form_code, result)` |
| `Update` | DB 更新成功后 `Set(form_code, result)`（旧值覆盖） |
| `Delete` | DB 删除成功后 `Del(form_code)` |

**Key 约定**：Get 用请求参数作为 key（code 或 id）；Create/Update/Delete 用实体的 `*_code` 或 `*_ulid` 字段作为 key。

**非阻塞**：Set/Del 失败不中断主流程。

## 不要求

- 不限实现方式（sync.Map / interface / 泛型）
- 不限 key 推导方式
- 不做过期淘汰（heims 侧自行管理实例生命周期）
- 不做 List 缓存（查询条件多变）
