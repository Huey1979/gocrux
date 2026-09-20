package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Huey1979/gocrux/common"
	errs "github.com/Huey1979/gocrux/errors"
	"github.com/Huey1979/gocrux/internal/database/mongodb"

	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
	"gorm.io/gorm"
)

// ============================================================
// 发布痕迹（publish trace）
// REQ: edit-version 把 version_status 改成 published 时，框架应同样留发布痕迹
//
// 背景：同一事实「一个版本变成线上生效版本」在框架内共有 4 条路径：
//
//	create       带 version_status=published（保存并发布）
//	update       带 version_status=published
//	activate     草稿 / 已废弃 → published     ← 原本只有这一条由框架留痕
//	edit-version deprecated → published        ← 原本完全不留痕，且应用补不了
//
// 第 4 条应用补不了的原因是钩子签名丢了判定输入：AfterEditVersion
// （service/hooks.go:52、handler/hooks.go:73）只给新值，拿不到旧状态，
// 无法区分「只改了版本备注」与「把 deprecated 复活成了 published」。
//
// 本文件把留痕收口到框架（方案 A），并同时提供统一的「已发布」回调（方案 C）：
//
//   - 基础列：写 VersionFields.PublishedAtField / PublishedByField；
//   - 发布历史：一条含 via 来源标记的记录，默认落 MongoDB `publish_history`
//     （与「业务数据统一放 MongoDB」口径一致），可用 SetPublishHistoryWriter 换实现；
//   - 统一回调：Config.OnPublished，四条路径各触发**恰好一次**。
//
// 口径（heims 拍板）：「复活一个已废弃版本」**算**一次需要留痕的新发布 ——
// 版本废弃是正常生命周期操作，把它重新挂上线是主动选择，等同于重新发布。
// 反向则不留痕：published → deprecated（下线）不写 published_at、不产生发布历史。
// ============================================================

// PublishVia 发布来源（「一个版本变成线上生效版本」的达成路径）。
//
// 刻意定义为**具名类型**而非 string 别名：它出现在公开回调签名里，
// 具名类型能让编译器拦住「随手传个字符串」的误用，同时保留字面值可读性
// （比较时需显式 PublishVia("create") 或直接 PublishVia 常量）。
type PublishVia string

const (
	// PublishViaCreate create 携带 version_status=published（保存并发布）。
	PublishViaCreate PublishVia = "create"
	// PublishViaUpdate update 携带 version_status=published。
	PublishViaUpdate PublishVia = "update"
	// PublishViaActivate activate 接口（草稿 / 已废弃 → 发布、回滚）。
	PublishViaActivate PublishVia = "activate"
	// PublishViaEditVersion edit-version 接口把状态改为 published（复活已废弃版本）。
	PublishViaEditVersion PublishVia = "edit-version"
)

// PublishHistoryCollection 发布历史集合名（内置 MongoDB 实现使用）。
//
// 独立常量而非塞进 mongodb.Collection 结构体：不改动 internal 包的既有契约。
const PublishHistoryCollection = "publish_history"

// IsValidPublishVia 判断来源标记是否为框架认可的取值（供自定义写入方与测试复用）。
func IsValidPublishVia(via PublishVia) bool {
	switch via {
	case PublishViaCreate, PublishViaUpdate, PublishViaActivate, PublishViaEditVersion:
		return true
	}
	return false
}

// PublishHistoryRecord 一条发布历史（框架 → 写入方的数据传输对象）。
//
// 字段与 MongoDB `publish_history` 集合文档一一对应（bson tag 均为 snake_case，
// 遵循 BUG-053/061 的 bson tag 约定：读路径统一取逗号前段）。
type PublishHistoryRecord struct {
	HistoryULID  string `bson:"history_ulid" json:"history_ulid"`   // 主键（ULID）
	EntityType   string `bson:"entity_type" json:"entity_type"`     // Config.EntityName
	EntityID     string `bson:"entity_id" json:"entity_id"`         // 被发布的版本主键
	EntityCode   string `bson:"entity_code" json:"entity_code"`     // 版本族业务编码
	VersionCode  string `bson:"version_code" json:"version_code"`   // 版本号
	Via          string `bson:"via" json:"via"`                     // create/update/activate/edit-version
	OperatorULID string `bson:"operator_ulid" json:"operator_ulid"` // 操作人 ULID
	RequestID    string `bson:"request_id" json:"request_id"`       // 请求追踪 ID
	PublishedAt  int64  `bson:"published_at" json:"published_at"`   // 发布时间（Unix 秒）
}

