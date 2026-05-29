package handler

import "sync"

// ============================================================
// HandlerRegistry — Handler 注册表
//
// 与 ServiceRegistry 对等，管理所有 Handler 实例的单例。
// 启动时注册，运行中并发安全取用。
//
// 使用示例：
//
//	handlerReg := NewHandlerRegistry()
//	handlerReg.Register("site",   siteHandler)
//	handlerReg.Register("domain", domainHandler)
//
// 父 Handler 在级联时通过名称拿到子 Handler：
//
//	child := handlerReg.Get("domain")
//	child.DoCreate(ctx, requests)
// ============================================================

type HandlerRegistry struct {
	mu       sync.RWMutex
	handlers map[string]CascadeHandler
}

// NewHandlerRegistry 创建 Handler 注册表。
func NewHandlerRegistry() *HandlerRegistry {
	return &HandlerRegistry{
		handlers: make(map[string]CascadeHandler),
	}
}

// Register 注册一个 Handler 实例。
// 同一 name 覆盖写（幂等），允许启动时多次注册。
func (r *HandlerRegistry) Register(name string, h CascadeHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[name] = h
}

// Get 按 name 获取 Handler 实例，未注册返回 nil。
func (r *HandlerRegistry) Get(name string) CascadeHandler {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.handlers[name]
}
