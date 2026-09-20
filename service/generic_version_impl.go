package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Huey1979/gocrux/common"
	errs "github.com/Huey1979/gocrux/errors"
	"github.com/Huey1979/gocrux/repository"

	"github.com/sirupsen/logrus"
)

// -------- Activate --------
func (s *GenericService[M]) _beforeActivate(ctx context.Context, id any) (any, error) {
	if !s.config.VersionMode {
		return nil, errs.ErrVersionNotEnabled
	}
	vf := s.config.VersionFields
	if vf == nil {
		return nil, errs.ErrVersionFieldsNotSet
	}

	// 取实体
	_entity, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if norm := normalizeNotFound(err); norm != err {
			return nil, norm
		}
		return nil, errs.ErrQueryRecordFailed(err)
	}

	// 已彻底废弃的版本不允许激活
	if vf.StatusField != "" {
		if getStrField(_entity, vf.StatusField) == string(VersionStatusAbolished) {
			return nil, errs.ErrInvalidVersionStatusTransition
		}
	}

	return _entity, nil
}

func (s *GenericService[M]) _doActivate(ctx context.Context, id any) error {
	_entity, ok := id.(*M)
	if !ok {
		return errs.ErrDoUpdateTypeMismatch
	}
	vf := s.config.VersionFields

	code := getStrField(_entity, vf.CodeField)
	entityULID := getStrField(_entity, vf.ULIDField)
	currentStatus := getStrField(_entity, vf.StatusField)

	codeCol := resolveColumn[M](vf.CodeField)

	now := time.Now()
	userULID := GetUserULID(ctx)

	// 1. 查出同 code 所有版本，区分目标与非目标
	allVersions, _, err := s.repo.ListByFilters(ctx, repository.ListFilters{
		Filters:  []repository.Filter{{Field: codeCol, Op: repository.OpEQ, Value: code}},
		Page:     1,
		PageSize: 0,
	})
	if err != nil {
		return err
	}

	var otherULIDs []any
	for i := range allVersions {
		vid := getStrField(&allVersions[i], vf.ULIDField)
		if vid != "" && vid != entityULID {
			otherULIDs = append(otherULIDs, vid)
		}
	}

	// 2. 非目标版本退位（仅废弃其他版本，不影响目标）
	if len(otherULIDs) > 0 {
		if err := s.repo.BatchDeprecateVersions(ctx, otherULIDs); err != nil {
			return err
		}
	}

	// 3. 目标行：设置 is_current + 状态更新（在 entity 上直接改，然后用 Save 持久化）
	common.SetFieldValue(_entity, vf.CurrentField, int8(1))
	common.SetFieldValue(_entity, "UpdatedAt", now)
	if userULID != "" {
		common.SetFieldValue(_entity, "UpdatedBy", userULID)
	}

	// 草稿 / 已废弃 → 正式发布
	//
	// 发布痕迹（REQ: publish trace）：基础列复用 applyPublishTraceFields，
	// 统一处理「两个字段都没配置」的配置错误；随后 recordPublished 写发布历史
	// 并触发 OnPublished 回调（via=activate）。
	// 本路径不经 MySQL TxCoordinator 事务（Save 直连），故直接写、无回滚顾虑。
	publishing := s.statusBecomesPublished(currentStatus, string(VersionStatusPublished))
	if currentStatus == string(VersionStatusDraft) || currentStatus == string(VersionStatusDeprecated) {
		common.SetFieldValue(_entity, vf.StatusField, string(VersionStatusPublished))
		if err := s.applyPublishTraceFields(_entity, now, userULID); err != nil {
			s.warnPublishTraceUnavailable(err)
		}
	}

	// 4. 用 Save 持久化（兼容 MySQL GORM 与 MongoDB）
	if err := s.repo.Save(ctx, _entity); err != nil {
		return err
	}

	if publishing {
		s.recordPublished(ctx, _entity, PublishViaActivate)
	}
	return nil
}

func (s *GenericService[M]) _afterActivate(ctx context.Context, id any) error {
	_entity, ok := id.(*M)
	if !ok {
		return nil
	}
	if s.opLogReady() {
		rec := s.makeOpLogRecord(ctx, extractEntityID(_entity), "activate")
		rec.RecordAfter = snapshotJSON(_entity)
		s.writeOpLogRecords(ctx, []OpLogRecord{rec})
	}

	// 备份日志文件（门控只看 bakWriter，与 opLog 解耦 —— BUG-077 §7/§8.2）
	if s.bakWriter != nil {
		if err := s.bakWriter(ctx, s.config.EntityName, extractEntityID(_entity), "activate", _entity, GetRequestID(ctx)); err != nil {
			logrus.Errorf("gocrux: 写备份日志失败（entity_type=%s operation=activate id=%v）: %v",
				s.config.EntityName, extractEntityID(_entity), err)
		}
	}
	return nil
}

