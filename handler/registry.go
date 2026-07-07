package handler

import "github.com/Huey1979/gocrux/common"

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

// HandlerRegistry 基于泛型 Registry[CascadeHandler] 的具体类型。
type HandlerRegistry = common.Registry[CascadeHandler]

// NewHandlerRegistry 创建 Handler 注册表。
func NewHandlerRegistry() *HandlerRegistry {
	return common.NewRegistry[CascadeHandler]()
}
