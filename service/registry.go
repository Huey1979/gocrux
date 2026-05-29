package service

import "sync"

// ============================================================
// ServiceRegistry — 全局服务注册表
// 每个实体类型全局仅一个 Service 实例，启动时注册，运行中安全并发取用。
// ============================================================

type ServiceRegistry struct {
	mu       sync.RWMutex
	services map[string]any // value 为 *GenericService[某个 M]
}

// NewServiceRegistry 创建注册表
func NewServiceRegistry() *ServiceRegistry {
	return &ServiceRegistry{
		services: make(map[string]any),
	}
}

// Register 注册一个服务实例，同一 name 覆盖写（幂等）。
func (r *ServiceRegistry) Register(name string, svc any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.services[name] = svc
}

// Get 按 name 获取服务实例，未注册返回 nil。
// 调用方需自行做类型断言，如 registry.Get("content").(*GenericService[Content])
func (r *ServiceRegistry) Get(name string) any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.services[name]
}

// GetTyped 按 name 获取指定泛型类型的服务实例。
// 不存在或类型不匹配时返回 nil。
func GetTyped[M Record](r *ServiceRegistry, name string) *GenericService[M] {
	r.mu.RLock()
	defer r.mu.RUnlock()
	svc, _ := r.services[name].(*GenericService[M])
	return svc
}
