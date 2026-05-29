package middleware

import (
	"bytes"
	"context"
	"time"

	"github.com/Huey1979/gocrux/internal/logger"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// Logger 请求日志中间件（保留向后兼容，等同 RequestLogger）
func Logger() gin.HandlerFunc {
	return RequestLogger()
}

// RequestLogger 请求日志中间件
// 1. 为每个请求生成唯一随机码（log_id）
// 2. 记录请求信息到 request 日志（URL、GET/POST数据）
// 3. 捕获并记录响应信息到 response 日志
func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 生成请求唯一 ID
		requestID := logger.GenerateRequestID()
		c.Set(logger.RequestIDKey, requestID)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), logger.RequestIDKey, requestID))
		// 在响应头中返回请求 ID，方便调试
		c.Header("X-Request-ID", requestID)

		// 记录请求
		logger.LogRequest(c, requestID)

		// 包装 ResponseWriter 以捕获响应体
		blw := &bodyLogWriter{body: &bytes.Buffer{}, ResponseWriter: c.Writer}
		c.Writer = blw

		start := time.Now()
		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()

		// 记录响应
		logger.LogResponse(c, requestID, status, blw.body.String())

		// 同时记录到控制台（主日志）
		logrus.WithFields(logrus.Fields{
			"log_id":     requestID,
			"method":     c.Request.Method,
			"path":       c.Request.URL.Path,
			"status":     status,
			"latency_ms": latency.Milliseconds(),
			"client_ip":  c.ClientIP(),
		}).Info("HTTP")
	}
}

// bodyLogWriter 包装 gin.ResponseWriter，拦截响应体写入
type bodyLogWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

func (w *bodyLogWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

// Recovery 异常恢复中间件
// 捕获 handler 中的 panic，返回统一 JSON 响应（含 request_id），避免暴露内部堆栈
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				requestID := ""
				if id, ok := c.Get(logger.RequestIDKey); ok {
					if s, ok := id.(string); ok {
						requestID = s
					}
				}

				// 日志记录完整 panic 信息
				logrus.WithFields(logrus.Fields{
					"log_id": requestID,
					"panic":  r,
					"path":   c.Request.URL.Path,
					"method": c.Request.Method,
				}).Error("PANIC recovered")

				// 返回用户友好的 JSON 错误（不暴露内部堆栈）
				c.JSON(200, gin.H{
					"code":       500,
					"msg":        "系统发生错误，请联系管理员。错误编号：" + requestID,
					"request_id": requestID,
				})

				c.Abort()
			}
		}()
		c.Next()
	}
}

// Cors 跨域中间件
func Cors() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}
