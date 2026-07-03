package service

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/Huey1979/gocrux/common"
	errs "github.com/Huey1979/gocrux/errors"
	"github.com/Huey1979/gocrux/internal/model/entity"
	"github.com/Huey1979/gocrux/repository"

	"gorm.io/gorm"
)

// ============================================================
// 内置 _before / _do / _after 默认实现
// ============================================================

func (s *GenericService[M]) _beforeCreate(ctx context.Context, input []CrudRequest[M]) ([]*M, error) {
	// 1. MergeTo → 将请求数据灌入实体
	entities := make([]*M, 0, len(input))
	now := time.Now()
	userID := GetUserULID(ctx)
	for _, req := range input {
		m := newRecord[M]()
		m.SetDefaults()
		if err := req.MergeTo(&m); err != nil {
			return nil, err
		}
		m.SetID()
		m.SetCreatedBy(userID)
		m.SetCreatedAt(now)
		m.SetUpdatedAt(now)

		// 版本化实体：Create 时自动设置初始版本号 v1.0
		// （Update 时由 _beforeUpdateVersioned 调用 nextVersionCode 递增）
		if s.config.VersionMode && s.config.VersionFields != nil {
			vf := s.config.VersionFields
			if getStrField(&m, vf.VersionField) == "" {
				common.SetFieldValue(&m, vf.VersionField, "v1.0")
			}
		}

		entities = append(entities, &m)
	}

	// 2. 配置驱动校验（唯一性等）
	if s.config.EnableUniqueValidation {
		if !s.validateUnique(ctx, entities, nil) {
			return nil, errs.ErrUniqueValidationFailed
		}
	}

	return entities, nil
}

// validateUnique 唯一性校验
// entities: 待校验实体
// selfPK:  更新场景传入自身主键以跳过自身；创建场景传 nil
func (s *GenericService[M]) validateUnique(ctx context.Context, entities []*M, selfPK any) bool {
	if !s.config.EnableUniqueValidation || len(s.config.UniqueFields) == 0 || len(entities) == 0 {
		return true
	}

	for _, group := range s.config.UniqueFields {
		// 1) 批次内互斥
		for i := 0; i < len(entities); i++ {
			for j := i + 1; j < len(entities); j++ {
				if s.allFieldsMatch(entities[i], entities[j], group) {
					return false
				}
			}
		}
		// 2) DB 冲突检查
		for _, ent := range entities {
			if !s.checkUniqueDB(ctx, ent, group, selfPK) {
				return false
			}
		}
	}
	return true
}

// allFieldsMatch 两个实体在指定字段组上全部等值才算匹配
func (s *GenericService[M]) allFieldsMatch(a, b *M, group []string) bool {
	for _, goField := range group {
		if getStrField(a, goField) != getStrField(b, goField) {
			return false
		}
	}
	return true
}

// checkUniqueDB 查 DB 是否存在冲突记录
func (s *GenericService[M]) checkUniqueDB(ctx context.Context, ent *M, group []string, selfPK any) bool {
	filters := make([]repository.Filter, 0, len(group)+2)
	for _, goField := range group {
		col := resolveColumn[M](goField)
		val := getFieldVal(ent, goField)
		filters = append(filters, repository.Filter{Field: col, Op: repository.OpEQ, Value: val})
	}

	// 排除自身（更新场景）
	if selfPK != nil {
		filters = append(filters, repository.Filter{Field: s.repo.PKField(), Op: repository.OpNEQ, Value: selfPK})
	}

	// 版本化：排除同 code 族（同 code 的多个版本允许重复）
	if s.config.VersionMode && s.config.VersionFields != nil {
		code := getStrField(ent, s.config.VersionFields.CodeField)
		if code != "" {
			codeCol := resolveColumn[M](s.config.VersionFields.CodeField)
			filters = append(filters, repository.Filter{Field: codeCol, Op: repository.OpNEQ, Value: code})
		}
	}

	_, count, err := s.repo.ListByFilters(ctx, repository.ListFilters{
		Filters:  filters,
		Page:     1,
		PageSize: 1,
	})
	if err != nil {
		return false
	}
	return count == 0
}

