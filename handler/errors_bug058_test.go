package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Huey1979/gocrux/constants"
	errs "github.com/Huey1979/gocrux/errors"

	"github.com/gin-gonic/gin"
)

// bug058Record 满足 service.Record 约束的最小测试实体（handleError 测试用，无需数据库）。
type bug058Record struct {
	ID string
}

func (bug058Record) SetDefaults()               {}
func (bug058Record) SetCreatedAt(t time.Time)   {}
func (bug058Record) SetCreatedBy(userID string) {}
func (bug058Record) SetUpdatedAt(t time.Time)   {}
func (bug058Record) SetUpdatedBy(userID string) {}
func (bug058Record) SupportsDraft() bool        { return false }
func (bug058Record) SetDelete() bool            { return false }
func (bug058Record) PKField() string            { return "id" }
func (bug058Record) SelfFKField() string        { return "" }

// TestMapServiceErrorBizError BUG-058 核心：BizError 直接返回/嵌套包装均透传业务码。
func TestMapServiceErrorBizError(t *testing.T) {
	biz := errs.NewBizError(15002, "同目录下已存在同名文件或文件夹")
	if got := mapServiceError(biz); got != constants.BusinessCode(15002) {
		t.Errorf("mapServiceError(BizError) = %d, want 15002", got)
	}

	// 单层 %w 包装
	wrapped := fmt.Errorf("创建失败: %w", biz)
	if got := mapServiceError(wrapped); got != constants.BusinessCode(15002) {
		t.Errorf("mapServiceError(wrapped BizError) = %d, want 15002", got)
	}

	// 多层 %w 包装（errors.As 沿 Unwrap 链识别）
	doubled := fmt.Errorf("外层: %w", wrapped)
	if got := mapServiceError(doubled); got != constants.BusinessCode(15002) {
		t.Errorf("mapServiceError(double wrapped BizError) = %d, want 15002", got)
	}

	// 哨兵优先：BizError 不覆盖既有哨兵映射（嵌套哨兵时走哨兵码）
	if got := mapServiceError(fmt.Errorf("包装: %w", errs.ErrRecordNotFound)); got != constants.CodeNotFound {
		t.Errorf("mapServiceError(wrapped ErrRecordNotFound) = %d, want %d", got, constants.CodeNotFound)
	}
}

// TestMapServiceErrorSentinelRegression BUG-058 回归：既有哨兵错误映射不改变。
func TestMapServiceErrorSentinelRegression(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want constants.BusinessCode
	}{
		{"ErrRecordNotFound", errs.ErrRecordNotFound, constants.CodeNotFound},
		{"ErrUniqueValidationFailed", errs.ErrUniqueValidationFailed, constants.CodeConflict},
		{"ErrInvalidParam", errs.ErrInvalidParam, constants.CodeParamError},
		{"ErrMissingParam", errs.ErrMissingParam("name"), constants.CodeParamError},
		{"ErrFieldValidation", errs.ErrFieldValidation("a", "必填"), constants.CodeParamError},
		{"ErrDuplicateCode", errs.ErrDuplicateCode, constants.CodeConflict},
		{"ErrVersionNotEnabled", errs.ErrVersionNotEnabled, constants.CodeBadRequest},
		{"ErrVersionFieldsNotSet", errs.ErrVersionFieldsNotSet, constants.CodeInternalError},
		{"ErrInvalidVersionStatusTransition", errs.ErrInvalidVersionStatusTransition, constants.CodeBadRequest},
		{"普通错误", errors.New("未知错误"), constants.CodeInternalError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mapServiceError(c.err); got != c.want {
				t.Errorf("mapServiceError(%v) = %d, want %d", c.err, got, c.want)
			}
		})
	}
}

// TestHandleErrorBizErrorResponse BUG-058：handleError 对 BizError 返回业务码+消息，
// 且不落入 InternalErrorWithDetail（响应 msg 即 BizError.Msg）。
func TestHandleErrorBizErrorResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var h GenericHandler[bug058Record]
	err := errs.NewBizError(15002, "同目录下已存在同名文件或文件夹")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	h.handleError(c, err)

	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Code != 15002 {
		t.Errorf("响应 code = %d, want 15002", resp.Code)
	}
	if resp.Msg != "同目录下已存在同名文件或文件夹" {
		t.Errorf("响应 msg = %q, want 透传 BizError.Msg", resp.Msg)
	}
}

// TestHandleErrorWrappedBizErrorResponse 嵌套 %w 包装的 BizError 经 handleError 同样透传业务码。
func TestHandleErrorWrappedBizErrorResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var h GenericHandler[bug058Record]
	err := fmt.Errorf("校验失败: %w", errs.NewBizError(15005, "file 缺少 storage_ulid"))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	h.handleError(c, err)

	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Code != 15005 {
		t.Errorf("响应 code = %d, want 15005", resp.Code)
	}
	if resp.Msg != "file 缺少 storage_ulid" {
		t.Errorf("响应 msg = %q, want 透传 BizError.Msg", resp.Msg)
	}
}
