package middleware

import (
	"github.com/Huey1979/gocrux/handler"

	"github.com/gin-gonic/gin"
)

// ============================================================
// AuthMiddleware — 基于 Authenticator 钩子的认证中间件
//
// 使用者需要实现 handler.Authenticator 接口并将其注入。
// 如果未设置认证器（authenticator == nil），则注入一个默认匿名用户，
// 方便在开发阶段或不需要认证的场景下使用。
// ============================================================

// DefaultAuthenticator 全局限认认证器（外部可替换）
var DefaultAuthenticator handler.Authenticator

// AuthMiddleware 返回一个 gin 中间件，该中间件调用 DefaultAuthenticator 的
// Middleware() 方法完成认证，并在 context 中注入 UserInfo。
//
// 如果 DefaultAuthenticator 为 nil，则自动注入匿名用户信息。
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if DefaultAuthenticator != nil {
			DefaultAuthenticator.Middleware()(c)
			return
		}

		// 默认行为：注入匿名用户，允许框架正常运行
		c.Set("user", handler.UserInfo{
			ULID:       "anonymous",
			Name:       "匿名用户",
			TenantULID: "",
		})
		c.Next()
	}
}
