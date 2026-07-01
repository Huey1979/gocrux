# GlobalStore 内存缓存 — heims 需求

> 日期：2026-07-01 | 仅描述接口约定和数据流，不限制实现方式
> ⚠️ 以下代码为设计示意，gocrux 开发者需补足空值判定、并发安全等工程细节

## 接口定义

```go
// GlobalStore 全局内存缓存接口。heims 侧提供实现，gocrux handler 管线自动调用。
type GlobalStore[M any] interface {
    GetUlidByCode() *map[string]string   // code → ulid 索引
    GetEntityMap()   *map[string]*M       // ulid → 实体
    SaveCode(M)                           // 写入 code → ulid 索引
    DelCode(M)                            // 删除 code → ulid 索引
}
```

## 公共帮助函数（gocrux 侧实现，示意代码）

```go
// GlobalStoreGet 优先用 ulid 查找，无 ulid 时从 code 索引转换后查找。
func GlobalStoreGet[M any](store GlobalStore[M], ulid string, code string) (*M, error) {
    if ulid == "" {
        if code == "" { return nil, fmt.Errorf("ulid 和 code 不能同时为空") }
        ulid = (*store.GetUlidByCode())[code]
        if ulid == "" { return nil, nil } // 缓存未命中
    }
    return (*store.GetEntityMap())[ulid], nil
}

// GlobalStoreSet 写入实体并更新 code 索引。
func GlobalStoreSet[M any](store GlobalStore[M], ulid string, entity *M) {
    (*store.GetEntityMap())[ulid] = entity
    store.SaveCode(*entity)
}

// GlobalStoreDel 删除实体和 code 索引。
func GlobalStoreDel[M any](store GlobalStore[M], ulid string, code string) {
    if ulid == "" {
        if code == "" { return }
        ulid = (*store.GetUlidByCode())[code]
    }
    if entity := (*store.GetEntityMap())[ulid]; entity != nil {
        store.DelCode(*entity)
    }
    delete((*store.GetEntityMap()), ulid)
}
```

## 管线行为

| 操作 | 调用 |
|------|------|
| `_doGet(id, code)` | `GlobalStoreGet(store, id, code)` → 命中返回；未命中走 DB → `GlobalStoreSet(store, pk, result)` |
| `_afterCreate` | `GlobalStoreSet(store, pk, result)` |
| `_afterUpdate` | `GlobalStoreSet(store, pk, result)` |
| `_afterDelete` | `GlobalStoreDel(store, id, code)` |

## heims 侧实现示例（同样为示意代码）

```go
type FormSettings struct {
    data      map[string]*entity.SysForm
    codeIndex map[string]string
}

func (s *FormSettings) GetUlidByCode() *map[string]string { return &s.codeIndex }
func (s *FormSettings) GetEntityMap() *map[string]*entity.SysForm { return &s.data }
func (s *FormSettings) SaveCode(f entity.SysForm) { s.codeIndex[f.FormCode] = f.FormULID }
func (s *FormSettings) DelCode(f entity.SysForm)  { delete(s.codeIndex, f.FormCode) }

// 配置：
HandlerConfig[*entity.SysForm]{
    GlobalStore: &FormSettings{data: make(...), codeIndex: make(...)},
}
```

## 不要求

- 不限实现方式（泛型/interface/struct）
- 不限并发模型（sync.Map / mutex）
- 不做过期淘汰（heims 侧在 AfterDelete 等时机主动 Del）
- 不做 List 缓存
