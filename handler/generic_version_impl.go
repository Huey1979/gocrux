package handler

import (
	"context"
	errs "github.com/Huey1979/gocrux/errors"
)

// ============================================================
// 内置 _before / _do / _after 默认实现
//
// _beforeXxx：纯数据管线中的前置处理（校验、转换等，不依赖 gin）
// _doXxx：    调用 Service（后续扩展级联逻辑）
// _afterXxx： 纯数据管线中的后置处理（结果转换等，不依赖 gin）
//
// gin 相关的 I/O 已全部上提至 HTTP 薄壳方法。
// 与 Service 层的 generic_impl.go 完全对等。
// ============================================================

// -------- Activate --------

func (h *GenericHandler[M]) _beforeActivate(_ context.Context, id any) (any, error) {
	// 默认：透传
	return id, nil
}

func (h *GenericHandler[M]) _doActivate(ctx context.Context, id any) error {
	// 1. Self: activate（仅版本化模式有意义，非版本化直接跳过）
	if h.svc.IsVersionMode() {
		if err := h.svc.Activate(ctx, id); err != nil {
			return err
		}
	}

	// 2. Cascade: 级联激活子记录
	//    父激活 → 查子记录（DoList）→ 逐个调用子 Handler 的 DoActivate
	//    非版本化 Handler 自身空操作，但级联继续向下穿透
	if h.hasCascadeFlag(func(r CascadeRelation) bool { return r.OnActivate }) && h.txCoord != nil && h.handlerReg != nil {
		return h.txCoord.Run(ctx, func(txCtx context.Context) error {
			if err := h.forEachCascade(
				func(r CascadeRelation) bool { return r.OnActivate },
				func(rel CascadeRelation, ch CascadeHandler) error {
					return errs.ErrCascadeActivate(rel.HandlerName,
						forEachCascadeChild(txCtx, ch, rel.FKField, id,
							func(txCtx context.Context, child map[string]any, childPK any) error {
								return ch.DoActivate(txCtx, childPK)
							}),
					)
				},
			); err != nil {
				return err
			}
			return nil
		})
	}
	return nil
}

func (h *GenericHandler[M]) _afterActivate(_ context.Context) error {
	// 默认：空操作
	return nil
}

// -------- ListVersions --------

func (h *GenericHandler[M]) _beforeListVersions(_ context.Context, id any, code string) (any, string, error) {
	// 默认：透传
	return id, code, nil
}

func (h *GenericHandler[M]) _doListVersions(ctx context.Context, id any, code string) ([]M, error) {
	return h.svc.ListVersions(ctx, id, code)
}

func (h *GenericHandler[M]) _afterListVersions(_ context.Context, result []M) ([]M, error) {
	// 默认：透传
	return result, nil
}

// -------- EditVersion --------

func (h *GenericHandler[M]) _beforeEditVersion(_ context.Context, id any, patches map[string]any) (any, map[string]any, error) {
	// 默认：透传
	return id, patches, nil
}

func (h *GenericHandler[M]) _doEditVersion(ctx context.Context, id any, patches map[string]any) (*M, error) {
	// 1. Self: edit version（仅版本化模式有意义）
	var result *M
	var err error
	if h.svc.IsVersionMode() {
		result, err = h.svc.EditVersion(ctx, id, patches)
		if err != nil {
			return nil, err
		}
	}

	// 2. Cascade: 级联编辑子记录的版本元数据
	if h.hasCascadeFlag(func(r CascadeRelation) bool { return r.OnEditVersion }) && h.txCoord != nil && h.handlerReg != nil {
		err = h.txCoord.Run(ctx, func(txCtx context.Context) error {
			if err := h.forEachCascade(
				func(r CascadeRelation) bool { return r.OnEditVersion },
				func(rel CascadeRelation, ch CascadeHandler) error {
					return errs.ErrCascadeEditVer(rel.HandlerName,
						forEachCascadeChild(txCtx, ch, rel.FKField, id,
							func(txCtx context.Context, child map[string]any, childPK any) error {
								_, err := ch.DoEditVersion(txCtx, childPK, patches)
								return err
							}),
					)
				},
			); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	return result, nil
}

func (h *GenericHandler[M]) _afterEditVersion(_ context.Context, result *M) (*M, error) {
	// 默认：透传
	return result, nil
}
