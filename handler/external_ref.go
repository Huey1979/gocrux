package handler

import (
	"context"
	"errors"
	"fmt"
	"strings"

	errs "github.com/Huey1979/gocrux/errors"
)

// ============================================================
// 外部引用解析（应用方 §19.4 / §20.5）
//
// 与 v3 装配（Assemblies）的分工 —— 应用方 §20.5 的判断是对的：
//
//	装配（Assemblies）：目标在同一请求树里「本次新建」，ULID 由预分配产出，
//	                    引用在落库前一次性写对；
//	外部引用（本文件）：目标**已经存在**（data_select 选中的表单/流程等），
//	                    不做装配，而是「解析权威版本 → 应用侧校验 → 按策略持久化」。
//
// 为什么提供**函数式 API** 而不是编译期声明（如 HandlerConfig.ExternalRefs）：
//
//	heims 的 data_select 模式（store_snapshot）来自**运行期表单配置**
//	（sys_form_write_field.options），同一实体在不同表单里的策略不同，
//	编译期声明无法表达。因此框架只提供「解析」这一步（需要 DB 与版本语义，
//	应用自己重写必然与框架的 published/current 约定分叉），
//	由应用在自己的写入管线里按表单配置调用。
//
// 框架不做的部分（属业务语义，应用侧负责）：
//   - 权限/归属校验（当前用户能否引用/读取该目标）；
//   - 业务状态合法性（如「目标必须已审核」）；
//   - **持久化形态**（PersistMode：只存 code / 只存 ULID / 存 {ulid, code}）
//     与数组/标量包装 —— 那由应用按表单配置决定，框架把权威身份原样交回。
// ============================================================

// ExternalRefMode 外部引用的解析模式（对应应用方 §19.4 的 ResolveMode）。
type ExternalRefMode string

const (
	// ExternalRefModeCurrent 按 code 取**当前工作版本**（is_current=1）。
	//
	// 注意：编辑中的草稿同样满足该条件 —— 仅在业务语义是「跟随我的编辑」时使用。
	ExternalRefModeCurrent ExternalRefMode = "current"

	// ExternalRefModePublished 按 code 取**线上生效版本**（version_status='published'）。
	//
	// 应用方 §20.3 的默认语义：store_snapshot=false 时只持久化 code，
	// 由 code 解析当前 published 版本；引用要固定到线上版本时应使用本模式。
	ExternalRefModePublished ExternalRefMode = "published"

	// ExternalRefModeSnapshot 按 ULID 取**固定版本**（快照语义）。
	//
	// 目标必须存在且未软删；若同时提供了 code，会校验二者一致
	// （防止跨 code 族 / 版本错配的引用悄悄落库）。
	ExternalRefModeSnapshot ExternalRefMode = "snapshot"
)

// ExternalRefLookup 一次外部引用解析的输入（应用侧的「选择意图」）。
//
// 三种典型形态（应用方 §19.4 要求框架不要再把它们混成一种隐式行为）：
//
//	{Code: "F001"}                       → 版本化目标：解析 published / current
//	{ULID: "01H..."}                     → 固定版本（快照）
//	{Code: "F001", ULID: "01H..."}       → 按 code 解析权威版本并**校验一致**
//	                                       （不一致 → ErrExternalRefStale）
type ExternalRefLookup struct {
	// HandlerName 目标 Handler 在 HandlerRegistry 中的注册名（必填）。
	HandlerName string
	// Code 选择锚点（业务编码）。published / current 模式必填。
	Code string
	// ULID 指定的目标版本。snapshot 模式必填；
	// 与 Code 同时提供时用于**陈旧检测**（权威 ULID 不一致 → ErrExternalRefStale）。
	ULID string
	// Mode 解析模式。留空时按 defaultExternalRefMode 推导（§23.3 对照表）：
	//
	//	仅 Code     → 版本化 published / 非版本化 current
	//	仅 ULID     → snapshot
	//	Code + ULID → published + 陈旧检测
	//
	// 「固定版本同时带 Code」必须显式传 snapshot —— 否则按上面的推导走
	// published（那才是 Code+ULID 的默认语义）。
	Mode ExternalRefMode
}