func (s *GenericService[M]) _doCreate(ctx context.Context, input []*M) ([]*M, error) {
	// 版本化实体：Create 时不允许使用已存在的 code（应去 Update 而非 Create）
	if s.config.VersionMode && s.config.VersionFields != nil && len(input) > 0 {
		vf := s.config.VersionFields
		codeCol := resolveColumn[M](vf.CodeField)
		for _, ent := range input {
			code := getStrField(ent, vf.CodeField)
			if code == "" {
				continue
			}
			_, total, err := s.repo.ListByFilters(ctx, repository.ListFilters{
				Filters:  []repository.Filter{{Field: codeCol, Op: repository.OpEQ, Value: code}},
				Page:     1,
				PageSize: 1,
			})
			if err != nil {
				return nil, err
			}
			if total > 0 {
				return nil, fmt.Errorf("%w: %s", errs.ErrDuplicateCode, code)
			}
		}
	}

	if err := s.repo.InsertBatch(ctx, input); err != nil {
		return nil, err
	}
	return input, nil
}

// opLogEntry 单条操作日志信息
type opLogEntry struct {
	EntityID  string
	Operation string
}

// deprecateByCode 将指定 code 的所有记录设为 is_current=0。
// 供 Create/Update/Activate 复用。
func (s *GenericService[M]) deprecateByCode(tx *gorm.DB, code string) error {
	vf := s.config.VersionFields
	codeCol := resolveColumn[M](vf.CodeField)
	currentCol := resolveColumn[M](vf.CurrentField)
	return tx.Model(new(M)).Where(codeCol+" = ?", code).
		Update(currentCol, int8(0)).Error
}

func (s *GenericService[M]) _afterCreate(ctx context.Context, result []*M) ([]*M, error) {
	if s.config.EnableOpLog && s.opLogRepo != nil && len(result) > 0 {
		entries := make([]opLogEntry, len(result))
		for i, ent := range result {
			entries[i] = opLogEntry{EntityID: extractEntityID(ent), Operation: "create"}
		}
		s.batchWriteOpLog(ctx, entries)
	}
	return result, nil
}

// writeOpLog 向日志表写入一条操作记录（仅元数据，不含数据快照）
// entityID：非版本化为单条 ULID；版本化删除时为涉及的所有 ULID（逗号分隔）
func (s *GenericService[M]) writeOpLog(ctx context.Context, entityID, operation string) {
	_ = s.opLogRepo.Insert(ctx, &entity.SysOperationLog{
		LogULID:      common.NewULID(),
		EntityType:   s.config.EntityName,
		EntityID:     entityID,
		Operation:    operation,
		OperatorULID: GetUserULID(ctx),
		RequestID:    GetRequestID(ctx),
		OperatedAt:   time.Now(),
	})
}

// batchWriteOpLog 批量写入操作日志（一次 InsertBatch，避免 N+1）
func (s *GenericService[M]) batchWriteOpLog(ctx context.Context, entries []opLogEntry) {
	if len(entries) == 0 {
		return
	}
	logs := make([]*entity.SysOperationLog, len(entries))
	now := time.Now()
	userID := GetUserULID(ctx)
	requestID := GetRequestID(ctx)
	for i, e := range entries {
		logs[i] = &entity.SysOperationLog{
			LogULID:      common.NewULID(),
			EntityType:   s.config.EntityName,
			EntityID:     e.EntityID,
			Operation:    e.Operation,
			OperatorULID: userID,
			RequestID:    requestID,
			OperatedAt:   now,
		}
	}
	_ = s.opLogRepo.InsertBatch(ctx, logs)
}

// extractEntityID 提取实体的 ULID（尝试 GetULID 接口，失败则用反射查找以 ULID 结尾的字段）
func extractEntityID(v any) string {
	type hasULID interface{ GetULID() string }
	if u, ok := v.(hasULID); ok {
		return u.GetULID()
	}
	val := reflect.ValueOf(v)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct {
		return ""
	}
	for i := 0; i < val.NumField(); i++ {
		if strings.HasSuffix(val.Type().Field(i).Name, "ULID") {
			return val.Field(i).String()
		}
	}
	return ""
}

