package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Huey1979/gocrux/internal/model/entity"
	"github.com/Huey1979/gocrux/repository"

	errs "github.com/Huey1979/gocrux/errors"
	"github.com/sirupsen/logrus"
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

// CtxKeyUserULID context key — 当前用户 ULID（由 auth middleware 注入）。
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

// CtxKeyExplicitColumns context key — 本次写入请求中"显式出现的字段"列名白名单（BUG-045）。
// 由 Create/Update 主流程在 before 之前写入，_doCreate/_doUpdate 读取后传给
// repo.InsertBatch / tx.Create().Select(...)，使显式零值字段（0/false/""）真实落库。
const CtxKeyExplicitColumns ctxKey = "explicit_columns"

// CtxKeyResolveMode context key — 引用解析模式（BUG-070）。
//
// 由 Handler 层在 References / ChildRefs 展开（DoResolve）时注入：
// 该模式按主键集合解析「引用锚点」，_doList **不追加**「当前有效」默认过滤
// （软删 / is_current / 版本可见性），使已软删或历史版本的引用目标仍能被解析出来，
// 并保留 is_deleted / version_status 等状态字段供调用方判断。
//
// 向下级联（Cascades，父表拥有的子集合）**不注入**此标记，继续按当前有效过滤 ——
// 订单删掉一条明细后不应再出现在订单详情里，这是期望行为。
const CtxKeyResolveMode ctxKey = "resolve_mode"

// WithResolveMode 注入引用解析模式标记。
func WithResolveMode(ctx context.Context) context.Context {
	return context.WithValue(ctx, CtxKeyResolveMode, true)
}

// resolveModeFrom 判断当前是否处于引用解析模式。
func resolveModeFrom(ctx context.Context) bool {
	v, _ := ctx.Value(CtxKeyResolveMode).(bool)
	return v
}

// withExplicitColumns 将显式字段列名白名单写入 ctx。
func withExplicitColumns(ctx context.Context, cols []string) context.Context {
	return context.WithValue(ctx, CtxKeyExplicitColumns, cols)
}

// explicitColumnsFrom 从 ctx 提取显式字段列名白名单，无则返回 nil。
func explicitColumnsFrom(ctx context.Context) []string {
	if v := ctx.Value(CtxKeyExplicitColumns); v != nil {
		if cols, ok := v.([]string); ok {
			return cols
		}
	}
	return nil
}

// ============================================================
// ctx key — 请求 ID（由 middleware 注入，关联日志文件）
// ============================================================

// CtxKeyRequestID context key — 请求 ID（由 middleware 注入，串联日志）。
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
	UniqueFields [][]string

	// DeletedField 软删除标记字段的 DB 列名（默认 "is_deleted"）。
	// _doList 自动追加 DeletedField = DeletedValue 过滤。
	DeletedField string

	// DeletedValue 未删除时的字段值（默认 int8(0)）。配合 DeletedField 使用。
	DeletedValue any
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

// ============================================================
// 软删除判定（BUG-069）
//
// 仓储层按主键的读写（GetByID / UpdateByID / Save …）不追加软删条件，
// 且 heims 实体用自维护的 IsDeleted 列而非 gorm.DeletedAt，GORM 不会自动过滤。
//
// 收口策略（读写不对称，有意如此）：
//   - 写路径（Update / BatchUpdateByIDs）：已删记录一律拒绝，返回 ErrRecordNotFound。
//     理由：这是数据完整性问题（静默改写、版本化"复活"），不是权限问题。
//     需要修改已删记录时，先 Restore 再 Update。
//   - 读路径（Get）：**不做过滤**，记录照常返回（含 is_deleted 标记）。
//     理由：是否允许查看已删数据属业务权限语义，应由应用端在 AfterGet 钩子中
//     按 IsSoftDeleted() 自行判定（无权则返回 403 / 置空），框架不越俎代庖
//     ——否则回收站、查看详情、恢复等场景会被框架彻底堵死。
// ============================================================

