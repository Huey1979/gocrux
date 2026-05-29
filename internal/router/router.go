package router

import (
	"net/http"

	"github.com/Huey1979/gocrux/internal/middleware"

	"github.com/gin-gonic/gin"
)

// Setup 注册框架路由
func Setup(r *gin.Engine) {
	// 全局中间件
	r.Use(middleware.RequestLogger())
	r.Use(middleware.Recovery())
	r.Use(middleware.Cors())

	// 健康检查
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 根路由
	r.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, "gocrux framework")
	})
}
