package service

import (
	"context"
	"time"

	"github.com/Huey1979/gocrux/internal/model/entity"
	"github.com/Huey1979/gocrux/repository"

	errs "github.com/Huey1979/gocrux/errors"
)

// ============================================================
// Record — 实体约束
// M 必须满足此接口，后续按需补充方法。
// ============================================================

type Record interface {
	SetDefaults()
	SetCreatedAt(t time.Time)
	SetCreatedBy(userID string)
	SetUpdatedAt(t time.Time)
	SetUpdatedBy(userID string)
	SetID()

	// SupportsDraft 返回当前表是否支持草稿箱。
	// 返回 true 时实体需提供 VersionStatus 字段（通过 VersionFieldMapping 映射）。
	SupportsDraft() bool

	// SetDelete 尝试软删除当前记录，返回是否使用了软删除。
	//   - true:  实体有 is_deleted 字段，内部已设置 is_deleted=1（调用方负责持久化）
	//   - false: 实体不支持软删除（无 is_deleted 字段），应物理删除并写备份日志
	SetDelete() bool

	// PKField 返回当前实体的主键数据库列名（如 "site_ulid"、"dept_ulid"、"id" 等）。
	// 用于 Handler 层批量展开 References/ChildRefs/Cascades 时生成 WHERE 条件和
	// 构建 lookup map 索引键。
	PKField() string

	// SelfFKField 返回自关联的外键字段名（如 "parent_ulid"）。
	// 返回值非空字符串 → 说明该实体存在自关联（同一张表的外键引用自身）。
	// 返回值空字符串 → 该实体无自关联。
	//
	// 自关联展开的循环防护由两层机制保证：
	//  1. 深度控制（MaxExpandDepth + FieldDepthLimits + hardMaxExpandDepth=10）
	//  2. visited 追踪（expandGet 中记录 (handlerName, recordID) 链条，防环）
	SelfFKField() string
}

// ============================================================
// VersionStatus 版本状态常量
// ============================================================

// VersionStatus 版本状态
type VersionStatus string

const (
	VersionStatusDraft      VersionStatus = "draft"      // 草稿
	VersionStatusPublished  VersionStatus = "published"  // 正式发布
	VersionStatusDeprecated VersionStatus = "deprecated" // 已废弃（系统自动标记）
	VersionStatusAbolished  VersionStatus = "abolished"  // 彻底归档（人工标记，前端默认隐藏）
)

// ============================================================
// ctx key — 从 context 取用户信息
// ============================================================

type ctxKey string

const CtxKeyUserULID ctxKey = "user_ulid"