// OnPublishedFunc 统一的「版本已进入 published」回调（方案 C）。
//
// 四条路径各触发恰好一次，via 标识来源。应用只需挂这一处即可覆盖全部发布路径
// （写业务日志表、发通知等），不必在每模块各写一份 onXxxPublished。
//
// 注意：回调在主业务流程中**同步**执行；panic 会被框架恢复，返回 error 只记日志，
// 均不影响发布结果 —— 发布痕迹属尽力而为的附加动作。
//
// 实现约束：Go 不允许把同签名的函数字面量直接赋给**具名泛型类型**
// （需显式类型转换，对调用方是纯语法噪音），而泛型类型别名又要求 go1.23+
// （本模块 go.mod 为 go1.20）。故此处不声明具名类型，
// Config.OnPublished 与 SetOnPublished 直接使用展开形态的函数类型：
//
//	func(ctx context.Context, id any, entity *M, via PublishVia) error
//
// 用法（模块外可直接编译）：
//
//	svc.SetOnPublished(func(ctx context.Context, id any, e *MyEntity, via service.PublishVia) error {
//	    return writePublishLog(ctx, id, via)
//	})

// PublishHistoryWriter 发布历史写入方抽象。
// 实现方需自行保证并发安全；框架按「尽力而为」调用，失败只记日志。
type PublishHistoryWriter interface {
	// WritePublishHistories 批量写入发布历史。len(records)==0 时应直接返回 nil。
	WritePublishHistories(ctx context.Context, records []PublishHistoryRecord) error
}

// ============================================================
// 内置 MongoDB 实现
// ============================================================

// mongoPublishHistoryWriter 内置实现：落 MongoDB `publish_history` 集合。
type mongoPublishHistoryWriter struct{}

// WritePublishHistories 实现 PublishHistoryWriter。
//
// 通过 common.GetMongoSession(ctx) 感知事务：MySQL 事务不跨库到 MongoDB，
// 但 activate / 版本化 update 的调用点已用 common.WithMongoSession 把同一 session
// 注入 ctx，写入会挂到该 session 上 —— 主业务回滚时发布历史一并回滚，
// 不产生「有发布记录但没有发布」的幽灵痕迹。
func (w *mongoPublishHistoryWriter) WritePublishHistories(ctx context.Context, records []PublishHistoryRecord) error {
	if len(records) == 0 {
		return nil
	}
	if mongodb.Database == nil {
		return nil // 未初始化 MongoDB：静默跳过（不阻塞主业务，也不刷日志）
	}
	coll := mongodb.Database.Collection(PublishHistoryCollection)
	if sess := common.GetMongoSession(ctx); sess != nil {
		coll = sess.Client().Database(mongodb.Database.Name()).Collection(PublishHistoryCollection)
	}
	docs := make([]any, 0, len(records))
	for _, r := range records {
		docs = append(docs, bson.M{
			"history_ulid":  r.HistoryULID,
			"entity_type":   r.EntityType,
			"entity_id":     r.EntityID,
			"entity_code":   r.EntityCode,
			"version_code":  r.VersionCode,
			"via":           r.Via,
			"operator_ulid": r.OperatorULID,
			"request_id":    r.RequestID,
			"published_at":  r.PublishedAt,
		})
	}
	_, err := coll.InsertMany(ctx, docs)
	return err
}

// NoopPublishHistoryWriter 空实现：显式关闭发布历史落库。
// 应用只想要 published_at / published_by 两个基础列时传它即可。
type NoopPublishHistoryWriter struct{}

// WritePublishHistories 实现 PublishHistoryWriter（恒返回 nil）。
func (NoopPublishHistoryWriter) WritePublishHistories(context.Context, []PublishHistoryRecord) error {
	return nil
}

// SetPublishHistoryWriter 注入自定义发布历史写入方。
// 传 nil 表示恢复内置 MongoDB 实现（默认行为，无需调用）。
func (s *GenericService[M]) SetPublishHistoryWriter(w PublishHistoryWriter) {
	s.publishHistoryWriter = w
}

// SetOnPublished 注入统一的「已发布」回调（方案 C）。
// 四条进入 published 的路径各触发恰好一次。
// 传 nil 表示移除回调。
func (s *GenericService[M]) SetOnPublished(fn func(ctx context.Context, id any, entity *M, via PublishVia) error) {
	s.config.OnPublished = fn
}

// ============================================================
// 判定
// ============================================================

