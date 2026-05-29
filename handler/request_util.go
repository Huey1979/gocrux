package handler

import (
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
		if n, _ := parseInt(p); n > 0 {
			page = n
		}
	}

	if ps := c.Query("page_size"); ps != "" {
		if n, _ := parseInt(ps); n > 0 {
			pageSize = n
		}
	}

	return
}

func parseInt(s string) (int, error) {
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, nil
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// GetCurrentUserULID 获取当前用户 ULID（从上下文）
func GetCurrentUserULID(c *gin.Context) string {
	if v, exists := c.Get("user_ulid"); exists {
		return v.(string)
	}
	return ""
}
