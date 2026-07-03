package handler

import (
	"context"
	"encoding/json"

	errs "github.com/Huey1979/gocrux/errors"
	"github.com/Huey1979/gocrux/service"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// PKField CascadeHandler 接口实现，返回实体 M 的主键数据库列名。
func (h *GenericHandler[M]) PKField() string {
	var zero M
	return zero.PKField()
}

// Create 创建记录
// POST /{prefix}/create
func (h *GenericHandler[M]) Create(c *gin.Context) {
	if !h.checkPerm(c, "create") {
		return
	}
	ctx := c.Request.Context()

	var rawReqs []map[string]any
	bodyBytes, _ := c.GetRawData()
	if err := json.Unmarshal(bodyBytes, &rawReqs); err != nil {
		// 兼容单对象：用户传 {k:v} 而非 [{k:v}] 时自动包裹成数组
		var single map[string]any
		if uerr := json.Unmarshal(bodyBytes, &single); uerr == nil {
			rawReqs = []map[string]any{single}
		} else {
			h.handleError(c, err)
			return
		}
	}
	if len(rawReqs) == 0 {
		h.handleError(c, errs.ErrInvalidParam)
		return
	}

	result, err := h.createPipeline(ctx, rawReqs)
	if err != nil {
		h.handleError(c, err)
		return
	}
	Success(c, gin.H{"items": result})
}

// createPipeline 统一管线：HTTP 和级联共享。
func (h *GenericHandler[M]) createPipeline(ctx context.Context, rawReqs []map[string]any) (_ []*M, err error) {
	start := traceStart(ctx, h.svcName+".create", logrus.Fields{"count": len(rawReqs)})
	defer func() { traceEnd(ctx, h.svcName+".create", start, err) }()

	// 框架层校验：类型/长度/必填等（自动推导 + 用户配置）
	if h.config.BatchErrorMode == "collect" && len(rawReqs) > 1 {
		var collected *BatchErrors
		for i, raw := range rawReqs {
			if berrs := validateInputCollect(h.validateRules.Create, raw, "create", i); berrs != nil {
				if collected == nil {
					collected = &BatchErrors{}
				}
				collected.Errors = append(collected.Errors, berrs.Errors...)
			}
		}
		if collected != nil {
			return nil, collected
		}
	} else {
		for i, raw := range rawReqs {
			if err := validateInput(h.validateRules.Create, raw, "create"); err != nil {
				return nil, errs.ErrReqValidation(i, err)
			}
		}
	}

	reqs := make([]service.CrudRequest[M], len(rawReqs))
	for i, raw := range rawReqs {
		reqs[i] = h.newCrudRequest(raw)
	}

	// 业务字段校验（具体 Request 的 Validate()，MapRequest 为 no-op）
	if h.config.BatchErrorMode == "collect" && len(rawReqs) > 1 {
		var collected *BatchErrors
		for i, r := range reqs {
			if err := r.Validate(); err != nil {
				if collected == nil {
					collected = &BatchErrors{}
				}
				collected.Errors = append(collected.Errors, BatchError{
					Index: i, Message: err.Error(),
				})
			}
		}
		if collected != nil {
			return nil, collected
		}
	} else {
		for i, r := range reqs {
			if err := r.Validate(); err != nil {
				return nil, errs.ErrReqValidation(i, err)
			}
		}
	}

	// 将原始 map 注入 ctx，供 _doCreate 级联创建时提取子数据
	ctx = context.WithValue(ctx, rawCreateMapsKey{}, rawReqs)

	processed, err := h.beforeCreate(ctx, reqs)
	if err != nil {
		return nil, err
	}

	created, err := h.doCreate(ctx, processed)
	if err != nil {
		return nil, err
	}

	return h.afterCreate(ctx, created)
}

func (h *GenericHandler[M]) beforeCreate(ctx context.Context, input []service.CrudRequest[M]) ([]service.CrudRequest[M], error) {
	if h.hooks.BeforeCreate != nil {
		return h.hooks.BeforeCreate(ctx, input)
	}
	return h._beforeCreate(ctx, input)
}

func (h *GenericHandler[M]) doCreate(ctx context.Context, input []service.CrudRequest[M]) ([]*M, error) {
	if h.hooks.DoCreate != nil {
		return h.hooks.DoCreate(ctx, input)
	}
	return h._doCreate(ctx, input)
}

func (h *GenericHandler[M]) afterCreate(ctx context.Context, result []*M) ([]*M, error) {
	if h.hooks.AfterCreate != nil {
		return h.hooks.AfterCreate(ctx, result)
	}
	return h._afterCreate(ctx, result)
}

// ============================================================
// 8. Update — 管线（HTTP → cascade → before → do → after）
// ============================================================

// rawUpdateMapsKey 用于在 updatePipeline 中将原始请求 map 切片透传给 _doUpdate，
// 以便级联更新时从父请求中提取子表数据。
type rawUpdateMapsKey struct{}

// DoUpdate CascadeHandler 接口实现。
// 注入 FK 后统一走 updatePipeline，享受完整的 before→do→after 管线。
func (h *GenericHandler[M]) DoUpdate(ctx context.Context, fkField string, fkValue any, childrenData []map[string]any, parentVersioned bool) error {
	// 注入 FK 到每条子数据，同时将实体 PK 映射到 "id"（MapRequest.GetID 优先查 "id"）
	pkField := h.PKField()
	for i := range childrenData {
		childrenData[i][fkField] = fkValue
		if pkVal, ok := childrenData[i][pkField]; ok && pkVal != nil {
			childrenData[i]["id"] = pkVal
		}
	}
	_, err := h.updatePipeline(ctx, childrenData, parentVersioned)
	return err
}

// Update 编辑记录
// POST /{prefix}/update
func (h *GenericHandler[M]) Update(c *gin.Context) {
	if !h.checkPerm(c, "update") {
		return
	}
	ctx := c.Request.Context()

	var raw map[string]any
	if err := c.ShouldBindJSON(&raw); err != nil {
		h.handleError(c, err)
		return
	}

	rid, ok := raw["id"]
	if !ok || rid == nil {
		h.handleError(c, errs.ErrInvalidParam)
		return
	}
	_ = rid

	results, err := h.updatePipeline(ctx, []map[string]any{raw}, false)
	if err != nil {
		h.handleError(c, err)
		return
	}
	Success(c, gin.H{"items": results})
}

// updatePipeline 统一管线（HTTP 入口 + 级联入口共享）。
// rawReqs 为待处理的原始请求 map 列表；parentVersioned 表示父链是否已出现版本化节点。
func (h *GenericHandler[M]) updatePipeline(ctx context.Context, rawReqs []map[string]any, parentVersioned bool) (_ []*M, err error) {
	start := traceStart(ctx, h.svcName+".update", logrus.Fields{"count": len(rawReqs)})
	defer func() { traceEnd(ctx, h.svcName+".update", start, err) }()
	// 框架层校验：类型/长度等（自动推导 + 用户配置）
	for i, raw := range rawReqs {
		if err := validateInput(h.validateRules.Update, raw, "update"); err != nil {
			return nil, errs.ErrUpdateReqValidation(i, err)
		}
	}

	reqs := make([]service.CrudRequest[M], len(rawReqs))
	for i, raw := range rawReqs {
		reqs[i] = h.newCrudRequestForUpdate(raw)
	}

	for i, r := range reqs {
		if err := r.Validate(); err != nil {
			return nil, errs.ErrUpdateReqValidation(i, err)
		}
	}

	// 若配置了级联更新，将原始 maps 注入 ctx，供 _doUpdate 提取子数据
	if h.hasCascadeFlag(func(r CascadeRelation) bool { return r.OnUpdate }) {
		ctx = context.WithValue(ctx, rawUpdateMapsKey{}, rawReqs)
	}

	processed, err := h.beforeUpdate(ctx, reqs, parentVersioned)
	if err != nil {
		return nil, err
	}

	results, err := h.doUpdate(ctx, processed, parentVersioned)
	if err != nil {
		return nil, err
	}

	return h.afterUpdate(ctx, results, parentVersioned)
}

func (h *GenericHandler[M]) beforeUpdate(ctx context.Context, reqs []service.CrudRequest[M], parentVersioned bool) ([]service.CrudRequest[M], error) {
	if h.hooks.BeforeUpdate != nil {
		return h.hooks.BeforeUpdate(ctx, reqs, parentVersioned)
	}
	return h._beforeUpdate(ctx, reqs, parentVersioned)
}

func (h *GenericHandler[M]) doUpdate(ctx context.Context, reqs []service.CrudRequest[M], parentVersioned bool) ([]*M, error) {
	if h.hooks.DoUpdate != nil {
		return h.hooks.DoUpdate(ctx, reqs, parentVersioned)
	}
	return h._doUpdate(ctx, reqs, parentVersioned)
}

func (h *GenericHandler[M]) afterUpdate(ctx context.Context, results []*M, parentVersioned bool) ([]*M, error) {
	if h.hooks.AfterUpdate != nil {
		return h.hooks.AfterUpdate(ctx, results, parentVersioned)
	}
	return h._afterUpdate(ctx, results, parentVersioned)
}

// ============================================================
// 9. Delete — 管线（HTTP → cascade → before → do → after）
// ============================================================

// DoDelete CascadeHandler 接口实现。
func (h *GenericHandler[M]) DoDelete(ctx context.Context, ids []any) error {
	return h.deletePipeline(ctx, ids, nil)
}

// DoDeleteByFK CascadeHandler 接口实现。
// 按外键字段批量软删除子记录（用于级联删除）。
// fkField 为数据库列名（与 JSON 字段名一致），fkValues 为父记录主键列表。
func (h *GenericHandler[M]) DoDeleteByFK(ctx context.Context, fkField string, fkValues []any) error {
	if len(fkValues) == 0 {
		return nil
	}
	return h.svc.DeleteByFK(ctx, fkField, fkValues)
}

// Delete 删除记录（逻辑删除）
// POST /{prefix}/delete
func (h *GenericHandler[M]) Delete(c *gin.Context) {
	if !h.checkPerm(c, "delete") {
		return
	}
	ctx := c.Request.Context()

	var raw struct {
		IDs   []any `json:"ids"`
		Codes []any `json:"codes"`
	}
	if err := c.ShouldBindJSON(&raw); err != nil {
		h.handleError(c, err)
		return
	}
	if len(raw.IDs) == 0 {
		h.handleError(c, errs.ErrInvalidParam)
		return
	}

	if err := h.deletePipeline(ctx, raw.IDs, raw.Codes); err != nil {
		h.handleError(c, err)
		return
	}
	SuccessWithMessage(c, "删除成功", nil)
}

// deletePipeline 统一管线。
func (h *GenericHandler[M]) deletePipeline(ctx context.Context, ids, codes any) (err error) {
	start := traceStart(ctx, h.svcName+".delete", logrus.Fields{"ids": ids})
	defer func() { traceEnd(ctx, h.svcName+".delete", start, err) }()
	pid, pdata, err := h.beforeDelete(ctx, ids, codes)
	if err != nil {
		return err
	}

	if err := h.doDelete(ctx, pid, pdata); err != nil {
		return err
	}

	// 将 ids 注入 ctx，供 _afterDelete 的 GlobalStore 缓存清理使用
	ctx = context.WithValue(ctx, deleteCacheIDsKey{}, ids)
	return h.afterDelete(ctx)
}

func (h *GenericHandler[M]) beforeDelete(ctx context.Context, ids, codes any) (any, any, error) {
	if h.hooks.BeforeDelete != nil {
		return h.hooks.BeforeDelete(ctx, ids, codes)
	}
	return h._beforeDelete(ctx, ids, codes)
}

func (h *GenericHandler[M]) doDelete(ctx context.Context, ids, codes any) error {
	if h.hooks.DoDelete != nil {
		return h.hooks.DoDelete(ctx, ids, codes)
	}
	return h._doDelete(ctx, ids, codes)
}

func (h *GenericHandler[M]) afterDelete(ctx context.Context) error {
	if h.hooks.AfterDelete != nil {
		return h.hooks.AfterDelete(ctx)
	}
	return h._afterDelete(ctx)
}
