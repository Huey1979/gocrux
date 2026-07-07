package handler

import (
	"fmt"
	"runtime"

	"github.com/Huey1979/gocrux/constants"
	"github.com/Huey1979/gocrux/internal/logger"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// Response 统一响应结构
type Response struct {
	Code      int         `json:"code"`
	Msg       string      `json:"msg"`
	RequestID string      `json:"request_id,omitempty"`
	Data      interface{} `json:"data,omitempty"`
}

// getRequestID 从 gin.Context 提取请求 ID
func getRequestID(c *gin.Context) string {
	if id, ok := c.Get(logger.RequestIDKey); ok {
		if s, ok := id.(string); ok {
			return s
		}
	}
	return ""
}

// Success 成功响应
func Success(c *gin.Context, data interface{}) {
	c.JSON(200, Response{
		Code:      int(constants.CodeSuccess),
		Msg:       constants.CodeSuccess.GetMsg(),
		RequestID: getRequestID(c),
		Data:      data,
	})
}

// SuccessWithMessage 成功响应（带自定义消息）
func SuccessWithMessage(c *gin.Context, msg string, data interface{}) {
	c.JSON(200, Response{
		Code:      int(constants.CodeSuccess),
		Msg:       msg,
		RequestID: getRequestID(c),
		Data:      data,
	})
}

// Error 错误响应（使用预定义业务码的默认消息）
func Error(c *gin.Context, code constants.BusinessCode) {
	c.JSON(200, Response{
		Code:      int(code),
		Msg:       code.GetMsg(),
		RequestID: getRequestID(c),
	})
}

// ErrorWithMsg 错误响应（用于业务层错误，消息对用户友好）
// 注意不要在 msg 中直接传 err.Error()，应将原始 error 记录到日志后返回中文友好消息。
// 如需同时记录原始错误，请使用 InternalError。
func ErrorWithMsg(c *gin.Context, code constants.BusinessCode, msg string) {
	c.JSON(200, Response{
		Code:      int(code),
		Msg:       msg,
		RequestID: getRequestID(c),
	})
}

// ErrorWithCode 错误响应（使用 int 错误码）
func ErrorWithCode(c *gin.Context, code int, msg string) {
	c.JSON(200, Response{
		Code:      code,
		Msg:       msg,
		RequestID: getRequestID(c),
	})
}

// InternalError 内部错误响应
// 将原始 err 记录到日志（含 request_id、调用位置），
// 返回对用户友好的通用错误消息"系统发生错误，请联系管理员。错误编号：xxx"。
//
// 使用场景：数据库错误、第三方服务错误、未预期的技术异常等不应暴露内部细节的错误。
func InternalError(c *gin.Context, err error) {
	requestID := getRequestID(c)

	// 记录原始错误到日志（含调用位置，方便技术人员排查）
	_, file, line, _ := runtime.Caller(1)
	logrus.WithFields(logrus.Fields{
		"log_id": requestID,
		"error":  err.Error(),
		"caller": fmt.Sprintf("%s:%d", file, line),
	}).Error("内部错误")

	// 记录到业务日志
	logger.LogBusiness(c, "internal_error", logrus.Fields{
		"error":  err.Error(),
		"caller": fmt.Sprintf("%s:%d", file, line),
	})

	// 返回通用错误消息
	c.JSON(200, Response{
		Code:      int(constants.CodeInternalError),
		Msg:       fmt.Sprintf("系统发生错误，请联系管理员。错误编号：%s", requestID),
		RequestID: requestID,
	})
}

// InternalErrorWithDetail 错误响应（带错误详情透传）。
// 将原始 err 记录到日志，同时将 err.Error() 透传返回给前端，
// 避免在 mapServiceError 未覆盖时吞掉具体错误原因。
//
// 与 InternalError 的区别：Message 使用 err.Error() 而非通用提示。
func InternalErrorWithDetail(c *gin.Context, code constants.BusinessCode, err error) {
	requestID := getRequestID(c)

	_, file, line, _ := runtime.Caller(1)
	logrus.WithFields(logrus.Fields{
		"log_id": requestID,
		"error":  err.Error(),
		"caller": fmt.Sprintf("%s:%d", file, line),
	}).Error("内部错误")

	logger.LogBusiness(c, "internal_error", logrus.Fields{
		"error":  err.Error(),
		"caller": fmt.Sprintf("%s:%d", file, line),
	})

	c.JSON(200, Response{
		Code:      int(code),
		Msg:       err.Error(),
		RequestID: requestID,
	})
}