// ExternalRefResolved 解析结果：权威身份 + 记录快照。
type ExternalRefResolved struct {
	// ULID 权威版本的主键值（version ≠ 调用方提交值时为「权威值」）。
	ULID string
	// Code 权威记录的业务编码（非版本化实体未配置 CodeField 时可能为空）。
	Code string
	// VersionStatus 版本状态（如 published / draft / deprecated）；非版本化为空。
	VersionStatus string
	// VersionCode 版本号（如 v1.0）；非版本化为空。
	VersionCode string
	// Record 权威记录的 JSON 形态（应用可据此做权限/状态校验与快照拷贝）。
	Record map[string]any
}

// externalRefTarget 目标 Handler 的外部引用解析能力（非泛型分发用）。
//
// 单列接口而不是扩充 CascadeHandler：外部引用解析是可选能力，
// 让所有 Handler 实现者都被迫实现三个方法并不合理。
type externalRefTarget interface {
	// RefIsVersioned 目标是否启用版本化（决定默认解析模式）。
	RefIsVersioned() bool
	// RefByCode 按业务 code 解析；publishedOnly=true 时只看线上生效版本。
	RefByCode(ctx context.Context, code string, publishedOnly bool) (*ExternalRefResolved, error)
	// RefByPK 按主键解析固定版本。
	RefByPK(ctx context.Context, id any) (*ExternalRefResolved, error)
}

// ResolveExternalRef 解析一个外部引用，返回权威身份。
//
// 错误分级（全部可被 errors.Is 精确识别，便于应用映射自己的业务码）：
//
//	ErrExternalRefInvalidParam  参数不合法（缺 HandlerName / code 与 ulid 都没给 / 模式未知）
//	ErrExternalRefTargetMissing 目标 Handler 未注册，或未实现外部引用解析
//	ErrExternalRefNotFound      目标不存在，或已软删（软删记录不可作为引用目标）
//	ErrExternalRefStale         提交的 ULID 与权威版本不一致（版本已变化，需重新选择）
//
// reg 为 nil（未注入注册表）时同样报 ErrExternalRefTargetMissing ——
// 「配了却没有能力执行」必须显式失败，否则引用会以未经验证的形态落库。
func ResolveExternalRef(ctx context.Context, reg *HandlerRegistry, in ExternalRefLookup) (*ExternalRefResolved, error) {
	name := strings.TrimSpace(in.HandlerName)
	if name == "" {
		return nil, fmt.Errorf("%w: 缺少 HandlerName", errs.ErrExternalRefInvalidParam)
	}
	if in.Code == "" && in.ULID == "" {
		return nil, fmt.Errorf("%w: code 与 ulid 至少提供一个（target=%s）",
			errs.ErrExternalRefInvalidParam, name)
	}
	if reg == nil {
		return nil, fmt.Errorf("%w: HandlerRegistry 未注入（target=%s）",
			errs.ErrExternalRefTargetMissing, name)
	}
	raw := reg.Get(name)
	if raw == nil {
		return nil, fmt.Errorf("%w: %s", errs.ErrExternalRefTargetMissing, name)
	}
	target, ok := raw.(externalRefTarget)
	if !ok {
		return nil, fmt.Errorf("%w: Handler %s 未实现外部引用解析（请使用 GenericHandler）",
			errs.ErrExternalRefTargetMissing, name)
	}

	mode := in.Mode
	if mode == "" {
		mode = defaultExternalRefMode(target.RefIsVersioned(), in)
	}

	switch mode {
	case ExternalRefModeSnapshot:
		if in.ULID == "" {
			return nil, fmt.Errorf("%w: snapshot 模式必须提供 ulid（target=%s）",
				errs.ErrExternalRefInvalidParam, name)
		}
		got, err := target.RefByPK(ctx, in.ULID)
		if err != nil {
			return nil, err
		}
		// 族校验：同时给了 code 时，固定版本的 code 必须一致 ——
		// 否则前端把「A 的 ULID + B 的 code」拼起来就能绕过业务校验。
		if in.Code != "" && got.Code != "" && got.Code != in.Code {
			return nil, fmt.Errorf("%w: ulid=%s 属于 code=%s，与提交的 code=%s 不一致（target=%s）",
				errs.ErrExternalRefInvalidParam, in.ULID, got.Code, in.Code, name)
		}
		return got, nil

	case ExternalRefModePublished, ExternalRefModeCurrent:
		if in.Code == "" {
			return nil, fmt.Errorf("%w: %s 模式必须提供 code（target=%s）",
				errs.ErrExternalRefInvalidParam, mode, name)
		}
		publishedOnly := mode == ExternalRefModePublished
		got, err := target.RefByCode(ctx, in.Code, publishedOnly)
		if err != nil {
			return nil, err
		}
		// 陈旧检测（应用方 §20.3 第 3 条）：调用方同时提交了 ULID（快照意图）
		// 时，它必须等于按 code 解析出的权威 ULID。不一致 → 引用的是旧版本，
		// 必须让调用方重新选择，**不得**静默改成新版本（那会悄悄改变业务语义）。
		if in.ULID != "" && got.ULID != "" && got.ULID != in.ULID {
			return nil, fmt.Errorf("%w: code=%s 的%s为 %s，提交的 ULID=%s 已过期（target=%s）",
				errs.ErrExternalRefStale, in.Code, modeLabel(mode), got.ULID, in.ULID, name)
		}
		return got, nil

	default:
		return nil, fmt.Errorf("%w: 未知 Mode=%q（支持 current/published/snapshot）",
			errs.ErrExternalRefInvalidParam, in.Mode)
	}
}