// -------- Update --------
func (s *GenericService[M]) _beforeUpdate(ctx context.Context, id, data any) (any, any, error) {
	// 1. 取出现有记录
	old, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, nil, errs.ErrQueryRecordFailed(err)
	}

	// 2. 版本模式：深拷贝 + 合并请求到新行 + 填写版本字段
	if s.config.VersionMode {
		vf := s.config.VersionFields
		if vf == nil {
			return nil, nil, errs.ErrVersionFieldsNotSet
		}
		return s._beforeUpdateVersioned(ctx, id, data, old, vf)
	}

	// 3. 非版本模式：CrudRequest → 直接合并到现有实体
	req, ok := data.(CrudRequest[M])
	if !ok {
		return nil, nil, errs.ErrUpdateDataNotRequest
	}
	// 合并前保存旧值快照，供 _afterUpdate 写日志
	oldCopy := *old
	if err := req.MergeTo(old); err != nil {
		return nil, nil, err
	}

	// 4. 审计字段
	ent := *old
	ent.SetUpdatedAt(time.Now())
	ent.SetUpdatedBy(GetUserULID(ctx))

	// 5. 唯一性校验（排除自身）
	if s.config.EnableUniqueValidation {
		if !s.validateUnique(ctx, []*M{&ent}, id) {
			return nil, nil, errs.ErrUniqueValidationFailed
		}
	}

	// 6. 用 updatePair 同时携带旧值和新值
	return id, &updatePair[M]{Old: &oldCopy, New: &ent}, nil
}

// _beforeUpdateVersioned 版本化更新的 before 处理
func (s *GenericService[M]) _beforeUpdateVersioned(ctx context.Context, id, data any, old *M, vf *VersionFieldMapping) (any, any, error) {
	// 1. 深拷贝旧数据（通过反射复制，避免指针共享）
	oldPtrVal := reflect.ValueOf(*old)
	for oldPtrVal.Kind() == reflect.Ptr {
		oldPtrVal = oldPtrVal.Elem()
	}
	newPtrVal := reflect.New(oldPtrVal.Type())
	newPtrVal.Elem().Set(oldPtrVal)
	newEntity := newPtrVal.Interface().(M)

	// 1b. 重置版本默认值：深拷贝保留了旧值，新版应重新初始化
	newEntity.SetDefaults()

	// 2. 合并请求字段到新行
	if data != nil {
		if req, ok := data.(CrudRequest[M]); ok {
			if err := req.MergeTo(&newEntity); err != nil {
				return nil, nil, err
			}
		}
	}

	// 2b. 审计字段（版本化模式下新行是一版全新记录，但仍记录编辑人）
	newEntity.SetUpdatedBy(GetUserULID(ctx))

	// 3. 提取旧版本信息
	oldVal := reflect.ValueOf(*old)
	for oldVal.Kind() == reflect.Ptr {
		oldVal = oldVal.Elem()
	}
	oldULID := ""
	oldVersionCode := ""
	if f := oldVal.FieldByName(vf.ULIDField); f.IsValid() {
		oldULID = f.String()
	}
	if f := oldVal.FieldByName(vf.VersionField); f.IsValid() {
		oldVersionCode = f.String()
	}

	// 4. 设置版本字段
	common.SetFieldValue(&newEntity, vf.ULIDField, common.NewULID())
	common.SetFieldValue(&newEntity, vf.CurrentField, int8(1))
	common.SetFieldValue(&newEntity, vf.ParentField, oldULID)
	common.SetFieldValue(&newEntity, vf.VersionField, nextVersionCode(oldVersionCode))
	// 草稿箱：若请求未指定状态，默认新版本为 draft；若用户明确传了值则保留
	if vf.StatusField != "" {
		newStatus := getStrField(&newEntity, vf.StatusField)
		if (*old).SupportsDraft() && (newStatus == "") {
			common.SetFieldValue(&newEntity, vf.StatusField, string(VersionStatusDraft))
		}
	}
		if vf.RemarkField != "" {
			// 若用户已在请求中传了 version_remark（步骤2合并后非空），保留用户值
			existingRemark := getStrField(&newEntity, vf.RemarkField)
			if existingRemark == "" {
				remark := "更新操作"
				if vr, ok := data.(interface{ GetVersionRemark() string }); ok && vr.GetVersionRemark() != "" {
					remark = vr.GetVersionRemark()
				}
				common.SetFieldValue(&newEntity, vf.RemarkField, remark)
			}
		}

	// 5. 唯一性校验（版本化：同 code 族豁免；新行尚无 DB 记录，无需自排除）
	if s.config.EnableUniqueValidation {
		if !s.validateUnique(ctx, []*M{&newEntity}, nil) {
			return nil, nil, errs.ErrUniqueValidationFailed
		}
	}

	return id, &updatePair[M]{Old: old, New: &newEntity}, nil
}

