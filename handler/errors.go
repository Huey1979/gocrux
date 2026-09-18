package handler

import (
	"errors"

	"github.com/Huey1979/gocrux/constants"
	errs "github.com/Huey1979/gocrux/errors"
)

// mapServiceError — Service 错误 → BusinessCode
func mapServiceError(err error) constants.BusinessCode {
	// 引用/级联解析失败（BUG-080）：必须**先于** ErrRecordNotFound 判定。
	// 这类错误用 %w 保留原始错误链，引用目标不存在时链上就带 ErrRecordNotFound；
	// 若不先行拦截，「被引用的父记录缺失」会被升格成「本次请求的主记录不存在」，
	// 于是同一主键 get 返回 404 而 list 照常返回该行（自相矛盾）。
	// 统一映射 500：引用解析失败属服务端数据完整性/基础设施问题，
	// 响应消息里带有 handler 名（如「向上级联解析 notification_channel 失败」），
	// 调用方可据此区分「我没这条」与「我这条的引用坏了」。
	if errs.IsRefResolveError(err) {
		return constants.CodeInternalError
	}

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

	// 软删除（BUG-069）：不支持软删的实体没有恢复语义
	if errors.Is(err, errs.ErrSoftDeleteNotSupported) {
		return constants.CodeBadRequest
	}

	// 版本化实体误用 restore / 编辑已废弃版本（BUG-069 复核）
	if errors.Is(err, errs.ErrUseActivateInstead) || errors.Is(err, errs.ErrUpdateDeprecatedVersion) {
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
