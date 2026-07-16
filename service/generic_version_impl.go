package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Huey1979/gocrux/common"
	errs "github.com/Huey1979/gocrux/errors"
	"github.com/Huey1979/gocrux/repository"

	"gorm.io/gorm"
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
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrRecordNotFound
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
	if currentStatus == string(VersionStatusDraft) || currentStatus == string(VersionStatusDeprecated) {
		common.SetFieldValue(_entity, vf.StatusField, string(VersionStatusPublished))
		if vf.PublishedAtField != "" {
			common.SetFieldValue(_entity, vf.PublishedAtField, &now)
		}
		if vf.PublishedByField != "" && userULID != "" {
			common.SetFieldValue(_entity, vf.PublishedByField, userULID)
		}
	}

	// 4. 用 Save 持久化（兼容 MySQL GORM 与 MongoDB）
	return s.repo.Save(ctx, _entity)
}

func (s *GenericService[M]) _afterActivate(ctx context.Context, id any) error {
	if !s.config.EnableOpLog || s.opLogRepo == nil {
		return nil
	}
	_entity, ok := id.(*M)
	if !ok {
		return nil
	}
	s.writeOpLog(ctx, extractEntityID(_entity), "activate")

	// 备份日志文件
	if s.bakWriter != nil {
		_ = s.bakWriter(ctx, s.config.EntityName, extractEntityID(_entity), "activate", _entity, GetRequestID(ctx))
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
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errs.ErrRecordNotFound
			}
			return nil, err
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
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, errs.ErrRecordNotFound
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

	// 将 Go 字段名映射为 DB 列名
	updates := make(map[string]any)
	for goField, val := range eCtx.Patches {
		col := resolveColumn[M](goField)
		updates[col] = val
	}
	updates["updated_at"] = time.Now()

	if err := s.repo.UpdateByID(ctx, id, updates); err != nil {
		return nil, err
	}

	// 查回更新后的结果
	result, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrRecordNotFound
		}
		return nil, errs.ErrQueryRecordFailed(err)
	}
	return result, nil
}

func (s *GenericService[M]) _afterEditVersion(ctx context.Context, id any, result *M, pdata any) (*M, error) {
	if s.config.EnableOpLog && s.opLogRepo != nil {
		s.writeOpLog(ctx, fmt.Sprintf("%v", id), "updateVersion")

		// 写备份日志文件（旧值快照）
		if s.bakWriter != nil {
			if eCtx, ok := pdata.(*editVersionCtx[M]); ok && eCtx.Old != nil {
				_ = s.bakWriter(ctx, s.config.EntityName, id, "updateVersion", eCtx.Old, GetRequestID(ctx))
			}
		}
	}
	return result, nil
}