func (s *GenericService[M]) _doUpdate(ctx context.Context, id, data any) (*M, error) {
	// 版本模式：事务中旧行退位 + 新行插入
	if s.config.VersionMode {
		pair, ok := data.(*updatePair[M])
		if !ok {
			return nil, errs.ErrUpdatePairTypeMismatch
		}
		vf := s.config.VersionFields
		if vf == nil {
			return nil, errs.ErrVersionFieldsNotSet
		}

		code := getStrField(pair.New, vf.CodeField)
		codeCol := resolveColumn[M](vf.CodeField)
		currentCol := resolveColumn[M](vf.CurrentField)

		if cr := s.CRUDRepo(); cr != nil {
			// MySQL：GORM 事务内批量退位 + 插入新版本
			var txErr error
			txErr = cr.Transaction(ctx, func(tx *gorm.DB) error {
				if err := tx.Model(new(M)).Where(codeCol+" = ?", code).
					Update(currentCol, int8(0)).Error; err != nil {
					return err
				}
				if (*pair.New).SupportsDraft() && vf.StatusField != "" {
					newStatus := getStrField(pair.New, vf.StatusField)
					statusCol := resolveColumn[M](vf.StatusField)
					if newStatus == string(VersionStatusPublished) {
						if err := tx.Model(new(M)).Where(
							codeCol+" = ? AND "+statusCol+" = ?", code, string(VersionStatusPublished),
						).Update(statusCol, string(VersionStatusDeprecated)).Error; err != nil {
							return err
						}
					}
				}
				return tx.Create(pair.New).Error
			})
			if txErr != nil {
				return nil, txErr
			}
		} else {
			// MongoDB：逐条退位 + 插入新版本（repo 层方法）
			if err := s.repo.BatchDeprecateVersionsByFK(ctx, codeCol, []any{code}); err != nil {
				return nil, err
			}
			if err := s.repo.Insert(ctx, pair.New); err != nil {
				return nil, err
			}
		}
		return pair.New, nil
	}

	// 非版本模式：解包 updatePair 后直接保存
	var _entity *M
	if pair, ok := data.(*updatePair[M]); ok {
		_entity = pair.New
	} else {
		_entity, ok = data.(*M)
		if !ok {
			return nil, errs.ErrDoUpdateTypeMismatch
		}
	}
	if err := s.repo.Save(ctx, _entity); err != nil {
		return nil, err
	}
	return _entity, nil
}
func (s *GenericService[M]) _afterUpdate(ctx context.Context, id any, result *M, pdata any) (*M, error) {
	if s.config.EnableOpLog && s.opLogRepo != nil {
		// 1. 日志表：只记元数据（谁、何时、对谁、做了什么），不存数据快照
		s.writeOpLog(ctx, extractEntityID(result), "update")

		// 2. 非版本化：旧数据会被覆盖丢失，写备份日志文件
		if !s.config.VersionMode && s.bakWriter != nil {
			if pair, ok := pdata.(*updatePair[M]); ok && pair.Old != nil {
				_ = s.bakWriter(ctx, s.config.EntityName, id, "update", pair.Old, GetRequestID(ctx))
			}
		}
	}
	return result, nil
}

