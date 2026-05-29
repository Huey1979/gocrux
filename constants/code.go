package constants

// BusinessCode 业务状态码
type BusinessCode int

// 通用成功/错误码 (1xxx)
const (
	CodeSuccess       BusinessCode = 200
	CodeBadRequest    BusinessCode = 400
	CodeUnauthorized  BusinessCode = 401
	CodeForbidden     BusinessCode = 403
	CodeNotFound      BusinessCode = 404
	CodeConflict      BusinessCode = 409
	CodeInternalError BusinessCode = 500
)

// 认证相关 (1000-1999)
const (
	CodeUserNotFound       BusinessCode = 1001
	CodeInvalidCredential  BusinessCode = 1002
	CodeUserDisabled       BusinessCode = 1003
	CodeUserAlreadyExists  BusinessCode = 1004
	CodePhoneAlreadyExists BusinessCode = 1005
	CodeEmailAlreadyExists BusinessCode = 1006
	CodeLoginCodeExpired   BusinessCode = 1007
	CodeTokenInvalid       BusinessCode = 1008
	CodeTokenExpired       BusinessCode = 1009
	CodeInvalidToken       BusinessCode = 1010
	CodeLoginFailed        BusinessCode = 1002
	CodeInvalidCode        BusinessCode = 1012
	CodeTenantNotAllowed   BusinessCode = 1011
)

// 多租户相关 (2000-2999)
const (
	CodeTenantNotFound       BusinessCode = 2001
	CodeTenantCodeExists     BusinessCode = 2002
	CodeUserNotInTenant      BusinessCode = 2003
	CodeUserAlreadyInTenant  BusinessCode = 2004
	CodeTenantDisabled       BusinessCode = 2005
	CodeTenantPending        BusinessCode = 2006
	CodeUserPendingReview    BusinessCode = 2007
	CodeUserRejected         BusinessCode = 2008
	CodeUserDisabledInTenant BusinessCode = 2009
	CodeNotTenantMember      BusinessCode = 2003
)

// 权限相关 (3000-3999)
const (
	CodePermissionDenied     BusinessCode = 3001
	CodeRoleNotFound         BusinessCode = 3002
	CodeRoleCodeExists       BusinessCode = 3003
	CodePermissionNotFound   BusinessCode = 3004
	CodePermissionCodeExists BusinessCode = 3005
)

// 参数验证相关 (4000-4999)
const (
	CodeMissingParam BusinessCode = 4001
	CodeInvalidParam BusinessCode = 4002
	CodeParamError   BusinessCode = 4001
)

// BusinessCodeMsg 状态码对应的默认消息
var BusinessCodeMsg = map[BusinessCode]string{
	CodeSuccess:       "操作成功",
	CodeBadRequest:    "请求参数错误",
	CodeUnauthorized:  "未登录或认证失败",
	CodeForbidden:     "权限不足，禁止访问",
	CodeNotFound:      "资源不存在",
	CodeConflict:      "资源冲突",
	CodeInternalError: "服务器内部错误",

	CodeUserNotFound:       "用户不存在",
	CodeInvalidCredential:  "用户名或密码错误",
	CodeUserDisabled:       "用户已被禁用",
	CodeUserAlreadyExists:  "用户名已存在",
	CodePhoneAlreadyExists: "手机号已被注册",
	CodeEmailAlreadyExists: "邮箱已被注册",
	CodeLoginCodeExpired:   "登录码无效或已过期",
	CodeTokenInvalid:       "令牌无效",
	CodeTokenExpired:       "令牌已过期",
	CodeInvalidToken:       "无效的令牌",
	CodeInvalidCode:        "验证码无效或已过期",
	CodeTenantNotAllowed:   "不能切换企业",

	CodeTenantNotFound:       "企业不存在",
	CodeTenantCodeExists:     "企业编码已存在",
	CodeUserNotInTenant:      "用户不属于该企业",
	CodeUserAlreadyInTenant:  "用户已加入该企业",
	CodeTenantDisabled:       "企业已被冻结",
	CodeTenantPending:        "企业还在审核中",
	CodeUserPendingReview:    "用户申请正在审核中",
	CodeUserRejected:         "用户申请已被驳回",
	CodeUserDisabledInTenant: "用户已被禁用，无法访问该企业",

	CodePermissionDenied:     "权限不足",
	CodeRoleNotFound:         "角色不存在",
	CodeRoleCodeExists:       "角色编码已存在",
	CodePermissionNotFound:   "权限不存在",
	CodePermissionCodeExists: "权限编码已存在",

	CodeMissingParam: "缺少必需参数",
	CodeInvalidParam: "参数无效",
}

// GetMsg 获取状态码对应的消息
func (c BusinessCode) GetMsg() string {
	if msg, ok := BusinessCodeMsg[c]; ok {
		return msg
	}
	return "未知错误"
}
