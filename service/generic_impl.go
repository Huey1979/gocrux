package service

import (
	"context"
	"errors"
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

// KeywordSearch 关键字搜索配置（通过 context 从 Handler 传递到 Service）。
type keywordSearchKey struct{}

type KeywordSearch struct {
	Keyword string
	Fields  []string
}

// WithKeywordSearch 将关键字搜索配置注入 context。
func WithKeywordSearch(ctx context.Context, ks KeywordSearch) context.Context {
	return context.WithValue(ctx, keywordSearchKey{}, ks)
}

// ============================================================
// 内置 before — 钩子优先，否则 fallback 默认实现
// ============================================================

func (s *GenericService[M]) beforeCreate(ctx context.Context, input []CrudRequest[M]) ([]*M, error) {
	if s.hooks.BeforeCreate != nil {
		return s.hooks.BeforeCreate(ctx, input)
	}
	return s._beforeCreate(ctx, input)
}

func (s *GenericService[M]) beforeUpdate(ctx context.Context, id, data any) (any, any, error) {
	if s.hooks.BeforeUpdate != nil {
		return s.hooks.BeforeUpdate(ctx, id, data)
	}
	return s._beforeUpdate(ctx, id, data)
}

func (s *GenericService[M]) beforeDelete(ctx context.Context, ids, codes any) (any, any, error) {
	if s.hooks.BeforeDelete != nil {
		return s.hooks.BeforeDelete(ctx, ids, codes)
	}
	return s._beforeDelete(ctx, ids, codes)
}

func (s *GenericService[M]) beforeGet(ctx context.Context, id any) (any, error) {
	if s.hooks.BeforeGet != nil {
		return s.hooks.BeforeGet(ctx, id)
	}
	return s._beforeGet(ctx, id)
}

func (s *GenericService[M]) beforeList(ctx context.Context, query any) (any, error) {
	if s.hooks.BeforeList != nil {
		return s.hooks.BeforeList(ctx, query)
	}
	return s._beforeList(ctx, query)
}

func (s *GenericService[M]) beforeActivate(ctx context.Context, id any) (any, error) {
	if s.hooks.BeforeActivate != nil {
		return s.hooks.BeforeActivate(ctx, id)
	}
	return s._beforeActivate(ctx, id)
}

func (s *GenericService[M]) beforeListVersions(ctx context.Context, id any, code string) (any, error) {
	if s.hooks.BeforeListVersions != nil {
		return s.hooks.BeforeListVersions(ctx, id, code)
	}
	return s._beforeListVersions(ctx, id, code)
}

func (s *GenericService[M]) beforeEditVersion(ctx context.Context, id any, patches map[string]any) (any, any, error) {
	if s.hooks.BeforeEditVersion != nil {
		return s.hooks.BeforeEditVersion(ctx, id, patches)
	}
	return s._beforeEditVersion(ctx, id, patches)
}

// ============================================================
// 内置 do — 钩子优先，否则 fallback 默认实现
// ============================================================

func (s *GenericService[M]) doCreate(ctx context.Context, input []*M) ([]*M, error) {
	if s.hooks.DoCreate != nil {
		return s.hooks.DoCreate(ctx, input)
	}
	return s._doCreate(ctx, input)
}

func (s *GenericService[M]) doUpdate(ctx context.Context, id, data any) (*M, error) {
	if s.hooks.DoUpdate != nil {
		return s.hooks.DoUpdate(ctx, id, data)
	}
	return s._doUpdate(ctx, id, data)
}

func (s *GenericService[M]) doDelete(ctx context.Context, id, data any) error {
	if s.hooks.DoDelete != nil {
		return s.hooks.DoDelete(ctx, id, data)
	}
	return s._doDelete(ctx, id, data)
}

func (s *GenericService[M]) doGet(ctx context.Context, id any) (*M, error) {
	if s.hooks.DoGet != nil {
		return s.hooks.DoGet(ctx, id)
	}
	return s._doGet(ctx, id)
}

func (s *GenericService[M]) doList(ctx context.Context, query any) ([]M, int64, error) {
	if s.hooks.DoList != nil {
		return s.hooks.DoList(ctx, query)
	}
	return s._doList(ctx, query)
}

func (s *GenericService[M]) doActivate(ctx context.Context, id any) error {
	if s.hooks.DoActivate != nil {
		return s.hooks.DoActivate(ctx, id)
	}
	return s._doActivate(ctx, id)
}

func (s *GenericService[M]) doListVersions(ctx context.Context, id any) ([]M, error) {
	if s.hooks.DoListVersions != nil {
		return s.hooks.DoListVersions(ctx, id)
	}
	return s._doListVersions(ctx, id)
}

func (s *GenericService[M]) doEditVersion(ctx context.Context, id any, pdata any) (*M, error) {
	if s.hooks.DoEditVersion != nil {
		// hook 层面传原始 patches（向后兼容）
		if eCtx, ok := pdata.(*editVersionCtx[M]); ok {
			return s.hooks.DoEditVersion(ctx, id, eCtx.Patches)
		}
		if patches, ok := pdata.(map[string]any); ok {
			return s.hooks.DoEditVersion(ctx, id, patches)
		}
		return nil, errs.ErrDoUpdateTypeMismatch
	}
	return s._doEditVersion(ctx, id, pdata)
}

// ============================================================
// 内置 after — 钩子优先，否则 fallback 默认实现（可修改返回值）
// ============================================================

func (s *GenericService[M]) afterCreate(ctx context.Context, result []*M) ([]*M, error) {
	if s.hooks.AfterCreate != nil {
		return s.hooks.AfterCreate(ctx, result)
	}
	return s._afterCreate(ctx, result)
}

func (s *GenericService[M]) afterUpdate(ctx context.Context, id any, result *M, pdata any) (*M, error) {
	if s.hooks.AfterUpdate != nil {
		return s.hooks.AfterUpdate(ctx, id, result, pdata)
	}
	return s._afterUpdate(ctx, id, result, pdata)
}

func (s *GenericService[M]) afterDelete(ctx context.Context, id, data any) error {
	if s.hooks.AfterDelete != nil {
		return s.hooks.AfterDelete(ctx, id, data)
	}
	return s._afterDelete(ctx, id, data)
}

func (s *GenericService[M]) afterGet(ctx context.Context, result *M) (*M, error) {
	if s.hooks.AfterGet != nil {
		return s.hooks.AfterGet(ctx, result)
	}
	return s._afterGet(ctx, result)
}

func (s *GenericService[M]) afterList(ctx context.Context, list []M, total int64) ([]M, int64, error) {
	if s.hooks.AfterList != nil {
		return s.hooks.AfterList(ctx, list, total)
	}
	return s._afterList(ctx, list, total)
}

func (s *GenericService[M]) afterActivate(ctx context.Context, id any) error {
	if s.hooks.AfterActivate != nil {
		return s.hooks.AfterActivate(ctx, id)
	}
	return s._afterActivate(ctx, id)
}

func (s *GenericService[M]) afterListVersions(ctx context.Context, result []M) ([]M, error) {
	if s.hooks.AfterListVersions != nil {
		return s.hooks.AfterListVersions(ctx, result)
	}
	return s._afterListVersions(ctx, result)
}

func (s *GenericService[M]) afterEditVersion(ctx context.Context, id any, result *M, pdata any) (*M, error) {
	if s.hooks.AfterEditVersion != nil {
		return s.hooks.AfterEditVersion(ctx, id, result)
	}
	return s._afterEditVersion(ctx, id, result, pdata)
}

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
	// 1. 深拷贝旧数据
	// æ·±æ·è´ï¼ä¸è½ç´æ¥ *(M)
	oldPtrVal := reflect.ValueOf(*old)
	for oldPtrVal.Kind() == reflect.Ptr {
		oldPtrVal = oldPtrVal.Elem()
	}
	newPtrVal := reflect.New(oldPtrVal.Type())
	newPtrVal.Elem().Set(oldPtrVal)
	newEntity := newPtrVal.Interface().(M)

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
	oldStatus := ""
	if f := oldVal.FieldByName(vf.ULIDField); f.IsValid() {
		oldULID = f.String()
	}
	if f := oldVal.FieldByName(vf.VersionField); f.IsValid() {
		oldVersionCode = f.String()
	}
	if f := oldVal.FieldByName(vf.StatusField); f.IsValid() {
		oldStatus = f.String()
	}

	// 4. 设置版本字段
	common.SetFieldValue(&newEntity, vf.ULIDField, common.NewULID())
	common.SetFieldValue(&newEntity, vf.CurrentField, int8(1))
	common.SetFieldValue(&newEntity, vf.ParentField, oldULID)
	common.SetFieldValue(&newEntity, vf.VersionField, nextVersionCode(oldVersionCode))
	// 草稿箱：默认新版本为 draft；若请求明确指定不同状态则保留
	if vf.StatusField != "" {
		newStatus := getStrField(&newEntity, vf.StatusField)
		if (*old).SupportsDraft() && (newStatus == "" || newStatus == oldStatus) {
			common.SetFieldValue(&newEntity, vf.StatusField, string(VersionStatusDraft))
		}
	}
	if vf.RemarkField != "" {
		remark := "更新操作"
		if vr, ok := data.(interface{ GetVersionRemark() string }); ok && vr.GetVersionRemark() != "" {
			remark = vr.GetVersionRemark()
		}
		common.SetFieldValue(&newEntity, vf.RemarkField, remark)
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

		cr := s.CRUDRepo()
		err := cr.Transaction(ctx, func(tx *gorm.DB) error {
			// 旧行退位
			common.SetFieldValue(pair.Old, vf.CurrentField, int8(0))

			// 草稿箱：根据新版本状态处理旧正式版
			if (*pair.New).SupportsDraft() {
				newStatus := getStrField(pair.New, vf.StatusField)
				code := getStrField(pair.New, vf.CodeField)
				oldStatus := getStrField(pair.Old, vf.StatusField)
				codeCol := resolveColumn[M](vf.CodeField)
				statusCol := resolveColumn[M](vf.StatusField)

				if newStatus == string(VersionStatusPublished) {
					// 新版本为正式版 → 同 code 旧正式版 → 已废弃
					if err := tx.Model(new(M)).Where(
						codeCol+" = ? AND "+statusCol+" = ?", code, string(VersionStatusPublished),
					).Update(statusCol, string(VersionStatusDeprecated)).Error; err != nil {
						return err
					}
					// 同步内存，防止后续 Save 覆盖 DB 改动
					if oldStatus == string(VersionStatusPublished) {
						common.SetFieldValue(pair.Old, vf.StatusField, string(VersionStatusDeprecated))
					}
				}
				// 新版本为草稿：旧正式版保持 published 不变（已退位即可）
			}

			if err := tx.Save(pair.Old).Error; err != nil {
				return err
			}
			return tx.Create(pair.New).Error
		})
		if err != nil {
			return nil, err
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

	// 版本化：收集所有 code
	vf := s.config.VersionFields
	if vf == nil {
		return nil, nil, errs.ErrVersionFieldsNotSet
	}
	codeCol := resolveColumn[M](vf.CodeField)

	var codeList []string
	if codes != nil {
		if cs, ok := codes.([]any); ok {
			for _, c := range cs {
				codeList = append(codeList, fmt.Sprintf("%v", c))
			}
		}
	}

	if len(codeList) == 0 {
		// codes 为空 → 用 IN 查询一次取出所有记录，再从结果集取 codes（去重）
		records, _, err := s.repo.ListByFilters(ctx, repository.ListFilters{
			Filters:  []repository.Filter{{Field: s.repo.PKField(), Op: repository.OpIn, Value: idList}},
			Page:     1,
			PageSize: 0,
		})
		if err != nil {
			return nil, nil, errs.ErrQueryRecordFailed(err)
		}
		codeSet := make(map[string]struct{})
		for i := range records {
			codeSet[getStrField(&records[i], vf.CodeField)] = struct{}{}
		}
		for c := range codeSet {
			codeList = append(codeList, c)
		}
	}

	if len(codeList) == 0 {
		return nil, nil, errs.ErrRecordNotFound
	}

	// 按 code 批量查全族 ULID（去重）—— 用 IN 一次查询
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

func (s *GenericService[M]) _doDelete(ctx context.Context, id, data any) error {
	// 归一化：单个 id → [id]
	ids, ok := id.([]any)
	if !ok {
		ids = []any{id}
	}
	if len(ids) == 0 {
		return errs.ErrDeleteDataInvalid
	}

	// 判断实体是否支持软删除
	m := newRecord[M]()
	if m.SetDelete() {
		return s.repo.BatchSoftDelete(ctx, ids)
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

// -------- Get --------
func (s *GenericService[M]) _beforeGet(ctx context.Context, id any) (any, error) {
	if id == nil {
		return nil, errs.ErrInvalidParam
	}
	return id, nil
}
func (s *GenericService[M]) _doGet(ctx context.Context, id any) (*M, error) {
	result, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.ErrRecordNotFound
		}
		return nil, err
	}
	return result, nil
}
func (s *GenericService[M]) _afterGet(ctx context.Context, result *M) (*M, error) { return result, nil }

// _doGetByCode 按业务编码查当前生效版本。
// 版本化模式：CodeField = code AND CurrentField = 1（is_current=true，不论是否 published）。
// 非版本化模式：退化为 CodeField 字段等值查询（repo.GetByField）。
func (s *GenericService[M]) _doGetByCode(ctx context.Context, code string) (*M, error) {
	if !s.config.VersionMode || s.config.VersionFields == nil {
		// 非版本化模式：使用 VersionFields.CodeField，不再硬编码 "code"
		codeField := "code"
		if s.config.VersionFields != nil && s.config.VersionFields.CodeField != "" {
			codeField = resolveColumn[M](s.config.VersionFields.CodeField)
		}
		result, err := s.repo.GetByField(ctx, codeField, code)
		if err != nil {
			return nil, err
		}
		if result == nil {
			return nil, errs.ErrRecordNotFound
		}
		return result, nil
	}

	vf := s.config.VersionFields
	codeCol := resolveColumn[M](vf.CodeField)
	currentCol := resolveColumn[M](vf.CurrentField)

	// 查当前生效版本（is_current=1），不论是否 published
	results, _, err := s.repo.ListByFilters(ctx, repository.ListFilters{
		Filters: []repository.Filter{
			{Field: codeCol, Op: repository.OpEQ, Value: code},
			{Field: currentCol, Op: repository.OpEQ, Value: int8(1)},
		},
		Page:     1,
		PageSize: 1,
	})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, errs.ErrRecordNotFound
	}
	return &results[0], nil
}

// -------- List --------
func (s *GenericService[M]) _beforeList(ctx context.Context, query any) (any, error) {
	return query, nil
}
func (s *GenericService[M]) _doList(ctx context.Context, query any) ([]M, int64, error) {
	var f repository.ListFilters

	switch v := query.(type) {
	case repository.ListFilters:
		f = v
	case map[string]any:
		// 分离控制参数与过滤条件
		f.Page, f.PageSize = popIntParam(v, "page"), popIntParam(v, "page_size")
		f.OrderBy, f.OrderDir = popStrParam(v, "order_by"), popStrParam(v, "order_dir")

		for k, val := range v {
			field, op, value := parseFilterKey(k, val)
			if field == "" {
				continue
			}
			f.Filters = append(f.Filters, repository.Filter{Field: field, Op: op, Value: value})
		}

	default:
		// 无过滤条件，不分页返回全部
			all, err := s.repo.ListAll(ctx)
			if err != nil {
				return nil, 0, err
			}
			return all, int64(len(all)), nil
	}

	// keyword 关键字搜索：多字段 OR LIKE 匹配
	if ks, ok := ctx.Value(keywordSearchKey{}).(KeywordSearch); ok && ks.Keyword != "" {
		keywordFilters := make([]repository.Filter, 0, len(ks.Fields))
		for _, field := range ks.Fields {
			keywordFilters = append(keywordFilters, repository.Filter{
				Field: field, Op: repository.OpLike, Value: "%" + ks.Keyword + "%",
			})
		}
		f.Filters = append(f.Filters, repository.Filter{
			Op: "or_group", Value: keywordFilters,
		})
	}

	return s.repo.ListByFilters(ctx, f)
}

// ============================================================
// parseFilterKey — 解析 URL 查询参数键中的操作符后缀
//
// 使用 `:` 分隔（MySQL 列名不含冒号，绝对安全）：
//
//	field           → (field, OpEQ / OpIn, value)     自动：切片=OpIn，否则=OpEQ
//	field:like      → (field, OpLike, value)           LIKE，自动包裹 %value%
//	field:gt        → (field, OpGT, value)
//	field:gte       → (field, OpGTE, value)
//	field:lt        → (field, OpLT, value)
//	field:lte       → (field, OpLTE, value)
//	field:ne        → (field, OpNEQ, value)
//	field:in        → (field, OpIn, value)             逗号分隔字符串 → []any
//	field:between   → (field, OpRange, value)          逗号分隔字符串 → []any{lo,hi}
//
// 注意：field:like 的值会自动在前后追加 %，除非已包含 %。
// ============================================================
func parseFilterKey(key string, rawValue any) (field string, op repository.FilterOp, value any) {
	// 查找 : 分隔的运算符后缀（如 form_code:like=xxx）。
	// 使用 : 而非 _ 作为分隔符，避免与字段名中的下划线冲突。
	// 兼容旧的 __ 后缀（后续移除）
	parseLegacy := func() bool {
		if idx := strings.LastIndex(key, "__"); idx > 0 {
			field = key[:idx]
			switch key[idx+2:] {
			case "like", "gt", "gte", "lt", "lte", "ne", "in", "between":
				return true // fall through to switch below
			}
		}
		return false
	}
	if parseLegacy() {
		// 旧后缀已设置 field，下面 switch 会 set op + value
		suffix := key[strings.LastIndex(key, "__")+2:]
		switch suffix {
		case "like":
			s := fmt.Sprintf("%v", rawValue)
			if !strings.Contains(s, "%") {
				s = "%" + s + "%"
			}
			return field, repository.OpLike, s
		case "gt":
			return field, repository.OpGT, rawValue
		case "gte":
			return field, repository.OpGTE, rawValue
		case "lt":
			return field, repository.OpLT, rawValue
		case "lte":
			return field, repository.OpLTE, rawValue
		case "ne":
			return field, repository.OpNEQ, rawValue
		case "in":
			return field, repository.OpIn, parseCSVValue(rawValue)
		case "between":
			return field, repository.OpRange, parseCSVValue(rawValue)
		}
	}

	if idx := strings.LastIndex(key, ":"); idx > 0 {
		suffix := key[idx+1:]
		field = key[:idx]
		switch suffix {
		case "eq":
			return field, repository.OpEQ, rawValue
		case "like":
			s := fmt.Sprintf("%v", rawValue)
			if !strings.Contains(s, "%") {
				s = "%" + s + "%"
			}
			return field, repository.OpLike, s
		case "gt":
			return field, repository.OpGT, rawValue
		case "gte":
			return field, repository.OpGTE, rawValue
		case "lt":
			return field, repository.OpLT, rawValue
		case "lte":
			return field, repository.OpLTE, rawValue
		case "ne":
			return field, repository.OpNEQ, rawValue
		case "in":
			return field, repository.OpIn, parseCSVValue(rawValue)
		case "between":
			return field, repository.OpRange, parseCSVValue(rawValue)
		}
		// 非已知运算符 → 整个 key 当作字段名
	}

	// 2. 无后缀：自动推断 Op
	field = key
	if isSlice(rawValue) {
		return field, repository.OpIn, rawValue
	}
	return field, repository.OpEQ, rawValue
}

// parseCSVValue 将逗号分隔的字符串值转为 []any（用于 OpIn / OpRange）。
// 若 rawValue 已是切片则直接返回；否则按 fmt.Sprintf + strings.Split 解析。
func parseCSVValue(rawValue any) []any {
	// 已是切片 → 直接转 []any
	if rv := reflect.ValueOf(rawValue); rv.Kind() == reflect.Slice {
		result := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			result[i] = rv.Index(i).Interface()
		}
		return result
	}

	s := strings.TrimSpace(fmt.Sprintf("%v", rawValue))
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	result := make([]any, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
func (s *GenericService[M]) _afterList(ctx context.Context, list []M, total int64) ([]M, int64, error) {
	return list, total, nil
}

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
	currentCol := resolveColumn[M](vf.CurrentField)
	statusCol := resolveColumn[M](vf.StatusField)
	now := time.Now()
	userULID := GetUserULID(ctx)

	cr := s.CRUDRepo()
	return cr.Transaction(ctx, func(tx *gorm.DB) error {
		// 同 code 所有行退位
		if err := tx.Model(new(M)).Where(codeCol+" = ?", code).
			Update(currentCol, int8(0)).Error; err != nil {
			return err
		}

		updates := map[string]any{
			currentCol:   int8(1),
			"updated_at": now,
		}

		// 草稿 / 已废弃 → 正式发布
		if currentStatus == string(VersionStatusDraft) || currentStatus == string(VersionStatusDeprecated) {
			updates[statusCol] = string(VersionStatusPublished)
			if vf.PublishedAtField != "" {
				updates[resolveColumn[M](vf.PublishedAtField)] = now
			}
			if vf.PublishedByField != "" && userULID != "" {
				updates[resolveColumn[M](vf.PublishedByField)] = userULID
			}
		}

		return tx.Model(new(M)).Where(s.repo.PKField()+" = ?", entityULID).
			Updates(updates).Error
	})
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
		return nil, nil, errs.ErrInvalidParam
	}

	// 查当前实体（用于状态校验 & 备份旧值）
	_entity, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, errs.ErrRecordNotFound
		}
		return nil, nil, errs.ErrQueryRecordFailed(err)
	}

	// 校验状态迁移
	if newStatus, ok := patches[vf.StatusField]; ok {
		curStatus := getStrField(_entity, vf.StatusField)
		newStatusStr := fmt.Sprintf("%v", newStatus)
		if !s.isValidStatusTransition(curStatus, newStatusStr) {
			return nil, nil, errs.ErrInvalidVersionStatusTransition
		}
	}

	return id, &editVersionCtx[M]{Old: _entity, Patches: patches}, nil
}