// statusBecomesPublished 判定「本次变更的终点是否为 published 且旧值不是 published」。
//
//   - 旧值为空（如 create 时的新实体）视为「不是 published」；
//   - 终点并非 published（如 published → deprecated 下线）返回 false，不产生发布痕迹；
//   - 状态字段未配置时返回 false（无法判定，交由应用自理）。
func (s *GenericService[M]) statusBecomesPublished(oldStatus, newStatus string) bool {
	if !s.config.VersionMode || s.config.VersionFields == nil {
		return false
	}
	if s.config.VersionFields.StatusField == "" {
		return false
	}
	if newStatus != string(VersionStatusPublished) {
		return false
	}
	return oldStatus != string(VersionStatusPublished)
}

// versionTraceColumns 从实体取值，构造发布历史所需的版本元信息。
func (s *GenericService[M]) versionTraceColumns(entity *M) (entityCode, versionCode string) {
	vf := s.config.VersionFields
	if vf == nil || entity == nil {
		return "", ""
	}
	if vf.CodeField != "" {
		entityCode = getStrField(entity, vf.CodeField)
	}
	if vf.VersionField != "" {
		versionCode = getStrField(entity, vf.VersionField)
	}
	return entityCode, versionCode
}

// buildPublishHistory 构造一条发布历史记录。
func (s *GenericService[M]) buildPublishHistory(ctx context.Context, entity *M, via PublishVia) PublishHistoryRecord {
	entityCode, versionCode := s.versionTraceColumns(entity)
	return PublishHistoryRecord{
		HistoryULID:  common.NewULID(),
		EntityType:   s.config.EntityName,
		EntityID:     fmt.Sprint(extractEntityID(entity)),
		EntityCode:   entityCode,
		VersionCode:  versionCode,
		Via:          string(via),
		OperatorULID: GetUserULID(ctx),
		RequestID:    GetRequestID(ctx),
		PublishedAt:  time.Now().Unix(),
	}
}

// publishedAtColumn / publishedByColumn 返回发布基础列的存储列名（未配置则空）。
func (s *GenericService[M]) publishedAtColumn() string {
	vf := s.config.VersionFields
	if vf == nil || vf.PublishedAtField == "" {
		return ""
	}
	return resolveColumn[M](vf.PublishedAtField)
}

func (s *GenericService[M]) publishedByColumn() string {
	vf := s.config.VersionFields
	if vf == nil || vf.PublishedByField == "" {
		return ""
	}
	return resolveColumn[M](vf.PublishedByField)
}

// applyPublishTraceFields 在实体上写发布基础列（published_at / published_by）。
//
// 两者都未配置时返回 ErrPublishTraceNotSupported —— 此时连「发布时间」都无处安放，
// 调用方按尽力而为处理：记 Warn 日志 + 跳过发布历史，避免出现
// 「有历史记录但两个基础列永远为空」的语义分裂。
//
// 操作人 ulid 取不到（未登录 / 非 HTTP 调用）时只写发布时间，不回退为匿名值。
func (s *GenericService[M]) applyPublishTraceFields(entity *M, now time.Time, userULID string) error {
	if entity == nil {
		return nil
	}
	vf := s.config.VersionFields
	if vf == nil {
		return errs.ErrVersionFieldsNotSet
	}
	if vf.PublishedAtField == "" && vf.PublishedByField == "" {
		return errs.ErrPublishTraceNotSupported
	}
	if vf.PublishedAtField != "" {
		common.SetFieldValue(entity, vf.PublishedAtField, &now)
	}
	if vf.PublishedByField != "" && userULID != "" {
		common.SetFieldValue(entity, vf.PublishedByField, userULID)
	}
	return nil
}

// ============================================================
// 写入口
// ============================================================

// recordPublished 统一的发布痕迹写入口（方案 A + C），在 **事务之外**调用。
//
// 职责：触发 OnPublished 回调 + 写发布历史。
// 基础列（published_at / published_by）由各调用点直接写在实体或 UPDATE 列上 ——
// 它们的落库方式因路径而异（Save / InsertBatch / UpdateByID / 事务内 UPDATE）。
//
// 尽力而为：任何失败只记日志，绝不冒泡影响发布结果。
func (s *GenericService[M]) recordPublished(ctx context.Context, entity *M, via PublishVia) {
	s.emitOnPublished(ctx, entity, via)
	s.writePublishHistory(ctx, entity, via)
}