// -------- ListVersions --------
func (s *GenericService[M]) _beforeListVersions(ctx context.Context, id any, code string) (any, error) {
	if !s.config.VersionMode {
		return nil, errs.ErrVersionNotEnabled
	}
	vf := s.config.VersionFields
	if vf == nil {
		return nil, errs.ErrVersionFieldsNotSet
	}

	if code == "" {
		// 尝试按 ULID 查实体，提取 code
		_entity, err := s.repo.GetByID(ctx, id)
		if err != nil {
			return nil, normalizeNotFound(err)
		}

		code = getStrField(_entity, vf.CodeField)
	}
	return code, nil
}
func (s *GenericService[M]) _doListVersions(ctx context.Context, code any) ([]M, error) {
	vf := s.config.VersionFields
	if vf == nil {
		return nil, errs.ErrVersionFieldsNotSet
	}
	codeStr := fmt.Sprintf("%v", code)
	codeCol := resolveColumn[M](vf.CodeField)
	verCol := resolveColumn[M](vf.VersionField)

	records, _, err := s.repo.ListByFilters(ctx, repository.ListFilters{
		Filters:  []repository.Filter{{Field: codeCol, Op: repository.OpEQ, Value: codeStr}},
		OrderBy:  verCol,
		OrderDir: "desc",
		Page:     1,
		PageSize: 0,
	})
	return records, err
}
func (s *GenericService[M]) _afterListVersions(ctx context.Context, result []M) ([]M, error) {
	return result, nil
}

// -------- EditVersion --------
func (s *GenericService[M]) _beforeEditVersion(ctx context.Context, id any, patches map[string]any) (any, any, error) {
	if !s.config.VersionMode {
		return nil, nil, errs.ErrVersionNotEnabled
	}
	vf := s.config.VersionFields
	if vf == nil {
		return nil, nil, errs.ErrVersionFieldsNotSet
	}
	if len(patches) == 0 {
		return nil, nil, errs.ErrMissingParam("patches")
	}

	// 查当前实体（用于状态校验 & 备份旧值）
	_entity, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if norm := normalizeNotFound(err); norm != err {
			return nil, nil, norm
		}
		return nil, nil, errs.ErrQueryRecordFailed(err)
	}

	// 校验状态迁移：遍历 patches 所有 key，通过 resolveColumn 匹配 StatusField
	if vf.StatusField != "" {
		statusCol := resolveColumn[M](vf.StatusField)
		for k, v := range patches {
			if k == vf.StatusField || resolveColumn[M](k) == statusCol {
				curStatus := getStrField(_entity, vf.StatusField)
				newStatusStr := fmt.Sprintf("%v", v)
				if !s.isValidStatusTransition(curStatus, newStatusStr) {
					return nil, nil, errs.ErrInvalidVersionStatusTransition
				}
				break
			}
		}
	}

	return id, &editVersionCtx[M]{Old: _entity, Patches: patches}, nil
}

// validVersionTransitions 版本状态迁移规则（edit-version API）。
//
//	deprecated ↔ published（双向）
//	deprecated -> abolished（单向）
//	abolished -> draft（双向）
//
// 注意：draft → published 必须通过 Activate API，edit-version 不支持此转换。
var validVersionTransitions = map[string][]string{
	"deprecated": {"published", "abolished"},
	"published":  {"deprecated"},
	"abolished":  {"draft"},
}

// isValidStatusTransition 校验状态迁移是否合法
func (s *GenericService[M]) isValidStatusTransition(from, to string) bool {
	if from == to {
		return true
	}
	targets, ok := validVersionTransitions[from]
	if !ok {
		return false
	}
	for _, t := range targets {
		if t == to {
			return true
		}
	}
	return false
}

