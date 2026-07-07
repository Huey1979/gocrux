package service

import "github.com/Huey1979/gocrux/common"

// ============================================================
// ServiceRegistry — 全局服务注册表
// 每个实体类型全局仅一个 Service 实例，启动时注册，运行中安全并发取用。
// ============================================================

// ServiceRegistry 基于泛型 Registry[any] 的具体类型。
type ServiceRegistry = common.Registry[any]

// NewServiceRegistry 创建注册表。
func NewServiceRegistry() *ServiceRegistry {
	return common.NewRegistry[any]()
}

// GetTyped 按 name 获取指定泛型类型的服务实例。
// 不存在或类型不匹配时返回 nil。
func GetTyped[M Record](r *ServiceRegistry, name string) *GenericService[M] {
	svc, _ := r.Get(name).(*GenericService[M])
	return svc
}