// emitOnPublished 触发统一回调；panic 被恢复（附加动作不得击穿主流程）。
func (s *GenericService[M]) emitOnPublished(ctx context.Context, entity *M, via PublishVia) {
	if s.config.OnPublished == nil || entity == nil {
		return
	}
	id := extractEntityID(entity)
	func() {
		defer func() {
			if r := recover(); r != nil {
				logrus.Errorf("gocrux: OnPublished 回调 panic（entity_type=%s id=%v via=%s）: %v",
					s.config.EntityName, id, via, r)
			}
		}()
		if err := s.config.OnPublished(ctx, id, entity, via); err != nil {
			logrus.Errorf("gocrux: OnPublished 回调失败（entity_type=%s id=%v via=%s）: %v",
				s.config.EntityName, id, via, err)
		}
	}()
}

// writePublishHistory 写一条发布历史；失败只记日志。
func (s *GenericService[M]) writePublishHistory(ctx context.Context, entity *M, via PublishVia) {
	if entity == nil {
		return
	}
	w := s.publishHistoryWriterOr()
	if w == nil {
		return
	}
	rec := s.buildPublishHistory(ctx, entity, via)
	if err := w.WritePublishHistories(ctx, []PublishHistoryRecord{rec}); err != nil {
		logrus.Errorf("gocrux: 写发布历史失败（entity_type=%s id=%v via=%s）: %v",
			s.config.EntityName, rec.EntityID, via, err)
	}
}

// publishHistoryWriterOr 返回生效的发布历史写入方（未注入则内置 MongoDB 实现）。
func (s *GenericService[M]) publishHistoryWriterOr() PublishHistoryWriter {
	if s.publishHistoryWriter != nil {
		return s.publishHistoryWriter
	}
	return &mongoPublishHistoryWriter{}
}

// recordPublishedTx 在 **MySQL 事务内**写入发布痕迹（activate / 版本化 update 走这里）。
//
// 与 recordPublished 的差别只有落库时机：发布历史挂到事务 AfterCommit 之后写
// （MySQL 事务不跨库到 MongoDB，若立即写会出现「主业务回滚但发布历史已落库」的
// 幽灵痕迹）。Mongo 侧通过 common.WithMongoSession 复用同一 session。
//
// fired 用于向调用方回传「回调已执行」的事实：回调是外部副作用、无法随事务撤回，
// 若最终回滚被调用方可据此记为已知不一致（见各调用点的 rollback 分支）。
func (s *GenericService[M]) recordPublishedTx(
	ctx context.Context, tx *gorm.DB, entity *M, via PublishVia, fired *bool,
) {
	s.emitOnPublished(ctx, entity, via)
	if fired != nil {
		*fired = true
	}

	w := s.publishHistoryWriterOr()
	if w == nil || entity == nil {
		return
	}
	rec := s.buildPublishHistory(ctx, entity, via)
	writePublishHistoryAfterCommit(ctx, tx, w, rec)
}

// writePublishHistoryAfterCommit 把发布历史写入挂到 gorm 事务的提交之后。
//
// GORM 没有公开的 commit 回调注册点，因此在**事务内**用 `tx.Statement.ConnPool`
// 注册一次 `gorm:after_commit` 前置钩子（该 ConnPool 即 *sql.Tx）：
// 事务成功提交时钩子会被调用，回滚时不会被调用 —— 不会留下
// 「主业务回滚但发布历史已落库」的幽灵痕迹。
//
// tx 为 nil（非事务场景）或注册失败时退化为立即写。失败只记日志。
func writePublishHistoryAfterCommit(
	ctx context.Context, tx *gorm.DB, w PublishHistoryWriter, rec PublishHistoryRecord,
) {
	write := func(commitCtx context.Context) {
		if err := w.WritePublishHistories(commitCtx, []PublishHistoryRecord{rec}); err != nil {
			logrus.Errorf("gocrux: 写发布历史失败（entity_type=%s id=%v via=%s）: %v",
				rec.EntityType, rec.EntityID, rec.Via, err)
		}
	}
	if tx == nil {
		write(ctx)
		return
	}
	// 事务内注册 after-commit 钩子：GORM 在 commit 成功后执行，回滚不执行。
	if err := tx.Callback().Create().After("gorm:commit_or_rollback_transaction").
		Register("gocrux:publish_history_after_commit", func(db *gorm.DB) {
			sess := common.GetMongoSession(db.Statement.Context)
			outCtx := ctx
			if sess != nil {
				outCtx = common.WithMongoSession(ctx, sess)
			}
			write(outCtx)
		}); err != nil {
		// 注册失败（重名等）：不阻塞主业务，退化为立即写
		logrus.Errorf("gocrux: 注册发布历史 AfterCommit 钩子失败（entity_type=%s id=%v）: %v",
			rec.EntityType, rec.EntityID, err)
		write(ctx)
	}
}

