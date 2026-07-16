package handler

import (
	"context"
	"encoding/json"

	errs "github.com/Huey1979/gocrux/errors"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// ============================================================
// 12. Activate — 管线（HTTP → cascade → before → do → after）
// ============================================================

// DoActivate CascadeHandler 接口实现。
// 激活 / 发布版本并级联到子 Handler。
// - 版本化自身：调用 svc.Activate，然后级联
// - 非版本化自身：空操作（svc 无 Activate 语义），仅级联
func (h *GenericHandler[M]) DoActivate(ctx context.Context, id any) error {
	return h.activatePipeline(ctx, id)
}

// Activate 激活版本（发布 / 回滚统一入口）
// POST /{prefix}/activate
func (h *GenericHandler[M]) Activate(c *gin.Context) {
	if !h.checkPerm(c, "activate") {
		return
	}
	ctx := c.Request.Context()

	var raw struct {
		ID any `json:"id"`
	}
	if err := c.ShouldBindJSON(&raw); err != nil {
		h.handleError(c, err)
		return
	}
	if raw.ID == nil {
		h.handleError(c, errs.ErrMissingParam("id"))
		return
	}

	if err := h.activatePipeline(ctx, raw.ID); err != nil {
		h.handleError(c, err)
		return
	}
	SuccessWithMessage(c, "操作成功", nil)
}

// activatePipeline 统一管线。
func (h *GenericHandler[M]) activatePipeline(ctx context.Context, id any) (err error) {
	start := traceStart(ctx, h.svcName+".activate", logrus.Fields{"id": id})
	defer func() { traceEnd(ctx, h.svcName+".activate", start, err) }()
	pid, err := h.beforeActivate(ctx, id)
	if err != nil {
		return err
	}

	if err := h.doActivate(ctx, pid); err != nil {
		return err
	}

	return h.afterActivate(ctx)
}

func (h *GenericHandler[M]) beforeActivate(ctx context.Context, id any) (any, error) {
	if h.hooks.BeforeActivate != nil {
		return h.hooks.BeforeActivate(ctx, id)
	}
	return h._beforeActivate(ctx, id)
}

func (h *GenericHandler[M]) doActivate(ctx context.Context, id any) error {
	if h.hooks.DoActivate != nil {
		return h.hooks.DoActivate(ctx, id)
	}
	return h._doActivate(ctx, id)
}

func (h *GenericHandler[M]) afterActivate(ctx context.Context) error {
	if h.hooks.AfterActivate != nil {
		return h.hooks.AfterActivate(ctx)
	}
	return h._afterActivate(ctx)
}

// ============================================================
// 13. ListVersions — 管线（HTTP → cascade → before → do → after）
// ============================================================

// DoListVersions CascadeHandler 接口实现。
// 版本化：调用 svc.ListVersions，marshal 为 map 列表返回。
// 非版本化：返回空列表。
func (h *GenericHandler[M]) DoListVersions(ctx context.Context, id any, code string) ([]map[string]any, error) {
	records, err := h.listVersionsPipeline(ctx, id, code)
	if err != nil {
		return nil, err
	}

	result := make([]map[string]any, len(records))
	for i, r := range records {
		data, err := json.Marshal(r)
		if err != nil {
			return nil, errs.ErrMarshalVersion(err)
		}
		if err := json.Unmarshal(data, &result[i]); err != nil {
			return nil, errs.ErrUnmarshalVersion(err)
		}
	}
	return result, nil
}

// ListVersions 获取版本列表
// GET /{prefix}/versions?code=xxx 或 ?id=xxx
func (h *GenericHandler[M]) ListVersions(c *gin.Context) {
	if !h.checkPerm(c, "versions") {
		return
	}
	ctx := c.Request.Context()

	rid := c.Query("id")
	rcode := c.Query("code")
	if rid == "" && rcode == "" {
		h.handleError(c, errs.ErrMissingParam("id 或 code"))
		return
	}

	versions, err := h.listVersionsPipeline(ctx, rid, rcode)
	if err != nil {
		h.handleError(c, err)
		return
	}
	Success(c, gin.H{"versions": versions, "total": len(versions)})
}

// listVersionsPipeline 统一管线。
func (h *GenericHandler[M]) listVersionsPipeline(ctx context.Context, id any, code string) ([]M, error) {
	pid, pcode, err := h.beforeListVersions(ctx, id, code)
	if err != nil {
		return nil, err
	}

	results, err := h.doListVersions(ctx, pid, pcode)
	if err != nil {
		return nil, err
	}

	return h.afterListVersions(ctx, results)
}

func (h *GenericHandler[M]) beforeListVersions(ctx context.Context, id any, code string) (any, string, error) {
	if h.hooks.BeforeListVersions != nil {
		return h.hooks.BeforeListVersions(ctx, id, code)
	}
	return h._beforeListVersions(ctx, id, code)
}

func (h *GenericHandler[M]) doListVersions(ctx context.Context, id any, code string) ([]M, error) {
	if h.hooks.DoListVersions != nil {
		return h.hooks.DoListVersions(ctx, id, code)
	}
	return h._doListVersions(ctx, id, code)
}

func (h *GenericHandler[M]) afterListVersions(ctx context.Context, result []M) ([]M, error) {
	if h.hooks.AfterListVersions != nil {
		return h.hooks.AfterListVersions(ctx, result)
	}
	return h._afterListVersions(ctx, result)
}

// ============================================================
// 14. EditVersion — 管线（HTTP → cascade → before → do → after）
// ============================================================

// DoEditVersion CascadeHandler 接口实现。
// 修改版本元数据并级联到子 Handler。
// - 版本化自身：调用 svc.EditVersion，然后级联
// - 非版本化自身：空操作（svc 无 EditVersion 语义），仅级联
func (h *GenericHandler[M]) DoEditVersion(ctx context.Context, id any, patches map[string]any) (map[string]any, error) {
	result, err := h.editVersionPipeline(ctx, id, patches)
	if err != nil {
		return nil, err
	}

	// 非版本化 Handler 返回 nil（自身空操作），级联已由 _doEditVersion 完成
	if result == nil {
		return map[string]any{}, nil
	}

	data, err := json.Marshal(result)
	if err != nil {
		return nil, errs.ErrMarshalEditVersion(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, errs.ErrUnmarshalEditVersion(err)
	}
	return m, nil
}

// EditVersion 修改版本元数据（状态、备注）
// POST /{prefix}/edit-version
func (h *GenericHandler[M]) EditVersion(c *gin.Context) {
	if !h.checkPerm(c, "edit-version") {
		return
	}
	ctx := c.Request.Context()

	var raw struct {
		ID      any            `json:"id"`
		Patches map[string]any `json:"patches"`
	}
	if err := c.ShouldBindJSON(&raw); err != nil {
		h.handleError(c, err)
		return
	}
	if raw.ID == nil {
		h.handleError(c, errs.ErrMissingParam("id"))
		return
	}
	if len(raw.Patches) == 0 {
		h.handleError(c, errs.ErrMissingParam("patches"))
		return
	}

	result, err := h.editVersionPipeline(ctx, raw.ID, raw.Patches)
	if err != nil {
		h.handleError(c, err)
		return
	}
	Success(c, result)
}

// editVersionPipeline 统一管线。
func (h *GenericHandler[M]) editVersionPipeline(ctx context.Context, id any, patches map[string]any) (_ *M, err error) {
	start := traceStart(ctx, h.svcName+".edit_version", logrus.Fields{"id": id})
	defer func() { traceEnd(ctx, h.svcName+".edit_version", start, err) }()
	pid, ppatches, err := h.beforeEditVersion(ctx, id, patches)
	if err != nil {
		return nil, err
	}

	result, err := h.doEditVersion(ctx, pid, ppatches)
	if err != nil {
		return nil, err
	}

	return h.afterEditVersion(ctx, result)
}

func (h *GenericHandler[M]) beforeEditVersion(ctx context.Context, id any, patches map[string]any) (any, map[string]any, error) {
	if h.hooks.BeforeEditVersion != nil {
		return h.hooks.BeforeEditVersion(ctx, id, patches)
	}
	return h._beforeEditVersion(ctx, id, patches)
}

func (h *GenericHandler[M]) doEditVersion(ctx context.Context, id any, patches map[string]any) (*M, error) {
	if h.hooks.DoEditVersion != nil {
		return h.hooks.DoEditVersion(ctx, id, patches)
	}
	return h._doEditVersion(ctx, id, patches)
}

func (h *GenericHandler[M]) afterEditVersion(ctx context.Context, result *M) (*M, error) {
	if h.hooks.AfterEditVersion != nil {
		return h.hooks.AfterEditVersion(ctx, result)
	}
	return h._afterEditVersion(ctx, result)
}

// ListArchivedVersions 获取已归档版本列表
// GET /{prefix}/versions-archived?code=xxx
func (h *GenericHandler[M]) ListArchivedVersions(c *gin.Context) {
	if !h.checkPerm(c, "versions") {
		return
	}
	ctx := c.Request.Context()
	rcode := c.Query("code")
	if rcode == "" {
		h.handleError(c, errs.ErrMissingParam("code"))
		return
	}

	records, err := h.svc.ListVersions(ctx, nil, rcode)
	if err != nil {
		h.handleError(c, err)
		return
	}

	// 仅保留 abolished 状态的版本
	var archived []map[string]any
	for _, r := range records {
		data, _ := json.Marshal(r)
		var m map[string]any
		json.Unmarshal(data, &m)
		if status, _ := m["version_status"].(string); status == "abolished" {
			archived = append(archived, m)
		}
	}
	if archived == nil {
		archived = []map[string]any{}
	}
	Success(c, gin.H{"versions": archived, "total": len(archived)})
}
