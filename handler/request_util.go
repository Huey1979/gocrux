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

// GetPageParams 获取分页参数（统一解析多组别名，供响应回显）。
//
//	页码（从 1 开始）：page / pageNum / page_num
//	每页数量（默认 20）：page_size / pageSize
//	offset & size：起点从 0 开始，成对使用，优先于页码模式
func GetPageParams(c *gin.Context) (page, pageSize int) {
	page = 1
	pageSize = 20

	if p := firstQuery(c, "page", "pageNum", "page_num"); p != "" {
		if n, _ := common.ParseInt(p); n > 0 {
			page = n
		}
	}

	if ps := firstQuery(c, "page_size", "pageSize"); ps != "" {
		if n, _ := common.ParseInt(ps); n > 0 {
			pageSize = n
		}
	}

	// offset & size 模式优先：回显近似页码（offset/pageSize + 1）
	if o := c.Query("offset"); o != "" {
		if n, _ := common.ParseInt(o); n >= 0 {
			if s := c.Query("size"); s != "" {
				if n2, _ := common.ParseInt(s); n2 > 0 {
					pageSize = n2
				}
			}
			page = n/pageSize + 1
		}
	}

	return
}

// firstQuery 依次读取多个 query 参数，返回第一个非空值。
func firstQuery(c *gin.Context, keys ...string) string {
	for _, k := range keys {
		if v := c.Query(k); v != "" {
			return v
		}
	}
	return ""
}

// GetCurrentUserULID 获取当前用户 ULID（从上下文）
func GetCurrentUserULID(c *gin.Context) string {
	if v, exists := c.Get("user_ulid"); exists {
		return v.(string)
	}
	return ""
}