func (s *GenericService[M]) _doEditVersion(ctx context.Context, id any, pdata any) (*M, error) {
	eCtx, ok := pdata.(*editVersionCtx[M])
	if !ok {
		return nil, errs.ErrDoUpdateTypeMismatch
	}
	vf := s.config.VersionFields
	if vf == nil {
		return nil, errs.ErrVersionFieldsNotSet
	}

	now := time.Now()
	userULID := GetUserULID(ctx)

	// 判定本次是否「进入 published」（REQ: publish trace）。
	// 判定输入在 _beforeEditVersion 已装进 eCtx（Old + Patches），此处直接用 ——
	// 应用侧钩子拿不到旧值，因此这一步必须由框架完成，不能推给下游。
	newStatus := s.patchedVersionStatus(eCtx.Old, eCtx.Patches)
	oldStatus := ""
	if eCtx.Old != nil && vf.StatusField != "" {
		oldStatus = getStrField(eCtx.Old, vf.StatusField)
	}
	publishing := s.statusBecomesPublished(oldStatus, newStatus)

	// 将 Go 字段名映射为 DB 列名
	updates := make(map[string]any)
	for goField, val := range eCtx.Patches {
		col := resolveColumn[M](goField)
		updates[col] = val
	}
	updates["updated_at"] = now

	// 发布痕迹基础列（方案 A）：与 activate 同一套语义 ——
	// 「复活一个已废弃版本」算一次需要留痕的新发布（REQ §五 口径）。
	// 注意：终点非 published（published → deprecated 下线）时**不写** published_at，
	// 也不产生发布历史（REQ §六·2）。
	if publishing {
		atCol, byCol := s.publishedAtColumn(), s.publishedByColumn()
		if atCol == "" && byCol == "" {
			s.warnPublishTraceUnavailable(errs.ErrPublishTraceNotSupported)
		}
		if atCol != "" {
			updates[atCol] = &now
		}
		if byCol != "" && userULID != "" {
			updates[byCol] = userULID
		}
	}

	if err := s.repo.UpdateByID(ctx, id, updates); err != nil {
		return nil, err
	}

	// 查回更新后的结果
	result, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if norm := normalizeNotFound(err); norm != err {
			return nil, norm
		}
		return nil, errs.ErrQueryRecordFailed(err)
	}

	// 发布历史 + OnPublished 回调（via=edit-version）。
	// 本路径是「复活已废弃版本」—— 最高危的动作，此前完全不留痕。
	if publishing {
		s.recordPublished(ctx, result, PublishViaEditVersion)
	}
	return result, nil
}

// patchedVersionStatus 从 patches 中解析本次要写入的版本状态值。
//
// patches 的 key 可能是 Go 字段名（"VersionStatus"）或请求里的列名/别名，
// 与 _beforeEditVersion 的状态迁移校验保持同一套匹配口径（resolveColumn 归一）。
// 未涉及状态字段时返回空串（调用点据此判定为「不改变状态」）。
func (s *GenericService[M]) patchedVersionStatus(old *M, patches map[string]any) string {
	vf := s.config.VersionFields
	if vf == nil || vf.StatusField == "" {
		return ""
	}
	statusCol := resolveColumn[M](vf.StatusField)
	for k, v := range patches {
		if k == vf.StatusField || resolveColumn[M](k) == statusCol {
			return fmt.Sprintf("%v", v)
		}
	}
	// patches 未含状态字段：维持旧值（不算「进入 published」）
	if old != nil {
		return getStrField(old, vf.StatusField)
	}
	return ""
}

func (s *GenericService[M]) _afterEditVersion(ctx context.Context, id any, result *M, pdata any) (*M, error) {
	if s.opLogReady() {
		rec := s.makeOpLogRecord(ctx, fmt.Sprintf("%v", id), "updateVersion")
		if eCtx, ok := pdata.(*editVersionCtx[M]); ok && eCtx.Old != nil {
			rec.RecordBefore = snapshotJSON(eCtx.Old)
		}
		rec.RecordAfter = snapshotJSON(result)
		s.writeOpLogRecords(ctx, []OpLogRecord{rec})
	}

	// 写备份日志文件（旧值快照）；门控只看 bakWriter（BUG-077 §8.2）
	if s.bakWriter != nil {
		if eCtx, ok := pdata.(*editVersionCtx[M]); ok && eCtx.Old != nil {
			if err := s.bakWriter(ctx, s.config.EntityName, id, "updateVersion", eCtx.Old, GetRequestID(ctx)); err != nil {
				logrus.Errorf("gocrux: 写备份日志失败（entity_type=%s operation=updateVersion id=%v）: %v",
					s.config.EntityName, id, err)
			}
		}
	}
	return result, nil
}