// defaultExternalRefMode 推导默认解析模式（应用方 §23.3 确认的对照表）。
//
//	仅 Code            → 版本化 published / 非版本化 current
//	仅 ULID            → snapshot（固定版本）
//	Code + ULID        → published + **陈旧检测**（校验 ULID 与权威版本一致）
//	需要固定版本**同时**带 Code → 必须显式传 Mode=snapshot
//
// 为什么「Code + ULID」不能推导成 snapshot（§23.3 指出的早期缺陷）：
// 同时带 code 与 ULID 的表达力远超「按 ULID 取一条」—— 它是「我选择的是
// code 的**这个版本**」，语义上必须回到 code 去确认权威版本是否已变；
// 若按 ULID 直取，前端拿旧 ULID + 新 code 拼接就能悄悄固定到一个过期版本，
// 与 §20.3 第 3 条「版本已变化请重新选择」直接冲突。
//
// 非版本化目标同样适用：published 会退化为「按 code 等值查询」，
// 随后的一致性校验等价于「ULID 与 code 必须指向同一条记录」。
func defaultExternalRefMode(versioned bool, in ExternalRefLookup) ExternalRefMode {
	switch {
	case in.Code != "" && in.ULID != "":
		return ExternalRefModePublished
	case in.ULID != "":
		return ExternalRefModeSnapshot
	case versioned:
		return ExternalRefModePublished
	default:
		return ExternalRefModeCurrent
	}
}

// modeLabel 错误文案用的模式中文说明。
func modeLabel(mode ExternalRefMode) string {
	if mode == ExternalRefModePublished {
		return "线上生效版本"
	}
	return "当前版本"
}

// ResolveExternalRef 便捷入口：使用本 Handler 的注册表解析外部引用。
//
// 返回的错误与包级 ResolveExternalRef 一致。
func (h *GenericHandler[M]) ResolveExternalRef(ctx context.Context, in ExternalRefLookup) (*ExternalRefResolved, error) {
	return ResolveExternalRef(ctx, h.handlerReg, in)
}

