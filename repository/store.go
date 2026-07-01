package repository

import (
	"context"
	"sync"
)

// GlobalStore 内存缓存接口。使用者提供实现（或使用内置 NewMapStore），
// 框架在 Get/Create/Update/Delete 管线中自动维护缓存。
//
// key 由框架生成（如 "ulid:01Jxxx"、"code:S001"），使用者无需关心 key 格式。
// 内置默认实现 NewMapStore() 基于 sync.Map，覆盖大部分场景。
type GlobalStore interface {
	// Get 按 key 查询缓存。命中返回 (entity, true)，未命中返回 (nil, false)。
	Get(ctx context.Context, key string) (any, bool)

	// Set 写入缓存。key 由框架生成，entity 为实体指针。
	Set(ctx context.Context, key string, entity any)

	// Del 删除缓存。key 由框架生成。
	Del(ctx context.Context, key string)
}

// MapStore 基于 sync.Map 的内置缓存实现，开箱即用。
type MapStore struct {
	m sync.Map
}

// NewMapStore 创建基于 sync.Map 的默认 GlobalStore 实现。
func NewMapStore() GlobalStore {
	return &MapStore{}
}

// Get 从 sync.Map 读取。
func (s *MapStore) Get(_ context.Context, key string) (any, bool) {
	v, ok := s.m.Load(key)
	if !ok {
		return nil, false
	}
	return v, true
}

// Set 写入 sync.Map。
func (s *MapStore) Set(_ context.Context, key string, entity any) {
	s.m.Store(key, entity)
}

// Del 从 sync.Map 删除。
func (s *MapStore) Del(_ context.Context, key string) {
	s.m.Delete(key)
}