// -------- Delete --------
func (s *GenericService[M]) _beforeDelete(ctx context.Context, ids, codes any) (any, any, error) {
	// ids 归一化为 []any
	var idList []any
	if ids != nil {
		if v, ok := ids.([]any); ok {
			idList = v
		}
	}

	if !s.config.VersionMode {
		// 非版本化：直接透传 ULID 列表
		if len(idList) == 0 {
			return nil, nil, errs.ErrRecordNotFound
		}
		return idList, nil, nil
	}

	// 版本化：若调用方显式传了 codes → 按 code 族全量删除。
	// 若仅有 ids → 仅标记指定 ULID，不扩展 code 族（BUG-001 修复）。
	if codes != nil {
		if cs, ok := codes.([]any); ok && len(cs) > 0 {
			vf := s.config.VersionFields
			if vf == nil {
				return nil, nil, errs.ErrVersionFieldsNotSet
			}
			codeCol := resolveColumn[M](vf.CodeField)
			codeList := make([]string, len(cs))
			for i, c := range cs {
				codeList[i] = fmt.Sprintf("%v", c)
			}

			// 按 code 批量查全族 ULID（去重）
			ulidSet := make(map[string]struct{})
			allRecords, _, err := s.repo.ListByFilters(ctx, repository.ListFilters{
				Filters:  []repository.Filter{{Field: codeCol, Op: repository.OpIn, Value: codeList}},
				Page:     1,
				PageSize: 0,
			})
			if err != nil {
				return nil, nil, errs.ErrQueryRecordFailed(err)
			}
			for i := range allRecords {
				ulidSet[getStrField(&allRecords[i], vf.ULIDField)] = struct{}{}
			}
			ulidList := make([]any, 0, len(ulidSet))
			for u := range ulidSet {
				ulidList = append(ulidList, u)
			}
			if len(ulidList) == 0 {
				return nil, nil, errs.ErrRecordNotFound
			}
			return ulidList, nil, nil
		}
	}

	// 仅指定 ULID：直接透传，不扩展 code 族
	if len(idList) == 0 {
		return nil, nil, errs.ErrRecordNotFound
	}
	return idList, nil, nil
}


func (s *GenericService[M]) _doDelete(ctx context.Context, id, data any) error {
	// 归一化：单个 id → [id]
	ids, ok := id.([]any)
	if !ok {
		ids = []any{id}
	}
	if len(ids) == 0 {
		return errs.ErrDeleteDataInvalid
	}

	// 版本化：废弃当前版本
	if s.config.VersionMode && s.config.VersionFields != nil {
		return s.repo.BatchDeprecateVersions(ctx, ids)
	}
	// 非版本化：软删除或物理删
	m := newRecord[M]()
	if m.SetDelete() {
		return s.repo.BatchSoftDelete(ctx, ids) // 设置 isDeleted=1
	}

	// 物理删除：先备份数据，再硬删除
	if s.config.EnableOpLog && s.bakWriter != nil {
		records, _ := s.repo.BatchFindByPK(ctx, ids)
		for i := range records {
			_ = s.bakWriter(ctx, s.config.EntityName, extractEntityID(&records[i]), "delete", &records[i], GetRequestID(ctx))
		}
	}
	return s.repo.BatchHardDelete(ctx, ids)
}

// DeleteByFK 按外键字段批量软删除记录（供级联删除使用）。
// fkField 为数据库列名（与 JSON 字段名一致），如 "parent_id"。
// 同样遵循 SetDelete() 的分支：软删 → Update is_deleted；物理删 → Unscoped Delete + 备份。
func (s *GenericService[M]) DeleteByFK(ctx context.Context, fkField string, fkValues []any) error {
	if len(fkValues) == 0 {
		return nil
	}

	m := newRecord[M]()
	if m.SetDelete() {
		return s.repo.BatchSoftDeleteByFK(ctx, fkField, fkValues)
	}

	// 物理删除：先备份数据
	if s.config.EnableOpLog && s.bakWriter != nil {
		records, _ := s.repo.BatchFindByFK(ctx, fkField, fkValues)
		for i := range records {
			_ = s.bakWriter(ctx, s.config.EntityName, extractEntityID(&records[i]), "delete_by_fk", &records[i], GetRequestID(ctx))
		}
	}
	return s.repo.BatchHardDeleteByFK(ctx, fkField, fkValues)
}

func (s *GenericService[M]) _afterDelete(ctx context.Context, id, data any) error {
	if !s.config.EnableOpLog || s.opLogRepo == nil {
		return nil
	}
	ids, ok := id.([]any)
	if !ok {
		ids = []any{id}
	}
	strs := make([]string, len(ids))
	for i, v := range ids {
		strs[i] = fmt.Sprintf("%v", v)
	}
	s.writeOpLog(ctx, strings.Join(strs, ","), "delete")
	return nil
}