// reportPublishTraceRollback 事务回滚后，若 OnPublished 回调已经执行过，
// 明确记一条 Error：回调是外部副作用、无法随事务撤回，本次发布**没有落库**。
// 不静默 —— 这是应用排查「发布记录与实际状态不一致」时唯一的一手线索。
func (s *GenericService[M]) reportPublishTraceRollback(
	ctx context.Context, fired bool, via PublishVia, txErr error,
) {
	if !fired {
		return
	}
	logrus.Errorf(
		"gocrux: 发布事务已回滚，但 OnPublished 回调已执行且无法撤回"+
			"（entity_type=%s via=%s）——本次发布未落库，回调产生的副作用需应用自行判定: %v",
		s.config.EntityName, via, txErr,
	)
}

// warnPublishTraceUnavailable 发布痕迹基础列都未配置时的提示（一次性语义：
// 每条发布记录一条 Warn，便于定位到具体实体，不做全局去重以免掩盖配置漂移）。
func (s *GenericService[M]) warnPublishTraceUnavailable(err error) {
	if err == nil {
		return
	}
	logrus.Warnf(
		"gocrux: 实体 %q 无法写发布痕迹基础列（%v）；发布历史与 OnPublished 回调仍会触发。"+
			"如需 published_at / published_by 落库，请在 VersionFields 配置 PublishedAtField / PublishedByField",
		s.config.EntityName, err,
	)
}

// appendPublishTraceColumns 在 BUG-045 白名单非空时并入发布痕迹基础列。
//
// 为什么必须并入：PublishedAtField 通常是 *time.Time，而 collectNonZeroColumns
// 明确跳过指针/接口字段（零值为 nil，GORM 默认不插入）。于是「显式字段白名单」
// 生效的 create（请求带 code/name 等）会把 published_at 排除在 INSERT 之外 ——
// 内存里写了、库里是 NULL，正是 REQ 要消除的那类「发布无痕迹」。
//
// cols 为空（无显式字段，走 DB 默认值路径）时原样返回，不改变旧行为。
func (s *GenericService[M]) appendPublishTraceColumns(cols []string, entities []*M) []string {
	// 非版本化实体没有发布痕迹概念，直接放行（不改动旧行为）
	if len(cols) == 0 || !s.config.VersionMode || s.config.VersionFields == nil {
		return cols
	}
	statusField := s.config.VersionFields.StatusField
	if statusField == "" {
		return cols
	}
	add := func(col string) {
		if col == "" {
			return
		}
		for _, c := range cols {
			if c == col {
				return
			}
		}
		cols = append(cols, col)
	}
	atCol, byCol := s.publishedAtColumn(), s.publishedByColumn()
	for _, e := range entities {
		if e == nil {
			continue
		}
		if !s.statusBecomesPublished("", getStrField(e, statusField)) {
			continue
		}
		add(atCol)
		add(byCol)
	}
	return cols
}

// ============================================================
// 读取（供应用与测试查询发布历史）
// ============================================================

// PublishHistoryFilter 发布历史查询条件（零值表示不过滤）。
type PublishHistoryFilter struct {
	EntityType string
	EntityID   string
	EntityCode string
	Via        string
	// Limit 最大返回条数，<=0 时取 100。
	Limit int64
}

// ListPublishHistories 查询发布历史（仅内置 MongoDB 集合）。
//
// 应用自定义了写入方（SetPublishHistoryWriter）时，本方法读不到那些数据，
// 应改用自定义写入方对应的读取接口。未初始化 MongoDB 时返回空列表。
func ListPublishHistories(ctx context.Context, f PublishHistoryFilter) ([]PublishHistoryRecord, error) {
	if mongodb.Database == nil {
		return nil, nil
	}
	filter := bson.M{}
	if f.EntityType != "" {
		filter["entity_type"] = f.EntityType
	}
	if f.EntityID != "" {
		filter["entity_id"] = f.EntityID
	}
	if f.EntityCode != "" {
		filter["entity_code"] = f.EntityCode
	}
	if f.Via != "" {
		filter["via"] = string(f.Via)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	coll := mongodb.Database.Collection(PublishHistoryCollection)
	cur, err := coll.Find(ctx, filter, options.Find().SetLimit(limit).SetSort(bson.D{{Key: "published_at", Value: -1}}))
	if err != nil {
		return nil, err
	}
	defer func() { _ = cur.Close(ctx) }()
	var out []PublishHistoryRecord
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}
