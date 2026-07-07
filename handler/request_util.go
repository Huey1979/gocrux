package handler

import (
	"github.com/Huey1979/gocrux/common"
	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
)

var validate = validator.New()

// BindJSON 绑定并验证 JSON 请求
func BindJSON(c *gin.Context, obj interface{}) error {
	if err := c.ShouldBindJSON(obj); err != nil {
		return err
	}
	return validate.Struct(obj)
}

// BindQuery 绑定 Query 参数
func BindQuery(c *gin.Context, obj interface{}) error {
	if err := c.ShouldBindQuery(obj); err != nil {
		return err
	}
	return validate.Struct(obj)
}

// GetPageParams 获取分页参数
func GetPageParams(c *gin.Context) (page, pageSize int) {
	page = 1
	pageSize = 20

	if p := c.Query("page"); p != "" {
		if n, _ := common.ParseInt(p); n > 0 {
			page = n
		}
	}

	if ps := c.Query("page_size"); ps != "" {
		if n, _ := common.ParseInt(ps); n > 0 {
			pageSize = n
		}
	}

	return
}

// GetCurrentUserULID 获取当前用户 ULID（从上下文）
func GetCurrentUserULID(c *gin.Context) string {
	if v, exists := c.Get("user_ulid"); exists {
		return v.(string)
	}
	return ""
}