// DeletedColumn 返回软删判定配置：(DB 列名, 未删除时的值, 是否启用软删)。
// 实体 SetDelete() 返回 false（无软删列）时 ok=false —— 调用方不得拼接过滤条件，
// 否则无该列的表会报未知列。
//
// 导出供应用端在 AfterGet 等钩子中自行判定（BUG-069）。
func (s *GenericService[M]) DeletedColumn() (field string, liveVal any, ok bool) {
	m := newRecord[M]()
	if !m.SetDelete() {
		return "", nil, false
	}
	field = s.config.DeletedField
	if field == "" {
		field = "is_deleted"
	}
	liveVal = s.config.DeletedValue
	if liveVal == nil {
		liveVal = int8(0)
	}
	return field, liveVal, true
}

// softDeleteFilter 构造「未删除」过滤条件，供 ListByFilters 类查询复用。
func (s *GenericService[M]) softDeleteFilter() (repository.Filter, bool) {
	field, val, ok := s.DeletedColumn()
	if !ok {
		return repository.Filter{}, false
	}
	return repository.Filter{Field: field, Op: repository.OpEQ, Value: val}, true
}

// IsSoftDeleted 判定记录是否已被软删除。
// 取不到软删字段（实体无该列）时一律返回 false，避免误伤不支持软删的实体。
//
// 导出供应用端在 AfterGet 钩子中决定是否向当前调用方暴露已删数据（BUG-069），
// 典型用法：
//
//	func (s *XxxService) afterGet(ctx context.Context, m *Entity) (*Entity, error) {
//	    if s.IsSoftDeleted(m) && !hasRecycleBinPerm(ctx) {   // 仅恢复/回收站权限可见
//	        return m, nil                                     // 或置空敏感字段 / 返回 ErrRecordNotFound
//	    }
//	    return m, nil
//	}
func (s *GenericService[M]) IsSoftDeleted(m *M) bool {
	if m == nil {
		return false
	}
	col, liveVal, ok := s.DeletedColumn()
	if !ok {
		return false
	}
	goField := resolveColumnFromDB[M](col)
	if goField == "" {
		// fail-open 告警（BUG-069 复核 P3）：配了软删列却解析不到实体 Go 字段
		// （典型成因：字段被挪进匿名嵌入 struct，而 resolveColumnFromDB 不递归展开），
		// 此处若静默返回 false，写守卫会整体失效且零报错 —— 必须留下痕迹。
		logrus.Warnf("[gocrux] 软删列 %q 解析不到实体字段，IsSoftDeleted 恒返回 false（已删记录将不被拦截）", col)
		return false
	}
	cur := getFieldVal(*m, goField)
	if cur == nil {
		logrus.Warnf("[gocrux] 软删列 %q（字段 %s）取值为 nil，IsSoftDeleted 恒返回 false", col, goField)
		return false
	}
	return !sameSoftDeleteValue(cur, liveVal)
}

// filterLiveIDs 从 id 列表中剔除已软删记录的 id；未启用软删时原样返回、不产生额外查询。
// 用于按 id 集合的批量写路径（BatchUpdateByIDs），避免已删记录被静默改写。
func (s *GenericService[M]) filterLiveIDs(ctx context.Context, ids []any) ([]any, error) {
	f, ok := s.softDeleteFilter()
	if !ok {
		return ids, nil
	}
	pkCol := s.repo.PKField()
	pkGo := resolveColumnFromDB[M](pkCol)
	if pkGo == "" {
		return ids, nil // 反查不到主键字段 → 不擅自过滤
	}
	live, _, err := s.repo.ListByFilters(ctx, repository.ListFilters{
		Filters: []repository.Filter{
			{Field: pkCol, Op: repository.OpIn, Value: ids},
			f,
		},
		Page:     1,
		PageSize: 0, // 全量（不分页），与 ListFilters 契约一致
	})
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(live))
	for i := range live {
		if v := getFieldVal(&live[i], pkGo); v != nil {
			out = append(out, v)
		}
	}
	return out, nil
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
	// BUG-045：收集所有请求显式字段列名 → Create 时 Select 白名单，显式零值真实落库
	if cols := collectAllExplicitColumns[M](input); len(cols) > 0 {
		ctx = withExplicitColumns(ctx, cols)
	}

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