// ============================================================
// GenericHandler 的解析能力实现（externalRefTarget）
// ============================================================

// RefIsVersioned 实现 externalRefTarget。
func (h *GenericHandler[M]) RefIsVersioned() bool { return h.svc.IsVersionMode() }

// RefByCode 实现 externalRefTarget：按业务 code 解析权威版本。
func (h *GenericHandler[M]) RefByCode(ctx context.Context, code string, publishedOnly bool) (*ExternalRefResolved, error) {
	var rec *M
	var err error
	if publishedOnly {
		rec, err = h.svc.GetPublishedByCode(ctx, code)
	} else {
		rec, err = h.svc.GetByCode(ctx, code)
	}
	if err != nil {
		return nil, h.wrapExternalRefNotFound(err, fmt.Sprintf("code=%s", code))
	}
	return h.externalRefOf(rec, fmt.Sprintf("code=%s", code))
}

// RefByPK 实现 externalRefTarget：按主键解析固定版本。
func (h *GenericHandler[M]) RefByPK(ctx context.Context, id any) (*ExternalRefResolved, error) {
	rec, err := h.svc.Get(ctx, id)
	if err != nil {
		return nil, h.wrapExternalRefNotFound(err, fmt.Sprintf("ulid=%v", id))
	}
	return h.externalRefOf(rec, fmt.Sprintf("ulid=%v", id))
}

// externalRefOf 把权威实体转成解析结果；不可作为引用目标时返回 NotFound。
func (h *GenericHandler[M]) externalRefOf(rec *M, what string) (*ExternalRefResolved, error) {
	if rec == nil {
		return nil, fmt.Errorf("%w（%s entity=%s）", errs.ErrExternalRefNotFound, what, h.svcName)
	}
	// 已软删记录不可作为引用目标（读路径本身不过滤软删 —— BUG-069，
	// 故此处必须显式判定，否则「引用一个已删除的配置」会被静默接受）。
	if _, _, ok := h.svc.DeletedColumn(); ok && h.svc.IsSoftDeleted(rec) {
		return nil, fmt.Errorf("%w（%s entity=%s 已删除）", errs.ErrExternalRefNotFound, what, h.svcName)
	}
	data, err := marshalToMap(rec)
	if err != nil {
		return nil, err
	}
	ulid, code, status, versionCode := h.svc.RefIdentity(rec)
	if ulid == "" {
		// 兜底：RefIdentity 取不到时退回框架统一的主键提取（handle Go 字段名差异）。
		if v := extractPKFromResult(rec); v != nil {
			ulid = scalarToString(v)
		}
	}
	if code == "" {
		// 兜底：非版本化实体未配置 CodeField 时按约定字段名取（JSON 形态）。
		if s, ok := data["code"].(string); ok {
			code = s
		}
	}
	return &ExternalRefResolved{
		ULID:          ulid,
		Code:          code,
		VersionStatus: status,
		VersionCode:   versionCode,
		Record:        data,
	}, nil
}

// wrapExternalRefNotFound 把「记录不存在」归一为外部引用语义的 NotFound。
//
// 归一而非直传的意义：应用按 errors.Is(err, errs.ErrExternalRefNotFound)
// 判定「引用目标不存在」，不需要知道底层是 MySQL 的 gorm.ErrRecordNotFound
// 还是 Mongo 的 ErrNoDocuments（BUG-062 的教训）。原错误链保留（双 %w），
// errors.Is(err, errs.ErrRecordNotFound) 仍成立。
func (h *GenericHandler[M]) wrapExternalRefNotFound(err error, what string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errs.ErrRecordNotFound) {
		return fmt.Errorf("%w（%s entity=%s): %w", errs.ErrExternalRefNotFound, what, h.svcName, err)
	}
	return err
}
