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
	if errors.Is(err, errs.ErrInvalidParam) || errs.IsFieldValidation(err) {
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

	return constants.CodeInternalError
}
