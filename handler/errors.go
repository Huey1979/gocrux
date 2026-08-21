package handler

import (
	"errors"

	"github.com/Huey1979/gocrux/constants"
	errs "github.com/Huey1979/gocrux/errors"
)

// mapServiceError — Service 错误 → BusinessCode
func mapServiceError(err error) constants.BusinessCode {
	// 通用
	if errors.Is(err, errs.ErrRecordNotFound) {
		return constants.CodeNotFound
	}
	if errors.Is(err, errs.ErrUniqueValidationFailed) {
		return constants.CodeConflict
	}
	if errors.Is(err, errs.ErrInvalidParam) || errs.IsMissingParam(err) || errs.IsFieldValidation(err) {
		return constants.CodeParamError
	}
	if errors.Is(err, errs.ErrDuplicateCode) {
		return constants.CodeConflict
	}

	// 版本管理
	if errors.Is(err, errs.ErrVersionNotEnabled) {
		return constants.CodeBadRequest
	}
	if errors.Is(err, errs.ErrVersionFieldsNotSet) {
		return constants.CodeInternalError
	}
	if errors.Is(err, errs.ErrInvalidVersionStatusTransition) {
		return constants.CodeBadRequest
	}

	// 业务码错误（BUG-058）：钩子/业务校验返回 BizError 时透传自定义业务码。
	// 置于哨兵错误之后，保证现有哨兵映射优先级不回归。
	var bizErr *errs.BizError
	if errors.As(err, &bizErr) {
		return constants.BusinessCode(bizErr.Code)
	}

	return constants.CodeInternalError
}
