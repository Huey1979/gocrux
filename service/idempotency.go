package service

import (
	"sync"
	"time"
)

// ============================================================
// IdempotencyStore — 幂等结果缓存
//
// 以幂等键为索引缓存 Create 结果，防止重复提交。
// 内部维护一个带 TTL 的 map，过期自动清理。
//
// 使用方式：
//
//	store := service.NewIdempotencyStore(time.Hour)
//	svc.SetIdemStore(store)
//
// Service.Create 会自动检查：相同幂等键的请求直接返回缓存结果。
// ============================================================

type idemEntry[M Record] struct {
	result    []*M
	expiresAt time.Time
}

// IdempotencyStore 基于内存的幂等缓存。
// 注意：服务重启后缓存丢失；生产环境可替换为 Redis 等外部存储。
type IdempotencyStore[M Record] struct {
	mu   sync.RWMutex
	data map[string]*idemEntry[M]
	ttl  time.Duration
}

// NewIdempotencyStore 创建幂等缓存。
// ttl 为缓存有效期，到期后自动视为失效。
func NewIdempotencyStore[M Record](ttl time.Duration) *IdempotencyStore[M] {
	return &IdempotencyStore[M]{
		data: make(map[string]*idemEntry[M]),
		ttl:  ttl,
	}
}

// Get 按幂等键取缓存结果。不存在或已过期返回 nil, false。
func (s *IdempotencyStore[M]) Get(key string) ([]*M, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.data[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	// 深拷贝返回，防止调用方修改缓存
	result := make([]*M, len(entry.result))
	copy(result, entry.result)
	return result, true
}

// Set 缓存创建结果。
func (s *IdempotencyStore[M]) Set(key string, result []*M) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data[key] = &idemEntry[M]{
		result:    result,
		expiresAt: time.Now().Add(s.ttl),
	}
}

// cleanExpired 清理过期条目（内部调用，由 gc 触发）。
// 在 Get 命中过期时触发惰性清理。
func (s *IdempotencyStore[M]) cleanExpiredLocked() {
	now := time.Now()
	for k, v := range s.data {
		if now.After(v.expiresAt) {
			delete(s.data, k)
		}
	}
}
