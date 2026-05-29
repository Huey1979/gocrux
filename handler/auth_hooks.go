package handler

import (
	"github.com/gin-gonic/gin"
)

// ============================================================
// UserInfo — 认证后的用户信息
//
// 由 Authenticator 在中间件中填充到 gin.Context，
// Handler 通过 FromContext 获取后用于日志、权限校验等。
// ============================================================

// UserInfo 认证后的用户信息。
type UserInfo struct {
	ULID       string         `json:"ulid"`        // 用户 ULID
	Name       string         `json:"name"`        // 用户名
	TenantULID string         `json:"tenant_ulid"` // 租户 ULID（多租户场景）
	Extra      map[string]any `json:"extra"`       // 扩展字段（角色列表等）
}

// ============================================================
// Authenticator — 认证钩子接口
//
// 使用者实现此接口，注入到 HandlerConfig.Auth 中。
// Handler 不关心具体认证方式（JWT / Session / OAuth2…），
// 只通过 FromContext 获取已认证的用户信息。
//
// 使用示例（JWT 实现）：
//
//	type JWTAuth struct{}
//	func (a *JWTAuth) Middleware() gin.HandlerFunc {
//	    return func(c *gin.Context) {
//	        token := c.GetHeader("Authorization")
//	        claims := parseJWT(token)  // 使用者自定义
//	        c.Set("user", UserInfo{ULID: claims.Sub, Name: claims.Name})
//	        c.Next()
//	    }
//	}
//	func (a *JWTAuth) FromContext(c *gin.Context) (UserInfo, bool) {
//	    v, ok := c.Get("user")
//	    if !ok { return UserInfo{}, false }
//	    return v.(UserInfo), true
//	}
// ============================================================

// Authenticator 认证钩子接口。
type Authenticator interface {
	// Middleware 返回 gin 中间件，完成认证并在 context 中注入 UserInfo。
	// 典型实现：解析 Header/Query/Cookie 中的 token，查库验证，
	// 通过 gin.Context.Set(key, UserInfo{…}) 注入用户信息。
	Middleware() gin.HandlerFunc

	// FromContext 从 gin.Context 中提取已认证的 UserInfo。
	// 返回 (UserInfo, true) 表示已认证；(UserInfo{}, false) 表示未认证。
	// Handler 在需要用户信息的场景（日志、权限检查等）调用此方法。
	FromContext(c *gin.Context) (UserInfo, bool)
}

// ============================================================
// Authorizer — 权限校验钩子接口
//
// 使用者实现此接口，注入到 HandlerConfig.Perm 中。
// Handler 在每个操作（Create/Update/Delete/Get/List…）执行前调用
// Check(info, resource, action) 进行权限验证。
//
// 使用示例（RBAC 实现）：
//
//	type RBACAuthorizer struct{}
//	func (a *RBACAuthorizer) Check(info UserInfo, resource, action string) bool {
//	    roles := info.Extra["roles"].([]string)
//	    return hasPermission(roles, resource, action)
//	}
// ============================================================

// Authorizer 权限校验钩子接口。
type Authorizer interface {
	// Check 检查用户是否有权限对指定资源执行指定操作。
	// resource: 资源标识（如 "site", "role", "menu"）。
	// action: 操作类型（如 "create", "update", "delete", "get", "list"）。
	// 返回 true 表示允许，false 表示拒绝。
	Check(info UserInfo, resource string, action string) bool
}