// validVersionTransitions 版本状态迁移规则
var validVersionTransitions = map[string][]string{
	"draft":      {"abolished"},
	"deprecated": {"abolished"},
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

// ============================================================
// 工具函数
// ============================================================

// newRecord 创建一个新的零值 Record 实例。
// M 必须是 *Struct 指针类型（gocrux 约定），通过反射分配底层 struct。
// 替代直接 var m M（当 M 为指针类型时会产生 nil 指针，导致方法调用 panic）。
func newRecord[M Record]() M {
	var z M
	t := reflect.TypeOf(z)
	if t.Kind() == reflect.Ptr {
		return reflect.New(t.Elem()).Interface().(M)
	}
	return z
}

// extractIdemKey 从批量请求中提取首个有效幂等键。
// 返回空字符串表示未启用幂等。
func extractIdemKey[M Record](input []CrudRequest[M]) string {
	if len(input) == 0 {
		return ""
	}
	if idem, ok := input[0].(HasIdempotencyKey); ok {
		return idem.GetIdempotencyKey()
	}
	return ""
}

// nextVersionCode 计算下一个版本号: v1.0 → v1.1, v1 → v2
func nextVersionCode(currentCode string) string {
	if currentCode == "" {
		return "v1.0"
	}
	code := currentCode
	if len(code) > 0 && (code[0] == 'v' || code[0] == 'V') {
		code = code[1:]
	}
	parts := strings.Split(code, ".")
	if len(parts) == 0 {
		return "v1.0"
	}
	if len(parts) == 1 {
		n, err := parseInt(parts[0])
		if err != nil {
			return "v1.0"
		}
		return "v" + itoa(n+1)
	}
	major := parts[0]
	minor, err := parseInt(parts[len(parts)-1])
	if err != nil {
		minor = 0
	}
	return "v" + major + "." + itoa(minor+1)
}

// parseInt 字符串转 int，失败返回 0
func parseInt(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n, nil
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// itoa int → string
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	r := ""
	for n > 0 {
		r = string(rune('0'+n%10)) + r
		n /= 10
	}
	return r
}

// getStrField 反射读取实体指定字段的 string 值
func getStrField(_entity any, fieldName string) string {
	v := reflect.ValueOf(_entity)
	for v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return ""
	}
	f := v.FieldByName(fieldName)
	if !f.IsValid() {
		return ""
	}
	return f.String()
}