// GetUserULID 从 ctx 提取用户 ULID，可能为空
func GetUserULID(ctx context.Context) string {
	if v := ctx.Value(CtxKeyUserULID); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// ============================================================
// ctx key — 请求 ID（由 middleware 注入，关联日志文件）
// ============================================================

const CtxKeyRequestID ctxKey = "log_id"

// GetRequestID 从 ctx 提取请求 ID
func GetRequestID(ctx context.Context) string {
	if v := ctx.Value(CtxKeyRequestID); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// ============================================================
// 版本字段映射
// ============================================================

// VersionFieldMapping 版本字段映射（Go 结构体字段名，非 DB 列名）。
// 启用 VersionMode 时必须配置。
type VersionFieldMapping struct {
	ULIDField        string // ULID 字段（如 "SiteULID"）
	CodeField        string // 编码字段（如 "SiteCode"）
	VersionField     string // 版本号字段（如 "VersionCode"）
	CurrentField     string // 当前标记字段（如 "IsCurrent"）
	StatusField      string // 版本状态字段（如 "VersionStatus"）
	ParentField      string // 父版本字段（如 "ParentULID"）
	RemarkField      string // 版本说明字段（如 "VersionRemark"）
	PublishedAtField string // 发布时间字段（如 "PublishedAt"）
	PublishedByField string // 发布人字段（如 "PublishedBy"）
}

// ============================================================
// Config — Service 配置
// ============================================================

type Config[M Record] struct {
	// EnableUniqueValidation 是否启用内置校验（唯一性等）。
	// 默认 false；由子 Service 在初始化时按需开启。
	EnableUniqueValidation bool

	// EnableOpLog 是否自动记录操作日志（_afterXxx 自动写 sys_operation_log）
	EnableOpLog bool

	// EntityName 实体中文名，用于日志 EntityType 字段
	EntityName string

	// === 版本管理 ===
	// VersionMode 为 true 时，Update 不原地修改，而是：
	//   旧行标记 is_current=0 → 插入新行（is_current=1）。
	// 启用时必须同时配置 VersionFields。
	VersionMode   bool
	VersionFields *VersionFieldMapping

	//后面还有其他配置，按照实际需要的业务场景添加。

	// UniqueFields 需要唯一性检查的字段列表，注意联合唯一索引（如 [["mobile"],["dept_id", "role_id"]]表示mobile要唯一，dept_id+role_id也要唯一）。
	// 仅在 EnableUniqueValidation == true 时生效。
	DeletedField string

	DeletedValue any

	UniqueFields [][]string
}

// updatePair 版本化更新时，在 _beforeUpdate 与 _doUpdate 之间传递新旧实体。
// Old 为退位旧行，New 为待插入新行。
type updatePair[M Record] struct {
	Old *M
	New *M
}

// editVersionCtx 版本元数据修改时，在 before/do/after 之间传递上下文。
// Old 为修改前快照（用于备份日志文件），Patches 为待修改字段。
type editVersionCtx[M Record] struct {
	Old     *M
	Patches map[string]any
}

// ============================================================
// GenericService 泛型服务基类
// M — 实体类型，对应数据库中一条记录
// ============================================================

type GenericService[M Record] struct {
	hooks     Hooks[M]
	repo      repository.Repo[M]
	config    Config[M]
	opLogRepo *repository.CRUDRepository[entity.SysOperationLog]
	bakWriter BackupWriteFunc      // 备份日志写入器（非版本化 Update 写旧数据到文件）
	idemStore *IdempotencyStore[M] // 幂等缓存（可选，nil 时不启用幂等）
}

// BackupWriteFunc 备份日志写入函数签名
type BackupWriteFunc func(ctx context.Context, tableName string, recordID any, operation string, oldData any, requestID string) error

// NewGenericService 创建服务实例（向后兼容，接受 *CRUDRepository[M]）。
func NewGenericService[M Record](repo *repository.CRUDRepository[M], cfg Config[M]) *GenericService[M] {
	return &GenericService[M]{
		repo:   repo,
		config: cfg,
	}
}

// NewGenericServiceWithRepo 使用任意 Repo[M] 实现创建服务（用于 MongoDB 等）。
func NewGenericServiceWithRepo[M Record](repo repository.Repo[M], cfg Config[M]) *GenericService[M] {
	return &GenericService[M]{
		repo:   repo,
		config: cfg,
	}
}

// IsVersionMode 返回是否启用版本化模式。
func (s *GenericService[M]) IsVersionMode() bool {
	return s.config.VersionMode
}

// SetHooks 注入钩子族（通常由子 Service 在构造时调用）
func (s *GenericService[M]) SetHooks(h Hooks[M]) {
	s.hooks = h
}

// SetOpLogRepo 注入操作日志仓储（_afterXxx 自动写日志）
func (s *GenericService[M]) SetOpLogRepo(repo *repository.CRUDRepository[entity.SysOperationLog]) {
	s.opLogRepo = repo
}

// SetBakWriter 注入备份日志写入器（_afterUpdate 对非版本化实体写旧数据到文件）
func (s *GenericService[M]) SetBakWriter(w BackupWriteFunc) {
	s.bakWriter = w
}

// SetIdemStore 注入幂等缓存（可选）。
// 注入后 Create 会自动检查幂等键，相同 key 的重复请求直接返回缓存结果。
func (s *GenericService[M]) SetIdemStore(store *IdempotencyStore[M]) {
	s.idemStore = store
}

// Repo 暴露数据访问层给子 Service 和钩子使用
func (s *GenericService[M]) Repo() repository.Repo[M] {
	return s.repo
}

// CRUDRepo 返回 MySQL CRUDRepository（仅当 repo 是 MySQL 时有效，否则 nil）。
func (s *GenericService[M]) CRUDRepo() *repository.CRUDRepository[M] {
	if cr, ok := s.repo.(*repository.CRUDRepository[M]); ok {
		return cr
	}
	return nil
}

// SupportsVersion 返回当前 Service 是否启用了版本管理（VersionMode）。
// Handler 层据此决定是否注册 ListVersions / EditVersion 路由。
func (s *GenericService[M]) SupportsVersion() bool {
	return s.config.VersionMode
}

// ResolveToPublished 将记录列表解析为各自所属实体族的正式发布版本（version_status='published'）。
//
// 场景：级联查询时，FK 可能指向旧版本（实体更新后生成新 ULID），
// 需要返回当前正式发布的版本而非 FK 指向的旧版本。
//
// 注意：不是 is_current=1，而是 version_status=published。
// is_current 只表示「当前激活」，但正式发布版本才是业务侧应该看到的最新版本。
//
// 优化：编辑不是高频操作，FK 指向的记录多数情况已经是 published。
// 因此先遍历一轮，把已经是 published 的记录直接保留，
// 只对非 published 的记录按 code 去重后查 DB 找 published 版本。
//
// 仅在版本化模式下执行实际解析；非版本化模式直接返回原列表。
// 若某个 code 没有 published 版本（如仅存草稿或已废弃），则舍弃不返回。
func (s *GenericService[M]) ResolveToPublished(ctx context.Context, records []M) ([]M, error) {
	if !s.config.VersionMode || s.config.VersionFields == nil || len(records) == 0 {
		return records, nil
	}

	vf := s.config.VersionFields

	// 第一轮：已 published 的直接保留；未 published 的收集 code（去重）
	var result []M
	needResolve := make(map[string]struct{})
	for i := range records {
		status := getStrField(&records[i], vf.StatusField)
		if status == string(VersionStatusPublished) {
			result = append(result, records[i])
		} else {
			code := getStrField(&records[i], vf.CodeField)
			if code != "" {
				needResolve[code] = struct{}{}
			}
		}
	}

	// 全部已是 published → 无需二次查询
	if len(needResolve) == 0 {
		return result, nil
	}

	codes := make([]any, 0, len(needResolve))
	for c := range needResolve {
		codes = append(codes, c)
	}

	// 第二轮：仅查非 published 的 code 的正式发布版本
	resolved, _, err := s.repo.ListByFilters(ctx, repository.ListFilters{
		Filters: []repository.Filter{
			{Field: resolveColumn[M](vf.CodeField), Op: repository.OpIn, Value: codes},
			{Field: resolveColumn[M](vf.StatusField), Op: repository.OpEQ, Value: string(VersionStatusPublished)},
		},
		Page:     1,
		PageSize: 0,
	})
	if err != nil {
		return nil, err
	}

	result = append(result, resolved...)
	return result, nil
}

// ============================================================
// 暴露方法（before → do → after，error 短路，after 可修改返回值）
// ctx 从 Handler 传入，全链路透传
// ============================================================

func (s *GenericService[M]) Create(ctx context.Context, input []CrudRequest[M]) ([]*M, error) {
	// 幂等检查：提取首个有效幂等键，命中则直接返回缓存
	if key := extractIdemKey(input); key != "" && s.idemStore != nil {
		if cached, ok := s.idemStore.Get(key); ok {
			return cached, nil
		}
	}

	processed, err := s.beforeCreate(ctx, input)
	if err != nil {
		return nil, err
	}
	result, err := s.doCreate(ctx, processed)
	if err != nil {
		return nil, err
	}
	result, err = s.afterCreate(ctx, result)

	// 缓存创建结果
	if key := extractIdemKey(input); key != "" && s.idemStore != nil && err == nil {
		s.idemStore.Set(key, result)
	}

	return result, err
}

func (s *GenericService[M]) Update(ctx context.Context, id, data any) (*M, error) {
	pid, pdata, err := s.beforeUpdate(ctx, id, data)
	if err != nil {
		return nil, err
	}
	result, err := s.doUpdate(ctx, pid, pdata)
	if err != nil {
		return nil, err
	}
	return s.afterUpdate(ctx, pid, result, pdata)
}

// Delete 批量逻辑删除记录。
//   - ids:  []any 类型，记录 ULID 列表（必传；单个时由 Handler 包装为 [id]）
//   - codes: []any 类型，业务编码列表（可选；版本化时直接用于定位 code 族，跳过解析）
func (s *GenericService[M]) Delete(ctx context.Context, ids, codes any) error {
	pid, pdata, err := s.beforeDelete(ctx, ids, codes)
	if err != nil {
		return err
	}
	if err := s.doDelete(ctx, pid, pdata); err != nil {
		return err
	}
	return s.afterDelete(ctx, pid, pdata)
}

func (s *GenericService[M]) Get(ctx context.Context, id any) (*M, error) {
	pid, err := s.beforeGet(ctx, id)
	if err != nil {
		return nil, err
	}
	result, err := s.doGet(ctx, pid)
	if err != nil {
		return nil, err
	}
	return s.afterGet(ctx, result)
}

// GetByCode 按业务编码查询实体的正式发布版本（version_status='published'）。
//
// 仅版本化模式生效；非版本化模式退化为 repo.GetByField(ctx, "code", code)。
// code 不能为空。
func (s *GenericService[M]) GetByCode(ctx context.Context, code string) (*M, error) {
	if code == "" {
		return nil, errs.ErrInvalidParam
	}
	return s._doGetByCode(ctx, code)
}

// ResolveOneToPublished 将单条记录解析为所属实体族的正式发布版本。
//
// 场景：HTTP Get?id=xxx&follow_published=true 时，前端拿到 ULID 对应的记录后，
// 需要进一步确认该记录族的正式发布版本（可能不同 ULID）。
//
// 内部委托 ResolveToPublished（批量版），返回第一条（最多一条）。
// 若该 code 族无 published 版本，返回 ErrRecordNotFound。
func (s *GenericService[M]) ResolveOneToPublished(ctx context.Context, record *M) (*M, error) {
	records, err := s.ResolveToPublished(ctx, []M{*record})
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, errs.ErrRecordNotFound
	}
	return &records[0], nil
}

func (s *GenericService[M]) List(ctx context.Context, query any) ([]M, int64, error) {
	pq, err := s.beforeList(ctx, query)
	if err != nil {
		return nil, 0, err
	}
	list, total, err := s.doList(ctx, pq)
	if err != nil {
		return nil, 0, err
	}
	return s.afterList(ctx, list, total)
}

// Activate 激活版本（发布 / 回滚）。
//
//   - id: 目标版本 ULID。
//
// 操作：将目标版本置为 is_current=1，原当前版本退位 is_current=0。
// 发布与回滚在数据库层面一致，差异仅体现在 before 阶段的校验逻辑。
func (s *GenericService[M]) Activate(ctx context.Context, id any) error {
	pid, err := s.beforeActivate(ctx, id)
	if err != nil {
		return err
	}
	if err := s.doActivate(ctx, pid); err != nil {
		return err
	}
	return s.afterActivate(ctx, pid)
}

func (s *GenericService[M]) ListVersions(ctx context.Context, id any, code string) ([]M, error) {
	pid, err := s.beforeListVersions(ctx, id, code)
	if err != nil {
		return nil, err
	}
	result, err := s.doListVersions(ctx, pid)
	if err != nil {
		return nil, err
	}
	return s.afterListVersions(ctx, result)
}

// EditVersion 修改版本元数据（状态、备注），不创建新版本行。
//
//   - id:     版本 ULID。
//   - patches: 要修改的字段，key 为 Go 结构体字段名（如 "VersionStatus"、"VersionRemark"）。
//
// 状态迁移限制：
//   - draft / deprecated → abolished
//   - abolished → draft
//   - published 禁止直接 abolished（必须先 Activate 触发 deprecated）
func (s *GenericService[M]) EditVersion(ctx context.Context, id any, patches map[string]any) (*M, error) {
	pid, pdata, err := s.beforeEditVersion(ctx, id, patches)
	if err != nil {
		return nil, err
	}
	result, err := s.doEditVersion(ctx, pid, pdata)
	if err != nil {
		return nil, err
	}
	return s.afterEditVersion(ctx, pid, result, pdata)
}