// Update 更新单条记录（版本化模式下创建新版本行，非原地修改）。
// id 为记录主键，data 为 map[string]any 或 CrudRequest[M]。
func (s *GenericService[M]) Update(ctx context.Context, id, data any) (*M, error) {
	// BUG-045：版本化 Update 插入新行，请求显式字段列名 → Select 白名单，显式零值真实落库
	// （非版本化走 GORM Save 全字段更新，无零值丢失问题，无需处理）
	if s.config.VersionMode {
		if req, ok := data.(CrudRequest[M]); ok {
			if cols := collectExplicitColumns[M](req); len(cols) > 0 {
				ctx = withExplicitColumns(ctx, cols)
			}
		}
	}

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

// BatchUpdateByIDs 简单批量更新：按 ID 列表统一设置相同字段值。
// SQL: UPDATE table SET ... WHERE pk IN (id1, id2, ...)
// 不做级联更新、不做版本管理；版本化模式直接返回错误。
// updates 为待更新的字段 map（仅 DB 列名，已由 Handler 层剥离控制参数）。
// BUG-052：ids 为空（含 BeforeBatchUpdate hook 过滤后为空）时无操作返回 nil，
// 不报「缺少必需参数」——请求层面缺 ids 已由 Handler 层拦截。
func (s *GenericService[M]) BatchUpdateByIDs(ctx context.Context, ids []any, updates map[string]any) error {
	if s.config.VersionMode {
		return errs.ErrBatchUpdateSimpleNotSupportVersion
	}
	if len(ids) == 0 {
		// BUG-052：空 ids 视为无操作成功（返回 nil）。
		// Handler 层已在请求解析时拦截「请求缺 ids / 空数组」（ErrMissingParam），
		// 此处 ids 为空只可能来自 BeforeBatchUpdate hook 过滤——如 heims 权限过滤
		// 后全部记录被排除（notify-delivery 非接收人标记已读），应无操作返回成功，
		// 与 SQL `WHERE pk IN ()` 无操作语义一致。
		return nil
	}
	if len(updates) == 0 {
		return errs.ErrMissingParam("updates")
	}

	// BUG-069：剔除已软删记录的 id —— 按主键的批量写无软删过滤时，
	// 已删记录会被静默改写（与单条 update 的收口保持一致）。
	liveIDs, err := s.filterLiveIDs(ctx, ids)
	if err != nil {
		return err
	}
	if len(liveIDs) == 0 {
		// 全部已删 → 无操作成功（与 BUG-052 空 ids 静默成功语义一致）
		return nil
	}
	ids = liveIDs

	// 补充审计字段
	s.fillAuditUpdates(ctx, updates)

	return s.repo.UpdateByIDs(ctx, ids, updates)
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

// Restore 批量恢复已软删记录（BUG-069 配套能力）。
//
// 语义：仅把软删字段置回「未删值」（如 is_deleted=0），**不改动任何业务字段**。
// 「恢复」与「修改」严格分离：已删记录不可直接 Update（写路径已收口拒绝），
// 必须先 Restore 再 Update —— 这样版本化实体不会以已删旧行为底派生
// is_current=1 的新版本行（即"复活"），恢复动作本身也保持幂等可审计。
//
// 不支持软删的实体（SetDelete() 返回 false，删除走物理删 + 备份日志）
// 返回 ErrSoftDeleteNotSupported。
func (s *GenericService[M]) Restore(ctx context.Context, ids []any) error {
	if len(ids) == 0 {
		return errs.ErrMissingParam("ids")
	}
	// 版本化实体的「删除」= 废弃（只写 is_current=0 / version_status=deprecated，
	// 从不写 is_deleted），restore 对其恒为空操作却返回 200，极易被误用
	// （BUG-069 复核 P2）。恢复当前版本应走 Activate。
	if s.config.VersionMode {
		return errs.ErrUseActivateInstead
	}
	field, liveVal, ok := s.DeletedColumn()
	if !ok {
		return errs.ErrSoftDeleteNotSupported
	}

	updates := map[string]any{field: liveVal}
	s.fillAuditUpdates(ctx, updates)

	// 直接走仓储层：**不能**用 service 的 BatchUpdateByIDs —— 它会剔除已删 id
	// （BUG-069 写路径收口），用它恢复会把待恢复的记录自己过滤掉。
	if err := s.repo.UpdateByIDs(ctx, ids, updates); err != nil {
		return err
	}

	// 恢复是安全相关状态迁移，纳入操作日志（与 update / delete 对齐）
	if s.config.EnableOpLog && s.opLogRepo != nil {
		entries := make([]opLogEntry, 0, len(ids))
		for _, id := range ids {
			entries = append(entries, opLogEntry{EntityID: fmt.Sprint(id), Operation: "restore"})
		}
		s.batchWriteOpLog(ctx, entries)
	}
	return nil
}

// fillAuditUpdates 补全审计字段（updated_at / updated_by），供 Restore 与
// BatchUpdateByIDs 共用，避免两处各写一份。
//
// 列名沿用框架约定（`updated_at` / `updated_by`，由 gentity 生成，
// 与 SetUpdatedAt/SetUpdatedBy 维护的列一致）；自定义审计列名的实体需自行覆盖。
func (s *GenericService[M]) fillAuditUpdates(ctx context.Context, updates map[string]any) {
	updates["updated_at"] = time.Now()
	if uid := GetUserULID(ctx); uid != "" {
		updates["updated_by"] = uid
	}
}

// Get 按主键查询单条记录（不含展开，仅数据库查询）。
// 若需级联展开数据，使用 Handler 层的 DoGetByID。
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
		return nil, errs.ErrMissingParam("code")
	}
	return s._doGetByCode(ctx, code)
}