// getFieldVal 反射读取实体指定字段的原始值（保持类型，用于 DB 精确匹配）
func getFieldVal(_entity any, fieldName string) any {
	v := reflect.ValueOf(_entity)
	for v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}
	f := v.FieldByName(fieldName)
	if !f.IsValid() {
		return nil
	}
	return f.Interface()
}

// resolveColumn 根据 Go 结构体字段名 → GORM column 名称
// 遍历 M 的字段，匹配 fieldName，从 gorm tag 提取 column。
func resolveColumn[M Record](fieldName string) string {
	var m M
	t := reflect.TypeOf(m)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	f, ok := t.FieldByName(fieldName)
	if !ok {
		return toCamelSnake(fieldName)
	}
	gormTag := f.Tag.Get("gorm")
	if col := extractGormColumn(gormTag); col != "" {
		return col
	}
	return toCamelSnake(fieldName)
}

// extractGormColumn 从 gorm tag 中提取 column 值
func extractGormColumn(tag string) string {
	for _, part := range strings.Split(tag, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "column:") {
			return strings.TrimPrefix(part, "column:")
		}
	}
	return ""
}

// toCamelSnake 驼峰转下划线 fallback（如 "SiteCode" → "site_code"）
func toCamelSnake(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r + 32)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// popIntParam 从 map 中取出并删除指定的键，将值转为 int。
// 若键不存在或无法解析则返回 0。
func popIntParam(m map[string]any, key string) int {
	v, ok := m[key]
	if !ok {
		return 0
	}
	delete(m, key)

	s := fmt.Sprintf("%v", v)
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
	}
	n, _ := parseInt(s)
	return n
}

// popStrParam 从 map 中取出并删除指定的键，将值转为 string。
// 若键不存在返回空字符串。
func popStrParam(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	delete(m, key)
	return fmt.Sprintf("%v", v)
}

// isSlice 判断值是否为切片/数组，用于自动从 OpEQ 切换为 OpIn。
func isSlice(v any) bool {
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array
}