// ResolveCodesToIDs 将业务编码列表解析为主键 ID 列表。
//
// 适用于级联删除场景：当用户传 codes 而非 ids 时，
// 需查询所有匹配的记录（不限 is_current），提取主键值用于级联子表删除。
//
// 返回 []any 类型的 PK 值列表，即使部分 code 无匹配记录也尽可能返回结果。
func (s *GenericService[M]) ResolveCodesToIDs(ctx context.Context, codes []any) []any {
	if len(codes) == 0 {
		return nil
	}
	codeField := "code"
	if s.config.VersionFields != nil && s.config.VersionFields.CodeField != "" {
		codeField = resolveColumn[M](s.config.VersionFields.CodeField)
	}

	var ids []any
	pkField := s.repo.PKField()
	for _, code := range codes {
		results, _, err := s.repo.ListByFilters(ctx, repository.ListFilters{
			Filters:  []repository.Filter{{Field: codeField, Op: repository.OpEQ, Value: code}},
			Page:     1,
			PageSize: 100,
		})
		if err != nil {
			continue
		}
		for i := range results {
			id := getFieldVal(&results[i], pkField)
			if id != nil {
				ids = append(ids, id)
			}
		}
	}
	return ids
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

// List 分页查询记录列表。query 为 map[string]any 或 ListFilters。
// 自动追加软删除过滤（SetDelete()=true 时）和版本化过滤（VersionMode 时）。
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

// ListVersions 查询指定 code 的所有历史版本，按版本号降序排列。
// 仅版本化模式生效；id 和 code 至少传一个。
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
